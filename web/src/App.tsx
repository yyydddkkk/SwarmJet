import { useCallback, useEffect, useState, lazy, Suspense } from "react";
import { api, APIError } from "./api";
import type { Project } from "./types";
import { Icon } from "./ConsoleIcon";
import { ErrorBox, Field } from "./ui";

const Tasks = lazy(() => import("./TasksPage"));
const TaskDetail = lazy(() => import("./TaskDetailPage"));
const Projects = lazy(() => import("./ProjectsPage"));
const RuntimeSettings = lazy(() => import("./RuntimeSettings"));
const NodeSettings = lazy(() => import("./NodeSettings"));
const NetworkSettings = lazy(() => import("./NetworkSettings"));
const ImageSettings = lazy(() => import("./ImageSettings"));
const SystemSettings = lazy(() => import("./SystemSettings"));
const IssuesWorkbench = lazy(() => import("./IssuesWorkbench"));
const MergeRequestsWorkbench = lazy(() => import("./MergeRequestsWorkbench"));
const WorkbenchOverview = lazy(() => import("./DashboardPage").then(module => ({ default: module.WorkbenchOverview })));
const ControlOverview = lazy(() => import("./DashboardPage").then(module => ({ default: module.ControlOverview })));
const ResourcesOverview = lazy(() => import("./DashboardPage").then(module => ({ default: module.ResourcesOverview })));

export default function App() {
  const [user, setUser] = useState<string | null>(null),
    [checking, setChecking] = useState(true),
    [error, setError] = useState(""),
    [route, setRoute] = useState(location.hash.slice(1) || "/overview"),
    [projectScope, setProjectScope] = useState(new URLSearchParams(location.hash.split("?")[1] || "").get("project") || ""),
    [projects, setProjects] = useState<Project[]>([]);
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
    const hash = () => setRoute(location.hash.slice(1) || "/overview");
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
  useEffect(() => {
    if (!user) return;
    api<Project[] | null>("/projects").then(result => setProjects(result || [])).catch(() => setProjects([]));
  }, [user]);
  useEffect(() => {
    const scoped = new URLSearchParams(route.split("?")[1] || "").get("project");
    if (scoped !== null) setProjectScope(scoped);
  }, [route]);
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
  const path = route.split("?")[0];
  const parts = path.split("/");
  const overviewPage = path === "/overview" || path === "/";
  const controlPage = path === "/control";
  const resourcesPage = path === "/resources";
  const projectPage = path.startsWith("/projects");
  const issuePage = path.startsWith("/issues");
  const mrPage = path.startsWith("/merge-requests");
  const taskPage = path.startsWith("/tasks");
  const runtimePage = path.startsWith("/runtime");
  const imagesPage = path.startsWith("/images");
  const settingsPage = path.startsWith("/settings");
  const nodesPage = path.startsWith("/nodes"), networksPage = path.startsWith("/networks");
  const executionPage = resourcesPage || runtimePage || nodesPage || imagesPage;
  const controlArea = controlPage || executionPage || projectPage || settingsPage || networksPage;
  const scopeVisible = overviewPage || (taskPage && !parts[2]) || (issuePage && !parts[2]) || (mrPage && !parts[2]);
  const scopedHref = (target: string) => `#${target}${projectScope ? `?project=${encodeURIComponent(projectScope)}` : ""}`;
  const changeScope = (value: string) => {
    setProjectScope(value);
    const params = new URLSearchParams(location.hash.split("?")[1] || "");
    if (value) params.set("project", value); else params.delete("project");
    history.replaceState(null, "", `#${path}${params.size ? `?${params}` : ""}`);
  };
  const pageName = overviewPage ? "行动总览" : taskPage ? "任务" : issuePage ? "Issue" : mrPage ? "MR" : controlPage ? "运行总览" : projectPage ? "项目与接入" : resourcesPage ? "执行资源" : runtimePage ? "Runner 环境" : nodesPage ? "Node 管理" : imagesPage ? "镜像管理" : networksPage ? "网络配置" : "系统设置";
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
        <a className="brand" href="#/overview">
          <span className="brand-mark">
            <Icon name="bolt" />
          </span>
          <span>SwarmJet</span>
        </a>
        <div className="workspace-identity">
          <span>S</span>
          <div>
            <strong>开发工作空间</strong>
            <small>任务协作与平台控制</small>
          </div>
        </div>
        <div className="nav-group"><div className="nav-label">工作台</div><nav aria-label="工作台导航">
          <a aria-current={overviewPage ? "page" : undefined} className={overviewPage ? "active" : ""} href={scopedHref("/overview")}><Icon name="overview" />行动总览</a>
          <a aria-current={taskPage ? "page" : undefined} className={taskPage ? "active" : ""} href={scopedHref("/tasks")}><Icon name="tasks" />任务</a>
          <a aria-current={issuePage ? "page" : undefined} className={issuePage ? "active" : ""} href={scopedHref("/issues")}><Icon name="issue" />Issue</a>
          <a aria-current={mrPage ? "page" : undefined} className={mrPage ? "active" : ""} href={scopedHref("/merge-requests")}><Icon name="branch" />MR</a>
        </nav></div>
        <div className="nav-group"><div className="nav-label">控制台</div><nav aria-label="控制台导航">
          <a aria-current={controlPage ? "page" : undefined} className={controlPage ? "active" : ""} href="#/control"><Icon name="activity" />运行总览</a>
          <a aria-current={projectPage ? "page" : undefined} className={projectPage ? "active" : ""} href="#/projects"><Icon name="projects" />项目与接入</a>
          <a aria-current={executionPage ? "page" : undefined} className={executionPage ? "active" : ""} href="#/resources"><Icon name="nodes" />执行资源</a>
          <a aria-current={settingsPage || networksPage ? "page" : undefined} className={settingsPage || networksPage ? "active" : ""} href="#/settings"><Icon name="settings" />系统设置</a>
        </nav></div>
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
        <header className="topbar"><span>{controlArea ? "控制台" : "工作台"}<span className="slash">/</span><strong>{pageName}</strong></span>{scopeVisible ? <label className="topbar-scope">项目范围 <select aria-label="项目范围" value={projectScope} onChange={event => changeScope(event.target.value)}><option value="">全部项目</option>{projects.map(project => <option key={project.id} value={project.id}>{project.name || project.id}</option>)}</select></label> : <span className="top-note">全平台</span>}</header>
        <main id="main" tabIndex={-1}>
          <ErrorBox error={error} />
          <Suspense fallback={<div className="loading">正在加载页面…</div>}>
          {overviewPage ? <WorkbenchOverview key={projectScope} projectId={projectScope} /> : controlPage ? <ControlOverview /> : resourcesPage ? <ResourcesOverview /> : issuePage ? <IssuesWorkbench key={route} route={route} initialProjectId={projectScope} onProjectChange={changeScope} /> : mrPage ? <MergeRequestsWorkbench key={route} route={route} initialProjectId={projectScope} onProjectChange={changeScope} /> : settingsPage ? (<SystemSettings section={parts[2] || "overview"} />) : nodesPage ? (<Suspense fallback={<p>正在加载节点…</p>}><NodeSettings/></Suspense>) : networksPage ? (<Suspense fallback={<p>正在加载网络配置…</p>}><NetworkSettings/></Suspense>) : imagesPage ? (
            <Suspense fallback={<div className="loading">正在加载镜像管理…</div>}><ImageSettings /></Suspense>
          ) : runtimePage ? (
            <Suspense
              fallback={<div className="loading">正在加载 Runner 环境…</div>}
            >
              <RuntimeSettings key={route} />
            </Suspense>
          ) : projectPage ? (
            <Projects
              key={route}
              id={parts[2]}
              initialization={parts[3] === "initialization"}
              importing={parts[3] === "import"}
            />
          ) : parts[1] === "tasks" && parts[2] ? (
            <TaskDetail key={route} id={decodeURIComponent(parts[2])} />
          ) : (
            <Tasks key={route} projectId={projectScope} onProjectChange={changeScope} />
          )}
          </Suspense>
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
        <a className="brand" href="#/overview">
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
