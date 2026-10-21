import { test, expect } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { mockStarsHTTP } from "./stars-http-mock";

async function boot(page: import("@playwright/test").Page) {
  const mock = await mockStarsHTTP(page);
  await page.goto("/");
  await expect(page.getByRole("dialog", { name: "简报" })).toBeVisible();
  return mock;
}

test("简报逐段显示，决定最多三项，旧失败不再堆积", async ({ page }) => {
  const mock = await boot(page);
  const brief = page.getByRole("dialog", { name: "简报" });
  for (const title of ["需要你", "正在进行", "需要留意", "昨晚完成"])
    await expect(brief.getByRole("heading", { name: title, exact: true })).toBeVisible();
  await expect(brief.getByText("PR #88 修复兼容问题")).toBeVisible();
  await expect(brief.locator(".periphery-brief-body .need")).toHaveCount(2);
  await expect(brief.getByRole("button", { name: "知道了" })).toHaveCount(2);
  await brief.getByRole("button", { name: "知道了" }).first().click();
  await expect(brief.getByRole("button", { name: "知道了" })).toHaveCount(1);
  await brief.getByRole("button", { name: "知道了" }).click();
  await expect(brief.getByRole("button", { name: "知道了" })).toHaveCount(0);
  await expect(page.getByRole("alert").filter({ hasText: "连接已断开" })).toHaveCount(0);
  await mock.disconnect();
  await expect(page.getByRole("alert").filter({ hasText: "连接已断开" })).toBeVisible();
});

test("列表可搜索、看历史并打开详情", async ({ page }) => {
  await boot(page);
  await page.getByRole("button", { name: "进入星图" }).click();
  await page.getByRole("button", { name: "列表", exact: true }).click();
  const list = page.getByRole("dialog", { name: "列表" });
  await expect(list).toBeVisible();
  await expect(list.getByText("PR #88 修复兼容问题")).toBeVisible();
  await expect(list.locator(".periphery-list .item form")).toHaveCount(0);
  await list.getByRole("button", { name: "今天" }).click();
  await expect(list.getByText("PR #87 完成文档")).toBeVisible();
  await list.getByLabel("查找项目、Agent 或 PR").fill("PR #87");
  await expect(list.locator("tr.item")).toHaveCount(1);
  await list.locator("tr.item").click();
  await expect(page.getByRole("region", { name: "对象详情" })).toBeVisible();
  await expect(list).toBeHidden();
});

test("命令、快捷键和文字显示菜单", async ({ page }) => {
  const mock = await boot(page);
  await page.getByRole("button", { name: "进入星图" }).click();
  const say = page.getByLabel("对话与命令");
  await say.fill("SwarmJet 平台");
  await say.press("Enter");
  await say.fill("请继续检查兼容要求");
  await say.press("Enter");
  await expect.poll(() => mock.state.calls.some((call) => (JSON.stringify(call.body) || "").includes("请继续检查兼容要求"))).toBeTruthy();
  await say.fill("显示");
  await say.press("Enter");
  const display = page.getByRole("dialog", { name: "显示" });
  await expect(display).toBeVisible();
  await expect(display.locator("select,input")).toHaveCount(0);
  await display.getByRole("button", { name: "简洁" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-style", "simple");
  await display.getByRole("button", { name: "夜空" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "night");
  await page.keyboard.press("Escape");
  await page.keyboard.press("l");
  await expect(page.getByRole("dialog", { name: "图例" })).toBeVisible();
  await expect(page.getByRole("dialog", { name: "图例" }).locator("svg")).toHaveCount(13);
  await page.keyboard.press("Escape");
  await page.keyboard.press("/");
  await expect(say).toBeFocused();
});

test("同号 Issue 与 PR 命令消歧；发送期间的新草稿保留", async ({ page }) => {
  const mock = await boot(page);
  await page.getByRole("button", { name: "进入星图" }).click();
  const say = page.getByLabel("对话与命令");
  await say.fill("PR #88");
  await say.press("Enter");
  await expect(page.getByTestId("reviewed-pr-head")).toHaveText("observed-head-88");
  await page.getByRole("button", { name: "关闭对象详情" }).click();
  await say.fill("#88");
  await say.press("Enter");
  await expect(page.getByRole("region", { name: "对象详情" })).toContainText("处理输入兼容");
  await page.getByRole("button", { name: "关闭对象详情" }).click();
  let release!: () => void;
  const pending = new Promise<void>((resolve) => { release = resolve; });
  await page.route("**/api/hives/swarmjet/messages", async (route) => {
    if (route.request().method() === "POST") await pending;
    await route.fallback();
  });
  await say.fill("第一条任务");
  await say.press("Enter");
  await say.fill("后来输入的新草稿");
  release();
  await expect.poll(() => mock.state.calls.some((call) => (JSON.stringify(call.body) || "").includes("第一条任务"))).toBeTruthy();
  await expect(say).toHaveValue("后来输入的新草稿");
});

test("无障碍没有 violations", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await boot(page);
  for (const name of ["简报", "列表", "图例", "显示"]) {
    if (name === "列表") await page.getByRole("button", { name: "进入星图" }).click();
    if (name !== "简报") await page.getByRole("button", { name }).click();
    const result = await new AxeBuilder({ page }).analyze();
    expect(result.violations, `${name}: ${JSON.stringify(result.violations)}`).toEqual([]);
    if (name !== "简报") await page.keyboard.press("Escape");
  }
});

test("弹出层和断线提示保留退场节点但不可聚焦", async ({ page }) => {
  const mock = await boot(page);
  await page.getByRole("button", { name: "进入星图" }).click();
  await page.getByRole("button", { name: "显示" }).click();
  const display = page.locator(".periphery-display");
  await expect(display).toHaveClass(/open/);
  await page.keyboard.press("Escape");
  await expect(display).not.toHaveClass(/open/);
  await expect(display).toHaveAttribute("inert", "");
  await mock.disconnect();
  const banner = page.locator(".stars-status");
  await expect(banner).toHaveClass(/open/);
  await page.evaluate(() => (window as unknown as { __starsStream: { ready(): void } }).__starsStream.ready());
  await expect(banner).not.toHaveClass(/open/);
  await expect(banner).toHaveAttribute("inert", "");
  await expect(banner).toHaveCSS("opacity", "0");
});

test("品牌、题签和五项工具按设计导航", async ({ page }) => {
  await boot(page);
  await page.getByRole("button", { name: "进入星图" }).click();
  const tools = page.getByRole("navigation", { name: "工具" });
  await expect(tools.getByRole("button")).toHaveCount(5);
  await expect(page.locator(".stars-brand")).toHaveAttribute("aria-label", "SwarmJet 平台设置");
  await page.locator(".stars-brand").click();
  await expect(page.getByRole("dialog", { name: "平台设置" })).toBeVisible();
  await page.getByRole("button", { name: "关闭平台设置" }).click();
  await expect(page.locator(".stars-cartouche")).toHaveAccessibleName(/SwarmJet 平台.*项目面板/);
  await page.locator(".stars-cartouche").click();
  await expect(page.getByRole("region", { name: "项目面板" })).toBeVisible();
  await page.getByRole("button", { name: "关闭项目面板" }).click();
  await page.getByRole("navigation", { name: "位置" }).getByRole("button", { name: "全部项目" }).click();
  await expect(page.locator(".stars-cartouche")).toHaveAccessibleName(/全部项目.*浏览所有项目/);
  await page.locator(".stars-cartouche").click();
  await expect(page.getByRole("dialog", { name: "列表" })).toBeVisible();
});

test("命令栏不显示额外按钮，Enter 仍执行命令", async ({ page }) => {
  await boot(page);
  await page.getByRole("button", { name: "进入星图" }).click();
  const say = page.getByLabel("对话与命令");
  await say.fill("模型");
  const submit = page.locator(".stars-say-submit");
  await expect(submit).toBeEnabled();
  expect((await submit.boundingBox())?.width).toBeLessThanOrEqual(1);
  await say.press("Enter");
  await expect(page.getByRole("dialog", { name: "平台设置" })).toBeVisible();
});

test("面板首次打开后宿主保留，关闭立即不可聚焦", async ({ page }) => {
  await boot(page);
  await page.getByRole("button", { name: "进入星图" }).click();
  await expect(page.locator(".stars-panels-host")).toHaveCount(0);
  await page.locator(".stars-cartouche").click();
  const host = page.locator(".stars-panels-host");
  await expect(host).toHaveCount(1);
  await expect(host).not.toHaveAttribute("inert", "");
  await expect(page.getByRole("region", { name: "项目面板" })).toBeVisible();
  await page.getByRole("button", { name: "关闭项目面板" }).click();
  await expect(host).toHaveCount(1);
  await expect(host).toHaveAttribute("inert", "");
  await expect(host).toHaveAttribute("aria-hidden", "true");
});
