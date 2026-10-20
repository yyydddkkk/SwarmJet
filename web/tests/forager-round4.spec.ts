// Round-4 Forager detail: methodology role, parent, detached revision,
// attestation history, merge readiness and spend.
import { expect, test } from "@playwright/test";
import { mockHiveAPI } from "./hive-api-mock";

test.use({ viewport: { width: 1440, height: 900 } });

test("the header shows the role, parent Agent and checked-out revision", async ({ page }, testInfo) => {
  await mockHiveAPI(page);
  await page.goto("/#/hives/swarmjet/foragers/forager-2b91");

  await expect(page.getByRole("heading", { name: /forager-2b91/ })).toBeVisible();
  await expect(page.locator(".hg-meta").filter({ hasText: "角色" }).first()).toContainText("verifier");
  const parent = page.locator(".hg-meta").filter({ hasText: "父 Agent" });
  await expect(parent).toContainText("forager-7f3a");
  await expect(page.locator(".hg-meta").filter({ hasText: "检出提交" }).first()).toContainText("a41c9de");

  await page.screenshot({ path: testInfo.outputPath("forager-round4-header-1440.png"), fullPage: true });
});

test("the merge tab lists attestations with author flag and superseded state", async ({ page }, testInfo) => {
  await mockHiveAPI(page);
  await page.goto("/#/hives/swarmjet/foragers/forager-7f3a/merge");

  await expect(page.getByRole("heading", { name: "验证与合并", level: 2 })).toBeVisible();
  await expect(page.getByTestId("merge-readiness")).toHaveText("可合并");
  await expect(page.getByText("所有合并条件都已满足。", { exact: true })).toBeVisible();

  const table = page.getByRole("table", { name: "结论历史" });
  await expect(table).toBeVisible();
  const rows = table.locator("tbody tr");
  await expect(rows).toHaveCount(2);

  const current = rows.filter({ hasText: "forager-2b91" });
  await expect(current).toContainText("PASS");
  await expect(current).toContainText("a41c9de");
  await expect(current).toContainText("独立");
  await expect(current).toContainText("有效");
  await expect(current.getByRole("link", { name: "在 GitHub 查看" })).toHaveAttribute(
    "href",
    "https://github.com/yyydddkkk/SwarmJet/pull/88#issuecomment-1",
  );

  const old = rows.filter({ hasText: "forager-7f3a" });
  await expect(old).toContainText("PASS+NOTES");
  await expect(old).toContainText("作者");
  await expect(old).toContainText("已作废");

  await page.screenshot({ path: testInfo.outputPath("forager-merge-1440.png"), fullPage: true });
});

test("a not-ready PR lists the merge policy reasons", async ({ page }) => {
  const state = await mockHiveAPI(page);
  state.prStatuses["swarmjet:88"].ready = false;
  state.prStatuses["swarmjet:88"].reasons = [
    "需要一条来自非作者的 PASS 结论。",
    "integration 检查尚未通过。",
  ];
  await page.goto("/#/hives/swarmjet/foragers/forager-7f3a/merge");

  await expect(page.getByTestId("merge-readiness")).toHaveText("尚不可合并");
  await expect(page.getByText("需要一条来自非作者的 PASS 结论。", { exact: true })).toBeVisible();
  await expect(page.getByText("integration 检查尚未通过。", { exact: true })).toBeVisible();
});

test("the usage tab shows this Agent's spend and model breakdown", async ({ page }, testInfo) => {
  await mockHiveAPI(page);
  await page.goto("/#/hives/swarmjet/foragers/forager-7f3a/usage");

  await expect(page.getByRole("heading", { name: "用量与花费", level: 2 })).toBeVisible();
  await expect(page.getByTestId("agent-usage-cost")).toHaveText("$6.82");
  await expect(page.getByText("1,120,400", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("132,200", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("deepseek/deepseek-chat", { exact: true })).toBeVisible();

  await page.screenshot({ path: testInfo.outputPath("forager-usage-1440.png"), fullPage: true });
});

test("usage asks for today and an Agent without rows never borrows another's", async ({ page }) => {
  const state = await mockHiveAPI(page);
  await page.goto("/#/hives/swarmjet/foragers/forager-5c40/usage");

  await expect(page.getByText("这只 Agent 还没有用量。", { exact: true })).toBeVisible();
  await expect(page.getByTestId("agent-usage-cost")).toHaveCount(0);
  const usage = state.calls.filter(call => call.path === "/api/usage");
  expect(usage.length).toBeGreaterThan(0);
  for (const call of usage) expect(new URLSearchParams(call.query).get("period")).toBe("today");
});

test("the Hive usage tab shows today's total and the Agent ranking", async ({ page }, testInfo) => {
  await mockHiveAPI(page);
  await page.goto("/#/hives/swarmjet/usage");

  await expect(page.getByRole("heading", { name: "今日用量", level: 2 })).toBeVisible();
  await expect(page.getByTestId("hive-usage-total")).toHaveText("$12.47");
  const ranking = page.locator(".hg-usage-ranking .hg-usage-row");
  await expect(ranking).toHaveCount(3);
  await expect(ranking.nth(0)).toContainText("forager-7f3a");
  await expect(ranking.nth(0)).toContainText("$6.82");
  await expect(ranking.nth(0)).toContainText("worker");
  await expect(ranking.nth(1)).toContainText("forager-2b91");
  await expect(ranking.nth(2)).toContainText("queen-swarmjet");

  await page.screenshot({ path: testInfo.outputPath("hive-usage-1440.png"), fullPage: true });
});
