import { writeFile } from "node:fs/promises";
import { test, expect } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { mockStarsHTTP } from "./stars-http-mock";
import { command } from "../src/stars/periphery-command";
import { createStarsUI } from "../src/stars/ui";
import { createProjectStore } from "../src/data/store";
import { createMockDataSource } from "../src/data/mock";
import { hives } from "../src/fixtures/hive-console";
import { methodologyResponses } from "../src/fixtures/round4";
const closeBrief = async (page: import("@playwright/test").Page) => {
  await page
    .getByRole("dialog", { name: "简报", exact: true })
    .getByRole("button", { name: "关闭", exact: true })
    .click();
  await page.getByRole("button", { name: "列表", exact: true }).click();
};
test("真实简报四组、正常快照、SSE断线与无障碍", async ({ page }, info) => {
  const mock = await mockStarsHTTP(page);
  await page.goto("/");
  const brief = page.getByRole("dialog", { name: "简报", exact: true });
  await expect(brief.getByText("PR #88 修复兼容问题")).toBeVisible();
  for (const text of ["需要你", "正在进行", "需要留意", "昨晚完成"])
    await expect(
      brief.getByRole("heading", { name: text, exact: true }),
    ).toBeVisible();
  await expect(brief.getByText("失败", { exact: true }).first()).toBeVisible();
  await expect(
    brief.getByText("昨晚合并数、昨晚花费：暂时无法获取。"),
  ).toBeVisible();
  await expect(
    page.getByText("连接已断开，正在重连", { exact: false }),
  ).toHaveCount(0);
  const results = await new AxeBuilder({ page }).analyze();
  await writeFile(
    info.outputPath("brief-axe.json"),
    JSON.stringify(
      {
        testEngine: results.testEngine,
        url: results.url,
        timestamp: results.timestamp,
        passes: results.passes.length,
        violations: results.violations,
      },
      null,
      2,
    ),
  );
  await info.attach("brief-axe.json", {
    body: JSON.stringify(results.violations, null, 2),
    contentType: "application/json",
  });
  expect(
    results.violations.filter((v) =>
      ["critical", "serious"].includes(v.impact || ""),
    ),
  ).toEqual([]);
  await page.screenshot({ path: info.outputPath("brief.png") });
  await mock.disconnect();
  await expect(
    page.getByRole("alert").filter({ hasText: "连接已断开" }),
  ).toBeVisible();
});
test("键盘列表搜索、插话取消、Issue派活与跳转", async ({ page }, info) => {
  const { state } = await mockStarsHTTP(page);
  await page.goto("/");
  await closeBrief(page);
  const list = page.getByRole("dialog", { name: "列表", exact: true });
  await expect(
    list.getByRole("button", { name: "PR #88 修复兼容问题", exact: true }),
  ).toBeVisible();
  const results = await new AxeBuilder({ page }).analyze();
  await writeFile(
    info.outputPath("list-axe.json"),
    JSON.stringify(
      {
        testEngine: results.testEngine,
        url: results.url,
        timestamp: results.timestamp,
        passes: results.passes.length,
        violations: results.violations,
      },
      null,
      2,
    ),
  );
  await page.screenshot({ path: info.outputPath("list.png") });
  await info.attach("list-axe.json", {
    body: JSON.stringify(results.violations, null, 2),
    contentType: "application/json",
  });
  expect(
    results.violations.filter((v) =>
      ["critical", "serious"].includes(v.impact || ""),
    ),
  ).toEqual([]);
  await list.getByLabel("向 forager-7f3a 插话").fill("请补充测试");
  await list.getByLabel("向 forager-7f3a 插话").press("Enter");
  await expect
    .poll(() =>
      state.calls.some(
        (c) =>
          c.method === "POST" &&
          (JSON.stringify(c.body) || "").includes("请补充测试"),
      ),
    )
    .toBeTruthy();
  await list.getByRole("button", { name: "交给主 Agent" }).click();
  await expect
    .poll(() =>
      state.calls.some((c) =>
        (JSON.stringify(c.body) || "").includes("请处理 Issue #88"),
      ),
    )
    .toBeTruthy();
  await list
    .getByRole("button", { name: "取消 Agent", exact: true })
    .first()
    .click();
  await expect
    .poll(() => state.calls.some((c) => c.path.endsWith("/cancel")))
    .toBeTruthy();
  await list.getByLabel("查找项目、Agent、PR 或 Issue").fill("PR #87");
  await expect(
    list.getByRole("button", { name: /PR #87.*详情/ }),
  ).toBeVisible();
  await expect(
    list.getByRole("button", { name: "PR #88 修复兼容问题", exact: true }),
  ).toHaveCount(0);
  await list.getByRole("button", { name: /PR #87.*详情/ }).click();
  await expect(list).toHaveCount(0);
  await expect(page.getByRole("region", { name: "对象详情" })).toBeVisible();
});
test("命令精确跳转、长句发queen、所有快捷键与输入隔离", async ({ page }) => {
  const { state } = await mockStarsHTTP(page);
  await page.goto("/");
  await closeBrief(page);
  await page
    .getByRole("dialog", { name: "列表", exact: true })
    .getByRole("button", { name: "关闭", exact: true })
    .click();
  const say = page.getByLabel("对话与命令");
  await say.fill("SwarmJet 平台");
  await say.press("Enter");
  await say.fill("请检查模型设置和 PR #88 的兼容要求并继续工作");
  await expect(
    page.getByText("回车发给主 Agent", { exact: true }),
  ).toBeVisible();
  await say.press("Enter");
  await expect
    .poll(() =>
      state.calls.some((c) =>
        (JSON.stringify(c.body) || "").includes("请检查模型设置和 PR #88"),
      ),
    )
    .toBeTruthy();
  await say.fill("模型");
  await say.press("Enter");
  await expect(page.getByRole("heading", { name: "平台设置" })).toBeVisible();
  await page.getByRole("button", { name: "关闭平台设置", exact: true }).click();
  for (const [key, name] of [
    ["b", "简报"],
    ["c", "列表"],
    ["l", "图例"],
  ] as const) {
    await page.mouse.click(5, 5);
    await page.keyboard.press(key);
    await expect(page.getByRole("dialog", { name, exact: true })).toBeVisible();
    await page
      .getByRole("dialog", { name, exact: true })
      .getByRole("button", { name: "关闭", exact: true })
      .click();
  }
  await page.keyboard.press("a");
  await expect(page.getByRole("region", { name: "项目面板" })).toBeVisible();
  await page.keyboard.press("Escape");
  await page.keyboard.press(",");
  await expect(page.getByRole("heading", { name: "平台设置" })).toBeVisible();
  await page.keyboard.press("Escape");
  await page.keyboard.press("t");
  await expect(
    page.getByRole("button", { name: "时间", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  await page.keyboard.press("Escape");
  await page.keyboard.press("s");
  await expect(page.locator("html")).toHaveAttribute("data-style", "simple");
  await page.keyboard.press("d");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "night");
  await page.keyboard.press("g");
  await expect
    .poll(() => page.evaluate(() => localStorage.getItem("sj-grid")))
    .not.toBeNull();
  await page.keyboard.press("/");
  await expect(say).toBeFocused();
  await say.fill("bclstgad");
  await page.keyboard.press("Escape");
  await expect(say).toBeFocused();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await say.fill("");
  await say.blur();
  await page.keyboard.press("Control+b");
  await expect(page.getByRole("dialog")).toHaveCount(0);
});
test("手机完成合并、问题答复kind与chainId、409提示", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const mock = await mockStarsHTTP(page);
  await page.goto("/");
  const brief = page.getByRole("dialog", { name: "简报", exact: true });
  await brief.getByLabel("答复 是否继续处理兼容问题？").fill("继续");
  await brief.getByRole("button", { name: "答复", exact: true }).click();
  await expect
    .poll(
      () =>
        mock.state.calls.find(
          (c) =>
            c.method === "POST" &&
            (c.body as { kind?: string })?.kind === "reply",
        )?.body,
    )
    .toMatchObject({
      to: "queen",
      kind: "reply",
      chainId: "decision-chain",
      body: "继续",
    });
  mock.setMergeStatus(409);
  await brief.getByRole("button", { name: "合并", exact: true }).click();
  await brief.getByRole("button", { name: "确认合并 PR #88" }).click();
  await expect(brief.getByRole("alert")).toContainText("PR 有新提交");
  mock.setMergeStatus(200);
  await brief.getByRole("button", { name: "合并", exact: true }).click();
  await brief.getByRole("button", { name: "确认合并 PR #88" }).click();
  await expect.poll(() => mock.prs[0].merged).toBeTruthy();
  expect(
    mock.state.calls
      .filter((c) => c.path.endsWith("/merge"))
      .every(
        (c) => (c.body as { headSha: string }).headSha === "observed-head-88",
      ),
  ).toBeTruthy();
});
test("登录失败原因可读且无严重无障碍问题", async ({ page }, info) => {
  await mockStarsHTTP(page, {
    unauthenticated: true,
    loginFailure: { status: 401, message: "invalid" },
  });
  await page.goto("/");
  await page.getByLabel("用户名").fill("test-user");
  await page.getByLabel("密码").fill("test-only-password");
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(page.getByRole("alert")).toHaveText(
    "用户名或密码不正确，请重新输入。",
  );
  const results = await new AxeBuilder({ page }).analyze();
  await writeFile(
    info.outputPath("login-axe.json"),
    JSON.stringify(
      {
        testEngine: results.testEngine,
        url: results.url,
        timestamp: results.timestamp,
        passes: results.passes.length,
        violations: results.violations,
      },
      null,
      2,
    ),
  );
  await page.screenshot({ path: info.outputPath("login.png") });
  await info.attach("login-axe.json", {
    body: JSON.stringify(results.violations, null, 2),
    contentType: "application/json",
  });
  expect(
    results.violations.filter((v) =>
      ["critical", "serious"].includes(v.impact || ""),
    ),
  ).toEqual([]);
});
test("首次三步真实状态、新建仓库模型过滤并打开Queen", async ({ page }) => {
  const mock = await mockStarsHTTP(page, { empty: true });
  await page.goto("/");
  await page
    .getByRole("dialog", { name: "简报", exact: true })
    .getByRole("button", { name: "关闭", exact: true })
    .click();
  const onboarding = page.getByRole("region", { name: "首次使用" });
  await expect(
    onboarding.getByRole("button", { name: "接入 GitHub，已完成" }),
  ).toBeVisible();
  await expect(
    onboarding.getByRole("button", { name: "填写模型凭证，已完成" }),
  ).toBeVisible();
  await onboarding
    .getByRole("button", { name: "创建第一个项目，待完成" })
    .click();
  const create = page.getByRole("dialog", { name: "创建项目" });
  await expect(create.getByLabel("仓库")).toBeVisible();
  await create.getByLabel("同时工作的 Agent 名额").fill("2");
  await create.getByRole("button", { name: "创建并打开主 Agent" }).click();
  await expect
    .poll(() =>
      mock.state.calls.some(
        (c) => c.method === "POST" && c.path === "/api/hives",
      ),
    )
    .toBeTruthy();
  await expect(create).toHaveCount(0);
  await expect(page.getByRole("region", { name: "对象详情" })).toBeVisible();
});
for (const [configured, available, gateway] of [
  [false, false, true],
  [true, false, true],
  [true, true, false],
] as const)
  test(`首次凭证可用性 ${configured}/${available}/${gateway}`, async ({
    page,
  }) => {
    const mock = await mockStarsHTTP(page, {
      empty: true,
      setup: {
        github: { configured: true, installations: 0 },
        models: { configured, available },
      },
    });
    mock.state.models.gatewayAvailable = gateway;
    await page.goto("/");
    await page
      .getByRole("dialog", { name: "简报", exact: true })
      .getByRole("button", { name: "关闭", exact: true })
      .click();
    const onboarding = page.getByRole("region", { name: "首次使用" });
    await expect(
      onboarding.getByRole("button", { name: "接入 GitHub，待完成" }),
    ).toBeVisible();
    await expect(
      onboarding.getByRole("button", { name: "填写模型凭证，待完成" }),
    ).toBeVisible();
  });
test("命令同号Issue/PR、精确平台词和长句", async () => {
  const hive = hives[0];
  const mock = createMockDataSource({
    [hive.id]: {
      hive,
      agents: [],
      queen: {
        enabled: true,
        state: "active",
        agentId: "q",
        model: "test/model",
        lastTurnAt: null,
      },
      localAddress: "local:test",
      prs: [
        {
          number: 88,
          headSha: "head",
          state: "open",
          ready: true,
          merged: false,
        },
      ],
      signals: [
        {
          messageId: "issue88",
          number: 88,
          title: "issue",
          source: "github",
          kind: "issue",
          url: "",
          claimed: false,
          claimedBy: "",
          createdAt: "",
        },
      ],
      queue: [],
      messages: [],
      methodology: methodologyResponses[3],
      methodologyEvents: [],
    },
  });
  const store = createProjectStore(hive.id, mock.source);
  store.start();
  await new Promise((r) => setTimeout(r, 0));
  const stores = new Map([[hive.id, store]]),
    projects = [
      { hive, activeForagers: 0, pendingApprovals: 0, inboxBacklog: 0 },
    ],
    ui = createStarsUI();
  command("#88", projects, stores, ui)?.run();
  expect(ui.getSnapshot().selection?.kind).toBe("signal");
  command("PR #88", projects, stores, ui)?.run();
  expect(ui.getSnapshot().selection?.kind).toBe("pr");
  expect(command("请把模型设置检查一下", projects, stores, ui)).toBeNull();
  store.stop();
});

test("搜索框Esc退出、空白创建事件清层与焦点归还", async ({ page }) => {
  await mockStarsHTTP(page);
  await page.goto("/");
  await closeBrief(page);
  const list = page.getByRole("dialog", { name: "列表", exact: true });
  await list.getByLabel("查找项目、Agent、PR 或 Issue").focus();
  await page.keyboard.press("Escape");
  await expect(list).toHaveCount(0);
  await page.getByRole("button", { name: "平台设置", exact: true }).click();
  await page.evaluate(() =>
    window.dispatchEvent(new CustomEvent("swarmjet:create-project")),
  );
  const create = page.getByRole("dialog", { name: "创建项目" });
  await expect(create).toBeVisible();
  await expect(page.getByRole("region", { name: "详情面板" })).toHaveCount(0);
  await expect(create.getByRole("button", { name: "关闭创建" })).toBeFocused();
  await create.getByLabel("项目名称").focus();
  await page.keyboard.press("Escape");
  await expect(create).toHaveCount(0);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await expect(create.getByRole("button", { name: "关闭创建" })).toBeFocused();
  await expect(
    create.getByRole("button", { name: "创建并打开主 Agent" }),
  ).toBeEnabled();
  await page.keyboard.press("Shift+Tab");
  await expect(
    create.getByRole("button", { name: "创建并打开主 Agent" }),
  ).toBeFocused();
  await create.getByRole("button", { name: "关闭创建" }).click();
  await expect(
    page.getByRole("button", { name: "创建项目", exact: true }),
  ).toBeFocused();
});
test("独立用量故障仍点亮接入与模型，平台关闭后重新读取可用性", async ({
  page,
}) => {
  const mock = await mockStarsHTTP(page, {
    empty: true,
    failures: { usage: 503 },
  });
  await page.goto("/");
  await page
    .getByRole("dialog", { name: "简报", exact: true })
    .getByRole("button", { name: "关闭", exact: true })
    .click();
  const onboarding = page.getByRole("region", { name: "首次使用" });
  await expect(
    onboarding.getByRole("button", { name: "接入 GitHub，已完成" }),
  ).toBeVisible();
  await expect(
    onboarding.getByRole("button", { name: "填写模型凭证，已完成" }),
  ).toBeVisible();
  await onboarding
    .getByRole("button", { name: "填写模型凭证，已完成" })
    .click();
  mock.state.setup.models.available = false;
  await page.getByRole("button", { name: "关闭平台设置", exact: true }).click();
  await expect(
    onboarding.getByRole("button", { name: "填写模型凭证，待完成" }),
  ).toBeVisible();
});
test("列表显示queue且检查结果不能派成Issue", async ({ page }) => {
  const mock = await mockStarsHTTP(page);
  mock.signals.push({
    ...mock.signals[0],
    messageId: "check-event",
    kind: "check",
    number: 92,
    title: "CI结果",
  });
  await page.goto("/");
  await closeBrief(page);
  const list = page.getByRole("dialog", { name: "列表", exact: true });
  await expect(
    list.getByText(/排队中.*等待处理 Issue #91.*worker.*第 1 位/),
  ).toBeVisible();
  await expect(
    list.getByRole("button", { name: "检查结果 #92 CI结果 / 详情" }),
  ).toBeVisible();
  await expect(
    list.getByRole("button", { name: "交给主 Agent", exact: true }),
  ).toHaveCount(1);
});

test("简报偏好、首次图例自动关闭及受限存储", async ({ page }) => {
  await mockStarsHTTP(page);
  await page.goto("/");
  const brief = page.getByRole("dialog", { name: "简报", exact: true });
  await brief.getByLabel("下次直接进入星图").check();
  await brief.getByRole("button", { name: "进入星图" }).click();
  const legend = page.getByRole("dialog", { name: "图例", exact: true });
  await expect(legend).toBeVisible();
  await expect(legend).toHaveCount(0, { timeout: 9000 });
  await page.reload();
  await expect(brief).toHaveCount(0);
  await page.getByRole("button", { name: "简报", exact: true }).click();
  await expect(brief).toBeVisible();
  await page.addInitScript(() => {
    Storage.prototype.getItem = () => {
      throw Error("storage disabled");
    };
    Storage.prototype.setItem = () => {
      throw Error("storage disabled");
    };
  });
  await page.reload();
  await expect(brief).toBeVisible();
});
test("已有项目按实际列表点亮第三步，GitHub无安装不假装完成", async ({
  page,
}) => {
  await mockStarsHTTP(page, {
    setup: { github: { configured: false, installations: 0 } },
  });
  await page.goto("/");
  await page
    .getByRole("dialog", { name: "简报", exact: true })
    .getByRole("button", { name: "关闭", exact: true })
    .click();
  const onboarding = page.getByRole("region", { name: "首次使用" });
  await expect(
    onboarding.getByRole("button", { name: "创建第一个项目，已完成" }),
  ).toBeVisible();
  await expect(
    onboarding.getByRole("button", { name: "接入 GitHub，待完成" }),
  ).toBeVisible();
});

test("手动打开的图例不会被已关闭的首次图例计时器收起", async ({ page }) => {
  await mockStarsHTTP(page);
  await page.goto("/");
  await page.getByRole("dialog", { name: "简报", exact: true })
    .getByRole("button", { name: "进入星图", exact: true }).click();
  const legend = page.getByRole("dialog", { name: "图例", exact: true });
  await expect(legend).toBeVisible();
  await legend.getByRole("button", { name: "关闭", exact: true }).click();
  await page.waitForTimeout(4500);
  await page.getByRole("button", { name: "图例", exact: true }).click();
  await expect(legend).toBeVisible();
  await page.waitForTimeout(3000);
  await expect(legend).toBeVisible();
});
