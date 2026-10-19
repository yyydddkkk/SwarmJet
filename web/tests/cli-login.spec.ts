// CLI 登录 page (#/cli-login). Status comes from GET /api/setup/status and is
// mocked with page.route; the terminal itself is never opened here (it needs a
// WebSocket backend that task E is adding).
import { expect, test } from "@playwright/test";
import { mockHiveAPI } from "./hive-api-mock";

test.use({ viewport: { width: 1440, height: 900 } });

test("Cell disabled explains why there is no terminal", async ({ page }, testInfo) => {
  await mockHiveAPI(page, {
    setup: {
      cells: { enabled: false, dockerAvailable: false },
      cliLogin: { available: false, loggedIn: false },
    },
  });
  await page.goto("/#/cli-login");

  await expect(page.getByRole("heading", { name: "CLI 登录", level: 1 })).toBeVisible();
  await expect(page.getByText("Cell 未启用，登录终端不可用", { exact: true })).toBeVisible();
  await expect(page.getByText(/compose\.cells\.yaml/)).toBeVisible();
  await expect(page.getByTestId("cli-terminal")).toHaveCount(0);
  await page.screenshot({
    path: testInfo.outputPath("cli-login-unavailable-1440.png"),
    fullPage: true,
  });
});

test("Cell enabled shows the login state and the terminal", async ({ page }, testInfo) => {
  await mockHiveAPI(page, {
    setup: {
      cells: { enabled: true, dockerAvailable: true },
      cliLogin: { available: true, loggedIn: true, cli: "opencode" },
    },
  });
  await page.goto("/#/cli-login");

  await expect(page.getByText("已登录", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "连接终端" })).toBeVisible();
  await expect(page.getByTestId("cli-terminal")).toBeVisible();
  await expect(page.getByText("Cell 未启用，登录终端不可用", { exact: true })).toHaveCount(0);
  await page.screenshot({ path: testInfo.outputPath("cli-login-1440.png"), fullPage: true });
});

test("an unavailable login service is explained instead of showing a blank terminal", async ({ page }) => {
  await mockHiveAPI(page, {
    setup: {
      cells: { enabled: true, dockerAvailable: true },
      cliLogin: { available: false, loggedIn: false },
    },
  });
  await page.goto("/#/cli-login");

  await expect(page.getByText("登录服务未就绪", { exact: true })).toBeVisible();
  await expect(page.getByTestId("cli-terminal")).toHaveCount(0);
});

test("a working login service is shown even when Cells are off", async ({ page }) => {
  await mockHiveAPI(page, {
    setup: {
      cells: { enabled: false, dockerAvailable: true },
      cliLogin: { available: true, loggedIn: false },
    },
  });
  await page.goto("/#/cli-login");

  // available comes from the CLI login handler (Docker), not from the Cell flag.
  await expect(page.getByText(/登录终端仍然可用/)).toBeVisible();
  await expect(page.getByTestId("cli-terminal")).toBeVisible();
});
