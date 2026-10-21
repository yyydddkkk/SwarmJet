import { useEffect, useRef, useState } from "react";
import {
  activeAgents, attentionAgents, catalogueRows, completedLastNight,
  agentSummary, messagePreview, prSummary,
  useDataAPI, weightedTruncate, writeAcknowledged,
  type Agent, type ProjectStore,
  type SetupStatus, type ModelsState, type UsageResponse,
} from "../data";
import type { StarsSlotProps } from "./slots";
import { THEMES, themeStore, useTheme, type ThemeChoice } from "./theme";
import { useStarsUI, type Overlay } from "./ui";
import { command, preference } from "./periphery-command";
import { LGLY, type StarGlyph } from "./star-glyphs";
import CreateProject from "./CreateProject";
import "./periphery.css";

type Entry = { id: string; store: ProjectStore; state: ReturnType<ProjectStore["getSnapshot"]>; s: NonNullable<ReturnType<ProjectStore["getSnapshot"]>["snapshot"]>; derived: NonNullable<ReturnType<ProjectStore["getSnapshot"]>["derived"]> };
type CatalogueRow = ReturnType<typeof catalogueRows>[number] & { project: Entry; open(): void };
const glyph = (kind: StarGlyph) => <svg viewBox="-12 -12 24 24" aria-hidden="true" dangerouslySetInnerHTML={{ __html: LGLY[kind] }} />;
const money = (value?: number) => value === undefined ? "" : `$${value.toFixed(2)}`;

export default function PeripherySlot(props: StarsSlotProps) {
  const { ui, projects, stores } = props;
  const state = useStarsUI(ui);
  const theme = useTheme();
  const api = useDataAPI();
  const input = useRef<HTMLInputElement>(null);
  const listInput = useRef<HTMLInputElement>(null);
  const legendSeen = useRef(preference.read("sj-legend-seen") === "1");
  const legendTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const legendAutoOpen = useRef(false);
  const [text, setText] = useState("");
  const [search, setSearch] = useState("");
  const [scope, setScope] = useState<"active" | "today" | "history">("active");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [create, setCreate] = useState(false);
  const [skip, setSkip] = useState(() => preference.read("sj-brief") === "off");
  const [setup, setSetup] = useState<SetupStatus>();
  const [models, setModels] = useState<ModelsState>();
  const [usage, setUsage] = useState<UsageResponse>();
  const [platformError, setPlatformError] = useState("");
  const [platformAttempt, setPlatformAttempt] = useState(0);
  const entries: Entry[] = [...stores].flatMap(([id, store]) => {
    const current = store.getSnapshot();
    return current.snapshot && current.derived ? [{ id, store, state: current, s: current.snapshot, derived: current.derived }] : [];
  });
  const current = projects.find((p) => p.hive.id === state.hiveId)?.hive;
  const route = command(text, projects, stores, ui);
  const loaded = entries.length === projects.length;
  const selected = entries.find((e) => e.id === state.hiveId);
  const decisions = entries.flatMap((e) => e.derived.decisions.map((d) => ({ e, d })));
  const active = entries.flatMap((e) => activeAgents(e.s.agents).map((a) => ({ e, a })));
  const attention = entries.flatMap((e) => attentionAgents(e.s).map((a) => ({ e, a })));
  const needCount = selected ? selected.derived.decisions.length : decisions.length;
  const activeCount = selected ? activeAgents(selected.s.agents).length : active.length;
  const summary = !loaded ? "正在读取项目状态…" : `${selected ? "" : `${projects.length} 个项目，`}${activeCount} 个 Agent 正在工作${needCount ? `，${needCount} 件事等你决定` : ""}。`;
  const openCreate = () => {
    while (ui.getSnapshot().overlay || ui.getSnapshot().platformPanel || ui.getSnapshot().selection || ui.getSnapshot().projectPanel) ui.close();
    setCreate(true);
  };

  useEffect(() => {
    const listener = () => openCreate();
    window.addEventListener("swarmjet:create-project", listener);
    return () => window.removeEventListener("swarmjet:create-project", listener);
  }, [ui]);
  useEffect(() => {
    const listener = (event: Event) => {
      const detail = (event as CustomEvent<{ scope?: "active" | "history"; query?: string }>).detail;
      if (detail?.scope) setScope(detail.scope);
      if (detail?.query !== undefined) setSearch(detail.query);
      ui.openOverlay("list");
    };
    window.addEventListener("swarmjet:catalogue-open", listener);
    return () => window.removeEventListener("swarmjet:catalogue-open", listener);
  }, [ui]);
  useEffect(() => {
    let active = true;
    const fail = (label: string, reason: unknown) => { if (active) setPlatformError((previous) => [previous, `${label}：${reason instanceof Error ? reason.message : String(reason)}`].filter(Boolean).join("；")); };
    setPlatformError("");
    void api.getSetupStatus().then((v) => { if (active) setSetup(v); }).catch((e) => fail("接入状态读取失败", e));
    void api.listModels().then((v) => { if (active) setModels(v); }).catch((e) => fail("模型状态读取失败", e));
    void api.getUsage({ period: "today" }).then((v) => { if (active) setUsage(v); }).catch((e) => fail("花费读取失败", e));
    return () => { active = false; };
  }, [api, projects.length, state.platformPanel, platformAttempt]);
  useEffect(() => { if (preference.read("sj-brief") !== "off" && !ui.getSnapshot().platformPanel) ui.openOverlay("brief"); }, [ui]);
  const enterStars = () => {
    ui.close();
    if (!state.hiveId) {
      const target = entries.find((entry) => entry.derived.needsAttention) ??
        entries.find((entry) => entry.s.queen.enabled) ?? entries[0];
      if (target || projects[0]) ui.selectProject(target?.id ?? projects[0].hive.id);
    }
    if (legendSeen.current) return;
    legendSeen.current = true;
    preference.write("sj-legend-seen", "1");
    legendTimer.current = setTimeout(() => {
      const live = ui.getSnapshot();
      if (live.overlay || live.platformPanel || live.selection) return;
      legendAutoOpen.current = true;
      ui.openOverlay("legend");
      legendTimer.current = setTimeout(() => {
        legendAutoOpen.current = false;
        if (ui.getSnapshot().overlay === "legend") ui.close();
      }, 7000);
    }, 900);
  };
  useEffect(() => ui.subscribe(() => {
    if (legendAutoOpen.current && ui.getSnapshot().overlay !== "legend") {
      legendAutoOpen.current = false;
      clearTimeout(legendTimer.current);
      legendTimer.current = undefined;
    }
  }), [ui]);
  useEffect(() => () => clearTimeout(legendTimer.current), []);
  useEffect(() => {
    if (state.overlay === "list") requestAnimationFrame(() => listInput.current?.focus());
    else if (document.activeElement === listInput.current) listInput.current?.blur();
  }, [state.overlay]);
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey || e.isComposing) return;
      const key = e.key.toLowerCase();
      if (key === "escape") {
        if (e.target === input.current && !ui.getSnapshot().overlay) return;
        e.preventDefault();
        if (create) setCreate(false);
        else ui.close();
        return;
      }
      if ((e.target as HTMLElement)?.closest("input,textarea,select,[contenteditable]")) return;
      const overlays = { t: "timeline", c: "list", b: "brief", l: "legend" } as const;
      if (key in overlays) { e.preventDefault(); const o = overlays[key as keyof typeof overlays]; ui.getSnapshot().overlay === o ? ui.close() : ui.openOverlay(o); }
      else if (key === "/") { e.preventDefault(); input.current?.focus(); }
      else if (key === "a") { e.preventDefault(); if (!ui.getSnapshot().hiveId && projects[0]) ui.selectProject(projects[0].hive.id); ui.openProject(); }
      else if (key === ",") { e.preventDefault(); ui.openPlatform(); }
      else if (key === "s") themeStore.setStyle(themeStore.getSnapshot().style === "classic" ? "simple" : "classic");
      else if (key === "g") themeStore.setGrid(!themeStore.getSnapshot().render.grid);
      else if (key === "d") themeStore.setTheme(themeStore.getSnapshot().dark ? "bright" : "night");
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [ui, projects, create]);

  const rows: CatalogueRow[] = entries.flatMap((e) =>
    catalogueRows(e.s, scope, search).map((row) => ({
      ...row, project: e, open: () => ui.selectObject(row.selection),
    })),
  );
  const groups = entries.map((e) => ({ e, rows: rows.filter((r) => r.project.id === e.id) })).filter((g) => g.rows.length);
  const lastNight = entries.reduce((sum, e) => sum + completedLastNight(e.s.agents), 0);
  const briefItem = (label: string, status: string, onClick: () => void, key: string, need = false) => <button key={key} className={`periphery-it${need ? " need" : ""}`} onClick={onClick}><span className="periphery-it-label">{weightedTruncate(label, 30)}</span><span className="periphery-it-state">{status}</span></button>;
  const acknowledge = (e: Entry, a: Agent) => {
    writeAcknowledged(e.id, a.id);
    e.store.rederive();
  };
  const onSay = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!text.trim()) return;
    if (route) { route.run(); setText(""); input.current?.blur(); return; }
    if (!current) { setError("先选择一个项目，再对主 Agent 说话。"); return; }
    const sent = text;
    input.current?.blur();
    setBusy(true); setError("");
    try { await api.talkToQueen(current.id, sent); setText((draft) => draft === sent ? "" : draft); }
    catch (reason) { setError(reason instanceof Error ? reason.message : String(reason)); }
    finally { setBusy(false); }
  };
  return <>
    <header className={`stars-mark${state.projectPanel || state.overlay === "list" ? " covered" : ""}`}><button className="stars-brand" aria-label="SwarmJet 平台设置" onClick={() => ui.openPlatform()}>SwarmJet</button><nav className="stars-crumbs" aria-label="位置"><button onClick={() => ui.selectProject()} className={!current ? "on" : ""}>全部项目</button>{current && <><i>/</i><button className="on" onClick={() => ui.selectProject(current.id)}>{current.name}</button></>}</nav></header>
    <button className={`stars-cartouche${state.projectPanel || state.platformPanel || state.overlay === "list" ? " covered" : ""}`} onClick={() => current ? ui.openProject() : ui.openOverlay("list")}><h1>{current?.name ?? "全部项目"}</h1><p>{summary}</p><span className="periphery-sr-only">{current ? "项目面板" : "浏览所有项目"}</span></button>
    <nav className={`stars-tools${state.selection || state.platformPanel || state.overlay === "list" ? " covered" : ""}`} aria-label="工具">{(["brief", "list", "timeline", "legend", "display"] as Overlay[]).map((o, i) => <button key={o} aria-pressed={state.overlay === o} className={state.overlay === o ? "on" : ""} onClick={() => state.overlay === o ? ui.close() : ui.openOverlay(o)}>{["简报", "列表", "时间", "图例", "显示"][i]}</button>)}</nav>
    {(!projects.length || (setup && (!setup.github.configured || !setup.models.available))) && <section className="periphery-onboarding" aria-label="首次使用"><h2>开始你的第一个项目</h2>{!setup ? <p role="status">{platformError || "正在读取接入和模型状态…"}{platformError && <button onClick={() => setPlatformAttempt((n) => n + 1)}>重新读取状态</button>}</p> : <ol>{[[setup.github.configured && setup.github.installations > 0, "接入 GitHub", () => ui.openPlatform("setup")], [setup.models.available && models?.gatewayAvailable === true, "填写模型凭证", () => ui.openPlatform("models")], [projects.length > 0, "创建第一个项目", openCreate]].map(([done, label, run]) => <li key={String(label)} data-complete={!!done}><span aria-hidden="true">{done ? "★" : "☆"}</span><button onClick={run as () => void}>{String(label)}，{done ? "已完成" : "待完成"}</button></li>)}</ol>}</section>}
    <section className={`periphery-brief${state.overlay === "brief" ? " open" : ""}`} aria-label="简报" role="dialog" aria-hidden={state.overlay !== "brief"} inert={state.overlay !== "brief"}><h2>{needCount ? "有事需要你。" : "一切正常，不需要你做什么。"}</h2><p className="sub">{summary}</p><div className="periphery-foot"><button className="go" onClick={enterStars}>进入星图</button><label><input type="checkbox" checked={skip} onChange={(e) => { setSkip(e.target.checked); preference.write("sj-brief", e.target.checked ? "off" : "on"); }} />下次直接进入星图</label></div><div className="periphery-brief-body"><h3>需要你</h3>{decisions.slice(0, 3).map(({ e, d }) => d.kind === "pr" ? briefItem(`PR #${d.pr.number} ${prSummary(d.pr, 30)}`, e.s.hive.name, () => ui.selectObject({ hiveId: e.id, kind: "pr", id: d.id, number: d.pr.number }), `${e.id}:${d.id}`, true) : briefItem(messagePreview(d.message), e.s.hive.name, () => d.message.from === "queen" ? ui.selectObject({ hiveId: e.id, kind: "queen", id: `${e.id}:queen` }) : ui.selectObject({ hiveId: e.id, kind: "agent", id: d.message.from.replace(/^forager:/, "") }), `${e.id}:${d.id}`, true))}{decisions.length > 3 && briefItem(`还有 ${decisions.length - 3} 个`, "在列表里查看", () => { setScope("active"); setSearch(""); ui.openOverlay("list"); }, "more-decisions")}{!decisions.length && <p className="periphery-muted">暂时没有等你决定的事。</p>}{!loaded && <p role="status">正在读取项目…</p>}{entries.some((e) => e.state.resources.prs.status === "loading") && <p role="status">正在向 GitHub 读取 PR…</p>}<h3>正在进行</h3>{active.slice(0, 3).map(({ e, a }) => briefItem(agentSummary(a, { prs: e.s.prs, issues: e.s.signals }, 30), e.s.hive.name, () => ui.selectObject({ hiveId: e.id, kind: "agent", id: a.id }), a.id))}{active.length > 3 && briefItem(`还有 ${active.length - 3} 个`, "在列表里查看", () => { setScope("active"); setSearch(""); ui.openOverlay("list"); }, "more-active")}{!active.length && <p className="periphery-muted">当前没有工作中的 Agent。</p>}<h3>需要留意</h3>{attention.map(({ e, a }) => <div className="periphery-attention" key={a.id}>{briefItem(agentSummary(a, { prs: e.s.prs, issues: e.s.signals }, 30), a.state === "failed" ? "失败" : "仍占名额", () => ui.selectObject({ hiveId: e.id, kind: "agent", id: a.id }), `attention:${a.id}`)}<button className="ack" onClick={() => acknowledge(e, a)}>知道了</button></div>)}{setup && (!setup.models.available || models?.gatewayAvailable === false) && briefItem(models?.gatewayAvailable === false ? "模型服务无法连接，请检查服务。" : setup.models.configured ? "凭证已保存，目前无可用凭证。" : "尚无可用模型凭证。", "平台设置", () => ui.openPlatform("models"), "models")}{platformError && <p role="alert">无法读取平台状态：{platformError} <button onClick={() => setPlatformAttempt((n) => n + 1)}>重新读取状态</button></p>}{!attention.length && !platformError && <p className="periphery-muted">{setup && loaded ? "已读取最新平台与项目状态。" : "正在检查平台与项目状态…"}</p>}<h3>昨晚完成</h3><p className="periphery-muted">{lastNight} 个 Agent 完成（按更新时间近似）；昨晚合并数和花费暂时无法获取。</p>{usage && <p className="periphery-muted">今天花费 {money(usage.totals.costUsd)}</p>}</div></section>
    <section className={`periphery-list${state.overlay === "list" ? " open" : ""}`} role="dialog" aria-label="列表" aria-hidden={state.overlay !== "list"} inert={state.overlay !== "list"}><button className="periphery-sheet-x" aria-label="关闭" onClick={() => ui.close()}>×</button><div className="inner"><h2>列表</h2><input ref={listInput} aria-label="查找项目、Agent 或 PR" placeholder="查找项目、Agent 或 PR" autoComplete="off" value={search} onChange={(e) => setSearch(e.target.value)} /><div className="periphery-scope" aria-label="列表筛选">{(["active", "today", "history"] as const).map((value, i) => <button key={value} className={scope === value ? "on" : ""} aria-pressed={scope === value} onClick={() => setScope(value)}>{["活跃", "今天", "历史"][i]}</button>)}</div><table><colgroup><col className="col-glyph" /><col className="col-summary" /><col className="col-status" /><col className="col-cost" /></colgroup><tbody>{groups.map(({ e, rows: projectRows }) => <FragmentRows key={e.id} e={e} rows={projectRows} glyph={glyph} />)}{!groups.length && <tr><td className="s" colSpan={4}>{loaded ? "没有找到。" : "正在读取项目列表…"}</td></tr>}</tbody></table></div></section>
    <section className={`periphery-pop periphery-legend${state.overlay === "legend" ? " open" : ""}`} role="dialog" aria-label="图例" aria-hidden={state.overlay !== "legend"} inert={state.overlay !== "legend"}><h2>图例</h2>{([ ["star", "主 Agent，每个项目中间的那颗"], ["hist", "做完的事，散在最外层；越亮越新"], ["cluster", "放不下的 PR 或 Issue，点开看全部"], ["busy", "工作中的 Agent，尾巴越长越忙"], ["parked", "空闲的 Agent"], ["failed", "失败的 Agent"], ["pr", "PR"], ["ready", "等你决定的 PR"], ["moon", "正在验证 PR 的 verifier"], ["signal", "GitHub 上的 Issue"], ["ci", "未通过的 CI"], ["queue", "排队等待的任务"], ["you", "你"] ] as [StarGlyph, string][]).map(([kind, label]) => <div className="k" key={kind}>{glyph(kind)}<span>{label}</span></div>)}</section>
    <section className={`periphery-pop periphery-display${state.overlay === "display" ? " open" : ""}`} role="dialog" aria-label="显示" aria-hidden={state.overlay !== "display"} inert={state.overlay !== "display"}><div className="opt">风格<span>{(["classic", "simple"] as const).map((value, i) => <button key={value} className={theme.style === value ? "on" : ""} aria-pressed={theme.style === value} onClick={() => themeStore.setStyle(value)}>{["经典", "简洁"][i]}</button>)}</span></div><div className="opt">主题<span>{Object.entries(THEMES).map(([id, t]) => <button key={id} className={theme.choice === id ? "on" : ""} aria-pressed={theme.choice === id} onClick={() => themeStore.setTheme(id as ThemeChoice)}>{t.label}</button>)}<button className={theme.choice === "system" ? "on" : ""} aria-pressed={theme.choice === "system"} onClick={() => themeStore.setTheme("system")}>跟随系统</button></span></div><div className="opt">经纬线<span><button className={theme.render.grid ? "on" : ""} aria-pressed={theme.render.grid} onClick={() => themeStore.setGrid(true)}>显示</button><button className={!theme.render.grid ? "on" : ""} aria-pressed={!theme.render.grid} onClick={() => themeStore.setGrid(false)}>隐藏</button></span></div></section>
    <form className={`stars-say${state.overlay === "brief" || state.overlay === "timeline" ? " suppressed" : ""}`} onSubmit={onSay}><input ref={input} aria-label="对话与命令" aria-describedby="periphery-say-hint" placeholder="对主 Agent 说：新任务、问题都可以" autoComplete="off" value={text} onChange={(e) => setText(e.target.value)} /><button className="stars-say-submit" disabled={!text.trim() || (busy && !route)} aria-label={route ? "前往" : "发送"}>{route ? "前往" : "发送"}</button></form><div id="periphery-say-hint" className={`stars-say-hint${text ? " on" : ""}`} role="status">{text ? `回车${route ? route.label : current ? "发给主 Agent" : "前请先选择项目"}` : ""}</div>
    {error && <p className="periphery-toast" role="alert">{error}</p>}
    {create && <CreateProject ui={ui} reloadProjects={props.reloadProjects} onClose={() => setCreate(false)} />}
  </>;
}

function FragmentRows({ e, rows, glyph }: { e: Entry; rows: CatalogueRow[]; glyph: (kind: StarGlyph) => React.ReactNode }) {
  return <><tr className="grp"><td colSpan={4}>{e.s.hive.name}<small>{e.s.queen.enabled ? "运行中" : "已停用"}</small></td></tr>{rows.map((row) => <tr className="item" key={row.id} tabIndex={0} onClick={row.open} onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); row.open(); } }} aria-label={`${row.summary}，${row.status}`}><td className="g">{glyph(row.glyph)}</td><td>{row.summary}</td><td className="s">{row.status}</td><td className="c">{money(row.cost)}</td></tr>)}</>;
}
