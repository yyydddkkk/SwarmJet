import { expect, test } from "@playwright/test";
import { round8Project } from "../src/fixtures/round8";
import { aggregateIssues, aggregatePRs, agentSummary, historyStars, localizedReason, shortAgentId, shortSha, shouldCollapseLongText, uniqueLongTexts, weightedTruncate } from "../src/data/rules";
import { mockStarsHTTP } from "./stars-http-mock";

test("realistic fixture exercises density and text rules", () => {
  const data = round8Project();
  expect(data.agents.filter((agent) => ["completed", "cancelled", "failed"].includes(agent.state)).length).toBeGreaterThan(40);
  expect(data.prs.filter((pr) => !pr.merged).length).toBeGreaterThan(14);
  expect(data.signals.length).toBeGreaterThan(11);
  expect(data.messages).toHaveLength(900);
  expect(data.agents[0].task!.length).toBeGreaterThan(3000);
  expect(shouldCollapseLongText(data.agents[0].task!)).toBe(true);
  expect(agentSummary(data.agents[0], { issues: data.signals })).toContain("Issue #100");
  expect(weightedTruncate("修复abc修复abc", 3)).toBe("修复a…");
  expect(shortSha(data.prs[0].headSha)).toHaveLength(7);
  expect(shortAgentId(data.agents[0].id)).toHaveLength(8);
  expect(localizedReason(data.prs[1].reasons![0]).text).toBe("当前提交还没有验证结论");
  expect(localizedReason("something unknown")).toEqual({ text: "其他原因", detail: "something unknown" });
  expect(uniqueLongTexts(["one  two", "one two", "three"])).toEqual(["one  two", "three"]);
  expect(aggregatePRs(data.prs, data.agents).visible.length).toBe(8);
  expect(aggregatePRs(data.prs, data.agents).cluster.length).toBe(8);
  expect(aggregateIssues(data.signals, data.agents).visible.length).toBe(5);
  const now = Date.now();
  const stars = historyStars(data.agents, data.prs, now);
  expect(stars.length).toBeGreaterThan(30);
  expect(historyStars(data.agents, data.prs, now)).toEqual(stars);
});

test("a 20 second PR request does not hold the other briefing sections", async ({ page }) => {
  test.setTimeout(35000);
  const mock = await mockStarsHTTP(page);
  let prRequests = 0;
  await page.route("**/api/hives/swarmjet/prs?state=all", async (route) => {
    prRequests++;
    await new Promise((resolve) => setTimeout(resolve, 20000));
    await route.fulfill({ contentType: "application/json", body: JSON.stringify({ prs: mock.prs }) });
  });
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "今天，有这些事" })).toBeVisible();
  const began = Date.now();
  await expect(page.getByRole("heading", { name: "正在进行" })).toBeVisible({ timeout: 1000 });
  await expect(page.getByText("当前没有工作中的 Agent。", { exact: true })).toHaveCount(0, { timeout: 1000 });
  expect(Date.now() - began).toBeLessThan(1000);
  expect(prRequests).toBe(1);
  await expect(page.getByRole("button", { name: /PR #88 修复兼容问题/ })).toBeVisible({ timeout: 25000 });
  expect(prRequests).toBe(1);
});
