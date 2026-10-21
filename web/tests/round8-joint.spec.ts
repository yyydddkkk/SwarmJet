import { expect, test, type Page } from "@playwright/test";

test.use({ viewport: { width: 1440, height: 900 }, reducedMotion: "reduce" });

async function boot(page: Page) {
  await page.addInitScript(() => localStorage.setItem("sj-legend-seen", "1"));
  await page.goto("/tests/round8-joint-preview.html");
  await page.getByRole("button", { name: "进入星图", exact: true }).click();
  await expect(page.locator(".stars-canvas")).toHaveAttribute("data-mode", "project");
  await page.waitForTimeout(900); // Wait for the design's camera easing to settle.
}

async function position(page: Page, id: string) {
  return page.locator(".stars-canvas").evaluate((canvas, id) => {
    return (canvas as HTMLCanvasElement & { starMapMetrics: { positions: Record<string, { x: number; y: number }> } }).starMapMetrics.positions[id];
  }, id);
}

test("history and cluster cards open the corresponding catalogue scope and object", async ({ page }) => {
  await boot(page);
  const history = await page.locator(".stars-canvas").evaluate((canvas) => {
    const points = (canvas as HTMLCanvasElement & { starMapMetrics: { positions: Record<string, { x: number; y: number }> } }).starMapMetrics.positions;
    return Object.entries(points).filter(([id, p]) => id.startsWith("history:") && p.x > 80 && p.x < 1360 && p.y > 120 && p.y < 690)
      .map(([id, p]) => ({ id: id.slice(8), ...p }))[0];
  });
  expect(history).toBeTruthy();
  await page.mouse.click(history.x, history.y);
  await page.locator(".stars-note.open").getByRole("button", { name: "在列表中查看", exact: true }).click();
  const list = page.getByRole("dialog", { name: "列表", exact: true });
  await expect(list.getByRole("button", { name: "历史", exact: true })).toHaveAttribute("aria-pressed", "true");
  await expect(list.getByRole("textbox")).toHaveValue(history.id);
  await expect(list.locator("tr.item")).toHaveCount(1);
  await list.locator("tr.item").click();
  const detail = page.getByRole("region", { name: "对象详情", exact: true });
  await expect(detail.getByRole("button", { name: new RegExp(`完整值 ${history.id}`) })).toBeVisible();
  await detail.getByRole("button", { name: "关闭对象详情", exact: true }).click();
  await expect(page.locator(".pc-right")).toHaveCount(0);
  await page.waitForTimeout(900);
  const cluster = await position(page, "swarmjet:cluster:pr");
  expect(cluster).toBeTruthy();
  await page.mouse.click(cluster.x, cluster.y);
  await page.locator(".stars-note.open").getByRole("button", { name: "在列表中查看全部", exact: true }).click();
  await expect(list.getByRole("button", { name: "活跃", exact: true })).toHaveAttribute("aria-pressed", "true");
  await expect(list.getByRole("textbox")).toHaveValue("PR");
  expect(await list.locator("tr.item").count()).toBeGreaterThanOrEqual(16);
});

test("acknowledging a failure removes it from the map and preserves its historical detail", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("sj-legend-seen", "1"));
  await page.goto("/tests/round8-joint-preview.html");
  const canvas = page.locator(".stars-canvas");
  await expect.poll(async () => Number(await canvas.getAttribute("data-nodes"))).toBeGreaterThan(0);
  const before = Number(await canvas.getAttribute("data-nodes"));
  await page.getByRole("button", { name: "知道了", exact: true }).click();
  await expect.poll(async () => Number(await canvas.getAttribute("data-nodes"))).toBe(before - 1);
  await page.getByRole("button", { name: "进入星图", exact: true }).click();
  await page.getByRole("button", { name: "列表", exact: true }).click();
  const list = page.getByRole("dialog", { name: "列表", exact: true });
  await list.getByRole("button", { name: "历史", exact: true }).click();
  await list.getByRole("textbox").fill("forager-00000007");
  await expect(list.locator("tr.item")).toHaveCount(1);
  await list.locator("tr.item").click();
  await expect(page.getByRole("region", { name: "对象详情" }).getByRole("heading", { name: "结果", exact: true })).toBeVisible();
});

test("lazy panels retain their contents throughout exit while closing keyboard access", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await boot(page);
  await page.locator(".stars-cartouche").click();
  const panel = page.locator(".pc-left");
  await expect(panel).toHaveClass(/pc-open/);
  await expect(panel).toHaveCSS("transform", "none");
  await page.getByRole("button", { name: "关闭项目面板", exact: true }).click();
  await expect(panel).toHaveCount(1);
  await expect(panel).toHaveAttribute("inert", "");
  await expect(panel.locator("h2")).toHaveText("test-for-myself");
  await page.waitForTimeout(100);
  const exiting = await panel.boundingBox();
  expect(exiting).not.toBeNull();
  expect(exiting!.x).toBeLessThan(0);
  expect(exiting!.x).toBeGreaterThan(-exiting!.width);
  await expect(panel).toHaveCount(0, { timeout: 1600 });
});

test("reduced motion redraws text coverage after deferred panels mount and close", async ({ page }) => {
  await boot(page);
  const canvas = page.locator(".stars-canvas");
  await page.getByRole("button", { name: "SwarmJet 平台设置", exact: true }).click();
  await expect(page.locator(".pc-platform")).toHaveClass(/pc-open/);
  await expect.poll(async () => Number(await canvas.getAttribute("data-labels-covered"))).toBeGreaterThan(0);
  await page.getByRole("button", { name: "关闭平台设置", exact: true }).click();
  await expect(page.locator(".pc-platform")).toHaveCount(0);
  await expect(canvas).toHaveAttribute("data-labels-covered", "0");
  const frame = await canvas.getAttribute("data-frames");
  await page.waitForTimeout(350);
  await expect(canvas).toHaveAttribute("data-frames", frame!);
});
