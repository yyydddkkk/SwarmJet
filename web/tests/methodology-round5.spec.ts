import { expect, test, type Page } from "@playwright/test";
import { mockHiveAPI } from "./hive-api-mock";
import type { MethodologyResponse } from "../src/hive/types";

test.use({ viewport: { width: 1440, height: 1000 } });

const templates = [
  { name: "pstack", title: "PStack 团队", description: "开发与独立验证协作，适合需要审查的团队。", version: "1.0.0" },
  { name: "minimal", title: "极简 Worker", description: "只有一个 worker，没有验证，不自动合并。", version: "1.0.0" },
];
const manifest = {
  defaultRole: "worker",
  roles: { worker: { permissions: { publish: true, message: true, attest: false, merge: false, methodology: false } } },
  merge: { requireAttestation: false, requireNonAuthor: false, requireChecks: false, auto: false },
};
const fallback: MethodologyResponse = {
  id: "", hiveId: "swarmjet", version: 0, active: false, manifest,
  digest: "", createdBy: "", createdAt: "0001-01-01T00:00:00Z",
  files: [{ path: "methodology.yaml", content: "defaultRole: worker\n" }], builtin: true,
};
const json = (body: unknown, status = 200) => ({
  status, contentType: "application/json", body: JSON.stringify(body),
});

async function uninstalledHive(page: Page, missing = false) {
  await mockHiveAPI(page);
  const installs: unknown[] = [];
  let installed: MethodologyResponse | undefined;
  let failInstall = false;
  await page.route("**/api/methodology-templates", route => route.fulfill(json(templates)));
  await page.route("**/api/hives/swarmjet/methodology", route => {
    if (installed) return route.fulfill(json(installed));
    return route.fulfill(missing ? json({ error: "not found" }, 404) : json(fallback));
  });
  await page.route("**/api/hives/swarmjet/methodology/versions", route => route.fulfill(json({
    hiveId: "swarmjet", activeVersion: installed ? 1 : 0,
    versions: installed ? [installed] : [], changes: [],
  })));
  await page.route("**/api/hives/swarmjet/methodology/install", route => {
    const body = route.request().postDataJSON();
    installs.push(body);
    if (failInstall) return route.fulfill(json({ error: "安装失败，请重试" }, 500));
    installed = {
      ...fallback, id: "method-installed", builtin: false, version: 1, active: true,
      digest: "ab".repeat(32), createdBy: "local:web-admin", createdAt: "2026-10-20T08:00:00Z",
      source: { url: `builtin:${body.name}`, ref: "1.0.0" },
    };
    return route.fulfill(json(installed));
  });
  return { installs, fail: () => { failInstall = true; }, succeed: () => { failInstall = false; } };
}

test("catalog installation requires confirmation and shows builtin source after installing", async ({ page }, testInfo) => {
  const state = await uninstalledHive(page);
  await page.goto("/#/legacy/hives/swarmjet/methodology");
  await expect(page.getByText("尚未选择方法论，可以在对话里请 Queen 推荐", { exact: true })).toBeVisible();
  await expect(page.getByRole("list", { name: "内置方法论目录" }).getByRole("listitem")).toHaveCount(2);
  await page.screenshot({ path: testInfo.outputPath("methodology-catalog-1440.png"), fullPage: true });

  await page.getByRole("button", { name: "安装 极简 Worker" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("builtin:minimal");
  expect(state.installs).toHaveLength(0);
  await dialog.getByRole("button", { name: "取消" }).click();
  expect(state.installs).toHaveLength(0);
  await page.getByRole("button", { name: "安装 极简 Worker" }).click();
  await page.screenshot({ path: testInfo.outputPath("methodology-install-confirm-1440.png"), fullPage: true });
  await dialog.getByRole("button", { name: "确认安装" }).click();

  await expect(page.getByTestId("methodology-version")).toContainText("v1");
  await expect(page.locator(".hg-source")).toContainText("builtin:minimal@1.0.0");
  await expect(page.getByRole("list", { name: "内置方法论目录" })).toHaveCount(0);
  expect(state.installs).toEqual([{ name: "minimal" }]);
  await page.screenshot({ path: testInfo.outputPath("methodology-installed-1440.png"), fullPage: true });
});

test("a missing methodology exposes the catalog and failed installs can be retried", async ({ page }) => {
  const state = await uninstalledHive(page, true);
  state.fail();
  await page.goto("/#/legacy/hives/swarmjet/methodology");
  await page.getByRole("button", { name: "安装 PStack 团队" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "确认安装" }).click();
  await expect(dialog).toContainText("安装失败，请重试");
  await expect(dialog.getByRole("button", { name: "确认安装" })).toBeEnabled();
  state.succeed();
  await dialog.getByRole("button", { name: "确认安装" }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.locator(".hg-source")).toContainText("builtin:pstack@1.0.0");
  expect(state.installs).toEqual([{ name: "pstack" }, { name: "pstack" }]);
});
