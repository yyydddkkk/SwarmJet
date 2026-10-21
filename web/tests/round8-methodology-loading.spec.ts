import { expect, test, type Page } from "@playwright/test";
import { mockStarsHTTP } from "./stars-http-mock";

async function openProject(page: Page) {
  await page.addInitScript(() => {
    localStorage.setItem("sj-brief", "off");
    localStorage.setItem("sj-legend-seen", "1");
  });
  await page.goto("/");
  const command = page.getByLabel("对话与命令");
  await command.fill("SwarmJet 平台");
  await command.press("Enter");
  await page.locator(".stars-cartouche").click();
  return page.getByRole("region", { name: "项目面板" });
}

test("hive and other sections stay usable while methodology loads later", async ({ page }) => {
  await mockStarsHTTP(page);
  let release!: () => void;
  const held = new Promise<void>(resolve => { release = resolve; });
  await page.route("**/api/hives/swarmjet/methodology", async route => {
    await held;
    await route.fallback();
  });
  const panel = await openProject(page);
  await expect(panel.getByRole("heading", { name: "设置" })).toBeVisible();
  await expect(panel.getByRole("heading", { name: "花费" })).toBeVisible();
  await expect(panel.getByRole("heading", { name: "记忆" })).toBeVisible();
  await expect(panel.getByText("正在读取方法论…", { exact: true })).toBeVisible();
  await expect(panel.getByRole("img", { name: "方法论角色轨道" })).toHaveCount(0);
  await expect(panel).not.toContainText("第 undefined 版");
  release();
  await expect(panel.getByRole("img", { name: "方法论角色轨道" })).toBeVisible();
  await expect(panel.getByText(/第 3 版/).first()).toBeVisible();
});

test("failed methodology shows a readable retry state without fake version or budget", async ({ page }) => {
  await mockStarsHTTP(page);
  await page.route("**/api/hives/swarmjet/methodology", route => route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "upstream unavailable" }) }));
  const panel = await openProject(page);
  await expect(panel.getByRole("alert").filter({ hasText: "方法论暂时无法读取" })).toBeVisible();
  await expect(panel.getByText("方法论预算暂时无法读取。", { exact: true })).toBeVisible();
  await expect(panel.getByRole("heading", { name: "设置" })).toBeVisible();
  await expect(panel.getByRole("heading", { name: "花费" })).toBeVisible();
  await expect(panel.getByRole("img", { name: "方法论角色轨道" })).toHaveCount(0);
  await expect(panel).not.toContainText("第 undefined 版");
});
