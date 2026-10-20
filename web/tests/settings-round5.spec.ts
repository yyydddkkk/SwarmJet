import { expect, test } from "@playwright/test";
import { mockHiveAPI } from "./hive-api-mock";

test.use({ viewport: { width: 1440, height: 1000 } });

test("Hive settings persist the Queen model, concurrency and enabled state", async ({ page }, testInfo) => {
  const state = await mockHiveAPI(page);
  state.modelCredentials = [{ provider: "deepseek", configured: true, masked: "sk-…test" }];
  await page.goto("/#/hives/swarmjet/settings");

  await expect(page.getByRole("heading", { name: "Hive 设置", exact: true })).toBeVisible();
  await page.getByLabel("Queen 模型", { exact: true }).selectOption("deepseek/deepseek-reasoner");
  await page.getByLabel("并发上限", { exact: true }).fill("7");
  await page.getByRole("checkbox", { name: /开启 Queen/ }).uncheck();
  await page.getByRole("button", { name: "保存设置", exact: true }).click();
  await expect(page.getByRole("status").filter({ hasText: "Hive 设置已保存。" })).toBeVisible();

  const update = state.calls.find(call => call.path === "/api/hives/swarmjet" && call.method !== "GET");
  expect(update?.headers["x-requested-with"]).toBe("swarmjet");
  expect(update?.body).toEqual({
    queenEnabled: false,
    config: { queenModel: "deepseek/deepseek-reasoner", maxConcurrentForagers: 7 },
  });
  await page.reload();
  await expect(page.getByLabel("Queen 模型", { exact: true })).toHaveValue("deepseek/deepseek-reasoner");
  await expect(page.getByLabel("并发上限", { exact: true })).toHaveValue("7");
  await expect(page.getByRole("checkbox", { name: /开启 Queen/ })).not.toBeChecked();
  await page.screenshot({ path: testInfo.outputPath("hive-settings-1440.png"), fullPage: true });
});

test("methodology limits are read only in Hive settings", async ({ page }) => {
  await mockHiveAPI(page);
  await page.goto("/#/hives/swarmjet/settings");
  await expect(page.getByRole("heading", { name: "方法论声明的上限" })).toBeVisible();
  const values = page.locator(".hg-limits");
  await expect(values).toContainText("并发 Agent");
  await expect(values).toContainText("单 Agent 花费");
  await expect(values.locator("input, select")).toHaveCount(0);
  await expect(page.getByLabel("单 Agent 花费", { exact: true })).toHaveCount(0);
  await expect(page.getByLabel("每日花费", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "模型页", exact: true })).toBeVisible();
  await expect(page.getByLabel("Queen 模型", { exact: true })).toBeDisabled();
});

test("failed settings save keeps the form available for retry", async ({ page }) => {
  const state = await mockHiveAPI(page);
  state.modelCredentials = [{ provider: "deepseek", configured: true, masked: "sk-…test" }];
  await page.route("**/api/hives/swarmjet", async route => {
    if (route.request().method() === "GET") return route.fallback();
    await route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: "保存失败，请重试。" }) });
  });
  await page.goto("/#/hives/swarmjet/settings");
  await page.getByLabel("并发上限", { exact: true }).fill("6");
  await page.getByRole("button", { name: "保存设置", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("保存失败，请重试。");
  await expect(page.getByLabel("并发上限", { exact: true })).toHaveValue("6");
  await expect(page.getByRole("button", { name: "保存设置", exact: true })).toBeEnabled();
});
