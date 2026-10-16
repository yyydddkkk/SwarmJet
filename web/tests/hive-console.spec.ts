import { expect, test } from "@playwright/test";

// Desktop only (ADR section 18). Screenshots land in test-results/ at 1440px.
test.use({ viewport: { width: 1440, height: 900 } });

test("Hive list renders cards, filters, and falls back to an empty state", async ({ page }, testInfo) => {
  await page.goto("/#/hives");

  await expect(page.getByRole("heading", { name: "Hive（蜂巢）" })).toBeVisible();
  await expect(page.getByRole("link", { name: /SwarmJet 平台/ })).toBeVisible();
  await expect(page.getByText("yyydddkkk/orders-api", { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: /支付网关/ })).toBeVisible();

  await page.screenshot({ path: testInfo.outputPath("hive-list-1440.png"), fullPage: true });

  await page.getByLabel("搜索 Hive").fill("不存在的仓库");
  await expect(page.getByText("没有匹配的 Hive。", { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: /SwarmJet 平台/ })).toHaveCount(0);
  await page.screenshot({ path: testInfo.outputPath("hive-list-empty-1440.png"), fullPage: true });

  await page.getByLabel("搜索 Hive").fill("");
  await expect(page.getByRole("link", { name: /SwarmJet 平台/ })).toBeVisible();
});

test("Hive conversation with Queen enabled shows prose, action cards, events and the Swarm rail", async ({ page }, testInfo) => {
  await page.goto("/#/hives/swarmjet");

  await expect(page.getByRole("heading", { name: "SwarmJet 平台", level: 1 })).toBeVisible();
  await expect(page.getByText("Queen（蜂王）已开启", { exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "对话", level: 2 })).toBeVisible();

  // Queen speaks as prose; actions and external events arrive as cards.
  await expect(
    page.getByText("收到。我先把 #142 交给一只使用 implementation 预设的 Forager", { exact: false }),
  ).toBeVisible();
  await expect(page.getByText("派出 Forager", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("外部内容", { exact: true }).first()).toBeVisible();
  await expect(page.getByRole("heading", { name: "Swarm（蜂群）", level: 2 })).toBeVisible();
  await expect(page.getByRole("link", { name: /forager-7f3a/ })).toBeVisible();

  await page.screenshot({ path: testInfo.outputPath("hive-conversation-1440.png"), fullPage: true });

  // The composer writes to the in-memory demo stream; the bee loader shows while Queen reads.
  await page.getByLabel("对 Queen（蜂王）说话").fill("再派一只 Forager 审一下 #88");
  await page.getByRole("button", { name: "发送" }).click();
  await expect(page.locator(".hg-loader")).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("hive-queen-thinking-1440.png"), fullPage: true });
  await expect(page.locator(".hg-form-status")).toContainText("已写入本地演示会话");
  await expect(page.locator(".hg-msg-queen .hg-msg-body").last()).toContainText("现在是本地假数据演示");
});

test("Hive with Queen disabled offers the direct dispatch form", async ({ page }, testInfo) => {
  await page.goto("/#/hives/orders");

  await expect(page.getByText("Queen（蜂王）未开启", { exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "直接派 Forager", level: 2 })).toBeVisible();

  await page.getByRole("button", { name: "派出 Forager" }).click();
  await expect(page.getByRole("alert")).toContainText("派活说明不能为空");
  await page.screenshot({ path: testInfo.outputPath("hive-dispatch-1440.png"), fullPage: true });

  await page.getByLabel("派活说明").fill("把订单导出改成流式响应，避免大报表超时。");
  await page.getByRole("button", { name: "派出 Forager" }).click();
  await expect(page.locator(".hg-form-status")).toContainText("已在本地演示中创建 Forager");
});

test("Forager detail implements the conversation and handoff sections", async ({ page }, testInfo) => {
  await page.goto("/#/hives/swarmjet/foragers/forager-7f3a");

  await expect(page.getByRole("heading", { name: /forager-7f3a/ })).toBeVisible();
  await expect(page.getByText("CI 2/3", { exact: true })).toBeVisible();
  await expect(page.getByText("Guard（守卫蜂） 3/4", { exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "对话", level: 2 })).toBeVisible();
  await expect(page.locator(".hg-msg-queen .hg-msg-body").first()).toContainText(
    "处理 #142：在 Swarm 面板增加按状态筛选。",
  );

  await page.screenshot({ path: testInfo.outputPath("forager-conversation-1440.png"), fullPage: true });

  await page.getByRole("link", { name: "交接摘要" }).click();
  await expect(page).toHaveURL(/#\/hives\/swarmjet\/foragers\/forager-7f3a\/handoff$/);
  await expect(page.getByRole("heading", { name: "交接摘要", level: 2 })).toBeVisible();
  await expect(page.getByText("在 Swarm 面板增加按状态筛选 Forager 的入口", { exact: true })).toBeVisible();
  await expect(page.getByText("前端筛选交互与空结果提示", { exact: true })).toBeVisible();

  await page.screenshot({ path: testInfo.outputPath("forager-handoff-1440.png"), fullPage: true });
});

test("Icon lab shows both bee variants at 16/20/32/64 on light and dark surfaces", async ({ page }, testInfo) => {
  await page.goto("/#/icons");

  await expect(page.getByRole("heading", { name: "图标规范", level: 1 })).toBeVisible();
  await expect(page.getByRole("heading", { name: "浅色背景", level: 2 })).toBeVisible();
  await expect(page.getByRole("heading", { name: "深色背景", level: 2 })).toBeVisible();

  const light = page.locator(".hg-icon-lab-panel").first();
  const dark = page.locator(".hg-icon-lab-panel").nth(1);
  await expect(light.locator(".hg-bee")).toHaveCount(4);
  await expect(light.locator(".hg-bee-line")).toHaveCount(4);
  await expect(dark.locator(".hg-bee")).toHaveCount(4);
  await expect(dark.locator(".hg-bee-line")).toHaveCount(4);
  await expect(page.getByText("16px", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("64px", { exact: true }).first()).toBeVisible();

  await page.screenshot({ path: testInfo.outputPath("icon-lab-1440.png"), fullPage: true });
});
