import { useCallback, useEffect, useState, lazy, Suspense } from "react";
import { api, APIError } from "./api";
import type { Project } from "./types";
import type { Principal } from "./permissions";
import { Icon } from "./ConsoleIcon";
import { ErrorBox } from "./ui";
import AuthScreen from "./AuthScreen";

const Tasks = lazy(() => import("./TasksPage"));
const TaskDetail = lazy(() => import("./TaskDetailPage"));
const Projects = lazy(() => import("./ProjectsPage"));
const RuntimeSettings = lazy(() => import("./RuntimeSettings"));
const NodeSettings = lazy(() => import("./NodeSettings"));
const NetworkSettings = lazy(() => import("./NetworkSettings"));
const SystemSettings = lazy(() => import("./SystemSettings"));
const AccessSettings = lazy(() => import("./AccessSettings"));
const GitLabIdentitySettings = lazy(() => import("./GitLabIdentitySettings"));
const IssuesWorkbench = lazy(() => import("./IssuesWorkbench"));
const MergeRequestsWorkbench = lazy(() => import("./MergeRequestsWorkbench"));
const WorkbenchOverview = lazy(() => import("./DashboardPage").then(module => ({ default: module.WorkbenchOverview })));
const ControlOverview = lazy(() => import("./DashboardPage").then(module => ({ default: module.ControlOverview })));
const ResourcesOverview = lazy(() => import("./DashboardPage").then(module => ({ default: module.ResourcesOverview })));

export default function App() {
  const [user, setUser] = useState<Principal | null>(null),
    [checking, setChecking] = useState(true),
    [error, setError] = useState(""),
    [route, setRoute] = useState(location.hash.slice(1) || "/overview"),
    [projectScope, setProjectScope] = useState(new URLSearchParams(location.hash.split("?")[1] || "").get("project") || ""),
    [projects, setProjects] = useState<Project[]>([]);
  const session = useCallback(async () => {
    setChecking(true);
    setError("");
    try {
      setUser(await api<Principal>("/session"));
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
  if (!user) return <AuthScreen outerError={error} onLogin={principal => { setError(""); setUser(principal); location.hash = "/overview"; }} />;
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
  const settingsPage = path.startsWith("/settings");
  const accessPage = path.startsWith("/access");
  const identityPage = path === "/identity";
  const nodesPage = path.startsWith("/nodes"), networksPage = path.startsWith("/networks");
  const executionPage = resourcesPage || runtimePage || nodesPage;
  const controlArea = controlPage || executionPage || projectPage || settingsPage || networksPage || accessPage;
  const scopeVisible = overviewPage || (taskPage && !parts[2]) || (issuePage && !parts[2]) || (mrPage && !parts[2]);
  const scopedHref = (target: string) => `#${target}${projectScope ? `?project=${encodeURIComponent(projectScope)}` : ""}`;
  const changeScope = (value: string) => {
    setProjectScope(value);
    const params = new URLSearchParams(location.hash.split("?")[1] || "");
    if (value) params.set("project", value); else params.delete("project");
    history.replaceState(null, "", `#${path}${params.size ? `?${params}` : ""}`);
  };
  const pageName = overviewPage ? "行动总览" : taskPage ? "任务" : issuePage ? "Issue" : mrPage ? "MR" : controlPage ? "运行总览" : projectPage ? "项目与接入" : resourcesPage ? "执行资源" : runtimePage ? "Runner 环境" : nodesPage ? "Node 管理" : networksPage ? "网络配置" : accessPage ? "成员与团队" : identityPage ? "个人 GitLab 身份" : "系统设置";
  const adminOnly = controlPage || executionPage || settingsPage || networksPage || accessPage;
  const allowProjectRoute = !projectPage || parts[2] !== "new" || user.platformAdmin;
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
          {user.platformAdmin && <>
          <a aria-current={controlPage ? "page" : undefined} className={controlPage ? "active" : ""} href="#/control"><Icon name="activity" />运行总览</a>
          </>}
          <a aria-current={projectPage ? "page" : undefined} className={projectPage ? "active" : ""} href="#/projects"><Icon name="projects" />项目与接入</a>
          {user.platformAdmin && <>
          <a aria-current={executionPage ? "page" : undefined} className={executionPage ? "active" : ""} href="#/resources"><Icon name="nodes" />执行资源</a>
          <a aria-current={settingsPage || networksPage ? "page" : undefined} className={settingsPage || networksPage ? "active" : ""} href="#/settings"><Icon name="settings" />系统设置</a>
          <a aria-current={accessPage ? "page" : undefined} className={accessPage ? "active" : ""} href="#/access"><Icon name="members" />成员与团队</a>
          </>}
          <a aria-current={identityPage ? "page" : undefined} className={identityPage ? "active" : ""} href="#/identity"><Icon name="identity" />个人 GitLab 身份</a>
        </nav></div>
        <div className="sidebar-foot">
          <div className="avatar">{(user.displayName || user.username).slice(0, 1).toUpperCase()}</div>
          <div>
            <strong>{user.displayName || user.username}</strong>
            <small>{user.platformAdmin ? "平台管理员" : "成员"}</small>
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
          {adminOnly && !user.platformAdmin || !allowProjectRoute ? <section className="panel panel-padding"><h1>无权访问</h1><p>当前账号没有此页面的访问权限。</p></section> : overviewPage ? <WorkbenchOverview key={projectScope} projectId={projectScope} platformAdmin={user.platformAdmin} canCreateIssue={user.platformAdmin || projects.some(project => project.myRole === "owner" || project.myRole === "operator")} /> : controlPage ? <ControlOverview /> : resourcesPage ? <ResourcesOverview /> : accessPage ? <AccessSettings projects={projects} currentUserId={user.id} /> : identityPage ? <GitLabIdentitySettings /> : issuePage ? <IssuesWorkbench key={route} route={route} initialProjectId={projectScope} onProjectChange={changeScope} accessProjects={projects} platformAdmin={user.platformAdmin} /> : mrPage ? <MergeRequestsWorkbench key={route} route={route} initialProjectId={projectScope} onProjectChange={changeScope} accessProjects={projects} platformAdmin={user.platformAdmin} /> : settingsPage ? (<SystemSettings section={parts[2] || "overview"} />) : nodesPage ? (<Suspense fallback={<p>正在加载节点…</p>}><NodeSettings/></Suspense>) : networksPage ? (<Suspense fallback={<p>正在加载网络配置…</p>}><NetworkSettings/></Suspense>) : runtimePage ? (
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
              platformAdmin={user.platformAdmin}
            />
          ) : parts[1] === "tasks" && parts[2] ? (
            <TaskDetail key={route} id={decodeURIComponent(parts[2])} platformAdmin={user.platformAdmin} />
          ) : (
            <Tasks key={route} projectId={projectScope} onProjectChange={changeScope} platformAdmin={user.platformAdmin} />
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
