import { expect, test } from "@playwright/test";

test("platform upgrade prepares, starts, shows node failure, and resumes", async ({ page }) => {
  let stage = "idle";
  let detail = {
    id: "upgrade-1", tag: "v2", status: "ready", stage: "ready", target: { tag: "v2", reference: "registry.example/swarmjet/platform@sha256:new" },
    preflight: [{ id: "registry", label: "镜像仓库", status: "passed", message: "连接正常" }],
    nodes: [{ id: "node-1", status: "pending", currentImage: "registry.example/swarmjet/platform@sha256:old", targetImage: "registry.example/swarmjet/platform@sha256:new", error: undefined as string | undefined }],
    startedAt: undefined as string | undefined,
    error: "",
  };
  await page.route("**/api/**", (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/session") return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (path === "/api/projects") return route.fulfill({ json: [] });
    if (path === "/api/system/upgrades/releases") return route.fulfill({ json: {
      current: { tag: "v1", reference: "registry.example/swarmjet/platform@sha256:old", digest: "sha256:old" },
      releases: [{ tag: "v2" }], nodes: [{ id: "node-1", online: true, platformImage: "registry.example/swarmjet/platform@sha256:old" }],
    } });
    if (path === "/api/system/upgrades" && route.request().method() === "GET") return route.fulfill({ json: { upgrades: stage === "idle" ? [] : [detail] } });
    if (path === "/api/system/upgrades/prepare") { stage = "ready"; return route.fulfill({ json: detail }); }
    if (path === "/api/system/upgrades/upgrade-1/start") {
      stage = "running"; detail = { ...detail, status: "running", stage: "upgrading_nodes", startedAt: "2026-09-28T09:00:00Z", nodes: [{ ...detail.nodes[0], status: "running" }] };
      return route.fulfill({ json: detail });
    }
    if (path === "/api/system/upgrades/upgrade-1/resume") {
      stage = "succeeded"; detail = { ...detail, status: "succeeded", stage: "completed", error: "", nodes: [{ ...detail.nodes[0], status: "succeeded", error: undefined }] };
      return route.fulfill({ json: detail });
    }
    if (path === "/api/system/upgrades/upgrade-1") {
      if (stage === "running") {
        stage = "failed";
        detail = { ...detail, status: "failed", error: "node-1 更新失败", nodes: [{ ...detail.nodes[0], status: "failed", error: "镜像拉取失败" }] };
      }
      return route.fulfill({ json: detail });
    }
    return route.fulfill({ status: 404, json: { error: "not found" } });
  });

  await page.goto("/#/settings/upgrade");
  await expect(page.getByRole("heading", { name: "平台升级" })).toBeVisible();
  await expect(page.getByText("v1", { exact: true })).toBeVisible();
  await expect(page.getByLabel("候选版本")).toHaveValue("v2");
  await page.getByRole("button", { name: "准备并预检" }).click();
  await expect(page.getByText("连接正常")).toBeVisible();
  await expect(page.getByRole("button", { name: "启动升级" })).toBeVisible();
  await page.getByRole("button", { name: "启动升级" }).click();
  await expect(page.getByRole("dialog", { name: "启动平台升级" })).toBeVisible();
  await page.getByRole("button", { name: "确认启动" }).click();
  await expect(page.getByRole("button", { name: "取消升级并解除维护" })).toHaveCount(0);
  await expect(page.getByText("镜像拉取失败")).toBeVisible({ timeout: 8000 });
  await expect(page.getByRole("button", { name: "重试失败步骤" })).toBeVisible();
  await expect(page.getByRole("button", { name: "取消准备" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "取消升级并解除维护" })).toHaveCount(0);
  await page.getByRole("button", { name: "重试失败步骤" }).click();
  await expect(page.getByRole("region", { name: "升级进度" }).locator('[role="status"]')).toHaveText("升级完成");
});

test("platform upgrade can cancel an unstarted preparation", async ({ page }) => {
  let cancelled = false;
  const detail = { id: "upgrade-2", tag: "v2", status: "preparing", preflight: [], nodes: [] };
  await page.route("**/api/**", (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/session") return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (path === "/api/projects") return route.fulfill({ json: [] });
    if (path === "/api/system/upgrades/releases") return route.fulfill({ json: { current: {}, releases: [{ tag: "v2" }] } });
    if (path === "/api/system/upgrades" && route.request().method() === "GET") return route.fulfill({ json: { upgrades: cancelled ? [{ ...detail, status: "cancelled" }] : [detail] } });
    if (path === "/api/system/upgrades/upgrade-2/cancel") { cancelled = true; return route.fulfill({ json: { ...detail, status: "cancelled" } }); }
    if (path === "/api/system/upgrades/upgrade-2") return route.fulfill({ json: { ...detail, status: cancelled ? "cancelled" : "preparing" } });
    return route.fulfill({ status: 404, json: { error: "not found" } });
  });
  await page.goto("/#/settings/upgrade");
  await page.getByRole("button", { name: "取消准备" }).click();
  await expect(page.getByText("已取消", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "准备并预检" })).toBeEnabled();
});

test("platform upgrade can abort only while draining", async ({ page }) => {
  let aborted = false;
  const detail = { id: "upgrade-3", tag: "v3", status: "running", stage: "draining", startedAt: "2026-09-28T09:00:00Z", preflight: [], nodes: [] };
  await page.route("**/api/**", (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/session") return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (path === "/api/projects") return route.fulfill({ json: [] });
    if (path === "/api/system/upgrades/releases") return route.fulfill({ json: { current: {}, releases: [{ tag: "v3" }] } });
    if (path === "/api/system/upgrades" && route.request().method() === "GET") return route.fulfill({ json: { upgrades: [{ ...detail, status: aborted ? "cancelled" : "running", stage: aborted ? "cancelled" : "draining" }] } });
    if (path === "/api/system/upgrades/upgrade-3/abort") { aborted = true; return route.fulfill({ json: { ...detail, status: "cancelled", stage: "cancelled" } }); }
    if (path === "/api/system/upgrades/upgrade-3") return route.fulfill({ json: { ...detail, status: aborted ? "cancelled" : "running", stage: aborted ? "cancelled" : "draining" } });
    return route.fulfill({ status: 404, json: { error: "not found" } });
  });
  await page.goto("/#/settings/upgrade");
  await expect(page.getByRole("region", { name: "升级进度" }).locator('[role="status"]')).toHaveText("升级中 · 等待任务排空");
  await expect(page.getByRole("button", { name: "取消升级并解除维护" })).toBeVisible();
  await page.getByRole("button", { name: "取消升级并解除维护" }).click();
  await expect(page.getByRole("dialog", { name: "取消平台升级" })).toBeVisible();
  await page.getByRole("button", { name: "确认取消并解除维护" }).click();
  await expect(page.getByRole("region", { name: "升级进度" }).locator('[role="status"]')).toHaveText("已取消");
  await expect(page.getByRole("button", { name: "取消升级并解除维护" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "准备并预检" })).toBeEnabled();
});
