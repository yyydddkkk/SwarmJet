import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { mockStarsHTTP } from "./stars-http-mock";

test.use({ viewport: { width: 1440, height: 1000 } });
const evidence = resolve("../docs/claude/round7-preview");

async function enter(page: Page) {
  await page.getByRole("dialog", { name: "简报", exact: true })
    .getByRole("button", { name: "进入星图", exact: true }).click();
  const legend = page.getByRole("dialog", { name: "图例", exact: true });
  await expect(legend).toBeVisible();
  await legend.getByRole("button", { name: "关闭", exact: true }).click();
}

async function settle(page: Page) {
  await page.evaluate(() => document.fonts.ready.then(() => undefined));
  let previous = -1;
  await expect.poll(async () => {
    const frames = Number(await page.locator(".stars-canvas").getAttribute("data-frames"));
    const stopped = frames > 0 && frames === previous;
    previous = frames;
    return stopped;
  }).toBe(true);
}

test("联合桌面入口连接真实详情、项目设置和平台账号", async ({ page }) => {
  const { state } = await mockStarsHTTP(page);
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto("/");
  const brief = page.getByRole("dialog", { name: "简报", exact: true });
  for (const name of ["需要你", "正在进行", "需要留意", "昨晚完成"])
    await expect(brief.getByRole("heading", { name, exact: true })).toBeVisible();
  await brief.getByRole("button", { name: /PR #88 修复兼容问题/ }).click();
  await expect(page.getByTestId("reviewed-pr-head")).toHaveText("observed-head-88");
  await page.getByRole("button", { name: "关闭对象详情", exact: true }).click();
  await page.getByLabel("选择项目", { exact: true }).selectOption("swarmjet");
  await page.getByRole("button", { name: "项目面板", exact: true }).click();
  const project = page.getByRole("region", { name: "项目面板", exact: true });
  await project.getByRole("button", { name: "设置", exact: true }).click();
  await project.getByLabel("并发上限", { exact: true }).fill("5");
  await project.getByRole("button", { name: "保存设置", exact: true }).click();
  await expect.poll(() => state.calls.filter((c) => c.method === "PATCH").length).toBe(1);
  const patch = state.calls.find((c) => c.method === "PATCH")!;
  expect(patch.body).toMatchObject({ config: { maxConcurrentForagers: 5 } });
  expect(patch.headers["x-requested-with"]).toBe("swarmjet");
  await page.getByRole("button", { name: "关闭项目面板", exact: true }).click();
  await page.getByRole("button", { name: "平台设置", exact: true }).click();
  const platform = page.getByRole("dialog", { name: "平台设置", exact: true });
  await platform.getByRole("button", { name: "账簿", exact: true }).click();
  await expect(platform.getByRole("heading", { name: "账簿", exact: true })).toBeVisible();
  await platform.getByRole("button", { name: "账号", exact: true }).click();
  await platform.getByRole("button", { name: "退出登录", exact: true }).click();
  await expect(page.getByRole("heading", { name: "登录 SwarmJet", exact: true })).toBeVisible();
  expect(state.calls.filter((c) => c.path === "/api/logout")).toHaveLength(1);
  expect(errors).toEqual([]);
});

test("手机从观测按钮进入真实PR面板完成合并并回答问题", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const { state, prs } = await mockStarsHTTP(page);
  await page.goto("/");
  await enter(page);
  await page.locator(".stars-observer").click();
  await page.getByRole("region", { name: "等你决定的事", exact: true })
    .getByRole("button", { name: /PR #88/ }).click();
  await expect(page.getByTestId("reviewed-pr-head")).toHaveText("observed-head-88");
  await page.getByRole("button", { name: "确认合并", exact: true }).click();
  await expect.poll(() => prs[0].merged).toBe(true);
  const merges = state.calls.filter((c) => c.path.endsWith("/prs/88/merge"));
  expect(merges).toHaveLength(1);
  expect(merges[0].body).toEqual({ headSha: "observed-head-88" });
  await page.getByRole("button", { name: "关闭对象详情", exact: true }).click();
  await page.locator(".stars-observer").click();
  await page.getByRole("region", { name: "等你决定的事", exact: true })
    .getByRole("button", { name: /回答主 Agent的问题/ }).click();
  const question = page.locator(".pc-messages li").filter({ hasText: "是否继续处理兼容问题？" });
  await question.getByRole("button", { name: "回复这条提问", exact: true }).click();
  await page.getByRole("textbox", { name: "对主 Agent 说", exact: true }).fill("可以继续，请保持原有行为。");
  await page.locator(".pc-right").getByRole("button", { name: "发送", exact: true }).click();
  await expect.poll(() => state.calls.some((c) => c.path.endsWith("/messages") &&
    (c.body as { body?: string } | undefined)?.body === "可以继续，请保持原有行为。")).toBe(true);
  const reply = state.calls.find((c) => (c.body as { body?: string } | undefined)?.body === "可以继续，请保持原有行为。")!;
  expect(reply.body).toMatchObject({ to: "queen", kind: "reply", chainId: "decision-chain" });
});

test("时间控件可操作，断线提示在项目面板上可见并保持至重新同步完成", async ({ page }) => {
  const fixture = await mockStarsHTTP(page);
  await page.goto("/");
  await enter(page);
  await page.getByLabel("选择项目", { exact: true }).selectOption("swarmjet");
  await page.getByRole("button", { name: "时间", exact: true }).click();
  await expect(page.getByText(/示意回放，不代表真实历史/)).toBeVisible();
  await page.getByRole("slider", { name: "示意时间", exact: true }).press("Home");
  await page.getByRole("button", { name: "曝光", exact: true }).click();
  await expect(page.getByRole("button", { name: "暂停", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "关闭时间轴", exact: true }).click();
  await page.getByRole("button", { name: "项目面板", exact: true }).click();
  await expect(page.getByTestId("methodology-version")).toBeVisible();
  await page.evaluate(() => (window as any).__starsStream.pauseReady());
  fixture.state.hives[0].hive.name = "恢复后的项目";
  await fixture.disconnect();
  const banner = page.getByRole("alert").filter({ hasText: "连接已断开，正在重连" });
  await expect(banner).toBeVisible();
  await banner.getByRole("button", { name: "重试", exact: true }).click();
  await expect(page.getByRole("region", { name: "项目面板", exact: true })
    .getByRole("heading", { name: "恢复后的项目", exact: true })).toBeVisible();
  await expect(banner).toBeVisible();
  await expect.poll(() => page.evaluate(() => (window as any).__starsStream.count())).toBe(1);
  await page.evaluate(() => (window as any).__starsStream.ready());
  await expect(banner).toHaveCount(0);
});

test("完整mock界面的主题截图及登录简报列表无障碍证据", async ({ page }) => {
  mkdirSync(evidence, { recursive: true });
  const { state } = await mockStarsHTTP(page);
  for (const [index, agent] of state.agents.swarmjet.entries()) {
    if (agent.role === "forager") {
      agent.task = ["修复输入兼容问题", "检查边界测试", "核对文档说明", "等待合并确认"][index % 4];
      agent.issueNumber = index + 12;
    }
  }
  async function audit(name: string) {
    const result = await new AxeBuilder({ page }).analyze();
    writeFileSync(resolve(evidence, `${name}-axe.json`), JSON.stringify({
      testEngine: result.testEngine, timestamp: result.timestamp,
      passes: result.passes.length, violations: result.violations,
    }, null, 2));
    expect(result.violations.filter((v) => ["serious", "critical"].includes(v.impact ?? ""))).toEqual([]);
  }
  await page.goto("/");
  await expect(page.getByRole("dialog", { name: "简报", exact: true })).toBeVisible();
  await settle(page);
  await audit("brief");
  await page.screenshot({ path: resolve(evidence, "brief.png") });
  await enter(page);
  await page.getByLabel("选择项目", { exact: true }).selectOption("swarmjet");
  for (const [theme, style] of [["bright", "classic"], ["paper", "classic"], ["night", "classic"], ["bright", "simple"]]) {
    await page.getByRole("button", { name: "显示", exact: true }).click();
    const display = page.getByRole("dialog", { name: "显示设置", exact: true });
    await display.getByLabel("主题", { exact: true }).selectOption(theme);
    await display.getByLabel("风格", { exact: true }).selectOption(style);
    await display.getByRole("button", { name: "关闭", exact: true }).click();
    await settle(page);
    await page.screenshot({ path: resolve(evidence, `${theme}-${style}.png`) });
  }
  await page.getByRole("button", { name: "项目面板", exact: true }).click();
  await expect(page.getByTestId("methodology-version")).toBeVisible();
  await page.screenshot({ path: resolve(evidence, "project-panel.png") });
  await page.getByRole("button", { name: "平台设置", exact: true }).click();
  const platform = page.getByRole("dialog", { name: "平台设置", exact: true });
  await expect(platform.getByRole("heading", { name: "GitHub", exact: true })).toBeVisible();
  await expect(platform.getByRole("heading", { name: "平台设置", exact: true })).toHaveCSS("font-style", "normal");
  await page.screenshot({ path: resolve(evidence, "platform-settings.png") });
  await page.getByRole("button", { name: "关闭平台设置", exact: true }).click();
  await page.getByRole("button", { name: "列表", exact: true }).click();
  await audit("list");
  await page.screenshot({ path: resolve(evidence, "list.png") });
  await page.getByRole("dialog", { name: "列表", exact: true })
    .getByRole("button", { name: "关闭", exact: true }).click();
  await page.getByRole("button", { name: "平台设置", exact: true }).click();
  await platform.getByRole("button", { name: "账号", exact: true }).click();
  await platform.getByRole("button", { name: "退出登录", exact: true }).click();
  await expect(page.getByRole("heading", { name: "登录 SwarmJet", exact: true })).toBeVisible();
  await audit("login");
  await page.screenshot({ path: resolve(evidence, "login.png") });
});
