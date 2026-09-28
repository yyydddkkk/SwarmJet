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
    if (path === "/api/session") return route.fulfill({ json: { username: "admin", platformAdmin: true } });
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
  await expect(page.getByRole("heading", { name: "镜像管理", exact: true })).toBeVisible();
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

test("developer option opens platform image versions and starts a platform build", async ({ page }) => {
  await imagesAPI(page);
  let enabled = false;
  let requestedTag = "";
  const platformDigest = "sha256:" + "c".repeat(64);
  const platformRef = `10.0.0.20:5443/swarmjet/platform@${platformDigest}`;
  const builds: any[] = [];
  await page.route("**/api/system/developer-options", (route) => {
    if (route.request().method() === "PUT") {
      expect(route.request().headers()["x-requested-with"]).toBe("swarmjet");
      enabled = route.request().postDataJSON().platformImages;
    }
    return route.fulfill({ json: { platformImages: enabled } });
  });
  await page.route("**/api/registry/platform-images", (route) => route.fulfill({ json: {
    repository: "10.0.0.20:5443/swarmjet/platform",
    images: [{ digest: platformDigest, reference: platformRef, tags: ["dev-20260924-1"] }],
  } }));
  await page.route("**/api/runtime/images/platform", (route) => route.fulfill({ json: { available: true, builds } }));
  await page.route("**/api/runtime/images/platform/builds", (route) => {
    requestedTag = route.request().postDataJSON().tag;
    builds.unshift({ id: "platform-build-1", tag: `10.0.0.20:5443/swarmjet/platform:${requestedTag}`, status: "building", source: "platform", createdAt: "2026-09-24T08:00:00Z" });
    return route.fulfill({ status: 202, json: builds[0] });
  });
  await page.route(`**/api/registry/platform-images/${encodeURIComponent(platformDigest)}/usage`, (route) => route.fulfill({ json: { references: ["节点 remote1"] } }));
  await page.goto("/#/settings/developer");
  await page.getByRole("checkbox", { name: "管理平台程序镜像" }).check();
  await page.getByRole("link", { name: "打开平台镜像管理" }).click();
  await expect(page.getByRole("tab", { name: "平台镜像" })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByRole("region", { name: "平台镜像版本" })).toContainText("dev-20260924-1");
  await page.setViewportSize({ width: 390, height: 844 });
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.getByLabel("新版本标签").fill("dev-20260924-2");
  await page.getByRole("button", { name: "构建并发布", exact: true }).click();
  expect(requestedTag).toBe("dev-20260924-2");
  await expect(page.getByRole("region", { name: "平台镜像构建记录" })).toContainText("dev-20260924-2");
  await page.getByRole("region", { name: "平台镜像版本" }).getByRole("button", { name: "删除版本" }).click();
  await expect(page.getByRole("dialog", { name: "确认删除平台镜像版本" }).getByRole("button", { name: "确认删除" })).toBeDisabled();
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

test("registry passwords can be revealed independently without changing their values", async ({ page }) => {
  await imagesAPI(page);
  await page.route("**/api/registry", (route) => route.fulfill({ json: {
    address: "registry.test:5443", repository: "swarmjet/agent", pushUser: "builder", pullUser: "reader",
    hasPushPassword: false, hasPullPassword: false,
  } }));
  await page.goto("/#/settings/registry");
  await expect(page.getByText("Distribution 镜像仓库")).toBeVisible();
  const push = page.getByLabel("推送密码（留空保留）");
  const pull = page.getByLabel("拉取密码（留空保留）");
  await push.fill("push-secret");
  await pull.fill("pull-secret");
  await page.getByRole("button", { name: "显示推送密码" }).click();
  await expect(push).toHaveAttribute("type", "text");
  await expect(push).toHaveValue("push-secret");
  await expect(pull).toHaveAttribute("type", "password");
  await page.getByRole("button", { name: "隐藏推送密码" }).click();
  await expect(push).toHaveAttribute("type", "password");
  await expect(pull).toHaveValue("pull-secret");
});

test("builtin registry is the default and a custom address can be saved", async ({ page }) => {
  await imagesAPI(page);
  let config = { mode: "builtin", builtinAvailable: true, builtinAddress: "10.0.0.20:5443", address: "10.0.0.20:5443", repository: "swarmjet/agent", pushUser: "", pullUser: "", hasPushPassword: false, hasPullPassword: false };
  await page.route("**/api/registry", (route) => {
    if (route.request().method() === "PUT") config = { ...config, ...route.request().postDataJSON() };
    return route.fulfill({ json: config });
  });
  await page.goto("/#/settings/registry");
  await expect(page.getByText("Distribution 镜像仓库")).toBeVisible();
  await expect(page.getByLabel("仓库来源")).toHaveValue("builtin");
  await expect(page.getByText("10.0.0.20:5443")).toBeVisible();
  await page.getByLabel("仓库来源").selectOption("custom");
  await page.getByLabel("仓库地址（host:port）").fill("registry.example:5443");
  await page.getByRole("button", { name: "保存仓库选择" }).click();
  await expect(page.getByRole("status")).toContainText("仓库配置已保存");
  expect(config.mode).toBe("custom");
  expect(config.address).toBe("registry.example:5443");
});

test("admin can reveal generated registry credentials on demand", async ({ page }) => {
  await imagesAPI(page);
  await page.route("**/api/registry", (route) => route.fulfill({ json: {
    mode: "builtin", builtinAvailable: true, address: "10.0.0.20:5443", repository: "swarmjet/agent",
    pushUser: "builder", pullUser: "reader", hasPushPassword: true, hasPullPassword: true,
  } }));
  await page.route("**/api/registry/reveal", (route) => route.fulfill({ json: { pushPassword: "generated-push", pullPassword: "generated-pull" } }));
  await page.goto("/#/settings/registry");
  await expect(page.getByText("Distribution 镜像仓库")).toBeVisible();
  await expect(page.getByText("generated-push")).toHaveCount(0);
  await page.getByRole("button", { name: "查看已保存密码" }).click();
  await expect(page.getByLabel("推送密码", { exact: true })).toHaveValue("generated-push");
  await expect(page.getByLabel("推送密码", { exact: true })).toHaveAttribute("type", "text");
  await page.getByRole("button", { name: "隐藏推送密码" }).click();
  await expect(page.getByLabel("推送密码", { exact: true })).toHaveAttribute("type", "password");
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

test("registry inventory includes external images, distributes by digest, and protects deletion", async ({ page }) => {
  const reference = `registry.test/swarmjet/agent@${digest}`;
  const image = { digest, reference, tags: ["external", "stable"] };
  const node = { node: { id: "n1", readiness: { status: "unconfigured", images: [] } }, config: { version: 1, name: "节点一", enabled: false, archived: false, capacity: 1, network: { mode: "default" }, image: "", images: [] } };
  let deleted = false;
  let usage: string[] = [];
  await page.route("**/api/**", async (route) => {
    const req = route.request(), path = decodeURIComponent(new URL(req.url()).pathname);
    if (path === "/api/session") return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (path === "/api/projects") return route.fulfill({ json: [] });
    if (path === "/api/nodes" && req.method() === "GET") return route.fulfill({ json: [node] });
    if (path === "/api/nodes/n1/config" && req.method() === "PUT") {
      node.config = req.postDataJSON();
      usage = ["节点 n1"];
      return route.fulfill({ json: node.config });
    }
    if (path === "/api/runtime/images") return route.fulfill({ json: {
      scope: "registry", config: { agents: [], baseImage: "node:22", extraPackages: [], buildArgs: {} }, builds: [], active: null, defaultImage: "", agents, parameters: [],
    } });
    if (path === "/api/registry/images" && req.method() === "GET") return route.fulfill({ json: { repository: "registry.test/swarmjet/agent", images: deleted ? [] : [image] } });
    if (path === `/api/registry/images/${digest}/usage`) return route.fulfill({ json: { references: usage } });
    if (path === `/api/registry/images/${digest}` && req.method() === "DELETE") {
      expect(req.postDataJSON()).toEqual({ expectedTags: ["external", "stable"] });
      deleted = true;
      return route.fulfill({ status: 204, body: "" });
    }
    return route.fulfill({ status: 404, json: { error: "not found" } });
  });
  await page.goto("/#/images");
  await expect(page.getByRole("region", { name: "仓库镜像" })).toContainText("external");
  await expect(page.getByText("外部推送")).toBeVisible();
  await page.setViewportSize({ width: 390, height: 900 });
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: "test-results/registry-images-390.png", fullPage: true });
  await page.getByRole("button", { name: "分发 / 回退" }).click();
  await page.getByRole("checkbox", { name: /节点一/ }).check();
  await page.getByRole("button", { name: "分发到所选节点" }).click();
  await expect.poll(() => node.config.image).toBe(reference);
  await page.getByRole("button", { name: "删除版本" }).click();
  await expect(page.getByRole("dialog", { name: "确认删除镜像版本" })).toContainText("节点 n1");
  await expect(page.getByRole("button", { name: "确认删除" })).toBeDisabled();
  await page.getByRole("button", { name: "取消", exact: true }).click();
  usage = []; node.config.image = "";
  await page.getByRole("button", { name: "删除版本" }).click();
  await page.getByRole("button", { name: "确认删除" }).click();
  await expect(page.getByText("仓库中还没有镜像版本。")).toBeVisible();
});
