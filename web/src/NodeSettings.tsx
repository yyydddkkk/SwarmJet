import { useEffect, useRef, useState } from "react";
import { api } from "./api";
import { ErrorBox, Field } from "./App";
import { NetworkSelect, type NetworkChoice } from "./NetworkSettings";
import "./image-settings.css";
type Config = {
  images?: string[];
  memoryMB?: number;
  cpus?: number;
  pidsLimit?: number;
  version: number;
  name: string;
  enabled: boolean;
  archived: boolean;
  capacity: number;
  network: NetworkChoice;
  image: string;
};
export type NodeView = {
  node: {
    id: string;
    address: string;
    capacity: number;
    readiness: {
      status?: string;
      image?: string;
      error?: string;
      architecture?: string;
      agents?: string[];
      configVersion?: number;
      images?: {
        reference: string;
        status: string;
        error?: string;
        agents?: string[];
      }[];
    };
  };
  running: number;
  online: boolean;
  config?: Config;
};
const stateNames: Record<string, string> = {
  ready: "镜像就绪",
  pulling: "正在准备镜像",
  failed: "准备失败",
  unconfigured: "待配置",
};
export default function NodeSettings() {
  const detailRef = useRef<HTMLDialogElement>(null);
  const detailGeneration = useRef(0);
  const [target, setTarget] = useState("https://registry-1.docker.io/v2/");
  const [checks, setChecks] = useState<
    { scope: string; status?: number; error?: string }[]
  >([]);
  const [nodes, setNodes] = useState<NodeView[]>([]),
    [draft, setDraft] = useState<Config | null>(null),
    [id, setID] = useState(""),
    [newID, setNewID] = useState(""),
    [secret, setSecret] = useState(""),
    [error, setError] = useState(""),
    [detailError, setDetailError] = useState(""),
    [notice, setNotice] = useState(""),
    [busy, setBusy] = useState(false);
  const load = () => api<NodeView[]>("/nodes").then(setNodes);
  useEffect(() => {
    load().catch((e) => setError(e.message));
    const t = setInterval(() => load().catch((e) => setError(e.message)), 5000);
    return () => clearInterval(t);
  }, []);
  useEffect(() => {
    const dialog = detailRef.current;
    if (draft && !dialog?.open) dialog?.showModal();
    if (!draft && dialog?.open) dialog.close();
  }, [draft]);
  function closeDetail() {
    detailGeneration.current += 1;
    setDraft(null);
    setID("");
    setChecks([]);
    setDetailError("");
    setNotice("");
  }
  function edit(n: NodeView) {
    detailGeneration.current += 1;
    setID(n.node.id);
    setDraft(
      n.config || {
        version: 0,
        name: n.node.id,
        enabled: false,
        archived: false,
        capacity: n.node.capacity,
        network: { mode: "default" },
        image: "",
      },
    );
    setChecks([]);
    setDetailError("");
    setNotice("");
  }
  async function save() {
    const generation = detailGeneration.current;
    const selectedID = id;
    setBusy(true);
    setDetailError("");
    try {
      const c = await api<Config>(
        `/nodes/${encodeURIComponent(selectedID)}/config`,
        "PUT",
        draft,
      );
      if (detailGeneration.current === generation) setDraft(c);
      await load();
      if (detailGeneration.current === generation)
        setNotice("配置已保存，等待节点同步。");
    } catch (e) {
      if (detailGeneration.current === generation)
        setDetailError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="image-settings">
      <div className="page-head">
        <div>
          <h1>执行节点</h1>
          <p>节点提供执行环境，项目可以使用多个符合条件的节点。</p>
        </div>
      </div>
      <ErrorBox error={error} />
      <details className="panel">
        <summary>添加节点</summary>
        <div className="panel-body">
          <p>
            在目标机器使用独立 DATA_DIR 和 Compose 项目部署
            Node，启动后自动注册。新节点默认不接收任务。
          </p>
          <Field label="节点 ID">
            <input
              value={newID}
              onChange={(e) => {
                setNewID(e.target.value);
                setSecret("");
              }}
            />
          </Field>
          <button
            disabled={!newID}
            onClick={async () => {
              try {
                const v = await api<{ secret: string }>(
                  `/nodes/${encodeURIComponent(newID)}/credential`,
                  "POST",
                );
                setSecret(v.secret);
              } catch (e) {
                setError((e as Error).message);
              }
            }}
          >
            创建或轮换注册凭据
          </button>
          {secret && (
            <div>
              <p>凭据仅在此显示，请放入节点部署的 NODE_SECRET。</p>
              <code className="environment-secret">{secret}</code>
              <button onClick={() => setSecret("")}>隐藏</button>
            </div>
          )}
          <p>
            部署变量：NODE_ID、NODE_SECRET、NODE_DATA_DIR、NODE_ADVERTISE_URL、CONTROL_PLANE_URL。使用
            deploy/compose.node.yaml 启动。
          </p>
        </div>
      </details>
      <section className="panel">
        <div className="panel-body">
          {nodes.length === 0 ? (
            <p>尚无节点注册。</p>
          ) : (
            nodes.map((n) => (
              <button
                className="environment-row"
                key={n.node.id}
                onClick={() => edit(n)}
              >
                <strong>{n.config?.name || n.node.id}</strong>
                <span>
                  {n.online ? "在线" : "离线"} ·{" "}
                  {n.config?.archived
                    ? "已归档"
                    : n.config?.enabled
                      ? "接收任务"
                      : "已暂停"}{" "}
                  · {n.running}/{n.config?.capacity || n.node.capacity}
                </span>
                <span>
                  {stateNames[n.node.readiness?.status || ""] || "等待能力上报"}{" "}
                  · {n.node.readiness?.architecture || "未知架构"}
                </span>
                <small>
                  {n.node.readiness?.error ||
                    n.node.readiness?.agents?.join(" / ") ||
                    "尚无 Agent 能力"}
                </small>
              </button>
            ))
          )}
        </div>
      </section>
      <dialog
        ref={detailRef}
        className="node-detail-dialog"
        aria-labelledby="node-detail-title"
        onClose={closeDetail}
      >
        {draft && (
          <>
            <div className="node-detail-head">
              <div>
                <h2 id="node-detail-title">{draft.name || id}</h2>
                <small>{id}</small>
              </div>
              <button
                type="button"
                onClick={() => detailRef.current?.close()}
                aria-label="关闭节点详情"
              >
                关闭
              </button>
            </div>
            <div className="node-detail-body">
              <ErrorBox error={detailError} />
              {notice && <p role="status">{notice}</p>}
              <details>
                <summary>镜像准备详情</summary>
                {nodes
                  .find((n) => n.node.id === id)
                  ?.node.readiness.images?.map((image) => (
                    <div key={image.reference}>
                      <code>{image.reference}</code>
                      <p>
                        {image.status === "ready" ? "就绪" : "失败"} ·{" "}
                        {image.error || image.agents?.join(" / ")}
                      </p>
                    </div>
                  ))}
              </details>
              <p>
                配置版本 {draft.version} · 节点已应用版本{" "}
                {nodes.find((n) => n.node.id === id)?.node.readiness
                  ?.configVersion || 0}
              </p>
              <div className="image-fields">
                <Field label="名称">
                  <input
                    value={draft.name}
                    onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                  />
                </Field>
                <Field label="并发任务容量">
                  <input
                    type="number"
                    min={1}
                    max={128}
                    value={draft.capacity}
                    onChange={(e) =>
                      setDraft({ ...draft, capacity: Number(e.target.value) })
                    }
                  />
                </Field>
                <Field label="每个 Agent 内存上限（MiB，0 使用 2048）">
                  <input
                    type="number"
                    min={0}
                    value={draft.memoryMB || 0}
                    onChange={(e) =>
                      setDraft({ ...draft, memoryMB: Number(e.target.value) })
                    }
                  />
                </Field>
                <Field label="每个 Agent CPU 上限（0 使用 2）">
                  <input
                    type="number"
                    min={0}
                    step={0.5}
                    value={draft.cpus || 0}
                    onChange={(e) =>
                      setDraft({ ...draft, cpus: Number(e.target.value) })
                    }
                  />
                </Field>
                <Field label="运行网络配置">
                  <NetworkSelect
                    value={draft.network}
                    onChange={(network) => setDraft({ ...draft, network })}
                  />
                </Field>
                <Field label="额外预加载镜像（每行一个仓库摘要）">
                  <textarea
                    rows={3}
                    value={(draft.images || []).join("\n")}
                    onChange={(e) =>
                      setDraft({
                        ...draft,
                        images: e.target.value.split("\n").filter(Boolean),
                      })
                    }
                  />
                </Field>
                <Field label="默认镜像（仓库摘要）">
                  <input
                    placeholder="registry.example.com/swarmjet/agent@sha256:…"
                    value={draft.image}
                    onChange={(e) =>
                      setDraft({ ...draft, image: e.target.value })
                    }
                  />
                </Field>
              </div>
              <label className="environment-check">
                <input
                  type="checkbox"
                  checked={draft.enabled}
                  disabled={draft.archived}
                  onChange={(e) =>
                    setDraft({ ...draft, enabled: e.target.checked })
                  }
                />
                接收新任务
              </label>
              <label className="environment-check">
                <input
                  type="checkbox"
                  checked={draft.archived}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      archived: e.target.checked,
                      enabled: e.target.checked ? false : draft.enabled,
                    })
                  }
                />
                归档节点
              </label>
              <div className="image-actions">
                <button className="primary" disabled={busy} onClick={save}>
                  保存节点配置
                </button>
                <a
                  className="button"
                  href={`#/runtime?node=${encodeURIComponent(id)}`}
                >
                  节点配置终端
                </a>
              </div>
              <Field label="连通性测试地址">
                <input
                  value={target}
                  onChange={(e) => setTarget(e.target.value)}
                />
              </Field>
              <button
                disabled={busy}
                onClick={async () => {
                  setBusy(true);
                  setChecks([]);
                  try {
                    const v = await api<{
                      checks: {
                        scope: string;
                        status?: number;
                        error?: string;
                      }[];
                    }>(
                      `/runtime/network-test?node=${encodeURIComponent(id)}`,
                      "POST",
                      { target },
                    );
                    setChecks(v.checks);
                  } catch (e) {
                    setDetailError((e as Error).message);
                  } finally {
                    setBusy(false);
                  }
                }}
              >
                从节点和 Agent 容器测试
              </button>
              {checks.map((c) => (
                <p key={c.scope}>
                  {c.scope === "node" ? "节点进程" : "Agent 容器"}：
                  {c.error || `已连接，HTTP ${c.status}`}
                </p>
              ))}
              <p>
                停用不影响已有任务；新镜像准备完成后才参与调度。镜像拉取由该节点的
                Docker 网络配置决定。
              </p>
            </div>
          </>
        )}
      </dialog>
    </div>
  );
}
