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
  const settingsPage = route.startsWith("/settings");
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
            aria-current={!projectPage && !runtimePage && !imagesPage && !nodesPage && !networksPage && !settingsPage ? "page" : undefined}
            className={!projectPage && !runtimePage && !imagesPage && !nodesPage && !networksPage && !settingsPage ? "active" : ""}
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
            Runner 环境
          </a>
          <a href="#/nodes" className={nodesPage?"active":""} aria-current={nodesPage?"page":undefined}><Icon name="nodes"/>Node 管理</a>
          <a aria-current={imagesPage ? "page" : undefined} className={imagesPage ? "active" : ""} href="#/images">
            <Icon name="images" />镜像管理
          </a>
          <a aria-current={settingsPage ? "page" : undefined} className={settingsPage ? "active" : ""} href="#/settings">
            <Icon name="settings" />系统设置
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
            {settingsPage ? "系统设置" : nodesPage ? "Node 管理" : networksPage ? "网络配置" : imagesPage ? "镜像管理" : runtimePage ? "Runner 环境" : projectPage ? "项目配置" : "任务中心"}
          </span>
          <span className="top-note">SwarmJet / 工作空间</span>
        </header>
        <main id="main" tabIndex={-1}>
          <ErrorBox error={error} />
          <Suspense fallback={<div className="loading">正在加载页面…</div>}>
          {settingsPage ? (<SystemSettings section={parts[2] || "overview"} />) : nodesPage ? (<Suspense fallback={<p>正在加载节点…</p>}><NodeSettings/></Suspense>) : networksPage ? (<Suspense fallback={<p>正在加载网络配置…</p>}><NetworkSettings/></Suspense>) : imagesPage ? (
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
            <Tasks key={route} projectId={selectedProject} />
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
