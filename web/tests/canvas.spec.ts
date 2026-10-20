import { expect, test, type Page } from "@playwright/test";
import { writeFileSync } from "node:fs";
async function mount(page: Page, large = false, multiple = false) {
  page.on("pageerror", (e) => console.log("CANVAS_ERROR", e.message));
  await page.route("**/api/**", (r) =>
    r.fulfill({ status: 401, json: { error: "test session" } }),
  );
  await page.goto("/");
  await page.evaluate(
    async ({ large, multiple }) => {
      const load = (p: string) => import(/* @vite-ignore */ p);
      const [R, DOM, data, UI, Canvas, Timeline, f, m] = await Promise.all([
        load("/node_modules/.vite/deps/react.js"),
        load("/node_modules/.vite/deps/react-dom_client.js"),
        load("/src/data/index.ts"),
        load("/src/stars/ui.ts"),
        load("/src/stars/CanvasSlot.tsx"),
        load("/src/stars/TimelineSlot.tsx"),
        load("/src/fixtures/hive-console.ts"),
        load("/src/fixtures/round4.ts"),
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
      const snapshot = {
        hive,
        localAddress: "local:web-admin",
        agents: large
          ? Array.from({ length: 300 }, (_, i) => ({
              ...agent,
              id: `agent-${i}`,
              issueNumber: i + 1,
              roleName: i % 10 === 0 ? "verifier" : "worker",
              prNumber: (i % 50) + 1,
            }))
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
          ? Array.from({ length: 50 }, (_, i) => ({
              number: i + 1,
              headSha: `head-${i}`,
              state: "open",
              merged: false,
              ready: i % 2 === 0,
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
        signals: [
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
        messages: [],
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
    { large, multiple },
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
  await expect
    .poll(async () => {
      const b = await page.locator(".stars-canvas").getAttribute("data-frames");
      await page.waitForTimeout(120);
      return (
        b === (await page.locator(".stars-canvas").getAttribute("data-frames"))
      );
    })
    .toBe(true);
}
test("agent SSE updates nodes; unique real messages pulse and acknowledge; idle stops", async ({
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
  const f = await page.locator(".stars-canvas").getAttribute("data-frames");
  await page.waitForTimeout(500);
  await expect(page.locator(".stars-canvas")).toHaveAttribute(
    "data-frames",
    f!,
  );
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
  test(`300 Agent 50 PR browser performance ${mode}`, async ({
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
    await page.waitForTimeout(1000);
    const end = Number(
        await page.locator(".stars-canvas").getAttribute("data-frames"),
      ),
      a = m.durations.sort((x: number, y: number) => x - y);
    const result = {
      agents: 300,
      prs: 50,
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
      idleWindowMs: 1000,
      idleFrames: end - m.frames,
    };
    writeFileSync(
      info.outputPath("canvas-performance.json"),
      JSON.stringify(result, null, 2),
    );
    console.log("CANVAS_PERFORMANCE", JSON.stringify(result));
    expect(result.idleFrames).toBe(0);
    expect(m.nodes).toBe(354);
    expect(result.p95).toBeLessThan(60);
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

test("live merge spirals and completed owner fades, then both stop rendering", async ({
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
  const before = await point(page, id);
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
  await expect
    .poll(async () => {
      const now = await point(page, id);
      return now && Math.hypot(now.x - before.x, now.y - before.y);
    })
    .toBeGreaterThan(10);
  await expect.poll(() => point(page, id)).toBeUndefined();
  await expect.poll(() => point(page, "worker-1")).toBeUndefined();
  await expect(page.locator(".stars-canvas")).toHaveAttribute(
    "data-trails",
    "1",
  );
  await settle(page);
});
