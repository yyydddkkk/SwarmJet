import { useCallback, useEffect, useRef, useState } from "react";
import MihomoSettings from "./MihomoSettings";
import { api } from "./api";
import { ConfirmDialog, ErrorBox, Field } from "./ui";
import type { NetworkProfile } from "./NetworkSelect";
import "./settings-page.css";
import "./network-settings.css";
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
  const profileRef = useRef<HTMLDialogElement>(null);
  const mihomoRef = useRef<HTMLDialogElement>(null);
  const editorGeneration = useRef(0);
  const [profiles, setProfiles] = useState<NetworkProfile[]>([]),
    [defaultId, setDefault] = useState(""),
    [draft, setDraft] = useState<NetworkProfile | null>(null),
    [baseline, setBaseline] = useState<NetworkProfile | null>(null),
    [password, setPassword] = useState(""),
    [pageError, setPageError] = useState(""),
    [profileError, setProfileError] = useState(""),
    [pageNotice, setPageNotice] = useState(""),
    [profileNotice, setProfileNotice] = useState(""),
    [confirmDiscard, setConfirmDiscard] = useState(false),
    [confirmDelete, setConfirmDelete] = useState(false),
    [showMihomo, setShowMihomo] = useState(false),
    [mihomoOpened, setMihomoOpened] = useState(false),
    [mihomoStatus, setMihomoStatus] = useState<{
      enabled: boolean;
      running?: boolean;
      active?: string;
    } | null>(null),
    [busy, setBusy] = useState(false);
  const load = useCallback(() =>
    api<{ profiles: NetworkProfile[]; defaultId: string }>("/networks").then(
      (v) => {
        setProfiles(v.profiles);
        setDefault(v.defaultId);
      },
    ), []);
  const updateMihomoStatus = useCallback((status: {
    enabled: boolean;
    running?: boolean;
    active?: string;
  }) => setMihomoStatus(status), []);
  useEffect(() => {
    void load().catch((e) => setPageError(e.message));
    void api<{ enabled: boolean; running?: boolean; active?: string }>("/mihomo/status")
      .then(updateMihomoStatus)
      .catch(() => {});
  }, [load, updateMihomoStatus]);
  useEffect(() => {
    const dialog = profileRef.current;
    if (draft && !dialog?.open) dialog?.showModal();
    if (!draft && dialog?.open) dialog.close();
  }, [draft]);
  useEffect(() => {
    const dialog = mihomoRef.current;
    if (showMihomo && !dialog?.open) dialog?.showModal();
    if (!showMihomo && dialog?.open) dialog.close();
  }, [showMihomo]);
  const dirty = !!draft && !!baseline &&
    (JSON.stringify(draft) !== JSON.stringify(baseline) || !!password);
  function openProfile(profile: NetworkProfile) {
    editorGeneration.current += 1;
    setDraft({ ...profile });
    setBaseline({ ...profile });
    setPassword("");
    setProfileError("");
    setProfileNotice("");
    setConfirmDiscard(false);
    setConfirmDelete(false);
  }
  function closeProfile() {
    if (dirty) {
      setConfirmDiscard(true);
      return;
    }
    profileRef.current?.close();
  }
  function resetProfile() {
    editorGeneration.current += 1;
    setDraft(null);
    setBaseline(null);
    setPassword("");
    setProfileError("");
    setProfileNotice("");
    setConfirmDiscard(false);
    setConfirmDelete(false);
  }
  async function save() {
    if (!draft) return;
    const generation = editorGeneration.current;
    setBusy(true);
    setProfileError("");
    setProfileNotice("");
    try {
      const p = await api<NetworkProfile>("/networks", "PUT", {
        ...draft,
        ...(password ? { password } : {}),
      });
      if (editorGeneration.current === generation) {
        setDraft(p);
        setBaseline(p);
        setPassword("");
        setConfirmDiscard(false);
      }
      await load();
      if (editorGeneration.current === generation)
        setProfileNotice("已保存，新任务使用新版本。");
    } catch (e) {
      if (editorGeneration.current === generation)
        setProfileError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function remove() {
    if (!draft?.version) return;
    const generation = editorGeneration.current;
    setBusy(true);
    setProfileError("");
    try {
      await api<void>(`/networks?id=${encodeURIComponent(draft.id)}&version=${draft.version}`, "DELETE");
      await load();
      if (editorGeneration.current === generation) {
        setConfirmDelete(false);
        profileRef.current?.close();
        setPageNotice("网络配置已删除。");
      }
    } catch (e) {
      if (editorGeneration.current === generation)
        setProfileError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const mihomoLabel = !mihomoStatus
    ? "状态待查看"
    : !mihomoStatus.enabled
      ? "未启用"
      : mihomoStatus.running
        ? "运行中"
        : mihomoStatus.active
          ? "内核未就绪"
          : "尚未应用配置";
  return (
    <div className="settings-page network-settings">
      <div className="page-head">
        <div>
          <h1>网络配置</h1>
          <p>统一维护代理，由构建服务和执行节点按需选择。</p>
        </div>
        <button className="primary" onClick={() => openProfile(blank())}>新增配置</button>
      </div>
      <ErrorBox error={pageError} />
      {pageNotice && <p role="status">{pageNotice}</p>}
      <section className="panel network-default" aria-labelledby="network-default-heading">
        <div className="panel-body">
          <div>
            <h2 id="network-default-heading">平台默认出口</h2>
            <p>选择“使用平台默认”的节点和构建服务会使用此配置。更改只影响后续任务。</p>
          </div>
          <Field label="默认网络">
            <select
              value={defaultId}
              onChange={async (e) => {
                try {
                  setPageError("");
                  await api("/networks/default", "PUT", { id: e.target.value });
                  await load();
                  setPageNotice("平台默认已更新，后续任务将使用新配置。");
                } catch (err) {
                  setPageError((err as Error).message);
                }
              }}
            >
              <option value="">直连</option>
              {profiles.filter((p) => !p.revoked).map((p) => (
                <option key={p.id} value={p.id}>{p.name}</option>
              ))}
            </select>
          </Field>
        </div>
      </section>
      <section className="panel network-list" aria-labelledby="network-list-heading">
        <div className="panel-body">
          <div className="network-section-head">
            <h2 id="network-list-heading">已保存配置</h2>
            <span>{profiles.length} 份</span>
          </div>
          {profiles.length === 0 ? (
            <p className="network-empty">尚无配置。使用“新增配置”添加代理；平台当前默认直连。</p>
          ) : (
            <div className="network-profiles">
              {profiles.map((p) => (
                <button className="network-profile" key={p.id} onClick={() => openProfile(p)}>
                  <span className="network-profile-title">
                    <strong>{p.name}</strong>
                    {defaultId === p.id && <em>默认</em>}
                  </span>
                  <span className="network-profile-meta">{p.revoked ? "已撤销" : "可用"} · 版本 {p.version}</span>
                  <small>{p.httpProxy || p.httpsProxy || "直连"}</small>
                </button>
              ))}
            </div>
          )}
        </div>
      </section>
      <section className="panel network-mihomo" aria-labelledby="network-mihomo-heading">
        <div className="panel-body">
          <div>
            <h2 id="network-mihomo-heading">Mihomo 代理服务</h2>
            <p>导入 HY2 或 YAML，校验代理链路并管理出口。</p>
          </div>
          <span className="network-mihomo-state">{mihomoLabel}</span>
          <button onClick={() => { setMihomoOpened(true); setShowMihomo(true); }}>管理服务</button>
        </div>
      </section>
      <p className="network-footnote">代理地址须从构建服务和执行节点可达。容器里的 localhost 指向容器自身；Docker 拉取镜像的代理在部署配置中管理。</p>
      <dialog
        ref={profileRef}
        className="network-profile-dialog"
        aria-labelledby="network-editor-heading"
        onCancel={(event) => { event.preventDefault(); closeProfile(); }}
        onClose={resetProfile}
      >
        {draft && (
          <>
            <div className="network-dialog-head">
              <div>
                <h2 id="network-editor-heading">{draft.version ? "编辑网络配置" : "新增网络配置"}</h2>
                <p>{draft.version ? `保存后生成版本 ${draft.version + 1}，已有任务仍使用原版本。` : "填写代理地址并保存，之后可分配给节点或构建服务。"}</p>
              </div>
              <button type="button" onClick={closeProfile} aria-label="关闭网络配置">关闭</button>
            </div>
            <div className="network-dialog-body">
              <ErrorBox error={profileError} />
              {profileNotice && <p role="status">{profileNotice}</p>}
              <div className="network-fields">
                <Field label="名称">
                  <input value={draft.name} placeholder="例如：构建出口" onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
                </Field>
                <Field label="HTTP 代理">
                  <input value={draft.httpProxy} placeholder="http://proxy.example:8080" onChange={(e) => setDraft({ ...draft, httpProxy: e.target.value })} />
                </Field>
                <Field label="HTTPS 代理">
                  <input value={draft.httpsProxy} placeholder="http://proxy.example:8080" onChange={(e) => setDraft({ ...draft, httpsProxy: e.target.value })} />
                </Field>
              </div>
              <div className="network-options">
                <h3>连接选项</h3>
                <div className="network-fields">
                  <Field label="NO_PROXY" hint="这些地址绕过代理直连。">
                    <input value={draft.noProxy} onChange={(e) => setDraft({ ...draft, noProxy: e.target.value })} />
                  </Field>
                  <Field label="代理用户名">
                    <input value={draft.username} autoComplete="username" onChange={(e) => setDraft({ ...draft, username: e.target.value })} />
                  </Field>
                  <Field label={draft.hasPassword ? "新密码（留空保留）" : "代理密码"}>
                    <input type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
                  </Field>
                </div>
              </div>
              {draft.version > 0 && (
                <label className="environment-check">
                  <input type="checkbox" checked={draft.revoked} onChange={(e) => setDraft({ ...draft, revoked: e.target.checked })} />
                  撤销配置（引用它的任务将无法继续解析代理）
                </label>
              )}
            </div>
            <div className="network-dialog-footer">
              {draft.version > 0 && <button className="network-delete" disabled={busy} onClick={() => setConfirmDelete(true)}>删除配置</button>}
              <button onClick={closeProfile}>取消</button>
              <button className="primary" disabled={busy} onClick={save}>保存配置</button>
            </div>
          </>
        )}
      </dialog>
      {confirmDiscard && (
        <ConfirmDialog
          title="放弃未保存的修改"
          confirmLabel="放弃修改"
          cancelLabel="继续编辑"
          danger
          onCancel={() => setConfirmDiscard(false)}
          onConfirm={() => { setConfirmDiscard(false); profileRef.current?.close(); }}
        >
          <p>当前网络配置有未保存的修改。放弃后，这些修改无法恢复。</p>
        </ConfirmDialog>
      )}
      {confirmDelete && draft && (
        <ConfirmDialog
          title="删除网络配置"
          confirmLabel="删除配置"
          danger
          busy={busy}
          error={profileError}
          onCancel={() => { setConfirmDelete(false); setProfileError(""); }}
          onConfirm={() => void remove()}
        >
          <p>将删除“{draft.name}”及其所有版本。引用该配置的 Node、构建或任务可能无法继续使用代理。</p>
        </ConfirmDialog>
      )}
      <dialog
        ref={mihomoRef}
        className="network-mihomo-dialog"
        aria-labelledby="mihomo-dialog-heading"
        onClose={() => setShowMihomo(false)}
      >
        <div className="network-dialog-head">
          <div>
            <h2 id="mihomo-dialog-heading">Mihomo 代理服务</h2>
            <p>导入配置、验证链路并管理平台代理入口。</p>
          </div>
          <button type="button" onClick={() => mihomoRef.current?.close()} aria-label="关闭 Mihomo 配置">关闭</button>
        </div>
        {mihomoOpened && (
          <MihomoSettings
            active={showMihomo}
            embedded
            onStatusChange={updateMihomoStatus}
            onNetworkCreated={() => { void load().catch((e) => setPageError(e.message)); }}
          />
        )}
      </dialog>
    </div>
  );
}
