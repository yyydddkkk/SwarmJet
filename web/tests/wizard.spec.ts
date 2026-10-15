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

async function setupWizard(page: Page, options?: { verifyFails?: boolean; branchFailsOnce?: boolean }) {
  const verifyFails = options?.verifyFails ?? false;
  let branchFails = options?.branchFailsOnce ?? false;
  const saves: Project[] = [];
  const inits: string[] = [];
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
      return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (pathname === "/api/users")
      return route.fulfill({ json: { users: [{ id: "11111111-1111-4111-8111-111111111111", email: "owner@example.test", displayName: "Project Owner", status: "active" }] } });
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
    if (pathname === "/api/connections/c1/repositories/101/branches" && branchFails) {
      branchFails = false;
      return route.fulfill({ status: 502, json: { error: "GitLab 暂时不可用" } });
    }
    if (pathname === "/api/connections/c1/repositories/101/branches")
      return route.fulfill({ json: ["dev", "main", "release/next"] });
    if (pathname === "/api/connections/c1/repositories/102/branches" || pathname === "/api/connections/c1/repositories/103/branches")
      return route.fulfill({ json: ["main"] });
    if (pathname === "/api/runtime/agents") return route.fulfill({json:{agents:[{id:"opencode",name:"OpenCode"},{id:"codex",name:"Codex"}],enabled:["opencode","codex"]}});
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
      if (method === "POST") {
        inits.push(initId);
        return route.fulfill({ status: 202, json: initRecord(initId, "running") });
      }
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
  return { saves, inits };
}

async function advanceToOpenCode(page: Page) {
  await page.getByLabel("GitLab 地址").fill("https://gitlab.example");
  await page.getByLabel("Access Token").fill("glpat-secret-value");
  await page.getByRole("button", { name: "验证并继续" }).click();
  await expect(page.getByText("team/core", { exact: false })).toBeVisible();
  await page.getByRole("checkbox").first().check();
  await page.getByRole("button", { name: "下一步" }).click();
  await expect(page.getByRole("checkbox", { name: /自动合并/ })).toBeChecked();
  await page.getByRole("button", { name: "下一步：配置 OpenCode" }).click();
  await expect(page.getByText("检测到已有的 OpenCode 登录配置")).toBeVisible();
  await page.getByRole("button", { name: "检查并继续" }).click();
  await page.getByLabel("项目默认 Agent",{exact:true}).selectOption("opencode");
  await expect(page.locator("select").filter({has:page.locator('option[value="example/development"]')})).toHaveValue("example/development");
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
  await expect(page.getByLabel("Core 的目标分支")).toContainText("release/next");
  await page.getByLabel("Core 的目标分支").selectOption("release/next");
  await expect(page.getByLabel("Core 的目标分支")).toHaveValue("release/next");
  await page.getByLabel("Core 的目标分支").selectOption("");
  const customBranch = page.getByLabel("Core 的自定义目标分支");
  await customBranch.fill("feature/test");
  await page.getByRole("button", { name: "下一页" }).click();
  await expect(page.getByText("team/web", { exact: false })).toBeVisible();
  await page.getByLabel("选择仓库 team/web", { exact: true }).check();
  await expect(customBranch).toHaveValue("feature/test");
  await page.getByLabel("选择仓库 team/web", { exact: true }).uncheck();
  await page.getByRole("button", { name: "上一页" }).click();
  await expect(page.getByText("team/core", { exact: false })).toBeVisible();
  await page.getByRole("checkbox").first().check();
  await expect(customBranch).toHaveValue("feature/test");
  await customBranch.fill("");
  await expect(customBranch).toHaveValue("");
  await expect(page.getByRole("button", { name: "下一步", exact: true })).toBeDisabled();
  await page.getByLabel("Core 的目标分支").selectOption("dev");
  await page.getByRole("button", { name: "下一步" }).click();
  await expect(page.getByRole("checkbox", { name: /自动合并/ })).toBeChecked();
  await expect(page.getByText("目标 dev")).toBeVisible();
  await page.getByRole("button", { name: "下一步：配置 OpenCode" }).click();
  await expect(page.getByText("检测到已有的 OpenCode 登录配置")).toBeVisible();
  await page.getByRole("button", { name: "检查并继续" }).click();
  await page.getByLabel("项目默认 Agent",{exact:true}).selectOption("opencode");
  await expect(page.locator("select").filter({has:page.locator('option[value="example/development"]')})).toHaveValue("example/development");
  await page.getByRole("button", { name: "验证模型" }).click();
  await expect(page.getByText("模型验证通过")).toBeVisible();
  await page.getByLabel("初始负责人（可选）", { exact: true }).selectOption("11111111-1111-4111-8111-111111111111");
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
  await page.getByRole("button", { name: "Core 工作区 的更多操作" }).click();
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
    autoMerge: true,
    acceptanceStrategy: "none",
    reviewMode: "none",
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

test("wizard shows branch read errors and retries", async ({ page }) => {
  await setupWizard(page, { branchFailsOnce: true });
  await page.goto("/#/projects/new");
  await page.getByLabel("GitLab 地址").fill("https://gitlab.example");
  await page.getByLabel("Access Token").fill("glpat-secret-value");
  await page.getByRole("button", { name: "验证并继续" }).click();
  await page.getByLabel("选择仓库 team/core").check();
  await expect(page.getByRole("alert")).toContainText("分支列表读取失败");
  await page.getByRole("button", { name: "重试" }).click();
  await expect(page.getByText("已读取 3 个 GitLab 分支")).toBeVisible();
  await expect(page.getByLabel("Core 的目标分支")).toContainText("release/next");
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

test("GitHub installation creates a direct single-repo project with platform checks", async ({ page }) => {
  const state = await setupWizard(page);
  await page.route("**/api/github/installations", route => route.fulfill({json:[{id:42,account:{login:"team"}},{id:43,account:{login:"suspended"},suspended_at:"2026-01-01"}]}));
  await page.route("**/api/github/installations/42/connections", route => route.fulfill({json:{id:"gh1",provider:"github",githubInstallationId:42,name:"team",username:"team",gitlabUrl:"",version:1}}));
  await page.route("**/api/connections/gh1/repositories?*", route => route.fulfill({json:{projects:[{id:501,name:"Core",pathWithNamespace:"team/core",defaultBranch:"main",httpUrlToRepo:"https://github.com/team/core.git",webUrl:"https://github.com/team/core",archived:false},{id:502,name:"Other",pathWithNamespace:"team/other",defaultBranch:"main",httpUrlToRepo:"https://github.com/team/other.git",webUrl:"https://github.com/team/other",archived:false}],page:1,hasNext:false}}));
  await page.route("**/api/connections/gh1/repositories/501/branches", route => route.fulfill({json:["main","develop"]}));
  await page.route("**/api/connections/gh1/repositories/502/branches", route => route.fulfill({json:["main"]}));
  await page.goto("/#/projects/new");
  await page.getByLabel("代码平台",{exact:true}).selectOption("github");
  await expect(page.getByLabel("GitHub App 安装")).toHaveValue("42");
  await expect(page.getByLabel("GitHub App 安装")).not.toContainText("suspended");
  await expect(page.getByLabel("Access Token",{exact:true})).toHaveCount(0);
  await page.getByRole("button",{name:"验证并继续"}).click();
  await expect(page.getByRole("heading",{name:"选择代码仓库"})).toBeVisible();
  await page.getByLabel("选择仓库 team/core",{exact:true}).check();
  await page.getByLabel("选择仓库 team/other",{exact:true}).check();
  await expect(page.getByLabel("选择仓库 team/core",{exact:true})).not.toBeChecked();
  await expect(page.getByLabel("选择仓库 team/other",{exact:true})).toBeChecked();
  await expect(page.getByLabel("Other 的目标分支")).toHaveValue("main");
  await page.getByLabel("选择仓库 team/core",{exact:true}).check();
  await expect(page.getByLabel("Core 的目标分支")).toHaveValue("main");
  await page.getByRole("button",{name:"下一步",exact:true}).click();
  await expect(page.getByLabel("GitHub 合并方式")).toHaveValue("squash");
  await expect(page.getByLabel("验收策略",{exact:true})).toHaveValue("platform_ci");
  await expect(page.getByLabel("Meta 分支")).toHaveCount(0);
  await page.getByRole("button",{name:"下一步：配置 OpenCode"}).click();
  await page.getByRole("button",{name:"检查并继续"}).click();
  await page.getByLabel("项目默认 Agent",{exact:true}).selectOption("opencode");
  await page.getByRole("button",{name:"验证模型"}).click();
  await expect(page.getByText("模型验证通过")).toBeVisible();
  await expect(page.getByLabel("初始负责人（可选）", { exact: true })).toHaveValue("");
  await expect(page.getByRole("button",{name:"创建项目",exact:true})).toBeEnabled();
  await page.getByRole("button",{name:"创建项目",exact:true}).click();
  await expect.poll(() => state.saves.length).toBe(1);
  expect(state.saves[0]).toMatchObject({provider:"github",connectionId:"gh1",metaMode:"direct",metaUrl:"",metaRef:"",gitlabUrl:"",mergeMethod:"squash",acceptanceStrategy:"platform_ci",requireCI:true,initialOwnerUserId:""});
  expect(state.saves[0].repositories?.[0]).toMatchObject({projectId:501,url:"https://github.com/team/core.git",path:".",targetBranch:"main"});
  expect(state.inits).toHaveLength(0);
});

test("GitHub repository picker blocks archived repositories and explains App authorization", async ({ page }) => {
  await setupWizard(page);
  await page.route("**/api/github/installations", route => route.fulfill({json:[{id:42,account:{login:"team"}}]}));
  await page.route("**/api/github/installations/42/connections", route => route.fulfill({json:{id:"gh1",provider:"github",githubInstallationId:42,name:"team",username:"team",gitlabUrl:"",version:1}}));
  await page.route("**/api/connections/gh1/repositories?*", route => route.fulfill({json:{projects:new URL(route.request().url()).searchParams.get("search") ? [] : [{id:501,name:"Archived",path_with_namespace:"team/archived",default_branch:"main",http_url_to_repo:"https://github.com/team/archived.git",web_url:"https://github.com/team/archived",archived:true}],page:1,hasNext:false}}));
  await page.goto("/#/projects/new");
  await page.getByLabel("代码平台",{exact:true}).selectOption("github");
  await expect(page.getByLabel("GitHub App 安装")).toHaveValue("42");
  await page.getByRole("button",{name:"验证并继续"}).click();
  await expect(page.getByLabel("选择仓库 team/archived",{exact:true})).toBeDisabled();
  await expect(page.getByRole("button",{name:"下一步",exact:true})).toBeDisabled();
  await page.getByPlaceholder("搜索仓库名称或路径").fill("missing");
  await page.getByRole("button",{name:"搜索",exact:true}).click();
  await expect(page.getByText("请调整搜索词，或检查 GitHub App 安装是否已授权目标仓库。")).toBeVisible();
  await page.getByRole("button",{name:/01.*连接代码平台/}).click();
  await expect(page.getByText("GitHub App 已连接：team（安装 42）")).toBeVisible();
  await expect(page.getByText(/连接已验证：.*指纹/)).toHaveCount(0);
});
