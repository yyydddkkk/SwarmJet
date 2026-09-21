import { expect, test, type Page, type WebSocketRoute } from "@playwright/test";

const status = {
  profile: "default",
  image: "swarmjet-agent:local",
  available: true,
  opencode: { installed: true, configured: true, providers: ["example"] },
  codex: { installed: true, configured: false, executionSupported: false },
};
async function runtimeAPI(page: Page) {
  let modelCalls = 0;
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/session")
      return route.fulfill({ json: { username: "admin" } });
    if (path === "/api/runtime/status") return route.fulfill({ json: status });
    if (path === "/api/runtime/models") {
      modelCalls++;
      return route.fulfill({
        json: {
          models: ["example/fast", "example/review"],
          providers: ["example"],
          warnings: ["模型清单不会验证账号余额。"],
        },
      });
    }
    if (path === "/api/tasks" || path === "/api/projects")
      return route.fulfill({ json: [] });
    return route.fulfill({ status: 404, json: { error: "not found" } });
  });
  return () => modelCalls;
}

test("runtime status and model catalog are explicit, searchable and copyable", async ({
  page,
  context,
}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  const modelCalls = await runtimeAPI(page);
  let connections = 0;
  await page.routeWebSocket("**/api/runtime/terminal", () => {
    connections++;
  });
  await page.goto("/#/runtime");
  await expect(
    page.getByRole("heading", { name: "执行器环境", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("region", { name: "OpenCode 环境状态" }),
  ).toContainText("检测到登录配置");
  await expect(
    page.getByRole("region", { name: "Codex 环境状态" }),
  ).toContainText("任务适配器尚未实现");
  expect(connections).toBe(0);
  expect(modelCalls()).toBe(0);
  await page.getByRole("button", { name: "刷新模型列表", exact: true }).click();
  await expect(page.getByText("模型清单不会验证账号余额。")).toBeVisible();
  await page.getByLabel("搜索模型标识").fill("review");
  await expect(
    page.getByRole("button", { name: "复制模型 example/fast" }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "复制模型 example/review" }).click();
  await expect
    .poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toBe("example/review");
  await page.getByText("环境详情", { exact: true }).click();
  await expect(
    page.getByText("swarmjet-agent:local", { exact: true }),
  ).toBeVisible();
  expect(connections).toBe(0);
});

test("terminal sends input without automatic Enter, fits narrow screens and clears on disconnect", async ({
  page,
  context,
}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await runtimeAPI(page);
  const messages: Array<{
    type: string;
    data?: string;
    cols?: number;
    rows?: number;
  }> = [];
  const sockets: WebSocketRoute[] = [];
  await page.routeWebSocket("**/api/runtime/terminal", (socket) => {
    sockets.push(socket);
    socket.onMessage((raw) => {
      const message = JSON.parse(String(raw));
      messages.push(message);
      if (message.type === "input") socket.send(Buffer.from(message.data));
    });
    socket.send(Buffer.from("runtime-ready\r\n$ "));
  });
  await page.goto("/#/runtime");
  await page.getByRole("button", { name: "打开配置终端" }).click();
  await expect(page.getByRole("button", { name: "断开连接" })).toBeVisible();
  await expect
    .poll(() =>
      messages.some(
        (message) => message.type === "resize" && (message.cols || 0) > 0,
      ),
    )
    .toBe(true);
  await expect(page.locator(".xterm-accessibility-tree")).toContainText(
    "runtime-ready",
  );
  await page.locator(".xterm-screen").scrollIntoViewIfNeeded();
  const screen = await page.locator(".xterm-screen").boundingBox();
  expect(screen).not.toBeNull();
  await page.mouse.dblclick(screen!.x + 35, screen!.y + 8);
  await expect(
    page.getByRole("button", { name: "复制选中内容" }),
  ).toBeEnabled();
  await page.getByRole("button", { name: "复制选中内容" }).click();
  await expect
    .poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toContain("runtime");
  await page
    .getByRole("button", { name: "opencode auth login", exact: true })
    .click();
  await expect
    .poll(() =>
      messages
        .filter((message) => message.type === "input")
        .map((message) => message.data),
    )
    .toEqual(["opencode auth login"]);
  await page.getByLabel("执行器配置终端输入").press("Enter");
  await expect
    .poll(() =>
      messages.some(
        (message) => message.type === "input" && message.data === "\r",
      ),
    )
    .toBe(true);
  const desktopCols = messages.find(
    (message) => message.type === "resize",
  )!.cols!;
  await page.setViewportSize({ width: 390, height: 844 });
  await expect
    .poll(
      () =>
        messages.filter((message) => message.type === "resize").at(-1)!.cols!,
    )
    .toBeLessThan(desktopCols);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.getByRole("button", { name: "断开连接" }).click();
  await expect(page.locator(".xterm")).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "打开配置终端" }),
  ).toBeEnabled();
  expect(sockets).toHaveLength(1);
  expect(
    await page.evaluate(() =>
      Object.values(localStorage).some((value) =>
        value.includes("runtime-ready"),
      ),
    ),
  ).toBe(false);
});

test("terminal backend errors are visible and reconnect requires another click", async ({
  page,
}) => {
  await runtimeAPI(page);
  let connections = 0;
  await page.routeWebSocket("**/api/runtime/terminal", (socket) => {
    connections++;
    socket.send(
      JSON.stringify({
        type: "error",
        message: "执行器镜像不可用，请检查部署。",
      }),
    );
  });
  await page.goto("/#/runtime");
  await page.getByRole("button", { name: "打开配置终端" }).click();
  await expect(page.getByRole("alert")).toContainText("执行器镜像不可用");
  await expect(page.locator(".xterm")).toHaveCount(0);
  expect(connections).toBe(1);
  await page.getByRole("button", { name: "打开配置终端" }).click();
  await expect.poll(() => connections).toBe(2);
});

test("leaving runtime closes its socket and returning does not reconnect", async ({
  page,
}) => {
  await runtimeAPI(page);
  let connections = 0;
  let closed = 0;
  await page.routeWebSocket("**/api/runtime/terminal", (socket) => {
    connections++;
    socket.onClose(() => {
      closed++;
    });
    socket.send(Buffer.from("temporary-output\r\n"));
  });
  await page.goto("/#/runtime");
  await page.getByRole("button", { name: "打开配置终端" }).click();
  await expect(page.locator(".xterm-accessibility-tree")).toContainText(
    "temporary-output",
  );
  await page.getByRole("link", { name: "任务中心", exact: true }).click();
  await expect.poll(() => closed).toBe(1);
  await page.getByRole("link", { name: "执行器环境", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "打开配置终端" }),
  ).toBeEnabled();
  await expect(page.locator(".xterm")).toHaveCount(0);
  expect(connections).toBe(1);
});
