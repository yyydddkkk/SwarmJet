import { expect, test, type Page } from "@playwright/test";
import { mockHiveAPI } from "./hive-api-mock";
async function open(page: Page) {
  const state = await mockHiveAPI(page);
  await page.route("**/panel-test", (route) =>
    route.fulfill({
      contentType: "text/html",
      body: '<script type="module">import RefreshRuntime from "/@react-refresh";RefreshRuntime.injectIntoGlobalHook(window);window.$RefreshReg$=()=>{};window.$RefreshSig$=()=>type=>type;window.__vite_plugin_react_preamble_installed__=true;</script><div id="root"></div><script type="module" src="/tests/panel-harness.tsx"></script>',
    }),
  );
  await page.goto("/panel-test");
  return state;
}
test.use({ viewport: { width: 1440, height: 1000 } });
test("every panel opens and closes; platform replaces both sides", async ({
  page,
}) => {
  await open(page);
  await page.getByRole("button", { name: "项目", exact: true }).click();
  const project = page.getByRole("region", { name: "项目面板" });
  for (const name of ["方法论", "设置", "花费", "记忆"]) {
    await project.getByRole("button", { name, exact: true }).click();
    await expect(project).toBeVisible();
  }
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
  await expect(panel).toContainText("请先验证兼容行为");
});
test("project settings PATCH includes CSRF and selected fields", async ({
  page,
}) => {
  const state = await open(page);
  await page.getByRole("button", { name: "项目", exact: true }).click();
  await page.getByRole("button", { name: "设置", exact: true }).click();
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
  await expect(page.locator(".pc-version-table tbody tr")).toHaveCount(3);
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
