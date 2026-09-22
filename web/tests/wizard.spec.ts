import { expect, test, type Page } from "@playwright/test";
import type { Project } from "../src/types";

function initRecord(id: string, status: string) {
  const needsInput = status === "needs_input";
  const ready = status === "ready";
  return {
    id: "init-" + id,
    projectId: id,
    status,
    stage: ready ? "complete" : "analyze",
    progress: ready ? 100 : needsInput ? 35 : 20,
    message: needsInput
      ? "分析需要补充信息后继续"
      : ready
        ? "Meta 仓库初始化完成：项目说明已保存。"
        : "初始化 Agent 正在分析仓库",
    repositoriesConnected: true,
    analysisComplete: ready,
    readyForTasks: ready,
    questions: needsInput
      ? [{ id: "q1", question: "请提供本地联调方式。", answered: false }]
      : [],
    reports: ready
      ? [{ path: "PROJECT.md", description: "项目说明", verified: true }]
      : [],
    verification: ready
      ? [
          {
            name: "unit",
            command: ["go", "test", "./..."],
            passed: true,
            exitCode: 0,
            report: "ok",
            dryRun: false,
          },
        ]
      : [],
    metaSha: "abc",
    repositoryShas: {},
    executionId: "",
    attempts: 1,
    version: 1,
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
  };
}

async function setupWizard(page: Page, options?: { verifyFails?: boolean }) {
  const verifyFails = options?.verifyFails ?? false;
  const saves: Project[] = [];
  let initId = "";
  let answered = false;
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const pathname = url.pathname;
    const method = request.method();
    if (method !== "GET")
      expect(request.headers()["x-requested-with"]).toBe("swarmjet");
    if (pathname === "/api/session")
      return route.fulfill({ json: { username: "admin" } });
    if (pathname === "/api/project-defaults")
      return route.fulfill({ json: { agentImage: "swarmjet-agent:configured" } });
    if (pathname === "/api/connections" && method === "GET")
      return route.fulfill({ json: [] });
    if (pathname === "/api/connections/verify")
      return route.fulfill({
        json: {
          ok: true,
          gitlabUrl: "https://gitlab.example",
          username: "operator",
          name: "Operator",
          fingerprint: "abc123def456",
          hasMergeToken: false,
        },
      });
    if (pathname === "/api/connections" && method === "POST")
      return route.fulfill({
        json: {
          id: "c1",
          name: "operator",
          gitlabUrl: "https://gitlab.example",
          username: "operator",
          version: 1,
          hasMergeToken: false,
          fingerprint: "abc123def456",
          createdAt: new Date().toISOString(),
          updatedAt: new Date().toISOString(),
        },
      });
    if (pathname === "/api/connections/c1/repositories") {
      const pageParam = Number(url.searchParams.get("page") || "1");
      const projects =
        pageParam === 1
          ? [
              {
                id: 101,
                name: "Core",
                path_with_namespace: "team/core",
                default_branch: "dev",
                http_url_to_repo: "https://gitlab.example/team/core.git",
                web_url: "",
                archived: false,
                description: "core library",
              },
              {
                id: 102,
                name: "API",
                path_with_namespace: "team/api",
                default_branch: "main",
                http_url_to_repo: "https://gitlab.example/team/api.git",
                web_url: "",
                archived: false,
                description: "",
              },
            ]
          : [
              {
                id: 103,
                name: "Web",
                path_with_namespace: "team/web",
                default_branch: "main",
                http_url_to_repo: "https://gitlab.example/team/web.git",
                web_url: "",
                archived: false,
                description: "",
              },
            ];
      return route.fulfill({
        json: {
          projects,
          page: pageParam,
          perPage: 50,
          hasNext: pageParam === 1,
          hasPrev: pageParam > 1,
        },
      });
    }
    if (pathname === "/api/runtime/status")
      return route.fulfill({
        json: {
          profile: "default",
          image: "agent",
          available: true,
          toolsChecked: true,
          opencode: {
            installed: true,
            configured: true,
            providers: ["example"],
          },
          codex: { installed: true, configured: false, executionSupported: false },
        },
      });
    if (pathname === "/api/runtime/models")
      return route.fulfill({
        json: {
          models: ["example/development", "example/review"],
          providers: ["example"],
          warnings: [],
        },
      });
    if (pathname === "/api/runtime/verify") {
      if (verifyFails)
        return route.fulfill({
          status: 502,
          json: { error: "模型调用失败，请确认登录有效且该模型可用。" },
        });
      return route.fulfill({
        json: {
          ok: true,
          model: "example/development",
          message: "最小实际调用成功。",
          durationSeconds: 1,
        },
      });
    }
    if (pathname === "/api/projects" && method === "GET")
      return route.fulfill({ json: saves.map((project) => ({ ...project, version: 1 })) });
    if (pathname === "/api/projects" && method === "POST") {
      const payload = request.postDataJSON() as Project;
      saves.push(payload);
      initId = payload.id;
      return route.fulfill({ json: { ...payload, version: 1 } });
    }
    if (initId && pathname === "/api/projects/" + initId + "/initialization") {
      if (method === "POST")
        return route.fulfill({ status: 202, json: initRecord(initId, "running") });
      return route.fulfill({
        json: initRecord(initId, answered ? "ready" : "needs_input"),
      });
    }
    if (initId && pathname === "/api/projects/" + initId + "/initialization/answers") {
      answered = true;
      return route.fulfill({ status: 202, json: initRecord(initId, "running") });
    }
    if (initId && pathname === "/api/projects/" + initId)
      return route.fulfill({
        json: { ...saves[saves.length - 1], id: initId, version: 1 },
      });
    return route.fulfill({ status: 404, json: { error: "not found" } });
  });
  return { saves };
}

async function advanceToOpenCode(page: Page) {
  await page.getByLabel("GitLab 地址").fill("https://gitlab.example");
  await page.getByLabel("Access Token").fill("glpat-secret-value");
  await page.getByRole("button", { name: "验证并继续" }).click();
  await expect(page.getByText("team/core", { exact: false })).toBeVisible();
  await page.getByRole("checkbox").first().check();
  await page.getByRole("button", { name: "下一步" }).click();
  await expect(page.getByRole("checkbox", { name: /自动合并/ })).not.toBeChecked();
  await page.getByRole("button", { name: "下一步：配置 OpenCode" }).click();
  await expect(page.getByText("检测到已有的 OpenCode 登录配置")).toBeVisible();
  await page.getByRole("button", { name: "检查并继续" }).click();
  await expect(page.locator("select")).toHaveValue("example/development");
}

test("wizard connects, paginates repos, verifies a model and completes initialization", async ({
  page,
}) => {
  const state = await setupWizard(page);
  await page.goto("/#/projects/new");
  await page.getByLabel("GitLab 地址").fill("https://gitlab.example");
  await page.getByLabel("Access Token").fill("glpat-secret-value");
  await page.getByRole("button", { name: "验证并继续" }).click();
  await expect(page.getByText("team/core", { exact: false })).toBeVisible();
  await page.getByText("Core", { exact: true }).click();
  await expect(page.getByLabel("选择仓库 team/core", { exact: true })).toBeChecked();
  await page.getByLabel("Core 的目标分支").fill("feature/test");
  await page.getByRole("button", { name: "下一页" }).click();
  await expect(page.getByText("team/web", { exact: false })).toBeVisible();
  await page.getByLabel("选择仓库 team/web", { exact: true }).check();
  await expect(page.getByLabel("Core 的目标分支")).toHaveValue("feature/test");
  await page.getByLabel("选择仓库 team/web", { exact: true }).uncheck();
  await page.getByRole("button", { name: "上一页" }).click();
  await expect(page.getByText("team/core", { exact: false })).toBeVisible();
  await page.getByRole("checkbox").first().check();
  await expect(page.getByLabel("Core 的目标分支")).toHaveValue("feature/test");
  await page.getByLabel("Core 的目标分支").fill("dev");
  await page.getByRole("button", { name: "下一步" }).click();
  await expect(page.getByRole("checkbox", { name: /自动合并/ })).not.toBeChecked();
  await expect(page.getByText("目标 dev")).toBeVisible();
  await page.getByRole("button", { name: "下一步：配置 OpenCode" }).click();
  await expect(page.getByText("检测到已有的 OpenCode 登录配置")).toBeVisible();
  await page.getByRole("button", { name: "检查并继续" }).click();
  await expect(page.locator("select")).toHaveValue("example/development");
  await page.getByRole("button", { name: "验证模型" }).click();
  await expect(page.getByText("模型验证通过")).toBeVisible();
  await page.getByRole("button", { name: "创建项目并开始初始化" }).click();
  await expect(
    page.getByRole("heading", { name: "需要补充信息" }),
  ).toBeVisible();
  await page.getByLabel("请提供本地联调方式。").fill("docker compose");
  await page.getByRole("button", { name: "提交并继续" }).click();
  await expect(page.getByText("初始化完成")).toBeVisible();
  await expect(page.getByRole("button", { name: "发布到 GitLab", exact: true })).toBeEnabled();
  await expect(page).toHaveURL(/projects\/[^/]+\/initialization/);
  await page.reload();
  await expect(page.getByText("初始化完成", { exact: false })).toBeVisible();
  await page.getByRole("link", { name: "返回项目配置" }).click();
  await page.getByRole("link", { name: "查看初始化进度" }).click();
  await expect(page.getByText("初始化完成", { exact: false })).toBeVisible();
  await page.getByRole("button", { name: "返回项目设置" }).click();
  const mergeToggle = page.getByRole("checkbox", { name: /自动合并/ });
  await expect(mergeToggle).toBeVisible();
  expect((await mergeToggle.boundingBox())?.width).toBeLessThanOrEqual(24);
  await mergeToggle.check();
  await expect(mergeToggle).toBeChecked();
  await page.getByRole("link", { name: "查看初始化进度" }).click();
  await expect(page.getByText("初始化完成", { exact: false })).toBeVisible();
  expect(state.saves).toHaveLength(1);
  expect(state.saves[0]).toMatchObject({
    metaMode: "managed",
    connectionId: "c1",
    autoMerge: false,
    metaUrl: "",
    enabled: false,
    credentialRef: "",
    development: { adapter: "opencode", model: "example/development" },
  });
  expect(state.saves[0].repositories?.[0]).toMatchObject({
    projectId: 101,
    url: "https://gitlab.example/team/core.git",
    path: "core",
    targetBranch: "dev",
  });
});

test("failed model verification blocks initialization", async ({ page }) => {
  await setupWizard(page, { verifyFails: true });
  await page.goto("/#/projects/new");
  await advanceToOpenCode(page);
  await page.getByRole("button", { name: "验证模型" }).click();
  await expect(page.getByText("模型调用失败")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "创建项目并开始初始化" }),
  ).toBeDisabled();
});

test("wizard stays within a small viewport", async ({ page }) => {
  await setupWizard(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/#/projects/new");
  await page.getByLabel("GitLab 地址").fill("https://gitlab.example");
  await page.getByLabel("Access Token").fill("glpat-secret-value");
  await page.getByRole("button", { name: "验证并继续" }).click();
  await expect(page.getByText("team/core", { exact: false })).toBeVisible();
  const checkbox = page.getByLabel("选择仓库 team/core", { exact: true });
  const box = await checkbox.boundingBox();
  expect(box?.width).toBeLessThanOrEqual(24);
  await checkbox.focus();
  await page.keyboard.press("Space");
  await expect(checkbox).toBeChecked();
  await expect(page.getByLabel("Core 的目标分支")).toHaveValue("dev");
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
});


test("wizard terminal opens in a floating window and releases the session on close", async ({ page }) => {
  await setupWizard(page);
  let closed = 0;
  let connections = 0;
  await page.routeWebSocket("**/api/runtime/terminal", socket => {
    connections++;
    socket.onClose(() => closed++);
    socket.send(Buffer.from("wizard-ready\r\n"));
  });
  await page.goto("/#/projects/new");
  await advanceToOpenCode(page);
  await expect(page.getByTestId("wizard-terminal")).not.toBeVisible();
  expect(connections).toBe(0);
  await page.getByRole("button", { name: "打开终端", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "OpenCode 配置终端", exact: true })).toBeVisible();
  await expect(page.locator(".xterm-accessibility-tree")).toContainText("wizard-ready");
  await page.getByRole("button", { name: "关闭终端" }).click();
  await expect.poll(() => closed).toBe(1);
  await expect(page.getByRole("button", { name: "检查并继续" })).toBeVisible();
  await expect(page.locator(".xterm")).toHaveCount(0);
});
