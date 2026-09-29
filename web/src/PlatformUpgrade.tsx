import { useCallback, useEffect, useState } from "react";
import { api } from "./api";
import { ConfirmDialog, ErrorBox, Field } from "./ui";
import { useToast } from "./ToastProvider";
import "./platform-upgrade.css";

type Release = { tag: string; reference?: string; digest?: string };
type Current = { tag?: string; version?: string; reference?: string; digest?: string };
type ReleaseList = {
  current: Current;
  releases: Release[];
  catalogError?: string;
  nodes?: { id: string; online: boolean; platformImage?: string; version?: string }[];
};
type Preflight = { id: string; label: string; status: "passed" | "warning" | "blocked"; message: string };
type UpgradeNode = { id: string; status: string; currentImage?: string; targetImage?: string; error?: string };
type Upgrade = {
  id: string;
  tag: string;
  status: string;
  stage?: string;
  target?: Release;
  preflight?: Preflight[];
  nodes?: UpgradeNode[];
  error?: string;
  needsRestore?: boolean;
  backupDir?: string;
  createdAt?: string;
  startedAt?: string;
  updatedAt?: string;
};
type UpgradeList = { upgrades: Upgrade[] };

const statuses: Record<string, string> = {
  preparing: "正在准备",
  ready: "等待启动",
  running: "升级中",
  failed: "升级失败",
  succeeded: "升级完成",
  cancelled: "已取消",
  pending: "等待处理",
  passed: "通过",
  warning: "需留意",
  blocked: "未通过",
};
const stages: Record<string, string> = {
  building: "构建平台镜像",
  draining: "等待任务排空",
  upgrading_control: "升级控制面",
  upgrading_nodes: "升级节点",
};
const activeStatuses = new Set(["preparing", "running"]);
const label = (status: string) => statuses[status] || status;
const upgradeLabel = (upgrade: Upgrade) => {
  const stage = upgrade.stage && stages[upgrade.stage];
  return stage && ["preparing", "running", "failed"].includes(upgrade.status) ? `${label(upgrade.status)} · ${stage}` : label(upgrade.status);
};
const shortRef = (value?: string) => value || "尚未上报";

export default function PlatformUpgrade() {
  const toast = useToast();
  const [releases, setReleases] = useState<ReleaseList | null>(null);
  const [history, setHistory] = useState<Upgrade[]>([]);
  const [upgrade, setUpgrade] = useState<Upgrade | null>(null);
  const [selectedTag, setSelectedTag] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [confirmStart, setConfirmStart] = useState(false);
  const [confirmAbort, setConfirmAbort] = useState(false);

  const load = useCallback(async () => {
    const [available, records] = await Promise.allSettled([
      api<ReleaseList>("/system/upgrades/releases"),
      api<UpgradeList>("/system/upgrades"),
    ]);
    if (available.status === "fulfilled") {
      setReleases(available.value);
      setSelectedTag((previous) => previous || available.value.releases?.[0]?.tag || "");
    }
    if (records.status === "fulfilled") {
      setHistory(records.value.upgrades || []);
      const latest = records.value.upgrades?.[0];
      if (latest) {
        const detail = await api<Upgrade>(`/system/upgrades/${encodeURIComponent(latest.id)}`);
        setUpgrade(detail);
      } else {
        setUpgrade(null);
      }
    }
    const failures = [available, records].flatMap((result) => result.status === "rejected" ? [(result.reason as Error).message] : []);
    if (failures.length) throw new Error(failures.join("；"));
    setError("");
  }, []);

  useEffect(() => {
    void load().catch((cause) => setError((cause as Error).message)).finally(() => setLoading(false));
  }, [load]);

  useEffect(() => {
    if (!upgrade || !activeStatuses.has(upgrade.status)) return;
    const timer = window.setInterval(() => {
      void api<Upgrade>(`/system/upgrades/${encodeURIComponent(upgrade.id)}`)
        .then((detail) => {
          setUpgrade(detail);
          if (!activeStatuses.has(detail.status)) void load().catch((cause) => setError((cause as Error).message));
        })
        .catch((cause) => setError((cause as Error).message));
    }, 3000);
    return () => window.clearInterval(timer);
  }, [upgrade?.id, upgrade?.status, load]);

  async function act(name: string, work: () => Promise<Upgrade>, success: string) {
    setBusy(name);
    setError("");
    try {
      const result = await work();
      setUpgrade(result);
      toast.success(success);
      if (!activeStatuses.has(result.status)) await load();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy("");
    }
  }

  const selected = releases?.releases.find((release) => release.tag === selectedTag);
  const preflightBlocked = upgrade?.preflight?.some((check) => check.status === "blocked") || false;
  const canStart = upgrade?.status === "ready" && !preflightBlocked;
  const canResume = upgrade?.status === "failed" && !!upgrade.startedAt && !upgrade.needsRestore;
  const canCancel = !!upgrade && !upgrade.startedAt && ["preparing", "ready", "failed"].includes(upgrade.status);
  const canAbort = upgrade?.status === "running" && upgrade.stage === "draining" && !!upgrade.startedAt;

  return <div className="platform-upgrade">
    <div className="system-section-intro">
      <h2>平台升级</h2>
      <p>选择版本后先准备并检查，再启动升级。进度和节点结果会持续保存在平台中。</p>
    </div>
    <ErrorBox error={error} retry={() => void load().catch((cause) => setError((cause as Error).message))} />
    {releases?.catalogError && <p className="platform-upgrade-failure" role="alert">无法读取候选版本：{releases.catalogError}</p>}
    <section className="panel" aria-labelledby="upgrade-release-heading">
      <div className="panel-head"><div><h2 id="upgrade-release-heading">版本</h2><p>当前版本与可选候选版本。</p></div><button disabled={!!busy || loading} onClick={() => void load().catch((cause) => setError((cause as Error).message))}>刷新版本</button></div>
      <div className="platform-upgrade-body">
        <div className="platform-upgrade-current"><span>当前控制面</span><strong>{releases?.current?.tag || releases?.current?.version || releases?.current?.reference || (loading ? "正在读取…" : "尚未上报")}</strong><code>{releases?.current?.digest || releases?.current?.reference || "镜像摘要尚未上报"}</code></div>
        {releases?.nodes && <div className="platform-upgrade-current"><span>当前节点</span><strong>{releases.nodes.filter((node) => node.online).length} / {releases.nodes.length} 在线</strong></div>}
        {!!releases?.nodes?.length && <div className="platform-upgrade-rows">{releases.nodes.map((node) => <div className="platform-upgrade-node" key={node.id}><strong>{node.id}</strong><span className={`platform-upgrade-status ${node.online ? "succeeded" : "failed"}`}>{node.online ? "在线" : "离线"}</span><small>程序镜像：<code>{shortRef(node.platformImage)}</code>{node.version && <> · {node.version}</>}</small></div>)}</div>}
        <div className="platform-upgrade-choice">
          <Field label="候选版本"><select value={selectedTag} disabled={!!busy || loading} onChange={(event) => setSelectedTag(event.target.value)}>
            {!(releases?.releases.length) && <option value="">暂无候选版本</option>}
            {releases?.releases.map((release) => <option key={release.tag} value={release.tag}>{release.tag}{release.digest ? ` · ${release.digest.slice(0, 19)}…` : " · 待构建"}</option>)}
          </select></Field>
          {selected && <code>{selected.reference || "准备时构建并发布平台镜像"}</code>}
          <button className="primary" disabled={!!busy || !selected || !!releases?.catalogError || !!upgrade && !["succeeded", "cancelled"].includes(upgrade.status)} onClick={() => void act("prepare", () => api<Upgrade>("/system/upgrades/prepare", "POST", { tag: selectedTag }), "已提交准备与预检，正在读取结果。")}>{busy === "prepare" ? "正在提交…" : "准备并预检"}</button>
        </div>
      </div>
    </section>
    <section className="panel" aria-labelledby="upgrade-progress-heading">
      <div className="panel-head"><div><h2 id="upgrade-progress-heading">升级进度</h2><p>{upgrade ? `目标 ${upgrade.tag} · 记录 ${upgrade.id}` : "准备候选版本后显示预检和逐节点进度。"}</p></div>{upgrade && <span className={`platform-upgrade-status ${upgrade.status}`} role="status">{upgradeLabel(upgrade)}</span>}</div>
      {!upgrade ? <p className="platform-upgrade-empty">{loading ? "正在读取升级记录…" : "尚无升级记录。"}</p> : <div className="platform-upgrade-body">
        {upgrade.error && <p className="platform-upgrade-failure" role="alert">{upgrade.error}</p>}
        {upgrade.needsRestore && <div className="platform-upgrade-failure" role="alert"><strong>数据迁移后升级失败，维护状态仍生效。</strong><p>请在控制面宿主机按部署恢复流程核对并恢复备份；恢复前不要启动旧版程序。</p>{upgrade.backupDir && <p>备份目录：<code>{upgrade.backupDir}</code></p>}</div>}
        <div className="platform-upgrade-target"><span>目标镜像</span><code>{shortRef(upgrade.target?.reference)}</code></div>
        <div className="platform-upgrade-actions">
          {canStart && <button className="primary" disabled={!!busy} onClick={() => setConfirmStart(true)}>启动升级</button>}
          {canResume && <button className="primary" disabled={!!busy} onClick={() => void act("resume", () => api<Upgrade>(`/system/upgrades/${encodeURIComponent(upgrade.id)}/resume`, "POST"), "已提交恢复，正在更新进度。")}>{busy === "resume" ? "正在恢复…" : "重试失败步骤"}</button>}
          {canCancel && <button disabled={!!busy} onClick={() => void act("cancel", () => api<Upgrade>(`/system/upgrades/${encodeURIComponent(upgrade.id)}/cancel`, "POST"), "准备记录已取消，可重新选择版本。")}>{busy === "cancel" ? "正在取消…" : "取消准备"}</button>}
          {canAbort && <button disabled={!!busy} onClick={() => setConfirmAbort(true)}>取消升级并解除维护</button>}
          {activeStatuses.has(upgrade.status) && <span>每 3 秒更新进度</span>}
        </div>
        <h3>预检结果</h3>
        {upgrade.preflight?.length ? <div className="platform-upgrade-rows">{upgrade.preflight.map((check) => <div className="platform-upgrade-row" key={check.id}><strong>{check.label}</strong><span className={`platform-upgrade-status ${check.status}`}>{label(check.status)}</span><p>{check.message}</p></div>)}</div> : <p className="platform-upgrade-empty">{upgrade.status === "preparing" ? "正在执行预检…" : "暂无预检结果。"}</p>}
        <h3>节点状态</h3>
        {upgrade.nodes?.length ? <div className="platform-upgrade-rows">{upgrade.nodes.map((node) => <div className="platform-upgrade-node" key={node.id}><strong>{node.id}</strong><span className={`platform-upgrade-status ${node.status}`}>{label(node.status)}</span><small>当前：<code>{shortRef(node.currentImage)}</code></small><small>目标：<code>{shortRef(node.targetImage)}</code></small>{node.error && <p className="platform-upgrade-failure">{node.error}</p>}</div>)}</div> : <p className="platform-upgrade-empty">暂无节点状态。</p>}
      </div>}
    </section>
    {history.length > 1 && <section className="panel" aria-labelledby="upgrade-history-heading"><div className="panel-head"><h2 id="upgrade-history-heading">历史记录</h2></div><div className="platform-upgrade-rows">{history.map((entry) => <button className="platform-upgrade-history" key={entry.id} onClick={() => void api<Upgrade>(`/system/upgrades/${encodeURIComponent(entry.id)}`).then(setUpgrade).catch((cause) => setError((cause as Error).message))}><strong>{entry.tag}</strong><span>{label(entry.status)}</span><small>{entry.createdAt ? new Date(entry.createdAt).toLocaleString() : entry.id}</small></button>)}</div></section>}
    {confirmStart && upgrade && <ConfirmDialog title="启动平台升级" confirmLabel="确认启动" busy={!!busy} onCancel={() => setConfirmStart(false)} onConfirm={() => { setConfirmStart(false); void act("start", () => api<Upgrade>(`/system/upgrades/${encodeURIComponent(upgrade.id)}/start`, "POST"), "升级已启动，正在更新进度。"); }}><p>将平台升级到 <strong>{upgrade.tag}</strong>。执行期间组件会依次更新，控制台连接可能短暂中断。</p></ConfirmDialog>}
    {confirmAbort && upgrade && <ConfirmDialog title="取消平台升级" confirmLabel="确认取消并解除维护" busy={!!busy} onCancel={() => setConfirmAbort(false)} onConfirm={() => { setConfirmAbort(false); void act("abort", () => api<Upgrade>(`/system/upgrades/${encodeURIComponent(upgrade.id)}/abort`, "POST"), "升级已取消，维护状态已解除。"); }}><p>当前仍在等待任务排空，宿主机升级尚未开始。取消后将解除维护并恢复接单。</p></ConfirmDialog>}
  </div>;
}
