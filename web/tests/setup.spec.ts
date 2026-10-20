// 接入 page (#/legacy/setup): every request is mocked with page.route through
// mockHiveAPI; these tests never touch the real backend (it lands in tasks D/E).
import { expect, test } from "@playwright/test";
import { mockHiveAPI } from "./hive-api-mock";

// Desktop only (ADR section 18). Screenshots land in test-results/ at 1440px.
test.use({
  viewport: { width: 1440, height: 900 },
  permissions: ["clipboard-read", "clipboard-write"],
});

test("GitHub not configured offers the one-click manifest flow", async ({ page }, testInfo) => {
  const state = await mockHiveAPI(page, {
    setup: {
      github: {
        configured: false,
        source: "",
        appId: 0,
        appSlug: "",
        htmlUrl: "",
        installUrl: "",
        installations: 0,
      },
    },
  });
  await page.goto("/#/legacy/setup");

  await expect(page.getByRole("heading", { name: "接入", level: 1 })).toBeVisible();
  await expect(page.getByText("未配置", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "一键创建 GitHub App" })).toBeVisible();
  await page.screenshot({
    path: testInfo.outputPath("setup-github-unconfigured-1440.png"),
    fullPage: true,
  });

  await page.route("https://github.example/**", route =>
    route.fulfill({
      status: 200,
      contentType: "text/html",
      body: "<!doctype html><title>GitHub manifest</title><h1>Review manifest</h1>",
    }),
  );
  const githubRequest = page.waitForRequest(request =>
    request.url().startsWith("https://github.example/"),
  );
  await page.getByRole("button", { name: "一键创建 GitHub App" }).click();
  const request = await githubRequest;
  expect(request.method()).toBe("POST");
  const postData = decodeURIComponent(request.postData() ?? "");
  expect(postData).toContain("manifest=");
  expect(postData).toContain('"name":"swarmjet-test"');

  const calls = state.calls.filter(
    call => call.method === "POST" && call.path === "/api/setup/github/manifest",
  );
  expect(calls).toHaveLength(1);
  expect(calls[0].body).toEqual({ owner: "" });
});

test("GitHub configured shows the App, its source and install links", async ({ page }, testInfo) => {
  await mockHiveAPI(page, {
    setup: {
      github: {
        configured: true,
        source: "database",
        appId: 424242,
        appSlug: "swarmjet-honey",
        htmlUrl: "https://github.com/apps/swarmjet-honey",
        installUrl: "https://github.com/apps/swarmjet-honey/installations/new",
        installations: 3,
      },
    },
  });
  await page.goto("/#/legacy/setup");

  await expect(page.getByRole("heading", { name: "接入", level: 1 })).toBeVisible();
  await expect(page.getByText("已配置", { exact: true })).toBeVisible();
  await expect(page.getByText("swarmjet-honey", { exact: true })).toBeVisible();
  await expect(page.getByText("已保存在数据库中", { exact: true })).toBeVisible();
  await expect(page.getByText("3 个账号或仓库", { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "在 GitHub 上查看" })).toHaveAttribute(
    "href",
    "https://github.com/apps/swarmjet-honey",
  );
  await expect(page.getByRole("link", { name: "安装到仓库" })).toHaveAttribute(
    "href",
    "https://github.com/apps/swarmjet-honey/installations/new",
  );
  await page.screenshot({
    path: testInfo.outputPath("setup-github-configured-1440.png"),
    fullPage: true,
  });
});

test("an App configured from the environment is labelled as such", async ({ page }) => {
  await mockHiveAPI(page, {
    setup: {
      github: {
        configured: true,
        source: "env",
        appId: 777,
        appSlug: "swarmjet-env",
        htmlUrl: "https://github.com/apps/swarmjet-env",
        installUrl: "https://github.com/apps/swarmjet-env/installations/new",
        installations: 1,
      },
    },
  });
  await page.goto("/#/legacy/setup");

  await expect(page.getByText("由环境变量配置", { exact: true })).toBeVisible();
});

test("the GitHub callback reports success", async ({ page }) => {
  await mockHiveAPI(page);
  await page.goto("/#/legacy/setup?github=connected");

  await expect(page.getByRole("status").filter({ hasText: "GitHub App 已创建并保存" })).toBeVisible();
});

test("the GitHub callback reports a failure", async ({ page }) => {
  await mockHiveAPI(page);
  await page.goto("/#/legacy/setup?error=state_mismatch");

  const alert = page.getByRole("alert").filter({ hasText: "GitHub App 连接失败" });
  await expect(alert).toBeVisible();
  await expect(alert).toContainText("state_mismatch");
});

test("the tunnel shows how to enable it when it is off", async ({ page }) => {
  await mockHiveAPI(page);
  await page.goto("/#/legacy/setup");

  await expect(page.getByText("隧道未启用", { exact: true })).toBeVisible();
  await expect(page.getByText(/安装脚本里选择启用隧道/)).toBeVisible();
});

test("the enabled tunnel shows its public URL, sync state and last error", async ({ page }, testInfo) => {
  await mockHiveAPI(page, {
    setup: {
      tunnel: {
        enabled: true,
        url: "https://honey-garden.trycloudflare.com",
        webhookSynced: true,
        lastError: "",
      },
    },
  });
  await page.goto("/#/legacy/setup");

  await expect(page.getByText("已启用", { exact: true })).toBeVisible();
  await expect(
    page.getByRole("link", { name: "https://honey-garden.trycloudflare.com" }),
  ).toBeVisible();
  await expect(page.getByText("已同步", { exact: true })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("setup-tunnel-1440.png"), fullPage: true });
});

test("a failing tunnel sync surfaces its last error", async ({ page }) => {
  await mockHiveAPI(page, {
    setup: {
      tunnel: {
        enabled: true,
        url: "https://honey-garden.trycloudflare.com",
        webhookSynced: false,
        lastError: "github: 401 bad credentials",
      },
    },
  });
  await page.goto("/#/legacy/setup");

  await expect(page.getByText("未同步", { exact: true })).toBeVisible();
  await expect(page.getByText("github: 401 bad credentials", { exact: true })).toBeVisible();
});

test("tokens can be revoked after a confirmation", async ({ page }) => {
  const state = await mockHiveAPI(page);
  await page.goto("/#/legacy/setup");

  await expect(page.getByText("工作笔记本", { exact: true })).toBeVisible();
  const tokenID = state.tokens[0].id;
  await page.getByRole("button", { name: "吊销连接 工作笔记本" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText("用这个 Token 的本地 Agent 会立即失去访问权限");
  await dialog.getByRole("button", { name: "吊销" }).click();

  await expect(page.getByRole("status").filter({ hasText: "已吊销连接「工作笔记本」" })).toBeVisible();
  const deletes = state.calls.filter(call => call.method === "DELETE");
  expect(deletes).toHaveLength(1);
  expect(deletes[0].path).toBe(`/api/tokens/${tokenID}`);
});

test("creating a token shows the plaintext once, plus copyable client configs", async ({ page }, testInfo) => {
  const state = await mockHiveAPI(page, { tokens: [] });
  await page.goto("/#/legacy/setup");

  await page.getByRole("button", { name: "新建连接" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await page.getByLabel("名称").fill("CI 笔记本");
  await page.getByRole("radio", { name: /管理员/ }).check();
  await page.getByRole("button", { name: "创建连接" }).click();

  await expect(page.getByText("关闭后无法再次查看", { exact: true })).toBeVisible();
  const plaintext = await page.getByTestId("token-plaintext").innerText();
  // The store mints 32 random bytes, so the one-time plaintext is 64 hex chars.
  expect(plaintext).toMatch(/^[0-9a-f]{64}$/);
  await page.screenshot({
    path: testInfo.outputPath("setup-token-created-1440.png"),
    fullPage: true,
  });

  const copyTargets: [string, string][] = [
    ["复制 MCP 地址", "http://127.0.0.1:8180/mcp"],
    ["复制 Claude Code", "claude mcp add"],
    ["复制 Codex 配置", "mcp_servers.swarmjet"],
    ["复制 JSON 配置", "mcpServers"],
  ];
  for (const [name, expected] of copyTargets) {
    await page.getByRole("button", { name }).click();
    const clipboard = await page.evaluate(() => navigator.clipboard.readText());
    expect(clipboard).toContain(expected);
    if (name !== "复制 MCP 地址") expect(clipboard).toContain(plaintext);
  }

  await page.getByRole("button", { name: "我已保存，关闭" }).click();
  await expect(page.getByText("CI 笔记本", { exact: true })).toBeVisible();

  const creates = state.calls.filter(
    call => call.method === "POST" && call.path === "/api/tokens",
  );
  expect(creates).toHaveLength(1);
  expect(creates[0].body).toEqual({ name: "CI 笔记本", scope: "admin" });
});
