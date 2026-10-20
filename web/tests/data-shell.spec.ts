import { expect, test } from "@playwright/test";
import { createProjectStore, type DataSource } from "../src/data/store";
import { createMockDataSource } from "../src/data/mock";
import { deriveProject } from "../src/data/derive";
import { APIError } from "../src/data/api";
import { hives, hiveAgents } from "../src/fixtures/hive-console";
import { methodologyResponses } from "../src/fixtures/round4";
import type { ProjectSnapshot, MessagePreview } from "../src/data/types";
import { mockHiveAPI } from "./hive-api-mock";
const fixture = (): ProjectSnapshot => ({
  hive: structuredClone(hives[0]),
  localAddress: "local:web-admin",
  agents: structuredClone(
    hiveAgents.swarmjet.filter((a) => a.role === "forager"),
  ),
  queen: {
    enabled: true,
    state: "active",
    model: "test/model",
    agentId: "queen",
    lastTurnAt: null,
  },
  prs: [
    {
      number: 1,
      title: "Actual title",
      headSha: "head-1",
      ready: false,
      merged: false,
      state: "open",
      ownerId: "owner",
      attestations: [],
    },
  ],
  queue: [],
  signals: [],
  methodology: structuredClone(methodologyResponses[3]),
  methodologyEvents: [],
  messages: [],
});
const message = (id = "message-1"): MessagePreview => ({
  id,
  hiveId: "swarmjet",
  from: "queen",
  to: "local:web-admin",
  kind: "question",
  chainId: id,
  createdAt: "2026-10-20T12:00:00Z",
  preview: "请决定",
});
const flush = () => new Promise<void>((resolve) => setImmediate(resolve));
test("snapshot and SSE increments preserve canonical fields and produce unique pulses", async () => {
  const initial = fixture(),
    canonical = { ...initial.prs[0], headSha: "head-2", ready: true };
  const mock = createMockDataSource(
    { swarmjet: initial },
    { listPRs: async () => ({ prs: [canonical] }) },
  );
  const store = createProjectStore("swarmjet", mock.source);
  store.start();
  await flush();
  mock.ready("swarmjet");
  await flush();
  mock.push("swarmjet", { type: "message", data: message() });
  mock.push("swarmjet", { type: "message", data: message() });
  mock.push("swarmjet", {
    type: "pr",
    data: {
      hiveId: "swarmjet",
      number: 1,
      headSha: "head-2",
      ready: true,
      merged: false,
      state: "open",
    },
  });
  mock.push("swarmjet", {
    type: "agent",
    data: {
      ...initial.agents[0],
      id: "verifier-new",
      role: "forager",
      roleName: "verifier",
      prNumber: 1,
    },
  });
  const q = {
    agentId: "queued",
    role: "worker",
    task: "task",
    position: 1,
    createdAt: "2026-10-20T00:00:00Z",
    expiresAt: "2026-10-21T00:00:00Z",
  };
  mock.push("swarmjet", { type: "queue", data: { ...q, state: "waiting" } });
  expect(store.getSnapshot().snapshot?.queue).toHaveLength(1);
  mock.push("swarmjet", { type: "queue", data: { ...q, state: "started" } });
  mock.push("swarmjet", {
    type: "hive",
    data: { ...initial.hive, name: "Renamed" },
  });
  mock.push("swarmjet", {
    type: "methodology",
    data: {
      version: 2,
      action: "activate",
      actor: "admin",
      note: "switch",
      createdAt: "2026-10-20T00:00:00Z",
    },
  });
  await flush();
  expect(store.getSnapshot().snapshot?.hive.name).toBe("Renamed");
  expect(store.getSnapshot().snapshot?.prs[0]).toMatchObject(canonical);
  expect(store.getSnapshot().snapshot?.messages).toHaveLength(1);
  expect(store.getSnapshot().pulses).toHaveLength(1);
  expect(
    store.getSnapshot().derived?.nodes.find((n) => n.id === "verifier-new"),
  ).toMatchObject({ kind: "satellite", parentId: "swarmjet:pr:1" });
  expect(store.getSnapshot().snapshot?.methodologyEvents).toHaveLength(1);
  expect(store.getSnapshot().snapshot?.queue).toHaveLength(0);
  store.stop();
  expect(mock.connections).toBe(0);
});
test("ready reload buffers increments even if the snapshot includes the same message", async () => {
  const mock = createMockDataSource({ swarmjet: fixture() });
  let resolve!: (s: ProjectSnapshot) => void;
  let count = 0;
  const source: DataSource = {
    ...mock.source,
    snapshot: async () =>
      ++count === 1
        ? fixture()
        : new Promise((r) => {
            resolve = r;
          }),
  };
  const store = createProjectStore("swarmjet", source);
  store.start();
  await flush();
  mock.ready("swarmjet");
  const event = message("during");
  mock.push("swarmjet", { type: "message", data: event });
  const next = fixture();
  next.hive.name = "Fresh";
  next.messages = [event];
  resolve(next);
  await flush();
  expect(count).toBe(2);
  expect(store.getSnapshot().snapshot?.hive.name).toBe("Fresh");
  expect(store.getSnapshot().snapshot?.messages).toHaveLength(1);
  expect(store.getSnapshot().pulses).toHaveLength(1);
  expect(store.getSnapshot().connection).toBe("connected");
  store.stop();
});
test("late canonical responses cannot overwrite a newer methodology activation", async () => {
  const initial = fixture();
  let old!: (v: typeof initial.methodology) => void;
  let count = 0;
  const mock = createMockDataSource(
    { swarmjet: initial },
    {
      getMethodology: async () =>
        ++count === 1
          ? new Promise((r) => {
              old = r;
            })
          : { ...initial.methodology, version: 3 },
    },
  );
  const store = createProjectStore("swarmjet", mock.source);
  store.start();
  await flush();
  const data = {
    version: 2,
    action: "activate",
    actor: "system",
    note: "",
    createdAt: "2026-10-20T00:00:00Z",
  };
  mock.push("swarmjet", { type: "methodology", data });
  mock.push("swarmjet", { type: "methodology", data: { ...data, version: 3 } });
  await flush();
  expect(store.getSnapshot().snapshot?.methodology.version).toBe(3);
  old({ ...initial.methodology, version: 2 });
  await flush();
  expect(store.getSnapshot().snapshot?.methodology.version).toBe(3);
  store.stop();
});
test("409 merges retain observed head and refresh with actionable text", async () => {
  const mock = createMockDataSource(
    { swarmjet: fixture() },
    {
      mergePR: async (_id, number, head) => {
        expect(number).toBe(1);
        expect(head).toBe("observed-head");
        throw new APIError("changed", 409);
      },
    },
  );
  const store = createProjectStore("swarmjet", mock.source);
  store.start();
  await flush();
  await expect(store.mergePR(1, "observed-head")).rejects.toThrow(
    "PR 有新提交，请重新查看",
  );
  expect(mock.calls.filter((c) => c.method === "snapshot")).toHaveLength(2);
  expect(store.getSnapshot().error).toBe("PR 有新提交，请重新查看");
  store.stop();
});
test("decisions match current administrator and nonempty reply chains", () => {
  const s = fixture();
  s.methodology.manifest.merge.auto = false;
  s.prs[0].ready = true;
  s.messages = [message(), { ...message("other"), to: "local:other" }];
  expect(deriveProject(s).decisions).toHaveLength(2);
  s.messages.push({
    ...message("reply"),
    from: s.localAddress,
    to: "queen",
    kind: "reply",
    chainId: "message-1",
  });
  expect(deriveProject(s).decisions).toHaveLength(1);
  s.messages.push(
    { ...message("empty"), chainId: "" },
    {
      ...message("empty-reply"),
      chainId: "",
      from: s.localAddress,
      to: "queen",
      kind: "reply",
    },
  );
  expect(deriveProject(s).decisions).toHaveLength(2);
});
test("retry doubles to thirty seconds and stops cleanly", async ({ page }) => {
  await page.route("**/api/**", (r) =>
    r.fulfill({ status: 401, contentType: "application/json", body: "{}" }),
  );
  await page.goto("/");
  await page.clock.install({ time: new Date("2030-01-01T00:00:00Z") });
  await page.clock.pauseAt(new Date("2030-01-01T00:00:01Z"));
  await page.evaluate(async () => {
    const path = "/src/data/store.ts";
    const { createProjectStore } = await import(path);
    let fail = () => {};
    let connects = 0;
    const source = {
      api: {},
      snapshot: () => new Promise(() => {}),
      connect: (
        _id: string,
        _ready: () => void,
        _event: () => void,
        error: () => void,
      ) => {
        fail = error;
        connects++;
        return { close() {} };
      },
    };
    const store = createProjectStore("x", source);
    store.start();
    (window as any).__retry = {
      store,
      get connects() {
        return connects;
      },
      fail: () => fail(),
    };
  });
  for (const delay of [1000, 2000, 4000, 8000, 16000, 30000, 30000]) {
    await page.evaluate(() => (window as any).__retry.fail());
    expect(
      await page.evaluate(
        () => (window as any).__retry.store.getSnapshot().retryDelay,
      ),
    ).toBe(delay);
    const before = await page.evaluate(() => (window as any).__retry.connects);
    await page.clock.runFor(delay - 1);
    expect(await page.evaluate(() => (window as any).__retry.connects)).toBe(
      before,
    );
    await page.clock.runFor(1);
    expect(await page.evaluate(() => (window as any).__retry.connects)).toBe(
      before + 1,
    );
  }
  await page.evaluate(() => (window as any).__retry.store.stop());
});
test("theme CSS and choices survive reload and denied storage", async ({
  page,
}) => {
  await mockHiveAPI(page);
  await page.goto("/");
  await page.getByRole("button", { name: "显示", exact: true }).click();
  await page.getByLabel("主题", { exact: true }).selectOption("night");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "night");
  expect(
    await page.evaluate(() =>
      document.documentElement.style.getPropertyValue("--paper"),
    ),
  ).toBe("#0A1020");
  await page.getByLabel("风格", { exact: true }).selectOption("simple");
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "night");
  await expect(page.locator("html")).toHaveAttribute("data-style", "simple");
  await page.addInitScript(() => {
    Storage.prototype.getItem = () => {
      throw new Error("denied");
    };
    Storage.prototype.setItem = () => {
      throw new Error("denied");
    };
  });
  await page.reload();
  await page.getByRole("button", { name: "显示", exact: true }).click();
  await page.getByLabel("主题", { exact: true }).selectOption("paper");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "paper");
});

test("corrupted theme names fall back to a usable theme", async ({ page }) => {
  await mockHiveAPI(page);
  await page.addInitScript(() => {
    localStorage.setItem("sj-theme", "constructor");
  });
  await page.goto("/");
  await page.getByRole("button", { name: "显示", exact: true }).click();
  await expect(page.locator("html")).toHaveAttribute(
    "data-theme",
    /bright|night/,
  );
  await page.getByLabel("主题", { exact: true }).selectOption("paper");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "paper");
});

test("a ready snapshot supersedes late signal fetches and stopped stores stay unchanged", async () => {
  const initial = fixture();
  let resolve!: (value: { signals: ProjectSnapshot["signals"] }) => void;
  const mock = createMockDataSource(
    { swarmjet: initial },
    {
      listSignals: async () =>
        new Promise((r) => {
          resolve = r;
        }),
    },
  );
  const store = createProjectStore("swarmjet", mock.source);
  store.start();
  await flush();
  mock.push("swarmjet", {
    type: "message",
    data: { ...message("external"), trust: "external" },
  });
  const next = fixture();
  next.signals = [
    {
      messageId: "canonical",
      source: "github",
      kind: "issue",
      number: 9,
      title: "real issue",
      url: "https://example.com/issue/9",
      claimed: false,
      claimedBy: "",
      createdAt: "2026-10-20T00:00:00Z",
    },
  ];
  mock.setSnapshot("swarmjet", next);
  mock.ready("swarmjet");
  await flush();
  resolve({ signals: [] });
  await flush();
  expect(store.getSnapshot().snapshot?.signals).toEqual(next.signals);
  store.stop();
  const revision = store.getSnapshot().revision;
  await flush();
  expect(store.getSnapshot().revision).toBe(revision);
});

test("default shell handles session expiry and opens GitHub callbacks in platform setup", async ({
  page,
}) => {
  await mockHiveAPI(page);
  await page.goto("/#/setup?github=connected");
  await expect(page.getByRole("heading", { name: "平台设置" })).toBeVisible();
  await page.evaluate(() =>
    window.dispatchEvent(new Event("swarmjet:session-expired")),
  );
  await expect(
    page.getByRole("heading", { name: "登录 SwarmJet" }),
  ).toBeVisible();
});
