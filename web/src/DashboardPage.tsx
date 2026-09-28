import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";
import { Icon } from "./ConsoleIcon";
import type { NodeView } from "./NodeSettings";
import { nextAction } from "./task-presentation";
import { date, Status } from "./TaskStatus";
import type { Task, TaskPage } from "./types";
import { ErrorBox } from "./ui";
import "./dashboard.css";

type Operations = {
  inboxPending: number;
  inboxRetrying: number;
  inboxOldestWaitSeconds: number;
  inboxProcessedLastHour: number;
  admissionWaiting: number;
  runnerSlotsReserved: number;
  measuredAt: string;
};
type Check = { id: string; status: "ok" | "warning" | "error" | "unavailable"; message: string };
type CheckResult = { checks: Check[]; checkedAt: string };

const checkNames: Record<string, string> = { registry: "镜像仓库", mihomo: "代理管理连接", nodes: "Node 心跳" };
const checkStates: Record<Check["status"], string> = { ok: "正常", warning: "需检查", error: "连接失败", unavailable: "未配置" };
const taskLink = (task: Task) => `#/tasks/${encodeURIComponent(task.id)}`;
const workbenchLink = (path: string, projectId: string) => `#${path}${projectId ? `${path.includes("?") ? "&" : "?"}project=${encodeURIComponent(projectId)}` : ""}`;
const taskSummary = (task: Task) => task.message || nextAction(task) || "查看任务进展与执行记录。";
const query = (view: string, projectId = "", limit = 4) => {
  const params = new URLSearchParams({ view, limit: String(limit) });
  if (projectId) params.set("project", projectId);
  return `/tasks?${params}`;
};

export function WorkbenchOverview({ projectId }: { projectId: string }) {
  const [attention, setAttention] = useState<TaskPage | null>(null);
  const [active, setActive] = useState<TaskPage | null>(null);
  const [waiting, setWaiting] = useState<TaskPage | null>(null);
  const [operations, setOperations] = useState<Operations | null>(null);
  const [nodes, setNodes] = useState<NodeView[] | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const generation = useRef(0);
  const load = useCallback(async () => {
    const request = ++generation.current;
    const results = await Promise.allSettled([
      api<TaskPage>(query("attention", projectId)),
      api<TaskPage>(query("active", projectId, 3)),
      api<TaskPage>(query("waiting", projectId, 2)),
      api<Operations>("/operations"),
      api<NodeView[]>("/nodes"),
    ]);
    if (request !== generation.current) return;
    if (results[0].status === "fulfilled") setAttention(results[0].value);
    if (results[1].status === "fulfilled") setActive(results[1].value);
    if (results[2].status === "fulfilled") setWaiting(results[2].value);
    if (results[3].status === "fulfilled") setOperations(results[3].value);
    if (results[4].status === "fulfilled") setNodes(results[4].value || []);
    const failed = results.flatMap((result, index) => result.status === "rejected" ? [["待处理任务", "运行中任务", "排队任务", "接入统计", "Node 状态"][index]] : []);
    setError(failed.length ? `无法读取${failed.join("、")}，请重试。` : "");
    setLoading(false);
  }, [projectId]);
  useEffect(() => {
    setAttention(null); setActive(null); setWaiting(null); setLoading(true);
    void load();
    const timer = window.setInterval(() => { if (!document.hidden) void load(); }, 15000);
    return () => { window.clearInterval(timer); generation.current++; };
  }, [load]);

  const attentionCount = attention?.counts?.attention;
  const activeCount = active?.counts?.active;
  const waitingCount = waiting?.counts?.waiting;
  const completedCount = attention?.counts?.completed;
  const flow = [...(active?.items || []), ...(waiting?.items || [])].slice(0, 4);
  const offline = nodes?.filter(item => !item.online).length || 0;
  return <div className="dashboard-page">
    <div className="page-head dashboard-head"><div><h1>今天先处理这些</h1><p>需要决策的任务排在前面，平台状态随时可见。</p></div><a className="button primary" href={workbenchLink("/issues", projectId)}>创建 Issue</a></div>
    <ErrorBox error={error} retry={() => void load()} />
    <div className="dashboard-layout"><div className="dashboard-primary">
      <section className="dashboard-section" aria-labelledby="dashboard-attention"><div className="dashboard-section-head"><div><span className="dashboard-kicker">待我处理</span><h2 id="dashboard-attention">{attentionCount === undefined ? "需要决策的任务" : `${attentionCount} 项需要决策`}</h2></div><small>按最近更新排序</small></div>
        <div className="dashboard-decision-list">{attention?.items?.length ? attention.items.map(task => <a className="dashboard-decision" key={task.id} href={taskLink(task)}><span className={`dashboard-decision-icon ${task.stage === "blocked" ? "is-blocked" : ""}`}><Icon name={task.stage === "blocked" ? "alert" : "branch"} /></span><span className="dashboard-decision-copy"><small>{task.project.name} · Issue #{task.issue.iid} · {date(task.updatedAt)}</small><strong>{task.issue.title}</strong><span>{taskSummary(task)}</span></span><span className="dashboard-decision-link">{task.stage === "blocked" ? "查看阻塞" : task.stage === "waiting_approval" ? "审阅变更" : "查看任务"}<Icon name="arrow" /></span></a>) : <div className="dashboard-empty">{loading ? "正在读取待处理任务…" : attention ? "当前没有需要你处理的任务。" : "待处理任务暂不可用。"}</div>}</div>
        {attentionCount !== undefined && attentionCount > (attention?.items?.length || 0) && <a className="dashboard-more" href={workbenchLink("/tasks?view=attention", projectId)}>查看全部 {attentionCount} 项</a>}
      </section>
      <section className="dashboard-section" aria-labelledby="dashboard-flow"><div className="dashboard-section-head"><div><span className="dashboard-kicker">执行中</span><h2 id="dashboard-flow">任务流转</h2></div><a href={workbenchLink("/tasks", projectId)}>查看全部任务 <Icon name="arrow" /></a></div><div className="dashboard-flow-box"><div className="dashboard-flow-counts"><div><strong>{activeCount ?? "—"}</strong><span>运行中</span></div><div><strong>{waitingCount ?? "—"}</strong><span>等待准入</span></div><div><strong>{completedCount ?? "—"}</strong><span>累计完成</span></div></div><div className="dashboard-flow-rows">{flow.length ? flow.map(task => <a href={taskLink(task)} key={task.id}><Status task={task} /><span>{task.issue.title}</span><small>{task.project.name}</small></a>) : <div className="dashboard-flow-empty">{loading ? "正在读取任务…" : active && waiting ? "当前没有运行或等待准入的任务。" : "任务流转暂不可用。"}</div>}</div></div></section>
    </div><aside className="dashboard-aside"><section className="dashboard-platform" aria-labelledby="dashboard-platform"><div className="dashboard-section-head"><div><span className="dashboard-kicker">平台状态</span><h2 id="dashboard-platform">接单与执行</h2></div>{offline > 0 && <i className="dashboard-warning-dot" aria-label="存在离线 Node" />}</div><div className="dashboard-platform-row"><span>待处理事件</span><strong>{operations?.inboxPending ?? "—"}</strong></div><div className="dashboard-platform-row"><span>等待准入</span><strong>{operations?.admissionWaiting ?? "—"}</strong></div><div className="dashboard-platform-row"><span>执行节点</span><strong>{nodes ? `${nodes.length - offline} / ${nodes.length} 在线` : "—"}</strong></div>{offline > 0 && <div className="dashboard-warning"><Icon name="alert" />{offline} 个 Node 心跳中断</div>}<a href="#/control">查看运行总览 <Icon name="arrow" /></a></section><section className="dashboard-context"><h2>从需求到交付</h2><p>Issue 发起任务，任务串起执行、审查与验收，MR 保留交付证据。</p><div><a href={workbenchLink("/issues", projectId)}>查看 Issue</a><a href={workbenchLink("/merge-requests", projectId)}>查看 MR</a></div></section></aside></div>
  </div>;
}

export function ControlOverview() {
  const [operations, setOperations] = useState<Operations | null>(null);
  const [nodes, setNodes] = useState<NodeView[] | null>(null);
  const [checks, setChecks] = useState<CheckResult | null>(null);
  const [loadError, setLoadError] = useState("");
  const [checkError, setCheckError] = useState("");
  const [checking, setChecking] = useState(false);
  const generation = useRef(0);
  const load = useCallback(async () => {
    const request = ++generation.current;
    const results = await Promise.allSettled([api<Operations>("/operations"), api<NodeView[]>("/nodes")]);
    if (request !== generation.current) return;
    if (results[0].status === "fulfilled") setOperations(results[0].value);
    if (results[1].status === "fulfilled") setNodes(results[1].value || []);
    setLoadError(results.some(result => result.status === "rejected") ? "运行统计或节点状态读取失败，请重试。" : "");
  }, []);
  const runChecks = useCallback(async () => {
    setChecking(true); setCheckError("");
    try { setChecks(await api<CheckResult>("/system/checks", "POST")); }
    catch (cause) { setCheckError((cause as Error).message); }
    finally { setChecking(false); }
  }, []);
  useEffect(() => {
    void load(); void runChecks();
    const timer = window.setInterval(() => { if (!document.hidden) void load(); }, 15000);
    return () => { window.clearInterval(timer); generation.current++; };
  }, [load, runChecks]);
  const problemChecks = checks?.checks.filter(check => check.status === "warning" || check.status === "error").length;
  return <div className="dashboard-page control-overview"><div className="page-head dashboard-head"><div><h1>运行总览</h1><p>查看接单、准入、执行与连接状态，沿异常进入对应管理页面。</p></div><button disabled={checking} onClick={() => { void load(); void runChecks(); }}>{checking ? "检查中…" : "重新检查"}</button></div><ErrorBox error={[loadError, checkError].filter(Boolean).join(" ")} retry={() => { void load(); void runChecks(); }} />
    <div className="control-metrics"><Metric label="待处理事件" value={operations?.inboxPending} hint="Webhook inbox" /><Metric label="等待准入" value={operations?.admissionWaiting} hint="等待调度的任务" /><Metric label="执行槽已预约" value={operations?.runnerSlotsReserved} hint="含准备、Runner 与验证" /><Metric label="异常检查" value={problemChecks} hint={checks?.checkedAt ? `检查于 ${new Date(checks.checkedAt).toLocaleTimeString("zh-CN")}` : checking ? "检查中…" : "尚未检查"} warning={!!problemChecks} /></div>
    <div className="control-panels"><section className="control-panel" aria-labelledby="control-intake"><div className="dashboard-section-head"><div><span className="dashboard-kicker">接单链路</span><h2 id="control-intake">事件进入后发生了什么</h2></div></div><div className="control-stages"><div><span>Inbox 待处理</span><strong>{operations?.inboxPending ?? "—"}</strong></div><div><span>Inbox 重试中</span><strong>{operations?.inboxRetrying ?? "—"}</strong></div><div><span>等待准入</span><strong>{operations?.admissionWaiting ?? "—"}</strong></div><div><span>执行槽已预约</span><strong>{operations?.runnerSlotsReserved ?? "—"}</strong></div></div><p>这些是不同环节的当前数量；执行槽预约包含工作区准备、Runner 和验证。</p><a href="#/tasks?view=waiting&project=">查看等待任务 <Icon name="arrow" /></a></section>
      <section className="control-panel" aria-labelledby="control-nodes"><div className="dashboard-section-head"><div><span className="dashboard-kicker">执行资源</span><h2 id="control-nodes">Node 与 Runner</h2></div><a href="#/resources">管理资源 <Icon name="arrow" /></a></div><div className="control-node-list">{nodes?.length ? nodes.slice(0, 5).map(item => <div className="control-node" key={item.node.id}><i className={item.online ? "online" : "offline"} /><strong>{item.config?.name || item.node.id}</strong><span>{item.running} / {item.config?.capacity ?? item.node.capacity}</span><small>{item.online ? "在线" : "心跳中断"}</small></div>) : <div className="dashboard-empty">{nodes ? "尚无节点，请先配置执行资源。" : loadError ? "节点状态暂不可用。" : "正在读取节点…"}</div>}</div><p>节点容量按各 Node 配置展示，不代表当前可用执行槽。</p><a href="#/nodes">查看全部 Node <Icon name="arrow" /></a></section>
      <section className="control-panel control-wide" aria-labelledby="control-checks"><div className="dashboard-section-head"><div><span className="dashboard-kicker">连接检查</span><h2 id="control-checks">外部依赖</h2></div><a href="#/settings">系统设置 <Icon name="arrow" /></a></div><div className="control-check-list">{checks?.checks.length ? checks.checks.map(check => <div className="control-check" key={check.id}><span className={`control-check-state ${check.status}`}>{checkStates[check.status]}</span><strong>{checkNames[check.id] || check.id}</strong><span>{check.message}</span></div>) : <div className="dashboard-empty">{checking ? "正在检查连接…" : checks ? "当前没有检查项。" : "连接检查暂不可用。"}</div>}</div></section></div>
  </div>;
}

function Metric({ label, value, hint, warning }: { label: string; value?: number; hint: string; warning?: boolean }) {
  return <div className={`control-metric ${warning ? "is-warning" : ""}`}><span>{label}</span><strong>{value ?? "—"}</strong><small>{hint}</small></div>;
}

export function ResourcesOverview() {
  return <div className="dashboard-page resources-overview"><div className="page-head"><div><h1>执行资源</h1><p>配置执行节点、Runner 环境和所需镜像。</p></div></div><div className="resource-links"><a href="#/nodes"><Icon name="nodes" /><strong>执行节点</strong><span>查看容量、心跳、镜像就绪情况和节点配置。</span><small>管理 Node <Icon name="arrow" /></small></a><a href="#/runtime"><Icon name="runtime" /><strong>Runner 环境</strong><span>启用 Agent、检查 CLI 与节点上的执行环境。</span><small>管理 Runner <Icon name="arrow" /></small></a><a href="#/images"><Icon name="images" /><strong>镜像管理</strong><span>构建、分发和回退 Runner 与平台镜像。</span><small>管理镜像 <Icon name="arrow" /></small></a></div></div>;
}
