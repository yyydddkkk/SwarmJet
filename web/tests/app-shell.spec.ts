// App shell after round 3: one administrator signs in, the top navigation has
// exactly three items, empty/retired routes go to #/hives, and 新建 Hive picks a
// repository from the GitHub App installation.
import { expect, test } from "@playwright/test";
import { mockHiveAPI } from "./hive-api-mock";

test.use({ viewport: { width: 1440, height: 900 } });

test("the administrator signs in and lands on the Hive list", async ({ page }, testInfo) => {
  await mockHiveAPI(page, { unauthenticated: true });
  await page.goto("/#/hives");

  await expect(page.getByRole("heading", { name: "登录蜂巢控制台" })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("login-1440.png"), fullPage: true });
  await page.getByLabel("用户名").fill("admin");
  await page.getByLabel("密码").fill("correct-horse-battery-staple");
  await page.getByRole("button", { name: "登录", exact: true }).click();

  await expect(page.getByRole("heading", { name: "Hive（蜂巢）" })).toBeVisible();
  await expect(page).toHaveURL(/#\/hives$/);
});

test("the top navigation has exactly Hive, 接入 and CLI 登录", async ({ page }) => {
  await mockHiveAPI(page);
  await page.goto("/#/hives");

  const nav = page.getByRole("navigation", { name: "控制台导航" });
  await expect(nav.getByRole("link")).toHaveCount(3);
  await expect(nav.getByRole("link", { name: "Hive" })).toBeVisible();
  await expect(nav.getByRole("link", { name: "接入" })).toBeVisible();
  await expect(nav.getByRole("link", { name: "CLI 登录" })).toBeVisible();
  await expect(nav.getByRole("link", { name: "审批" })).toHaveCount(0);
  await expect(nav.getByRole("link", { name: "系统" })).toHaveCount(0);
});

test("#/ and retired routes redirect to #/hives", async ({ page }) => {
  await mockHiveAPI(page);

  await page.goto("/#/");
  await expect(page).toHaveURL(/#\/hives$/);
  await expect(page.getByRole("heading", { name: "Hive（蜂巢）" })).toBeVisible();

  for (const retired of ["/#/tasks/123", "/#/overview", "/#/projects", "/#/nodes"]) {
    await page.goto(retired);
    await expect(page).toHaveURL(/#\/hives$/);
  }
});

test("新建 Hive picks a repository from the GitHub App installation", async ({ page }, testInfo) => {
  const state = await mockHiveAPI(page);
  await page.goto("/#/hives");

  await page.getByRole("button", { name: "新建 Hive" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await page.getByLabel("名称").fill("订单服务");
  await dialog.getByLabel("仓库", { exact: true }).selectOption("yyydddkkk/orders-api");
  await page.screenshot({ path: testInfo.outputPath("hive-create-1440.png"), fullPage: true });
  await dialog.getByRole("button", { name: "创建 Hive" }).click();

  await expect(page).toHaveURL(/#\/hives\/hive-new-1$/);
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
  await page.goto("/#/hives");

  await page.getByRole("button", { name: "新建 Hive" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("GitHub App 还没配置");
  await expect(dialog.getByRole("link", { name: "接入页" })).toHaveAttribute("href", "#/setup");

  await page.getByLabel("名称").fill("手动仓库");
  await dialog.getByLabel("仓库", { exact: true }).fill("owner/repo");
  await dialog.getByRole("button", { name: "创建 Hive" }).click();

  await expect(page).toHaveURL(/#\/hives\/hive-new-1$/);
  const creates = state.calls.filter(call => call.method === "POST" && call.path === "/api/hives");
  expect(creates[0].body).toMatchObject({ repository: "owner/repo" });
});
