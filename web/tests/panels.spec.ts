import { expect, test, type Page } from "@playwright/test";
import { mockHiveAPI } from "./hive-api-mock";
import { mockStarsHTTP } from "./stars-http-mock";
async function open(page: Page) {
  const state = await mockHiveAPI(page);
  state.prStatuses["swarmjet:88"].headSha = "panel-observed-head";
  await page.route("**/panel-test", (route) =>
    route.fulfill({
      contentType: "text/html",
      body: '<script type="module">import RefreshRuntime from "/@react-refresh";RefreshRuntime.injectIntoGlobalHook(window);window.$RefreshReg$=()=>{};window.$RefreshSig$=()=>type=>type;window.__vite_plugin_react_preamble_installed__=true;</script><div id="root"></div><script type="module" src="/tests/panel-harness.tsx"></script>',
    }),
  );
  await page.goto("/panel-test");
  return state;
}
async function syncMessages(page: Page) {
  await page.evaluate(() => (window as unknown as {panelHarnessSyncMessages(): Promise<void>}).panelHarnessSyncMessages());
}
async function openMethodology(page: Page) {
  await page.getByRole("button", { name: "编辑方法论与版本历史" }).click();
}
test.use({ viewport: { width: 1440, height: 1000 } });
test("every panel opens and closes; platform replaces both sides", async ({
  page,
}) => {
  await open(page);
  await page.getByRole("button", { name: "项目", exact: true }).click();
  const project = page.getByRole("region", { name: "项目面板" });
  await expect(project.getByRole("heading", {name:"方法论"})).toBeVisible();
  await expect(project.getByRole("heading", {name:"设置"})).toBeVisible();
  await expect(project.getByRole("heading", {name:"花费"})).toBeVisible();
  await expect(project.getByRole("heading", {name:"记忆"})).toBeVisible();
  await openMethodology(page);
  await expect(page.getByTestId("methodology-version")).toBeVisible();
  await project.getByRole("button", {name:"返回项目面板"}).click();
  await page.getByRole("button", { name: "Agent详情", exact: true }).click();
  await expect(project).toBeVisible();
  await expect(page.getByRole("region", { name: "对象详情" })).toBeVisible();
  await page.getByRole("button", { name: "关闭对象详情" }).click();
  await expect(project).toBeVisible();
  await page.getByRole("button", { name: "关闭项目面板" }).click();
  await expect(project).toHaveCount(0);
  for (const name of ["主Agent详情", "PR详情"]) {
    await page.getByRole("button", { name, exact: true }).click();
    await expect(page.getByRole("region", { name: "对象详情" })).toBeVisible();
    await page.getByRole("button", { name: "关闭对象详情" }).click();
  }
  await page.getByRole("button", { name: "项目", exact: true }).click();
  await page.getByRole("button", { name: "平台", exact: true }).click();
  await expect(project).toHaveCount(0);
  const platform = page.getByRole("dialog", { name: "平台设置", exact: true });
  await expect(
    platform.getByText("GitHub App 已创建并保存。", { exact: false }),
  ).toBeVisible();
  for (const name of ["接入", "模型", "连接", "账簿", "账号"]) {
    await platform.getByRole("button", { name, exact: true }).click();
    await expect(platform).toBeVisible();
  }
  await page.getByRole("button", { name: "关闭平台设置" }).click();
  await expect(platform).toHaveCount(0);
});
test("interjection sends the exact agent address and full messages are visible", async ({
  page,
}) => {
  const state = await open(page);
  await page.getByRole("button", { name: "Agent详情", exact: true }).click();
  const panel = page.getByRole("region", { name: "对象详情" });
  await panel.getByRole("textbox", { name: "插话" }).fill("请先验证兼容行为");
  await panel.getByRole("button", { name: "发送", exact: true }).click();
  await expect(panel.getByText("消息已发出。", { exact: true })).toBeVisible();
  const call = state.calls.find(
    (c) => c.method === "POST" && c.path.endsWith("/messages"),
  );
  expect(call?.body).toMatchObject({
    to: expect.stringMatching(/^forager:/),
    body: "请先验证兼容行为",
  });
  await syncMessages(page);
  await expect(panel).toContainText("请先验证兼容行为");
});
test("project settings PATCH includes CSRF and selected fields", async ({
  page,
}) => {
  const state = await open(page);
  await page.getByRole("button", { name: "项目", exact: true }).click();
  await page.getByRole("button", { name: "更多项目设置" }).click();
  await page.getByLabel("并发上限", { exact: true }).fill("7");
  await page.getByRole("button", { name: "保存设置" }).click();
  await expect(page.getByRole("status")).toContainText("项目设置已保存");
  const call = state.calls.find((c) => c.method === "PATCH");
  expect(call?.headers["x-requested-with"]).toBe("swarmjet");
  expect(call?.body).toMatchObject({ config: { maxConcurrentForagers: 7 } });
});
test("methodology activation and rollback are confirmed and retain notes", async ({
  page,
}) => {
  const state = await open(page);
  await page.getByRole("button", { name: "项目", exact: true }).click();
  await openMethodology(page);
  await expect(page.locator(".pc-version-table tbody tr")).toHaveCount(3);
  await page.getByText("版本历史与回滚", { exact: true }).click();
  for (const version of [2, 3]) {
    const row = page
      .locator(".pc-version-table tbody tr")
      .filter({ hasText: `v${version}` });
    await row.getByRole("button", { name: "切换到此版本" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("切换备注").fill(`test v${version}`);
    await dialog
      .getByRole("button", { name: `切换到 v${version}`, exact: true })
      .click();
    await expect(dialog).toHaveCount(0);
    await expect
      .poll(
        () =>
          state.calls.filter(
            (c) =>
              c.method === "POST" && c.path.endsWith("/methodology/activate"),
          ).length,
      )
      .toBe(version === 2 ? 1 : 2);
  }
  expect(
    state.calls
      .filter(
        (c) => c.path.endsWith("/methodology/activate") && c.method === "POST",
      )
      .map((c) => c.body),
  ).toEqual([
    { version: 2, note: "test v2" },
    { version: 3, note: "test v3" },
  ]);
});
test("project view changes start at the top after scrolling the overview", async ({page}) => {
  await open(page);
  await page.getByRole("button", {name:"项目", exact:true}).click();
  const panel = page.getByRole("region", {name:"项目面板"});
  await panel.evaluate(el => el.scrollTop = el.scrollHeight);
  expect(await panel.evaluate(el => el.scrollTop)).toBeGreaterThan(100);
  await panel.getByRole("button", {name:"编辑方法论与版本历史"}).click();
  await expect.poll(() => panel.evaluate(el => el.scrollTop)).toBe(0);
  await expect(panel.getByRole("combobox", {name:"查看方法论版本"})).toBeInViewport();
  await panel.evaluate(el => el.scrollTop = el.scrollHeight);
  await panel.getByRole("button", {name:"返回项目面板"}).click();
  await expect.poll(() => panel.evaluate(el => el.scrollTop)).toBe(0);
  await panel.evaluate(el => el.scrollTop = el.scrollHeight);
  await panel.getByRole("button", {name:"查看第 2 版"}).click();
  await expect.poll(() => panel.evaluate(el => el.scrollTop)).toBe(0);
  await expect(panel.getByRole("combobox", {name:"查看方法论版本"})).toHaveValue("2");
  await expect(panel.getByRole("combobox", {name:"查看方法论版本"})).toBeInViewport();
});
test("merge submits the head actually displayed and prevents double click", async ({
  page,
}) => {
  let count = 0;
  let body: unknown;
  await open(page);
  await page.route("**/api/hives/swarmjet/prs/88/merge", async (route) => {
    count++;
    body = route.request().postDataJSON();
    await new Promise((resolve) => setTimeout(resolve, 150));
    await route.fulfill({
      json: {
        merged: true,
        queued: false,
        headSha: "panel-observed-head",
        ready: true,
        reasons: [],
      },
    });
  });
  await page.getByRole("button", { name: "PR详情", exact: true }).click();
  const head = page.getByRole("button", {name: /复制完整已读取提交/});
  await expect(head).toContainText("panel-o");
  await expect(head).not.toContainText("panel-observed-head");
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  await head.click();
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe("panel-observed-head");
  await page.getByRole("button", { name: "确认合并", exact: true }).dblclick();
  await expect.poll(() => count).toBe(1);
  expect(body).toEqual({ headSha: "panel-observed-head" });
});
test("token plaintext is only available once and credential inputs never persist", async ({
  page,
}) => {
  const state = await open(page);
  await page.getByRole("button", { name: "平台", exact: true }).click();
  await page.getByRole("button", { name: "连接", exact: true }).click();
  await page.getByRole("button", { name: "新建连接", exact: true }).click();
  await page.getByLabel("名称", { exact: true }).fill("test laptop");
  await page.getByRole("button", { name: "创建连接", exact: true }).click();
  await expect(page.getByTestId("token-plaintext")).toBeVisible();
  await page.getByRole("button", { name: "我已保存，关闭" }).click();
  await expect(page.getByTestId("token-plaintext")).toHaveCount(0);
  await page.getByRole("button", { name: "新建连接", exact: true }).click();
  await expect(page.getByTestId("token-plaintext")).toHaveCount(0);
  await page.getByRole("button", { name: "关闭对话框" }).click();
  await page.getByRole("button", { name: "模型", exact: true }).click();
  const fake = "test-only-fake-credential-abc";
  await page.getByLabel("API key", { exact: true }).fill(fake);
  expect(
    await page.evaluate(() =>
      JSON.stringify({
        local: { ...localStorage },
        session: { ...sessionStorage },
        url: location.href,
      }),
    ),
  ).not.toContain(fake);
  await page.getByRole("button", { name: "保存凭证", exact: true }).click();
  await expect(page.getByLabel("API key", { exact: true })).toHaveValue("");
  expect(
    state.calls.find(
      (c) => c.method === "PUT" && c.path.includes("/credentials/"),
    )?.body,
  ).toMatchObject({ apiKey: fake });
  await page.getByLabel("API key", { exact: true }).fill(fake);
  await page.getByRole("button", { name: "账号", exact: true }).click();
  await page.getByRole("button", { name: "模型", exact: true }).click();
  await expect(page.getByLabel("API key", { exact: true })).toHaveValue("");
});
test("complete conversations drain pages, include bootstrap, sort instants and rehydrate SSE previews", async ({
  page,
}) => {
  const state = await open(page);
  const base = {
    hiveId: "swarmjet",
    from: "queen",
    to: "hive",
    kind: "report" as const,
    chainId: "test",
    hop: 0,
    state: "handled" as const,
    trust: "agent",
    createdAt: "2026-10-20T00:00:00Z",
  };
  for (let i = 0; i < 205; i++)
    state.messages.push({ ...base, id: `page-${i}`, body: `完整引导 ${i}` });
  state.messages.push({
    ...base,
    id: "fraction",
    body: "半秒消息",
    createdAt: "2026-10-20T00:00:00.5Z",
  });
  await syncMessages(page);
  await page.getByRole("button", { name: "主Agent详情", exact: true }).click();
  const panel = page.getByRole("region", { name: "对象详情" });
  await expect(panel).toContainText("完整引导 204");
  expect(
    state.calls.some(
      (c) => c.path.endsWith("/messages") && c.query.includes("after="),
    ),
  ).toBeTruthy();
  const texts = await panel.locator(".pc-messages li").allTextContents();
  expect(texts.at(-1)).toContain("半秒消息");
  const full = "完整实时消息".repeat(40);
  state.messages.push({
    ...base,
    id: "live-full",
    body: full,
    createdAt: "2026-10-20T00:00:01Z",
  });
  await page.getByRole("button", { name: "消息事件" }).click();
  await expect(panel).toContainText(full);
  await expect(panel.getByText("短预览", { exact: true })).toHaveCount(0);
});
test("head conflict refreshes and shows the shared Chinese message", async ({
  page,
}) => {
  await open(page);
  await page.route("**/api/hives/swarmjet/prs/88/merge", (route) =>
    route.fulfill({ status: 409, json: { error: "head changed" } }),
  );
  await page.getByRole("button", { name: "PR详情", exact: true }).click();
  await page.getByRole("button", { name: "确认合并", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText(
    "PR 有新提交，请重新查看",
  );
});
test("platform traps keyboard focus and returns it when closed", async ({
  page,
}) => {
  await open(page);
  const entry = page.getByRole("button", { name: "平台", exact: true });
  await entry.click();
  const platform = page.getByRole("dialog", { name: "平台设置", exact: true });
  await expect(
    page.getByRole("button", { name: "关闭平台设置" }),
  ).toBeFocused();
  await expect(platform.getByRole("button", { name: "管理模型凭证" })).toBeVisible();
  await page.keyboard.press("Shift+Tab");
  await expect(
    platform.getByRole("button", { name: "管理模型凭证" }),
  ).toBeFocused();
  await page.getByRole("button", { name: "关闭平台设置" }).click();
  await expect(entry).toBeFocused();
});
test("GitHub manifest creation posts a hidden manifest form then callback offers installation", async ({
  page,
}) => {
  const state = await open(page);
  state.setup.github.configured = false;
  await page.getByRole("button", { name: "平台", exact: true }).click();
  await page.route("https://github.example/**", (route) =>
    route.fulfill({ contentType: "text/html", body: "<h1>确认应用</h1>" }),
  );
  const request = page.waitForRequest((r) =>
    r.url().startsWith("https://github.example/"),
  );
  await page.getByRole("button", { name: "一键创建 GitHub App" }).click();
  const post = await request;
  expect(post.method()).toBe("POST");
  expect(new URLSearchParams(post.postData() || "").get("manifest")).toContain(
    "swarmjet-test",
  );
  expect(
    state.calls.find((c) => c.path.endsWith("/github/manifest"))?.headers[
      "x-requested-with"
    ],
  ).toBe("swarmjet");
  await page.goto("/panel-test");
  state.setup.github.configured = true;
  await page.getByRole("button", { name: "平台", exact: true }).click();
  await expect(
    page.getByText("GitHub App 已创建并保存。", { exact: false }),
  ).toBeVisible();
  await expect(page.getByRole("link", { name: "安装到仓库" })).toHaveAttribute(
    "href",
    state.setup.github.installUrl,
  );
  await page.getByRole("button", { name: "重新读取", exact: true }).click();
});
test("panels remain legible in each theme and on mobile", async ({
  page,
}, testInfo) => {
  await open(page);
  await page.getByRole("button", { name: "项目", exact: true }).click();
  await openMethodology(page);
  await expect(page.getByTestId("methodology-version")).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("project-panel.png") });
  await page.getByRole("button", { name: "平台", exact: true }).click();
  await page.getByRole("button", { name: "模型", exact: true }).click();
  await expect(page.getByRole("heading", { name: "模型凭证" })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("platform-models.png") });
  await page.getByRole("button", { name: "关闭平台设置" }).click();
  await page.getByRole("button", { name: "PR详情", exact: true }).click();
  await expect(page.getByRole("button", { name: "确认合并" })).toBeVisible();
  for (const theme of ["bright", "paper", "night"]) {
    await page.evaluate(async (id) => {
      const path = "/src/stars/theme.ts";
      const { THEMES } = await import(path);
      for (const [key, value] of Object.entries(THEMES[id].colors))
        document
          .querySelector<HTMLElement>(".stars-app")!
          .style.setProperty(`--${key}`, String(value));
    }, theme);
    await page.screenshot({ path: testInfo.outputPath(`pr-${theme}.png`) });
  }
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole("button", { name: "确认合并" })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("pr-mobile.png") });
});
test("administrator questions reply to their actual sender and chain", async ({
  page,
}) => {
  const state = await open(page);
  state.messages.push({
    id: "admin-question",
    hiveId: "swarmjet",
    from: "forager:forager-7f3a",
    to: "local:web-admin",
    kind: "question",
    chainId: "exact-question-chain",
    hop: 1,
    body: "现在是否继续？",
    trust: "agent",
    state: "pending",
    createdAt: "2026-10-20T12:00:00Z",
  });
  await syncMessages(page);
  await page.getByRole("button", { name: "主Agent详情", exact: true }).click();
  await page.getByRole("button", { name: "回复这条提问" }).click();
  await page
    .getByRole("textbox", { name: "对主 Agent 说" })
    .fill("继续，保留兼容行为");
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect
    .poll(
      () =>
        state.calls.filter(
          (c) => c.method === "POST" && c.path.endsWith("/messages"),
        ).length,
    )
    .toBe(1);
  expect(
    state.calls.find((c) => c.method === "POST" && c.path.endsWith("/messages"))
      ?.body,
  ).toMatchObject({
    to: "forager:forager-7f3a",
    kind: "reply",
    chainId: "exact-question-chain",
    body: "继续，保留兼容行为",
  });
});
test("catalog distinguishes source release from project version and confirms installation", async ({
  page,
}) => {
  const state = await open(page);
  const active = state.methodologies.swarmjet.find((v) => v.active)!;
  active.source = { url: "builtin:pstack", ref: "older-test-release" };
  await page.getByRole("button", { name: "项目", exact: true }).click();
  await openMethodology(page);
  await page.getByText("从内置目录安装方法论", { exact: true }).click();
  await expect(
    page.getByText("当前来源版本：older-test-release。", { exact: false }),
  ).toContainText("目录有新版本可安装");
  const catalog = page.getByRole("list", { name: "内置方法论目录" });
  await catalog
    .getByRole("button", { name: "安装 pstack", exact: false })
    .click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: "确认安装" }).click();
  await expect(dialog).toHaveCount(0);
  expect(
    state.calls.find(
      (c) => c.method === "POST" && c.path.endsWith("/methodology/install"),
    )?.body,
  ).toMatchObject({ name: "pstack" });
});

test("logout delegates once to the shell and exposes failures for retry", async ({
  page,
}) => {
  const state = await open(page);
  await page.getByRole("button", { name: "平台", exact: true }).click();
  await page.getByRole("button", { name: "账号", exact: true }).click();
  await page.route("**/api/logout", (route) =>
    route.fulfill({ status: 500, json: { error: "test logout unavailable" } }),
  );
  await page.getByRole("button", { name: "退出登录", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("退出失败");
  await page.unroute("**/api/logout");
  await page.getByRole("button", { name: "退出登录", exact: true }).click();
  await expect
    .poll(
      () =>
        state.calls.filter(
          (c) => c.method === "POST" && c.path === "/api/logout",
        ).length,
    )
    .toBe(1);
});

test("each close button dismisses its own side while Escape semantics stay layered", async ({
  page,
}) => {
  await open(page);
  await page.getByRole("button", { name: "项目", exact: true }).click();
  await page.getByRole("button", { name: "Agent详情", exact: true }).click();
  await page.getByRole("button", { name: "关闭项目面板" }).click();
  await expect(page.getByRole("region", { name: "项目面板" })).toHaveCount(0);
  await expect(page.getByRole("region", { name: "对象详情" })).toBeVisible();
  await expect(
    page.getByRole("button", { name: "关闭对象详情" }),
  ).toBeFocused();
  await page.getByRole("button", { name: "项目", exact: true }).click();
  await page.getByRole("button", { name: "关闭对象详情" }).click();
  await expect(page.getByRole("region", { name: "对象详情" })).toHaveCount(0);
  await expect(page.getByRole("region", { name: "项目面板" })).toBeVisible();
  const result = await page.evaluate(async () => {
    const path = "/src/stars/ui.ts";
    const { createStarsUI } = await import(path);
    const ui = createStarsUI({
      hiveId: "h",
      projectPanel: "settings",
      selection: { hiveId: "h", id: "a", kind: "agent" },
    });
    ui.close();
    const afterObject = ui.getSnapshot();
    ui.close();
    return { afterObject, afterProject: ui.getSnapshot() };
  });
  expect(result.afterObject.projectPanel).toBe("settings");
  expect(result.afterObject.selection).toBeUndefined();
  expect(result.afterProject.projectPanel).toBeUndefined();
  expect(result.afterProject.hiveId).toBe("h");
});

test("usage ranking opens agent details and pending events retain complete external content", async ({
  page,
}) => {
  const state = await open(page);
  state.messages.push({
    id: "signal-full",
    hiveId: "swarmjet",
    from: "router",
    to: "hive",
    kind: "event",
    chainId: "external",
    hop: 0,
    trust: "external",
    state: "pending",
    body: "这是完整的任务原文。".repeat(30),
    createdAt: "2026-10-20T00:00:00Z",
  });
  await syncMessages(page);
  await page.getByRole("button", { name: "项目", exact: true }).click();
  await page.getByRole("button", { name: "查看花费详情" }).click();
  const rank = page.getByRole("list", { name: "Agent 花费排行" });
  await rank.getByRole("button").first().click();
  await expect(page.getByRole("region", { name: "对象详情" })).toBeVisible();
  await page.getByRole("button", { name: "关闭对象详情" }).click();
  await page.getByRole("button", { name: "关闭项目面板" }).click();
  await page.getByRole("button", { name: "事件详情", exact: true }).click();
  await expect(page.getByRole("region", { name: "对象详情" })).toContainText(
    "这是完整的任务原文。".repeat(30),
  );
  await expect(
    page.getByText("外部内容，未经核实", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "请主 Agent 处理 Issue #42" }).click();
  await expect
    .poll(
      () =>
        state.calls.filter(
          (c) => c.method === "POST" && c.path.endsWith("/messages"),
        ).length,
    )
    .toBe(1);
  expect(
    state.calls.find((c) => c.method === "POST" && c.path.endsWith("/messages"))
      ?.body,
  ).toMatchObject({ to: "queen", body: "请处理 Issue #42" });
});
test("check and pull request signals are read only; only Issues can be assigned", async ({page}) => {
  const state = await open(page);
  const baseline = state.calls.filter(call => call.method === "POST" && call.path.endsWith("/messages")).length;
  for (const name of ["检查详情", "外部PR详情"]) {
    await page.getByRole("button", {name, exact:true}).click();
    const panel = page.getByRole("region", {name:"对象详情"});
    await expect(panel).toBeVisible();
    await expect(panel.getByRole("link", {name:"在 GitHub 查看"})).toBeVisible();
    await expect(panel.getByRole("button", {name:/请主 Agent 处理 Issue/})).toHaveCount(0);
    await page.getByRole("button", {name:"关闭对象详情"}).click();
    await expect(panel).toHaveCount(0);
  }
  expect(state.calls.filter(call => call.method === "POST" && call.path.endsWith("/messages"))).toHaveLength(baseline);
});
for (const result of ["delay", "failure"] as const) {
  test(`new PR head cannot merge with old validation while detail refresh ${result}`, async ({
    page,
  }) => {
    const state = await open(page);
    const initial = state.prStatuses["swarmjet:88"];
    initial.ready = true;
    initial.checks = [
      { name: "OLD_HEAD_CHECK", status: "completed", conclusion: "success" },
    ];
    let changed = false;
    let release!: () => void;
    const pending = new Promise<void>((resolve) => {
      release = resolve;
    });
    await page.route("**/api/hives/swarmjet/prs/88", async (route) => {
      if (!changed) {
        await route.fallback();
        return;
      }
      await pending;
      await route.fulfill(
        result === "failure"
          ? { status: 503, json: { error: "test detail unavailable" } }
          : {
              json: {
                ...initial,
                headSha: "panel-new-head",
                ready: true,
                checks: [
                  {
                    name: "NEW_HEAD_CHECK",
                    status: "completed",
                    conclusion: "success",
                  },
                ],
              },
            },
      );
    });
    const merges: unknown[] = [];
    await page.route("**/api/hives/swarmjet/prs/88/merge", (route) => {
      merges.push(route.request().postDataJSON());
      return route.fulfill({
        json: {
          merged: false,
          queued: true,
          headSha: "panel-new-head",
          ready: true,
          reasons: [],
        },
      });
    });
    await page.getByRole("button", { name: "PR详情", exact: true }).click();
    const panel = page.getByRole("region", { name: "对象详情" });
    const merge = panel.getByRole("button", { name: "确认合并", exact: true });
    await expect(merge).toBeEnabled();
    await expect(panel).toContainText("OLD_HEAD_CHECK");
    changed = true;
    await page.getByRole("button", { name: "PR新提交", exact: true }).click();
    await expect(panel).toContainText("PR 有新提交，请重新查看");
    await expect(panel.getByTestId("reviewed-pr-head")).toHaveText(
      "panel-observed-head",
    );
    await expect(merge).toBeDisabled();
    await merge.dispatchEvent("click");
    expect(merges).toHaveLength(0);
    release();
    if (result === "failure") {
      await expect(panel).toContainText("test detail unavailable");
      await expect(merge).toBeDisabled();
      await merge.dispatchEvent("click");
      expect(merges).toHaveLength(0);
      await expect(panel.getByTestId("reviewed-pr-head")).toHaveText(
        "panel-observed-head",
      );
    } else {
      await expect(panel.getByTestId("reviewed-pr-head")).toHaveText(
        "panel-new-head",
      );
      await expect(panel).toContainText("NEW_HEAD_CHECK");
      await expect(panel).not.toContainText("OLD_HEAD_CHECK");
      await expect(merge).toBeEnabled();
      await merge.click();
      await expect.poll(() => merges.length).toBe(1);
      expect(merges).toEqual([{ headSha: "panel-new-head" }]);
      await expect(panel).toContainText("已进入合并队列，等待平台处理。");
    }
  });
}
test("head changes during merge never retarget the pending request", async ({
  page,
}) => {
  const state = await open(page);
  state.prStatuses["swarmjet:88"].ready = true;
  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  const merges: unknown[] = [];
  await page.route("**/api/hives/swarmjet/prs/88/merge", async (route) => {
    merges.push(route.request().postDataJSON());
    await pending;
    await route.fulfill({ status: 409, json: { error: "head changed" } });
  });
  await page.getByRole("button", { name: "PR详情", exact: true }).click();
  const panel = page.getByRole("region", { name: "对象详情" });
  await panel.getByRole("button", { name: "确认合并", exact: true }).click();
  await expect.poll(() => merges.length).toBe(1);
  await page.route("**/api/hives/swarmjet/prs/88", (route) =>
    route.fulfill({ status: 503, json: { error: "test refresh failed" } }),
  );
  await page.getByRole("button", { name: "PR新提交", exact: true }).click();
  await expect(panel).toContainText("PR 有新提交，请重新查看");
  await expect(
    panel.getByRole("button", { name: "正在合并…", exact: true }),
  ).toBeDisabled();
  expect(merges).toEqual([{ headSha: "panel-observed-head" }]);
  release();
  await expect(
    panel.getByRole("button", { name: "确认合并", exact: true }),
  ).toBeDisabled();
  await expect(panel).toContainText("test refresh failed");
  expect(merges).toHaveLength(1);
});
test("a same-head detail response that is not ready overrides ready SSE", async ({
  page,
}) => {
  const state = await open(page);
  state.prStatuses["swarmjet:88"].ready = false;
  state.prStatuses["swarmjet:88"].reasons = ["需要重新验证"];
  await page.getByRole("button", { name: "PR详情", exact: true }).click();
  await expect(page.getByTestId("reviewed-pr-head")).toHaveText(
    "panel-observed-head",
  );
  await expect(
    page.getByRole("button", { name: "确认合并", exact: true }),
  ).toBeDisabled();
  await expect(page.getByRole("region", { name: "对象详情" })).toContainText(
    "需要重新验证",
  );
});

test("same-head not-ready SSE disables stale ready checks immediately", async ({
  page,
}) => {
  const state = await open(page);
  state.prStatuses["swarmjet:88"].ready = true;
  await page.getByRole("button", { name: "PR详情", exact: true }).click();
  const merge = page.getByRole("button", { name: "确认合并", exact: true });
  await expect(merge).toBeEnabled();
  await page.getByRole("button", { name: "PR条件变化", exact: true }).click();
  await expect(merge).toBeDisabled();
  await merge.dispatchEvent("click");
  expect(
    state.calls.filter((c) => c.method === "POST" && c.path.endsWith("/merge")),
  ).toHaveLength(0);
});

for (const failure of [false, true]) {
  test(`switching projects resets methodology version and never shows previous project data (${failure ? "failure" : "success"})`, async ({
    page,
  }) => {
    const state = await open(page);
    const version = structuredClone(
      state.methodologies.swarmjet.find((v) => v.version === 1)!,
    );
    version.hiveId = "orders";
    version.version = 1;
    version.active = true;
    version.note = "B项目独有备注";
    version.manifest.standingOrders = ["B项目独有规则"];
    state.methodologies.orders = [version];
    state.methodologyFiles.orders = {
      1: [{ path: "methodology.yaml", content: "B项目独有文件正文" }],
    };
    await page.getByRole("button", { name: "项目", exact: true }).click();
    await openMethodology(page);
    await page.getByLabel("查看方法论版本", { exact: true }).selectOption("2");
    await expect(page.getByTestId("methodology-version")).toContainText("v2");
    await page.getByText("查看文件内容", { exact: true }).click();
    const oldBody = await page
      .getByTestId("methodology-file-content")
      .innerText();
    if (failure)
      await page.route("**/api/hives/orders/methodology?*", (route) =>
        route.fulfill({ status: 503, json: { error: "B项目方法论读取失败" } }),
      );
    if (failure)
      await page.route("**/api/hives/orders/methodology", (route) =>
        route.fulfill({ status: 503, json: { error: "B项目方法论读取失败" } }),
      );
    await page.getByRole("button", { name: "切到B项目", exact: true }).click();
    const panel = page.getByRole("region", { name: "项目面板" });
    await openMethodology(page);
    await expect(
      panel.getByRole("heading", {
        name: state.hives.find((p) => p.hive.id === "orders")!.hive.name,
        exact: true,
      }),
    ).toBeVisible();
    if (failure) {
      await expect(panel.getByRole("alert")).toContainText(
        "B项目方法论读取失败",
      );
      await expect(page.getByTestId("methodology-file-content")).toHaveCount(0);
    } else {
      await expect(page.getByTestId("methodology-version")).toContainText("v1");
      await expect(page.getByTestId("methodology-file-content")).toHaveText(
        "B项目独有文件正文",
      );
      await expect(panel).toContainText("B项目独有规则");
    }
    await expect(panel).not.toContainText(oldBody);
    expect(
      state.calls.filter(
        (c) =>
          c.path === "/api/hives/orders/methodology" &&
          c.query.includes("version=2"),
      ),
    ).toHaveLength(0);
  });
}
test("methodology first view prioritizes role diagram and plain rules while advanced tools remain available", async ({
  page,
}, testInfo) => {
  await open(page);
  await page.getByRole("button", { name: "项目", exact: true }).click();
  const panel = page.getByRole("region", { name: "项目面板" });
  await expect(panel.getByRole("img", { name: "方法论角色轨道" })).toBeVisible();
  await openMethodology(page);
  await expect(panel.getByRole("img", { name: "角色分工示意" })).toBeVisible();
  const diagram = await panel
    .getByRole("img", { name: "角色分工示意" })
    .boundingBox();
  expect(diagram!.y).toBeLessThan(450);
  expect(
    (await panel
      .getByRole("heading", { name: "事件规则", exact: true })
      .first()
      .boundingBox())!.y,
  ).toBeLessThan(600);
  expect(
    (await panel
      .getByRole("heading", { name: "合并规则", exact: true })
      .boundingBox())!.y,
  ).toBeLessThan(800);
  const versionDetails = panel
    .locator("details")
    .filter({ has: page.locator("summary", { hasText: "版本详情" }) })
    .first();
  expect(
    await versionDetails.evaluate((node: HTMLDetailsElement) => node.open),
  ).toBe(false);
  await expect(page.getByTestId("methodology-file-content")).toBeHidden();
  await page.screenshot({
    path: testInfo.outputPath("methodology-first-view.png"),
  });
  await panel.getByRole("button", { name: /roles\/verifier.md/ }).click();
  await expect(page.getByTestId("methodology-file-content")).toBeVisible();
  await panel.getByText("版本详情", { exact: true }).click();
  await expect(panel.getByText("摘要", { exact: true })).toBeVisible();
  await panel.getByText("比较版本差异", { exact: true }).click();
  await expect(page.getByTestId("methodology-diff").first()).toBeVisible();
  await panel.getByText("角色、权限与全部规则", { exact: true }).click();
  await expect(page.getByTestId("method-role-verifier")).toBeVisible();
});
test("methodology layout also renders in the real application shell with bundled fonts", async ({
  page,
}, testInfo) => {
  const { state } = await mockStarsHTTP(page);
  await page.goto("/");
  await page.getByRole("dialog", { name: "简报", exact: true })
    .getByRole("button", { name: "进入星图", exact: true }).click();
  await page.getByRole("dialog", { name: "图例", exact: true })
    .getByRole("button", { name: "关闭", exact: true }).click();
  await page
    .getByRole("combobox", { name: "选择项目", exact: true })
    .selectOption("swarmjet");
  await page.getByRole("button", { name: "项目面板", exact: true }).click();
  const panel = page.getByRole("region", { name: "项目面板" });
  await expect(panel.getByRole("img", { name: "方法论角色轨道" })).toBeVisible();
  await openMethodology(page);
  await expect(panel.getByRole("img", { name: "角色分工示意" })).toBeVisible();
  await expect(
    panel.getByRole("heading", { name: "合并规则", exact: true }),
  ).toBeVisible();
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({
    path: testInfo.outputPath("methodology-actual-shell.png"),
  });
  expect(
    state.calls.some((c) => c.path === "/api/hives/swarmjet/methodology"),
  ).toBeTruthy();
});
