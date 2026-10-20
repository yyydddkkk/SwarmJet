import { expect, test } from "@playwright/test";
import { mockHiveAPI } from "./hive-api-mock";

test.use({ viewport: { width: 1440, height: 900 } });

test("Hive wizard selects repository, credential-backed Queen model and concurrency", async ({ page }, testInfo) => {
  const state = await mockHiveAPI(page);
  state.modelCredentials = [
    { provider: "deepseek", configured: true, type: "api_key", masked: "sk-…test" },
  ];
  await page.goto("/#/hives");
  await page.getByRole("button", { name: "新建 Hive" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("仓库", { exact: true }).selectOption("yyydddkkk/orders-api");
  await expect(dialog.getByLabel("仓库", { exact: true })).toHaveValue("yyydddkkk/orders-api");
  await page.screenshot({ path: testInfo.outputPath("hive-wizard-repository-1440.png"), fullPage: true });
  await dialog.getByRole("button", { name: "下一步" }).click();
  await dialog.getByLabel("Queen 模型").selectOption("deepseek/deepseek-chat");
  await expect(dialog.getByLabel("Queen 模型").locator("option").filter({ hasText: "OpenAI" })).toHaveCount(0);
  await page.screenshot({ path: testInfo.outputPath("hive-wizard-model-1440.png"), fullPage: true });
  await dialog.getByRole("button", { name: "下一步" }).click();
  await expect(dialog.getByLabel("并发上限", { exact: true })).toHaveValue("4");
  await expect(dialog).toContainText("orders-api · yyydddkkk/orders-api");
  await dialog.getByLabel("并发上限", { exact: true }).fill("6");
  await page.screenshot({ path: testInfo.outputPath("hive-wizard-concurrency-1440.png"), fullPage: true });
  await dialog.getByRole("button", { name: "上一步" }).click();
  await expect(dialog.getByLabel("Queen 模型")).toHaveValue("deepseek/deepseek-chat");
  await dialog.getByRole("button", { name: "下一步" }).click();
  await expect(dialog.getByLabel("并发上限", { exact: true })).toHaveValue("6");
  await dialog.getByRole("button", { name: "创建 Hive" }).click();
  await expect(page).toHaveURL(/#\/hives\/hive-new-1$/);
  const creates = state.calls.filter(call => call.method === "POST" && call.path === "/api/hives");
  expect(creates).toHaveLength(1);
  expect(creates[0].body).toEqual({
    name: "orders-api",
    repository: "yyydddkkk/orders-api",
    queenEnabled: true,
    config: { queenModel: "deepseek/deepseek-chat", maxConcurrentForagers: 6 },
  });
});

test("Hive wizard explains missing credentials and links to models", async ({ page }, testInfo) => {
  const state = await mockHiveAPI(page);
  await page.goto("/#/hives");
  await page.getByRole("button", { name: "新建 Hive" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("仓库", { exact: true }).selectOption("yyydddkkk/orders-api");
  await dialog.getByRole("button", { name: "下一步" }).click();
  await expect(dialog).toContainText("没有可用的模型凭证");
  await expect(dialog.getByRole("button", { name: "下一步" })).toBeDisabled();
  await page.screenshot({ path: testInfo.outputPath("hive-wizard-missing-credentials-1440.png"), fullPage: true });
  await dialog.getByRole("link", { name: "模型", exact: true }).click();
  await expect(page).toHaveURL(/#\/models$/);
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(state.calls.filter(call => call.method === "POST" && call.path === "/api/hives")).toHaveLength(0);
});
