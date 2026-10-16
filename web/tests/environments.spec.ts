import { test, expect, type Page } from "@playwright/test";
const ref = "registry.test/swarmjet/agent@sha256:" + "a".repeat(64);
const publishedRef = "ghcr.io/example/swarmjet/agent:v2.0.0";
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
      return route.fulfill({ json: { username: "admin", platformAdmin: true } });
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
  await page.locator(".toast").getByRole("button", { name: "关闭通知" }).click();
  await expect(page.locator(".toast")).toHaveCount(0);
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
  const discard = page.getByRole("dialog", { name: "放弃未保存的修改" });
  await expect(discard).toContainText("无法恢复");
  await discard.getByRole("button", { name: "继续编辑" }).click();
  await expect(savedEditor.getByLabel("名称", { exact: true })).toHaveValue("构建出口 2");
  await savedEditor.getByRole("button", { name: "关闭网络配置" }).click();
  await discard.getByRole("button", { name: "放弃修改" }).click();
  await expect(savedEditor).not.toBeVisible();
});

test("network editor opens when randomUUID is unavailable on an HTTP LAN origin", async ({ page }) => {
  await page.addInitScript(() => {
    Object.defineProperty(Crypto.prototype, "randomUUID", { value: undefined, configurable: true });
  });
  await mock(page);
  await page.goto("/#/networks");
  await page.getByRole("button", { name: "新增配置" }).click();
  await expect(page.getByRole("dialog", { name: "新增网络配置" })).toBeVisible();
});

test("node deployment bundle uses the entered platform image and private LAN addresses", async ({ page }) => {
  await mock(page);
  const platformRef = "ghcr.io/example/swarmjet:v1.2.3";
  let request: any;
  await page.route("**/api/nodes/remote1/deployment", (route) => {
    request = route.request().postDataJSON();
    expect(route.request().headers()["x-requested-with"]).toBe("swarmjet");
    return route.fulfill({ status: 200, contentType: "application/gzip", body: Buffer.from("bundle") });
  });
  await page.goto("/#/nodes");
  const addNode = page.getByRole("dialog", { name: "添加节点" });
  await expect(addNode).not.toBeVisible();
  await page.getByRole("button", { name: "添加节点" }).click();
  await expect(addNode).toBeVisible();
  await addNode.getByLabel("节点 ID").fill("remote1");
  await addNode.getByLabel("程序镜像").fill(platformRef);
  await addNode.getByLabel("Node 内网访问地址").fill("http://10.0.0.21:8092");
  await addNode.getByLabel("控制面内网地址").fill("http://10.0.0.20:8180");
  const download = page.waitForEvent("download");
  await addNode.getByRole("button", { name: "下载 Node 部署包" }).click();
  expect((await download).suggestedFilename()).toBe("swarmjet-node-remote1.tar.gz");
  expect(request).toMatchObject({ image: platformRef, advertiseUrl: "http://10.0.0.21:8092", controlPlaneUrl: "http://10.0.0.20:8180" });
  await expect(addNode.getByRole("status")).toContainText("部署包已下载");
  await addNode.getByRole("button", { name: "关闭添加节点" }).click();
  await expect(addNode).not.toBeVisible();
});
test("node list failure does not claim all nodes are gone", async ({ page }) => {
  await mock(page);
  const failNodes = (route: import("@playwright/test").Route) => route.fulfill({
    status: 500,
    json: { error: "operation failed; check server configuration and retry" },
  });
  await page.route("**/api/nodes", failNodes);
  await page.goto("/#/nodes");
  await expect(page.getByRole("alert")).toContainText("operation failed");
  await expect(page.getByText("尚无节点注册。")).toHaveCount(0);
  await page.unroute("**/api/nodes", failNodes);
  await page.reload();
  await expect(page.getByRole("button", { name: /内网执行节点/ })).toBeVisible();
  await expect(page.getByRole("alert")).toHaveCount(0);
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
  await page.setViewportSize({ width: 390, height: 900 });
  await page.getByLabel("默认网络", { exact: true }).click();
  await page.screenshot({ path: "test-results/network-select-open-390.png" });
  await page.keyboard.press("Escape");
  await page.getByLabel("默认网络", { exact: true }).selectOption({ label: "临时出口" });
  await page.getByRole("button", { name: /临时出口/ }).click();
  await page.getByRole("dialog", { name: "编辑网络配置" }).getByRole("button", { name: "删除配置" }).click();
  const confirmation = page.getByRole("dialog", { name: "删除网络配置" });
  await expect(confirmation).toContainText("临时出口");
  await page.setViewportSize({ width: 390, height: 900 });
  await page.screenshot({ path: "test-results/network-delete-confirm-390.png" });
  await confirmation.getByRole("button", { name: "删除配置" }).click();
  await expect(page.getByRole("status").filter({ hasText: "网络配置已删除" })).toBeVisible();
  await expect(page.getByText("临时出口", { exact: true })).toHaveCount(0);
  await expect(page.getByLabel("默认网络", { exact: true })).toHaveValue("");
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
  await page.getByRole("button", { name: "测试节点进程与临时容器" }).click();
  await expect(
    page.getByText("节点进程：已连接，HTTP 401", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("临时测试容器：已连接，HTTP 200", { exact: true }),
  ).toBeVisible();
  await detail.getByRole("button", { name: "关闭节点详情" }).click();
  await expect(detail).not.toBeVisible();
  await page.getByRole("button", { name: /内网执行节点/ }).click();
  await expect(detail).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(detail).not.toBeVisible();
});
test("node Agent image accepts a free-form image reference", async ({ page }) => {
  const state = await mock(page);
  await page.goto("/#/nodes");
  await page.getByRole("button", { name: /内网执行节点/ }).click();
  await page.getByLabel("Agent 镜像").fill(publishedRef);
  await page.getByRole("button", { name: "保存节点配置" }).click();
  expect(state.saved().image).toBe(publishedRef);
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
        .getByRole("button", { name: "测试节点进程与临时容器" })
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
