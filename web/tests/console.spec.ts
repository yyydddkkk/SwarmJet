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
async function mock(page: Page, loggedIn = true, taskList: Task[] = [task]) {
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
    if (path === "/api/runtime/agents")
      return json({
        agents: [{ id: "opencode", name: "OpenCode", executable: "opencode", setupCommand: "" }],
        enabled: ["opencode"],
      });
    if (path === "/api/projects/platform" && method === "PUT") {
      saved = { ...request.postDataJSON(), version: 2 };
      return json(saved);
    }
    if (path === "/api/projects")
      return json(
        Array.from(
          new Map(
            [saved, ...taskList.map((t) => t.project)].map((p) => [p.id, p]),
          ).values(),
        ),
      );
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
    const requestedTask = taskList.find(
      (t) => path === `/api/tasks/${encodeURIComponent(t.id)}`,
    );
    if (requestedTask) return json(requestedTask);
    if (path === "/api/tasks")
      return json(
        method === "POST" ? task : taskList,
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
  await page.getByRole("button", { name: "执行记录", exact: true }).click();
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

test("workbench separates intervention states and combines project, stage and text filters", async ({
  page,
}) => {
  const states = [
    "developing",
    "waiting_approval",
    "waiting_manual_merge",
    "partial_merge",
    "blocked",
    "developing",
    "completed",
    "cancelled",
  ];
  const list = states.map((stage, i): Task => ({
    ...structuredClone(task),
    id: `task-${i}`,
    stage,
    paused: i === 5 || i === 7,
    issue: { ...task.issue, iid: 100 + i, title: `工作台任务 ${i}` },
    project:
      i === 0 ? { ...project, id: "payments", name: "交易服务" } : project,
  }));
  await mock(page, true, list);
  await page.goto("/");
  await expect(page.locator(".task-list tbody tr")).toHaveCount(8);
  await expect(page.locator(".attention-card")).toHaveCount(4);
  await expect(
    page.getByRole("button", { name: "需要处理 5", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "进行中 1", exact: true }).click();
  await expect(page.locator(".task-list tbody tr")).toHaveCount(1);
  await expect(page.locator(".task-list")).toContainText("工作台任务 0");
  await page.getByRole("button", { name: "已结束 2", exact: true }).click();
  await expect(page.locator(".task-list tbody tr")).toHaveCount(2);
  await expect(page.locator(".task-list .status.cancelled")).toHaveText(
    "已取消",
  );
  await page
    .getByRole("button", { name: "查看全部 5 项", exact: true })
    .click();
  await expect(page.locator(".task-list tbody tr")).toHaveCount(5);
  await page.getByRole("button", { name: "全部任务 8", exact: true }).click();
  await page.getByLabel("筛选任务阶段").selectOption("waiting_manual_merge");
  await expect(page.locator(".task-list tbody tr")).toHaveCount(1);
  await page.getByLabel("搜索任务").fill("102");
  await expect(page.locator(".task-list tbody tr")).toHaveCount(1);
  await page.getByLabel("搜索任务").fill("不存在");
  await expect(page.getByText("没有符合筛选条件的任务。")).toBeVisible();
  await page
    .getByRole("navigation", { name: "按项目查看任务" })
    .getByRole("link", { name: "交易服务" })
    .click();
  await expect(page.getByLabel("筛选项目")).toHaveValue("payments");
  await expect(page.locator(".task-list tbody tr")).toHaveCount(1);
  await expect(page.locator(".attention-card")).toHaveCount(0);
});

for (const stage of [
  "blocked",
  "waiting_manual_merge",
  "partial_merge",
  "cancelled",
]) {
  test(`task detail preserves ${stage} state and available actions`, async ({
    page,
  }) => {
    await mock(page, true, [{ ...task, stage }]);
    await page.goto("/#/tasks/task-001");
    await expect(
      page.getByRole("heading", { name: task.issue.title, exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "批准此版本", exact: true }),
    ).toHaveCount(0);
    if (stage === "cancelled") {
      await expect(
        page.getByRole("button", { name: "暂停任务", exact: true }),
      ).toHaveCount(0);
    } else {
      await expect(
        page.getByRole("button", { name: "暂停任务", exact: true }),
      ).toBeVisible();
      await expect(page.locator(".task-guidance")).toBeVisible();
    }
    if (stage === "blocked")
      await expect(
        page.getByRole("button", { name: "重试任务", exact: true }),
      ).toBeVisible();
    if (stage === "blocked" || stage === "cancelled")
      await expect(
        page.locator('.stage-track [aria-current="step"]'),
      ).toHaveCount(0);
    await page.getByRole("button", { name: "执行记录", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "查看日志", exact: true }),
    ).toBeVisible();
  });
}

test("commands use the real API without optimistic completion and note draft survives view changes", async ({
  page,
}) => {
  await mock(page);
  const received: Record<string, unknown>[] = [];
  await page.route("**/api/tasks/task-001/commands", (route) => {
    received.push(route.request().postDataJSON());
    return route.fulfill({ status: 202, json: { accepted: true } });
  });
  await page.goto("/#/tasks/task-001");
  await page.getByRole("button", { name: "批准此版本", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("指令已提交");
  expect(received[0]).toMatchObject({ kind: "approve", digest: "digest-1" });
  await expect(
    page.getByRole("button", { name: "批准此版本", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "需求与说明", exact: true }).click();
  await page.getByLabel("补充说明", { exact: true }).fill("保留现有接口兼容性");
  await page.getByRole("button", { name: "交付结果", exact: true }).click();
  await page.getByRole("button", { name: "需求与说明", exact: true }).click();
  await expect(page.getByLabel("补充说明", { exact: true })).toHaveValue(
    "保留现有接口兼容性",
  );
  await page.getByRole("button", { name: "提交说明", exact: true }).click();
  await expect.poll(() => received.length).toBe(2);
  expect(received[1]).toMatchObject({
    kind: "note",
    text: "保留现有接口兼容性",
  });
});

for (const width of [390, 1440]) {
  test(`workbench layout at ${width}`, async ({ page }, testInfo) => {
    await mock(page, true, [
      task,
      {
        ...task,
        id: "task-002",
        stage: "developing",
        issue: { ...task.issue, title: "支持按项目筛选执行记录" },
      },
    ]);
    await page.setViewportSize({ width, height: 1000 });
    await page.goto("/");
    await expect(page.locator(".task-list tbody tr")).toHaveCount(2);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: testInfo.outputPath("tasks.png"),
      fullPage: true,
    });
    await page
      .getByRole("link", { name: task.issue.title, exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: "批准此版本", exact: true }),
    ).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: testInfo.outputPath("detail.png"),
      fullPage: true,
    });
  });
}
