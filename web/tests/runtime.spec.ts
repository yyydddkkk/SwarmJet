import { expect, test, type Page, type WebSocketRoute } from "@playwright/test";

const status = {
  profile: "default",
  image: "swarmjet-agent:local",
  available: true,
  toolsChecked:true,
  agents:[{id:"opencode",installed:true,configured:true,executionSupported:true},{id:"codex",installed:true,configured:false,executionSupported:true}],
  opencode: { installed: true, configured: true, providers: ["example"] },
  codex: { installed: true, configured: false, executionSupported: false },
};
async function runtimeAPI(page: Page) {
  let modelCalls = 0;
  const calls: string[] = [];
  let enabled:string[] = [];
  const agents=[{id:"opencode",name:"OpenCode",executable:"opencode",setupCommand:"opencode auth login"},{id:"codex",name:"Codex",executable:"codex",setupCommand:"codex login --device-auth"},{id:"dst",name:"DeepSeek Harness TUI",executable:"dsh",setupCommand:"dst"},{id:"agy",name:"Antigravity",executable:"agy",setupCommand:"agy"},{id:"claude",name:"Claude Code",executable:"claude",setupCommand:"claude"}];
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    if (path === "/api/runtime/agents") {
      if(route.request().method()==="PUT") enabled=route.request().postDataJSON().enabled;
      return route.fulfill({json:{agents,enabled}});
    }
    if (path === "/api/session")
      return route.fulfill({ json: { username: "admin" } });
    if (path === "/api/nodes") return route.fulfill({json:[
      {node:{id:"node-a",readiness:{status:"ready"}},online:true,config:{name:"节点 A",image:"registry.test/runner@sha256:aaa"}},
      {node:{id:"node-b",readiness:{status:"ready"}},online:true,config:{name:"节点 B",image:"registry.test/runner@sha256:bbb"}},
    ]});
    if (path.startsWith("/api/runtime/") && path !== "/api/runtime/agents") {
      calls.push(`${path}?node=${url.searchParams.get("node")}`);
      expect(["node-a","node-b"]).toContain(url.searchParams.get("node"));
    }
    if (path === "/api/runtime/status") return route.fulfill({ json: status });
    if (path === "/api/runtime/runner-test") return route.fulfill({json:{ok:true,image:"registry.test/runner@sha256:aaa",agents:["opencode","dst"]}});
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
    if (path === "/api/tasks")
      return route.fulfill({ json: {items: [], total: 0, counts: {all: 0, active: 0, attention: 0, ended: 0, completed: 0}} });
    if (path === "/api/projects")
      return route.fulfill({ json: [] });
    return route.fulfill({ status: 404, json: { error: "not found" } });
  });
  return {modelCalls:() => modelCalls,calls};
}

test("runtime status does not expose the OpenCode model catalog", async ({
  page,
}) => {
  const {modelCalls} = await runtimeAPI(page);
  let connections = 0;
  await page.routeWebSocket("**/api/runtime/terminal?node=*", () => {
    connections++;
  });
  await page.goto("/#/runtime");
  await expect(
    page.getByRole("heading", { name: "Runner 环境", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", {name:"展开 OpenCode 详情",exact:true}).click();
  await expect(
    page.getByRole("region", { name: "OpenCode 环境状态" }),
  ).toContainText("已检测到（不代表认证有效）");
  await expect(
    page.getByRole("region", { name: "Codex 环境状态" }),
  ).toContainText("agent:codex");
  expect(connections).toBe(0);
  await expect(page.getByRole("heading", {name:"模型标识"})).toHaveCount(0);
  await expect(page.getByRole("button", {name:"刷新模型列表"})).toHaveCount(0);
  await page.getByText("环境详情", { exact: true }).click();
  await expect(
    page.getByText("swarmjet-agent:local", { exact: true }),
  ).toBeVisible();
  expect(connections).toBe(0);
  expect(modelCalls()).toBe(0);
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
  await page.routeWebSocket("**/api/runtime/terminal?node=*", (socket) => {
    sockets.push(socket);
    socket.onMessage((raw) => {
      const message = JSON.parse(String(raw));
      messages.push(message);
      if (message.type === "input") socket.send(Buffer.from(message.data));
    });
    socket.send(Buffer.from("runtime-ready\r\n$ "));
  });
  await page.goto("/#/runtime");
  await page.getByRole("button", { name: "打开终端", exact: true }).click();
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
  await page.getByLabel("Runner 配置终端输入").press("Enter");
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
    page.getByRole("button", { name: "重新连接", exact: true }),
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
  await page.routeWebSocket("**/api/runtime/terminal?node=*", (socket) => {
    connections++;
    socket.send(
      JSON.stringify({
        type: "error",
        message: "执行器镜像不可用，请检查部署。",
      }),
    );
  });
  await page.goto("/#/runtime");
  await page.getByRole("button", { name: "打开终端", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("执行器镜像不可用");
  await expect(page.locator(".xterm")).toHaveCount(0);
  expect(connections).toBe(1);
  await page.getByRole("button", { name: "重新连接", exact: true }).click();
  await expect.poll(() => connections).toBe(2);
});

test("leaving runtime closes its socket and returning does not reconnect", async ({
  page,
}) => {
  await runtimeAPI(page);
  let connections = 0;
  let closed = 0;
  await page.routeWebSocket("**/api/runtime/terminal?node=*", (socket) => {
    connections++;
    socket.onClose(() => {
      closed++;
    });
    socket.send(Buffer.from("temporary-output\r\n"));
  });
  await page.goto("/#/runtime");
  await page.getByRole("button", { name: "打开终端", exact: true }).click();
  await expect(page.locator(".xterm-accessibility-tree")).toContainText(
    "temporary-output",
  );
  await page.evaluate(() => { location.hash = "/tasks"; });
  await expect.poll(() => closed).toBe(1);
  await page.getByRole("link", { name: "Runner 环境", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "打开终端", exact: true }),
  ).toBeEnabled();
  await expect(page.locator(".xterm")).toHaveCount(0);
  expect(connections).toBe(1);
});


test("floating terminal opens on demand, maximizes, preserves Escape and closes cleanly", async ({ page }) => {
  await runtimeAPI(page);
  let connections = 0;
  let closed = 0;
  const input: string[] = [];
  const sizes: number[] = [];
  await page.routeWebSocket("**/api/runtime/terminal?node=*", socket => {
    connections++;
    socket.onClose(() => closed++);
    socket.onMessage(raw => {
      const message = JSON.parse(String(raw));
      if (message.type === "input") input.push(message.data);
      if (message.type === "resize") sizes.push(message.rows);
    });
    socket.send(Buffer.from("floating-ready\r\n"));
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto("/#/runtime");
  await expect(page.getByTestId("runtime-terminal")).not.toBeVisible();
  expect(connections).toBe(0);
  const launch = page.getByRole("button", { name: "打开终端", exact: true });
  await launch.click();
  const dialog = page.getByRole("dialog", { name: "Runner 配置终端", exact: true });
  await expect(dialog).toBeVisible();
  await expect(page.locator(".xterm-accessibility-tree")).toContainText("floating-ready");
  const host = await page.getByTestId("runtime-terminal").boundingBox();
  expect(host!.height).toBeGreaterThan(500);
  await page.getByLabel("Runner 配置终端输入").press("Escape");
  await expect.poll(() => input.includes("\u001b")).toBe(true);
  await expect(dialog).toBeVisible();
  const initialRows = sizes.at(-1)!;
  await page.getByRole("button", { name: "最大化", exact: true }).click();
  await expect.poll(() => sizes.at(-1)!).toBeGreaterThan(initialRows);
  expect(connections).toBe(1);
  await page.getByRole("button", { name: "还原窗口" }).click();
  await expect.poll(() => sizes.at(-1)!).toBe(initialRows);
  await page.getByRole("button", { name: "关闭终端" }).click();
  await expect(dialog).not.toBeVisible();
  await expect.poll(() => closed).toBe(1);
  await expect(launch).toBeFocused();
  await expect(page.locator(".xterm")).toHaveCount(0);
  await launch.click();
  await expect.poll(() => connections).toBe(2);
});


test("agents start unchecked and only explicit saved selections persist", async ({page})=>{
  await runtimeAPI(page);
  await page.goto("/#/runtime");
  await expect(page.getByRole("checkbox")).toHaveCount(5);
  await expect(page.getByRole("checkbox",{checked:true})).toHaveCount(0);
  await expect(page.getByText("工具安装",{exact:true}).first()).not.toBeVisible();
  await page.getByRole("button",{name:"展开 OpenCode 详情",exact:true}).click();
  await expect(page.getByText("工具安装",{exact:true}).first()).toBeVisible();
  await expect(page.getByRole("region",{name:"Codex 环境状态"}).getByText("工具安装",{exact:true})).not.toBeVisible();
  await page.getByRole("button",{name:"收起 OpenCode 详情",exact:true}).click();
  await page.getByRole("checkbox",{name:"Codex",exact:true}).check();
  await page.getByRole("button",{name:"保存启用列表",exact:true}).click();
  await expect(page.getByRole("status").filter({hasText:"启用列表已保存"})).toBeVisible();
  await page.reload();
  await expect(page.getByRole("checkbox",{name:"Codex",exact:true})).toBeChecked();
  await expect(page.getByRole("checkbox",{checked:true})).toHaveCount(1);
});

test("Runner test and status follow the selected Node", async ({page}) => {
  const {calls} = await runtimeAPI(page);
  await page.goto("/#/runtime");
  await expect(page.getByLabel("选择 Node")).toHaveValue("node-a");
  await page.getByRole("button",{name:"测试此 Node 的 Runner"}).click();
  await expect(page.getByRole("status").filter({hasText:"Runner 镜像启动成功"})).toContainText("dst");
  expect(calls).toContain("/api/runtime/runner-test?node=node-a");
  await page.getByLabel("选择 Node").selectOption("node-b");
  await expect(page.getByLabel("选择 Node")).toHaveValue("node-b");
  await expect(page.getByText("registry.test/runner@sha256:bbb")).toBeVisible();
  await expect.poll(() => calls.includes("/api/runtime/status?node=node-b")).toBe(true);
  expect(calls.some((call) => call.startsWith("/api/runtime/models?"))).toBe(false);
});

test("minimizing terminal preserves connection and closing it disconnects", async ({page}) => {
  await runtimeAPI(page);
  let connections = 0, closed = 0;
  await page.routeWebSocket("**/api/runtime/terminal?node=*", socket => {
    connections++;
    socket.onClose(() => closed++);
    socket.send(Buffer.from("alive\r\n"));
  });
  await page.goto("/#/runtime");
  await page.getByRole("button",{name:"打开终端",exact:true}).click();
  await expect(page.locator(".xterm-accessibility-tree")).toContainText("alive");
  await page.getByRole("button",{name:"最小化"}).click();
  await expect(page.getByText("Runner 配置终端已最小化，连接保持中。")).toBeVisible();
  expect(connections).toBe(1);
  expect(closed).toBe(0);
  await page.getByRole("button",{name:"恢复终端"}).last().click();
  await expect(page.locator(".xterm-accessibility-tree")).toContainText("alive");
  expect(connections).toBe(1);
  await page.getByRole("button",{name:"最小化"}).click();
  await page.getByRole("button",{name:"关闭终端"}).last().click();
  await expect.poll(() => closed).toBe(1);
  await expect(page.getByRole("button",{name:"打开终端",exact:true})).toBeVisible();
});
