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
import {
  taskView,
  nextAction,
  taskPhase,
  type TaskView,
} from "./task-presentation";
import { ProjectEditor } from "./ProjectEditor";
import { TaskTerminal } from "./TaskTerminal";
const RuntimeSettings = lazy(() => import("./RuntimeSettings"));
const NodeSettings = lazy(() => import("./NodeSettings"));
const NetworkSettings = lazy(() => import("./NetworkSettings"));
const ImageSettings = lazy(() => import("./ImageSettings"));
const ProjectWizard = lazy(() => import("./ProjectWizard"));
function Icon({
  name,
}: {
  name:
    | "tasks"
    | "projects"
    | "runtime"
    | "images"
    | "logout"
    | "plus"
    | "search"
    | "arrow"
    | "bolt"
    | "branch"
    | "alert"
    | "check";
}) {
  const paths = {
    bolt: "m13 2-9 12h7l-1 8 10-13h-7l1-7Z",
    branch: "M6 3v12a4 4 0 0 0 4 4h8M6 8h7a4 4 0 0 0 4-4V3",
    alert: "M12 8v5m0 3v.1M12 3 2 21h20L12 3Z",
    check: "m5 12 4 4L19 6",
    tasks: "M8 5h12M8 12h12M8 19h12M3 5h.01M3 12h.01M3 19h.01",
    projects:
      "M3 7V5a2 2 0 0 1 2-2h5l2 3h7a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7Z",
    runtime: "m5 7 5 5-5 5M13 17h6",
    images: "m12 3 9 5-9 5-9-5 9-5Zm-9 9 9 5 9-5M3 16l9 5 9-5",
    logout: "M9 4H5v16h4M9 12h12m-5-5 5 5-5 5",
    plus: "M12 5v14M5 12h14",
    search: "m16 16 5 5M18 10a8 8 0 1 1-16 0 8 8 0 0 1 16 0",
    arrow: "M5 12h14m-6-6 6 6-6 6",
  };
  return (
    <svg
      className="ui-icon"
      width="18"
      height="18"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d={paths[name]} />
    </svg>
  );
}

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
  const paused =
    task.paused && !["completed", "cancelled"].includes(task.stage);
  return (
    <span className={`status ${paused ? "paused" : task.stage}`}>
      {paused ? "已暂停" : stages[task.stage] || task.stage}
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
  const imagesPage = route.startsWith("/images");
  const nodesPage=route.startsWith("/nodes"), networksPage=route.startsWith("/networks");
  const parts = route.split("?")[0].split("/");
  const selectedProject =
    new URLSearchParams(route.split("?")[1]).get("project") || "";
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
          <span className="brand-mark">
            <Icon name="bolt" />
          </span>
          <span>SwarmJet</span>
        </a>
        <div className="workspace-identity">
          <span>S</span>
          <div>
            <strong>开发工作空间</strong>
            <small>自主开发 · 跨仓库协作</small>
          </div>
        </div>
        <nav aria-label="主导航">
          <a
            aria-current={!projectPage && !runtimePage && !imagesPage && !nodesPage && !networksPage ? "page" : undefined}
            className={!projectPage && !runtimePage && !imagesPage && !nodesPage && !networksPage ? "active" : ""}
            href="#/tasks"
          >
            <Icon name="tasks" />
            任务中心
          </a>
          <a
            aria-current={projectPage ? "page" : undefined}
            className={projectPage ? "active" : ""}
            href="#/projects"
          >
            <Icon name="projects" />
            项目配置
          </a>
          <a
            aria-current={runtimePage ? "page" : undefined}
            className={runtimePage ? "active" : ""}
            href="#/runtime"
          >
            <Icon name="runtime" />
            执行器环境
          </a>
          <a href="#/nodes" className={nodesPage?"active":""} aria-current={nodesPage?"page":undefined}><Icon name="projects"/>执行节点</a>
          <a href="#/networks" className={networksPage?"active":""} aria-current={networksPage?"page":undefined}><Icon name="branch"/>网络配置</a>
          <a aria-current={imagesPage ? "page" : undefined} className={imagesPage ? "active" : ""} href="#/images">
            <Icon name="images" />镜像管理
          </a>
        </nav>
        <SidebarProjects route={route} selectedProject={selectedProject} />
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
            <Icon name="logout" />
          </button>
        </div>
      </aside>
      <div className="body">
        <header className="topbar">
          <span>
            工作空间 <span className="slash">/</span>{" "}
            {nodesPage ? "执行节点" : networksPage ? "网络配置" : imagesPage ? "镜像管理" : runtimePage ? "执行器环境" : projectPage ? "项目配置" : "任务中心"}
          </span>
          <span className="top-note">SwarmJet / 工作空间</span>
        </header>
        <main id="main" tabIndex={-1}>
          <ErrorBox error={error} />
          {nodesPage ? (<Suspense fallback={<p>正在加载节点…</p>}><NodeSettings/></Suspense>) : networksPage ? (<Suspense fallback={<p>正在加载网络配置…</p>}><NetworkSettings/></Suspense>) : imagesPage ? (
            <Suspense fallback={<div className="loading">正在加载镜像管理…</div>}><ImageSettings /></Suspense>
          ) : runtimePage ? (
            <Suspense
              fallback={<div className="loading">正在加载执行器环境…</div>}
            >
              <RuntimeSettings key={route} />
            </Suspense>
          ) : projectPage ? (
            <Projects
              key={route}
              id={parts[2]}
              initialization={parts[3] === "initialization"}
            />
          ) : parts[1] === "tasks" && parts[2] ? (
            <TaskDetail key={route} id={decodeURIComponent(parts[2])} />
          ) : (
            <Tasks key={route} projectId={selectedProject} />
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
          <span className="brand-mark">
            <Icon name="bolt" />
          </span>
          SwarmJet
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
function SidebarProjects({
  route,
  selectedProject,
}: {
  route: string;
  selectedProject: string;
}) {
  const [projects, setProjects] = useState<Project[]>([]);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let current = true;
    api<Project[] | null>("/projects")
      .then((result) => {
        if (current) {
          setProjects(result || []);
          setFailed(false);
        }
      })
      .catch(() => {
        if (current) setFailed(true);
      });
    return () => {
      current = false;
    };
  }, [route]);
  return (
    <div className="sidebar-projects">
      <div className="nav-label">项目</div>
      {failed ? (
        <a className="sidebar-project-hint" href="#/projects">
          项目加载失败，前往重试
        </a>
      ) : projects.length ? (
        <nav aria-label="按项目查看任务">
          {projects.map((p) => (
            <a
              key={p.id}
              className={selectedProject === p.id ? "active" : ""}
              href={`#/tasks?project=${encodeURIComponent(p.id)}`}
            >
              <i className="project-dot" />
              <span>{p.name || p.id}</span>
            </a>
          ))}
        </nav>
      ) : (
        <a className="sidebar-project-hint" href="#/projects">
          接入你的第一个项目
        </a>
      )}
    </div>
  );
}

function Tasks({ projectId }: { projectId: string }) {
  const [tasks, setTasks] = useState<Task[]>([]),
    [projects, setProjects] = useState<Project[]>([]),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true),
    [filter, setFilter] = useState(""),
    [view, setView] = useState<TaskView>("all"),
    [projectFilter, setProjectFilter] = useState(projectId),
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
  const scoped = tasks.filter(
    (t) => !projectFilter || t.project.id === projectFilter,
  );
  const attention = scoped.filter((t) => taskView(t) === "attention");
  const active = scoped.filter((t) => taskView(t) === "active");
  const ended = scoped.filter((t) => taskView(t) === "ended");
  const rows = scoped
    .filter(
      (t) =>
        (view === "all" || taskView(t) === view) &&
        (!filter || t.stage === filter) &&
        `${t.issue.title} ${t.issue.iid} ${t.project.name} ${t.id}`
          .toLowerCase()
          .includes(search.trim().toLowerCase()),
    )
    .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt));
  const projectOptions = Array.from(
    new Map(
      [...tasks.map((t) => t.project), ...projects].map((p) => [p.id, p]),
    ).values(),
  );
  return (
    <div className="tasks-page">
      <div className="page-head">
        <div>
          <h1>任务中心</h1>
          <p>掌握进展，把注意力留给需要你的地方。</p>
        </div>
        <button className="primary" onClick={() => setCreate(true)}>
          <Icon name="plus" /> 创建任务
        </button>
      </div>
      <div className="task-overview" aria-label="任务概况">
        <span>
          <i className="live-dot" />
          <strong>{loading ? "—" : active.length}</strong> 项任务正在推进
        </span>
        <span>
          <Icon name="check" />
          <strong>
            {loading
              ? "—"
              : scoped.filter((t) => t.stage === "completed").length}
          </strong>{" "}
          项已完成
        </span>
        <small>
          {projectOptions.find((p) => p.id === projectFilter)?.name ||
            "全部项目"}{" "}
          · {scoped.length} 项任务
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
              需要你处理 <span className="count">{attention.length}</span>
            </h2>
            {attention.length > 4 && (
              <button
                onClick={() => {
                  setView("attention");
                  setFilter("");
                  setSearch("");
                  document
                    .getElementById("task-list")
                    ?.scrollIntoView({ block: "start" });
                }}
              >
                查看全部 {attention.length} 项
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
                ["all", "全部任务", scoped.length],
                ["active", "进行中", active.length],
                ["attention", "需要处理", attention.length],
                ["ended", "已结束", ended.length],
              ] as const
            ).map(([key, label, count]) => (
              <button
                key={key}
                aria-pressed={view === key}
                onClick={() => {
                  setView(key);
                  setFilter("");
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
              onChange={(e) => setSearch(e.target.value)}
            />
          </div>
        </div>
        <div className="task-filterbar">
          <div className="filters">
            <select
              aria-label="筛选项目"
              value={projectFilter}
              onChange={(e) => setProjectFilter(e.target.value)}
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
                  <th>任务</th>
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
                      <small className="task-latest">
                        {t.message ||
                          nextAction(t) ||
                          stages[t.stage] ||
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
            显示 {rows.length} / {scoped.length} 项任务
          </span>
        </div>
      </section>
      {create && (
        <CreateTask projects={projects} close={() => setCreate(false)} />
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
    [logBusy, setLogBusy] = useState(""),
    [detailView, setDetailView] = useState("delivery");
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
            {task.project.name} <span> / Issue #{task.issue.iid}</span>
          </div>
          <h1>{task.issue.title}</h1>
          <p>
            <Status task={task} /> <span className="mono">{task.id}</span>
          </p>
        </div>
        <div className="task-detail-actions">
          <TaskTerminal task={task} />
          {task.issue.url && (
            <a className="button" href={task.issue.url} target="_blank" rel="noreferrer">
              查看 Issue ↗
            </a>
          )}
        </div>
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
      <div className="detail-grid">
        <div className="main-column">
          <section className="task-summary">
            <div className="section-heading">
              <h2>执行进展</h2>
              <small>更新于 {date(task.updatedAt)}</small>
            </div>
            <ol className="stage-track" aria-label="任务流程">
              {[
                "准备",
                "开发",
                "验证",
                "发布与审查",
                "合并",
                task.stage === "completed" ? "完成" : "收尾",
              ].map((label, index) => (
                <li
                  key={label}
                  aria-current={taskPhase(task) === index ? "step" : undefined}
                  className={taskPhase(task) === index ? "current" : ""}
                >
                  <span />
                  {label}
                </li>
              ))}
            </ol>
            <div className="current-progress">
              <Status task={task} />
              <p>{task.message || stages[task.stage] || task.stage}</p>
            </div>
            <small>返工 {task.reworks} 次 · 每 3 秒自动更新</small>
          </section>
          {taskView(task) === "attention" &&
            task.stage !== "waiting_approval" && (
              <section
                className={`task-guidance ${task.stage === "blocked" ? "is-blocked" : ""}`}
              >
                <Icon name="alert" />
                <div>
                  <h2>{task.paused ? "任务已暂停" : stages[task.stage]}</h2>
                  <p>{nextAction(task)}</p>
                </div>
              </section>
            )}
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
          <div className="view-tabs detail-views" aria-label="任务详情视图">
            {[
              ["delivery", "交付结果"],
              ["execution", "执行记录"],
              ["context", "需求与说明"],
            ].map(([key, label]) => (
              <button
                key={key}
                aria-pressed={detailView === key}
                onClick={() => setDetailView(key)}
              >
                {label}
              </button>
            ))}
          </div>
          <div hidden={detailView !== "delivery"}>
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
          </div>
          <div hidden={detailView !== "execution"}>
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
          </div>
          <div hidden={detailView !== "context"}>
            <Panel title="需求假设与补充说明">
              {task.issue.description && (
                <div className="issue-description">
                  {task.issue.description}
                </div>
              )}
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
        </div>
        <aside className="context-column">
          <Panel title="任务操作">
            <div className="panel-padding">
              <div className="actions">
                {!terminal && (
                  <>
                    <button
                      disabled={busy}
                      onClick={() =>
                        void command(task.paused ? "resume" : "pause")
                      }
                    >
                      {task.paused ? "继续任务" : "暂停任务"}
                    </button>
                    {task.stage === "blocked" && (
                      <button
                        disabled={busy}
                        onClick={() => void command("retry")}
                      >
                        重试任务
                      </button>
                    )}
                    <button
                      className="danger-text"
                      disabled={busy}
                      onClick={() => {
                        if (
                          window.confirm("取消此任务？已经合并的变更不会撤回。")
                        )
                          void command("cancel");
                      }}
                    >
                      取消任务
                    </button>
                  </>
                )}
              </div>

              <button
                className="execution-shortcut"
                onClick={() => setDetailView("execution")}
              >
                <Icon name="runtime" />
                查看执行记录
              </button>
            </div>
          </Panel>
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
                [...events].reverse().map((e) => (
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
function Projects({
  id,
  initialization,
}: {
  id?: string;
  initialization?: boolean;
}) {
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
    if (p && initialization && p.metaMode === "managed")
      return (
        <Suspense fallback={<div className="loading">正在加载初始化进度…</div>}>
          <ProjectWizard
            initialProject={p}
            onOpenProject={(project) =>
              go(`/projects/${encodeURIComponent(project)}`)
            }
          />
        </Suspense>
      );
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
                {p.metaMode === "managed" && (
                  <a
                    className="button"
                    href={`#/projects/${encodeURIComponent(p.id)}/initialization`}
                  >
                    查看初始化进度
                  </a>
                )}
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
