import { test, expect, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { mkdirSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { round8Project } from "../src/fixtures/round8";

const evidence = resolve(process.cwd(), "../docs/claude/round8-panels");
const previewHTML = `<!doctype html><html lang="zh-CN"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/fonts/fonts.css"></head><body><script type="module">import RefreshRuntime from "/@react-refresh";RefreshRuntime.injectIntoGlobalHook(window);window.$RefreshReg$=()=>{};window.$RefreshSig$=()=>type=>type;window.__vite_plugin_react_preamble_installed__=true;</script><div id="root"></div><script type="module" src="/tests/round8-panels-preview.tsx"></script></body></html>`;
async function preview(page: Page, theme: "bright" | "night" = "bright") {
  await page.addInitScript((theme) => {
    localStorage.setItem("sj-brief", "off");
    localStorage.setItem("sj-legend-seen", "1");
    localStorage.setItem("sj-theme", theme);
  }, theme);
  await page.route("**/round8-panel-preview", route => route.fulfill({contentType: "text/html", body: previewHTML}));
  await page.goto("/round8-panel-preview");
  const command = page.getByLabel("对话与命令");
  await command.fill("test-for-myself");
  await command.press("Enter");
  await expect(page.getByRole("navigation", {name: "位置"})).toContainText("test-for-myself");
}
async function objectFromList(page: Page, search: string) {
  await page.getByRole("button", {name: "列表", exact: true}).click();
  await page.getByRole("textbox", {name: /查找项目/}).fill(search);
  const rows = page.getByRole("dialog", {name: "列表", exact: true}).locator("tr.item");
  await expect(rows).toHaveCount(1);
  await rows.click();
  await expect(page.getByRole("region", {name: "对象详情"})).toBeVisible();
}

test.use({viewport: {width: 1440, height: 900}});

test("claimed Issue shows a short Agent identifier and copies its full value", async ({page, context}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await preview(page);
  await objectFromList(page, "signal-0");
  const detail = page.getByRole("region", {name: "对象详情"});
  await expect(detail.getByRole("link", {name: "在 GitHub 查看"})).toBeVisible();
  const copy = detail.getByRole("button", {name: /复制完整处理 Agent.*完整值 forager-00000000/});
  await expect(copy).toHaveText("00000000");
  await copy.click();
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe("forager-00000000");
});

test("realistic fixture and long details stay readable", async ({page}) => {
  const fixture = round8Project();
  expect(fixture.agents.length).toBeGreaterThanOrEqual(40);
  expect(fixture.prs.filter(p => p.state === "open").length).toBeGreaterThanOrEqual(14);
  expect(fixture.signals.length).toBeGreaterThanOrEqual(11);
  expect(fixture.messages.length).toBeGreaterThanOrEqual(900);
  expect(fixture.agents[0].task!.length).toBeGreaterThanOrEqual(3000);
  await preview(page);
  await objectFromList(page, "forager-00000000");
  const panel = page.getByRole("region", {name: "对象详情"});
  await expect(panel.locator("h2")).toContainText("Issue #100");
  const agentHeading = panel.locator("h2");
  await expect(agentHeading).toHaveCSS("white-space", "nowrap");
  await expect(agentHeading).toHaveCSS("text-overflow", "ellipsis");
  await expect(agentHeading).toHaveAttribute("title", fixture.signals[0].title);
  const agentHeadingSize = await agentHeading.evaluate(el => ({height:el.getBoundingClientRect().height, lineHeight:parseFloat(getComputedStyle(el).lineHeight)}));
  expect(agentHeadingSize.height).toBeLessThanOrEqual(agentHeadingSize.lineHeight + 2);
  const task = panel.locator(".pc-long-text").first();
  await expect(task.locator(".pc-clamped")).toBeVisible();
  const metrics = await task.locator("p").evaluate(el => ({height: el.getBoundingClientRect().height, lineHeight: parseFloat(getComputedStyle(el).lineHeight)}));
  expect(metrics.height).toBeLessThanOrEqual(metrics.lineHeight * 4 + 2);
  await expect(panel.locator(".pc-messages li").filter({hasText: fixture.agents[0].task!})).toHaveCount(0);
  await task.getByRole("button", {name: "展开任务全文"}).click();
  await expect(task.locator("p")).toContainText(fixture.agents[0].task!);
  await page.getByRole("button", {name: "关闭对象详情"}).click();
  await expect(panel).toHaveCount(0, {timeout: 1500});
  await objectFromList(page, "PR #2");
  await expect(panel.locator("h2")).toContainText(fixture.prs[1].title!);
  await expect(panel.locator("h2")).toHaveAttribute("title", fixture.prs[1].title!);
  await expect(panel.locator("h2")).toHaveCSS("white-space", "nowrap");
  const prHeadingSize = await panel.locator("h2").evaluate(el => ({height:el.getBoundingClientRect().height, lineHeight:parseFloat(getComputedStyle(el).lineHeight)}));
  expect(prHeadingSize.height).toBeLessThanOrEqual(prHeadingSize.lineHeight + 2);
  await expect(panel).toContainText("GitHub CI");
  await expect(panel).toContainText("独立验证");
  await expect(panel).toContainText("当前提交还没有验证结论");
  await expect(panel.locator(".pc-attestation .pc-clamped")).toBeVisible();
  await expect(panel.locator(".pc-attestation")).toContainText("其他提交");
  await expect(panel.getByRole("button", {name: "确认合并"})).toHaveCSS("color", "rgb(138, 103, 22)");
});

test("panels animate out, close access, remain usable at 375px and axe reports all violations", async ({page}, testInfo) => {
  await preview(page);
  const violations: Record<string, unknown> = {};
  const check = async (key: string, selector: string) => {
    const result = await new AxeBuilder({page}).include(selector).analyze();
    violations[key] = result.violations.map(v => ({id:v.id, impact:v.impact, nodes:v.nodes.map(n => n.target)}));
  };
  await page.locator(".stars-cartouche").click();
  await expect(page.locator(".pc-left")).toHaveClass(/pc-open/);
  await expect(page.locator(".pc-left")).toHaveCSS("background-color", "rgba(244, 243, 239, 0.96)");
  await check("project", ".pc-left");
  await page.getByRole("button", {name: "关闭项目面板"}).click();
  await expect(page.locator(".pc-left")).toHaveAttribute("inert", "");
  await expect(page.locator(".pc-left")).toHaveCount(0, {timeout: 1500});
  await page.getByRole("button", {name: "SwarmJet 平台设置", exact: true}).click();
  await expect(page.locator(".pc-platform")).toHaveCSS("background-color", "rgb(244, 243, 239)");
  await expect(page.locator(".pc-platform-body .pc-page-head h2")).toHaveCSS("font-size", "30px");
  await check("platform", ".pc-platform");
  await page.getByRole("button", {name: "关闭平台设置"}).click();
  await objectFromList(page, "forager-00000000");
  await expect(page.locator(".pc-right")).toHaveCSS("background-color", "rgba(244, 243, 239, 0.96)");
  await check("object", ".pc-right");
  await expect(page.locator(".stars-tools")).toHaveCSS("opacity", "0");
  await page.setViewportSize({width: 375, height: 812});
  await expect(page.locator(".pc-right")).toHaveCSS("width", "375px");
  await expect(page.locator(".pc-right .pc-long-text").first()).toBeVisible();
  mkdirSync(evidence, {recursive: true});
  writeFileSync(resolve(evidence, "axe-violations.json"), JSON.stringify(violations, null, 2));
  await testInfo.attach("axe-violations", {body: JSON.stringify(violations, null, 2), contentType:"application/json"});
  expect(Object.values(violations).flat()).toEqual([]);
});

test("visual evidence for every panel and subpage in bright and night", async ({page, context}) => {
  test.setTimeout(180_000);
  mkdirSync(evidence, {recursive: true});
  const allAxe: Record<string, unknown> = {};
  const checkAxe = async (key: string, selector: string) => {
    const result = await new AxeBuilder({page}).include(selector).analyze();
    allAxe[key] = result.violations.map(v => ({id:v.id, impact:v.impact, nodes:v.nodes.map(n => n.target)}));
  };
  const design = await context.newPage();
  await design.setViewportSize({width: 1440, height: 900});
  await design.addInitScript(() => { localStorage.setItem("sj-brief", "off"); localStorage.setItem("sj-legend-seen", "1"); });
  for (const theme of ["bright", "night"] as const) {
    await preview(page, theme);
    await design.goto("http://localhost:4731");
    await design.evaluate((theme) => { (window as unknown as {setTheme(v:string):void}).setTheme(theme); }, theme);
    await page.locator(".stars-cartouche").click();
    await design.evaluate("closeBrief();goSys(T);openAlmanac();document.querySelector('#almanac').scrollTop=0");
    await page.waitForTimeout(800);
    await design.waitForTimeout(800);
    await page.screenshot({path:resolve(evidence, `implementation-project-overview-${theme}.png`)});
    await design.screenshot({path:resolve(evidence, `design-project-overview-${theme}.png`)});
    await checkAxe(`project-overview-${theme}`, ".pc-left");
    for (const [tab, name] of [["methodology","编辑方法论与版本历史"],["settings","更多项目设置"],["usage","查看花费详情"]] as const) {
      await page.locator(".pc-left").getByRole("button", {name, exact:true}).click();
      await page.screenshot({path:resolve(evidence, `implementation-project-${tab}-${theme}.png`)});
      await design.screenshot({path:resolve(evidence, `design-project-${tab}-${theme}.png`)});
      await checkAxe(`project-${tab}-${theme}`, ".pc-left");
      await page.locator(".pc-left").getByRole("button", {name: "返回项目面板"}).click();
    }
    await page.locator(".pc-left").evaluate(el => el.scrollTop = el.scrollHeight);
    await design.locator("#almanac").evaluate(el => el.scrollTop = el.scrollHeight);
    await page.screenshot({path:resolve(evidence, `implementation-project-memory-${theme}.png`)});
    await design.screenshot({path:resolve(evidence, `design-project-memory-${theme}.png`)});
    await checkAxe(`project-memory-${theme}`, ".pc-left");
    await page.getByRole("button", {name: "关闭项目面板"}).click();
    await design.evaluate("closeAlmanac()");
    await page.getByRole("button", {name: "SwarmJet 平台设置", exact: true}).click();
    await page.waitForTimeout(850);
    for (const [tab, name] of [["setup","接入"],["models","模型"],["connections","连接"],["ledger","账簿"],["account","账号"]] as const) {
      await page.locator(".pc-platform > nav").getByRole("button", {name, exact:true}).click();
      await design.evaluate((name) => { (window as unknown as {openAppendix(v:string):void}).openAppendix(name); }, name);
      await design.waitForTimeout(780);
      await page.screenshot({path:resolve(evidence, `implementation-platform-${tab}-${theme}.png`)});
      await design.screenshot({path:resolve(evidence, `design-platform-${tab}-${theme}.png`)});
      await checkAxe(`platform-${tab}-${theme}`, ".pc-platform");
    }
    await page.getByRole("button", {name: "关闭平台设置"}).click();
    await design.evaluate("closeAppendix()");
    await expect(page.locator(".pc-platform")).toHaveCount(0, {timeout:1500});
    for (const [key, search] of [
      ["agent", "forager-00000000"],
      ["pr", "PR #2"],
      ["signal", "signal-0"],
      ["queen", "主 Agent"],
    ] as const) {
      await objectFromList(page, search);
      if (key === "signal") {
        const signal = page.getByRole("region", {name: "对象详情"});
        await expect(signal.locator(".pc-kicker")).toContainText("外部事件");
        await expect(signal.locator("h2")).toHaveAttribute("title", "Issue #100：处理超长字段与重复导入");
        await expect(signal.getByRole("link", {name: "在 GitHub 查看"})).toHaveAttribute("href", "https://example.test/issues/100");
      }
      await design.evaluate("lens(B.find(b=>b.kind==='comet'&&b.state==='busy'))");
      await page.waitForTimeout(800);
      await design.waitForTimeout(800);
      await page.screenshot({path:resolve(evidence, `implementation-object-${key}-${theme}.png`)});
      await design.screenshot({path:resolve(evidence, `design-object-${key}-${theme}.png`)});
      await checkAxe(`object-${key}-${theme}`, ".pc-right");
      await page.getByRole("button", {name: "关闭对象详情"}).click();
      await expect(page.locator(".pc-right")).toHaveCount(0, {timeout:1500});
    }
  }
  writeFileSync(resolve(evidence, "axe-all-pages.json"), JSON.stringify(allAxe, null, 2));
  expect(Object.values(allAxe).flat()).toEqual([]);
  await design.close();
});
