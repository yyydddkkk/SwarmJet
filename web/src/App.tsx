import {
  useCallback,
  useId,
  cloneElement,
  isValidElement,
  useEffect,
  useRef,
  useState,
  lazy,
  Suspense,
  type FormEvent,
  type ReactNode,
} from "react";
import { api, APIError, getLog } from "./api";
import type { Task, TaskEvent, Project, Execution } from "./types";
import { ProjectEditor } from "./ProjectEditor";
const RuntimeSettings = lazy(() => import("./RuntimeSettings"));
const ProjectWizard = lazy(() => import("./ProjectWizard"));
export const stages: Record<string, string> = {
  queued: "排队中",
  waiting_dependencies: "等待依赖",
  preparing: "准备工作区",
  developing: "开发中",
  verifying: "验证中",
  publishing: "发布 MR",
  reviewing: "审查中",
  waiting_approval: "等待批准",
  waiting_manual_merge: "等待人工合并",
  acquiring_merge: "等待合并锁",
  merging: "合并中",
  partial_merge: "部分已合并",
  finalizing: "收尾中",
  completed: "已完成",
  blocked: "需要处理",
  cancelled: "已取消",
};
const date = (value: string) =>
  value
    ? new Date(value).toLocaleString("zh-CN", {
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
      })
    : "—";
const go = (path: string) => {
  location.hash = path;
};
export function ErrorBox({
  error,
  retry,
}: {
  error: string;
  retry?: () => void;
}) {
  return error ? (
    <div className="error" role="alert">
      <span>{error}</span>
      {retry && <button onClick={retry}>重试</button>}
    </div>
  ) : null;
}
export function Field({
  label,
  children,
  hint,
}: {
  label: string;
  children: ReactNode;
  hint?: string;
}) {
  const id = useId();
  return (
    <label className="field">
      <span id={`${id}-label`}>{label}</span>
      {isValidElement<Record<string, unknown>>(children)
        ? cloneElement(children, {
            "aria-labelledby": `${id}-label`,
            "aria-describedby": hint ? `${id}-hint` : undefined,
          })
        : children}
      {hint && <small id={`${id}-hint`}>{hint}</small>}
    </label>
  );
}
export function Panel({
  title,
  children,
  extra,
}: {
  title: string;
  children: ReactNode;
  extra?: ReactNode;
}) {
  return (
    <section className="panel">
      <div className="panel-head">
        <h2>{title}</h2>
        {extra}
      </div>
      {children}
    </section>
  );
}
function Status({ task }: { task: Task }) {
  return (
    <span className={`status ${task.paused ? "paused" : task.stage}`}>
      {task.paused ? "已暂停" : stages[task.stage] || task.stage}
    </span>
  );
}
function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>;
}
function Modal({
  title,
  children,
  close,
}: {
  title: string;
  children: ReactNode;
  close: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    ref.current?.showModal();
    const dialog = ref.current;
    return () => dialog?.close();
  }, []);
  return (
    <dialog ref={ref} onCancel={close} aria-labelledby="dialog-title">
      <div className="panel-head">
        <h2 id="dialog-title">{title}</h2>
        <button onClick={close} aria-label="关闭对话框">
          ×
        </button>
      </div>
      {children}
    </dialog>
  );
}
export default function App() {
  const [user, setUser] = useState<string | null>(null),
    [checking, setChecking] = useState(true),
    [error, setError] = useState(""),
    [route, setRoute] = useState(location.hash.slice(1) || "/tasks");
  const session = useCallback(async () => {
    setChecking(true);
    setError("");
    try {
      setUser((await api<{ username: string }>("/session")).username);
    } catch (e) {
      if (!(e instanceof APIError && e.status === 401))
        setError((e as Error).message);
    } finally {
      setChecking(false);
    }
  }, []);
  useEffect(() => {
    void session();
    const hash = () => setRoute(location.hash.slice(1) || "/tasks");
    const expired = () => {
      setUser(null);
      setError("会话已过期，请重新登录。");
    };
    window.addEventListener("hashchange", hash);
    window.addEventListener("session-expired", expired);
    return () => {
      window.removeEventListener("hashchange", hash);
      window.removeEventListener("session-expired", expired);
    };
  }, [session]);
  if (checking) return <div className="loading">正在连接控制台…</div>;
  if (!user)
    return (
      <Login
        error={error}
        onLogin={(name) => {
          setError("");
          setUser(name);
        }}
      />
    );
  const projectPage = route.startsWith("/projects");
  const runtimePage = route.startsWith("/runtime");
  const parts = route.split("/");
  return (
    <div className="app">
      <a
        className="skip"
        href="#main"
        onClick={(e) => {
          e.preventDefault();
          document.getElementById("main")?.focus();
        }}
      >
        跳至主要内容
      </a>
      <aside className="sidebar">
        <a className="brand" href="#/tasks">
          <span className="brand-mark">s.</span>
          <span>
            SwarmJet<small>工作控制台</small>
          </span>
        </a>
        <div className="nav-label">工作空间</div>
        <nav>
          <a
            className={!projectPage && !runtimePage ? "active" : ""}
            href="#/tasks"
          >
            <span aria-hidden="true">▤</span>任务中心
          </a>
          <a className={projectPage ? "active" : ""} href="#/projects">
            <span aria-hidden="true">▦</span>项目配置
          </a>
          <a className={runtimePage ? "active" : ""} href="#/runtime">
            <span aria-hidden="true">⌘</span>执行器环境
          </a>
        </nav>
        <div className="sidebar-foot">
          <div className="avatar">{user.slice(0, 1).toUpperCase()}</div>
          <div>
            <strong>{user}</strong>
            <small>管理员</small>
          </div>
          <button
            aria-label="退出登录"
            title="退出登录"
            onClick={async () => {
              try {
                await api("/logout", "POST");
                setUser(null);
              } catch (e) {
                setError((e as Error).message);
              }
            }}
          >
            ↪
          </button>
        </div>
      </aside>
      <div className="body">
        <header className="topbar">
          <span>
            工作空间 <span className="slash">/</span>{" "}
            {runtimePage ? "执行器环境" : projectPage ? "项目配置" : "任务中心"}
          </span>
          <span className="top-note">AUTONOMOUS WORKSPACE</span>
        </header>
        <main id="main" tabIndex={-1}>
          <ErrorBox error={error} />
          {runtimePage ? (
            <Suspense
              fallback={<div className="loading">正在加载执行器环境…</div>}
            >
              <RuntimeSettings />
            </Suspense>
          ) : projectPage ? (
            <Projects key={route} id={parts[2]} />
          ) : parts[1] === "tasks" && parts[2] ? (
            <TaskDetail key={route} id={decodeURIComponent(parts[2])} />
          ) : (
            <Tasks />
          )}
        </main>
        <footer>
          SwarmJet <span>·</span> 从需求到交付，每一步清晰可见。
        </footer>
      </div>
    </div>
  );
}
function Login({
  onLogin,
  error: outer,
}: {
  onLogin: (u: string) => void;
  error: string;
}) {
  const [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  return (
    <div className="login">
      <div className="login-story">
        <a className="brand" href="#/tasks">
          <span className="brand-mark">s.</span>SwarmJet
        </a>
        <div>
          <span className="eyebrow">YOUR ENGINEERING WORKSPACE</span>
          <h1>
            让工作前进。
            <br />
            让过程清晰。
          </h1>
          <p>
            连接需求、开发、审查与交付，
            <br />
            在一个工作空间里掌握全程。
          </p>
          <div className="login-flow">
            <span>需求</span>
            <i />
            <span>开发与验证</span>
            <i />
            <span>审查与交付</span>
          </div>
        </div>
        <small>SWARMJET / ADMIN CONSOLE</small>
      </div>
      <div className="login-form">
        <form
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            setError("");
            const data = new FormData(e.currentTarget);
            try {
              const result = await api<{ username: string }>("/login", "POST", {
                username: data.get("username"),
                password: data.get("password"),
              });
              onLogin(result.username);
            } catch (e) {
              setError((e as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          <span className="eyebrow">欢迎回来</span>
          <h1>登录工作控制台</h1>
          <p className="muted">使用管理员账号继续。</p>
          <ErrorBox error={error || outer} />
          <Field label="用户名">
            <input name="username" autoComplete="username" required autoFocus />
          </Field>
          <Field label="密码">
            <input
              name="password"
              type="password"
              autoComplete="current-password"
              required
            />
          </Field>
          <button className="primary full" disabled={busy}>
            {busy ? "正在登录…" : "登录控制台 →"}
          </button>
          <small className="muted">仅限授权管理员访问</small>
        </form>
      </div>
    </div>
  );
}
function Tasks() {
  const [tasks, setTasks] = useState<Task[]>([]),
    [projects, setProjects] = useState<Project[]>([]),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true),
    [filter, setFilter] = useState(""),
    [search, setSearch] = useState(""),
    [create, setCreate] = useState(false);
  const refresh = useCallback(async () => {
    try {
      const [t, p] = await Promise.all([
        api<Task[] | null>("/tasks"),
        api<Project[] | null>("/projects"),
      ]);
      setTasks(t || []);
      setProjects(p || []);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => {
    void refresh();
    const timer = setInterval(() => {
      if (!document.hidden) void refresh();
    }, 5000);
    return () => clearInterval(timer);
  }, [refresh]);
  const rows = tasks.filter(
    (t) =>
      (!filter || t.stage === filter) &&
      (t.issue.title + t.project.name + t.id)
        .toLowerCase()
        .includes(search.toLowerCase()),
  );
  return (
    <>
      <div className="page-head">
        <div>
          <div className="eyebrow">OVERVIEW</div>
          <h1>任务中心</h1>
          <p>追踪自主开发任务，从 Issue 到合并交付。</p>
        </div>
        <button className="primary" onClick={() => setCreate(true)}>
          ＋ 创建任务
        </button>
      </div>
      <div className="metrics">
        {[
          ["全部任务", tasks.length, "工作空间中的所有任务"],
          [
            "正在进行",
            tasks.filter(
              (t) =>
                ![
                  "completed",
                  "cancelled",
                  "blocked",
                  "waiting_approval",
                ].includes(t.stage),
            ).length,
            "开发、验证与交付",
          ],
          [
            "等待批准",
            tasks.filter((t) => t.stage === "waiting_approval").length,
            "需要你的审查决定",
          ],
          [
            "已完成",
            tasks.filter((t) => t.stage === "completed").length,
            "已完成交付",
          ],
        ].map(([label, value, sub]) => (
          <div className="metric" key={label}>
            <span>{label}</span>
            <strong>{value}</strong>
            <small>{sub}</small>
          </div>
        ))}
      </div>
      <ErrorBox error={error} retry={refresh} />
      <section className="panel">
        <div className="toolbar">
          <h2>
            任务列表 <span className="count">{rows.length}</span>
          </h2>
          <div className="filters">
            <input
              aria-label="搜索任务"
              placeholder="搜索任务或项目…"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
            <select
              aria-label="筛选任务阶段"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
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
                  <th>任务 / ISSUE</th>
                  <th>项目</th>
                  <th>阶段</th>
                  <th>更新时间</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {rows.map((t) => (
                  <tr key={t.id}>
                    <td>
                      <a
                        className="task-title"
                        href={`#/tasks/${encodeURIComponent(t.id)}`}
                      >
                        {t.issue.title}
                      </a>
                      <small>
                        #{t.issue.iid} <span>·</span> {t.id.slice(0, 12)}
                      </small>
                    </td>
                    <td>{t.project.name}</td>
                    <td>
                      <Status task={t} />
                    </td>
                    <td className="muted">{date(t.updatedAt)}</td>
                    <td>
                      <a
                        aria-label={`查看任务 ${t.issue.title}`}
                        href={`#/tasks/${encodeURIComponent(t.id)}`}
                      >
                        ↗
                      </a>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <Empty>
            {filter || search
              ? "没有符合筛选条件的任务。"
              : "还没有任务。创建任务，开始第一项交付。"}
          </Empty>
        )}
        <div className="table-foot">
          每 5 秒自动更新 <span>{tasks.length} 个任务</span>
        </div>
      </section>
      {create && (
        <CreateTask projects={projects} close={() => setCreate(false)} />
      )}
    </>
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
          <select name="projectId" required defaultValue="">
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
          <Field label="Issue 所属 GitLab 项目 ID">
            <input name="issueProjectId" type="number" min="1" required />
          </Field>
          <Field label="Issue IID">
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
function TaskDetail({ id }: { id: string }) {
  const [task, setTask] = useState<Task | null>(null),
    [events, setEvents] = useState<TaskEvent[]>([]),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [text, setText] = useState(""),
    [feedback, setFeedback] = useState(""),
    [reject, setReject] = useState(false),
    [log, setLog] = useState<{ name: string; body: string } | null>(null),
    [logBusy, setLogBusy] = useState("");
  const refresh = useCallback(async () => {
    try {
      const [t, e] = await Promise.all([
        api<Task>(`/tasks/${encodeURIComponent(id)}`),
        api<TaskEvent[] | null>(
          `/tasks/${encodeURIComponent(id)}/events?after=0`,
        ),
      ]);
      setTask(t);
      setEvents(e || []);
    } catch (e) {
      setError((e as Error).message);
    }
  }, [id]);
  useEffect(() => {
    void refresh();
    const timer = setInterval(() => {
      if (!document.hidden) void refresh();
    }, 3000);
    return () => clearInterval(timer);
  }, [refresh]);
  async function command(kind: string, message = "") {
    if (!task) return;
    setBusy(true);
    setError("");
    setFeedback("");
    try {
      await api(`/tasks/${encodeURIComponent(id)}/commands`, "POST", {
        id: crypto.randomUUID(),
        kind,
        text: message,
        digest: kind === "approve" ? task.reviewDigest : "",
      });
      setFeedback("指令已提交，任务状态将在执行后更新。");
      setText("");
      setReject(false);
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function showLog(name: string, execution: Execution) {
    setLogBusy(execution.id);
    try {
      setLog({ name, body: await getLog(id, execution.id) });
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLogBusy("");
    }
  }
  if (!task)
    return (
      <>
        <ErrorBox error={error} retry={refresh} />
        <Empty>{error ? "无法读取任务。" : "正在加载任务…"}</Empty>
      </>
    );
  const terminal = ["completed", "cancelled"].includes(task.stage);
  return (
    <>
      <a className="back" href="#/tasks">
        ← 返回任务中心
      </a>
      <div className="page-head detail-heading">
        <div>
          <div className="eyebrow">
            {task.project.name} <span> / ISSUE #{task.issue.iid}</span>
          </div>
          <h1>{task.issue.title}</h1>
          <p>
            <Status task={task} /> <span className="mono">{task.id}</span>
          </p>
        </div>
        {task.issue.url && (
          <a
            className="button"
            href={task.issue.url}
            target="_blank"
            rel="noreferrer"
          >
            查看 Issue ↗
          </a>
        )}
      </div>
      <ErrorBox
        error={error}
        retry={() => {
          setError("");
          void refresh();
        }}
      />
      {feedback && (
        <div className="notice" role="status">
          {feedback}
        </div>
      )}
      <div className="task-summary">
        <div>
          <span className="eyebrow">当前进展</span>
          <h2>{task.message || stages[task.stage]}</h2>
          <small>
            更新于 {date(task.updatedAt)} · 返工 {task.reworks} 次 · 每 3 秒更新
          </small>
        </div>
        <div className="actions">
          {!terminal && (
            <>
              <button
                disabled={busy}
                onClick={() => void command(task.paused ? "resume" : "pause")}
              >
                {task.paused ? "继续任务" : "暂停任务"}
              </button>
              {task.stage === "blocked" && (
                <button disabled={busy} onClick={() => void command("retry")}>
                  重试任务
                </button>
              )}
              <button
                className="danger-text"
                disabled={busy}
                onClick={() => {
                  if (window.confirm("取消此任务？已经合并的变更不会撤回。"))
                    void command("cancel");
                }}
              >
                取消任务
              </button>
            </>
          )}
        </div>
      </div>
      {task.stage === "waiting_approval" && (
        <section className="approval">
          <div>
            <span className="eyebrow">需要你的决定</span>
            <h2>审查已完成，等待批准合并</h2>
            <p>批准将绑定下方审查版本。代码变更后需要重新审查。</p>
            <code className="digest">
              {task.reviewDigest || "尚无审查摘要"}
            </code>
          </div>
          <div className="actions">
            <button disabled={busy} onClick={() => setReject(true)}>
              退回修改
            </button>
            <button
              className="primary"
              disabled={busy || !task.reviewDigest}
              onClick={() => void command("approve")}
            >
              {busy ? "正在提交…" : "批准此版本"}
            </button>
          </div>
        </section>
      )}
      <div className="detail-grid">
        <div className="main-column">
          <Panel
            title="代码仓库与合并请求"
            extra={
              <span className="count">
                {task.workspace?.revisions?.length || 0}
              </span>
            }
          >
            {task.workspace?.revisions?.length ? (
              task.workspace.revisions.map((r) => (
                <article className="repo-revision" key={r.repositoryId}>
                  <div>
                    <strong>{r.repositoryId}</strong>
                    <span className={`status ${r.merged ? "completed" : ""}`}>
                      {r.merged ? "已合并" : r.changed ? "有变更" : "无变更"}
                    </span>
                  </div>
                  <p>
                    <code>{r.branch || "—"}</code>
                    <span className="muted"> → </span>
                    <code>{r.targetBranch}</code>
                  </p>
                  <small className="mono">
                    {r.baseSha?.slice(0, 12) || "—"} →{" "}
                    {r.headSha?.slice(0, 12) || "—"}
                  </small>
                  {r.mrUrl && (
                    <a href={r.mrUrl} target="_blank" rel="noreferrer">
                      MR !{r.mrIid} ↗
                    </a>
                  )}
                  {r.sensitive && (
                    <small className="warning-text">包含敏感路径变更</small>
                  )}
                  <details>
                    <summary>查看变更文件（{r.files?.length || 0}）</summary>
                    <ul>
                      {(r.files || []).map((f) => (
                        <li key={f}>
                          <code>{f}</code>
                        </li>
                      ))}
                    </ul>
                  </details>
                </article>
              ))
            ) : (
              <Empty>工作区准备后，仓库版本会显示在这里。</Empty>
            )}
          </Panel>
          <Panel title="验证结果">
            {task.verification?.length ? (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>检查项</th>
                      <th>要求</th>
                      <th>结果</th>
                    </tr>
                  </thead>
                  <tbody>
                    {task.verification.map((v, i) => (
                      <tr key={i}>
                        <td>
                          <strong>{v.name}</strong>
                          {v.report && (
                            <details>
                              <summary>查看报告</summary>
                              <pre>{v.report}</pre>
                            </details>
                          )}
                        </td>
                        <td>{v.required ? "必需" : "可选"}</td>
                        <td>
                          <span
                            className={`status ${v.passed ? "completed" : "blocked"}`}
                          >
                            {v.passed ? "通过" : `未通过 · ${v.exitCode}`}
                          </span>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <Empty>尚无验证结果。</Empty>
            )}
          </Panel>
          <Panel title="Agent 执行记录">
            {Object.entries(task.executions || {}).length ? (
              Object.entries(task.executions || {}).map(([role, e]) => (
                <article className="execution" key={e.id}>
                  <div className="row-between">
                    <div>
                      <strong>
                        {role === "development"
                          ? "开发 Agent"
                          : role === "review"
                            ? "审查 Agent"
                            : role}
                      </strong>
                      <small>
                        {e.status} · 退出码 {e.exitCode}
                      </small>
                    </div>
                    <button
                      disabled={logBusy === e.id}
                      onClick={() => void showLog(role, e)}
                    >
                      {logBusy === e.id ? "读取中…" : "查看日志"}
                    </button>
                  </div>
                  {e.result && (
                    <>
                      <p>{e.result.summary}</p>
                      {e.result.needsHelp && (
                        <p className="warning-text">{e.result.needsHelp}</p>
                      )}
                      {(e.result.findings || []).map((f, i) => (
                        <p key={i}>
                          <code>
                            {f.repositoryId}/{f.path}
                          </code>
                          ：{f.message}
                        </p>
                      ))}
                    </>
                  )}
                </article>
              ))
            ) : (
              <Empty>Agent 启动后可在这里查看运行日志。</Empty>
            )}
          </Panel>
          <Panel title="需求假设与补充说明">
            {task.assumptions?.length ? (
              <ul className="text-list">
                {task.assumptions.map((a, i) => (
                  <li key={i}>{a}</li>
                ))}
              </ul>
            ) : (
              <p className="panel-padding muted">暂无记录的需求假设。</p>
            )}
            {(task.notes || []).map((note, i) => (
              <div className="note" key={i}>
                {note}
              </div>
            ))}
            <form
              className="panel-padding"
              onSubmit={(e) => {
                e.preventDefault();
                void command("note", text);
              }}
            >
              <Field
                label="补充说明"
                hint="为任务提供更多上下文，说明本身不会暂停任务。"
              >
                <textarea
                  required
                  value={text}
                  onChange={(e) => setText(e.target.value)}
                  rows={3}
                  placeholder="记录需求澄清、约束或验收补充…"
                />
              </Field>
              <button disabled={busy || !text.trim()} className="primary">
                提交说明
              </button>
            </form>
          </Panel>
        </div>
        <aside className="context-column">
          <Panel title="任务信息">
            <dl>
              <dt>创建时间</dt>
              <dd>{date(task.createdAt)}</dd>
              <dt>项目版本</dt>
              <dd>v{task.project.version}</dd>
              <dt>Meta-Repo 版本</dt>
              <dd className="mono">
                {task.workspace?.metaSha?.slice(0, 12) || "尚未准备"}
              </dd>
              <dt>Issue 标签</dt>
              <dd>
                {(task.issue.labels || []).map((l) => (
                  <span className="tag" key={l}>
                    {l}
                  </span>
                ))}
              </dd>
              <dt>前置依赖</dt>
              <dd>
                {task.dependencies?.length
                  ? task.dependencies.map((d) => (
                      <div key={`${d.projectId}-${d.iid}`}>
                        {d.projectId} / #{d.iid}
                      </div>
                    ))
                  : "无前置依赖"}
              </dd>
            </dl>
          </Panel>
          <Panel title="状态时间线">
            <ol className="timeline">
              {events.length ? (
                events.map((e) => (
                  <li key={e.id}>
                    <time>{date(e.createdAt)}</time>
                    <strong>{stages[e.kind] || e.kind}</strong>
                    <p>{e.message}</p>
                  </li>
                ))
              ) : (
                <li>
                  <p>暂无事件记录。</p>
                </li>
              )}
            </ol>
          </Panel>
        </aside>
      </div>
      {reject && (
        <Modal title="退回修改" close={() => setReject(false)}>
          <form
            onSubmit={(e: FormEvent<HTMLFormElement>) => {
              e.preventDefault();
              void command(
                "reject",
                String(new FormData(e.currentTarget).get("reason")),
              );
            }}
          >
            <Field label="修改要求">
              <textarea name="reason" required autoFocus rows={5} />
            </Field>
            <div className="form-actions">
              <button type="button" onClick={() => setReject(false)}>
                取消
              </button>
              <button className="primary" disabled={busy}>
                提交修改要求
              </button>
            </div>
          </form>
        </Modal>
      )}
      {log && (
        <Modal title={`${log.name} · 运行日志`} close={() => setLog(null)}>
          <pre className="log">{log.body || "日志为空。"}</pre>
        </Modal>
      )}
    </>
  );
}
function Projects({ id }: { id?: string }) {
  const [projects, setProjects] = useState<Project[]>([]),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true),
    [checks, setChecks] = useState<
      { name: string; ok: boolean; message: string }[] | null
    >(null),
    [checking, setChecking] = useState("");
  const refresh = useCallback(async () => {
    try {
      setProjects((await api<Project[] | null>("/projects")) || []);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => {
    void refresh();
  }, [refresh]);
  if (id === "new")
    return (
      <Suspense fallback={<div className="loading">正在加载配置向导…</div>}>
        <ProjectWizard
          onOpenProject={(project) =>
            go(`/projects/${encodeURIComponent(project)}`)
          }
        />
      </Suspense>
    );
  if (id) {
    const p = projects.find((p) => p.id === decodeURIComponent(id));
    return p ? (
      <ProjectEditor
        key={p.id}
        initial={p}
        onSaved={(p) => {
          setProjects((old) =>
            old.map((item) => (item.id === p.id ? p : item)),
          );
        }}
      />
    ) : (
      <>
        <ErrorBox error={error} retry={refresh} />
        <Empty>
          {loading ? "正在加载项目…" : "未找到项目。"}{" "}
          <a href="#/projects">返回列表</a>
        </Empty>
      </>
    );
  }
  return (
    <>
      <div className="page-head">
        <div>
          <div className="eyebrow">CONFIGURATION</div>
          <h1>项目配置</h1>
          <p>连接 Meta-Repo，定义仓库目标、Agent 和交付检查。</p>
        </div>
        <a className="button primary" href="#/projects/new">
          ＋ 新建项目
        </a>
      </div>
      <ErrorBox error={error} retry={refresh} />
      {loading ? (
        <Empty>正在加载项目…</Empty>
      ) : projects.length ? (
        <div className="project-grid">
          {projects.map((p) => (
            <article className="panel project-card" key={p.id}>
              <div className="row-between">
                <span className="project-icon">{p.name.slice(0, 1)}</span>
                <span
                  className={`status ${p.enabled ? "completed" : "paused"}`}
                >
                  {p.enabled ? "已启用" : "未启用"}
                </span>
              </div>
              <h2>
                <a href={`#/projects/${encodeURIComponent(p.id)}`}>{p.name}</a>
              </h2>
              <p className="mono muted">
                {p.id} · v{p.version}
              </p>
              <p className="project-url">{p.metaUrl}</p>
              <div className="project-stats">
                <span>{p.repositories?.length || 0} 个仓库</span>
                <span>{p.integration?.checks?.length || 0} 项检查</span>
              </div>
              <div className="form-actions">
                <button
                  disabled={checking === p.id}
                  onClick={async () => {
                    setChecking(p.id);
                    setError("");
                    try {
                      const result = await api<{
                        checks: {
                          name: string;
                          ok: boolean;
                          message: string;
                        }[];
                      }>(`/projects/${encodeURIComponent(p.id)}/check`, "POST");
                      setChecks(result.checks || []);
                    } catch (e) {
                      setError((e as Error).message);
                    } finally {
                      setChecking("");
                    }
                  }}
                >
                  {checking === p.id ? "检查中…" : "检查连接"}
                </button>
                <a
                  className="button"
                  href={`#/projects/${encodeURIComponent(p.id)}`}
                >
                  编辑项目 →
                </a>
              </div>
            </article>
          ))}
        </div>
      ) : (
        <Panel title="项目">
          <Empty>尚未配置项目。添加一个项目，连接你的开发工作空间。</Empty>
        </Panel>
      )}
      {checks && (
        <Modal title="项目连接检查" close={() => setChecks(null)}>
          {checks.length ? (
            checks.map((c, i) => (
              <div className="check-result" key={i}>
                <span className={`status ${c.ok ? "completed" : "blocked"}`}>
                  {c.ok ? "通过" : "失败"}
                </span>
                <strong>{c.name}</strong>
                <p>{c.message}</p>
              </div>
            ))
          ) : (
            <Empty>服务端没有返回检查结果。</Empty>
          )}
        </Modal>
      )}
    </>
  );
}
