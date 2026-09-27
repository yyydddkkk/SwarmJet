import { expect, test } from "@playwright/test";

test("system settings shows effective addresses and connection results", async ({ page }) => {
  let checks = 0;
  await page.route("**/api/**", (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/session") return route.fulfill({ json: { username: "admin" } });
    if (path === "/api/projects") return route.fulfill({ json: [] });
    if (path === "/api/system") return route.fulfill({ json: {
      executionMode: "distributed", publicOrigin: "https://console.example", hostListen: "10.0.0.20:8180",
      secureCookies: true, builderEnabled: true, mihomoEnabled: true,
    } });
    if (path === "/api/system/checks") {
      checks++;
      return route.fulfill({ json: { checkedAt: "2026-09-24T08:00:00Z", checks: [
        { id: "registry", status: "ok", message: "控制面已连接镜像仓库" },
        { id: "mihomo", status: "warning", message: "代理出口需单独测试" },
        { id: "nodes", status: "ok", message: "最近 30 秒有 1/1 个节点上报心跳" },
      ] } });
    }
    if (path === "/api/registry") return route.fulfill({ json: {
      mode: "builtin", builtinAvailable: true, address: "registry.example:5443", builtinAddress: "registry.example:5443",
      repository: "swarmjet/agent", pushUser: "builder", pullUser: "reader", hasPushPassword: true, hasPullPassword: true,
    } });
    if (path === "/api/mihomo/status") return route.fulfill({ json: { enabled: true, running: true, proxyUrl: "http://proxy.example:17890", revisions: [] } });
    if (path === "/api/nodes") return route.fulfill({ json: [{ node: { id: "node-1", address: "https://node.example:8092", readiness: {} }, online: true, running: 0 }] });
    if (path === "/api/operations") return route.fulfill({ json: { inboxPending: 3, inboxRetrying: 1, inboxOldestWaitSeconds: 12, inboxProcessedLastHour: 24, inboxP95ProcessSeconds: 2.4, admissionWaiting: 2, runnerSlotsReserved: 1, measuredAt: "2026-09-24T08:00:00Z" } });
    if (path === "/api/networks") return route.fulfill({ json: { profiles: [], defaultId: "" } });
    return route.fulfill({ status: 404, json: { error: "not found" } });
  });
  await page.goto("/#/settings");
  await expect(page.getByRole("heading", { name: "系统设置" })).toBeVisible();
  await expect(page.getByText("https://console.example")).toBeVisible();
  await expect(page.getByText("10.0.0.20:8180")).toBeVisible();
  await expect(page.getByText("registry.example:5443")).toBeVisible();
  await expect(page.getByText("http://proxy.example:17890")).toBeVisible();
  await expect(page.getByText("最近 30 秒有 1/1 个节点上报心跳")).toBeVisible();
  await expect(page.getByText("待处理 3 · 已重试 1 · 最久等待 12 秒")).toBeVisible();
  const addressStarts = await page.locator(".system-address-value").evaluateAll((elements) => elements.map((element) => element.getBoundingClientRect().left));
  expect(Math.max(...addressStarts) - Math.min(...addressStarts)).toBeLessThan(1);
  await page.screenshot({ path: "test-results/system-settings-desktop.png", fullPage: true });
  await page.getByRole("button", { name: "重新检查" }).click();
  await expect.poll(() => checks).toBeGreaterThan(1);
  await page.getByRole("link", { name: "镜像仓库", exact: true }).click();
  await expect(page.getByLabel("仓库来源")).toHaveValue("builtin");
  await page.getByRole("link", { name: "网络与代理" }).click();
  await expect(page.getByRole("heading", { name: "平台默认出口" })).toBeVisible();
});

test("system settings fit a narrow viewport", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route("**/api/**", (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/session") return route.fulfill({ json: { username: "admin" } });
    if (path === "/api/projects") return route.fulfill({ json: [] });
    if (path === "/api/system") return route.fulfill({ json: { executionMode: "local", publicOrigin: "https://console.example", hostListen: "127.0.0.1:8180", secureCookies: true, builderEnabled: true, mihomoEnabled: false } });
    if (path === "/api/system/checks") return route.fulfill({ json: { checkedAt: "2026-09-24T08:00:00Z", checks: [] } });
    if (path === "/api/registry") return route.fulfill({ json: { mode: "custom", address: "registry.example:5443", repository: "swarmjet/agent", hasPushPassword: false, hasPullPassword: false } });
    if (path === "/api/mihomo/status") return route.fulfill({ json: { enabled: false } });
    if (path === "/api/nodes") return route.fulfill({ json: [] });
    if (path === "/api/operations") return route.fulfill({ json: { inboxPending: 0, inboxRetrying: 0, inboxOldestWaitSeconds: 0, inboxProcessedLastHour: 0, inboxP95ProcessSeconds: 0, admissionWaiting: 0, runnerSlotsReserved: 0, measuredAt: "2026-09-24T08:00:00Z" } });
    return route.fulfill({ status: 404, json: { error: "not found" } });
  });
  await page.goto("/#/settings");
  await expect(page.getByRole("heading", { name: "系统设置" })).toBeVisible();
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: "test-results/system-settings-390.png", fullPage: true });
});
