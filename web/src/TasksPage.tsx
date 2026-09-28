import { useCallback, useEffect, useState } from "react";
import { api } from "./api";
import type { Task, TaskPage, Project } from "./types";
import { nextAction, type TaskView } from "./task-presentation";
import { Icon } from "./ConsoleIcon";
import { stages, stageLabel, date, Status } from "./TaskStatus";
import { Empty, ErrorBox, Field, Modal } from "./ui";

const go = (path: string) => { location.hash = path; };
const initialView = (): TaskView => {
  const value = new URLSearchParams(location.hash.split("?")[1] || "").get("view");
  return value === "active" || value === "waiting" || value === "attention" || value === "ended" ? value : "all";
};

export default function Tasks({ projectId, onProjectChange, platformAdmin = false }: { projectId: string; onProjectChange?: (projectId: string) => void; platformAdmin?: boolean }) {
  const [tasks, setTasks] = useState<Task[]>([]),
    [page, setPage] = useState<TaskPage | null>(null),
    [attention, setAttention] = useState<Task[]>([]),
    [cursor, setCursor] = useState(""),
    [history, setHistory] = useState<string[]>([]),
    [projects, setProjects] = useState<Project[]>([]),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true),
    [filter, setFilter] = useState(""),
    [view, setView] = useState<TaskView>(initialView),
    [projectFilter, setProjectFilter] = useState(projectId),
    [search, setSearch] = useState(""),
    [create, setCreate] = useState(false);
  const refresh = useCallback(async () => {
    try {
      const params = new URLSearchParams({ limit: "50", view });
      if (projectFilter) params.set("project", projectFilter);
      if (filter) params.set("stage", filter);
      if (search.trim()) params.set("q", search.trim());
      if (cursor) params.set("cursor", cursor);
      const attentionParams = new URLSearchParams({ limit: "4", view: "attention" });
      if (projectFilter) attentionParams.set("project", projectFilter);
      const [t, urgent, p] = await Promise.all([
        api<TaskPage>(`/tasks?${params}`),
        api<TaskPage>(`/tasks?${attentionParams}`),
        api<Project[] | null>("/projects"),
      ]);
      setTasks(t.items || []);
      setPage(t);
      setAttention(urgent.items || []);
      setProjects(p || []);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [projectFilter, filter, search, view, cursor]);
  useEffect(() => { setProjectFilter(projectId); setCursor(""); setHistory([]); }, [projectId]);
  useEffect(() => {
    void refresh();
    const timer = setInterval(() => {
      if (!document.hidden) void refresh();
    }, 5000);
    return () => clearInterval(timer);
  }, [refresh]);
  const counts = page?.counts || { all: 0, active: 0, waiting: 0, attention: 0, ended: 0, completed: 0 };
  const rows = tasks;
  const resetPage = () => { setCursor(""); setHistory([]); };
  const projectOptions = Array.from(
    new Map(
      [...tasks.map((t) => t.project), ...projects].map((p) => [p.id, p]),
    ).values(),
  );
  const operableProjects = projects.filter(project => project.enabled && (platformAdmin || project.myRole === "owner" || project.myRole === "operator"));
  return (
    <div className="tasks-page">
      <div className="page-head">
        <div>
          <h1>任务中心</h1>
          <p>掌握进展，把注意力留给需要你的地方。</p>
        </div>
        {operableProjects.length > 0 && <button className="primary" onClick={() => setCreate(true)}>
          <Icon name="plus" /> 创建任务
        </button>}
      </div>
      <div className="task-overview" aria-label="任务概况">
        <span>
          <i className="live-dot" />
          <strong>{loading ? "—" : counts.active}</strong> 项任务正在推进
        </span>
        <span>
          <Icon name="clock" />
          <strong>{loading ? "—" : counts.waiting}</strong> 项等待准入
        </span>
        <span>
          <Icon name="check" />
          <strong>
            {loading ? "—" : counts.completed}
          </strong>{" "}
          项已完成
        </span>
        <small>
          {projectOptions.find((p) => p.id === projectFilter)?.name ||
            "全部项目"}{" "}
          · {counts.all} 项任务
        </small>
      </div>
      <ErrorBox error={error} retry={refresh} />
      {!loading && attention.length > 0 && (
        <section
          className="attention-section"
          aria-labelledby="attention-title"
        >
          <div className="section-heading">
            <h2 id="attention-title">
              需要你处理 <span className="count">{counts.attention}</span>
            </h2>
            {counts.attention > 4 && (
              <button
                onClick={() => {
                  setView("attention");
                  setFilter("");
                  setSearch("");
                  resetPage();
                  document
                    .getElementById("task-list")
                    ?.scrollIntoView({ block: "start" });
                }}
              >
                查看全部 {counts.attention} 项
              </button>
            )}
          </div>
          <div className="attention-grid">
            {attention.slice(0, 4).map((t) => (
              <a
                className="attention-card"
                key={t.id}
                href={`#/tasks/${encodeURIComponent(t.id)}`}
              >
                <span
                  className={`attention-icon ${t.stage === "blocked" ? "is-blocked" : ""}`}
                >
                  <Icon
                    name={t.stage === "waiting_approval" ? "branch" : "alert"}
                  />
                </span>
                <div>
                  <div className="attention-meta">
                    {t.project.name} <span>#{t.issue.iid}</span>
                    <Status task={t} />
                  </div>
                  <h3>{t.issue.title}</h3>
                  <p>{t.message || nextAction(t)}</p>
                  <span className="attention-link">
                    {t.stage === "waiting_approval" && !t.paused
                      ? "审阅变更"
                      : "查看任务"}
                    <Icon name="arrow" />
                  </span>
                </div>
              </a>
            ))}
          </div>
        </section>
      )}
      <section id="task-list" className="task-list" aria-label="任务列表">
        <div className="toolbar task-toolbar">
          <div className="view-tabs" aria-label="任务视图">
            {(
              [
                ["all", "全部任务", counts.all],
                ["active", "进行中", counts.active],
                ["waiting", "等待准入", counts.waiting],
                ["attention", "需要处理", counts.attention],
                ["ended", "已结束", counts.ended],
              ] as const
            ).map(([key, label, count]) => (
              <button
                key={key}
                aria-pressed={view === key}
                onClick={() => {
                  setView(key);
                  setFilter("");
                  resetPage();
                }}
              >
                {label}
                <span>{count}</span>
              </button>
            ))}
          </div>
          <div className="task-search">
            <Icon name="search" />
            <input
              type="search"
              aria-label="搜索任务"
              placeholder="搜索任务或编号…"
              value={search}
              onChange={(e) => { setSearch(e.target.value); resetPage(); }}
            />
          </div>
        </div>
        <div className="task-filterbar">
          <div className="filters">
            <select
              aria-label="筛选项目"
              value={projectFilter}
              onChange={(e) => { setProjectFilter(e.target.value); onProjectChange?.(e.target.value); resetPage(); }}
            >
              <option value="">全部项目</option>
              {projectOptions.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
            <select
              aria-label="筛选任务阶段"
              value={filter}
              onChange={(e) => { setFilter(e.target.value); resetPage(); }}
            >
              <option value="">全部阶段</option>
              {Object.entries(stages).map(([v, s]) => (
                <option key={v} value={v}>
                  {s}
                </option>
              ))}
            </select>
          </div>
        </div>
        {loading ? (
          <Empty>正在加载任务…</Empty>
        ) : rows.length ? (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>任务</th>
                  <th>项目</th>
                  <th>阶段</th>
                  <th>更新时间</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {rows.map((t) => (
                  <tr
                    key={t.id}
                    className="task-row"
                    onClick={(event) => {
                      if (
                        event.button !== 0 ||
                        event.metaKey ||
                        event.ctrlKey ||
                        event.shiftKey ||
                        event.altKey ||
                        (event.target as Element).closest("a, button, input, select")
                      ) return;
                      go(`/tasks/${encodeURIComponent(t.id)}`);
                    }}
                  >
                    <td>
                      <a
                        className="task-title"
                        href={`#/tasks/${encodeURIComponent(t.id)}`}
                      >
                        {t.issue.title}
                      </a>
                      <small className="task-latest">
                        {t.message ||
                          nextAction(t) ||
                          stageLabel(t.stage, t.project.provider) ||
                          t.stage}
                      </small>
                      <small className="task-reference">
                        #{t.issue.iid} <span>·</span> {t.id.slice(0, 12)}
                      </small>
                    </td>
                    <td>
                      {t.project.name}
                      <small>
                        {(t.project.repositories || [])
                          .map((r) => r.id)
                          .join(" / ")}
                      </small>
                    </td>
                    <td>
                      <Status task={t} />
                    </td>
                    <td className="muted task-updated">{date(t.updatedAt)}</td>
                    <td>
                      <a
                        aria-label={`查看任务 ${t.issue.title}`}
                        href={`#/tasks/${encodeURIComponent(t.id)}`}
                      >
                        <Icon name="arrow" />
                      </a>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <Empty>
            {filter || search || projectFilter || view !== "all"
              ? "没有符合筛选条件的任务。"
              : "还没有任务。创建任务，开始第一项交付。"}
          </Empty>
        )}
        <div className="table-foot">
          每 5 秒自动更新{" "}
          <span>
            显示 {rows.length} / {page?.total || 0} 项任务
          </span>
          <span className="task-page-actions">
            <button disabled={history.length === 0} onClick={() => { setCursor(history[history.length - 1]); setHistory(history.slice(0, -1)); }}>上一页</button>
            <button disabled={!page?.nextCursor} onClick={() => { setHistory([...history, cursor]); setCursor(page?.nextCursor || ""); }}>下一页</button>
          </span>
        </div>
      </section>
      {create && (
        <CreateTask projects={operableProjects} close={() => setCreate(false)} />
      )}
    </div>
  );
}
function CreateTask({
  projects,
  close,
}: {
  projects: Project[];
  close: () => void;
}) {
  const [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const [selectedProject, setSelectedProject] = useState("");
  const github = projects.find(project => project.id === selectedProject)?.provider === "github";
  return (
    <Modal title="创建任务" close={close}>
      <p className="muted">从带有 autonomous 标签的已打开 Issue 创建任务。</p>
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          const data = new FormData(e.currentTarget);
          setBusy(true);
          setError("");
          try {
            const task = await api<Task>("/tasks", "POST", {
              projectId: data.get("projectId"),
              issueProjectId: Number(data.get("issueProjectId")),
              issueIid: Number(data.get("issueIid")),
            });
            close();
            go(`/tasks/${encodeURIComponent(task.id)}`);
          } catch (e) {
            setError((e as Error).message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <ErrorBox error={error} />
        <Field label="项目">
          <select name="projectId" required value={selectedProject} onChange={e => setSelectedProject(e.target.value)}>
            <option value="" disabled>
              选择已启用项目
            </option>
            {projects
              .filter((p) => p.enabled)
              .map((p) => (
                <option value={p.id} key={p.id}>
                  {p.name}
                </option>
              ))}
          </select>
        </Field>
        <div className="form-grid">
          <Field label={github ? "Issue 所属 GitHub 仓库 ID" : "Issue 所属 GitLab 项目 ID"}>
            <input name="issueProjectId" type="number" min="1" required />
          </Field>
          <Field label={github ? "Issue 编号" : "Issue IID"}>
            <input name="issueIid" type="number" min="1" required />
          </Field>
        </div>
        {!projects.some((p) => p.enabled) && (
          <p>请先在项目配置中创建并启用项目。</p>
        )}
        <div className="form-actions">
          <button type="button" onClick={close}>
            取消
          </button>
          <button
            className="primary"
            disabled={busy || !projects.some((p) => p.enabled)}
          >
            {busy ? "正在创建…" : "创建任务"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
