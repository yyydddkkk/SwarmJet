import { expect, test } from "@playwright/test";

test("project card groups secondary actions while keeping connection checks available", async ({ page }) => {
  let checks = 0;
  await page.route("**/api/**", route => {
    const { pathname } = new URL(route.request().url());
    if (pathname === "/api/session")
      return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (pathname === "/api/projects")
      return route.fulfill({ json: [{ id: "alpha", name: "Alpha", version: 1, enabled: true, metaMode: "managed", repositories: [] }] });
    if (pathname === "/api/projects/alpha/check") {
      checks += 1;
      return route.fulfill({ json: { checks: [{ name: "GitLab", ok: true, message: "连接正常" }] } });
    }
    return route.fulfill({ json: [] });
  });

  await page.goto("/#/projects");
  const card = page.locator(".project-card").filter({ hasText: "Alpha" });
  await expect(card.getByRole("link", { name: "编辑项目" })).toBeVisible();
  await expect(card.getByRole("link", { name: "查看初始化进度" })).toHaveCount(0);

  const more = card.getByRole("button", { name: "Alpha 的更多操作" });
  await more.click();
  await expect(more).toHaveAttribute("aria-expanded", "true");
  await expect(card.getByRole("link", { name: "查看初始化进度" })).toBeVisible();
  await expect(card.getByRole("link", { name: "成员与触发" })).toBeVisible();
  await card.getByRole("button", { name: "检查连接" }).click();
  await expect(page.getByRole("dialog", { name: "项目连接检查" })).toContainText("连接正常");
  expect(checks).toBe(1);
  await page.getByRole("button", { name: "关闭对话框" }).click();
  await more.click();
  await page.keyboard.press("Escape");
  await expect(more).toHaveAttribute("aria-expanded", "false");
  await page.setViewportSize({ width: 390, height: 844 });
  await more.click();
  await expect(card.getByRole("link", { name: "成员与触发" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
});
