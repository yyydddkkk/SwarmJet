// 模型 page (#/models): replaces the retired CLI login terminal. The API key
// travels only in the PUT body; the page keeps the masked value the server
// returns and nothing else.
import { expect, test } from "@playwright/test";
import { mockHiveAPI } from "./hive-api-mock";

test.use({ viewport: { width: 1440, height: 900 } });

test("the model page shows the missing-credential state and the catalog", async ({ page }, testInfo) => {
  await mockHiveAPI(page);
  await page.goto("/#/models");

  await expect(page.getByRole("heading", { name: "模型", level: 1 })).toBeVisible();
  await expect(page.getByText("还没有可用凭证", { exact: true })).toBeVisible();
  await expect(page.getByText("deepseek/deepseek-chat", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("DeepSeek Chat", { exact: true })).toBeVisible();
  await expect(page.getByRole("table")).toBeVisible();
  await expect(page.getByText("缺少凭证", { exact: true }).first()).toBeVisible();
  await expect(page.getByRole("link", { name: "CLI 登录" })).toHaveCount(0);

  await page.screenshot({ path: testInfo.outputPath("models-empty-1440.png"), fullPage: true });
});

test("saving a key shows only the masked value and never puts it in the URL or storage", async ({ page }, testInfo) => {
  const state = await mockHiveAPI(page);
  await page.goto("/#/models");

  const key = "sk-test-deepseek-not-a-real-key-1234";
  const sessionBefore = await page.evaluate(() => JSON.stringify(sessionStorage));
  const input = page.getByLabel("API key");
  await expect(input).toHaveAttribute("type", "password");
  await input.fill(key);
  await page.screenshot({ path: testInfo.outputPath("models-key-form-1440.png"), fullPage: true });
  await page.getByRole("button", { name: "保存凭证" }).click();

  const masked = page.getByTestId("masked-deepseek");
  await expect(masked).toHaveText("sk-…1234");
  await expect(page.getByRole("status").filter({ hasText: "已保存 DeepSeek" }).first()).toBeVisible();
  await expect(input).toHaveValue("");
  expect(await page.evaluate(() => JSON.stringify(localStorage))).not.toContain(key);
  // sessionStorage is untouched: same contents as before the key was typed.
  expect(await page.evaluate(() => JSON.stringify(sessionStorage))).toBe(sessionBefore);
  expect(await page.evaluate(() => JSON.stringify(sessionStorage))).not.toContain(key);
  expect(page.url()).not.toContain(key);

  const puts = state.calls.filter(call => call.method === "PUT");
  expect(puts).toHaveLength(1);
  expect(puts[0].path).toBe("/api/models/credentials/deepseek");
  expect(puts[0].body).toEqual({ apiKey: key });
  expect(puts[0].headers["x-requested-with"]).toBe("swarmjet");
  expect(puts[0].query).toBe("");
  // The submitted plaintext is absent from the visible page.
  await expect(page.getByText(key, { exact: false })).toHaveCount(0);

  await page.screenshot({ path: testInfo.outputPath("models-credential-saved-1440.png"), fullPage: true });

  // A reload reads the masked value back from the server, not from the browser.
  await page.reload();
  await expect(page.getByTestId("masked-deepseek")).toHaveText("sk-…1234");
  await expect(page.getByText("已有可用凭证", { exact: true })).toBeVisible();
});

test("removing a credential asks for confirmation", async ({ page }) => {
  const state = await mockHiveAPI(page);
  state.modelCredentials = [
    { provider: "deepseek", type: "api_key", masked: "sk-…1234", configured: true },
  ];
  await page.goto("/#/models");

  await page.getByRole("button", { name: "移除" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("使用这个提供方的 Agent 会立即无法调用模型");
  await dialog.getByRole("button", { name: "移除" }).click();

  await expect(page.getByRole("status").filter({ hasText: "已移除 DeepSeek" })).toBeVisible();
  await expect(page.getByTestId("masked-deepseek")).toHaveCount(0);
  const deletes = state.calls.filter(call => call.method === "DELETE");
  expect(deletes).toHaveLength(1);
  expect(deletes[0].path).toBe("/api/models/credentials/deepseek");
});
