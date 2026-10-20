// Hive methodology tab (#/hives/{id}/methodology): manifest, files, history,
// two-version diff and a confirmed version switch.
import { expect, test } from "@playwright/test";
import { mockHiveAPI } from "./hive-api-mock";

test.use({ viewport: { width: 1440, height: 900 } });

test("the methodology page renders the manifest, files and version history", async ({ page }, testInfo) => {
  await mockHiveAPI(page);
  await page.goto("/#/hives/swarmjet/methodology");

  await expect(page.getByRole("heading", { name: "方法论", level: 2 })).toBeVisible();
  await expect(page.getByText("当前生效", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("3 个角色", { exact: true })).toBeVisible();

  const worker = page.getByTestId("method-role-worker");
  await expect(worker).toContainText("写代码、开 PR");
  await expect(worker).toContainText("发布分支 / PR");
  await expect(worker).toContainText("派生子 Agent");
  const verifier = page.getByTestId("method-role-verifier");
  await expect(verifier).toContainText("独立审查一个提交");
  await expect(verifier).toContainText("deepseek/deepseek-reasoner");
  await expect(verifier).toContainText("禁用工具");
  await expect(verifier).toContainText("edit, write");

  // Standing orders, event routing and merge policy are rendered as prose.
  await expect(page.getByText("合并前需要一个非作者的验证结论。", { exact: true })).toBeVisible();
  await expect(page.getByText("issue_labeled", { exact: true })).toBeVisible();
  await expect(page.getByText("issue_comment", { exact: true })).toBeVisible();
  const policy = page.locator(".hg-policy-list");
  await expect(policy).toContainText("合并前需要一条结论");
  await expect(policy).toContainText("结论必须来自非作者");
  await expect(policy).toContainText("GitHub 必需检查必须通过");
  await expect(policy).toContainText("满足条件后仍需人工确认");
  await expect(policy).toContainText("squash");

  // File browser: methodology.yaml is selected first, then a role prompt.
  await expect(page.getByTestId("methodology-file-content")).toContainText("defaultRole: worker");
  await page.getByRole("button", { name: /roles\/verifier.md/ }).click();
  await expect(page.getByTestId("methodology-file-content")).toContainText("只读审查");

  await expect(page.locator(".hg-version-table tbody tr")).toHaveCount(3);
  await expect(page.getByRole("heading", { name: "修改记录", level: 2 })).toBeVisible();
  await expect(page.locator(".hg-change-log > li")).toHaveCount(4);

  await page.screenshot({ path: testInfo.outputPath("methodology-1440.png"), fullPage: true });
});

test("switching a version asks for confirmation before posting it", async ({ page }, testInfo) => {
  const state = await mockHiveAPI(page);
  await page.goto("/#/hives/swarmjet/methodology");

  const row = page.locator(".hg-version-table tbody tr").filter({ hasText: "v2" });
  await row.getByRole("button", { name: "切换到此版本" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText("当前生效的是 v3");
  await expect(dialog).toContainText("收紧验证");
  await page.screenshot({
    path: testInfo.outputPath("methodology-switch-confirm-1440.png"),
    fullPage: true,
  });

  await dialog.getByRole("button", { name: "切换到 v2" }).click();
  await expect(
    page.getByRole("status").filter({ hasText: "已切换到 v2" }).first(),
  ).toBeVisible();

  const posts = state.calls.filter(
    call => call.method === "POST" && call.path === "/api/hives/swarmjet/methodology/activate",
  );
  expect(posts).toHaveLength(1);
  expect(posts[0].body).toEqual({ version: 2 });

  const activeRow = page.locator(".hg-version-table tbody tr").filter({ hasText: "v2" });
  await expect(activeRow).toContainText("当前生效");
});

test("the version diff compares the manifest and every changed file", async ({ page }, testInfo) => {
  await mockHiveAPI(page);
  await page.goto("/#/hives/swarmjet/methodology");

  await expect(page.getByRole("heading", { name: "版本差异", level: 2 })).toBeVisible();
  // The default comparison is v2 -> v3.
  await expect(page.getByTestId("methodology-diff").first()).toContainText("maxCostPerAgentUsd");
  const files = page.locator(".hg-file-diffs");
  await expect(files).toContainText("rules/security.md");
  await expect(files).toContainText("新增");
  await expect(files).toContainText("roles/worker.md");

  await page.screenshot({ path: testInfo.outputPath("methodology-diff-1440.png"), fullPage: true });

  await page.getByLabel("差异基线版本").selectOption("1");
  await expect(files).toContainText("roles/verifier.md");
});
