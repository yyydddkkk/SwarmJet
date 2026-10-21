import { expect, test, type Page } from "@playwright/test";
import { mockHiveAPI, type MockState } from "./hive-api-mock";
import type { Message } from "../src/data";

const message = (id: string, trust: string): Message => ({
  id, trust, hiveId: "swarmjet", from: "system", to: "queen", kind: "event",
  chainId: id, hop: 0, body: `消息 ${id}`, state: "delivered",
  createdAt: "2026-10-20T12:00:00Z",
});

async function open(page: Page, prepare: (state: MockState) => void) {
  const state = await mockHiveAPI(page);
  prepare(state);
  await page.route("**/provenance-test", (route) => route.fulfill({
    contentType: "text/html",
    body: '<script type="module">import RefreshRuntime from "/@react-refresh";RefreshRuntime.injectIntoGlobalHook(window);window.$RefreshReg$=()=>{};window.$RefreshSig$=()=>type=>type;window.__vite_plugin_react_preamble_installed__=true;</script><div id="root"></div><script type="module" src="/tests/panel-harness.tsx"></script>',
  }));
  await page.goto("/provenance-test");
  await expect(page.getByRole("button", { name: "主Agent详情", exact: true })).toBeVisible();
  await page.evaluate(() => (window as unknown as {panelHarnessSyncMessages(): Promise<void>}).panelHarnessSyncMessages());
}

test("新对话保留管理员、Agent、系统和外部消息来源", async ({ page }) => {
  await open(page, (state) => {
    state.messages = [
      { ...message("administrator", "user"), from: state.localAddress },
      { ...message("agent", "agent"), from: "queen", to: state.localAddress },
      message("system", "system"),
      message("external", "external"),
    ];
  });
  await page.getByRole("button", { name: "主Agent详情", exact: true }).click();
  for (const label of ["来自管理员", "来自 Agent", "来自系统", "外部内容，未经核实"])
    await expect(page.getByText(label, { exact: true })).toBeVisible();
});

test("较新服务端的未知来源仍如实显示", async ({ page }) => {
  await open(page, (state) => { state.messages = [message("partner", "partner")]; });
  await page.getByRole("button", { name: "主Agent详情", exact: true }).click();
  await expect(page.getByText("partner", { exact: true })).toBeVisible();
});

test("待处理事件正文保留外部来源提示", async ({ page }) => {
  await open(page, (state) => {
    state.messages.push({ ...message("signal-full", "external"), to: "hive", body: "待处理事件的完整原文" });
  });
  await page.getByRole("button", { name: "事件详情", exact: true }).click();
  await expect(page.getByText("待处理事件的完整原文", { exact: true })).toBeVisible();
  await expect(page.getByText("外部内容，未经核实", { exact: true })).toBeVisible();
});
