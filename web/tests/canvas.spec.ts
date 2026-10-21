import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { writeFileSync } from "node:fs";
async function mount(page: Page, large = false, multiple = false, realisticFixture = false) {
  page.on("pageerror", (e) => console.log("CANVAS_ERROR", e.message));
  await page.route("**/api/**", (r) =>
    r.fulfill({ status: 401, json: { error: "test session" } }),
  );
  await page.goto("/");
  await page.evaluate(
    async ({ large, multiple, realisticFixture }) => {
      const load = (p: string) => import(/* @vite-ignore */ p);
      const [R, DOM, data, UI, Canvas, Timeline, f, m, round8] = await Promise.all([
        load("/node_modules/.vite/deps/react.js"),
        load("/node_modules/.vite/deps/react-dom_client.js"),
        load("/src/data/index.ts"),
        load("/src/stars/ui.ts"),
        load("/src/stars/CanvasSlot.tsx"),
        load("/src/stars/TimelineSlot.tsx"),
        load("/src/fixtures/hive-console.ts"),
        load("/src/fixtures/round4.ts"),
        load("/src/fixtures/round8.ts"),
      ]);
      document.getElementById("root")!.style.display = "none";
      const el = document.createElement("main");
      el.className = "stars-app";
      document.body.append(el);
      const hive = structuredClone(f.hives[0]),
        agent = {
          ...f.hiveAgents.swarmjet.find((a: any) => a.role === "forager"),
          hiveId: hive.id,
          roleName: "worker",
          state: "active",
        };
      const realistic = round8.round8Project();
      const snapshot = realisticFixture ? realistic : {
        hive,
        localAddress: "local:web-admin",
        agents: large
          ? [...Array.from({ length: 300 }, (_, i) => ({
              ...agent,
              id: `agent-${i}`,
              issueNumber: i + 1,
              roleName: "worker",
              prNumber: (i % 16) + 1,
              task: i === 0 ? realistic.agents[0].task : `处理 Issue #${i + 1}`,
              updatedAt: new Date().toISOString(),
            })), ...Array.from({ length: 900 }, (_, i) => ({
              ...agent, id: `ended-${i}`, roleName: "worker", state: "completed", prNumber: undefined,
              task: `历史完成事项 ${i}`, updatedAt: new Date(Date.now() - i * 3600000).toISOString(),
            }))]
          : [
              { ...agent, id: "worker-1", issueNumber: 12 },
              {
                ...agent,
                id: "idle",
                state: "parked",
                updatedAt: "2026-10-19T00:00:00Z",
              },
              { ...agent, id: "failed", state: "failed" },
              { ...agent, id: "verifier", roleName: "verifier", prNumber: 8 },
            ],
        queen: {
          enabled: true,
          state: "active",
          model: "test/model",
          agentId: "queen",
          lastTurnAt: null,
        },
        prs: large
          ? Array.from({ length: 16 }, (_, i) => ({
              number: i + 1,
              headSha: `head-${i}`,
              state: "open",
              merged: false,
              ready: i % 2 === 0,
              title: realistic.prs[i].title,
              updatedAt: new Date(Date.now() - i * 86400000).toISOString(),
            }))
          : [
              {
                number: 8,
                headSha: "head-8",
                state: "open",
                merged: false,
                ready: false,
                ownerId: "worker-1",
              },
            ],
        queue: [
          {
            agentId: "waiting",
            role: "worker",
            task: "排队任务",
            position: 1,
            createdAt: "",
            expiresAt: "",
          },
        ],
        signals: large ? realistic.signals : [
          {
            messageId: "issue",
            source: "github",
            kind: "issue",
            number: 12,
            title: "Issue #12",
            url: "",
            claimed: false,
            claimedBy: "",
            createdAt: "",
          },
          {
            messageId: "ci",
            source: "github",
            kind: "check",
            number: 8,
            title: "检查结果",
            url: "",
            claimed: true,
            claimedBy: "",
            createdAt: "",
          },
        ],
        methodology: structuredClone(m.methodologyResponses[3]),
        methodologyEvents: [],
        messages: large ? realistic.messages : [],
      };
      snapshot.methodology.manifest.merge.auto = false;
      const extra = {
        ...snapshot,
        hive: { ...hive, id: "other", name: "api-server" },
        agents: [],
        prs: [],
        queue: [],
        signals: [],
        messages: [],
      };
      const initial = multiple
        ? { [hive.id]: snapshot, other: extra }
        : { [hive.id]: snapshot };
      const mock = data.createMockDataSource(initial, {
          assignIssue: (id: string, n: number) => data.api.assignIssue(id, n),
        }),
        store = data.createProjectStore(hive.id, mock.source);
      store.start();
      const stores = new Map([[hive.id, store]]);
      if (multiple) {
        const other = data.createProjectStore("other", mock.source);
        other.start();
        stores.set("other", other);
      }
      const ui = UI.createStarsUI();
      const props = {
        ui,
        projects: Object.values(initial).map((s: any) => ({
          hive: s.hive,
          activeForagers: 0,
          pendingApprovals: 0,
          inboxBacklog: 0,
        })),
        stores,
        principal: { id: "test", username: "test" },
        onLogout() {},
        reloadProjects() {},
      };
      (DOM.createRoot ?? DOM.default.createRoot)(el).render(
        (R.createElement ?? R.default.createElement)(
          data.DataProvider,
          { source: mock.source },
          (R.createElement ?? R.default.createElement)(
            R.Fragment ?? R.default.Fragment,
            null,
            (R.createElement ?? R.default.createElement)(Canvas.default, props),
            (R.createElement ?? R.default.createElement)(
              Timeline.default,
              props,
            ),
          ),
        ),
      );
      (window as any).__canvas = {
        mock,
        store,
        ui,
        snapshot,
        hive,
        agent,
        load,
      };
    },
    { large, multiple, realisticFixture },
  );
  await expect(page.locator(".stars-canvas")).toHaveAttribute(
    "data-mode",
    "all",
  );
  await page.evaluate(() => {
    const x = (window as any).__canvas;
    x.mock.ready(x.hive.id);
  });
  await expect
    .poll(() => page.locator(".stars-canvas").getAttribute("data-nodes"))
    .not.toBe("0");
}
const point = (page: Page, id: string) =>
  page
    .locator(".stars-canvas")
    .evaluate((el: any, id) => el.starMapMetrics.positions[id], id);
async function settle(page: Page) {
  // The lens eases into place while the ambient canvas continues at about 30 fps.
  await page.waitForTimeout(900);
}
test("agent SSE updates nodes; unique real messages pulse and acknowledge; ambient continues and hidden stops", async ({
  page,
}) => {
  await mount(page);
  const n = Number(
    await page.locator(".stars-canvas").getAttribute("data-nodes"),
  );
  await page.evaluate(() => {
    const x = (window as any).__canvas;
    x.mock.push(x.hive.id, {
      type: "agent",
      data: { ...x.agent, id: "new-agent" },
    });
  });
  await expect(page.locator(".stars-canvas")).toHaveAttribute(
    "data-nodes",
    String(n + 1),
  );
  await expect(page.locator(".stars-canvas")).toHaveAttribute(
    "data-pulses",
    "0",
  );
  await page.evaluate(() => {
    const x = (window as any).__canvas,
      e = {
        type: "message",
        data: {
          id: "real",
          hiveId: x.hive.id,
          from: "queen",
          to: "forager:new-agent",
          kind: "task",
          chainId: "c",
          preview: "开始",
          createdAt: new Date().toISOString(),
        },
      };
    x.mock.push(x.hive.id, e);
    x.mock.push(x.hive.id, e);
  });
  await expect(page.locator(".stars-canvas")).toHaveAttribute(
    "data-pulses",
    "1",
  );
  expect(
    await page.evaluate(
      () => (window as any).__canvas.store.getSnapshot().pulses,
    ),
  ).toEqual([]);
  await settle(page);
  const f = Number(await page.locator(".stars-canvas").getAttribute("data-frames"));
  await page.waitForTimeout(500);
  expect(Number(await page.locator(".stars-canvas").getAttribute("data-frames"))).toBeGreaterThan(f);
  await page.evaluate(() => Object.defineProperty(document, "hidden", { configurable: true, value: true }));
  await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")));
  const hiddenFrames = await page.locator(".stars-canvas").getAttribute("data-frames");
  await page.waitForTimeout(300);
  await expect(page.locator(".stars-canvas")).toHaveAttribute("data-frames", hiddenFrames!);
});
test("three lenses, pan, double click, wheel and Issue drag route to queen", async ({
  page,
}) => {
  await mount(page);
  const qid = await page.evaluate(
    () => (window as any).__canvas.hive.id + ":queen",
  );
  let q = await point(page, qid);
  await page.mouse.click(q.x, q.y);
  await expect(page.locator(".stars-canvas")).toHaveAttribute(
    "data-mode",
    "project",
  );
  await expect(page.locator(".stars-canvas")).toHaveAttribute("data-hover", "");
  await settle(page);
  let issue = await point(page, "issue");
  q = await point(page, qid);
  let sent: any;
  await page.route("**/api/hives/*/messages", async (r) => {
    sent = r.request().postDataJSON();
    await r.fulfill({ json: { id: "assigned" } });
  });
  await page.mouse.move(issue.x, issue.y);
  await page.mouse.down();
  await page.mouse.move(q.x, q.y, { steps: 12 });
  await page.mouse.up();
  await expect
    .poll(() => sent)
    .toMatchObject({ to: "queen", body: "请处理 Issue #12" });
  await expect(page.getByRole("status")).toContainText("已请主 Agent");
  const a = await point(page, "worker-1");
  await page.mouse.click(a.x, a.y);
  await expect(page.locator(".stars-canvas")).toHaveAttribute(
    "data-mode",
    "object",
  );
  expect(
    await page.evaluate(
      () => (window as any).__canvas.ui.getSnapshot().selection.id,
    ),
  ).toBe("worker-1");
  await settle(page);
  await page.mouse.wheel(0, 80);
  await expect(page.locator(".stars-canvas")).toHaveAttribute(
    "data-mode",
    "project",
  );
  await settle(page);
  const before = await point(page, qid);
  await page.mouse.move(700, 180);
  await page.mouse.down();
  await page.mouse.move(750, 200, { steps: 4 });
  await page.mouse.up();
  await expect.poll(async () => (await point(page, qid)).x).not.toBe(before.x);
  await page.mouse.dblclick(700, 160);
  await expect(page.locator(".stars-canvas")).toHaveAttribute(
    "data-mode",
    "all",
  );
});
test("canvas labels stay out of mounted side panels and overlays during exit", async ({ page }) => {
  await mount(page);
  await page.evaluate(() => { const x = (window as any).__canvas; x.ui.selectProject(x.hive.id); });
  await settle(page);
  const canvas = page.locator(".stars-canvas");
  expect(Number(await canvas.getAttribute("data-labels-drawn"))).toBeGreaterThan(0);
  for (const className of ["pc-right", "pc-left", "pc-platform", "periphery-list", "periphery-pop"]) {
    await page.evaluate((className) => {
      const cover = document.createElement("section"); cover.className = className; cover.id = "test-cover";
      Object.assign(cover.style, { position: "fixed", inset: "0", width: "100vw", height: "100vh", maxHeight: "none", opacity: ".5", visibility: "visible", pointerEvents: "none" });
      document.body.append(cover);
    }, className);
    await expect.poll(async () => Number(await canvas.getAttribute("data-labels-covered"))).toBeGreaterThan(0);
    await expect(canvas).toHaveAttribute("data-labels-drawn", "0");
    expect(await point(page, "worker-1")).toBeTruthy();
    await page.locator("#test-cover").evaluate((el) => { (el as HTMLElement).style.opacity = "0"; });
    await expect.poll(async () => Number(await canvas.getAttribute("data-labels-drawn"))).toBeGreaterThan(0);
    await page.locator("#test-cover").evaluate((el) => el.classList.add("open"));
    await expect.poll(async () => Number(await canvas.getAttribute("data-labels-covered"))).toBeGreaterThan(0);
    await page.locator("#test-cover").evaluate((el) => el.remove());
  }
  await expect.poll(async () => Number(await canvas.getAttribute("data-labels-drawn"))).toBeGreaterThan(0);
});
test("reduced motion redraws once when covers or local cards change, then rests", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await mount(page, true);
  await page.evaluate(() => { const x = (window as any).__canvas; x.ui.selectProject(x.hive.id); });
  const canvas = page.locator(".stars-canvas");
  const frames = async () => Number(await canvas.getAttribute("data-frames"));
  const rest = async () => { await page.waitForTimeout(250); const before = await frames(); await page.waitForTimeout(300); expect(await frames()).toBe(before); };
  await rest();
  let before = await frames();
  await page.evaluate(() => {
    const cover = document.createElement("section"); cover.className = "pc-right"; cover.id = "rm-cover";
    Object.assign(cover.style, { position: "fixed", inset: "0", width: "100vw", height: "100vh", opacity: "1", pointerEvents: "none" });
    document.body.append(cover);
    requestAnimationFrame(() => cover.classList.add("pc-open"));
  });
  await expect.poll(frames).toBeGreaterThan(before);
  await expect.poll(async () => Number(await canvas.getAttribute("data-labels-covered"))).toBeGreaterThan(0);
  await rest();
  before = await frames();
  await page.locator("#rm-cover").evaluate((el) => el.remove());
  await expect.poll(frames).toBeGreaterThan(before);
  await rest();

  before = await frames();
  await page.locator(".stars-observer").click();
  await expect.poll(frames).toBeGreaterThan(before);
  await rest();
  before = await frames();
  await page.getByRole("button", { name: "关闭等你决定的事" }).click();
  await expect.poll(frames).toBeGreaterThan(before);
  await rest();

  const id = await page.evaluate(() => `${(window as any).__canvas.hive.id}:cluster:pr`);
  const q = await point(page, id);
  before = await frames();
  await page.mouse.click(q.x, q.y);
  await expect(page.locator(".stars-note.open")).toBeVisible();
  await expect.poll(frames).toBeGreaterThan(before);
  await rest();
  before = await frames();
  await page.locator(".stars-note.open").getByRole("button", { name: "关闭" }).click();
  await expect.poll(frames).toBeGreaterThan(before);
  await rest();
});
test("observer manual PR and question decisions open correct detail", async ({
  page,
}) => {
  await mount(page);
  await expect(page.locator(".stars-observer")).toHaveAttribute(
    "data-attention",
    "false",
  );
  await page.evaluate(() => {
    const x = (window as any).__canvas;
    x.mock.push(x.hive.id, {
      type: "pr",
      data: { ...x.snapshot.prs[0], hiveId: x.hive.id, ready: true },
    });
  });
  await expect(page.locator(".stars-observer")).toHaveAttribute(
    "data-attention",
    "true",
  );
  await page.locator(".stars-observer").click();
  await page.getByRole("button", { name: /PR #8 等你决定/ }).click();
  expect(
    await page.evaluate(
      () => (window as any).__canvas.ui.getSnapshot().selection.kind,
    ),
  ).toBe("pr");
  await page.evaluate(() => {
    const x = (window as any).__canvas;
    x.mock.push(x.hive.id, {
      type: "message",
      data: {
        id: "question",
        hiveId: x.hive.id,
        from: "forager:worker-1",
        to: "local:web-admin",
        kind: "question",
        chainId: "question",
        preview: "需要回答",
        createdAt: new Date().toISOString(),
      },
    });
  });
  await page.locator(".stars-observer").click();
  await page.getByRole("button", { name: /回答 Agent的问题/ }).click();
  expect(
    await page.evaluate(
      () => (window as any).__canvas.ui.getSnapshot().selection.id,
    ),
  ).toBe("worker-1");
});
test("illustrative timeline freezes presentation and never changes real snapshots", async ({
  page,
}) => {
  await mount(page);
  await page.evaluate(() =>
    (window as any).__canvas.ui.openOverlay("timeline"),
  );
  await expect(page.getByText(/示意回放，不代表真实历史/)).toBeVisible();
  await page.getByRole("slider").fill("200");
  const before = await page.evaluate(() =>
    JSON.stringify((window as any).__canvas.store.getSnapshot().snapshot),
  );
  await page.getByRole("button", { name: "曝光", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "暂停", exact: true }),
  ).toBeVisible();
  await page.waitForTimeout(100);
  expect(
    await page.evaluate(() =>
      JSON.stringify((window as any).__canvas.store.getSnapshot().snapshot),
    ),
  ).toBe(before);
  await page.getByRole("button", { name: "暂停", exact: true }).click();
  await page.evaluate(() => {
    const x = (window as any).__canvas;
    x.mock.push(x.hive.id, {
      type: "agent",
      data: { ...x.agent, id: "during-replay" },
    });
  });
  expect(await point(page, "during-replay")).toBeUndefined();
  await settle(page);
  const frozen = await page
    .locator(".stars-canvas")
    .evaluate((el: HTMLCanvasElement) => el.toDataURL());
  await page.evaluate(() => {
    const x = (window as any).__canvas;
    x.mock.push(x.hive.id, {
      type: "pr",
      data: { ...x.snapshot.prs[0], hiveId: x.hive.id, merged: true },
    });
    x.mock.push(x.hive.id, {
      type: "agent",
      data: { ...x.agent, id: "worker-1", state: "completed" },
    });
  });
  await settle(page);
  expect(
    await page
      .locator(".stars-canvas")
      .evaluate((el: HTMLCanvasElement) => el.toDataURL()),
  ).toBe(frozen);
  await page.getByRole("button", { name: "关闭时间轴" }).click();
  await expect.poll(() => point(page, "during-replay")).toBeTruthy();
});
for (const theme of ["bright", "paper", "night"])
  for (const style of ["classic", "simple"])
    test(`pixels ${theme}/${style}`, async ({ page }, info) => {
      await page.setViewportSize({ width: 1440, height: 1000 });
      await mount(page, false, true);
      await page.evaluate(
        async ({ theme, style }) => {
          const { themeStore } = await (window as any).__canvas.load(
            "/src/stars/theme.ts",
          );
          themeStore.setTheme(theme);
          themeStore.setStyle(style);
        },
        { theme, style },
      );
      await page.waitForTimeout(100);
      const result = await page
        .locator(".stars-canvas")
        .evaluate((canvas: HTMLCanvasElement) => {
          const { data } = canvas
            .getContext("2d")!
            .getImageData(0, 0, canvas.width, canvas.height);
          let count = 0;
          const bg = [...data.slice(0, 3)];
          for (let i = 0; i < data.length; i += 4)
            if (
              Math.abs(data[i] - bg[0]) +
                Math.abs(data[i + 1] - bg[1]) +
                Math.abs(data[i + 2] - bg[2]) >
              60
            )
              count++;
          return { bg, count };
        });
      await page.screenshot({ path: info.outputPath(`${theme}-${style}.png`) });
      await page.evaluate(() => {
        const x = (window as any).__canvas;
        x.ui.selectProject(x.hive.id);
      });
      await settle(page);
      await page.screenshot({
        path: info.outputPath(`${theme}-${style}-project.png`),
      });
      expect(result.count).toBeGreaterThan(300);
      expect(
        Math.abs(
          result.bg[0] -
            (theme === "night" ? 10 : theme === "paper" ? 230 : 244),
        ),
      ).toBeLessThanOrEqual(style === "classic" ? 7 : 0);
    });
test("merge transition and owner completion trail respect reduced motion", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await mount(page);
  await page.evaluate(() => {
    const x = (window as any).__canvas;
    x.mock.push(x.hive.id, {
      type: "pr",
      data: { ...x.snapshot.prs[0], hiveId: x.hive.id, merged: true },
    });
    x.mock.push(x.hive.id, {
      type: "agent",
      data: { ...x.agent, id: "worker-1", state: "completed" },
    });
  });
  await expect(page.locator(".stars-canvas")).toHaveAttribute(
    "data-merges",
    "1",
  );
  await expect(page.locator(".stars-canvas")).toHaveAttribute(
    "data-trails",
    "1",
  );
  await settle(page);
});
test("turning on reduced motion during a merge flare stops the flare and redraw loop", async ({ page }) => {
  await mount(page);
  await page.evaluate(() => {
    const x = (window as any).__canvas;
    x.ui.selectProject(x.hive.id);
    x.mock.push(x.hive.id, { type: "pr", data: { ...x.snapshot.prs[0], hiveId: x.hive.id, merged: true } });
  });
  await expect(page.locator(".stars-canvas")).toHaveAttribute("data-flares", "1", { timeout: 4000 });
  await page.emulateMedia({ reducedMotion: "reduce" });
  await expect(page.locator(".stars-canvas")).toHaveAttribute("data-flares", "0");
  await page.waitForTimeout(150);
  const frames = await page.locator(".stars-canvas").getAttribute("data-frames");
  await page.waitForTimeout(400);
  await expect(page.locator(".stars-canvas")).toHaveAttribute("data-frames", frames!);
});
test("observer local Escape restores focus without closing project, and queue explains wait", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await mount(page);
  await page.evaluate(() => {
    const x = (window as any).__canvas;
    x.ui.selectProject(x.hive.id);
  });
  await settle(page);
  await page.locator(".stars-observer").click();
  await expect(
    page.getByRole("button", { name: "关闭等你决定的事" }),
  ).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.locator(".stars-observer")).toBeFocused();
  expect(
    await page.evaluate(() => (window as any).__canvas.ui.getSnapshot().hiveId),
  ).toBeTruthy();
  const q = await point(page, "queue:waiting");
  await page.mouse.click(q.x, q.y);
  await expect(page.getByRole("status")).toContainText("第 1 位");
});
for (const mode of ["all", "project"])
  test(`300 active Agent 900 history browser performance ${mode}`, async ({
    page,
  }, info) => {
    await page.setViewportSize({ width: 1440, height: 1000 });
    await mount(page, true);
    if (mode === "project")
      await page.evaluate(() => {
        const x = (window as any).__canvas;
        x.ui.selectProject(x.hive.id);
      });
    await settle(page);
    await page.locator(".stars-canvas").evaluate((el: any) => {
      el.starMapMetrics.durations.length = 0;
    });
    for (let i = 0; i < 30; i++) {
      await page.evaluate((i) => {
        const x = (window as any).__canvas;
        x.mock.push(x.hive.id, {
          type: "message",
          data: {
            id: `perf-${i}`,
            hiveId: x.hive.id,
            from: "queen",
            to: `forager:agent-${i}`,
            kind: "task",
            chainId: "",
            preview: "work",
            createdAt: new Date().toISOString(),
          },
        });
      }, i);
      await page.waitForTimeout(20);
    }
    await settle(page);
    const m = await page
      .locator(".stars-canvas")
      .evaluate((el: any) => structuredClone(el.starMapMetrics));
    // Let SSE pulses and camera easing finish before measuring background CPU.
    await page.waitForTimeout(2200);
    const framesBefore = Number(await page.locator(".stars-canvas").getAttribute("data-frames"));
    const cdp = await page.context().browser()!.newBrowserCDPSession();
    const cpuBefore = await cdp.send("SystemInfo.getProcessInfo");
    const wallBefore = performance.now();
    await page.waitForTimeout(5000);
    const wallMs = performance.now() - wallBefore;
    const cpuAfter = await cdp.send("SystemInfo.getProcessInfo");
    const beforeRenderers = new Map((cpuBefore.processInfo as any[]).filter((p) => p.type === "renderer").map((p) => [p.id, p.cpuTime]));
    const stableRenderers = (cpuAfter.processInfo as any[]).filter((p) => p.type === "renderer" && beforeRenderers.has(p.id));
    const cpuDelta = stableRenderers.reduce((sum, p) => sum + p.cpuTime - (beforeRenderers.get(p.id) ?? p.cpuTime), 0);
    const end = Number(
        await page.locator(".stars-canvas").getAttribute("data-frames"),
      ),
      a = m.durations.sort((x: number, y: number) => x - y);
    const result = {
      agents: 300,
      prs: 16,
      history: m.history,
      viewport: "1440x1000",
      dpr: await page.evaluate(() => devicePixelRatio),
      browser: `Chromium ${page.context().browser()?.version()}`,
      theme: await page.evaluate(() => document.documentElement.dataset.theme),
      style: await page.evaluate(() => document.documentElement.dataset.style),
      reducedMotion: false,
      mode: m.mode,
      samples: a.length,
      p50: a[Math.floor(a.length * 0.5)],
      p95: a[Math.floor(a.length * 0.95)],
      max: a.at(-1),
      idleWindowMs: wallMs,
      idleFrames: end - framesBefore,
      targetFps: m.targetFps,
      measuredFps: ((end - framesBefore) * 1000) / wallMs,
      rendererCpuPercentOfOneCore: (cpuDelta / (wallMs / 1000)) * 100,
      rendererProcessCount: stableRenderers.length,
      cpuMethod: "CDP SystemInfo.getProcessInfo renderer cpuTime delta over wall time after 2.2 s settle; single worker/page; percent of one CPU core",
    };
    writeFileSync(
      info.outputPath("canvas-performance.json"),
      JSON.stringify(result, null, 2),
    );
    console.log("CANVAS_PERFORMANCE", JSON.stringify(result));
    expect(result.idleFrames).toBeGreaterThan(0);
    expect(m.history).toBe(900);
    expect(m.nodes).toBeGreaterThan(300);
    expect(result.p95).toBeLessThan(60);
  });

test("round8 fixture keeps normal project motion near 30 fps with measured CPU", async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await mount(page, false, false, true);
  await page.evaluate(() => { const x = (window as any).__canvas; x.ui.selectProject(x.hive.id); });
  await page.waitForTimeout(2200);
  const canvas = page.locator(".stars-canvas");
  await canvas.evaluate((el: any) => { el.starMapMetrics.durations.length = 0; });
  const framesBefore = Number(await canvas.getAttribute("data-frames"));
  const cdp = await page.context().browser()!.newBrowserCDPSession();
  const before = await cdp.send("SystemInfo.getProcessInfo");
  const wallBefore = performance.now();
  await page.waitForTimeout(5000);
  const wallMs = performance.now() - wallBefore;
  const after = await cdp.send("SystemInfo.getProcessInfo");
  const beforeRenderers = new Map((before.processInfo as any[]).filter((p) => p.type === "renderer").map((p) => [p.id, p.cpuTime]));
  const stableRenderers = (after.processInfo as any[]).filter((p) => p.type === "renderer" && beforeRenderers.has(p.id));
  const cpuDelta = stableRenderers.reduce((sum, p) => sum + p.cpuTime - (beforeRenderers.get(p.id) ?? p.cpuTime), 0);
  const metrics = await canvas.evaluate((el: any) => structuredClone(el.starMapMetrics));
  const durations = metrics.durations.sort((a: number, b: number) => a - b);
  const frames = Number(await canvas.getAttribute("data-frames")) - framesBefore;
  const result = {
    fixture: "round8Project()", viewport: "1440x1000", dpr: await page.evaluate(() => devicePixelRatio),
    browser: `Chromium ${page.context().browser()?.version()}`, theme: "bright", style: "classic", reducedMotion: false,
    mode: "project", nodes: metrics.nodes, history: metrics.history, targetFps: metrics.targetFps,
    measuredFps: frames * 1000 / wallMs, idleFrames: frames, idleWindowMs: wallMs,
    samples: durations.length, p50: durations[Math.floor(durations.length * .5)], p95: durations[Math.floor(durations.length * .95)],
    rendererCpuPercentOfOneCore: cpuDelta / (wallMs / 1000) * 100, rendererProcessCount: stableRenderers.length,
    cpuMethod: "CDP SystemInfo.getProcessInfo renderer cpuTime delta over 5s idle wall time; one worker/page; percent of one CPU core",
  };
  writeFileSync(info.outputPath("round8-performance.json"), JSON.stringify(result, null, 2));
  console.log("ROUND8_PERFORMANCE", JSON.stringify(result));
  expect(result.targetFps).toBe(30);
  expect(result.measuredFps).toBeGreaterThan(25);
});

test("blank click requests creation; double click cancels creation; distant projects remain", async ({
  page,
}) => {
  await mount(page, false, true);
  await page.evaluate(() => {
    (window as any).__creates = 0;
    window.addEventListener("swarmjet:create-project", () => {
      (window as any).__creates++;
    });
  });
  await page.mouse.click(500, 100);
  await expect
    .poll(() => page.evaluate(() => (window as any).__creates))
    .toBe(1);
  await page.mouse.dblclick(500, 100);
  await page.waitForTimeout(350);
  expect(await page.evaluate(() => (window as any).__creates)).toBe(1);
  await page.evaluate(() => {
    const x = (window as any).__canvas;
    x.ui.selectProject(x.hive.id);
  });
  await settle(page);
  expect(await point(page, "other:queen")).toBeTruthy();
});

test("live merge spirals and completed owner flies to history", async ({
  page,
}) => {
  await mount(page);
  await page.evaluate(() => {
    const x = (window as any).__canvas;
    x.ui.selectProject(x.hive.id);
  });
  await settle(page);
  const id = await page.evaluate(
    () => (window as any).__canvas.hive.id + ":pr:8",
  );
  await page.evaluate(() => {
    const x = (window as any).__canvas;
    x.mock.push(x.hive.id, {
      type: "pr",
      data: { ...x.snapshot.prs[0], hiveId: x.hive.id, merged: true },
    });
    x.mock.push(x.hive.id, {
      type: "agent",
      data: { ...x.agent, id: "worker-1", state: "completed" },
    });
  });
  await expect(page.locator(".stars-canvas")).toHaveAttribute(
    "data-merges",
    "1",
  );
  await expect.poll(() => point(page, id)).toBeUndefined();
  await expect.poll(() => point(page, "worker-1")).toBeUndefined();
  await expect(page.locator(".stars-canvas")).toHaveAttribute("data-completing", "1");
  await expect(page.locator(".stars-canvas")).toHaveAttribute(
    "data-trails",
    "1",
  );
  expect(Number(await page.locator(".stars-canvas").getAttribute("data-history"))).toBeGreaterThan(0);
  await expect(page.locator(".stars-canvas")).toHaveAttribute("data-completing", "0", { timeout: 5000 });
});

test("cluster opens the first six items and links to the active list", async ({ page }) => {
  await mount(page, true);
  await page.evaluate(() => { const x = (window as any).__canvas; x.ui.selectProject(x.hive.id); });
  await settle(page);
  await expect(page.locator(".stars-canvas")).toHaveAttribute("data-clusters", "2");
  const id = await page.evaluate(() => `${(window as any).__canvas.hive.id}:cluster:pr`);
  const q = await point(page, id);
  await page.mouse.click(q.x, q.y);
  await expect(page.locator(".stars-note.open")).toContainText("还有 8 个 PR");
  await expect(page.locator(".stars-note.open")).toContainText("PR #");
  await page.locator(".stars-note.open").getByRole("button", { name: "在列表中查看全部" }).click();
  expect(await page.evaluate(() => (window as any).__canvas.ui.getSnapshot().overlay)).toBe("list");
});

test("note and decision cards fade with their content, then become inert", async ({ page }) => {
  await mount(page, true);
  await page.evaluate(() => { const x = (window as any).__canvas; x.ui.selectProject(x.hive.id); const h1 = document.createElement("h1"); h1.textContent = "星图"; document.querySelector("main.stars-app")!.prepend(h1); });
  await settle(page);
  const id = await page.evaluate(() => `${(window as any).__canvas.hive.id}:cluster:pr`);
  const q = await point(page, id);
  await page.mouse.click(q.x, q.y);
  const note = page.locator(".stars-note");
  await expect(note).toHaveClass(/open/);
  await page.waitForTimeout(100);
  const entering = Number(await note.evaluate((el) => getComputedStyle(el).opacity));
  expect(entering).toBeGreaterThan(0);
  expect(entering).toBeLessThan(1);
  await expect(note).toHaveCSS("opacity", "1");
  const noteAxe = await new AxeBuilder({ page }).analyze();
  expect(noteAxe.violations).toEqual([]);
  await note.getByRole("button", { name: "关闭" }).click();
  await expect(note).toHaveAttribute("aria-hidden", "true");
  await expect(note).toHaveAttribute("inert", "");
  await page.waitForTimeout(100);
  const leaving = Number(await note.evaluate((el) => getComputedStyle(el).opacity));
  expect(leaving).toBeGreaterThan(0);
  expect(leaving).toBeLessThan(1);
  await expect(note).toContainText("还有 8 个 PR");
  await expect(note).toHaveCSS("visibility", "hidden", { timeout: 1000 });
  expect(await note.getByRole("button", { name: "关闭", includeHidden: true }).evaluate((el) => { (el as HTMLElement).focus(); return document.activeElement === el; })).toBe(false);

  await page.locator(".stars-observer").click();
  const decisions = page.locator(".stars-decisions");
  await expect(decisions).toHaveClass(/open/);
  await page.waitForTimeout(100);
  const opening = Number(await decisions.evaluate((el) => getComputedStyle(el).opacity));
  expect(opening).toBeGreaterThan(0);
  expect(opening).toBeLessThan(1);
  await expect(decisions).toHaveCSS("opacity", "1");
  const decisionAxe = await new AxeBuilder({ page }).analyze();
  expect(decisionAxe.violations).toEqual([]);
  await decisions.getByRole("button", { name: "关闭等你决定的事" }).click();
  await expect(decisions).toHaveAttribute("aria-hidden", "true");
  await expect(decisions).toHaveAttribute("inert", "");
  await page.waitForTimeout(100);
  const closing = Number(await decisions.evaluate((el) => getComputedStyle(el).opacity));
  expect(closing).toBeGreaterThan(0);
  expect(closing).toBeLessThan(1);
  await expect(decisions).toHaveCSS("visibility", "hidden", { timeout: 1000 });
  expect(await decisions.getByRole("button", { name: "关闭等你决定的事", includeHidden: true }).evaluate((el) => { (el as HTMLElement).focus(); return document.activeElement === el; })).toBe(false);
  await page.emulateMedia({ reducedMotion: "reduce" });
  await expect(note).toHaveCSS("transition-duration", "0s");
  await expect(decisions).toHaveCSS("transition-duration", "0s");
  await page.evaluate(async () => {
    const x = (window as any).__canvas;
    const { themeStore } = await x.load("/src/stars/theme.ts");
    themeStore.setTheme("night");
  });
  const nightCluster = await point(page, id);
  await page.mouse.click(nightCluster.x, nightCluster.y);
  await expect(note).toHaveCSS("opacity", "1");
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await note.getByRole("button", { name: "关闭" }).click();
  await page.locator(".stars-observer").click();
  await expect(decisions).toHaveCSS("opacity", "1");
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
});

test("only a confirmed failed CI appears as a diamond beside its PR", async ({ page }) => {
  await mount(page);
  await page.evaluate(() => { const x = (window as any).__canvas; x.ui.selectProject(x.hive.id); });
  await settle(page);
  const prId = await page.evaluate(() => `${(window as any).__canvas.hive.id}:pr:8`);
  expect(await point(page, "ci:ci")).toBeUndefined();
  await page.evaluate(() => {
    const x = (window as any).__canvas;
    x.mock.push(x.hive.id, { type: "pr", data: { ...x.snapshot.prs[0], hiveId: x.hive.id, reasons: ["required check failed: lint"] } });
  });
  await expect.poll(() => point(page, "ci:pr:8")).toBeTruthy();
  const ci = await point(page, "ci:pr:8"), pr = await point(page, prId);
  expect(Math.hypot(ci.x - pr.x, ci.y - pr.y)).toBeLessThan(50);
  await page.mouse.click(ci.x, ci.y);
  expect(await page.evaluate(() => (window as any).__canvas.ui.getSnapshot().selection)).toMatchObject({ kind: "pr", id: prId, number: 8 });
});

test("PR and verifier strokes keep design pixel widths at project and object zoom", async ({ page }) => {
  await page.addInitScript(() => {
    const proto = CanvasRenderingContext2D.prototype as any;
    const originalEllipse = proto.ellipse;
    const originalStroke = proto.stroke;
    const originalArc = proto.arc;
    (window as any).__glyphPens = [];
    proto.ellipse = function(this: CanvasRenderingContext2D, ...args: any[]) {
      const [x, y, rx, ry] = args;
      if (x === 0 && y === 0 && rx === 9 && ry === 2.6) (this as any).__glyphKind = "pr";
      else if (x === 0 && y === 0 && this.getLineDash().join(",") === "1,1.5") (this as any).__glyphKind = "moon";
      return originalEllipse.apply(this, args);
    };
    proto.stroke = function(this: CanvasRenderingContext2D, ...args: any[]) {
      const kind = (this as any).__glyphKind;
      if (kind) {
        const t = this.getTransform();
        (window as any).__glyphPens.push({ kind, width: this.lineWidth * Math.hypot(t.a, t.b) / devicePixelRatio });
        (this as any).__glyphKind = undefined;
        if (kind === "moon") (this as any).__nextMoonDot = true;
      }
      return originalStroke.apply(this, args);
    };
    proto.arc = function(this: CanvasRenderingContext2D, ...args: any[]) {
      if ((this as any).__nextMoonDot) {
        (window as any).__glyphPens.push({ kind: "moon-dot", radius: args[2] });
        (this as any).__nextMoonDot = false;
      }
      return originalArc.apply(this, args);
    };
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await mount(page);
  const readPens = () => page.evaluate(() => { const p = (window as any).__glyphPens; (window as any).__glyphPens = []; return p; });
  await page.evaluate(async () => { const x = (window as any).__canvas; const { themeStore } = await x.load("/src/stars/theme.ts"); themeStore.setTheme("bright"); x.ui.selectProject(x.hive.id); });
  await settle(page);
  const project = await readPens();
  expect(project.some((p: any) => p.kind === "pr")).toBe(true);
  expect(project.some((p: any) => p.kind === "moon")).toBe(true);
  expect(project.filter((p: any) => p.kind === "pr").every((p: any) => Math.abs(p.width - 1.32) < .05)).toBe(true);
  expect(project.filter((p: any) => p.kind === "moon").every((p: any) => Math.abs(p.width - .84) < .05)).toBe(true);
  await page.evaluate(() => { const x = (window as any).__canvas; x.ui.selectObject({ hiveId: x.hive.id, kind: "pr", id: `${x.hive.id}:pr:8`, number: 8 }); });
  await settle(page);
  const object = await readPens();
  expect(object.filter((p: any) => p.kind === "pr").every((p: any) => Math.abs(p.width - 1.32) < .05)).toBe(true);
  expect(object.filter((p: any) => p.kind === "moon").every((p: any) => Math.abs(p.width - .84) < .05)).toBe(true);
  expect(object.filter((p: any) => p.kind === "moon-dot").every((p: any) => p.radius <= 3.75)).toBe(true);
});

test("failed CI backed by a real check message opens that signal", async ({ page }) => {
  await mount(page);
  await page.evaluate(() => {
    const x = (window as any).__canvas;
    x.ui.selectProject(x.hive.id);
    x.mock.push(x.hive.id, { type: "message", data: {
      id: "ci", hiveId: x.hive.id, from: "github", to: "queen", kind: "external", chainId: "", hop: 0,
      body: "GitHub event: check\nCheck: lint -> failed\n\nLint failed", trust: "external", state: "handled", createdAt: new Date().toISOString(),
    } });
  });
  await settle(page);
  await expect.poll(() => point(page, "ci:ci")).toBeTruthy();
  const ci = await point(page, "ci:ci");
  await page.mouse.click(ci.x, ci.y);
  expect(await page.evaluate(() => (window as any).__canvas.ui.getSnapshot().selection)).toMatchObject({ kind: "signal", id: "ci", number: 8 });
});

test("hovering a finished Agent shows summary and opens its historical card", async ({ page }) => {
  await mount(page);
  await page.evaluate(() => {
    const x = (window as any).__canvas; x.ui.selectProject(x.hive.id);
    for (let i = 0; i < 20; i++) x.mock.push(x.hive.id, { type: "agent", data: { ...x.agent, id: `historic-${i}`, state: "completed", issueNumber: undefined, prNumber: undefined, task: "长标题".repeat(120), updatedAt: new Date().toISOString() } });
  });
  await settle(page);
  const chosen: any = await page.locator(".stars-canvas").evaluate((el: any) => Object.entries(el.starMapMetrics.positions).filter(([id, p]: any) => id.startsWith("history:historic-") && p.x > 50 && p.x < el.clientWidth - 50 && p.y > 50 && p.y < el.clientHeight - 50).map(([id, p]) => ({ id: id.slice(8), p }))[0]);
  const q = chosen?.p;
  expect(q).toBeTruthy();
  await page.mouse.move(q.x, q.y);
  expect(Number(await page.locator(".stars-canvas").getAttribute("data-history"))).toBeGreaterThan(0);
  const expected = await page.evaluate((id) => (window as any).__canvas.store.getSnapshot().derived.history.find((h: any) => h.id === id).summary, chosen.id);
  await expect(page.locator(".stars-history-hover p")).toHaveText(expected);
  expect((await page.locator(".stars-history-hover").boundingBox())!.width).toBeLessThanOrEqual(341);
  await page.mouse.click(q.x, q.y);
  await expect(page.locator(".stars-note.open")).toContainText("结束时间按 Agent 最近更新时间估算");
  await page.evaluate(() => { const h1 = document.createElement("h1"); h1.textContent = "星图"; document.querySelector("main.stars-app")!.prepend(h1); });
  const historyAxe = await new AxeBuilder({ page }).analyze();
  expect(historyAxe.violations).toEqual([]);
  await page.evaluate(async () => { const x = (window as any).__canvas; const { themeStore } = await x.load("/src/stars/theme.ts"); themeStore.setTheme("night"); });
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
});

test("an expired unacknowledged failure is not described as already read", async ({ page }) => {
  await mount(page);
  await page.evaluate(() => {
    const x = (window as any).__canvas;
    x.ui.selectProject(x.hive.id);
    for (let i = 0; i < 20; i++) x.mock.push(x.hive.id, { type: "agent", data: {
      ...x.agent, id: `old-failure-${i}`, roleName: "worker", state: "failed",
      issueNumber: undefined, prNumber: undefined, task: "旧任务失败",
      updatedAt: new Date(Date.now() - 25 * 3600000).toISOString(),
    } });
  });
  await settle(page);
  const point = await page.locator(".stars-canvas").evaluate((canvas: any) => Object.entries(canvas.starMapMetrics.positions)
    .filter(([id, p]: any) => id.startsWith("history:old-failure-") && p.x > 50 && p.x < canvas.clientWidth - 50 && p.y > 50 && p.y < canvas.clientHeight - 50)
    .map(([, p]) => p as {x: number; y: number})[0]);
  expect(point).toBeTruthy();
  await page.mouse.click(point.x, point.y);
  const title = page.locator(".stars-note.open h2");
  await expect(title).toContainText("失败");
  await expect(title).not.toContainText("已看过");
});

test("reduced motion renders a static canvas after camera settles", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await mount(page);
  await page.evaluate(() => { const x = (window as any).__canvas; x.ui.selectProject(x.hive.id); });
  await page.waitForTimeout(500);
  const canvas = page.locator(".stars-canvas");
  const before = await canvas.evaluate((el: HTMLCanvasElement) => el.toDataURL());
  const frames = await canvas.getAttribute("data-frames");
  await page.waitForTimeout(500);
  expect(await canvas.evaluate((el: HTMLCanvasElement) => el.toDataURL())).toBe(before);
  await expect(canvas).toHaveAttribute("data-frames", frames!);
});

test("font completion keeps the same data revision during ambient redraw", async ({ page }) => {
  await mount(page);
  const revision = await page.evaluate(() => (window as any).__canvas.store.getSnapshot().revision);
  const before = Number(await page.locator(".stars-canvas").getAttribute("data-frames"));
  await page.evaluate(() => document.fonts.dispatchEvent(new Event("loadingdone")));
  await page.waitForTimeout(250);
  expect(Number(await page.locator(".stars-canvas").getAttribute("data-frames"))).toBeGreaterThan(before);
  expect(await page.evaluate(() => (window as any).__canvas.store.getSnapshot().revision)).toBe(revision);
});

test("timeline opens live, freezes only in the past, and now restores current nodes and time", async ({
  page,
}) => {
  await mount(page);
  const openedAt = await page.evaluate(async () => {
    const x = (window as any).__canvas;
    const { timelineFor } = await x.load("/src/stars/timeline-state.ts");
    x.clock = timelineFor(x.ui);
    (window as any).__timelineNow = Date.now() + 120000;
    Date.now = () => (window as any).__timelineNow;
    x.ui.openOverlay("timeline");
    return Date.now();
  });
  await expect(page.getByRole("slider")).toBeVisible();
  await expect
    .poll(() =>
      page.evaluate(() => (window as any).__canvas.clock.getSnapshot().anchor),
    )
    .toBe(openedAt);
  await page.evaluate(() => {
    const x = (window as any).__canvas;
    x.mock.push(x.hive.id, {
      type: "agent",
      data: { ...x.agent, id: "open-live" },
    });
  });
  await expect.poll(() => point(page, "open-live")).toBeTruthy();
  await page.getByRole("slider").fill("200");
  await settle(page);
  const frozen = await page
    .locator(".stars-canvas")
    .evaluate((el: HTMLCanvasElement) => el.toDataURL());
  await page.evaluate(() => {
    const x = (window as any).__canvas;
    x.mock.push(x.hive.id, {
      type: "agent",
      data: { ...x.agent, id: "arrived-in-past" },
    });
    x.mock.push(x.hive.id, {
      type: "agent",
      data: { ...x.agent, id: "worker-1", state: "completed" },
    });
    (window as any).__timelineNow += 60000;
  });
  await settle(page);
  expect(await point(page, "arrived-in-past")).toBeUndefined();
  expect(await point(page, "worker-1")).toBeTruthy();
  expect(
    await page
      .locator(".stars-canvas")
      .evaluate((el: HTMLCanvasElement) => el.toDataURL()),
  ).toBe(frozen);
  await page.getByRole("button", { name: "现在", exact: true }).click();
  await expect.poll(() => point(page, "arrived-in-past")).toBeTruthy();
  await expect.poll(() => point(page, "worker-1")).toBeUndefined();
  expect(
    await page.evaluate(
      () => (window as any).__canvas.clock.getSnapshot().anchor,
    ),
  ).toBe(openedAt + 60000);
  await page.getByRole("button", { name: "关闭时间轴" }).click();
  await page.evaluate(() => {
    (window as any).__timelineNow += 120000;
    (window as any).__canvas.ui.openOverlay("timeline");
  });
  await expect(page.getByRole("slider")).toBeVisible();
  await expect
    .poll(() =>
      page.evaluate(() => (window as any).__canvas.clock.getSnapshot().anchor),
    )
    .toBe(openedAt + 180000);
});
