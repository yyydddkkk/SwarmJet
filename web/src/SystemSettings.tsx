import { useCallback, useEffect, useState } from "react";
import { api } from "./api";
import type { NodeView } from "./NodeSettings";
import NetworkSettings from "./NetworkSettings";
import GitLabIdentitySettings from "./GitLabIdentitySettings";
import { ErrorBox } from "./ui";
import "./settings-page.css";
import "./system-settings.css";

type Deployment = {
  executionMode: "local" | "distributed";
  publicOrigin: string;
  hostListen: string;
  secureCookies: boolean;
  mihomoEnabled: boolean;
};
type Proxy = { enabled: boolean; running?: boolean; proxyUrl?: string };
type Check = { id: string; status: "ok" | "warning" | "error" | "unavailable"; message: string };
type CheckResult = { checks: Check[]; checkedAt: string };
type Operations = {
  inboxPending: number;
  inboxRetrying: number;
  inboxOldestWaitSeconds: number;
  inboxProcessedLastHour: number;
  inboxP95ProcessSeconds: number;
  admissionWaiting: number;
  runnerSlotsReserved: number;
  measuredAt: string;
};

const labels: Record<string, string> = {
  mihomo: "Mihomo 管理连接",
  nodes: "Node 心跳",
};
const statusLabel: Record<Check["status"], string> = {
  ok: "正常",
  warning: "需检查",
  error: "连接失败",
  unavailable: "未配置",
};

export default function SystemSettings({ section }: { section: string }) {
  const current = section === "network" || section === "gitlab" ? section : "overview";
  const [deployment, setDeployment] = useState<Deployment | null>(null);
  const [proxy, setProxy] = useState<Proxy | null>(null);
  const [nodes, setNodes] = useState<NodeView[]>([]);
  const [operations, setOperations] = useState<Operations | null>(null);
  const [checks, setChecks] = useState<CheckResult | null>(null);
  const [loading, setLoading] = useState(true);
  const [checking, setChecking] = useState(false);
  const [loadError, setLoadError] = useState("");
  const [checkError, setCheckError] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    const results = await Promise.allSettled([
      api<Deployment>("/system"),
      api<Proxy>("/mihomo/status"),
      api<NodeView[]>("/nodes"),
      api<Operations>("/operations"),
    ]);
    setDeployment(results[0].status === "fulfilled" ? results[0].value : null);
    setProxy(results[1].status === "fulfilled" ? results[1].value : null);
    setNodes(results[2].status === "fulfilled" ? results[2].value || [] : []);
    setOperations(results[3].status === "fulfilled" ? results[3].value : null);
    const names = ["部署状态", "代理状态", "节点列表", "运行统计"];
    const failed = results.flatMap((result, index) => result.status === "rejected" ? [names[index]] : []);
    setLoadError(failed.length ? `无法读取${failed.join("、")}，请检查服务连接后重试。` : "");
    setLoading(false);
  }, []);
  const runChecks = useCallback(async () => {
    setChecking(true);
    setCheckError("");
    try {
      setChecks(await api<CheckResult>("/system/checks", "POST"));
    } catch (cause) {
      setCheckError((cause as Error).message);
    } finally {
      setChecking(false);
    }
  }, []);
  useEffect(() => {
    if (current !== "overview") return;
    void load();
    void runChecks();
  }, [current, load, runChecks]);

  return (
    <div className="settings-page system-settings">
      <div className="page-head system-page-head">
        <div>
          <h1>系统设置</h1>
          <p>查看服务实际使用的地址，检查代理与节点连接，并管理网络出口。</p>
        </div>
        {current === "overview" && <button disabled={checking || loading} onClick={() => { void load(); void runChecks(); }}>
          {checking ? "检查中…" : "重新检查"}
        </button>}
      </div>
      <nav className="system-tabs" aria-label="系统设置分区">
        <a href="#/settings" aria-current={current === "overview" ? "page" : undefined}>概览与服务地址</a>
        <a href="#/settings/network" aria-current={current === "network" ? "page" : undefined}>网络与代理</a>
        <a href="#/settings/gitlab" aria-current={current === "gitlab" ? "page" : undefined}>GitLab 身份</a>
      </nav>
      {current === "network" && <NetworkSettings embedded />}
      {current === "gitlab" && <GitLabIdentitySettings />}
      {current === "overview" && <>
        <ErrorBox error={[loadError, checkError].filter(Boolean).join(" ")} retry={() => { void load(); void runChecks(); }} />
        <div className="system-context">
          <span>运行方式：{deployment?.executionMode === "distributed" ? "分布式" : deployment ? "单机" : "读取中"}</span>
          <span>配置来源：部署环境与平台设置</span>
        </div>
        <section className="panel system-addresses" aria-labelledby="system-addresses-heading">
          <div className="panel-head"><div><h2 id="system-addresses-heading">服务地址</h2><p>显示当前生效值；部署地址需在服务器配置中修改并重启服务。</p></div></div>
          <div className="system-address-list">
            <AddressRow label="控制台公开地址" value={deployment?.publicOrigin} source="PUBLIC_ORIGIN · 启动配置" note="浏览器登录与同源校验使用此地址。" />
            <AddressRow label="宿主监听地址" value={deployment?.hostListen} source="LISTEN_HOST / HTTP_PORT · 部署配置" note="本机开发显示 API 实际监听地址。" />
            <AddressRow label="代理入口" value={proxy?.proxyUrl} source={proxy?.enabled ? "Mihomo 服务配置" : "未启用"} note="使用代理的 Node 必须能访问这个地址。" href="#/settings/network" />
          </div>
          <div className="system-address-foot">Cookie：{deployment?.secureCookies ? "仅 HTTPS 发送" : "未要求 HTTPS"}。主密钥、管理员密码和证书私钥由部署环境管理，不在页面中显示。</div>
        </section>
        <section className="panel system-checks" aria-labelledby="system-checks-heading">
          <div className="panel-head"><div><h2 id="system-checks-heading">连接检查</h2><p>从控制面检查 Mihomo 管理连接；Node 状态使用最近 30 秒心跳。</p></div><small>{checks?.checkedAt ? `检查于 ${new Date(checks.checkedAt).toLocaleTimeString()}` : checking ? "正在检查" : "尚未检查"}</small></div>
          <div className="system-check-list">
            {(checks?.checks || []).map((check) => <div className="system-check-row" key={check.id}>
              <span className={`system-check-state ${check.status}`}>{statusLabel[check.status]}</span>
              <strong>{labels[check.id] || check.id}</strong>
              <span>{check.message}</span>
            </div>)}
            {!checks && <p className="system-check-empty">{checking ? "正在检查服务连接…" : "点击“重新检查”读取服务状态。"}</p>}
            {checks?.checks.length === 0 && <p className="system-check-empty">当前没有可显示的检查结果。</p>}
          </div>
        </section>
        <section className="panel system-checks" aria-labelledby="system-operations-heading">
          <div className="panel-head"><div><h2 id="system-operations-heading">接入与执行</h2><p>统计来自数据库；执行槽包含工作区准备、Runner 和验证。</p></div><small>{operations?.measuredAt ? `采样于 ${new Date(operations.measuredAt).toLocaleTimeString()}` : ""}</small></div>
          <div className="system-check-list">
            {operations ? <>
              <div className="system-check-row system-operation-row"><strong>Webhook inbox</strong><span>待处理 {operations.inboxPending} · 已重试 {operations.inboxRetrying} · 最久等待 {Math.round(operations.inboxOldestWaitSeconds)} 秒</span></div>
              <div className="system-check-row system-operation-row"><strong>事件处理</strong><span>近一小时完成 {operations.inboxProcessedLastHour} · 接收到完成 P95 {operations.inboxP95ProcessSeconds.toFixed(1)} 秒</span></div>
              <div className="system-check-row system-operation-row"><strong>任务准入</strong><span>等待 {operations.admissionWaiting} · 执行槽已预约 {operations.runnerSlotsReserved}</span></div>
            </> : <p className="system-check-empty">{loading ? "正在读取运行统计…" : "运行统计暂不可用。"}</p>}
          </div>
        </section>
        <section className="panel system-nodes" aria-labelledby="system-nodes-heading">
          <div className="panel-head"><div><h2 id="system-nodes-heading">执行节点</h2><p>节点对外地址由各节点部署配置上报。</p></div><a className="button" href="#/nodes">管理 Node</a></div>
          <div className="system-node-list">
            {nodes.map(({ node, online }) => <div className="system-node-row" key={node.id}>
              <strong>{node.id}</strong><code>{node.address}</code><span className={online ? "online" : "offline"}>{online ? "心跳正常" : "心跳中断"}</span>
            </div>)}
            {!loading && nodes.length === 0 && <p>尚无节点注册。先在 Node 管理中创建注册凭据。</p>}
            {loading && nodes.length === 0 && <p>正在读取节点…</p>}
          </div>
        </section>
      </>}
    </div>
  );
}

function AddressRow({ label, value, source, note, href }: { label: string; value?: string; source: string; note: string; href?: string }) {
  return <div className="system-address-row">
    <div><strong>{label}</strong><small>{note}</small></div>
    <div className="system-address-value"><code>{value || "未配置"}</code><small>{source}</small></div>
    {href ? <a href={href}>查看设置</a> : <span aria-hidden="true" />}
  </div>;
}
