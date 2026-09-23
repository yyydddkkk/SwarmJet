import { useCallback, useEffect, useState } from "react";
import { api } from "./api";
import { ErrorBox, Field } from "./App";
import "./mihomo-settings.css";

type Revision = {
  id: string;
  name: string;
  createdAt: string;
  warnings: string[];
};
type Probe = {
  revisionId: string;
  url: string;
  proxy?: string;
  ok: boolean;
  transportOk: boolean;
  status?: number;
  latencyMs: number;
  proxyLatencyMs?: number;
  error?: string;
  at: string;
};
type View = {
  enabled: boolean;
  running?: boolean;
  busy?: boolean;
  active?: string;
  previous?: string;
  revisions: Revision[];
  lastProbe?: Probe;
  error?: string;
  proxyUrl?: string;
  message?: string;
};
type Group = { name: string; type: string; now?: string; all?: string[] };
export default function MihomoSettings({
  active = true,
  embedded = false,
  onStatusChange,
  onNetworkCreated,
}: {
  active?: boolean;
  embedded?: boolean;
  onStatusChange?: (status: { enabled: boolean; running?: boolean; active?: string }) => void;
  onNetworkCreated: () => void;
}) {
  const [noProxy, setNoProxy] = useState("localhost,127.0.0.1,::1");
  const [view, setView] = useState<View | null>(null),
    [groups, setGroups] = useState<Group[]>([]),
    [selected, setSelected] = useState(""),
    [format, setFormat] = useState("hy2"),
    [name, setName] = useState(""),
    [content, setContent] = useState(""),
    [target, setTarget] = useState("https://www.gstatic.com/generate_204"),
    [proxy, setProxy] = useState(""),
    [endpoint, setEndpoint] = useState(""),
    [busy, setBusy] = useState(""),
    [error, setError] = useState(""),
    [notice, setNotice] = useState(""),
    [showImport, setShowImport] = useState(false),
    [choices, setChoices] = useState<Record<string, string>>({});
  const refresh = useCallback(async () => {
    const v = await api<View>("/mihomo/status");
    setView(v);
    onStatusChange?.(v);
    setSelected((old) => old || v.active || v.revisions[0]?.id || "");
    setEndpoint((old) => old || v.proxyUrl || "");
    return v;
  }, [onStatusChange]);
  useEffect(() => {
    if (!active) return;
    let live = true;
    const load = () =>
      refresh().catch((e) => {
        if (live) setError(e.message);
      });
    void load();
    const t = setInterval(load, 5000);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, [active, refresh]);
  useEffect(() => {
    if (!active || !view?.running) {
      setGroups([]);
      return;
    }
    const ctl = new AbortController();
    api<{ groups: Group[] }>("/mihomo/groups", "GET", undefined, ctl.signal)
      .then((v) => setGroups(v.groups))
      .catch((e) => {
        if (!ctl.signal.aborted) setError(e.message);
      });
    return () => ctl.abort();
  }, [active, view?.running, view?.active]);
  async function act(action: string, fn: () => Promise<void>) {
    setBusy(action);
    setError("");
    setNotice("");
    try {
      await fn();
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  }
  const revision = view?.revisions.find((r) => r.id === selected);
  const probe =
    view?.lastProbe?.revisionId === selected ? view.lastProbe : undefined;
  const canApply =
    !!probe?.ok &&
    Date.now() - Date.parse(probe.at) < 300000 &&
    view?.active !== selected;
  if (!view && !error)
    return (
      <section className="panel mihomo-panel">
        <p>正在读取代理服务…</p>
      </section>
    );
  return (
    <section className="panel mihomo-panel" aria-label="Mihomo 代理服务">
      <div className="mihomo-head">
        <div>
          {!embedded && <h2>Mihomo 代理服务</h2>}
          <p>导入 HY2 链接或 Clash / Mihomo YAML，测试后应用到代理入口。</p>
        </div>
        <div className="mihomo-head-actions">
          {view?.enabled && view.revisions.length > 0 && (
            <button onClick={() => setShowImport(!showImport)}>
              {showImport ? "返回版本管理" : "导入配置"}
            </button>
          )}
          <span className={`status ${view?.running ? "completed" : "paused"}`}>
            {!view?.enabled
              ? "未启用"
              : view.running
                ? "运行中"
                : view?.active
                  ? "内核未就绪"
                  : "尚未应用配置"}
          </span>
        </div>
      </div>
      <ErrorBox error={error} />
      {notice && (
        <p role="status" className="notice">
          {notice}
        </p>
      )}
      {view?.error && <p role="alert">{view.error}</p>}
      {!view?.enabled ? (
        <p>
          这是可选服务。部署时加入 <code>compose.mihomo.yaml</code>
          ，并配置管理连接后即可导入。已有外部代理仍可在下方配置。
        </p>
      ) : (
        <>
          {(showImport || view.revisions.length === 0) && (
            <div className="mihomo-import-panel">
              <div className="mihomo-import">
                <Field label="导入方式">
                  <select
                    value={format}
                    onChange={(e) => setFormat(e.target.value)}
                  >
                    <option value="hy2">HY2 链接</option>
                    <option value="yaml">YAML / 自定义配置</option>
                  </select>
                </Field>
                <Field label="配置名称">
                  <input
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder="例如：服务器 HY2 出口"
                    maxLength={200}
                  />
                </Field>
              </div>
              <Field
                label={
                  format === "hy2"
                    ? "HY2 链接（含认证信息）"
                    : "Mihomo YAML（含认证信息）"
                }
              >
                <textarea
                  spellCheck={false}
                  autoComplete="off"
                  className="mihomo-editor"
                  rows={format === "hy2" ? 3 : 13}
                  value={content}
                  onChange={(e) => setContent(e.target.value)}
                  placeholder={
                    format === "hy2"
                      ? "hy2://…"
                      : "粘贴完整 YAML，或在这里编辑自定义配置"
                  }
                />
              </Field>
              <div className="image-actions">
                <button
                  className="primary"
                  disabled={!!busy || !name.trim() || !content.trim()}
                  onClick={() =>
                    void act("import", async () => {
                      const r = await api<Revision>("/mihomo/import", "POST", {
                        name,
                        format,
                        content,
                      });
                      setSelected(r.id);
                      if (format === "hy2") setProxy("SWARMJET-PROXY");
                      else setProxy("");
                      setContent("");
                      setShowImport(false);
                      setNotice("已保存为新版本，尚未应用。请校验并运行探针。");
                    })
                  }
                >
                  {busy === "import" ? "正在导入…" : "导入为新版本"}
                </button>
                <label className="button mihomo-upload">
                  上传 YAML
                  <input
                    aria-label="上传 YAML 文件"
                    type="file"
                    accept=".yaml,.yml,text/yaml,text/plain"
                    disabled={!!busy}
                    onChange={async (e) => {
                      const file = e.target.files?.[0];
                      if (!file) return;
                      if (file.size > 1024 * 1024) {
                        setError("配置文件不能超过 1 MiB");
                        return;
                      }
                      setFormat("yaml");
                      setContent(await file.text());
                      if (!name) setName(file.name);
                      e.target.value = "";
                    }}
                  />
                </label>
              </div>
              <p className="mihomo-help">
                监听端口和控制接口由平台管理，不启用 TUN。草稿与活动配置分开保存。
              </p>
            </div>
          )}
          <div className="mihomo-workspace">
            <div>
              <h3>配置版本</h3>
              {view.revisions.length === 0 ? (
                <p>还没有导入配置。</p>
              ) : (
                <div className="mihomo-revisions">
                  {view.revisions.map((r) => (
                    <button
                      key={r.id}
                      className={selected === r.id ? "selected" : ""}
                      onClick={() => setSelected(r.id)}
                      aria-pressed={selected === r.id}
                    >
                      <strong>{r.name}</strong>
                      <span>
                        {r.id === view.active
                          ? "当前活动"
                          : r.id === view.previous
                            ? "上一版本"
                            : "草稿 / 历史版本"}
                      </span>
                      <small>{new Date(r.createdAt).toLocaleString()}</small>
                    </button>
                  ))}
                </div>
              )}
            </div>
            <div>
              <h3>校验与可用性探针</h3>
              {revision ? (
                <>
                  <p>
                    选中：<strong>{revision.name}</strong>
                  </p>
                  {revision.warnings.length > 0 && (
                    <details>
                      <summary>导入调整（{revision.warnings.length}）</summary>
                      <ul>
                        {revision.warnings.map((w, i) => (
                          <li key={i}>{w}</li>
                        ))}
                      </ul>
                    </details>
                  )}
                  <div className="image-actions">
                    <button
                      disabled={!!busy}
                      onClick={() =>
                        void act("validate", async () => {
                          await api("/mihomo/validate", "POST", {
                            id: selected,
                          });
                          setNotice(
                            "内核配置校验通过。连接是否可用请继续运行探针。",
                          );
                        })
                      }
                    >
                      {busy === "validate" ? "正在校验…" : "校验配置"}
                    </button>
                    <button
                      disabled={!!busy}
                      onClick={() =>
                        void act("edit", async () => {
                          const v = await api<{ yaml: string }>(
                            `/mihomo/revisions/${selected}`,
                          );
                          setFormat("yaml");
                          setContent(v.yaml);
                          setName(revision.name + "（修改）");
                          setShowImport(true);
                          setNotice("配置已载入编辑器，保存会创建新版本。");
                        })
                      }
                    >
                      载入编辑器（含密钥）
                    </button>
                  </div>
                  <Field label="代理探针目标 URL">
                    <input
                      value={target}
                      onChange={(e) => setTarget(e.target.value)}
                    />
                  </Field>
                  <Field label="额外测试的节点或策略组（可选）">
                    <input
                      value={proxy}
                      onChange={(e) => setProxy(e.target.value)}
                      list="mihomo-probe-proxies"
                      placeholder="例如 SWARMJET-PROXY 或 YAML 中的节点名"
                    />
                    <datalist id="mihomo-probe-proxies">
                      {groups.map((g) => (
                        <option key={g.name} value={g.name} />
                      ))}
                    </datalist>
                  </Field>
                  <p className="mihomo-help">
                    探针通过候选内核的真实 HTTP
                    入口请求目标，不使用环境变量中的直连绕过。填写节点名可额外验证该出口，避免规则走直连掩盖节点故障。
                  </p>
                  <button
                    disabled={!!busy || !target}
                    onClick={() =>
                      void act("probe", async () => {
                        await api("/mihomo/probe", "POST", {
                          id: selected,
                          url: target,
                          proxy,
                        });
                      })
                    }
                  >
                    {busy === "probe" ? "正在测试代理…" : "运行可用性探针"}
                  </button>
                  {probe && (
                    <div
                      className={`mihomo-probe ${probe.ok ? "passed" : "failed"}`}
                      role="status"
                    >
                      <strong>{probe.ok ? "探针通过" : "探针未通过"}</strong>
                      <p>
                        {probe.error ||
                          `HTTP ${probe.status} · 代理链路 ${probe.latencyMs} ms`}
                      </p>
                      {probe.proxyLatencyMs !== undefined && (
                        <p>
                          指定出口 {probe.proxy}：{probe.proxyLatencyMs} ms
                        </p>
                      )}
                      <small>
                        {new Date(probe.at).toLocaleString()} · 通过结果 5
                        分钟内可应用
                      </small>
                    </div>
                  )}
                  <div className="image-actions">
                    <button
                      className="primary"
                      disabled={!!busy || !canApply}
                      onClick={() =>
                        void act("apply", async () => {
                          await api("/mihomo/apply", "POST", { id: selected });
                          setNotice("已应用到代理入口。");
                        })
                      }
                    >
                      应用这个版本
                    </button>
                    {view.previous && (
                      <button
                        disabled={!!busy}
                        onClick={() =>
                          void act("rollback", async () => {
                            await api("/mihomo/rollback", "POST", {
                              url: target,
                              proxy: "",
                            });
                            setNotice("探针通过，已回滚到上一版本。");
                          })
                        }
                      >
                        测试并回滚上一版本
                      </button>
                    )}
                  </div>
                  <p className="mihomo-help">
                    应用或切换出口会影响所有引用这个代理服务的环境，包括正在运行的任务。
                  </p>
                </>
              ) : (
                <p>先导入并选择一个配置版本。</p>
              )}
            </div>
          </div>
          {view.running && (
            <>
              <details className="mihomo-section">
                <summary>手动策略组</summary>
                {groups
                  .filter((g) => g.type === "Selector")
                  .map((g) => (
                    <div className="mihomo-group" key={g.name}>
                      <Field label={`${g.name}（当前：${g.now || "未选择"}）`}>
                        <select
                          value={choices[g.name] || g.now || ""}
                          onChange={(e) =>
                            setChoices({ ...choices, [g.name]: e.target.value })
                          }
                        >
                          {g.all?.map((n) => (
                            <option key={n} value={n}>
                              {n}
                            </option>
                          ))}
                        </select>
                      </Field>
                      <button
                        disabled={
                          !!busy ||
                          !choices[g.name] ||
                          choices[g.name] === g.now
                        }
                        onClick={() =>
                          void act("select", async () => {
                            await api("/mihomo/select", "PUT", {
                              group: g.name,
                              name: choices[g.name],
                            });
                            setGroups(
                              (await api<{ groups: Group[] }>("/mihomo/groups"))
                                .groups,
                            );
                            setNotice(
                              "出口选择已保存。可再次运行探针检查链路。",
                            );
                          })
                        }
                      >
                        切换出口
                      </button>
                    </div>
                  ))}
              </details>
              <details className="mihomo-section">
                <summary>添加到平台网络配置</summary>
                <p>
                  创建后可在 BuildKit 和 Node 中选择；不会自动改动平台默认代理。
                </p>
                <Field label="构建服务与节点可达的代理入口">
                  <input
                    value={endpoint}
                    onChange={(e) => setEndpoint(e.target.value)}
                    placeholder="http://服务器内网 IP:17890"
                  />
                </Field>
                <Field label="直连地址（NO_PROXY）">
                  <input
                    value={noProxy}
                    onChange={(e) => setNoProxy(e.target.value)}
                  />
                </Field>
                <p className="mihomo-help">
                  容器和远程节点通常不能使用
                  127.0.0.1。请使用可达的服务器地址，并从节点页面测试连接。
                </p>
                <button
                  disabled={!!busy || !endpoint}
                  onClick={() =>
                    void act("network", async () => {
                      await api("/mihomo/network", "POST", {
                        name: "Mihomo 代理",
                        endpoint,
                        noProxy,
                      });
                      onNetworkCreated();
                      setNotice(
                        "网络配置已创建，入口认证已安全保存；请在对应节点或构建配置中选择。",
                      );
                    })
                  }
                >
                  创建网络配置
                </button>
              </details>
            </>
          )}
        </>
      )}
    </section>
  );
}
