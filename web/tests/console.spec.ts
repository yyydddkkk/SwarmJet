import { test, expect, type Page } from "@playwright/test";
import type { Project, Task } from "../src/types";
import { taskView } from "../src/task-presentation";
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
    if (path === "/api/tasks") {
      if (method === "POST") return json(task, 202);
      const query = new URL(request.url()).searchParams;
      const scoped = taskList.filter((t) => !query.get("project") || t.project.id === query.get("project"));
      const counts = {
        all: scoped.length,
        active: scoped.filter((t) => taskView(t) === "active").length,
        attention: scoped.filter((t) => taskView(t) === "attention").length,
        ended: scoped.filter((t) => taskView(t) === "ended").length,
        completed: scoped.filter((t) => t.stage === "completed").length,
      };
      const needle = (query.get("q") || "").toLowerCase();
      const items = scoped.filter((t) =>
        (!query.get("view") || query.get("view") === "all" || taskView(t) === query.get("view")) &&
        (!query.get("stage") || t.stage === query.get("stage")) &&
        (!needle || `${t.issue.title} ${t.issue.iid} ${t.project.name} ${t.id}`.toLowerCase().includes(needle)));
      const offset = Number(query.get("cursor") || 0), limit = Number(query.get("limit") || 50);
      return json({ items: items.slice(offset, offset + limit), nextCursor: offset + limit < items.length ? String(offset + limit) : "", total: items.length, counts });
    }
    return json({ error: "not found" }, 404);
  });
  return { commands, getSaved: () => saved };
}
test("task center pages through more than fifty results", async ({ page }) => {
  const many = Array.from({ length: 55 }, (_, index): Task => ({
    ...task,
    id: `task-${String(index + 1).padStart(3, "0")}`,
    issue: { ...task.issue, iid: index + 1, title: `任务 ${index + 1}` },
  }));
  await mock(page, true, many);
  await page.goto("/#/tasks");
  await expect(page.getByRole("row")).toHaveCount(51);
  await page.getByRole("button", { name: "下一页" }).click();
  await expect(page.getByRole("row")).toHaveCount(6);
  await expect(page.getByRole("link", { name: "任务 55", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "上一页" }).click();
  await expect(page.getByRole("row")).toHaveCount(51);
});
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

test("task auto-merge switch inherits the project default and locks once merging starts", async ({ page }) => {
  let current: Task = { ...task, stage: "developing", project: { ...project, autoMerge: true }, autoMergeOverride: null };
  await mock(page, true, [current]);
  const received: Record<string, unknown>[] = [];
  await page.route("**/api/tasks/task-001", (route) => route.fulfill({ json: current }));
  await page.route("**/api/tasks/task-001/commands", (route) => {
    const body = route.request().postDataJSON();
    received.push(body);
    return route.fulfill({ status: 202, json: { accepted: true } });
  });
  await page.goto("/#/tasks/task-001");
  const toggle = page.getByRole("button", { name: "已开启", exact: true });
  await expect(toggle).toHaveAttribute("aria-pressed", "true");
  await toggle.click();
  await expect.poll(() => received.length).toBe(1);
  expect(received[0]).toMatchObject({ kind: "set_auto_merge", autoMerge: false });
  await expect(page.getByRole("button", { name: "切换中…", exact: true })).toBeDisabled();
  await expect(page.getByRole("button", { name: "切换中…", exact: true })).toHaveAttribute("aria-pressed", "true");
  current = { ...current, autoMergeOverride: false };
  await expect(page.getByRole("button", { name: "已关闭", exact: true })).toHaveAttribute("aria-pressed", "false");
  current = { ...current, stage: "merging", autoMergeOverride: true };
  await page.reload();
  await expect(page.getByRole("button", { name: "已开启", exact: true })).toBeDisabled();
});

test("cancel task shows a reversible confirmation before sending the command", async ({ page }) => {
  const state = await mock(page);
  await page.goto("/#/tasks/task-001");
  await page.getByRole("button", { name: "取消任务" }).click();
  const confirmation = page.getByRole("dialog", { name: "取消任务" });
  await expect(confirmation).toContainText("已经合并的变更不会撤回");
  await confirmation.getByRole("button", { name: "取消", exact: true }).click();
  expect(state.commands).toHaveLength(0);
  await page.getByRole("button", { name: "取消任务" }).click();
  await confirmation.getByRole("button", { name: "取消任务" }).click();
  await expect.poll(() => state.commands.length).toBe(1);
  expect(state.commands[0]).toMatchObject({ kind: "cancel" });
});

test("task terminal follows live Agent output, switches runs, and stops polling on close", async ({ page }) => {
  const liveTask: Task = {
    ...task,
    stage: "developing",
    executions: {
      dev: { ...task.executions!.development!, id: "exec-live", status: "running", exitCode: -1 },
      review: { ...task.executions!.development!, id: "exec-review", status: "succeeded" },
    },
  };
  await mock(page, true, [liveTask]);
  let liveOutput = '{"type":"text","part":{"text":"开始分析"}}\n';
  let requests = 0;
  await page.route("**/api/tasks/task-001/artifacts?**", route => {
    requests++;
    const relative = new URL(route.request().url()).searchParams.get("path");
    return route.fulfill({ status: 200, contentType: "text/plain", body: relative?.includes("exec-review") ? "审查完成\n" : liveOutput });
  });
  await page.setViewportSize({ width: 390, height: 740 });
  await page.goto("/#/tasks/task-001");
  await page.getByRole("button", { name: "打开终端", exact: true }).click();
  const terminal = page.getByRole("dialog", { name: "任务终端 · 只读" });
  await expect(terminal).toBeVisible();
  await expect(terminal.getByTestId("task-terminal-output")).toContainText("开始分析");
  await expect(terminal.getByRole("textbox")).toHaveCount(0);
  await terminal.getByRole("button", { name: "原始日志" }).click();
  await expect(terminal.getByTestId("task-terminal-output")).toContainText('"part":{"text":"开始分析"}');
  await terminal.getByRole("button", { name: "易读视图" }).click();
  const box = await terminal.boundingBox();
  expect(box).not.toBeNull();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(390);
  liveOutput += '{"type":"text","part":{"text":"修改完成"}}\n';
  await expect(terminal.getByTestId("task-terminal-output")).toContainText("修改完成", { timeout: 8000 });
  await terminal.getByLabel("Agent 执行").selectOption("review");
  await expect(terminal.getByTestId("task-terminal-output")).toContainText("审查完成");
  await terminal.getByRole("button", { name: "关闭终端" }).click();
  await expect(terminal).not.toBeVisible();
  const countAtClose = requests;
  await page.waitForTimeout(3300);
  expect(requests).toBe(countAtClose);
});

test("task terminal explains missing runs and waits for the first live output", async ({ page }) => {
  const queued: Task = { ...task, stage: "queued", executions: {} };
  await mock(page, true, [queued]);
  await page.goto("/#/tasks/task-001");
  await page.getByRole("button", { name: "打开终端", exact: true }).click();
  await expect(page.getByTestId("task-terminal-output")).toContainText("Agent 尚未启动");

  const running: Task = { ...task, stage: "developing", executions: {
    dev: { ...task.executions!.development!, id: "exec-live", status: "running" },
  } };
  await page.route("**/api/tasks/task-001", route => route.fulfill({ json: running }));
  let logStatus = 404;
  await page.route("**/api/tasks/task-001/artifacts?**", route => logStatus === 200
    ? route.fulfill({ status: 200, contentType: "text/plain", body: "Agent 输出已恢复\n" })
    : route.fulfill({ status: logStatus, json: { error: logStatus === 404 ? "artifact not found" : "node unavailable" } }));
  await expect(page.getByTestId("task-terminal-output")).toContainText("等待首批运行输出", { timeout: 8000 });
  await expect(page.getByRole("dialog").getByRole("alert")).toHaveCount(0);
  logStatus = 502;
  await page.getByRole("dialog").getByRole("button", { name: "刷新" }).click();
  await expect(page.getByRole("dialog").getByRole("alert")).toContainText("node unavailable");
  logStatus = 200;
  await page.getByRole("dialog").getByRole("button", { name: "刷新" }).click();
  await expect(page.getByTestId("task-terminal-output")).toContainText("Agent 输出已恢复");
  await expect(page.getByRole("dialog").getByRole("alert")).toHaveCount(0);
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
