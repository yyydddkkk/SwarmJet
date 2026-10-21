import { expect, test, type Page } from "@playwright/test";

test.use({ viewport: { width: 1440, height: 900 } });

async function boot(page: Page) {
  await page.addInitScript(() => localStorage.setItem("sj-legend-seen", "1"));
  await page.goto("/tests/round8-joint-preview.html");
  await page.getByRole("button", { name: "进入星图", exact: true }).click();
  await expect(page.locator(".stars-canvas")).toHaveAttribute("data-mode", "project");
}
const outline = (page: Page) => page.evaluate(() => {
  const style = getComputedStyle(document.activeElement!);
  return { tag: document.activeElement!.tagName, style: style.outlineStyle, width: parseFloat(style.outlineWidth) };
});

test("mouse-opened panels and inputs show no focus box, keyboard focus stays visible", async ({ page }) => {
  await boot(page);
  await page.getByRole("button", { name: "列表", exact: true }).click();
  const list = page.getByRole("dialog", { name: "列表", exact: true });
  await expect(list.getByRole("textbox")).toBeFocused();
  expect((await outline(page)).style).toBe("none");
  await page.keyboard.press("Escape");
  await page.locator(".stars-cartouche").click();
  const close = page.getByRole("button", { name: "关闭项目面板", exact: true });
  await expect(close).toBeVisible();
  await page.waitForTimeout(900);
  await close.focus();
  expect((await outline(page)).style).toBe("none");
  await page.keyboard.press("Shift+Tab");
  await page.keyboard.press("Tab");
  const keyboard = await outline(page);
  expect(keyboard.tag).toBe("BUTTON");
  expect(keyboard.style).toBe("solid");
});

test("Escape leaves the command line so single-key shortcuts work again", async ({ page }) => {
  await boot(page);
  const input = page.getByRole("textbox", { name: "对话与命令" });
  await page.keyboard.press("/");
  await expect(input).toBeFocused();
  await page.keyboard.type("x");
  await page.keyboard.press("Escape");
  await expect(input).not.toBeFocused();
  await page.keyboard.press("l");
  await expect(page.getByRole("dialog", { name: "图例", exact: true })).toBeVisible();
  await expect(input).toHaveValue("x");
});

test("PR detail puts the decision before CI and verification and keeps agent ids out of rows", async ({ page }) => {
  await boot(page);
  await page.getByRole("button", { name: "列表", exact: true }).click();
  const list = page.getByRole("dialog", { name: "列表", exact: true });
  await list.getByRole("textbox").fill("PR");
  await list.locator("tr.item").first().click();
  const detail = page.getByRole("region", { name: "对象详情", exact: true });
  const title = detail.getByRole("heading", { level: 2 });
  await expect(title).toHaveAttribute("title", /.+/);
  const merge = detail.getByRole("button", { name: "确认合并", exact: true });
  await expect(merge).toBeVisible();
  const mergeBox = await merge.boundingBox();
  const ci = await detail.getByRole("heading", { name: "GitHub CI", exact: true }).boundingBox();
  const verify = await detail.getByRole("heading", { name: "独立验证", exact: true }).boundingBox();
  expect(mergeBox!.y).toBeLessThan(ci!.y);
  expect(mergeBox!.y).toBeLessThan(verify!.y);
  expect(await merge.evaluate((el) => getComputedStyle(el).borderBottomStyle === "solid")).toBe(true);
  for (const row of await detail.locator(".pc-attestation > p").allTextContents()) expect(row).not.toMatch(/[0-9a-f]{6,}/);
});
