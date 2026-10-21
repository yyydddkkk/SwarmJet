import { expect, test } from "@playwright/test";
import { mockHiveAPI } from "./hive-api-mock";

test("overview version link opens that version's files and rules without activating it", async ({ page }) => {
  const state = await mockHiveAPI(page);
  await page.route("**/round8-methodology-version", route => route.fulfill({
    contentType: "text/html",
    body: '<script type="module">import RefreshRuntime from "/@react-refresh";RefreshRuntime.injectIntoGlobalHook(window);window.$RefreshReg$=()=>{};window.$RefreshSig$=()=>type=>type;window.__vite_plugin_react_preamble_installed__=true;</script><div id="root"></div><script type="module" src="/tests/panel-harness.tsx"></script>',
  }));
  await page.goto("/round8-methodology-version");
  await page.getByRole("button", { name: "项目", exact: true }).click();
  const panel = page.getByRole("region", { name: "项目面板" });
  await panel.getByRole("button", { name: "查看第 2 版" }).click();
  await expect(panel.getByTestId("methodology-version")).toContainText("v2");
  await expect(panel.getByRole("combobox", { name: "查看方法论版本" })).toHaveValue("2");
  await expect(panel.getByText(/合并前需要非作者给出通过的结论/)).toBeVisible();
  await panel.getByRole("button", { name: /methodology.yaml/ }).click();
  await expect(panel.getByTestId("methodology-file-content")).toContainText("SwarmJet 方法论 v2");
  await expect(panel.getByTestId("methodology-file-content")).toContainText("message: false");
  expect(state.calls.some(call => call.path === "/api/hives/swarmjet/methodology" && call.query.includes("version=2"))).toBe(true);
  expect(state.calls.filter(call => call.method === "POST" && call.path.endsWith("/methodology/activate"))).toHaveLength(0);
});
