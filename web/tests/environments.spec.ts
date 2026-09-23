import { test, expect, type Page } from "@playwright/test";
const ref = "registry.test/swarmjet/agent@sha256:" + "a".repeat(64);
async function mock(page: Page) {
  const profiles: any[] = [];
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
    if (path === "/api/networks") {
      if (r.method() === "PUT") {
        saved = r.postDataJSON();
        profiles.push({ ...saved, version: 1, hasPassword: !!saved.password });
      }
      return route.fulfill({
        json:
          r.method() === "PUT"
            ? profiles[profiles.length - 1]
            : { profiles, defaultId: "" },
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
});
test("node drain preserves image and scopes network tests to selected node", async ({
  page,
}) => {
  const state = await mock(page);
  await page.goto("/#/nodes");
  await page.getByRole("button", { name: /内网执行节点/ }).click();
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
  });
