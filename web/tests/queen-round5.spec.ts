import { expect, test } from "@playwright/test";
import { mockHiveAPI } from "./hive-api-mock";

test.use({ viewport: { width: 1440, height: 1000 } });

test("default Queen conversation includes its Hive bootstrap report without unrelated messages or duplicates", async ({ page }) => {
  const state = await mockHiveAPI(page);
  const report = {
    id: "queen-bootstrap", hiveId: "swarmjet", from: "queen", to: "hive",
    kind: "report" as const, chainId: "system-bootstrap", hop: 1,
    body: "首次引导：仓库已检查，请选择方法论。", trust: "agent" as const,
    state: "handled" as const, createdAt: "2026-10-16T09:03:00Z",
  };
  state.messages.unshift(report, { ...report });
  state.messages.push(
    { ...report, id: "other-local", to: "local:another-admin", body: "另一个本地用户的私信" },
    { ...report, id: "other-queen-task", to: "forager:other", body: "Queen 给其他 Forager 的任务" },
    { ...report, id: "other-hive", from: "forager:other", body: "其他 Hive 汇报" },
  );
  await page.goto("/#/legacy/hives/swarmjet");
  const stream = page.locator(".hg-stream");
  const bootstrap = stream.locator("li").filter({ hasText: report.body });
  await expect(bootstrap).toHaveCount(1);
  await expect(bootstrap.locator(".hg-msg-author")).toHaveText("Queen（蜂王）");
  await expect(bootstrap.locator(".hg-trust")).toContainText("Agent");
  await expect(stream.locator("li").first()).toContainText(report.body);
  for (const body of ["另一个本地用户的私信", "Queen 给其他 Forager 的任务", "其他 Hive 汇报", "GitHub issues.opened：#142"]) {
    await expect(stream).not.toContainText(body);
  }
});

test("Queen conversation shows Hive inbox errors and can retry the bootstrap report", async ({ page }) => {
  const state = await mockHiveAPI(page);
  state.messages.push({
    id: "bootstrap-retry", hiveId: "swarmjet", from: "queen", to: "hive",
    kind: "report", chainId: "system-bootstrap", hop: 1, body: "重试后显示首次引导",
    trust: "agent", state: "handled", createdAt: "2026-10-16T09:03:00Z",
  });
  let unavailable = true;
  await page.route("**/api/hives/swarmjet/messages?to=hive", async route => {
    if (unavailable) {
      await route.fulfill({ status: 503, json: { error: "Hive inbox 暂时不可用" } });
    } else {
      await route.fallback();
    }
  });
  await page.goto("/#/legacy/hives/swarmjet");
  await expect(page.getByRole("alert")).toContainText("Hive inbox 暂时不可用");
  await expect(page.locator(".hg-stream")).toContainText("收到。我先把 #142");
  unavailable = false;
  await page.getByRole("alert").getByRole("button").click();
  await expect(page.locator(".hg-stream")).toContainText("重试后显示首次引导");
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("Queen status comes from its endpoint and the conversation is ordered plain text", async ({ page }, testInfo) => {
  const state = await mockHiveAPI(page);
  state.agents.swarmjet.find(agent => agent.role === "queen")!.state = "hibernated";
  const body = '<img src=x onerror="window.injected=true">\n请推荐方法论';
  // Insertion order intentionally differs from event time.
  state.messages.unshift({
    id: "queen-html", hiveId: "swarmjet", from: "queen", to: state.localAddress,
    kind: "reply", chainId: "chain-html", hop: 0, body, trust: "agent", state: "handled",
    createdAt: "2026-10-16T11:30:00Z",
  });
  await page.goto("/#/legacy/hives/swarmjet");
  await expect(page.getByRole("status").filter({ hasText: "Queen 状态：休眠" })).toContainText("deepseek/deepseek-chat");
  await expect(page.locator(".hg-stream > li").last()).toContainText(body);
  await expect(page.locator(".hg-stream img")).toHaveCount(0);
  await expect(page.locator(".hg-msg-author").getByText("你（网页）", { exact: true })).toBeVisible();
  await expect(page.locator(".hg-msg-author").getByText("Queen（蜂王）", { exact: true }).first()).toBeVisible();
  await expect(page.locator(".hg-msg-author").getByText("Forager · forager-7f3a", { exact: true })).toBeVisible();
  await expect(page.locator(".hg-msg-author").getByText("系统事件", { exact: true })).toBeVisible();
  await expect(page.getByText("外部内容，未经核实", { exact: true })).toBeVisible();
  await page.getByLabel("对 Queen（蜂王）说话").fill("请帮我选择方法论");
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect(page.getByText("请帮我选择方法论", { exact: true })).toBeVisible();
  const sent = state.calls.find(call => call.path === "/api/hives/swarmjet/messages" && call.method === "POST");
  expect(sent?.body).toMatchObject({ to: "queen", body: "请帮我选择方法论" });
  expect(sent?.headers["x-requested-with"]).toBe("swarmjet");
  await page.screenshot({ path: testInfo.outputPath("queen-conversation-1440.png"), fullPage: true });
});

test("disabled Queen links to settings and enabling it restores conversation", async ({ page }, testInfo) => {
  const state = await mockHiveAPI(page);
  state.messages.push({
    id: "disabled-queen-report", hiveId: "orders", from: "queen", to: "hive",
    kind: "report", chainId: "system-bootstrap", hop: 1, body: "Queen 关闭前的引导报告",
    trust: "agent", state: "handled", createdAt: "2026-10-15T20:10:00Z",
  });
  await page.goto("/#/legacy/hives/orders");
  await expect(page.getByText("Queen 未开启", { exact: true })).toBeVisible();
  await expect(page.getByLabel("对 Queen（蜂王）说话")).toHaveCount(0);
  await expect(page.locator(".hg-stream")).toContainText("Queen 关闭前的引导报告");
  await expect(page.locator(".hg-stream")).not.toContainText("这条事件已存入 Hive Inbox");
  await page.screenshot({ path: testInfo.outputPath("queen-disabled-1440.png"), fullPage: true });
  await page.getByRole("link", { name: "去设置页开启 Queen" }).click();
  await page.getByRole("checkbox", { name: /开启 Queen/ }).check();
  await page.getByRole("button", { name: "保存设置" }).click();
  await expect(page.getByText("Hive 设置已保存。", { exact: true })).toBeVisible();
  await page.getByRole("link", { name: "对话", exact: true }).click();
  await expect(page.getByLabel("对 Queen（蜂王）说话")).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: "Queen 状态" })).toContainText("待命");
});

test("Queen status failures are visible and sending failures preserve the draft", async ({ page }) => {
  await mockHiveAPI(page, { failures: { queen: 503, send: 500 } });
  await page.goto("/#/legacy/hives/swarmjet");
  await expect(page.getByRole("alert").first()).toContainText("模拟的接口故障");
  await page.getByLabel("对 Queen（蜂王）说话").fill("保留这条草稿");
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect(page.locator(".hg-composer [role=alert]")).toContainText("模拟的接口故障");
  await expect(page.getByLabel("对 Queen（蜂王）说话")).toHaveValue("保留这条草稿");
});

 test("Queen without a completed turn accepts null lastTurnAt", async ({ page }) => {
  await mockHiveAPI(page);
  await page.goto("/#/legacy/hives/swarmjet");
  const status = await page.evaluate(async () => (await fetch("/api/hives/swarmjet/queen")).json());
  expect(status.lastTurnAt).toBeNull();
  await expect(page.getByLabel("对 Queen（蜂王）说话")).toBeVisible();
 });
