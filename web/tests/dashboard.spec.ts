import { expect, test, type Page } from "@playwright/test";

const platform = { id: "platform", name: "平台工程" };
const commerce = { id: "commerce", name: "交易服务" };
const makeTask = (id: string, project: typeof platform, stage: string, title: string, admissionStatus?: string) => ({
  id, project, stage, admissionStatus, paused: false, message: "请查看当前进展。",
  issue: { iid: Number(id), title }, updatedAt: "2026-09-28T04:00:00Z",
});
const tasks = [
  makeTask("142", platform, "waiting_approval", "增加工作区权限检查"),
  makeTask("138", commerce, "blocked", "修复订单重复扣款"),
  makeTask("145", platform, "developing", "支持按项目筛选执行记录"),
  makeTask("151", commerce, "queued", "梳理账单空状态", "waiting"),
];

async function mockDashboard(page: Page) {
  await page.route("**/api/**", route => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    if (path === "/api/session") return route.fulfill({ json: { username: "admin" } });
    if (path === "/api/projects") return route.fulfill({ json: [platform, commerce] });
    if (path === "/api/tasks") {
      const scoped = tasks.filter(task => !url.searchParams.get("project") || task.project.id === url.searchParams.get("project"));
      const group = (task: typeof tasks[number]) => task.stage === "waiting_approval" || task.stage === "blocked" ? "attention" : task.admissionStatus === "waiting" ? "waiting" : "active";
      return route.fulfill({ json: { items: scoped.filter(task => group(task) === url.searchParams.get("view")), counts: { all: scoped.length, active: scoped.filter(task => group(task) === "active").length, waiting: scoped.filter(task => group(task) === "waiting").length, attention: scoped.filter(task => group(task) === "attention").length, ended: 0, completed: 0 }, total: scoped.length } });
    }
    if (path === "/api/operations") return route.fulfill({ json: { inboxPending: 3, inboxRetrying: 1, admissionWaiting: 2, runnerSlotsReserved: 1, measuredAt: "2026-09-28T04:00:00Z" } });
    if (path === "/api/nodes") return route.fulfill({ json: [
      { node: { id: "east", capacity: 2 }, running: 1, online: true },
      { node: { id: "west", capacity: 2 }, running: 0, online: false },
    ] });
    if (path === "/api/system/checks") return route.fulfill({ json: { checkedAt: "2026-09-28T04:00:00Z", checks: [
      { id: "registry", status: "ok", message: "镜像仓库拉取认证正常" },
      { id: "nodes", status: "warning", message: "1 个 Node 心跳中断" },
    ] } });
    if (path === "/api/workbench/issues") return route.fulfill({ json: { items: [], nextCursor: "" } });
    if (path === "/api/workbench/merge-requests") return route.fulfill({ json: { items: [], nextCursor: "" } });
    return route.fulfill({ status: 404, json: { error: "not found" } });
  });
}

test("new shell uses distinct sidebar icons and routes from workbench to control", async ({ page }, testInfo) => {
  await mockDashboard(page);
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "今天先处理这些" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "2 项需要决策" })).toBeVisible();
  const icons = await page.locator(".sidebar .nav-group nav a .ui-icon path").evaluateAll(paths => paths.map(path => path.getAttribute("d")));
  expect(icons).toHaveLength(8);
  expect(new Set(icons).size).toBe(icons.length);
  await page.screenshot({ path: testInfo.outputPath("overview.png"), fullPage: true });
  await page.getByLabel("项目范围").selectOption("platform");
  await expect(page.getByRole("heading", { name: "1 项需要决策" })).toBeVisible();
  await expect(page.getByText("修复订单重复扣款")).toHaveCount(0);
  await page.getByRole("link", { name: "运行总览", exact: true }).click();
  await expect(page.getByRole("heading", { name: "运行总览" })).toBeVisible();
  await expect(page.getByText("镜像仓库拉取认证正常")).toBeVisible();
  await expect(page.getByText("1 个 Node 心跳中断")).toBeVisible();
  await page.getByRole("link", { name: "执行资源", exact: true }).click();
  await expect(page.getByRole("heading", { name: "执行资源" })).toBeVisible();
  await expect(page.getByRole("link", { name: /Runner 环境/ })).toBeVisible();
  await page.getByRole("navigation", { name: "工作台导航" }).getByRole("link", { name: "Issue" }).click();
  await expect(page.locator(".workbench-filters select").first()).toHaveValue("platform");
  await page.locator(".workbench-filters select").first().selectOption("commerce");
  await expect(page.getByLabel("项目范围")).toHaveValue("commerce");
});

test("workbench and control fit a narrow viewport", async ({ page }) => {
  await mockDashboard(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "今天先处理这些" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.getByRole("link", { name: "运行总览", exact: true }).click();
  await expect(page.getByRole("heading", { name: "运行总览" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});
