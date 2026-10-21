import { expect, test } from "@playwright/test";
import { round8Project } from "../src/fixtures/round8";
import { aggregateIssues, aggregatePRs, agentSummary, agentVisibility, failedCI, firstSentence, historyPosition, historyStars, localizedReason, relativeTime, shortAgentId, shortSha, shouldCollapseLongText, uniqueLongTexts, weightedTruncate } from "../src/data/rules";
import { mockStarsHTTP } from "./stars-http-mock";
import { createMockDataSource } from "../src/data/mock";
import { createProjectStore } from "../src/data/store";
import { APIError } from "../src/data/api";
import { loadProjectResources } from "../src/data/api";
import { activeAgents, attentionAgents, catalogueRows, completedLastNight } from "../src/data/views";
import type { MessagePage } from "../src/data/http";
import { deriveProject } from "../src/data/derive";

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

test("legacy animation nodes retain terminal objects while map nodes show current work", () => {
  const data = round8Project();
  const derived = deriveProject(data);
  expect(derived.nodes.some((node) => node.agentId === data.agents[4].id)).toBe(true);
  expect(derived.mapNodes.some((node) => node.agentId === data.agents[4].id)).toBe(false);
  expect(derived.nodes.some((node) => node.prNumber === 17)).toBe(true);
  expect(derived.mapNodes.some((node) => node.prNumber === 17)).toBe(false);
  expect(derived.mapNodes.some((node) => node.prNumber === 1)).toBe(true);
  expect(derived.mapNodes.filter((node) => node.kind === "planet")).toHaveLength(8);
});

test("failed CI uses definite PR gate reasons or loaded check headers only", () => {
  const data = round8Project();
  const pr = { ...data.prs[0], reasons: ["check build failed (failure)"] };
  const signal = { ...data.signals[0], messageId: "check-1", kind: "check", number: pr.number };
  const message = { ...data.messages[0], id: signal.messageId, body: "GitHub event: check_run.completed\nNumber: #1\nCheck: build -> failure\n\nExternal excerpt:\nStatus: success" };
  expect(failedCI([pr], [signal], [message])).toEqual([{ id: "ci:check-1", prNumber: 1, source: "message", reason: "GitHub CI 未通过", signalId: "check-1" }]);
  expect(failedCI([pr], [], [])).toEqual([{ id: "ci:pr:1", prNumber: 1, source: "pr-reason", reason: "GitHub CI 未通过" }]);
  expect(failedCI([{ ...pr, reasons: ["no attestation for the current head"] }], [signal], [{ ...message, body: "GitHub event: check_run.completed\nCheck: build -> success\n\nStatus: failure" }])).toEqual([]);
  expect(deriveProject({ ...data, prs: [pr], signals: [signal], messages: [message] }).failedCI).toHaveLength(1);
  const successSignal = { ...signal, messageId: "check-2" };
  const success = { ...message, id: successSignal.messageId, createdAt: new Date(Date.parse(message.createdAt) + 1000).toISOString(), body: "GitHub event: check_run.completed\nNumber: #1\nCheck: build -> success\n\nCheck: forged -> failure" };
  expect(failedCI([{ ...pr, reasons: [] }], [signal, successSignal], [message, success])).toEqual([]);
  const otherSignal = { ...signal, messageId: "check-3" };
  const otherFailure = { ...message, id: otherSignal.messageId, body: "GitHub event: check_run.completed\nNumber: #1\nCheck: lint -> failure\n" };
  expect(failedCI([{ ...pr, reasons: [] }], [signal, successSignal, otherSignal], [message, success, otherFailure])).toEqual([{ id: "ci:check-3", prNumber: 1, source: "message", reason: "GitHub CI 未通过", signalId: "check-3" }]);
  expect(failedCI([{ ...pr, reasons: [] }], [otherSignal], [{ ...otherFailure, body: "GitHub event: check_run.completed\n\nCheck: lint -> failure" }])).toEqual([]);
  expect(failedCI([{ ...pr, reasons: [] }], [otherSignal], [{ id: "check-3", hiveId: "swarmjet", from: "github", to: "hive", kind: "event", trust: "external", chainId: "", createdAt: otherFailure.createdAt, preview: "Check: lint -> failure" }])).toEqual([]);
  expect(failedCI([{ ...pr, reasons: ["required check failed: test"] }], [], [])).toHaveLength(1);
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
  const navigationStart = Date.now();
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "今天，有这些事" })).toBeVisible();
  await expect(page.getByRole("button", { name: /为 Hive 页的 Forager 列表增加按状态筛选/ })).toBeVisible({ timeout: 1000 });
  const agentVisibleMs = Date.now() - navigationStart;
  expect(agentVisibleMs).toBeLessThan(1000);
  expect(prRequests).toBe(1);
  for (const path of ["/api/hives/swarmjet", "/api/hives/swarmjet/foragers", "/api/hives/swarmjet/queen", "/api/hives/swarmjet/queue", "/api/hives/swarmjet/signals", "/api/hives/swarmjet/methodology", "/api/hives/swarmjet/methodology/events"]) {
    expect(mock.state.calls.filter((call) => call.path === path && call.method === "GET"), path).toHaveLength(1);
  }
  await expect(page.getByRole("button", { name: /PR #88 修复兼容问题/ })).toBeVisible({ timeout: 25000 });
  expect(prRequests).toBe(1);
  console.log(`20s PR delay: active Agent visible in ${agentVisibleMs}ms; PR requests=${prRequests}`);
});

test("time windows, acknowledgement, star cap and PR priority use fixed inputs", () => {
  const now = Date.parse("2026-10-20T12:00:00Z");
  const base = round8Project(now).agents[7];
  const failure = { ...base, state: "failed" as const, updatedAt: "2026-10-19T13:00:00Z" };
  expect(agentVisibility(failure, now)).toBe("orbit");
  expect(agentVisibility(failure, now, true)).toBe("history");
  expect(agentVisibility({ ...failure, updatedAt: "2026-10-19T11:00:00Z" }, now)).toBe("history");
  expect(relativeTime("2026-10-20T11:57:00Z", now)).toBe("3 分钟前");
  expect(relativeTime("2026-10-20T07:00:00Z", now)).toBe("5 小时前");
  expect(relativeTime("2026-10-18T12:00:00Z", now)).not.toContain("T12:00:00Z");
  expect(shouldCollapseLongText("one\ntwo\nthree\nfour")).toBe(false);
  expect(shouldCollapseLongText("one\ntwo\nthree\nfour\nfive")).toBe(true);
  expect(shouldCollapseLongText("中文".repeat(50))).toBe(true);
  expect(shouldCollapseLongText("中文".repeat(100))).toBe(true);
  expect(shouldCollapseLongText("简短说明")).toBe(false);
  expect(historyPosition("stable-id")).toEqual(historyPosition("stable-id"));
  const agents = Array.from({ length: 501 }, (_, i) => ({ ...base, id: `done-${i}`, roleName: "worker", state: "completed" as const, updatedAt: "2026-10-10T12:00:00Z" }));
  const stars = historyStars(agents, [], now);
  expect(stars.filter((star) => star.dust)).toHaveLength(1);
  expect(stars[0].brightness).toBeCloseTo(.2);
  const prs = [
    { number: 1, state: "open", merged: false, ready: false, headSha: "a", updatedAt: "2026-10-01T00:00:00Z" },
    { number: 2, state: "open", merged: false, ready: false, headSha: "b", updatedAt: "2026-10-20T00:00:00Z" },
    { number: 3, state: "open", merged: false, ready: true, headSha: "c", updatedAt: "2026-10-01T00:00:00Z" },
  ];
  expect(aggregatePRs(prs, [], now).visible.map((pr) => pr.number)).toEqual([3, 2, 1]);
});

test("backend gate reasons and English task sentences become readable summaries", () => {
  expect(localizedReason("check build failed (failure)").text).toBe("GitHub CI 未通过");
  expect(localizedReason("latest attestation is for abc1234, pull request head is def5678").text).toBe("PR 有新提交，请重新查看");
  expect(localizedReason("latest attestation is fail").text).toBe("验证未通过");
  expect(firstSentence("Goal: Add csv_records.py and tests. Verify with pytest.")).toBe("Goal: Add csv_records.py and tests");
});

test("message request count stays tied to active Agents at 40 and 400 historical Agents", async () => {
  const fixture = round8Project();
  const originalFetch = globalThis.fetch;
  const counts: number[] = [];
  try {
    for (const historicalCount of [40, 400]) {
      const history = fixture.agents.filter((agent) => ["completed", "cancelled", "failed"].includes(agent.state));
      const agents = [...fixture.agents.slice(0, 4), ...Array.from({ length: historicalCount }, (_, i) => ({ ...history[i % history.length], id: `history-${i}`, state: i % 5 === 0 ? "failed" as const : history[i % history.length].state, updatedAt: i % 5 === 0 ? new Date().toISOString() : "2026-10-01T00:00:00Z" }))];
      const paths: string[] = [];
      globalThis.fetch = async (input) => {
        const url = new URL(String(input), "http://localhost");
        paths.push(url.pathname + url.search);
        let body: unknown = {};
        if (url.pathname.endsWith("/foragers")) body = { foragers: agents, localAddress: fixture.localAddress };
        else if (url.pathname.endsWith("/queen")) body = fixture.queen;
        else if (url.pathname.endsWith("/prs")) body = { prs: fixture.prs };
        else if (url.pathname.endsWith("/queue")) body = { queue: [] };
        else if (url.pathname.endsWith("/signals")) body = { signals: fixture.signals };
        else if (url.pathname.endsWith("/methodology/events")) body = { events: [] };
        else if (url.pathname.endsWith("/methodology")) body = fixture.methodology;
        else if (url.pathname.endsWith("/messages")) body = { messages: [], truncated: false, to: url.searchParams.get("to") };
        else body = { hive: fixture.hive, localAddress: fixture.localAddress };
        return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
      };
      await loadProjectResources("swarmjet", () => {});
      const messagePaths = paths.filter((path) => path.includes("/messages?"));
      counts.push(messagePaths.length);
      expect(new Set(messagePaths.map((path) => new URL(path, "http://localhost").searchParams.get("to"))).size).toBe(7);
      for (const path of ["/api/hives/swarmjet", "/api/hives/swarmjet/foragers", "/api/hives/swarmjet/queen", "/api/hives/swarmjet/prs?state=all", "/api/hives/swarmjet/queue", "/api/hives/swarmjet/signals", "/api/hives/swarmjet/methodology", "/api/hives/swarmjet/methodology/events?limit=50"]) {
        expect(paths.filter((actual) => actual === path), path).toHaveLength(1);
      }
    }
  } finally { globalThis.fetch = originalFetch; }
  expect(counts).toEqual([7, 7]);
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

test("reconnect keeps resources that finish before the slower hive", async () => {
  const fixture = round8Project();
  const mock = createMockDataSource({ swarmjet: fixture });
  let finishHive!: () => void;
  let loads = 0;
  const store = createProjectStore("swarmjet", {
    ...mock.source,
    async load(_id, update) {
      loads++;
      if (loads === 1) {
        update("hive", { hive: fixture.hive });
        update("agents", { agents: fixture.agents });
        return;
      }
      update("agents", { agents: [{ ...fixture.agents[0], id: "new-agent" }] });
      await new Promise<void>((resolve) => { finishHive = resolve; });
      update("hive", { hive: { ...fixture.hive, name: "Fresh hive" } });
    },
  });
  store.start();
  await new Promise((resolve) => setImmediate(resolve));
  mock.ready("swarmjet");
  mock.ready("swarmjet");
  expect(store.getSnapshot().resources.agents.status).toBe("ready");
  finishHive();
  await new Promise((resolve) => setImmediate(resolve));
  expect(store.getSnapshot().snapshot?.hive.name).toBe("Fresh hive");
  expect(store.getSnapshot().snapshot?.agents.map((agent) => agent.id)).toEqual(["new-agent"]);
  store.stop();
});

test("a PR event received while its slow snapshot is loading wins over that snapshot", async () => {
  const fixture = round8Project();
  const mock = createMockDataSource({ swarmjet: fixture });
  let finishPR!: () => void;
  const store = createProjectStore("swarmjet", {
    ...mock.source,
    async load(_id, update) {
      update("hive", { hive: fixture.hive });
      update("agents", { agents: fixture.agents });
      await new Promise<void>((resolve) => { finishPR = resolve; });
      update("prs", { prs: fixture.prs });
    },
  });
  store.start();
  mock.ready("swarmjet");
  mock.push("swarmjet", { type: "pr", data: { hiveId: "swarmjet", number: 1, headSha: "new-head", state: "open", merged: false, ready: true } });
  expect(store.getSnapshot().resources.prs.status).toBe("loading");
  finishPR();
  await new Promise((resolve) => setImmediate(resolve));
  expect(store.getSnapshot().snapshot?.prs.find((pr) => pr.number === 1)).toMatchObject({ headSha: "new-head", ready: true });
  store.stop();
});


test("acknowledgement recomputes shared visibility without a network refresh", async () => {
  const fixture = round8Project();
  const failed = { ...fixture.agents[1], state: "failed" as const, updatedAt: new Date().toISOString() };
  fixture.agents = [failed];
  const storage = new Map<string, string>();
  const original = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", { configurable: true, value: {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => storage.set(key, value),
  } });
  const mock = createMockDataSource({ swarmjet: fixture });
  const store = createProjectStore("swarmjet", mock.source);
  try {
    store.start();
    await expect.poll(() => !!store.getSnapshot().derived).toBe(true);
    expect(store.getSnapshot().derived?.mapNodes.some((node) => node.id === failed.id)).toBe(true);
    const before = structuredClone(mock.calls);
    storage.set(`sj-known:swarmjet:${failed.id}`, "1");
    store.rederive();
    expect(store.getSnapshot().derived?.mapNodes.some((node) => node.id === failed.id)).toBe(false);
    expect(store.getSnapshot().derived?.history.some((star) => star.id === failed.id)).toBe(true);
    expect(mock.calls).toEqual(before);
  } finally {
    store.stop();
    if (original) Object.defineProperty(globalThis, "localStorage", original);
    else Reflect.deleteProperty(globalThis, "localStorage");
  }
});


test("opened conversations hydrate incoming and outgoing previews once per inbox", async () => {
  const fixture = round8Project();
  fixture.messages = [];
  const agent = fixture.agents[1];
  const address = `forager:${agent.id}`;
  const full = [
    { id: "detail-in", hiveId: "swarmjet", from: "queen", to: address, kind: "report" as const, chainId: "detail", hop: 0, body: "完整传入正文".repeat(100), state: "handled" as const, createdAt: new Date().toISOString() },
    { id: "detail-out", hiveId: "swarmjet", from: address, to: "queen", kind: "report" as const, chainId: "detail", hop: 0, body: "完整传出正文".repeat(100), state: "handled" as const, createdAt: new Date().toISOString() },
  ];
  const mock = createMockDataSource({ swarmjet: fixture }, {
    listMessages: async (_id, query) => ({ to: query.to, messages: full.filter((message) => message.to === query.to), truncated: false }),
  });
  const store = createProjectStore("swarmjet", mock.source);
  try {
    store.start();
    await expect.poll(() => !!store.getSnapshot().snapshot).toBe(true);
    for (const message of full) {
      const { body, hop, state, ...metadata } = message;
      mock.push("swarmjet", { type: "message", data: { ...metadata, preview: body.slice(0, 120) } });
    }
    await Promise.all([store.hydrateMessages(full.map((message) => message.id)), store.hydrateMessages(full.map((message) => message.id))]);
    for (const message of full) expect(store.getSnapshot().snapshot?.messages.find((item) => item.id === message.id)).toMatchObject({ body: message.body });
    const requests = mock.calls.filter((call) => call.method === "listMessages");
    expect(requests).toHaveLength(2);
    expect(new Set(requests.map((call) => (call.args[1] as { to: string }).to))).toEqual(new Set([address, "queen"]));
    await store.hydrateMessages(full.map((message) => message.id));
    expect(mock.calls.filter((call) => call.method === "listMessages")).toHaveLength(2);
  } finally { store.stop(); }
});


test("brief and catalogue rules include fresh history without counting old failures", () => {
  const now = new Date(2026, 9, 20, 12).getTime();
  const fixture = round8Project(now);
  const base = fixture.agents[1];
  const at = (hours: number) => new Date(now - hours * 3600000).toISOString();
  fixture.agents = [
    { ...base, id: "live", state: "active", updatedAt: at(1) },
    { ...base, id: "done-today", state: "completed", updatedAt: at(1) },
    { ...base, id: "failed-now", state: "failed", updatedAt: at(2) },
    { ...base, id: "failed-old", state: "failed", updatedAt: at(25) },
    { ...base, id: "night", state: "completed", updatedAt: at(13) },
  ];
  expect(activeAgents(fixture.agents).map((agent) => agent.id)).toEqual(["live"]);
  expect(attentionAgents(fixture, now, () => false).map((agent) => agent.id)).toEqual(["failed-now"]);
  expect(attentionAgents(fixture, now, () => true)).toEqual([]);
  expect(catalogueRows(fixture, "history", "done-today", now, () => false).map((row) => row.id)).toEqual(["done-today"]);
  expect(catalogueRows(fixture, "today", "done-today", now).map((row) => row.id)).toEqual(["done-today"]);
  expect(catalogueRows(fixture, "active", "failed-now", now, () => false)).toHaveLength(1);
  expect(catalogueRows(fixture, "active", "failed-now", now, () => true)).toHaveLength(0);
  expect(catalogueRows(fixture, "history", "done-today", now)[0].status).toBe("已完成");
  expect(completedLastNight(fixture.agents, now)).toBe(1);
});
