// App shell after round 3: one administrator signs in, the top navigation has
// exactly three items, empty/retired routes go to #/legacy/hives, and 新建 Hive picks a
// repository from the GitHub App installation.
import { expect, test } from "@playwright/test";
import { mockHiveAPI } from "./hive-api-mock";

test.use({ viewport: { width: 1440, height: 900 } });

test("the administrator signs in and lands on the Hive list", async ({ page }, testInfo) => {
  await mockHiveAPI(page, { unauthenticated: true });
  await page.goto("/#/legacy/hives");

  await expect(page.getByRole("heading", { name: "登录蜂巢控制台" })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("login-1440.png"), fullPage: true });
  await page.getByLabel("用户名").fill("admin");
  await page.getByLabel("密码").fill("correct-horse-battery-staple");
  await page.getByRole("button", { name: "登录", exact: true }).click();

  await expect(page.getByRole("heading", { name: "Hive（蜂巢）" })).toBeVisible();
  await expect(page).toHaveURL(/#\/legacy\/hives$/);
});

test("a failed sign-in shows the server message instead of a fake expiry", async ({ page }) => {
  await mockHiveAPI(page, {
    unauthenticated: true,
    loginFailure: { status: 401, message: "invalid username or password" },
  });
  await page.goto("/#/legacy/hives");

  await page.getByLabel("用户名").fill("admin");
  await page.getByLabel("密码").fill("wrong-password");
  await page.getByRole("button", { name: "登录", exact: true }).click();

  await expect(page.getByRole("alert")).toContainText("invalid username or password");
  await expect(page.getByRole("alert")).not.toContainText("会话已过期");
});

test("the top navigation has exactly Hive, 接入 and 模型", async ({ page }) => {
  await mockHiveAPI(page);
  await page.goto("/#/legacy/hives");

  const nav = page.getByRole("navigation", { name: "控制台导航" });
  await expect(nav.getByRole("link")).toHaveCount(3);
  await expect(nav.getByRole("link", { name: "Hive" })).toBeVisible();
  await expect(nav.getByRole("link", { name: "接入" })).toBeVisible();
  await expect(nav.getByRole("link", { name: "模型" })).toBeVisible();
  await expect(nav.getByRole("link", { name: "审批" })).toHaveCount(0);
  await expect(nav.getByRole("link", { name: "系统" })).toHaveCount(0);
});

test("#/legacy/ and retired routes redirect to #/legacy/hives", async ({ page }) => {
  await mockHiveAPI(page);

  await page.goto("/#/legacy/");
  await expect(page).toHaveURL(/#\/legacy\/hives$/);
  await expect(page.getByRole("heading", { name: "Hive（蜂巢）" })).toBeVisible();

  for (const retired of ["/#/legacy/tasks/123", "/#/legacy/overview", "/#/legacy/projects", "/#/legacy/nodes", "/#/legacy/cli-login"]) {
    await page.goto(retired);
    await expect(page).toHaveURL(/#\/legacy\/hives$/);
  }
});

test("新建 Hive picks a repository from the GitHub App installation", async ({ page }, testInfo) => {
  const state = await mockHiveAPI(page);
  state.modelCredentials = [{ provider: "deepseek", configured: true }];
  await page.goto("/#/legacy/hives");

  await page.getByRole("button", { name: "新建 Hive" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await page.getByLabel("名称").fill("订单服务");
  await dialog.getByLabel("仓库", { exact: true }).selectOption("yyydddkkk/orders-api");
  await page.screenshot({ path: testInfo.outputPath("hive-create-1440.png"), fullPage: true });
  await dialog.getByRole("button", { name: "下一步" }).click();
  await dialog.getByRole("button", { name: "下一步" }).click();
  await dialog.getByRole("button", { name: "创建 Hive" }).click();

  await expect(page).toHaveURL(/#\/legacy\/hives\/hive-new-1$/);
  await expect(page.getByRole("heading", { name: "订单服务", level: 1 })).toBeVisible();

  const creates = state.calls.filter(call => call.method === "POST" && call.path === "/api/hives");
  expect(creates).toHaveLength(1);
  expect(creates[0].body).toMatchObject({
    name: "订单服务",
    repository: "yyydddkkk/orders-api",
    queenEnabled: true,
  });
});

test("without a GitHub App the dialog explains how to connect and still accepts owner/name", async ({ page }) => {
  const state = await mockHiveAPI(page, {
    setup: { github: { configured: false, source: "", appSlug: "" } },
  });
  state.modelCredentials = [{ provider: "deepseek", configured: true }];
  await page.goto("/#/legacy/hives");

  await page.getByRole("button", { name: "新建 Hive" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("GitHub App 还没配置");
  await expect(dialog.getByRole("link", { name: "接入页" })).toHaveAttribute("href", "#/legacy/setup");

  await page.getByLabel("名称").fill("手动仓库");
  await dialog.getByLabel("仓库", { exact: true }).fill("owner/repo");
  await dialog.getByRole("button", { name: "下一步" }).click();
  await dialog.getByRole("button", { name: "下一步" }).click();
  await dialog.getByRole("button", { name: "创建 Hive" }).click();

  await expect(page).toHaveURL(/#\/legacy\/hives\/hive-new-1$/);
  const creates = state.calls.filter(call => call.method === "POST" && call.path === "/api/hives");
  expect(creates[0].body).toMatchObject({ repository: "owner/repo" });
});
