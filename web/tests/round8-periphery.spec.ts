import { test, expect } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { mkdir } from "node:fs/promises";
import { mockStarsHTTP } from "./stars-http-mock";

const evidence = new URL("../../docs/claude/round8-periphery/", import.meta.url).pathname;
async function preview(page: import("@playwright/test").Page, theme: "bright" | "night" = "bright") {
  await page.addInitScript((choice) => { localStorage.setItem("sj-theme", choice); localStorage.setItem("sj-legend-seen", "1"); }, theme);
  await page.goto("/round8-periphery-preview.html");
  await expect(page.getByRole("dialog", { name: "简报" })).toBeVisible();
}

test("48 Agent、19 PR、13 Issue 与 900 消息保持单行密度和历史可检索", async ({ page }) => {
  await preview(page);
  const brief = page.getByRole("dialog", { name: "简报" });
  await expect(brief.locator(".need")).toHaveCount(3);
  await expect(brief.getByText(/还有 \d+ 个/).first()).toBeVisible();
  const labels = await brief.locator(".periphery-it-label").allTextContents();
  expect(labels.every((label) => label.length < 100)).toBeTruthy();
  await brief.getByRole("button", { name: "知道了" }).first().click();
  await expect(brief.getByRole("button", { name: "知道了" })).toHaveCount(0);
  await brief.getByRole("button", { name: "进入星图" }).click();
  await page.evaluate(() => window.dispatchEvent(new CustomEvent("swarmjet:catalogue-open", { detail: { scope: "history", query: "forager-00000023" } })));
  const list = page.getByRole("dialog", { name: "列表" });
  await expect(list.getByRole("button", { name: "历史" })).toHaveAttribute("aria-pressed", "true");
  await expect(list.locator("tr.item")).toHaveCount(1);
  await expect(list.locator("tr.item").first()).toContainText("Issue");
  await expect(list.locator("tr.item form")).toHaveCount(0);
});

test("1440×900 简报需要项与留意项同屏，进入后推近待决定项目", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await preview(page);
  const brief = page.getByRole("dialog", { name: "简报" });
  const need = brief.getByRole("heading", { name: "需要你", exact: true });
  const attention = brief.getByRole("button", { name: "知道了" });
  await expect(need).toBeVisible();
  await expect(attention).toBeVisible();
  const end = await attention.boundingBox();
  expect(end).not.toBeNull();
  expect(end!.y + end!.height).toBeLessThanOrEqual(900);
  expect(await brief.evaluate((element) => element.scrollTop)).toBe(0);
  await brief.getByRole("button", { name: "进入星图" }).click();
  await expect(page.getByRole("navigation", { name: "位置" })).toContainText("test-for-myself");
  await expect(page.locator(".stars-canvas")).toHaveAttribute("data-mode", "project");
});

test("375px 和快捷键可操作", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 812 });
  await preview(page);
  await expect(page.getByRole("dialog", { name: "简报" })).toBeVisible();
  const width = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(width).toBeLessThanOrEqual(375);
  await page.getByRole("button", { name: "进入星图" }).click();
  await page.keyboard.press("c");
  await expect(page.getByRole("dialog", { name: "列表" })).toBeVisible();
  await page.keyboard.press("Escape");
  await page.keyboard.press("l");
  await expect(page.getByRole("dialog", { name: "图例" })).toBeVisible();
  await page.keyboard.press("Escape");
  await page.keyboard.press("/");
  await expect(page.getByLabel("对话与命令")).toBeFocused();
});

test("真实 axe 对每种外围界面检查所有 violations", async ({ page }) => {
  await preview(page);
  for (const name of ["简报", "列表", "图例", "显示"] as const) {
    if (name === "列表") await page.getByRole("button", { name: "进入星图" }).click();
    if (name !== "简报") await page.getByRole("button", { name }).click();
    const result = await new AxeBuilder({ page }).analyze();
    expect(result.violations, `${name}: ${JSON.stringify(result.violations)}`).toEqual([]);
    if (name !== "简报") await page.keyboard.press("Escape");
  }
});

test("登录、命令和断线状态的 axe violations 为零", async ({ browser }) => {
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const login = await context.newPage();
  await mockStarsHTTP(login, { unauthenticated: true });
  await login.goto("/");
  await expect(login.getByRole("heading", { name: "登录 SwarmJet" })).toBeVisible();
  await expect(login.locator(".stars-login")).toHaveCSS("opacity", "1");
  expect((await new AxeBuilder({ page: login }).analyze()).violations).toEqual([]);
  await login.close();
  const page = await context.newPage();
  const mock = await mockStarsHTTP(page);
  await page.goto("/");
  await page.getByRole("button", { name: "进入星图" }).click();
  await page.getByLabel("对话与命令").fill("列表");
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.getByLabel("对话与命令").fill("");
  await mock.disconnect();
  await expect(page.getByRole("alert").filter({ hasText: "连接已断开" })).toBeVisible();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await context.close();
});

test("明亮与夜空逐界面保存 1440×900 证据", async ({ browser }) => {
  await mkdir(evidence, { recursive: true });
  for (const theme of ["bright", "night"] as const) {
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, reducedMotion: "reduce" });
    const page = await context.newPage();
    await preview(page, theme);
    const shots = ["brief", "list", "legend", "display"] as const;
    for (const shot of shots) {
      if (shot === "list") await page.getByRole("button", { name: "进入星图" }).click();
      if (shot !== "brief") await page.getByRole("button", { name: { list: "列表", legend: "图例", display: "显示" }[shot] }).click();
      await page.waitForTimeout(600);
      await page.screenshot({ path: `${evidence}app-${theme}-${shot}.png` });
      if (shot !== "brief") await page.keyboard.press("Escape");
    }
    await context.close();
    const reference = await browser.newContext({ viewport: { width: 1440, height: 900 }, reducedMotion: "reduce" });
    await reference.addInitScript((choice) => { localStorage.setItem("sj-theme", choice); localStorage.removeItem("sj-brief"); localStorage.setItem("sj-legend-seen", "1"); }, theme);
    const design = await reference.newPage();
    await design.goto("http://localhost:4731/");
    await design.waitForTimeout(1000);
    for (const shot of shots) {
      if (shot === "list") await design.locator("#b-enter").click();
      if (shot !== "brief") await design.locator(`#t-${shot}`).click();
      if (shot === "list") await expect(design.locator("#catalogue")).toHaveClass(/open/);
      if (shot === "legend" || shot === "display") await expect(design.locator(`#${shot}`)).toHaveClass(/open/);
      await design.waitForTimeout(700);
      await design.screenshot({ path: `${evidence}design-${theme}-${shot}.png` });
      if (shot === "list") await design.locator("#cat-x").click();
      else if (shot !== "brief") await design.locator(`#t-${shot}`).click();
    }
    await reference.close();
  }
});

test("登录、命令与断线提示保存明亮和夜空证据", async ({ browser }) => {
  await mkdir(evidence, { recursive: true });
  for (const theme of ["bright", "night"] as const) {
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, reducedMotion: "reduce" });
    await context.addInitScript((choice) => { localStorage.setItem("sj-theme", choice); localStorage.setItem("sj-legend-seen", "1"); }, theme);
    const login = await context.newPage();
    await mockStarsHTTP(login, { unauthenticated: true });
    await login.goto("/");
    await expect(login.getByRole("heading", { name: "登录 SwarmJet" })).toBeVisible();
    await login.screenshot({ path: `${evidence}app-${theme}-login.png` });
    await login.close();
    const page = await context.newPage();
    const mock = await mockStarsHTTP(page);
    await page.goto("/");
    await page.getByRole("button", { name: "进入星图" }).click();
    await page.getByLabel("对话与命令").fill("列表");
    await page.screenshot({ path: `${evidence}app-${theme}-command.png` });
    await page.getByLabel("对话与命令").fill("");
    await mock.disconnect();
    await expect(page.getByRole("alert").filter({ hasText: "连接已断开" })).toBeVisible();
    await page.screenshot({ path: `${evidence}app-${theme}-disconnect.png` });
    const design = await context.newPage();
    await design.goto("http://localhost:4731/");
    await design.locator("#b-enter").click();
    await design.locator("#say").fill("列表");
    await design.waitForTimeout(600);
    await design.screenshot({ path: `${evidence}design-${theme}-command.png` });
    await context.close();
  }
});
