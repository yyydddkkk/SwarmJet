import { expect, test } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";

type Color = [number, number, number, number];
function color(css: string): Color {
  if (css.startsWith("#")) return [1, 3, 5].map(i => parseInt(css.slice(i, i + 2), 16)).concat(1) as Color;
  const values = css.match(/[\d.]+/g)!.map(Number);
  return [values[0], values[1], values[2], values[3] ?? 1];
}
function luminance(rgb: number[]) {
  const channel = rgb.map(v => v / 255).map(v => v <= .04045 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4);
  return .2126 * channel[0] + .7152 * channel[1] + .0722 * channel[2];
}
function minimumContrast(foreground: Color, background: Color) {
  // Bound every RGB pixel after the veil/dimming and the text compositing.
  // Independent luminance extrema give a conservative lower bound, including
  // intermediate colors rather than assuming the minimum occurs at a corner.
  const bgLow = background.slice(0, 3).map(v => v * background[3]);
  const bgHigh = bgLow.map(v => v + 255 * (1 - background[3]));
  const fgLow = foreground.slice(0, 3).map((v, i) => v * foreground[3] + bgLow[i] * (1 - foreground[3]));
  const fgHigh = foreground.slice(0, 3).map((v, i) => v * foreground[3] + bgHigh[i] * (1 - foreground[3]));
  const [b0, b1, f0, f1] = [bgLow, bgHigh, fgLow, fgHigh].map(luminance);
  if (b0 >= f1) return (b0 + .05) / (f1 + .05);
  if (f0 >= b1) return (f0 + .05) / (b1 + .05);
  return 1;
}

test("auxiliary text has sufficient contrast over every possible dimmed canvas pixel", async ({ page }) => {
  await page.emulateMedia({reducedMotion: "reduce"});
  const report: Record<string, unknown> = {};
  for (const theme of ["bright", "night"] as const) {
    await page.addInitScript(theme => {
      localStorage.setItem("sj-theme", theme);
      localStorage.setItem("sj-legend-seen", "1");
    }, theme);
    await page.goto("/tests/round8-joint-preview.html");
    await expect(page.locator(".periphery-brief.open")).toHaveCSS("opacity", "1");
    await expect(page.locator(".stars-canvas")).toHaveCSS("opacity", "0.13");
    const paper = color(await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue("--paper").trim()));
    const canvasOpacity = Number(await page.locator(".stars-canvas").evaluate(el => getComputedStyle(el).opacity));
    const background: Color = [paper[0], paper[1], paper[2], 1 - canvasOpacity];
    for (const selector of [".periphery-brief h3", ".periphery-it-state", ".periphery-attention .ack", ".periphery-muted", ".periphery-foot label"]) {
      const foreground = color(await page.locator(selector).first().evaluate(el => getComputedStyle(el).color));
      const minimum = minimumContrast(foreground, background);
      report[`${theme}:${selector}`] = {foreground, background, minimum};
      expect(minimum, `${theme} ${selector}`).toBeGreaterThanOrEqual(4.5);
    }
    await page.getByRole("button", {name: "进入星图", exact: true}).click();
    await page.getByRole("button", {name: "显示", exact: true}).click();
    const menu = page.locator(".periphery-display");
    await expect(menu).toHaveCSS("opacity", "1");
    const veil = color(await menu.evaluate(el => getComputedStyle(el).backgroundColor));
    const foreground = color(await menu.locator("button:not(.on)").first().evaluate(el => getComputedStyle(el).color));
    const minimum = minimumContrast(foreground, veil);
    report[`${theme}:display`] = {foreground, background: veil, minimum};
    expect(minimum, `${theme} display`).toBeGreaterThanOrEqual(4.5);
  }
  const directory = resolve(process.cwd(), "../docs/claude/round8-integration");
  mkdirSync(directory, {recursive: true});
  writeFileSync(resolve(directory, "contrast-bounds.json"), JSON.stringify(report, null, 2));
});
