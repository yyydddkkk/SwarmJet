// Disposable design study: can an intervention-first workbench make parallel AI tasks easier to follow?
// Sample data only. All actions are in memory; no API calls or persisted changes.
import { useEffect, useRef, useState, type ReactNode } from "react";
import "./workbench-prototype.css";

type State = "approval" | "blocked" | "developing" | "reviewing" | "verifying" | "completed" | "paused";
type DemoTask = { id: number; title: string; project: string; repo: string; state: State; message: string; updated: string; files: number; checks: number; resumeState?: State };
const initial: DemoTask[] = [
  { id: 142, title: "为工作区增加成员权限校验", project: "平台工程", repo: "api / web", state: "approval", message: "开发与独立审查已完成，等待批准合并。", updated: "2 分钟前", files: 6, checks: 12 },
  { id: 138, title: "修复订单重试时的重复扣款", project: "交易服务", repo: "payment", state: "blocked", message: "集成测试无法连接支付沙箱，需要检查环境配置。", updated: "8 分钟前", files: 3, checks: 7 },
  { id: 145, title: "支持按项目筛选执行记录", project: "平台工程", repo: "api / web", state: "developing", message: "正在实现筛选接口，并补充前端交互。", updated: "刚刚", files: 4, checks: 0 },
  { id: 143, title: "优化仓库初始化的错误提示", project: "平台工程", repo: "worker", state: "reviewing", message: "独立审查 Agent 正在检查错误处理与测试覆盖。", updated: "1 分钟前", files: 5, checks: 9 },
  { id: 136, title: "增加订单导出的日期范围校验", project: "交易服务", repo: "orders / web", state: "verifying", message: "正在执行集成测试，已完成基础校验。", updated: "3 分钟前", files: 2, checks: 4 },
  { id: 139, title: "统一模型选择器的空状态", project: "平台工程", repo: "web", state: "completed", message: "合并请求已合并，Issue 已关闭。", updated: "24 分钟前", files: 3, checks: 8 },
  { id: 132, title: "补充退款回调的幂等性测试", project: "交易服务", repo: "payment", state: "completed", message: "验证通过，变更已合并到 main。", updated: "46 分钟前", files: 2, checks: 11 },
];
const labels: Record<State, string> = { approval: "等待批准", blocked: "需要处理", developing: "开发中", reviewing: "审查中", verifying: "验证中", completed: "已完成", paused: "已暂停" };
const needsAttention = (s: State) => ["approval", "blocked", "paused"].includes(s);
const phase: Record<State, number> = { approval: 4, blocked: 2, developing: 1, reviewing: 3, verifying: 2, completed: 5, paused: 1 };
function Icon({ name, size = 18 }: { name: string; size?: number }) {
  const paths: Record<string, string> = {
    grid: "M3 3h7v7H3zM14 3h7v7h-7zM3 14h7v7H3zM14 14h7v7h-7z",
    folder: "M3 7V5h6l3 3h9v12H3V7Z", arrow: "M5 12h14m-5-5 5 5-5 5",
    search: "M21 21l-5-5M18 10a8 8 0 1 1-16 0 8 8 0 0 1 16 0",
    plus: "M12 5v14M5 12h14", check: "m5 12 4 4L19 6", clock: "M12 8v5l3 2M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0",
    branch: "M6 3v12a4 4 0 0 0 4 4h8M6 8h7a4 4 0 0 0 4-4V3", terminal: "m4 6 6 6-6 6M13 18h7",
    chevron: "m9 5 7 7-7 7", back: "M19 12H5m5-5-5 5 5 5", bolt: "m13 2-9 12h7l-1 8 10-13h-7l1-7Z",
    alert: "M12 8v5m0 3v.1M12 3 2 21h20L12 3Z", close: "m6 6 12 12M6 18 18 6",
  };
  return <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.65" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d={paths[name] || paths.grid} /></svg>;
}
function Badge({ state }: { state: State }) { return <span className={`wb-badge wb-${state}`}><i />{labels[state]}</span>; }
function Dialog({ title, close, children }: { title: string; close: () => void; children: ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => { ref.current?.showModal(); }, []);
  return <dialog className="wb-dialog" ref={ref} onCancel={close}><div className="wb-dialog-head"><h2>{title}</h2><button aria-label="关闭" onClick={close}><Icon name="close" /></button></div>{children}</dialog>;
}
export default function WorkbenchPrototype() {
  const [tasks, setTasks] = useState(initial);
  const [route, setRoute] = useState(location.hash || "#/tasks");
  const [filter, setFilter] = useState("all");
  const [project, setProject] = useState("全部项目");
  const [query, setQuery] = useState("");
  const [detailTab, setDetailTab] = useState("overview");
  const [dialog, setDialog] = useState("");
  const [notice, setNotice] = useState("");
  const [notes, setNotes] = useState<Record<number, string[]>>({});
  useEffect(() => { const change = () => { setRoute(location.hash); setNotice(""); setDetailTab("overview"); window.scrollTo(0, 0); }; window.addEventListener("hashchange", change); return () => window.removeEventListener("hashchange", change); }, []);
  const id = Number(route.split("/")[2]);
  const task = tasks.find(t => t.id === id);
  const scoped = tasks.filter(t => project === "全部项目" || t.project === project);
  const attention = scoped.filter(t => needsAttention(t.state));
  const active = scoped.filter(t => !needsAttention(t.state) && t.state !== "completed");
  const completed = scoped.filter(t => t.state === "completed");
  const rows = scoped.filter(t => (filter === "all" || (filter === "attention" ? needsAttention(t.state) : filter === "active" ? !needsAttention(t.state) && t.state !== "completed" : t.state === "completed")) && `${t.title} ${t.project} ${t.id}`.includes(query.trim()));
  function changeState(state: State, message: string) { if (!task) return; setTasks(ts => ts.map(t => t.id === task.id ? { ...t, state, message, resumeState: state === "paused" ? t.state : t.resumeState, updated: "刚刚" } : t)); setNotice(`预览已更新：${message}`); setDialog(""); }
  function chooseProject(name: string) { setProject(name); location.hash = "/tasks"; }
  return <div className="workbench-prototype">
    <a className="wb-skip" href="#wb-main" onClick={e => { e.preventDefault(); document.getElementById("wb-main")?.focus(); }}>跳至主要内容</a>
    <aside className="wb-sidebar">
      <a className="wb-brand" href="#/tasks"><span className="wb-symbol"><Icon name="bolt" size={21} /></span>SwarmJet</a>
      <div className="wb-workspace"><span className="wb-workspace-icon">S</span><div><strong>开发工作空间</strong><small>自主开发 · 跨仓库协作</small></div></div>
      <nav aria-label="主导航"><a href="#/tasks" className="wb-nav-active" onClick={() => setProject("全部项目")}><Icon name="grid" />任务中心<span>{tasks.filter(t => needsAttention(t.state)).length}</span></a></nav>
      <div className="wb-nav-section">项目</div>
      <nav aria-label="按项目查看任务">{["平台工程", "交易服务"].map((p, i) => <button key={p} className={project === p ? "wb-project-selected" : ""} onClick={() => chooseProject(p)}><span className={`wb-project-dot wb-dot-${i}`} />{p}<span>{tasks.filter(t => t.project === p && t.state !== "completed").length}</span></button>)}</nav>
      <div className="wb-sidebar-bottom"><div className="wb-preview-note"><Icon name="terminal" /><span>设计预览<small>示例数据 · 操作不会提交</small></span></div><div className="wb-profile"><span>Y</span><div><strong>开发者</strong><small>工作空间管理员</small></div></div></div>
    </aside>
    <div className="wb-body"><header className="wb-topbar"><div>工作空间<Icon name="chevron" size={12} /><a href="#/tasks">任务中心</a>{task && <><Icon name="chevron" size={12} /><span>#{task.id}</span></>}</div><span className="wb-mode">交互原型 · 第一版</span></header>
    <main id="wb-main" tabIndex={-1}>
      {notice && <div className="wb-notice" role="status"><Icon name="check" />{notice}<button aria-label="关闭提示" onClick={() => setNotice("")}><Icon name="close" size={14} /></button></div>}
      {!task ? <>
        <div className="wb-page-head"><div><h1>任务中心<span className="wb-heading-dot" /></h1><p>掌握进展，把注意力留给需要你的地方。</p></div><button className="wb-primary" onClick={() => setDialog("create")}><Icon name="plus" size={16} />创建任务</button></div>
        <div className="wb-overview"><div><span className="wb-live-dot" /><strong>{active.length}</strong> 项任务正在推进</div><span className="wb-overview-separator" /><div><Icon name="check" size={15} /><strong>{completed.length}</strong> 项已完成</div><span className="wb-overview-scope">{project} · {scoped.length} 项任务</span></div>
        {attention.length > 0 && <section className="wb-attention" aria-labelledby="attention-title"><div className="wb-section-title"><h2 id="attention-title"><span className="wb-attention-dot" />需要你处理 <span>{attention.length}</span></h2><span>下一步由你决定</span></div><div className="wb-attention-items">{attention.map(t => <a key={t.id} href={`#/tasks/${t.id}`} className="wb-attention-item"><span className={`wb-action-icon wb-${t.state}`}><Icon name={t.state === "approval" ? "branch" : "alert"} size={20} /></span><div><div className="wb-attention-meta">{t.project}<span>#{t.id}</span><Badge state={t.state} /></div><h3>{t.title}</h3><p>{t.message}</p></div><span className="wb-attention-link">{t.state === "approval" ? "审阅变更" : "查看原因"}<Icon name="arrow" size={16} /></span></a>)}</div></section>}
        <section className="wb-task-section" aria-label="任务列表"><div className="wb-list-tools"><div className="wb-tabs" aria-label="任务视图">{[["all", "全部任务", scoped.length], ["active", "进行中", active.length], ["attention", "需要处理", attention.length], ["done", "已结束", completed.length]].map(([v, label, count]) => <button key={v} aria-pressed={filter === v} className={filter === v ? "wb-tab-active" : ""} onClick={() => setFilter(String(v))}>{label}<span>{count}</span></button>)}</div><div className="wb-search"><Icon name="search" size={16} /><input aria-label="搜索任务" placeholder="搜索任务或编号" value={query} onChange={e => setQuery(e.target.value)} /></div></div>
        <div className="wb-table-controls"><label><Icon name="folder" size={14} /><select aria-label="筛选项目" value={project} onChange={e => setProject(e.target.value)}>{["全部项目", "平台工程", "交易服务"].map(p => <option key={p}>{p}</option>)}</select></label><span>最近进展与交付状态</span></div>
        <div className="wb-table-scroll"><table className="wb-table"><thead><tr><th>任务</th><th>状态</th><th>项目 / 仓库</th><th>最近更新</th><th><span className="wb-sr-only">查看任务</span></th></tr></thead><tbody>{rows.map(t => <tr key={t.id}><td><div className="wb-task-name"><span className="wb-issue">#{t.id}</span><a href={`#/tasks/${t.id}`}>{t.title}</a></div><p className="wb-task-message">{t.message}</p></td><td><Badge state={t.state} />{!needsAttention(t.state) && t.state !== "completed" && <div className="wb-mini-progress" aria-label={`${labels[t.state]}，执行尚未完成`}>{[0,1,2,3,4].map(n => <i key={n} className={n <= phase[t.state] ? "wb-progress-filled" : ""} />)}</div>}</td><td><span>{t.project}</span><small>{t.repo}</small></td><td className="wb-updated">{t.updated}</td><td><a href={`#/tasks/${t.id}`} aria-label={`查看 ${t.title}`}><Icon name="chevron" size={15} /></a></td></tr>)}</tbody></table></div>
        {!rows.length && <div className="wb-empty"><Icon name="search" size={24} /><h3>没有找到符合条件的任务</h3><button onClick={() => { setFilter("all"); setProject("全部项目"); setQuery(""); }}>清除筛选</button></div>}
        <div className="wb-table-foot"><span>{rows.length} 项任务</span><span>示例快照，不连接真实任务</span></div></section>
      </> : <>
        <a className="wb-back" href="#/tasks"><Icon name="back" size={15} />所有任务</a>
        <div className="wb-detail-head"><div className="wb-detail-meta"><span>{task.project}</span><span>/</span><span>Issue #{task.id}</span><Badge state={task.state} /></div><h1>{task.title}</h1><p>{task.repo} <span>创建于 9 月 22 日 10:24</span><span>返工 0 次</span></p></div>
        <div className="wb-detail-layout"><div className="wb-detail-main">
          <section className="wb-progress-card"><div className="wb-section-title"><h2>执行进展</h2><span>{task.updated}更新</span></div><ol className="wb-pipeline">{["准备", "开发", "验证", "审查", "合并", "完成"].map((p, i) => <li key={p} className={i < phase[task.state] ? "wb-step-done" : i === phase[task.state] ? "wb-step-current" : ""}><span>{i < phase[task.state] ? <Icon name="check" size={13} /> : <i />}</span>{p}</li>)}</ol><div className="wb-progress-message"><span className={`wb-state-dot wb-${task.state}`} /><p>{task.message}</p></div></section>
          {needsAttention(task.state) && <section className={`wb-decision wb-decision-${task.state}`}><span className="wb-decision-icon"><Icon name={task.state === "approval" ? "branch" : "alert"} size={23} /></span><div><h2>{task.state === "approval" ? "变更已就绪，等待你的批准" : task.state === "blocked" ? "任务遇到阻塞，需要检查运行环境" : "任务已暂停，工作现场已保留"}</h2><p>{task.state === "approval" ? `${task.files} 个文件变更，${task.checks} 项检查通过。批准后将按当前审查版本进入合并。` : task.message}</p><div className="wb-decision-buttons">{task.state === "approval" ? <><button className="wb-primary" onClick={() => setDialog("approve")}><Icon name="check" size={15} />批准此版本</button><button onClick={() => setDialog("reject")}>退回修改</button></> : <button onClick={() => task.state === "paused" ? changeState(task.resumeState || "developing", "任务已恢复到暂停前的阶段。") : setDialog("logs")}>{task.state === "paused" ? "继续任务" : "查看失败日志"}</button>}<span>{task.state === "approval" ? "批准绑定当前提交版本" : "解决问题后可重试"}</span></div></div></section>}
          <div className="wb-detail-tabs wb-tabs" aria-label="任务详情视图">{[["overview", "交付结果"], ["execution", "执行记录"], ["context", "需求与说明"]].map(([v, label]) => <button aria-pressed={detailTab === v} className={detailTab === v ? "wb-tab-active" : ""} onClick={() => setDetailTab(v)} key={v}>{label}</button>)}</div>
          {detailTab === "overview" && <><section className="wb-detail-section"><div className="wb-section-title"><h2>代码变更</h2><span>{task.files} 个文件</span></div>{(task.files ? task.repo.split(" / ") : []).map((repo, i) => <article className="wb-repository" key={repo}><div className="wb-repo-name"><span className="wb-repo-icon"><Icon name="branch" /></span><div><h3>{repo}</h3><p><code>swarmjet/issue-{task.id}</code><Icon name="arrow" size={12} /><code>main</code></p></div><span className="wb-mr">{task.state === "completed" ? "已合并" : "合并请求"} !{83 + i}</span></div><details><summary>查看变更摘要</summary><p>更新 {repo} 中的业务实现，补充边界条件测试与错误处理。</p><code>7a81c92 → b4e29d1</code></details></article>)}</section><section className="wb-detail-section"><div className="wb-section-title"><h2>验证结果</h2><span>{task.checks} 项已通过</span></div>{(task.checks ? ["类型检查与静态分析", "业务单元测试", "跨服务集成测试"] : []).map((name, i) => <div className="wb-check-row" key={name}><Icon name={task.state === "blocked" && i === 2 ? "alert" : "check"} size={16} /><span>{name}</span><small>必需</small><strong className={task.state === "blocked" && i === 2 ? "wb-failed" : ""}>{task.state === "blocked" && i === 2 ? "未通过" : "通过"}</strong></div>)}{!task.checks && <p className="wb-muted">开发完成后将在此展示验证结果。</p>}</section></>}
          {detailTab === "execution" && <section className="wb-detail-section"><div className="wb-section-title"><h2>Agent 执行记录</h2><button onClick={() => setDialog("logs")}><Icon name="terminal" size={15} />查看日志</button></div><div className="wb-agent"><span className="wb-agent-icon"><Icon name="terminal" size={22} /></span><div><h3>开发 Agent</h3><p>分析相关模块，完成实现并补充测试。</p><small>OpenCode · 独立开发工作区</small></div></div><div className="wb-agent"><span className="wb-agent-icon"><Icon name="check" size={22} /></span><div><h3>独立审查 Agent</h3><p>{phase[task.state] >= 4 ? "已检查改动范围与测试结果，没有待处理的审查意见。" : "开发和验证完成后，检查整组仓库的变更。"}</p><small>OpenCode · 独立审查会话</small></div></div></section>}
          {detailTab === "context" && <section className="wb-detail-section"><div className="wb-section-title"><h2>需求与补充说明</h2></div><p>在现有实现上完成「{task.title}」，保留已有接口行为，并为边界场景补充测试。</p>{(notes[task.id] || []).map((n, i) => <div className="wb-note" key={i}>{n}</div>)}<form onSubmit={e => { e.preventDefault(); const form = e.currentTarget; const value = String(new FormData(form).get("note")).trim(); if (!value) return; setNotes(n => ({ ...n, [task.id]: [...(n[task.id] || []), value] })); form.reset(); setNotice("说明已添加到当前预览，未发送给 Agent。"); }}><label className="wb-field">补充说明<textarea name="note" required rows={4} placeholder="补充约束、验收要求或需要注意的边界…" /></label><button className="wb-primary">添加说明</button></form></section>}
        </div><aside className="wb-detail-aside"><section><h2>任务操作</h2><p>控制任务，保留当前工作现场。</p><div className="wb-side-actions"><button disabled={task.state === "completed"} onClick={() => changeState(task.state === "paused" ? task.resumeState || "developing" : "paused", task.state === "paused" ? "任务已恢复到暂停前的阶段。" : "已暂停执行，工作现场已保留。")}>{task.state === "paused" ? "继续任务" : "暂停任务"}</button><button onClick={() => setDialog("logs")}><Icon name="terminal" size={14} />查看日志</button></div></section><section><h2>任务信息</h2><dl><dt>来源</dt><dd>GitLab Issue #{task.id}</dd><dt>目标分支</dt><dd><Icon name="branch" size={13} />main</dd><dt>合并策略</dt><dd>审查后人工批准</dd><dt>前置依赖</dt><dd>无</dd><dt>项目版本</dt><dd>v3</dd></dl></section><section><h2>最近活动</h2><ol className="wb-activity"><li><time>现在</time><strong>{labels[task.state]}</strong><p>{task.message}</p></li>{phase[task.state] >= 3 && <li><time>10:48</time><strong>验证完成</strong><p>必需检查全部通过。</p></li>}<li><time>10:24</time><strong>任务已创建</strong><p>已固定仓库基线与项目配置。</p></li></ol></section></aside></div>
      </>}
    </main><footer className="wb-footer"><span>SwarmJet</span><span>从 Issue 到交付，保持掌控。</span></footer></div>
    <div className="wb-prototype-bar"><span className="wb-prototype-dot" />设计预览<span className="wb-bar-divider" />工作台方向<button onClick={() => { setTasks(initial); setNotes({}); setFilter("all"); setProject("全部项目"); setQuery(""); setNotice("示例状态已重置。"); location.hash = "/tasks"; }}>重置示例</button></div>
    {dialog && <Dialog title={dialog === "logs" ? "运行日志" : dialog === "approve" ? "批准当前审查版本" : dialog === "reject" ? "退回修改" : "创建任务"} close={() => setDialog("")}>
      <p className="wb-dialog-note">交互演示：所有操作只更新本页示例。</p>
      {dialog === "logs" ? <pre className="wb-log">{task?.state === "blocked" ? "[10:42:08] Starting integration checks\n[10:42:10] Connecting to payment sandbox…\n[10:42:40] Connection timed out\n[10:42:40] Required check failed: payment-integration\n\n请检查支付沙箱地址与网络配置。" : `[10:24:01] Workspace prepared\n[10:26:12] Development agent started\n${task && phase[task.state] >= 3 ? "[10:48:20] Required checks passed\n" : ""}[当前状态] ${task ? labels[task.state] : "就绪"}\n${task?.message || ""}`}</pre>
      : dialog === "approve" ? <><p>批准「{task?.title}」的当前变更。代码变化后需要重新审查。</p><div className="wb-note"><code>审查版本：b4e29d1</code></div><button className="wb-primary" onClick={() => changeState("completed", "示例变更已合并，任务已完成。")}>模拟批准并完成</button></>
      : dialog === "reject" ? <form onSubmit={e => { e.preventDefault(); changeState("developing", `根据修改意见返工：${new FormData(e.currentTarget).get("reason")}`); }}><label className="wb-field">修改要求<textarea required name="reason" rows={4} placeholder="说明需要调整的内容…" /></label><button className="wb-primary">提交修改要求</button></form>
      : <form onSubmit={e => { e.preventDefault(); const form = new FormData(e.currentTarget); const newID = Math.max(...tasks.map(t => t.id)) + 1; setTasks(ts => [{ id: newID, title: String(form.get("title")), project: String(form.get("project")), repo: "api / web", state: "developing", message: "示例任务已创建，正在准备开发。", updated: "刚刚", files: 0, checks: 0 }, ...ts]); setDialog(""); location.hash = `/tasks/${newID}`; }}><label className="wb-field">项目<select name="project"><option>平台工程</option><option>交易服务</option></select></label><label className="wb-field">示例任务标题<input name="title" required placeholder="例如：支持按项目导出执行记录" /></label><button className="wb-primary">创建示例任务</button></form>}
    </Dialog>}
  </div>;
}
