import { expect, test } from "@playwright/test";
import { round8Project } from "../src/fixtures/round8";
import { aggregateIssues, aggregatePRs, agentSummary, historyStars, localizedReason, shortAgentId, shortSha, shouldCollapseLongText, uniqueLongTexts, weightedTruncate } from "../src/data/rules";
import { mockStarsHTTP } from "./stars-http-mock";
import { createMockDataSource } from "../src/data/mock";
import { createProjectStore } from "../src/data/store";
import { APIError } from "../src/data/api";
import type { MessagePage } from "../src/data/http";

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
  const stars = historyStars(data.agents, data.prs, now, () => false, { x: 0, y: 0 }, data.signals);
  expect(stars.length).toBeGreaterThan(30);
  expect(stars.some((star) => data.agents.find((agent) => agent.id === star.id)?.roleName === "verifier")).toBe(false);
  expect(stars.find((star) => star.id === data.agents[4].id)?.summary).toContain("Issue #104");
  expect(historyStars(data.agents, data.prs, now, () => false, { x: 0, y: 0 }, data.signals)).toEqual(stars);
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

test("PR envelope stale and unavailable flags reach the resource state", async () => {
  const fixture = round8Project();
  const mock = createMockDataSource({ swarmjet: fixture });
  const store = createProjectStore("swarmjet", {
    ...mock.source,
    async load(_id, update) {
      update("hive", { hive: fixture.hive, localAddress: fixture.localAddress });
      update("prs", { prs: fixture.prs }, undefined, { stale: true, unavailable: true });
    },
  });
  store.start();
  await new Promise((resolve) => setImmediate(resolve));
  expect(store.getSnapshot().resources.prs).toMatchObject({ status: "ready", stale: true, unavailable: true });
  store.stop();
});

test("partial 401 remains an authentication failure", async () => {
  const fixture = round8Project();
  const mock = createMockDataSource({ swarmjet: fixture });
  const store = createProjectStore("swarmjet", {
    ...mock.source,
    async load(_id, update) {
      update("hive", { hive: fixture.hive });
      update("prs", undefined, new APIError("session expired", 401));
    },
  });
  store.start();
  await new Promise((resolve) => setImmediate(resolve));
  expect(store.getSnapshot().errorStatus).toBe(401);
  expect(store.getSnapshot().resources.prs.status).toBe("error");
  store.stop();
});

test("active inboxes loaded at startup are not requested again for agent events", async () => {
  const fixture = round8Project();
  const mock = createMockDataSource({ swarmjet: fixture });
  const store = createProjectStore("swarmjet", mock.source);
  store.start();
  await new Promise((resolve) => setImmediate(resolve));
  mock.push("swarmjet", { type: "agent", data: fixture.agents[0] });
  await new Promise((resolve) => setImmediate(resolve));
  expect(mock.calls.filter((call) => call.method === "listMessages")).toHaveLength(0);
  store.stop();
});

test("an opened historical inbox preserves SSE messages arriving during pagination", async () => {
  const fixture = round8Project();
  let complete!: (value: MessagePage) => void;
  const mock = createMockDataSource({ swarmjet: fixture }, {
    listMessages: async () => new Promise((resolve) => { complete = resolve; }),
  });
  const store = createProjectStore("swarmjet", mock.source);
  store.start();
  await new Promise((resolve) => setImmediate(resolve));
  const historical = fixture.agents[4].id;
  const pending = store.loadAgentMessages(historical);
  const incoming = { id: "live-while-loading", hiveId: "swarmjet", from: "queen", to: `forager:${historical}`, kind: "report" as const, trust: "agent", chainId: "", createdAt: new Date().toISOString(), preview: "实时进展" };
  mock.push("swarmjet", { type: "message", data: incoming });
  complete({ messages: [], truncated: false, to: incoming.to });
  await pending;
  expect(store.getSnapshot().snapshot?.messages.some((message) => message.id === incoming.id)).toBe(true);
  store.stop();
});
