import { useEffect, useState } from "react";
import MihomoSettings from "./MihomoSettings";
import { api } from "./api";
import { ErrorBox, Field } from "./App";
import "./image-settings.css";
import "./network-settings.css";
export type NetworkChoice = { mode: string; id?: string };
export type NetworkProfile = {
  id: string;
  name: string;
  version: number;
  httpProxy: string;
  httpsProxy: string;
  noProxy: string;
  username: string;
  hasPassword: boolean;
  revoked: boolean;
};
export function NetworkSelect({
  value,
  onChange,
}: {
  value: NetworkChoice;
  onChange: (v: NetworkChoice) => void;
}) {
  const [profiles, setProfiles] = useState<NetworkProfile[]>([]);
  const [error, setError] = useState("");
  useEffect(() => {
    api<{ profiles: NetworkProfile[] }>("/networks")
      .then((v) => setProfiles(v.profiles))
      .catch((e) => setError(e.message));
  }, []);
  return (
    <>
      <select
        aria-label="网络配置"
        value={value.mode === "profile" ? value.id : value.mode || "default"}
        onChange={(e) =>
          onChange(
            ["default", "direct"].includes(e.target.value)
              ? { mode: e.target.value }
              : { mode: "profile", id: e.target.value },
          )
        }
      >
        <option value="default">使用平台默认</option>
        <option value="direct">直连</option>
        {profiles
          .filter((p) => !p.revoked)
          .map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
      </select>
      {error && <small role="alert">{error}</small>}
    </>
  );
}
const blank = (): NetworkProfile => ({
  id: `network-${crypto.randomUUID().slice(0, 8)}`,
  name: "",
  version: 0,
  httpProxy: "",
  httpsProxy: "",
  noProxy: "localhost,127.0.0.1,::1",
  username: "",
  hasPassword: false,
  revoked: false,
});
export default function NetworkSettings() {
  const [profiles, setProfiles] = useState<NetworkProfile[]>([]),
    [defaultId, setDefault] = useState(""),
    [draft, setDraft] = useState(blank),
    [password, setPassword] = useState(""),
    [error, setError] = useState(""),
    [notice, setNotice] = useState(""),
    [showMihomo, setShowMihomo] = useState(false),
    [busy, setBusy] = useState(false);
  const load = () =>
    api<{ profiles: NetworkProfile[]; defaultId: string }>("/networks").then(
      (v) => {
        setProfiles(v.profiles);
        setDefault(v.defaultId);
      },
    );
  useEffect(() => {
    load().catch((e) => setError(e.message));
  }, []);
  async function save() {
    setBusy(true);
    setError("");
    try {
      const p = await api<NetworkProfile>("/networks", "PUT", {
        ...draft,
        ...(password ? { password } : {}),
      });
      setDraft(p);
      setPassword("");
      await load();
      setNotice("已保存，新任务使用新版本。");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="image-settings network-settings">
      <div className="page-head">
        <div>
          <h1>网络配置</h1>
          <p>统一维护代理，由构建服务和执行节点按需选择。</p>
        </div>
        <button
          onClick={() => {
            setDraft(blank());
            setPassword("");
            setNotice("");
          }}
        >
          新增配置
        </button>
      </div>
      <ErrorBox error={error} />
      {notice && <p role="status">{notice}</p>}
      <section className="panel network-default" aria-labelledby="network-default-heading">
        <div className="panel-body">
          <div>
            <h2 id="network-default-heading">平台默认出口</h2>
            <p>节点和构建服务选择“使用平台默认”时，采用这里的配置。更改只影响后续任务。</p>
          </div>
          <Field label="默认网络">
            <select
              value={defaultId}
              onChange={async (e) => {
                try {
                  setError("");
                  await api("/networks/default", "PUT", { id: e.target.value });
                  await load();
                  setNotice("平台默认已更新，后续任务将使用新配置。");
                } catch (err) {
                  setError((err as Error).message);
                }
              }}
            >
              <option value="">直连</option>
              {profiles
                .filter((p) => !p.revoked)
                .map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
            </select>
          </Field>
        </div>
      </section>
      <div className="network-workspace">
        <section className="panel network-list" aria-labelledby="network-list-heading">
          <div className="panel-body">
            <div className="network-section-head">
              <h2 id="network-list-heading">已保存配置</h2>
              <span>{profiles.length} 份</span>
            </div>
            {profiles.length === 0 ? (
              <p className="network-empty">尚无配置。填写右侧表单可添加外部代理；平台当前默认直连。</p>
            ) : (
              <div className="network-profiles">{profiles.map((p) => (
                <button
                  className={`network-profile ${draft.version && draft.id === p.id ? "selected" : ""}`}
                  key={p.id}
                  aria-pressed={!!draft.version && draft.id === p.id}
                  onClick={() => {
                    setDraft(p);
                    setPassword("");
                    setNotice("");
                  }}
                >
                  <span className="network-profile-title"><strong>{p.name}</strong>{defaultId === p.id && <em>默认</em>}</span>
                  <span className="network-profile-meta">版本 {p.version} · {p.revoked ? "已撤销" : "可用"}</span>
                  <small>{p.httpProxy || p.httpsProxy || "直连"}</small>
                </button>
              ))}</div>
            )}
          </div>
        </section>
        <section className="panel network-editor" aria-labelledby="network-editor-heading">
          <div className="panel-body">
            <div className="network-section-head">
              <div>
                <h2 id="network-editor-heading">{draft.version ? "编辑网络配置" : "新增网络配置"}</h2>
                <p>{draft.version ? `保存后生成版本 ${draft.version + 1}，已有任务仍使用原版本。` : "填写代理地址并保存，之后可分配给节点或构建服务。"}</p>
              </div>
            </div>
            <div className="network-fields">
              <Field label="名称">
                <input
                  value={draft.name}
                  placeholder="例如：构建出口"
                  onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                />
              </Field>
              <Field label="HTTP 代理"><input value={draft.httpProxy} placeholder="http://proxy.example:8080" onChange={(e) => setDraft({ ...draft, httpProxy: e.target.value })} /></Field>
              <Field label="HTTPS 代理"><input value={draft.httpsProxy} placeholder="http://proxy.example:8080" onChange={(e) => setDraft({ ...draft, httpsProxy: e.target.value })} /></Field>
            </div>
            <div className="network-options">
              <h3>连接选项</h3>
              <div className="network-fields">
                <Field label="NO_PROXY" hint="这些地址绕过代理直连。"><input value={draft.noProxy} onChange={(e) => setDraft({ ...draft, noProxy: e.target.value })} /></Field>
                <Field label="代理用户名"><input value={draft.username} autoComplete="username" onChange={(e) => setDraft({ ...draft, username: e.target.value })} /></Field>
              <Field
                label={draft.hasPassword ? "新密码（留空保留）" : "代理密码"}
              >
                <input
                  type="password"
                  autoComplete="new-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </Field>
              </div>
            </div>
            {draft.version > 0 && <label className="environment-check">
              <input
                type="checkbox"
                checked={draft.revoked}
                onChange={(e) =>
                  setDraft({ ...draft, revoked: e.target.checked })
                }
              />
              撤销配置（引用它的任务将无法继续解析代理）
            </label>}
            <div className="network-save image-actions">
              <button className="primary" disabled={busy} onClick={save}>
                保存配置
              </button>
            </div>
          </div>
        </section>
      </div>
      <p className="network-footnote">代理地址须从构建服务和执行节点可达。容器里的 localhost 指向容器自身；Docker 拉取镜像的代理在部署配置中管理。</p>
      <section className="network-mihomo">
        <button className="network-mihomo-toggle" aria-expanded={showMihomo} onClick={() => setShowMihomo(!showMihomo)}>
          <span><strong>Mihomo 代理服务</strong><small>导入 HY2 或 YAML，测试代理入口并添加到平台配置</small></span>
          <span aria-hidden="true">{showMihomo ? "收起" : "展开"}</span>
        </button>
        {showMihomo && <MihomoSettings onNetworkCreated={() => { void load().catch((e) => setError(e.message)); }} />}
      </section>
    </div>
  );
}
