import { test, expect, type Page } from "@playwright/test";
const ref = "registry.test/swarmjet/agent@sha256:" + "a".repeat(64);
async function mock(page: Page) {
  const profiles: any[] = [];
  let defaultId = "";
  let saved: any;
  const nodes = [
    {
      node: {
        id: "node-a",
        address: "http://node-a:8092",
        capacity: 2,
        readiness: {
          status: "ready",
          configVersion: 1,
          architecture: "amd64",
          image: ref,
          agents: ["codex"],
        },
      },
      running: 1,
      online: true,
      config: {
        name: "内网执行节点",
        version: 1,
        capacity: 2,
        enabled: true,
        archived: false,
        network: { mode: "direct" },
        image: ref,
      },
    },
  ];
  await page.route("**/api/**", async (route) => {
    const r = route.request(),
      u = new URL(r.url());
    const path = u.pathname;
    if (path === "/api/session")
      return route.fulfill({ json: { username: "admin" } });
    if (path === "/api/projects") return route.fulfill({ json: [] });
    if (path === "/api/mihomo/status")
      return route.fulfill({
        json: {
          enabled: true,
          running: false,
          active: "revision-a",
          revisions: [{ id: "revision-a", name: "内网出口", createdAt: "2026-09-23T00:00:00Z", warnings: [] }],
        },
      });
    if (path === "/api/networks/default" && r.method() === "PUT") {
      defaultId = r.postDataJSON().id;
      return route.fulfill({ json: { id: defaultId } });
    }
    if (path === "/api/networks" && r.method() === "DELETE") {
      const id = u.searchParams.get("id");
      const index = profiles.findIndex((p) => p.id === id);
      if (index < 0 || String(profiles[index].version) !== u.searchParams.get("version"))
        return route.fulfill({ status: 409, json: { error: "stale version" } });
      profiles.splice(index, 1);
      if (defaultId === id) defaultId = "";
      return route.fulfill({ status: 204 });
    }
    if (path === "/api/networks") {
      if (r.method() === "PUT") {
        saved = r.postDataJSON();
        profiles.push({ ...saved, version: 1, hasPassword: !!saved.password });
      }
      return route.fulfill({
        json:
          r.method() === "PUT"
            ? profiles[profiles.length - 1]
            : { profiles, defaultId },
      });
    }
    if (path === "/api/nodes") return route.fulfill({ json: nodes });
    if (path === "/api/nodes/node-a/config") {
      saved = r.postDataJSON();
      nodes[0].config = { ...saved, version: 2 };
      return route.fulfill({ json: nodes[0].config });
    }
    if (path === "/api/runtime/network-test") {
      expect(u.searchParams.get("node")).toBe("node-a");
      return route.fulfill({
        json: {
          checks: [
            { scope: "node", status: 401 },
            { scope: "agent-container", status: 200 },
          ],
        },
      });
    }
    return route.fulfill({ status: 404, json: { error: "not found" } });
  });
  return { saved: () => saved, nodes };
}
test("network editor creates versioned profile without placing password in the list", async ({
  page,
}) => {
  const state = await mock(page);
  await page.goto("/#/networks");
  await page.getByRole("button", { name: "新增配置" }).click();
  const editor = page.getByRole("dialog", { name: "新增网络配置" });
  await expect(editor).toBeVisible();
  await page.getByLabel("名称", { exact: true }).fill("构建出口");
  await page
    .getByLabel("HTTP 代理", { exact: true })
    .fill("http://proxy.internal:8080");
  await page
    .getByLabel("代理密码", { exact: true })
    .fill("private-fixture-password");
  await page.getByRole("button", { name: "保存配置", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("已保存");
  expect(state.saved().password).toBe("private-fixture-password");
  await expect(
    page.getByText("private-fixture-password", { exact: true }),
  ).toHaveCount(0);
  await expect(page.getByLabel("新密码（留空保留）")).toHaveValue("");
  const savedEditor = page.getByRole("dialog", { name: "编辑网络配置" });
  await savedEditor.getByRole("button", { name: "关闭网络配置" }).click();
  await expect(savedEditor).not.toBeVisible();
  await page.getByRole("button", { name: /构建出口/ }).click();
  await savedEditor.getByLabel("名称", { exact: true }).fill("构建出口 2");
  await page.keyboard.press("Escape");
  await expect(savedEditor.getByText("有未保存的修改，确定放弃吗？")).toBeVisible();
  await savedEditor.getByRole("button", { name: "放弃修改" }).click();
  await expect(savedEditor).not.toBeVisible();
});
test("Mihomo management opens above the overview with versions shown first", async ({ page }) => {
  await mock(page);
  await page.goto("/#/networks");
  await page.getByRole("button", { name: "管理服务" }).click();
  const dialog = page.getByRole("dialog", { name: "Mihomo 代理服务" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole("heading", { name: "配置版本" })).toBeVisible();
  await expect(dialog.getByLabel("导入方式")).toHaveCount(0);
  await dialog.getByRole("button", { name: "导入配置" }).click();
  await expect(dialog.getByLabel("导入方式")).toBeVisible();
  await dialog.getByLabel("配置名称").fill("待导入出口");
  await dialog.getByRole("button", { name: "关闭 Mihomo 配置" }).click();
  await expect(dialog).not.toBeVisible();
  await page.getByRole("button", { name: "管理服务" }).click();
  await expect(dialog.getByLabel("导入方式")).toBeVisible();
  await expect(dialog.getByLabel("配置名称")).toHaveValue("待导入出口");
});
test("saved network profile can be deleted and default returns to direct", async ({ page }) => {
  await mock(page);
  await page.goto("/#/networks");
  await page.getByRole("button", { name: "新增配置" }).click();
  await page.getByLabel("名称", { exact: true }).fill("临时出口");
  await page.getByRole("button", { name: "保存配置", exact: true }).click();
  await page.getByRole("button", { name: "关闭网络配置" }).click();
  await page.getByLabel("默认网络").selectOption({ label: "临时出口" });
  await page.getByRole("button", { name: /临时出口/ }).click();
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("dialog", { name: "编辑网络配置" }).getByRole("button", { name: "删除配置" }).click();
  await expect(page.getByRole("status")).toContainText("网络配置已删除");
  await expect(page.getByText("临时出口", { exact: true })).toHaveCount(0);
  await expect(page.getByLabel("默认网络")).toHaveValue("");
});
test("node drain preserves image and scopes network tests to selected node", async ({
  page,
}) => {
  const state = await mock(page);
  await page.goto("/#/nodes");
  await page.getByRole("button", { name: /内网执行节点/ }).click();
  const detail = page.getByRole("dialog", { name: "内网执行节点" });
  await expect(detail).toBeVisible();
  await page.getByLabel("接收新任务", { exact: true }).uncheck();
  await page.getByRole("button", { name: "保存节点配置" }).click();
  await expect(page.getByRole("status")).toContainText("等待节点同步");
  expect(state.saved().image).toBe(ref);
  expect(state.saved().enabled).toBe(false);
  await page.getByRole("button", { name: "从节点和 Agent 容器测试" }).click();
  await expect(
    page.getByText("节点进程：已连接，HTTP 401", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Agent 容器：已连接，HTTP 200", { exact: true }),
  ).toBeVisible();
  await detail.getByRole("button", { name: "关闭节点详情" }).click();
  await expect(detail).not.toBeVisible();
  await page.getByRole("button", { name: /内网执行节点/ }).click();
  await expect(detail).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(detail).not.toBeVisible();
});
for (const width of [1440, 390])
  test(`node and network settings fit ${width}px`, async ({ page }) => {
    await mock(page);
    await page.setViewportSize({ width, height: 1000 });
    await page.goto("/#/nodes");
    await page.getByRole("button", { name: /内网执行节点/ }).click();
    await expect
      .poll(() =>
        page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
      )
      .toBe(true);
    await page.screenshot({
      path: `test-results/nodes-${width}.png`,
      fullPage: true,
    });
    if (width === 390) {
      const detail = page.getByRole("dialog", { name: "内网执行节点" });
      await detail
        .getByRole("button", { name: "从节点和 Agent 容器测试" })
        .scrollIntoViewIfNeeded();
      await expect
        .poll(() => detail.evaluate((element) => element.scrollTop))
        .toBeGreaterThan(0);
    }
    await page.goto("/#/networks");
    await expect(
      page.getByRole("heading", { name: "网络配置", exact: true }),
    ).toBeVisible();
    await expect
      .poll(() =>
        page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
      )
      .toBe(true);
    await page.screenshot({
      path: `test-results/networks-${width}.png`,
      fullPage: true,
    });
    await page.getByRole("button", { name: "新增配置" }).click();
    await expect(page.getByRole("dialog", { name: "新增网络配置" })).toBeVisible();
    await expect
      .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
      .toBe(true);
    await page.screenshot({ path: `test-results/network-editor-${width}.png` });
    await page.getByRole("button", { name: "关闭网络配置" }).click();
    await page.getByRole("button", { name: "管理服务" }).click();
    await expect(page.getByRole("dialog", { name: "Mihomo 代理服务" })).toBeVisible();
    await expect
      .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
      .toBe(true);
    await page.screenshot({ path: `test-results/mihomo-${width}.png` });
  });
