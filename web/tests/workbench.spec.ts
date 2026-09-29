import { test, expect } from "@playwright/test";

const project = { id: "platform", name: "平台工程", provider: "gitlab", gitlabUrl: "https://gitlab.example", repositories: [{ id: "api", projectId: 12, path: "api", targetBranch: "main" }] };
const issue = { projectId: "platform", repositoryId: "api", gitlabProjectId: 12, iid: 42, title: "修复权限检查", description: "需求说明", state: "opened", labels: ["autonomous"], assignees: [], webUrl: "https://gitlab.example/issues/42", updatedAt: "2026-09-28T01:00:00Z", taskIds: [], tasks: [], notes: [] };
const mr = { projectId: "platform", repositoryId: "api", gitlabProjectId: 12, iid: 8, title: "实现权限检查", description: "变更说明", state: "opened", sourceBranch: "feature", targetBranch: "main", sha: "abc123", detailedMergeStatus: "mergeable", webUrl: "https://gitlab.example/merge_requests/8", updatedAt: "2026-09-28T02:00:00Z", discussions: [], pipelines: [], approvals: { approved_by: [] } };

test("Issue workbench opens detail and starts a task", async ({ page }) => {
  await page.route("**/api/**", route => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/session") return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (url.pathname === "/api/projects") return route.fulfill({ json: [project] });
    if (url.pathname === "/api/workbench/issues") { expect(url.searchParams.get("state")).toBe("opened"); return route.fulfill({ json: { items: [issue], nextCursor: "" } }); }
    if (url.pathname === "/api/workbench/issues/platform/api/42") return route.fulfill({ json: issue });
    if (url.pathname === "/api/tasks" && route.request().method() === "POST") return route.fulfill({ status: 202, json: { id: "task-42" } });
    return route.fulfill({ json: [] });
  });
  await page.goto("/");
  await page.getByRole("navigation", { name: "工作台导航" }).getByRole("link", { name: "Issue", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Issue 工作台" })).toBeVisible();
  await expect(page.getByRole("link", { name: /修复权限检查/ }).locator(".workbench-row-labels")).toHaveText("autonomous");
  await page.getByRole("link", { name: /修复权限检查/ }).click();
  await expect(page.getByRole("heading", { name: "修复权限检查" })).toBeVisible();
  await page.getByRole("button", { name: "开始任务" }).click();
  await expect(page).toHaveURL(/#\/tasks\/task-42/);
});

test("Issue detail opens in reading mode and saves edits back to the overview", async ({ page }) => {
  let current = { ...issue, description: "## 验收标准\n\n- 登录可用" };
  let saves = 0;
  await page.route("**/api/**", route => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/session") return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (url.pathname === "/api/workbench/issues/platform/api/42" && route.request().method() === "PUT") {
      saves++;
      current = { ...current, ...route.request().postDataJSON() };
      return route.fulfill({ json: current });
    }
    if (url.pathname === "/api/workbench/issues/platform/api/42") return route.fulfill({ json: current });
    return route.fulfill({ json: [] });
  });
  await page.goto("/#/issues/platform/api/42");
  await expect(page.getByRole("heading", { name: "验收标准" })).toBeVisible();
  await expect(page.getByRole("textbox", { name: "标题" })).toHaveCount(0);
  await page.getByRole("button", { name: "编辑 Issue" }).click();
  await page.getByRole("textbox", { name: "标题" }).fill("暂存标题");
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await expect(page.getByRole("heading", { name: "修复权限检查" })).toBeVisible();
  expect(saves).toBe(0);
  await page.getByRole("button", { name: "编辑 Issue" }).click();
  await page.getByRole("textbox", { name: "标题" }).fill("更新后的 Issue");
  await page.getByRole("button", { name: "保存更改" }).click();
  await expect(page.getByRole("heading", { name: "更新后的 Issue" })).toBeVisible();
  await expect(page.getByRole("textbox", { name: "标题" })).toHaveCount(0);
  expect(saves).toBe(1);
});

test("Issue detail renders uploaded images and shows the newest comment below the composer", async ({ page }) => {
  const secret = "0123456789abcdef0123456789abcdef";
  const withComments = {
    ...issue,
    webUrl: "https://gitlab.example/group/api/-/issues/42",
    description: `说明\n\n![截图](/uploads/${secret}/screen.png)`,
    notes: [
      { id: 1, body: "较早评论", created_at: "2026-09-27T01:00:00Z", author: { name: "甲" } },
      { id: 2, body: `较新评论\n\n![新截图](/uploads/${secret}/new.png)`, created_at: "2026-09-28T01:00:00Z", author: { name: "乙" } },
    ],
  };
  const imageRequests: string[] = [];
  await page.route("**/api/**", route => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/session") return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (url.pathname === "/api/workbench/issues/platform/api/42") return route.fulfill({ json: withComments });
    if (url.pathname.startsWith(`/api/workbench/issues/platform/api/42/uploads/${secret}/`)) {
      imageRequests.push(url.pathname);
      return route.fulfill({ status: 200, contentType: "image/png", body: Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/XWcAAAAASUVORK5CYII=", "base64") });
    }
    return route.fulfill({ json: [] });
  });
  await page.goto("/#/issues/platform/api/42");
  await expect(page.getByRole("img", { name: "截图", exact: true })).toBeVisible();
  await expect(page.getByRole("img", { name: "新截图" })).toBeVisible();
  expect(imageRequests).toContain(`/api/workbench/issues/platform/api/42/uploads/${secret}/screen.png`);
  expect(imageRequests).toContain(`/api/workbench/issues/platform/api/42/uploads/${secret}/new.png`);
  const comments = page.locator(".workbench-comments");
  const text = await comments.innerText();
  expect(text.indexOf("发表评论")).toBeLessThan(text.indexOf("较新评论"));
  expect(text.indexOf("较新评论")).toBeLessThan(text.indexOf("较早评论"));
});

test("Issue comments keep plain text and request a follow-up through a new note", async ({ page }) => {
  const posts: Record<string, unknown>[] = [];
  let nextRoundCalls = 0;
  const withPreviousRound = { ...issue, tasks: [{ id: "task-1", stage: "completed", round: 1 }] };
  await page.route("**/api/**", route => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/session") return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (url.pathname === "/api/workbench/issues/platform/api/42/notes") {
      posts.push(route.request().postDataJSON());
      return route.fulfill({ status: 201, json: { id: posts.length } });
    }
    if (url.pathname.endsWith("/next-round")) nextRoundCalls++;
    if (url.pathname === "/api/workbench/issues/platform/api/42") return route.fulfill({ json: withPreviousRound });
    return route.fulfill({ json: [] });
  });
  await page.goto("/#/issues/platform/api/42");
  await expect(page.getByRole("button", { name: "发起下一轮", exact: true })).toHaveCount(0);
  const editor = page.getByRole("textbox", { name: "评论内容或补充需求" });
  await editor.fill("普通评论 `代码`\n第二行");
  await page.getByRole("button", { name: "发表评论" }).click();
  await expect(page.getByRole("status")).toHaveText("评论已发表。");
  await editor.fill("补充 `特殊`\u00a0空格\n再加一项");
  await page.getByRole("button", { name: "评论并发起下一轮" }).click();
  await expect(page.getByRole("status")).toHaveText("评论已提交，等待系统创建任务。");
  expect(posts).toEqual([
    { body: "普通评论 `代码`\n第二行" },
    { body: "补充 `特殊`\u00a0空格\n再加一项", startNextRound: true },
  ]);
  expect(nextRoundCalls).toBe(0);
});

test("Issue follow-up queues during an active round, ignores double clicks, and keeps failed text", async ({ page }) => {
  let postCount = 0;
  const activeIssue = { ...issue, tasks: [{ id: "task-1", stage: "implementing", round: 1 }] };
  await page.route("**/api/**", async route => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/session") return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (url.pathname === "/api/workbench/issues/platform/api/42/notes") {
      postCount++;
      await new Promise(resolve => setTimeout(resolve, 75));
      return postCount === 1
        ? route.fulfill({ status: 201, json: { id: 81 } })
        : route.fulfill({ status: 503, json: { error: "GitLab 暂不可用" } });
    }
    if (url.pathname === "/api/workbench/issues/platform/api/42") return route.fulfill({ json: activeIssue });
    return route.fulfill({ json: [] });
  });
  await page.goto("/#/issues/platform/api/42");
  await expect(page.getByText("当前轮仍在运行，补充需求会排队等待本轮结束后创建下一轮任务。")).toBeVisible();
  const editor = page.getByRole("textbox", { name: "评论内容或补充需求" });
  await editor.fill("第一条补充需求");
  await page.getByRole("button", { name: "评论并发起下一轮" }).evaluate(button => { (button as HTMLButtonElement).click(); (button as HTMLButtonElement).click(); });
  await expect(page.getByRole("status")).toHaveText("评论已提交，等待系统创建任务。");
  expect(postCount).toBe(1);
  await editor.fill("失败后保留的需求");
  await page.getByRole("button", { name: "评论并发起下一轮" }).click();
  await expect(page.getByRole("alert")).toContainText("GitLab 暂不可用");
  await expect(editor).toHaveValue("失败后保留的需求");
  await expect(page.getByRole("status")).toHaveCount(0);
  expect(postCount).toBe(2);
});

test("Issue timeline presents SwarmJet notes and system activity without raw control text", async ({ page }) => {
  const createdAt = "2026-09-28T12:00:00Z";
  const taskId = "b2b196e48f1dd0730fc7f47cb2f89ede";
  const review = `<!-- swarmjet-review:${taskId}:${"a".repeat(64)} -->\n## 人工复核指引 · SwarmJet 第 2 轮\n\n任务：\`${taskId}\`。本轮由系统执行验收与独立审查。\n\n审查提交集合：\`${"c".repeat(64)}\`\n\n### 本轮 MR\n- api：https://gitlab.example/group/api/-/merge_requests/53 · \`${"d".repeat(40)}\` → \`main\`\n\n### 操作步骤与预期结果\n1. 检查登录\n2. 检查退出`;
  const progress = `<!-- swarmjet-progress:${"b".repeat(64)} -->\n**waiting_manual_merge** · ${taskId} · 第 2 轮\n\n等待合并`;
  const withTimeline = { ...issue, tasks: [{ id: taskId, stage: "completed", round: 2, createdAt, triggerNoteIds: [3], workspace: { revisions: [{ path: "api", mrUrl: "https://gitlab.example/group/api/-/merge_requests/53" }] } }], notes: [
    { id: 5, body: "mentioned in commit 1c394033999a2c90f387b05d782a2bbde0d53a55", system: true, created_at: "2026-09-28T12:04:00Z", author: { name: "GitLab" } },
    { id: 4, body: review, created_at: "2026-09-28T12:03:00Z", author: { name: "SwarmJet" } },
    { id: 3, body: "/swarmjet run 改进错误提示\n补充第二行", created_at: "2026-09-28T12:02:00Z", author: { name: "甲" } },
    { id: 2, body: progress, created_at: "2026-09-28T12:01:00Z", author: { name: "SwarmJet" } },
    { id: 1, body: "普通讨论", created_at: "2026-09-28T11:59:00Z", author: { name: "乙" } },
  ] };
  await page.route("**/api/**", route => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/session") return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (url.pathname === "/api/workbench/issues/platform/api/42") return route.fulfill({ json: withTimeline });
    return route.fulfill({ json: [] });
  });
  await page.goto("/#/issues/platform/api/42");
  await expect(page.getByRole("heading", { name: "第 2 轮期间" })).toBeVisible();
  const reviewCard = page.locator(".issue-note-review");
  await expect(reviewCard.getByText("人工复核指引 · 第 2 轮")).toBeVisible();
  await expect(reviewCard.getByText("2 个操作步骤", { exact: false })).toBeVisible();
  await expect(reviewCard.getByRole("link", { name: "api MR !53" })).toBeVisible();
  await expect(reviewCard.getByText("swarmjet-review", { exact: false })).toBeHidden();
  await reviewCard.getByText("查看完整复核指引").click();
  await expect(reviewCard.getByText("检查登录", { exact: true })).toBeVisible();
  await expect(reviewCard.locator(".issue-note-expanded")).not.toContainText(taskId);
  await expect(reviewCard.locator(".issue-note-expanded")).not.toContainText("c".repeat(64));
  await expect(reviewCard.locator(".issue-note-expanded")).not.toContainText("https://gitlab.example/group/api/-/merge_requests/53");
  await expect(reviewCard.locator(".issue-note-expanded").getByRole("link", { name: "MR !53" })).toHaveAttribute("href", "https://gitlab.example/group/api/-/merge_requests/53");
  await expect(page.locator(".issue-note-progress .issue-note-kind")).toHaveText("任务进展 · 等待合并");
  await expect(page.locator(".issue-note-progress .issue-note-body")).not.toContainText(taskId);
  await expect(page.getByText("在提交 1c394033 中提及此 Issue")).toBeVisible();
  await expect(page.getByText("改进错误提示")).toBeVisible();
  await expect(page.locator(".issue-note-followup .issue-note-body")).not.toContainText("/swarmjet run");
  await page.getByRole("button", { name: "讨论 2" }).click();
  await expect(page.locator(".issue-note-review")).toHaveCount(0);
  await expect(page.locator(".issue-note-followup")).toBeVisible();
  await page.getByRole("button", { name: "系统活动 1" }).click();
  await expect(page.locator(".issue-activity")).toHaveCount(1);
  await expect(page.locator(".issue-note-followup")).toHaveCount(0);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
});

test("MR workbench shows paged diff and approval with current SHA", async ({ page }) => {
  let approval: unknown;
  await page.route("**/api/**", route => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/session") return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (url.pathname === "/api/projects") return route.fulfill({ json: [project] });
    if (url.pathname === "/api/workbench/merge-requests") { expect(url.searchParams.get("state")).toBe("opened"); return route.fulfill({ json: { items: [mr], nextCursor: "" } }); }
    if (url.pathname === "/api/workbench/merge-requests/platform/api/8") return route.fulfill({ json: mr });
    if (url.pathname.endsWith("/diffs")) return route.fulfill({ json: { diffs: [{ old_path: "src/auth.ts", new_path: "src/auth.ts", diff: "@@ -1 +1 @@\n-old\n+new" }], hasNext: false, incomplete: false, page: 1, version: { baseCommitSha: "base", startCommitSha: "start", headCommitSha: "abc123" } } });
    if (url.pathname.endsWith("/approve")) { approval = route.request().postDataJSON(); return route.fulfill({ json: {} }); }
    return route.fulfill({ json: [] });
  });
  await page.goto("/#/merge-requests");
  await page.getByRole("link", { name: /实现权限检查/ }).click();
  await expect(page.getByText("src/auth.ts", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "批准当前版本" }).click();
  expect(approval).toEqual({ sha: "abc123" });
});

test("MR workbench explains an empty open list and offers the full history", async ({ page }) => {
  await page.route("**/api/**", route => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/session") return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (url.pathname === "/api/projects") return route.fulfill({ json: [project] });
    if (url.pathname === "/api/workbench/merge-requests") return route.fulfill({ json: { items: url.searchParams.get("state") === "all" ? [{ ...mr, state: "merged" }] : [], nextCursor: "" } });
    return route.fulfill({ json: [] });
  });
  await page.goto("/#/merge-requests");
  await expect(page.getByText(/当前没有符合条件的未合并 MR/)).toBeVisible();
  await page.getByLabel("状态").selectOption("all");
  await expect(page.getByRole("link", { name: /实现权限检查/ })).toBeVisible();
});

test("Issue workbench stays usable on a narrow screen", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route("**/api/**", route => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/session") return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (path === "/api/projects") return route.fulfill({ json: [project] });
    if (path === "/api/workbench/issues") return route.fulfill({ json: { items: [], nextCursor: "" } });
    return route.fulfill({ json: [] });
  });
  await page.goto("/#/issues");
  await expect(page.getByText("没有符合条件的 Issue。")).toBeVisible();
  await expect(page.getByRole("button", { name: "创建 Issue" })).toBeVisible();
  const width = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(width).toBeLessThanOrEqual(390);
  await page.getByRole("button", { name: "创建 Issue" }).click();
  const checkbox = page.getByRole("checkbox", { name: /创建并开始任务/ });
  const bounds = await checkbox.boundingBox();
  expect(bounds).not.toBeNull();
  expect(bounds!.width).toBeLessThanOrEqual(22);
  expect(bounds!.height).toBeLessThanOrEqual(22);
});

test("managed MR leads to task acceptance and whole-round merge", async ({ page }) => {
  const commands: unknown[] = [];
  let saved = false;
  const managed = { ...mr, taskId: "task-8" };
  const task = { id: "task-8", project: { ...project, autoMerge: false, acceptanceStrategy: "none" }, issue: { projectId: 12, iid: 42, title: "修复权限检查", description: "需求说明", url: issue.webUrl, state: "opened", labels: ["autonomous"] }, stage: "waiting_manual_merge", message: "等待人工合并", humanAcceptance: { prerequisites: ["测试环境可访问"], steps: [{ action: "运行验收", expected: "通过" }], notes: ["记录测试结果"] }, workspace: { taskId: "task-8", root: "", metaSha: "", revisions: [{ repositoryId: "api", projectId: 12, path: "api", baseSha: "base", headSha: "abc123", targetBranch: "main", branch: "feature", changed: true, sensitive: false, files: [], mrIid: 8, mrUrl: mr.webUrl, merged: false }] }, executions: {}, verification: [], assumptions: [], notes: [], reworks: 0, reviewDigest: "review", approvalDigest: "", paused: false, createdAt: "2026-09-28T01:00:00Z", updatedAt: "2026-09-28T02:00:00Z" };
  await page.route("**/api/**", route => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/session") return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (url.pathname === "/api/workbench/merge-requests/platform/api/8") return route.fulfill({ json: managed });
    if (url.pathname.endsWith("/diffs")) return route.fulfill({ json: { diffs: [], hasNext: false, incomplete: false, page: 1, version: {} } });
    if (url.pathname === "/api/tasks/task-8") return route.fulfill({ json: task });
    if (url.pathname === "/api/tasks/task-8/events") return route.fulfill({ json: [] });
    if (url.pathname === "/api/tasks/task-8/manual-acceptance") {
      if (route.request().method() === "PUT") { const body = route.request().postDataJSON(); expect(body).toEqual({ digest: "evidence", checks: [{ index: 0, confirmed: true }, { index: 1, confirmed: true }], note: "验收通过" }); saved = true; return route.fulfill({ json: body }); }
      return route.fulfill({ json: { digest: saved ? "evidence" : "", expectedDigest: "evidence", checks: saved ? [{ index: 0, confirmed: true }, { index: 1, confirmed: true }] : [], note: saved ? "验收通过" : "" } });
    }
    if (url.pathname === "/api/tasks/task-8/commands") { commands.push(route.request().postDataJSON()); return route.fulfill({ status: 202, json: { accepted: true } }); }
    return route.fulfill({ json: [] });
  });
  await page.goto("/#/merge-requests/platform/api/8");
  await page.getByRole("link", { name: "前往任务审查与合并" }).click();
  await expect(page.getByRole("heading", { name: "人工验收与整轮合并" })).toBeVisible();
  await page.getByText("测试环境可访问").click();
  await page.getByText("运行验收 → 预期：通过").click();
  await page.getByPlaceholder("记录实际验收结果与证据…").fill("验收通过");
  await page.getByRole("button", { name: "保存人工验收" }).click();
  await expect(page.getByText("人工验收已保存，并绑定当前审查与提交版本。")).toBeVisible();
  await page.getByRole("button", { name: "合并整轮" }).click();
  expect(commands).toHaveLength(1);
  expect(commands[0]).toMatchObject({ kind: "merge_round" });
});

test("workbench shows GitLab permission errors", async ({ page }) => {
  await page.route("**/api/**", route => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/session") return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (path === "/api/projects") return route.fulfill({ json: [project] });
    if (path === "/api/workbench/issues") return route.fulfill({ status: 403, json: { error: "当前 GitLab 身份没有执行此操作的权限。" } });
    return route.fulfill({ json: [] });
  });
  await page.goto("/#/issues");
  await expect(page.getByRole("alert")).toContainText("当前 GitLab 身份没有执行此操作的权限。");
});

test("Issue selectors show names and submit GitLab IDs", async ({ page }) => {
  let created: Record<string, unknown> | undefined;
  const requests: string[] = [];
  await page.route("**/api/**", route => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/session") return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (url.pathname === "/api/projects") return route.fulfill({ json: [project] });
    if (url.pathname === "/api/workbench/issues" && route.request().method() === "GET") return route.fulfill({ json: { items: [], nextCursor: "" } });
    if (url.pathname === "/api/workbench/issues/platform/api/options") {
      requests.push(url.searchParams.get("search") || "");
      return route.fulfill({ json: { labels: [{ name: "backend", color: "#315caa" }, { name: "autonomous" }], assignees: [{ id: 77, name: "张明", username: "zhangming" }] } });
    }
    if (url.pathname === "/api/workbench/issues" && route.request().method() === "POST") {
      created = route.request().postDataJSON();
      return route.fulfill({ json: { ...issue, iid: 43, title: "新需求", labels: ["backend"], assignees: [{ id: 77, name: "张明", username: "zhangming" }] } });
    }
    return route.fulfill({ json: [] });
  });
  await page.goto("/#/issues");
  await expect(page.getByRole("combobox", { name: "负责人" })).toBeDisabled();
  await page.getByRole("button", { name: "创建 Issue" }).click();
  const dialog = page.getByRole("dialog", { name: "创建 Issue" });
  await dialog.getByLabel("项目").selectOption("platform");
  await dialog.getByLabel("仓库").selectOption("api");
  await dialog.getByRole("combobox", { name: "标签" }).fill("back");
  await expect(dialog.getByRole("option", { name: "backend" })).toBeVisible();
  await expect(dialog.getByRole("option", { name: "autonomous" })).toHaveCount(0);
  await dialog.getByRole("option", { name: "backend" }).click();
  await dialog.getByRole("combobox", { name: "负责人" }).fill("zhang");
  await dialog.getByRole("option", { name: /张明/ }).click();
  await dialog.getByLabel("标题").fill("新需求");
  await dialog.getByRole("button", { name: "仅创建 Issue" }).click();
  await expect(page).toHaveURL(/#\/issues\/platform\/api\/43/);
  expect(created).toMatchObject({ labels: ["backend"], assigneeIds: [77] });
  expect(requests).toContain("zhang");
});

test("MR edit preserves selected assignee when search returns other users", async ({ page }) => {
  let updated: Record<string, unknown> | undefined;
  const optionRequests: string[] = [];
  const withAssignee = { ...mr, labels: ["backend"], assignees: [{ id: 77, name: "张明", username: "zhangming" }] };
  await page.route("**/api/**", route => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/session") return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (url.pathname === "/api/workbench/merge-requests/platform/api/options") { optionRequests.push(url.searchParams.get("search") || ""); return route.fulfill({ json: { labels: [{ name: "backend" }], assignees: [{ id: 88, name: "李华", username: "lihua" }] } }); }
    if (url.pathname === "/api/workbench/merge-requests/platform/api/8" && route.request().method() === "PUT") { updated = route.request().postDataJSON(); return route.fulfill({ json: withAssignee }); }
    if (url.pathname === "/api/workbench/merge-requests/platform/api/8") return route.fulfill({ json: withAssignee });
    if (url.pathname.endsWith("/diffs")) return route.fulfill({ json: { diffs: [], hasNext: false, incomplete: false, page: 1, version: {} } });
    return route.fulfill({ json: [] });
  });
  await page.goto("/#/merge-requests/platform/api/8");
  await expect(page.getByRole("button", { name: "移除负责人 张明" })).toBeVisible();
  await page.getByRole("combobox", { name: "负责人" }).fill("李");
  await page.getByRole("option", { name: /李华/ }).click();
  await page.getByRole("button", { name: "保存更改" }).click();
  await expect.poll(() => updated?.assigneeIds).toEqual([77, 88]);
  expect(optionRequests).toContain("李");
});
