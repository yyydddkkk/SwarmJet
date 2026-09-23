import { expect, test, type Page } from "@playwright/test";

const digest = "sha256:" + "a".repeat(64);
const agents = [
  { id: "opencode", name: "OpenCode" }, { id: "codex", name: "Codex" },
  { id: "dst", name: "DeepSeek Harness TUI" }, { id: "agy", name: "Antigravity" }, { id: "claude", name: "Claude Code" },
];
async function imagesAPI(page: Page) {
  const state = {
    config: { agents: [] as string[], baseImage: "node:22.23.1-bookworm-slim", extraPackages: [] as string[], buildArgs: { CODEX_VERSION: "0.155.1", NPM_REGISTRY: "https://registry.npmjs.org" } },
    builds: [] as any[], active: null as any, defaultImage: "swarmjet-agent:local", agents,
    parameters: [{ key: "CODEX_VERSION", label: "Codex 版本", default: "0.155.1" }, { key: "NPM_REGISTRY", label: "npm 软件源", default: "https://registry.npmjs.org" }],
  };
  let launches = 0;
  await page.route("**/api/**", async (route) => {
    const req = route.request(), path = new URL(req.url()).pathname;
    if (path === "/api/session") return route.fulfill({ json: { username: "admin" } });
    if (path === "/api/projects") return route.fulfill({ json: [] });
    if (path === "/api/runtime/images") return route.fulfill({ json: state });
    if (req.method() !== "GET") expect(req.headers()["x-requested-with"]).toBe("swarmjet");
    if (path === "/api/runtime/images/config") { state.config = req.postDataJSON(); return route.fulfill({ json: state.config }); }
    if (path === "/api/runtime/images/builds") {
      launches++;
      state.config = req.postDataJSON();
      const build = { id: "build-1", tag: "swarmjet-agent:build-1", status: "building", source: "build", config: structuredClone(state.config), createdAt: "2026-09-22T08:00:00Z" };
      state.builds = [build]; return route.fulfill({ status: 202, json: build });
    }
    if (path.endsWith("/log")) return route.fulfill({ json: { log: "#1 Installing selected CLIs\n#2 Build complete\n" } });
    if (path.endsWith("/activate")) {
      state.active = { buildId: state.builds[0].id, tag: state.builds[0].tag, imageId: digest };
      return route.fulfill({ json: state.active });
    }
    if (path.endsWith("/cancel")) { state.builds[0].status = "cancelled"; return route.fulfill({ json: { ok: true } }); }
    if (path.endsWith("/import")) {
      const build = { id: "import-1", tag: req.postDataJSON().reference, imageId: digest, source: "existing", status: "succeeded", config: structuredClone(state.config), createdAt: "2026-09-22T08:00:00Z" };
      state.builds = [build]; return route.fulfill({ status: 201, json: build });
    }
    return route.fulfill({ status: 404, json: { error: "not found" } });
  });
  return { state, launches: () => launches, finish(status = "succeeded") { state.builds[0].status = status; state.builds[0].imageId = digest; if (status === "failed") state.builds[0].error = "镜像构建失败，请查看构建日志。"; } };
}

test("image recipe saves, builds without switching, then explicitly activates", async ({ page }) => {
  const backend = await imagesAPI(page);
  await page.goto("/#/images");
  await expect(page.getByRole("heading", { name: "镜像配置", exact: true })).toBeVisible();
  await expect(page.getByRole("checkbox", { checked: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "保存并构建" })).toBeDisabled();
  expect(backend.launches()).toBe(0);
  await page.getByRole("checkbox", { name: "Codex", exact: true }).check();
  await page.getByText("高级构建参数", { exact: true }).click();
  await page.getByLabel("额外系统包", { exact: true }).fill("ffmpeg libvips-dev");
  await page.getByRole("button", { name: "保存配置", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("构建配置已保存");
  expect(backend.state.config.agents).toEqual(["codex"]);
  expect(backend.state.config.extraPackages).toEqual(["ffmpeg", "libvips-dev"]);
  expect(backend.launches()).toBe(0);
  await page.getByRole("button", { name: "保存并构建" }).click();
  await expect(page.getByRole("region", { name: "构建日志", exact: true })).toContainText("Installing selected CLIs");
  expect(backend.launches()).toBe(1);
  expect(backend.state.active).toBeNull();
  await page.getByLabel("基础镜像", { exact: true }).fill("node:24-bookworm-slim");
  backend.finish();
  await expect(page.getByRole("button", { name: "应用此镜像" })).toBeEnabled();
  await expect(page.getByLabel("基础镜像", { exact: true })).toHaveValue("node:24-bookworm-slim");
  expect(backend.state.builds[0].config.baseImage).toBe("node:22.23.1-bookworm-slim");
  await page.getByRole("button", { name: "应用此镜像" }).click();
  await expect(page.getByRole("region", { name: "当前执行镜像" })).toContainText("swarmjet-agent:build-1");
  await page.reload();
  await expect(page.getByRole("region", { name: "当前执行镜像" })).toContainText("swarmjet-agent:build-1");
  await expect(page.getByRole("checkbox", { name: "Codex", exact: true })).toBeChecked();
});

test("failed and cancelled builds preserve the current image", async ({ page }) => {
  const backend = await imagesAPI(page);
  await page.goto("/#/images");
  await page.getByRole("checkbox", { name: "OpenCode", exact: true }).check();
  await page.getByRole("button", { name: "保存并构建" }).click();
  await page.getByRole("button", { name: "取消构建" }).click();
  await expect(page.getByText("已取消", { exact: true })).toBeVisible();
  expect(backend.state.active).toBeNull();
  await page.getByRole("button", { name: "保存并构建" }).click();
  await expect.poll(() => backend.launches()).toBe(2);
  backend.finish("failed");
  await expect(page.getByText("构建失败", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "应用此镜像" })).toHaveCount(0);
  await expect(page.getByRole("region", { name: "当前执行镜像" })).toContainText("swarmjet-agent:local");
});

test("existing local image can be checked without starting a build", async ({ page }) => {
  const backend = await imagesAPI(page);
  await page.goto("/#/images");
  await page.getByText("使用已有镜像", { exact: true }).click();
  await page.getByLabel("已有镜像地址", { exact: true }).fill("registry.example/team/agent:v2");
  await page.getByRole("button", { name: "检查并添加" }).click();
  await expect(page.getByRole("button", { name: "应用此镜像" })).toBeEnabled();
  expect(backend.launches()).toBe(0);
  expect(backend.state.active).toBeNull();
});

for (const width of [1440, 390]) test(`image settings fit ${width}px and expose advanced options on demand`, async ({ page }) => {
  await imagesAPI(page);
  await page.setViewportSize({ width, height: 1000 });
  await page.goto("/#/images");
  await expect(page.getByRole("checkbox")).toHaveCount(5);
  await expect(page.getByLabel("基础镜像", { exact: true })).not.toBeVisible();
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: `test-results/images-${width}.png`, fullPage: true });
  await page.getByText("高级构建参数", { exact: true }).click();
  await expect(page.getByLabel("基础镜像", { exact: true })).toBeVisible();
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});
