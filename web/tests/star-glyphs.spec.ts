import { expect, test } from "@playwright/test";

test("shared PR glyph covers its inner ring with paper and moon orbit is dashed", async ({ page }) => {
  await page.route("**/api/**", (r) => r.fulfill({ status: 401, json: { error: "test session" } }));
  await page.goto("/");
  const result = await page.evaluate(async () => {
    const load = (path: string) => import(/* @vite-ignore */ path);
    const { GLY, LGLY, drawCanvasGlyph } = await load("/src/stars/star-glyphs.ts");
    const canvas = document.createElement("canvas"); canvas.width = canvas.height = 60;
    const ctx = canvas.getContext("2d")!;
    ctx.fillStyle = "#ffffff"; ctx.fillRect(0, 0, 60, 60);
    ctx.translate(30, 30); ctx.strokeStyle = "#000000"; ctx.fillStyle = "#ffffff";
    drawCanvasGlyph(ctx, "pr");
    const center = [...ctx.getImageData(30, 30, 1, 1).data];
    const dashCalls: number[][] = [];
    const original = ctx.setLineDash.bind(ctx);
    ctx.setLineDash = (segments) => { dashCalls.push([...segments]); original(segments); };
    drawCanvasGlyph(ctx, "moon");
    return { center, dashCalls, svgPaper: GLY.pr.includes('fill="var(--paper)"'), svgDash: GLY.moon.includes('stroke-dasharray="1 1.5"'), legendKeys: ["ci", "hist", "cluster", "you"].every((key) => key in LGLY) };
  });
  expect(result.svgPaper).toBe(true);
  expect(result.svgDash).toBe(true);
  expect(result.legendKeys).toBe(true);
  expect(result.center.slice(0, 3)).toEqual([255, 255, 255]);
  expect(result.dashCalls).toContainEqual([1, 1.5]);
});
