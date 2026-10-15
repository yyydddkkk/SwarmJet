import { lazy, Suspense, useCallback, useEffect, useState } from "react";
import { api } from "./api";
import type { Project } from "./types";
import { ProjectEditor, newProject } from "./ProjectEditor";
import { Empty, ErrorBox, Modal, Panel } from "./ui";
import { canManageProject } from "./permissions";
import ProjectAccess from "./ProjectAccess";

const ProjectWizard = lazy(() => import("./ProjectWizard"));
const go = (path: string) => { location.hash = path; };

export default function Projects({
  id,
  initialization,
  importing,
  platformAdmin,
}: {
  id?: string;
  initialization?: boolean;
  importing?: boolean;
  platformAdmin: boolean;
}) {
  const [projects, setProjects] = useState<Project[]>([]),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true),
    [checks, setChecks] = useState<
      { name: string; ok: boolean; message: string }[] | null
    >(null),
    [checking, setChecking] = useState(""),
    [openMenu, setOpenMenu] = useState<string | null>(null);
  useEffect(() => {
    if (!openMenu) return;
    const closeOutside = (event: PointerEvent) => {
      if (event.target instanceof Element && !event.target.closest(".project-action-menu"))
        setOpenMenu(null);
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpenMenu(null);
    };
    document.addEventListener("pointerdown", closeOutside);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("pointerdown", closeOutside);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, [openMenu]);
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
  if (id === "new" && importing)
    return (
      <ProjectEditor
        initial={newProject()}
        platformAdmin={platformAdmin}
        onSaved={(project) => go(`/projects/${encodeURIComponent(project.id)}`)}
      />
    );
  if (id === "new")
    return (
      <Suspense fallback={<div className="loading">正在加载配置向导…</div>}>
        <ProjectWizard
          onSaved={(project) =>
            setProjects((old) => [...old.filter((item) => item.id !== project.id), { ...project, myRole: project.myRole || "owner" }])
          }
          onOpenProject={(project) =>
            go(`/projects/${encodeURIComponent(project)}`)
          }
        />
      </Suspense>
    );
  if (id) {
    const p = projects.find((p) => p.id === decodeURIComponent(id));
    if (p && (initialization || importing) && !canManageProject(p.myRole, platformAdmin)) return <Empty>当前账号没有项目配置权限。</Empty>;
    if (p && !initialization && !importing && location.hash.split("?")[0].endsWith("/access"))
      return canManageProject(p.myRole, platformAdmin) ? <ProjectAccess project={p} platformAdmin={platformAdmin} /> : <Empty>当前账号没有成员管理权限。</Empty>;
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
    return p && !canManageProject(p.myRole, platformAdmin) ? <div className="access-page"><div className="page-head"><div><a href="#/projects">← 项目列表</a><h1>{p.name}</h1><p>你在此项目的角色：{p.myRole === "operator" ? "操作员" : "观察者"}</p></div></div><section className="panel panel-padding"><h2>项目信息</h2><p>项目 ID：{p.id}</p><p>接入平台：{p.provider === "github" ? "GitHub" : "GitLab"}</p><p>仓库：{p.repositories?.length || 0} 个</p></section></div> : p ? (
      <ProjectEditor
        key={p.id}
        initial={p}
        platformAdmin={platformAdmin}
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
          <p>连接 GitLab Meta-Repo 或 GitHub 单仓库，定义目标分支、Agent 和交付检查。</p>
        </div>
        {platformAdmin && <a className="button primary" href="#/projects/new">
          ＋ 新建项目
        </a>}
      </div>
      <ErrorBox error={error} retry={refresh} />
      {loading ? (
        <Empty>正在加载项目…</Empty>
      ) : projects.length ? (
        <div className="project-grid">
          {projects.map((p) => (
            <article className={`panel project-card${openMenu === p.id ? " project-card-menu-open" : ""}`} key={p.id}>
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
              <p className="project-url">{p.metaMode === "direct" ? (p.repositories?.[0]?.url || "单仓库") : p.metaUrl}</p>
              <div className="project-stats">
                <span>{p.repositories?.length || 0} 个仓库</span>
                <span>{p.integration?.checks?.length || 0} 项检查</span>
              </div>
              <div className="form-actions project-actions">
                {(p.metaMode === "managed" || canManageProject(p.myRole, platformAdmin)) && (
                  <div className="project-action-menu">
                    <button
                      type="button"
                      className="project-action-trigger"
                      aria-expanded={openMenu === p.id}
                      aria-label={`${p.name} 的更多操作`}
                      onClick={() => setOpenMenu(current => current === p.id ? null : p.id)}
                    >
                      更多操作
                    </button>
                    {openMenu === p.id && <div className="project-action-list">
                      {p.metaMode === "managed" && (
                        <a href={`#/projects/${encodeURIComponent(p.id)}/initialization`}>查看初始化进度</a>
                      )}
                      {canManageProject(p.myRole, platformAdmin) && <>
                        <button
                          type="button"
                          disabled={checking === p.id}
                          onClick={async () => {
                            setOpenMenu(null);
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
                        <a href={`#/projects/${encodeURIComponent(p.id)}/access`}>成员与触发</a>
                      </>}
                    </div>}
                  </div>
                )}
                <a
                  className="button"
                  href={`#/projects/${encodeURIComponent(p.id)}`}
                >
                  {canManageProject(p.myRole, platformAdmin) ? "编辑项目 →" : "查看项目 →"}
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
