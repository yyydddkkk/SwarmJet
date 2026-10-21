import { test, expect } from "@playwright/test";
import { resolve } from "node:path";
import { writeFileSync } from "node:fs";

const out = resolve(process.cwd(), "../docs/claude/round8-starmap");
const saveCanvas = async (locator: any, path: string) => {
  const url = await locator.evaluate((el: HTMLCanvasElement) => el.toDataURL("image/png"));
  writeFileSync(resolve(out, path), Buffer.from(url.split(",")[1], "base64"));
};
const variants = [
  ["bright", "classic"], ["paper", "classic"], ["night", "classic"],
  ["bright", "simple"], ["paper", "simple"], ["night", "simple"],
] as const;

test("same round8 fixture in current canvas and design reference", async ({ page, context }) => {
  test.setTimeout(120000);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.route("**/api/**", (r) => r.fulfill({ status: 401, json: { error: "test session" } }));
  await page.goto("/");
  const fixture = await page.evaluate(async () => {
    const load = (p: string) => import(/* @vite-ignore */ p);
    const [R, DOM, data, UI, Canvas, fixtures, themes] = await Promise.all([
      load("/node_modules/.vite/deps/react.js"), load("/node_modules/.vite/deps/react-dom_client.js"),
      load("/src/data/index.ts"), load("/src/stars/ui.ts"), load("/src/stars/CanvasSlot.tsx"),
      load("/src/fixtures/round8.ts"), load("/src/stars/theme.ts"),
    ]);
    document.getElementById("root")!.style.display = "none";
    const el = document.createElement("main"); el.className = "stars-app"; document.body.append(el);
    const snapshot = fixtures.round8Project();
    const mock = data.createMockDataSource({ [snapshot.hive.id]: snapshot });
    const store = data.createProjectStore(snapshot.hive.id, mock.source); store.start();
    const ui = UI.createStarsUI();
    const props = { ui, projects: [{ hive: snapshot.hive, activeForagers: 0, pendingApprovals: 0, inboxBacklog: 0 }], stores: new Map([[snapshot.hive.id, store]]), principal: { id: "test", username: "test" }, onLogout() {}, reloadProjects() {} };
    (DOM.createRoot ?? DOM.default.createRoot)(el).render((R.createElement ?? R.default.createElement)(data.DataProvider, { source: mock.source }, (R.createElement ?? R.default.createElement)(Canvas.default, props)));
    (window as any).__visual = { mock, store, ui, themes, snapshot };
    return { snapshot, derived: data.deriveProject(snapshot) };
  });
  await page.evaluate(() => { const v = (window as any).__visual; v.mock.ready(v.snapshot.hive.id); });
  await expect(page.locator(".stars-canvas")).toHaveAttribute("data-history", String(fixture.derived.history.length));

  const ref = await context.newPage();
  await ref.setViewportSize({ width: 1440, height: 900 });
  await ref.goto("http://localhost:4731/");
  await ref.evaluate(({ snapshot, derived }) => {
    // The reference's own constructors and layout functions consume the same entities.
    const w = window as any;
    w.eval(`
      A.x=0;A.y=0;A.id='swarmjet';A.on=true;
      SYS.splice(0,SYS.length,A);B=[];needs.splice(0,needs.length);
    `);
    (w as any).__fixture = { snapshot, derived };
    w.eval(`
      const fx=window.__fixture, snap=fx.snapshot, drv=fx.derived;A.name=snap.hive.name;
      for(const n of drv.mapNodes){
        if(n.kind==='comet'){
          const a=snap.agents.find(x=>x.id===n.id),r=118+hh(n.id+':r')*65;
          comet(A,n.id,'worker',r,{state:a.state==='active'||a.state==='creating'?'busy':a.state==='failed'?'failed':'parked',short:n.label,task:a.task,target:a.issueNumber?'signal-'+(a.issueNumber-100):undefined});
        }else if(n.kind==='planet'){
          const p=snap.prs.find(x=>x.number===n.prNumber);
          pr(A,n.id,'PR #'+p.number,.15+p.number*TAU/16,{title:p.title,state:p.ready?'ready':'open',st:Date.now()-Date.parse(p.updatedAt)>7*864e5?'stale':'open'});
        }else if(n.kind==='signal'){
          const q=snap.signals.find(x=>x.messageId===n.id);
          signal(A,n.id,'Issue #'+q.number,3.05+(q.number-100)*.16,{title:q.title,claimed:q.claimed});
        }else if(n.kind==='queue')queue(A,n.id);
      }
      for(const n of drv.mapNodes.filter(x=>x.kind==='satellite'))moon(A,n.id,n.parentId);
      for(const p of drv.prGroups.cluster)pr(A,'swarmjet:pr:'+p.number,'PR #'+p.number,.15+p.number*TAU/16,{title:p.title,state:p.ready?'ready':'open',st:Date.now()-Date.parse(p.updatedAt)>7*864e5?'stale':'open'});
      for(const q of drv.issueGroups.cluster)signal(A,q.messageId,'Issue #'+q.number,3.05+(q.number-100)*.16,{title:q.title,claimed:q.claimed});
      for(const c of drv.failedCI)signal(A,c.id,'CI',5,{ci:true,title:c.reason,claimed:true});
      A.hist=drv.history.map(h=>({id:h.id,sys:A,kind:'hist',out:h.result,age:h.ageHours,title:h.summary,dust:h.dust,x:A.x+h.x,y:A.y+h.y}));
      drv.decisions.forEach(d=>needs.push({sys:A.id,pr:d.id}));
      layoutSys(A);closeBrief();$('legend').classList.remove('open');$('t-legend').classList.remove('on');
      window.__refCounts={nodes:B.filter(x=>x.kind!=='cluster').length,history:A.hist.length,clusters:B.filter(x=>x.kind==='cluster').length};
    `);
  }, fixture);
  const counts = await ref.evaluate(() => (window as any).__refCounts);
  expect(counts.history).toBe(fixture.derived.history.length);
  expect(counts.nodes).toBe(fixture.derived.mapNodes.length - 1 + fixture.derived.prGroups.cluster.length + fixture.derived.issueGroups.cluster.length + fixture.derived.failedCI.length);
  expect(counts.clusters).toBe(Number(!!fixture.derived.prGroups.clusterLabel) + Number(!!fixture.derived.issueGroups.clusterLabel));
  writeFileSync(resolve(out, "fixture-manifest.json"), JSON.stringify({
    fixture: "round8Project()", viewport: "1440x900", projectId: fixture.snapshot.hive.id,
    mapNodes: fixture.derived.mapNodes.length, history: fixture.derived.history.length,
    prCluster: fixture.derived.prGroups.cluster.length, issueCluster: fixture.derived.issueGroups.cluster.length,
    failedCI: fixture.derived.failedCI.length, referenceCounts: counts,
    referenceAdapter: "Clears the design's built-in systems and bodies; rebuilds from the same ProjectSnapshot and deriveProject result, including history, clusters, decisions and CI.",
  }, null, 2));
  for (const [theme, style] of variants) {
    await page.evaluate(({ theme, style }) => { const v = (window as any).__visual; v.themes.themeStore.setTheme(theme); v.themes.themeStore.setStyle(style); v.ui.selectProject(); }, { theme, style });
    await ref.evaluate(({ theme, style }) => { const w = window as any; w.eval(`setTheme('${theme}');applyStyle('${style}');goCosmos();cam.s=cam.ts=.8;cam.x=cam.tx=0;cam.y=cam.ty=0;$('legend').classList.remove('open');$('t-legend').classList.remove('on')`); }, { theme, style });
    await page.waitForTimeout(250);
    await ref.waitForTimeout(250);
    await saveCanvas(page.locator(".stars-canvas"), `actual-${theme}-${style}-all.png`);
    await saveCanvas(ref.locator("#cv"), `reference-${theme}-${style}-all.png`);
    await page.evaluate(() => { const v = (window as any).__visual; v.ui.selectProject(v.snapshot.hive.id); });
    await ref.evaluate(() => { (window as any).eval("goSys(A)"); });
    await page.waitForTimeout(2200);
    await ref.waitForTimeout(2200);
    await ref.evaluate(() => { (window as any).eval("$('legend').classList.remove('open');$('t-legend').classList.remove('on')"); });
    await saveCanvas(page.locator(".stars-canvas"), `actual-${theme}-${style}-project.png`);
    await saveCanvas(ref.locator("#cv"), `reference-${theme}-${style}-project.png`);
    if (style === "classic" && (theme === "bright" || theme === "night")) {
      const clusterId = `${fixture.snapshot.hive.id}:cluster:pr`;
      const cluster: any = await page.locator(".stars-canvas").evaluate((el: any, id) => el.starMapMetrics.positions[id], clusterId);
      await page.mouse.click(cluster.x, cluster.y);
      await expect(page.locator(".stars-note.open")).toBeVisible();
      await ref.evaluate(() => (window as any).eval("note(B.find(b=>b.kind==='cluster'&&b.of==='pr'))"));
      await page.waitForTimeout(400); await ref.waitForTimeout(400);
      await page.screenshot({ path: resolve(out, `actual-${theme}-cluster-dom.png`) });
      await ref.screenshot({ path: resolve(out, `reference-${theme}-cluster-dom.png`) });
      await page.locator(".stars-note.open").getByRole("button", { name: "关闭" }).click();
      await ref.evaluate(() => (window as any).eval("hideNote()"));

      const point: any = await page.locator(".stars-canvas").evaluate((el: any) => Object.entries(el.starMapMetrics.positions).filter(([id, p]: any) => id.startsWith("history:") && p.x > 70 && p.x < 1370 && p.y > 70 && p.y < 830).map(([id, p]) => ({ id: id.slice(8), p }))[0]);
      expect(point).toBeTruthy();
      await page.mouse.click(point.p.x, point.p.y);
      await expect(page.locator(".stars-note.open")).toBeVisible();
      await ref.evaluate((id) => (window as any).eval(`note(A.hist.find(h=>h.id===${JSON.stringify(id)}))`), point.id);
      await page.waitForTimeout(400); await ref.waitForTimeout(400);
      await page.screenshot({ path: resolve(out, `actual-${theme}-history-dom.png`) });
      await ref.screenshot({ path: resolve(out, `reference-${theme}-history-dom.png`) });
      await page.locator(".stars-note.open").getByRole("button", { name: "关闭" }).click();
      await ref.evaluate(() => (window as any).eval("hideNote()"));

      await page.locator(".stars-observer").click();
      await ref.evaluate(() => (window as any).eval("openDecide()"));
      await page.waitForTimeout(450); await ref.waitForTimeout(450);
      await page.screenshot({ path: resolve(out, `actual-${theme}-observer-dom.png`) });
      await ref.screenshot({ path: resolve(out, `reference-${theme}-observer-dom.png`) });
      await page.getByRole("button", { name: "关闭等你决定的事" }).click();
      await ref.evaluate(() => (window as any).eval("$('decide').classList.remove('open')"));
    }
  }
  await ref.close();
});
