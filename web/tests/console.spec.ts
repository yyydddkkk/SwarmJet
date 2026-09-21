import { test, expect, type Page } from "@playwright/test";
import type { Project, Task } from "../src/types";
const project: Project = {
  id: "platform",
  name: "平台工程",
  metaUrl: "https://gitlab.example/team/meta.git",
  metaRef: "main",
  version: 1,
  enabled: true,
  gitlabUrl: "https://gitlab.example",
  credentialRef: "GITLAB_TOKEN",
  mergeCredentialRef: "GITLAB_MERGE_TOKEN",
  repositories: [
    {
      id: "api",
      projectId: 12,
      url: "https://gitlab.example/team/api.git",
      path: "api",
      targetBranch: "release",
      createFrom: "main",
      dependsOn: null,
      sensitivePaths: null,
    },
  ],
  development: {
    adapter: "opencode",
    model: "provider/model",
    image: "swarmjet-agent:local",
    timeoutSeconds: 5400,
  },
  review: {
    adapter: "opencode",
    model: "provider/review",
    image: "swarmjet-agent:local",
    timeoutSeconds: 5400,
  },
  integration: {
    composeFile: "compose.yaml",
    startupTimeoutSeconds: 180,
    checks: [
      {
        name: "integration",
        service: "api",
        command: ["npm", "test"],
        required: true,
      },
    ],
  },
  maxReworks: 3,
  requireCI: true,
};
const task: Task = {
  id: "task-001",
  project,
  issue: {
    projectId: 12,
    iid: 42,
    title: "增加工作区权限检查",
    description: "",
    url: "https://gitlab.example/team/api/-/issues/42",
    state: "opened",
    labels: ["autonomous"],
  },
  dependencies: null,
  stage: "waiting_approval",
  message: "验证与审查已完成",
  workspace: {
    taskId: "task-001",
    root: "/runs/task-001",
    metaSha: "abc123",
    revisions: [
      {
        repositoryId: "api",
        projectId: 12,
        path: "api",
        baseSha: "abc",
        headSha: "def",
        targetBranch: "release",
        branch: "swarmjet/task-001",
        changed: true,
        sensitive: false,
        files: ["src/auth.go"],
        mrIid: 31,
        mrUrl: "https://gitlab.example/team/api/-/merge_requests/31",
        merged: false,
      },
    ],
  },
  executions: {
    development: {
      id: "exec-1",
      sessionId: "session",
      containerId: "container",
      status: "completed",
      exitCode: 0,
      logPath: "/runs/task-001/executions/exec-1/agent.log",
    },
  },
  verification: [
    {
      name: "integration",
      required: true,
      passed: true,
      exitCode: 0,
      report: "checks passed",
      revisionDigest: "digest-1",
    },
  ],
  assumptions: null,
  notes: null,
  reworks: 0,
  reviewDigest: "digest-1",
  approvalDigest: "",
  paused: false,
  createdAt: "2026-09-21T01:00:00Z",
  updatedAt: "2026-09-21T02:00:00Z",
};
async function mock(page: Page, loggedIn = true) {
  let saved = structuredClone(project);
  let signedIn = loggedIn;
  const commands: unknown[] = [];
  await page.route("**/api/**", async (route) => {
    const request = route.request(),
      path = new URL(request.url()).pathname,
      method = request.method();
    if (method !== "GET")
      expect(request.headers()["x-requested-with"]).toBe("swarmjet");
    const json = (data: unknown, status = 200) =>
      route.fulfill({ status, json: data });
    if (path === "/api/session")
      return signedIn
        ? json({ username: "admin" })
        : json({ error: "unauthorized" }, 401);
    if (path === "/api/login") {
      signedIn = true;
      return json({ username: "admin" });
    }
    if (path === "/api/logout") {
      signedIn = false;
      return route.fulfill({ status: 204 });
    }
    if (path === "/api/projects/platform" && method === "PUT") {
      saved = { ...request.postDataJSON(), version: 2 };
      return json(saved);
    }
    if (path === "/api/projects") return json([saved]);
    if (path.endsWith("/commands")) {
      commands.push(request.postDataJSON());
      return json({ error: "审查版本已变化，请重新确认" }, 409);
    }
    if (path.endsWith("/events"))
      return json([
        {
          id: 1,
          taskId: task.id,
          kind: "reviewing",
          message: "审查完成",
          createdAt: task.updatedAt,
        },
      ]);
    if (path.endsWith("/artifacts"))
      return route.fulfill({
        body: "agent: verification complete",
        contentType: "text/plain",
      });
    if (path === "/api/tasks/task-001") return json(task);
    if (path === "/api/tasks")
      return json(
        method === "POST" ? task : [task],
        method === "POST" ? 202 : 200,
      );
    return json({ error: "not found" }, 404);
  });
  return { commands, getSaved: () => saved };
}
test("login, task filtering, exact-digest approval errors, logs and logout", async ({
  page,
}) => {
  const state = await mock(page, false);
  await page.goto("/");
  await page.getByLabel("用户名", { exact: true }).fill("admin");
  await page.getByLabel("密码", { exact: true }).fill("password");
  await page.getByLabel("密码", { exact: true }).press("Enter");
  await expect(
    page.getByRole("heading", { name: "任务中心", exact: true }),
  ).toBeVisible();
  await page.getByLabel("筛选任务阶段").selectOption("completed");
  await expect(page.getByText("没有符合筛选条件的任务。")).toBeVisible();
  await page.getByLabel("筛选任务阶段").selectOption("waiting_approval");
  await page.getByRole("link", { name: task.issue.title, exact: true }).click();
  await expect(page.getByText("digest-1", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "批准此版本" }).click();
  await expect(page.getByRole("alert")).toContainText("审查版本已变化");
  expect(state.commands).toEqual([
    expect.objectContaining({ kind: "approve", digest: "digest-1" }),
  ]);
  await page.getByRole("button", { name: "查看日志" }).click();
  await expect(page.getByRole("dialog")).toContainText(
    "agent: verification complete",
  );
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).not.toBeVisible();
  await page.getByRole("button", { name: "退出登录" }).click();
  await expect(
    page.getByRole("heading", { name: "登录工作控制台" }),
  ).toBeVisible();
});
test("project form preserves repository target and argv while saving configuration", async ({
  page,
}) => {
  const state = await mock(page);
  await page.goto("/#/projects/platform");
  await page.getByLabel("项目名称", { exact: true }).fill("核心平台");
  await page.getByLabel("api 的目标分支", { exact: true }).fill("release/next");
  await page.locator(".repository-details > summary").click();
  await page.getByLabel("初始化引用（可选）", { exact: true }).fill("stable");
  await page.getByLabel("检查 1 命令", { exact: true }).fill("go test ./...");
  await page.getByRole("button", { name: "保存更改" }).click();
  await expect(page.getByRole("status")).toContainText("项目配置已保存");
  expect(state.getSaved().version).toBe(2);
  expect(state.getSaved().repositories?.[0]).toMatchObject({
    targetBranch: "release/next",
    createFrom: "stable",
    dependsOn: [],
    sensitivePaths: [],
  });
  expect(state.getSaved().integration.checks?.[0].command).toEqual([
    "go",
    "test",
    "./...",
  ]);
  await page.getByText("连接与凭据设置", { exact: false }).click();
  await expect(page.getByLabel("项目标识", { exact: false })).toBeDisabled();
});
test("create task dialog supports keyboard, narrow screens have no page overflow", async ({
  page,
}) => {
  await mock(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");
  await page.getByRole("button", { name: "创建任务" }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await page
    .getByRole("dialog")
    .getByLabel("项目", { exact: true })
    .selectOption("platform");
  await page.getByLabel("Issue 所属 GitLab 项目 ID").fill("12");
  await page.getByLabel("Issue IID").fill("42");
  await page.getByLabel("Issue IID").press("Enter");
  await expect(
    page.getByRole("heading", { name: task.issue.title }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await expect(page.getByRole("button", { name: "退出登录" })).toBeVisible();
});
