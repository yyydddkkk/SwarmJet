import { useCallback, useEffect, useState } from "react";
import { api } from "./api";
import type { AgentSpec } from "./agents";
import RegistrySettings from "./RegistrySettings";
import { NetworkSelect, type NetworkChoice } from "./NetworkSettings";
import type { NodeView } from "./NodeSettings";
import { Field } from "./App";
import "./image-settings.css";

type ImageConfig = {
  repository?: string;
  platform?: string;
  network?: NetworkChoice;
  agents: string[];
  baseImage: string;
  extraPackages: string[];
  buildArgs: Record<string, string>;
};
type Build = {
  id: string;
  tag: string;
  imageId?: string;
  reference?: string;
  status: string;
  source: string;
  config: ImageConfig;
  createdAt: string;
  error?: string;
};
type View = {
  scope?: string;
  config: ImageConfig;
  builds: Build[];
  active: { buildId: string; tag: string; imageId: string } | null;
  defaultImage: string;
  agents: AgentSpec[];
  parameters: { key: string; label: string; default: string }[];
};
type RegistryImage = { digest: string; reference: string; tags: string[] };
type RegistryList = { repository: string; images: RegistryImage[] };
const statuses: Record<string, string> = {
  building: "构建中",
  cancelling: "正在取消",
  succeeded: "可应用",
  failed: "构建失败",
  cancelled: "已取消",
  interrupted: "已中断",
};
const running = (b: Build) =>
  b.status === "building" || b.status === "cancelling";

export default function ImageSettings() {
	const [tab, setTab] = useState<"images" | "build" | "settings">("build");
  const [targets, setTargets] = useState<string[]>([]);
  const [nodes, setNodes] = useState<NodeView[]>([]);
  const reloadNodes = useCallback(() => api<NodeView[]>("/nodes").then(setNodes), []);
  useEffect(() => {
    void reloadNodes().catch(() => {});
  }, [reloadNodes]);
  const [view, setView] = useState<View | null>(null);
  const [config, setConfig] = useState<ImageConfig | null>(null);
  const [extraPackages, setExtraPackages] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [pending, setPending] = useState("");
  const [existingImage, setExistingImage] = useState("");
  const [logID, setLogID] = useState("");
  const [log, setLog] = useState("");
  const [logError, setLogError] = useState("");

  const refresh = useCallback(async (signal?: AbortSignal) => {
    const result = await api<View>("/runtime/images", "GET", undefined, signal);
    setView(result);
    return result;
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    refresh(controller.signal)
      .then((result) => {
        setConfig(result.scope === "registry" ? { ...result.config, repository: "" } : result.config);
        setExtraPackages(result.config.extraPackages.join(" "));
        if (result.scope === "registry") setTab("images");
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(e.message);
      });
    return () => controller.abort();
  }, [refresh]);

  const building = view?.builds.some(running) || false;
  useEffect(() => {
    if (!building) return;
    const controller = new AbortController();
    const timer = setInterval(() => {
      refresh(controller.signal).catch((e) => {
        if (!controller.signal.aborted) setError(e.message);
      });
    }, 1500);
    return () => {
      clearInterval(timer);
      controller.abort();
    };
  }, [building, refresh]);

  const logRunning =
    view?.builds.some((b) => b.id === logID && running(b)) || false;
  useEffect(() => {
    if (!logID) return;
    const controller = new AbortController();
    setLog("");
    setLogError("");
    const load = () =>
      api<{ log: string }>(
        `/runtime/images/builds/${logID}/log`,
        "GET",
        undefined,
        controller.signal,
      )
        .then((result) => setLog(result.log))
        .catch((e) => {
          if (!controller.signal.aborted) setLogError(e.message);
        });
    void load();
    const timer = logRunning ? setInterval(() => void load(), 1500) : undefined;
    return () => {
      clearInterval(timer);
      controller.abort();
    };
  }, [logID, logRunning]);

  async function action(name: string, work: () => Promise<void>) {
    setPending(name);
    setError("");
    setNotice("");
    try {
      await work();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setPending("");
    }
  }

  function currentConfig(): ImageConfig {
    return {
      ...config!,
      ...(view?.scope === "registry" ? { repository: "" } : {}),
      extraPackages: extraPackages.split(/\s+/).filter(Boolean),
    };
  }

  return (
    <div className="image-settings">
      <div className="page-head">
        <div>
          <h1>镜像管理</h1>
          <p>查看仓库版本、分发到执行节点，并构建 Agent 镜像。</p>
        </div>
        <a className="button" href="#/runtime">
          执行器环境
        </a>
      </div>
      <div className="image-tabs" role="tablist" aria-label="镜像管理视图">
        <button role="tab" aria-selected={tab === "images"} onClick={() => setTab("images")}>仓库镜像</button>
        <button role="tab" aria-selected={tab === "build"} onClick={() => setTab("build")}>镜像构建</button>
        <button role="tab" aria-selected={tab === "settings"} onClick={() => setTab("settings")}>仓库设置</button>
      </div>
      {tab === "settings" && <RegistrySettings />}
      {tab === "images" && <RegistryInventory builds={view?.builds || []} nodes={nodes} reloadNodes={reloadNodes} onBuild={() => setTab("build")} />}
      {error && (
        <div className="error" role="alert">
          {error}
          <button
            onClick={() =>
              void action("refresh", async () => {
                const result = await refresh();
                if (!config) {
                  setConfig(result.config);
                  setExtraPackages(result.config.extraPackages.join(" "));
                }
              })
            }
            disabled={!!pending}
          >
            重新检查
          </button>
        </div>
      )}
      {notice && (
        <p className="notice" role="status">
          {notice}
        </p>
      )}
      {!view && !error && tab === "build" && <p className="loading">正在读取镜像配置…</p>}
      {tab === "build" && view && config && (
        <>
          {view.scope !== "registry" && <section className="panel image-current" aria-label="当前执行镜像">
            <div>
              <h2>{view.scope === "registry" ? "镜像分发" : "当前执行镜像"}</h2>
              <code>
                {view.scope === "registry"
                  ? "按节点应用仓库镜像"
                  : view.active?.tag || view.defaultImage}
              </code>
              <p>应用镜像后，新任务使用此版本；已接单任务保留原镜像。</p>
            </div>
            <span className="status completed">
              {view.scope === "registry"
                ? "跨节点分发"
                : view.active
                  ? "已应用"
                  : "部署默认"}
            </span>
            {view.active && (
              <details>
                <summary>镜像摘要</summary>
                <code>{view.active.imageId}</code>
              </details>
            )}
          </section>}

          <section className="panel image-recipe" aria-label="构建配置">
            <div className="panel-head">
              <div>
                <h2>构建配置</h2>
                <p>为 Agent 执行环境安装 CLI 和开发工具；构建完成后再选择节点分发。</p>
              </div>
            </div>
            <div className="panel-padding">
              <fieldset className="image-agent-picker">
                <legend>安装的 Agent</legend>
                {view.agents.map((agent) => (
                  <label className="checkbox" key={agent.id}>
                    <input
                      type="checkbox"
                      checked={config.agents.includes(agent.id)}
                      disabled={!!pending}
                      onChange={(e) =>
                        setConfig({
                          ...config,
                          agents: e.target.checked
                            ? [...config.agents, agent.id]
                            : config.agents.filter((id) => id !== agent.id),
                        })
                      }
                    />
                    {agent.name}
                  </label>
                ))}
              </fieldset>
              <p className="image-help">
                构建不会启用
                Agent。应用镜像后，请在执行器环境中完成启用和登录。受管项目的静态初始化需要
                OpenCode。
              </p>
              <Field label="构建步骤网络配置">
                <NetworkSelect
                  value={config.network || { mode: "default" }}
                  onChange={(network) => setConfig({ ...config, network })}
                />
              </Field>
              {view.scope !== "registry" && <Field label="发布镜像路径（留空使用仓库默认）">
                <input
                  value={config.repository || ""}
                  onChange={(e) =>
                    setConfig({ ...config, repository: e.target.value })
                  }
                />
              </Field>}
              <Field label="目标架构">
                <select
                  value={config.platform || ""}
                  onChange={(e) =>
                    setConfig({ ...config, platform: e.target.value })
                  }
                >
                  <option value="">构建机原生架构</option>
                  <option value="linux/amd64">linux/amd64</option>
                  <option value="linux/arm64">linux/arm64</option>
                </select>
              </Field>
              <details className="image-advanced">
                <summary>高级构建参数</summary>
                <div className="form-grid">
                  <Field
                    label="基础镜像"
                    hint="使用包含 Node 的 Debian / Ubuntu 兼容镜像。"
                  >
                    <input
                      value={config.baseImage}
                      onChange={(e) =>
                        setConfig({ ...config, baseImage: e.target.value })
                      }
                    />
                  </Field>
                  <Field
                    label="额外系统包"
                    hint="空格分隔的 apt 包名，例如 ffmpeg libvips-dev。"
                  >
                    <input
                      value={extraPackages}
                      onChange={(e) => setExtraPackages(e.target.value)}
                      placeholder="ffmpeg libvips-dev"
                    />
                  </Field>
                  {view.parameters.map((parameter) => (
                    <Field label={parameter.label} key={parameter.key}>
                      <input
                        value={
                          config.buildArgs[parameter.key] ?? parameter.default
                        }
                        onChange={(e) =>
                          setConfig({
                            ...config,
                            buildArgs: {
                              ...config.buildArgs,
                              [parameter.key]: e.target.value,
                            },
                          })
                        }
                      />
                    </Field>
                  ))}
                </div>
                <p className="image-help">
                  Antigravity 使用构建模板中固定版本及校验值。DeepSeek Harness
                  的精确续接需要支持该功能的发行版，当前固定版本为预发行版。软件源地址不接受内嵌账号或密钥。
                </p>
              </details>
              <div className="image-actions">
                <button
                  disabled={!!pending}
                  onClick={() =>
                    void action("save", async () => {
                      const saved = await api<ImageConfig>(
                        "/runtime/images/config",
                        "PUT",
                        currentConfig(),
                      );
                      setConfig(saved);
                      setNotice("构建配置已保存，当前镜像未切换。");
                    })
                  }
                >
                  {pending === "save" ? "保存中…" : "保存配置"}
                </button>
                <button
                  className="primary"
                  disabled={!!pending || building || config.agents.length === 0}
                  onClick={() =>
                    void action("build", async () => {
                      const build = await api<Build>(
                        "/runtime/images/builds",
                        "POST",
                        currentConfig(),
                      );
                      setLogID(build.id);
                      await refresh();
                      setNotice("镜像开始构建，成功后可单独应用。");
                    })
                  }
                >
                  {pending === "build"
                    ? "正在提交…"
                    : building
                      ? "构建进行中"
                      : "保存并构建"}
                </button>
              </div>
            </div>
          </section>

          {view.scope !== "registry" && (
            <details className="panel image-import">
              <summary>使用已有镜像</summary>
              <p>
                填写宿主机 Docker 中已有的 Agent
                镜像。检查成功后，再从记录中应用。
              </p>
              <div className="image-import-form">
                <Field label="已有镜像地址">
                  <input
                    value={existingImage}
                    onChange={(e) => setExistingImage(e.target.value)}
                    placeholder="registry.example.com/team/agent:version"
                  />
                </Field>
                <button
                  disabled={!!pending || !existingImage.trim()}
                  onClick={() =>
                    void action("import", async () => {
                      await api("/runtime/images/import", "POST", {
                        reference: existingImage.trim(),
                      });
                      await refresh();
                      setNotice("镜像检查通过，可在记录中应用。");
                    })
                  }
                >
                  {pending === "import" ? "检查中…" : "检查并添加"}
                </button>
              </div>
            </details>
          )}

          <section className="panel image-builds" aria-label="镜像记录">
            <div className="panel-head">
              <div>
                <h2>构建任务</h2>
                <p>保留构建快照和日志；已发布版本请到仓库镜像中查看与分发。</p>
              </div>
              <button
                disabled={!!pending}
                onClick={() =>
                  void action("refresh", async () => {
                    await refresh();
                  })
                }
              >
                刷新记录
              </button>
            </div>
            {view.builds.length === 0 && (
              <p className="image-empty">
                还没有镜像记录。选择 Agent 后开始构建，或添加已有镜像。
              </p>
            )}
            {view.builds.map((build) => (
              <article
                className="image-build-row"
                key={build.id}
                aria-label={`镜像 ${build.tag}`}
              >
                <div className="image-build-main">
                  <code>{build.reference || build.tag}</code>
                  <p>
                    {new Date(build.createdAt).toLocaleString()} ·{" "}
                    {build.source === "existing"
                      ? "已有镜像"
                      : build.config.agents.join(" / ")}
                  </p>
                  {build.error && (
                    <p className="image-build-error">{build.error}</p>
                  )}
                  {build.source === "build" && (
                    <details>
                      <summary>查看此版本配置</summary>
                      <pre>{JSON.stringify(build.config, null, 2)}</pre>
                    </details>
                  )}
                </div>
                <span
                  className={`status ${build.status === "succeeded" ? "completed" : "paused"}`}
                >
                  {view.active?.buildId === build.id
                    ? "当前使用"
                    : view.scope === "registry" && build.status === "succeeded"
                      ? "已发布"
                    : statuses[build.status] || build.status}
                </span>
                <div className="image-actions">
                  {build.source === "build" && (
                    <button
                      onClick={() =>
                        setLogID(logID === build.id ? "" : build.id)
                      }
                    >
                      {logID === build.id ? "收起日志" : "查看日志"}
                    </button>
                  )}
                  {running(build) && (
                    <button
                      disabled={!!pending || build.status === "cancelling"}
                      onClick={() =>
                        void action("cancel", async () => {
                          await api(
                            `/runtime/images/builds/${build.id}/cancel`,
                            "POST",
                          );
                          await refresh();
                        })
                      }
                    >
                      取消构建
                    </button>
                  )}
              {view.scope !== "registry" && build.status === "succeeded" && build.reference && (
                    <div>
                      <p>分发目标节点</p>
                      {nodes.map((n) => (
                        <label className="environment-check" key={n.node.id}>
                          <input
                            type="checkbox"
                            checked={targets.includes(n.node.id)}
                            onChange={(e) =>
                              setTargets(
                                e.target.checked
                                  ? [...targets, n.node.id]
                                  : targets.filter((id) => id !== n.node.id),
                              )
                            }
                          />
                          {n.config?.name || n.node.id}
                        </label>
                      ))}
                      <button
                        disabled={!!pending || !targets.length}
                        onClick={() =>
                          void action("distribute", async () => {
                            const latest = await api<NodeView[]>("/nodes");
                            const failed: string[] = [];
                            for (const id of targets) {
                              const n = latest.find((n) => n.node.id === id);
                              try {
                                if (!n?.config) throw new Error("节点尚未配置");
                                await api(`/nodes/${id}/config`, "PUT", {
                                  ...n.config,
                                  image: build.reference,
                                  images: [
                                    ...new Set(
                                      [
                                        ...(n.config.images || []),
                                        n.config.image,
                                      ].filter(
                                        (v) => v && v !== build.reference,
                                      ),
                                    ),
                                  ],
                                });
                              } catch (e) {
                                failed.push(`${id}: ${(e as Error).message}`);
                              }
                            }
                            setNodes(await api<NodeView[]>("/nodes"));
                            if (failed.length)
                              throw new Error(
                                `部分节点分发失败：${failed.join("；")}`,
                              );
                            setNotice(
                              "已提交分发，请在执行节点页面查看拉取与就绪状态。",
                            );
                          })
                        }
                      >
                        分发到所选节点
                      </button>
                      <a href="#/nodes">查看节点状态</a>
                    </div>
                  )}
                  {build.status === "succeeded" &&
                    !build.reference &&
                    view.scope !== "registry" && (
                      <button
                        disabled={
                          !!pending || view.active?.buildId === build.id
                        }
                        onClick={() =>
                          void action("activate", async () => {
                            await api(
                              `/runtime/images/builds/${build.id}/activate`,
                              "POST",
                            );
                            await refresh();
                            setNotice(
                              "镜像已应用。新任务和下次打开的配置终端使用此版本，已有任务不受影响。",
                            );
                          })
                        }
                      >
                        {view.active?.buildId === build.id
                          ? "已应用"
                          : "应用此镜像"}
                      </button>
                    )}
                </div>
              </article>
            ))}
            {logID && (
              <section className="image-log" aria-label="构建日志">
                <div>
                  <strong>构建日志</strong>
                  <button onClick={() => setLogID("")}>关闭日志</button>
                </div>
                {logError ? (
                  <p role="alert">{logError}</p>
                ) : (
                  <pre tabIndex={0}>{log || "等待构建输出…"}</pre>
                )}
              </section>
            )}
          </section>
        </>
      )}
    </div>
  );
}

function RegistryInventory({ builds, nodes, reloadNodes, onBuild }: {
  builds: Build[];
  nodes: NodeView[];
  reloadNodes: () => Promise<unknown>;
  onBuild: () => void;
}) {
  const [list, setList] = useState<RegistryList | null>(null);
  const [selected, setSelected] = useState("");
  const [targets, setTargets] = useState<string[]>([]);
  const [deleteTarget, setDeleteTarget] = useState<RegistryImage | null>(null);
  const [usage, setUsage] = useState<string[]>([]);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const refresh = useCallback(async () => {
    const result = await api<RegistryList>("/registry/images");
    setList(result);
    await reloadNodes();
  }, [reloadNodes]);
  useEffect(() => { void refresh().catch((e) => setError(e.message)); }, [refresh]);
  async function run(name: string, work: () => Promise<void>) {
    setBusy(name); setError(""); setNotice("");
    try { await work(); } catch (e) { setError((e as Error).message); }
    finally { setBusy(""); }
  }
  async function distribute(image: RegistryImage) {
    const latest = await api<NodeView[]>("/nodes");
    const failures: string[] = [];
    for (const id of targets) {
      const node = latest.find((n) => n.node.id === id);
      try {
        if (!node?.config) throw new Error("节点尚未配置");
        await api(`/nodes/${encodeURIComponent(id)}/config`, "PUT", {
          ...node.config,
          image: image.reference,
          images: [...new Set([...(node.config.images || []), node.config.image].filter((v) => v && v !== image.reference))],
        });
      } catch (e) { failures.push(`${id}: ${(e as Error).message}`); }
    }
    await reloadNodes();
    if (failures.length) throw new Error(`部分节点分发失败：${failures.join("；")}`);
    setNotice("已提交分发；节点会拉取并验证镜像，准备完成后才会用于新任务。");
    setSelected(""); setTargets([]);
  }
  const nodeStatus = (node: NodeView, image: RegistryImage) => {
    const isDefault = node.config?.image === image.reference;
    const isExtra = node.config?.images?.includes(image.reference);
    if (!isDefault && !isExtra) return "未配置";
    const ready = node.node.readiness.images?.find((entry) => entry.reference === image.reference);
    const status = ready?.status || (node.node.readiness.image === image.reference ? node.node.readiness.status : "");
    return `${isDefault ? "默认镜像" : "预加载"} · ${status === "ready" ? "就绪" : status === "failed" ? "失败" : status === "pulling" ? "准备中" : "待同步"}`;
  };
  return <section className="panel image-inventory" aria-label="仓库镜像">
    <div className="panel-head">
      <div><h2>仓库镜像</h2><p>{list?.repository || "配置仓库路径后查看可分发的镜像版本"}</p></div>
      <button disabled={!!busy} onClick={() => void run("refresh", refresh)}>刷新清单</button>
    </div>
    {error && <p className="error" role="alert">{error}</p>}
    {notice && <p className="notice" role="status">{notice}</p>}
    {!list && !error && <p className="image-empty">正在读取仓库镜像…</p>}
    {list && !list.repository && <p className="image-empty">尚未配置镜像仓库。请先在“仓库设置”保存地址与凭据。</p>}
    {list?.repository && list.images.length === 0 && <div className="image-empty">仓库中还没有镜像版本。<button onClick={onBuild}>前往镜像构建</button></div>}
    {list?.images.map((image) => {
      const build = builds.find((entry) => entry.reference === image.reference && entry.status === "succeeded");
      const configured = nodes.filter((node) => node.config?.image === image.reference || node.config?.images?.includes(image.reference));
      return <article className="image-build-row" key={image.digest} aria-label={`仓库镜像 ${image.digest}`}>
        <div className="image-build-main">
          <div className="image-tags">{image.tags.map((tag) => <span key={tag}>{tag}</span>)}</div>
          <code>{image.reference}</code>
          <p>{build ? `平台构建 · ${new Date(build.createdAt).toLocaleString()}` : "外部推送"} · {configured.length} 个节点已配置</p>
          {configured.length > 0 && <details><summary>节点使用情况</summary>{configured.map((node) => <p key={node.node.id}>{node.config?.name || node.node.id}：{nodeStatus(node, image)}</p>)}</details>}
          {selected === image.digest && <div className="image-distribute">
            <strong>选择分发节点</strong>
            {nodes.length === 0 && <p>还没有执行节点。</p>}
            {nodes.map((node) => <label className="environment-check" key={node.node.id}>
              <input type="checkbox" checked={targets.includes(node.node.id)} disabled={!node.config || !!busy} onChange={(event) => setTargets(event.target.checked ? [...targets, node.node.id] : targets.filter((id) => id !== node.node.id))} />
              {node.config?.name || node.node.id} <small>{nodeStatus(node, image)}</small>
            </label>)}
            <p className="image-help">旧默认版本会保留在节点预加载列表；如需删除，请先在节点配置中移除引用。</p>
            <button className="primary" disabled={!!busy || targets.length === 0} onClick={() => void run("distribute", () => distribute(image))}>分发到所选节点</button>
          </div>}
        </div>
        <div className="image-actions">
          <button disabled={!!busy} onClick={() => { setSelected(selected === image.digest ? "" : image.digest); setTargets([]); }}>{selected === image.digest ? "收起分发" : "分发 / 回退"}</button>
          <button disabled={!!busy} onClick={() => void run("usage", async () => {
            const result = await api<{ references: string[] }>(`/registry/images/${encodeURIComponent(image.digest)}/usage`);
            setUsage(result.references); setDeleteTarget(image);
          })}>删除版本</button>
        </div>
      </article>;
    })}
    {deleteTarget && <div className="image-delete-confirm" role="dialog" aria-modal="true" aria-label="确认删除镜像版本" onKeyDown={(event) => { if (event.key === "Escape") setDeleteTarget(null); }}>
      <div className="panel">
        <h2>删除镜像版本</h2>
        <p>将从仓库删除摘要 <code>{deleteTarget.digest}</code> 及其所有标签：{deleteTarget.tags.join("、")}。删除后无法从仓库拉取；磁盘空间需在维护时单独回收。</p>
        {usage.length > 0 && <p className="image-build-error">平台仍在使用：{usage.join("、")}。请先移除这些引用。</p>}
        {error && <p className="error" role="alert">{error}</p>}
        <div className="image-actions">
          <button autoFocus onClick={() => setDeleteTarget(null)}>取消</button>
          <button className="primary" disabled={!!busy || usage.length > 0} onClick={() => void run("delete", async () => {
            await api(`/registry/images/${encodeURIComponent(deleteTarget.digest)}`, "DELETE", { expectedTags: deleteTarget.tags });
            setDeleteTarget(null); setSelected(""); await refresh();
            setNotice("镜像版本已从仓库删除；磁盘空间将在维护时回收。");
          })}>确认删除</button>
        </div>
      </div>
    </div>}
  </section>;
}
