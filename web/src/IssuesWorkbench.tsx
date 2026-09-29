import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { api } from "./api";
import type { Project, Task } from "./types";
import { canOperateProject } from "./permissions";
import { Empty, ErrorBox, Field, Modal } from "./ui";
import MarkdownEditor, { MarkdownContent } from "./MarkdownEditor";
import IssueConversation from "./IssueConversation";
import { taskStageLabel } from "./task-presentation";
import { detailHash, getProjects, normalizeIssue, workbenchPath, type Assignee, type Page, type WorkbenchIssue } from "./workbench-api";
import { AssigneeSelector, LabelSelector, type WorkbenchLabel } from "./WorkbenchSelectors";
import "./workbench-pages.css";

const date = (value?: string) => value ? new Date(value).toLocaleString("zh-CN") : "—";

export default function IssuesWorkbench({ route, initialProjectId = "", onProjectChange, accessProjects = [], platformAdmin = false }: { route: string; initialProjectId?: string; onProjectChange?: (projectId: string) => void; accessProjects?: Project[]; platformAdmin?: boolean }) {
  const parts = route.split("?")[0].split("/");
  if (parts[2] && parts[3] && parts[4]) return <IssueDetail projectId={decodeURIComponent(parts[2])} repositoryId={decodeURIComponent(parts[3])} iid={Number(parts[4])} canOperate={canOperateProject(accessProjects.find(p => p.id === decodeURIComponent(parts[2]))?.myRole, platformAdmin)} />;
  return <IssueList initialProjectId={initialProjectId} openCreate={new URLSearchParams(route.split("?")[1] || "").get("create") === "1"} onProjectChange={onProjectChange} platformAdmin={platformAdmin} />;
}

function IssueList({ initialProjectId, openCreate, onProjectChange, platformAdmin }: { initialProjectId: string; openCreate: boolean; onProjectChange?: (projectId: string) => void; platformAdmin: boolean }) {
  const [projects, setProjects] = useState<Project[]>([]);
  const [projectId, setProjectId] = useState(initialProjectId);
  const [repositoryId, setRepositoryId] = useState("");
  const [state, setState] = useState("opened");
  const [label, setLabel] = useState<WorkbenchLabel[]>([]);
  const [assignee, setAssignee] = useState<Assignee[]>([]);
  const [search, setSearch] = useState("");
  const [cursor, setCursor] = useState("");
  const [history, setHistory] = useState<string[]>([]);
  const [page, setPage] = useState<Page<WorkbenchIssue>>({ items: [] });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(openCreate);
  useEffect(() => { setProjectId(initialProjectId); setRepositoryId(""); setLabel([]); setAssignee([]); setCursor(""); setHistory([]); }, [initialProjectId]);
  useEffect(() => { void getProjects().then(setProjects).catch(cause => setError((cause as Error).message)); }, []);
  const reset = () => { setCursor(""); setHistory([]); };
  const refresh = useCallback(async () => {
    setLoading(true); setError("");
    const params = new URLSearchParams({ state });
    if (projectId) params.set("projectId", projectId);
    if (repositoryId) params.set("repositoryId", repositoryId);
    if (label.length) params.set("labels", label[0].name);
    if (assignee.length) params.set("assigneeId", String(assignee[0].id));
    if (search.trim()) params.set("search", search.trim());
    if (cursor) params.set("cursor", cursor);
    try { const result = await api<Page<WorkbenchIssue>>(`/workbench/issues?${params}`); setPage({ ...result, items: (result.items || []).map(normalizeIssue) }); }
    catch (cause) { setError((cause as Error).message); }
    finally { setLoading(false); }
  }, [projectId, repositoryId, state, label, assignee, search, cursor]);
  useEffect(() => { void refresh(); }, [refresh]);
  const repositories = useMemo(() => projects.find(project => project.id === projectId)?.repositories || [], [projects, projectId]);
  const writableProjects = projects.filter(project => canOperateProject(project.myRole, platformAdmin));
  return <div className="workbench-page">
    <div className="page-head"><div><h1>Issue 工作台</h1><p>查看已接入 GitLab 仓库中的需求、讨论与任务轮次。</p></div>{writableProjects.length > 0 && <button className="primary" onClick={() => setCreating(true)}>创建 Issue</button>}</div>
    <section className="panel workbench-list">
      <div className="workbench-filters">
        <label>项目<select value={projectId} onChange={e => { setProjectId(e.target.value); setRepositoryId(""); setLabel([]); setAssignee([]); reset(); onProjectChange?.(e.target.value); }}><option value="">全部项目</option>{projects.map(p => <option value={p.id} key={p.id}>{p.name}</option>)}</select></label>
        <label>仓库<select value={repositoryId} disabled={!projectId} onChange={e => { setRepositoryId(e.target.value); setLabel([]); setAssignee([]); reset(); }}><option value="">全部仓库</option>{repositories.map(r => <option value={r.id} key={r.id}>{r.path || r.id}</option>)}</select></label>
        <label>状态<select value={state} onChange={e => { setState(e.target.value); reset(); }}><option value="opened">未关闭</option><option value="closed">已关闭</option><option value="all">全部</option></select></label>
        <LabelSelector kind="issues" projectId={projectId} repositoryId={repositoryId} selected={label} onChange={value => { setLabel(value); reset(); }} multiple={false} />
        <AssigneeSelector kind="issues" projectId={projectId} repositoryId={repositoryId} selected={assignee} onChange={value => { setAssignee(value); reset(); }} multiple={false} />
        <label>搜索<input type="search" value={search} onChange={e => { setSearch(e.target.value); reset(); }} placeholder="标题或内容" /></label>
      </div>
      <ErrorBox error={error} retry={() => void refresh()} />
      {loading ? <p className="workbench-muted">正在读取 Issue…</p> : page.items.length === 0 ? <Empty>没有符合条件的 Issue。</Empty> : <div className="workbench-rows">{page.items.map(issue => <a className="workbench-row" key={`${issue.projectId}/${issue.repositoryId}/${issue.iid}`} href={detailHash("issues", issue.projectId, issue.repositoryId, issue.iid)}>
        <div><span className="workbench-number">#{issue.iid}</span><strong>{issue.title}</strong><span className="workbench-state">{issue.state === "closed" ? "已关闭" : "未关闭"}</span></div>
        <div className="workbench-row-labels" role="group" aria-label="标签">{(issue.labels || []).length ? issue.labels.map(label => <span className="issue-meta-chip" key={label}>{label}</span>) : <span className="workbench-row-no-labels">无标签</span>}</div>
        <p>{projects.find(p => p.id === issue.projectId)?.name || issue.projectId} / {issue.repositoryId}</p>
      </a>)}</div>}
      <div className="workbench-pager"><button disabled={!history.length || loading} onClick={() => { setCursor(history.at(-1) || ""); setHistory(h => h.slice(0, -1)); }}>上一页</button><button disabled={!page.nextCursor || loading} onClick={() => { setHistory(h => [...h, cursor]); setCursor(page.nextCursor || ""); }}>下一页</button></div>
    </section>
    {creating && writableProjects.length > 0 && <CreateIssue projects={writableProjects} initialProjectId={writableProjects.some(project => project.id === projectId) ? projectId : ""} close={() => setCreating(false)} />}
  </div>;
}

function CreateIssue({ projects, initialProjectId, close }: { projects: Project[]; initialProjectId: string; close: () => void }) {
  const [projectId, setProjectId] = useState(initialProjectId);
  const [repositoryId, setRepositoryId] = useState("");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [selectedLabels, setSelectedLabels] = useState<WorkbenchLabel[]>([]);
  const [selectedAssignees, setSelectedAssignees] = useState<Assignee[]>([]);
  const [startTask, setStartTask] = useState(false);
  const [createdIssue, setCreatedIssue] = useState<WorkbenchIssue | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const repositories = projects.find(p => p.id === projectId)?.repositories || [];
  const create = async (event: FormEvent) => {
    event.preventDefault(); setBusy(true); setError("");
    try {
      const issue = createdIssue || normalizeIssue(await api<WorkbenchIssue>("/workbench/issues", "POST", { projectId, repositoryId, title, description, labels: Array.from(new Set([...selectedLabels.map(label => label.name), ...(startTask ? ["autonomous"] : [])])), assigneeIds: selectedAssignees.map(user => user.id) }));
      setCreatedIssue(issue);
      if (startTask) {
        const task = await api<Task>("/tasks", "POST", { projectId, issueProjectId: issue.gitlabProjectId, issueIid: issue.iid });
        location.hash = `#/tasks/${encodeURIComponent(task.id)}`;
      } else location.hash = detailHash("issues", projectId, repositoryId, issue.iid);
      close();
    } catch (cause) { setError((cause as Error).message); }
    finally { setBusy(false); }
  };
  return <Modal title="创建 Issue" close={close} className="workbench-dialog"><form onSubmit={create}><ErrorBox error={error} />{createdIssue && <p className="notice">Issue 已创建：<a href={detailHash("issues", projectId, repositoryId, createdIssue.iid)}>#{createdIssue.iid}</a>。可重试启动任务。</p>}
    <div className="form-grid"><Field label="项目"><select required value={projectId} onChange={e => { setProjectId(e.target.value); setRepositoryId(""); setSelectedLabels([]); setSelectedAssignees([]); }}><option value="">选择项目</option>{projects.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}</select></Field><Field label="仓库"><select required value={repositoryId} onChange={e => { setRepositoryId(e.target.value); setSelectedLabels([]); setSelectedAssignees([]); }}><option value="">选择仓库</option>{repositories.map(r => <option key={r.id} value={r.id}>{r.path || r.id}</option>)}</select></Field></div>
    <Field label="标题"><input required value={title} onChange={e => setTitle(e.target.value)} /></Field>
    <MarkdownEditor value={description} onChange={setDescription} />
    <div className="form-grid"><LabelSelector kind="issues" projectId={projectId} repositoryId={repositoryId} selected={selectedLabels} onChange={setSelectedLabels} /><AssigneeSelector kind="issues" projectId={projectId} repositoryId={repositoryId} selected={selectedAssignees} onChange={setSelectedAssignees} /></div>
    <label className="workbench-check"><input type="checkbox" checked={startTask} onChange={e => setStartTask(e.target.checked)} />创建并开始任务（添加 autonomous 标签）</label>
    <div className="form-actions"><button type="button" onClick={close}>取消</button><button className="primary" disabled={busy || !projectId || !repositoryId}>{busy ? "正在创建…" : createdIssue ? "重试开始任务" : startTask ? "创建并开始任务" : "仅创建 Issue"}</button></div>
  </form></Modal>;
}

function IssueDetail({ projectId, repositoryId, iid, canOperate }: { projectId: string; repositoryId: string; iid: number; canOperate: boolean }) {
  const path = workbenchPath("issues", projectId, repositoryId, iid);
  const [issue, setIssue] = useState<WorkbenchIssue | null>(null);
  const [title, setTitle] = useState(""); const [description, setDescription] = useState("");
  const [selectedLabels, setSelectedLabels] = useState<WorkbenchLabel[]>([]); const [selectedAssignees, setSelectedAssignees] = useState<Assignee[]>([]);
  const [note, setNote] = useState(""); const [error, setError] = useState(""); const [notice, setNotice] = useState(""); const [busy, setBusy] = useState(false);
  const busyRef = useRef(false);
  const editingRef = useRef(false);
  const [editing, setEditing] = useState(false);
  const load = useCallback(async () => {
    try { const item = normalizeIssue(await api<WorkbenchIssue>(path)); setIssue(item); if (!editingRef.current) { setTitle(item.title); setDescription(item.description || ""); setSelectedLabels((item.labels || []).map(name => ({ name }))); setSelectedAssignees(item.assignees || []); } setError(""); }
    catch (cause) { setError((cause as Error).message); }
  }, [path]);
  useEffect(() => { editingRef.current = false; setEditing(false); void load(); }, [load]);
  const act = async (action: () => Promise<unknown>, message: string) => { if (busyRef.current) return false; busyRef.current = true; setBusy(true); setError(""); setNotice(""); try { await action(); await load(); setNotice(message); return true; } catch (cause) { setError((cause as Error).message); return false; } finally { busyRef.current = false; setBusy(false); } };
  const hasActiveTask = (issue?.tasks || []).some(t => !["completed", "cancelled"].includes(t.stage));
  const hasPreviousTask = Boolean(issue?.tasks?.length);
  const canRequestNextRound = hasPreviousTask && issue?.state !== "closed" && (issue?.labels || []).includes("autonomous");
  const orderedTasks = [...(issue?.tasks || [])].sort((a, b) => (a.round || 0) - (b.round || 0));
  const latestTask = orderedTasks.at(-1);
  const cancelEdit = () => { if (!issue) return; editingRef.current = false; setEditing(false); setTitle(issue.title); setDescription(issue.description || ""); setSelectedLabels((issue.labels || []).map(name => ({ name }))); setSelectedAssignees(issue.assignees || []); };
  const submitNote = (startNextRound: boolean) => {
    const body = note;
    void act(async () => { await api(`${path}/notes`, "POST", startNextRound ? { body, startNextRound: true } : { body }); setNote(""); }, startNextRound ? "评论已提交，等待系统创建任务。" : "评论已发表。");
  };
  return <div className="workbench-page workbench-detail"><a href="#/issues" className="workbench-back">← Issue 列表</a><ErrorBox error={error} retry={() => void load()} />{notice && <p role="status" className="notice">{notice}</p>}{!issue ? <p>正在读取 Issue…</p> : <>
    <div className="page-head"><div><span className="workbench-number">Issue #{iid}</span><span className={`issue-state-pill ${issue.state === "closed" ? "closed" : ""}`}>{issue.state === "closed" ? "已关闭" : "进行中"}</span><h1>{issue.title}</h1><p>更新于 {date(issue.updatedAt)} · <a href={issue.webUrl} target="_blank" rel="noreferrer">在 GitLab 查看</a></p></div>{canOperate && <div className="actions"><button disabled={busy || (issue.state !== "closed" && hasActiveTask)} title={hasActiveTask ? "关联任务仍在进行中" : undefined} onClick={() => void act(() => api(path, "PUT", { stateEvent: issue.state === "closed" ? "reopen" : "close" }), issue.state === "closed" ? "已重开 Issue。" : "已关闭 Issue。")}>{issue.state === "closed" ? "重开" : "关闭"}</button></div>}</div>
    <div className="workbench-columns"><section className="panel workbench-main"><div className="panel-head issue-content-head"><h2>需求内容</h2>{canOperate && !editing && <button type="button" onClick={() => { editingRef.current = true; setEditing(true); }}>编辑 Issue</button>}</div><div className="workbench-pad">{canOperate && editing ? <>
      <Field label="标题"><input value={title} onChange={e => setTitle(e.target.value)} /></Field><MarkdownEditor value={description} onChange={setDescription} uploadPath={path} baseUrl={issue.webUrl} /><div className="form-grid"><LabelSelector kind="issues" projectId={projectId} repositoryId={repositoryId} selected={selectedLabels} onChange={setSelectedLabels} /><AssigneeSelector kind="issues" projectId={projectId} repositoryId={repositoryId} selected={selectedAssignees} onChange={setSelectedAssignees} /></div><div className="form-actions"><button type="button" disabled={busy} onClick={cancelEdit}>取消</button><button className="primary" disabled={busy || !title.trim()} onClick={() => void act(() => api(path, "PUT", { title, description, labels: selectedLabels.map(label => label.name), assigneeIds: selectedAssignees.map(user => user.id) }), "Issue 已保存。").then(saved => { if (saved) { editingRef.current = false; setEditing(false); } })}>{busy ? "正在保存…" : "保存更改"}</button></div>
    </> : <><div className="issue-description">{issue.description ? <MarkdownContent value={issue.description} baseUrl={issue.webUrl} uploadPath={path} /> : <p className="workbench-muted">暂无需求描述。</p>}</div><div className="issue-read-meta"><span>标签</span><div>{(issue.labels || []).length ? issue.labels.map(label => <span className="issue-meta-chip" key={label}>{label}</span>) : <small>无</small>}</div><span>负责人</span><div>{(issue.assignees || []).length ? issue.assignees!.map(person => <span className="issue-meta-chip" key={person.id}>{person.name || person.username}</span>) : <small>未指定</small>}</div></div></>}</div></section>
    <aside className="workbench-side"><section className="panel workbench-pad"><h2>任务轮次</h2>{latestTask && <a className="issue-current-round" href={`#/tasks/${encodeURIComponent(latestTask.id)}`}><span>当前轮次</span><strong>第 {latestTask.round || "?"} 轮 · {taskStageLabel(latestTask.stage)}</strong></a>}{hasPreviousTask ? <ol className="workbench-timeline">{orderedTasks.map(task => <li key={task.id}><a href={`#/tasks/${encodeURIComponent(task.id)}`}>第 {task.round || "?"} 轮 · {taskStageLabel(task.stage)}</a><small>{date(task.createdAt)}</small></li>)}</ol> : <p className="workbench-muted">尚无关联任务。</p>}{hasPreviousTask ? canOperate && <small>在下方填写补充需求，选择“评论并发起下一轮”。</small> : canOperate && <button disabled={busy || issue.state === "closed" || !(issue.labels || []).includes("autonomous")} onClick={() => void act(async () => { const task = await api<Task>("/tasks", "POST", { projectId, issueProjectId: issue.gitlabProjectId, issueIid: issue.iid }); location.hash = `#/tasks/${encodeURIComponent(task.id)}`; }, "已发起任务。")}>开始任务</button>}{canOperate && !(issue.labels || []).includes("autonomous") && <small>开始任务需要 autonomous 标签。</small>}</section></aside></div>
    <IssueConversation issue={issue} path={path} note={note} setNote={setNote} busy={busy} canOperate={canOperate} canRequestNextRound={canRequestNextRound} hasActiveTask={hasActiveTask} hasPreviousTask={hasPreviousTask} submitNote={submitNote} />
  </>}</div>;
}
