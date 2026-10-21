import { useState } from "react";
import { useDataAPI, relativeTime, type ProjectSnapshot } from "../../data";
import { useResource } from "./use-resource";
import { ErrorNotice } from "./primitives";

const roleName = (name: string) => name === "queen" ? "主 Agent" : name;
export default function AlmanacOverview({ snapshot, onMethodology, onSettings, onUsage, onSaved }: {
  snapshot: ProjectSnapshot;
  onMethodology(): void;
  onSettings(): void;
  onUsage(): void;
  onSaved(): void;
}) {
  const api = useDataAPI();
  const id = snapshot.hive.id;
  const method = snapshot.methodology;
  const manifest = method.manifest;
  const roles = Object.keys(manifest.roles ?? {}).filter(role => role !== "queen");
  const versions = useResource(signal => api.listMethodologyVersions(id, signal), [id]);
  const usage = useResource(signal => api.getUsage({hive:id, period:"today"}, signal), [id]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const occupied = snapshot.agents.filter(agent => ["creating", "active", "parked", "hibernated"].includes(agent.state)).length;
  const limit = snapshot.hive.config.maxConcurrentForagers;
  const update = async (patch: Parameters<typeof api.updateHive>[1]) => {
    if (busy) return;
    setBusy(true); setError("");
    try { await api.updateHive(id, patch); onSaved(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); }
    finally { setBusy(false); }
  };
  const rules = (manifest.events ?? []).map((event) => {
    if (event.event === "issue_labeled") return `Issue 加上“${event.label || "指定"}”标签时，交给 ${roleName(event.role)}。`;
    if (event.event === "issue_comment") return `Issue 收到以“${event.commentPrefix || "指定前缀"}”开头的评论时，交给 ${roleName(event.role)}。`;
    if (event.event === "pr_head") return `PR 有新提交时，交给 ${roleName(event.role)}。`;
    return `收到 ${event.event} 事件时，交给 ${roleName(event.role)}。`;
  });
  const merge = manifest.merge;
  const mergeRule = [
    merge.requireAttestation ? `合并需要${merge.requireNonAuthor ? "另一位 Agent" : "Agent"}验证通过` : "合并不要求验证结论",
    merge.requireChecks ? "GitHub 必需检查全部通过" : "不要求 GitHub 检查",
    merge.auto ? "满足条件后自动合并。" : "最后由你决定。",
  ].join("，");
  const previous = (versions.data?.versions ?? []).filter(item => item.version !== method.version).sort((a,b) => b.version-a.version);
  return <div className="pc-almanac-overview">
    <p className="pc-almanac-lead">{snapshot.queen.enabled ? `按 ${method.source?.url.startsWith("builtin:") ? method.source.url.slice(8) : "当前方法论"} 第 ${method.version} 版运转。` : "主 Agent 已停用，新事件会留在项目里等待处理。"}</p>
    <section>
      <h3>方法论</h3>
      <svg className="pc-almanac-mini" viewBox="-124 -50 248 96" role="img" aria-label="方法论角色轨道">
        {roles.map((role, index) => { const rx = index ? 104 : 56, ry = index ? 38 : 20; return <g key={role}><ellipse rx={rx} ry={ry}/><text x="0" y={-ry-6} textAnchor="middle">{roleName(role)}</text></g>; })}
        <circle r="5"/>
      </svg>
      <div className="pc-almanac-rules">
        {rules.map((rule,i) => <p key={i}>{rule}</p>)}
        {(manifest.standingOrders ?? []).map((rule,i) => <p key={`standing-${i}`}>{rule}</p>)}
        <p>{mergeRule}</p>
      </div>
      <div className="pc-almanac-files">{method.files.map(file => <div key={file.path}>{file.path}</div>)}</div>
      <div className="pc-almanac-versions">第 {method.version} 版，{method.note || `${relativeTime(method.createdAt)}由 ${method.createdBy || "项目"}创建`}。<br/>
        {previous.map(version => <button key={version.version} type="button" onClick={onMethodology}>查看第 {version.version} 版</button>)}
      </div>
      <button type="button" className="pc-almanac-link" onClick={onMethodology}>编辑方法论与版本历史</button>
    </section>
    <section>
      <h3>设置</h3>
      <div className="pc-almanac-line"><span>同时工作的 Agent</span><span className="pc-almanac-slots">
        <button type="button" className="pc-almanac-step" aria-label="减少并发上限" disabled={busy || limit <= 1} onClick={() => void update({config:{maxConcurrentForagers:limit-1}})}>−</button>
        {Array.from({length:Math.min(limit,12)},(_,i) => <i key={i} className={`pc-almanac-slot ${i<occupied?"filled":""}`}/>)}
        <button type="button" className="pc-almanac-step" aria-label="增加并发上限" disabled={busy || limit >= 12} onClick={() => void update({config:{maxConcurrentForagers:limit+1}})}>+</button>
      </span></div>
      <div className="pc-almanac-line"><span>主 Agent {snapshot.queen.enabled ? "正在照看这个项目" : "已停用"}</span><button type="button" disabled={busy} onClick={() => void update({queenEnabled:!snapshot.queen.enabled})}>{snapshot.queen.enabled ? "停用" : "启用"}</button></div>
      <div className="pc-almanac-line"><span>主 Agent 使用 {snapshot.hive.config.queenModel || snapshot.queen.model || "默认模型"}</span><button type="button" onClick={onSettings}>更换</button></div>
      <button type="button" className="pc-almanac-link" onClick={onSettings}>更多项目设置</button>
      {error && <ErrorNotice message={error} />}
    </section>
    <section>
      <h3>花费</h3>
      <p className="pc-almanac-mute">{usage.data ? `今天 $${usage.data.totals.costUsd.toFixed(2)}。` : usage.error ? "今天的花费暂时无法读取。" : "正在读取今天的花费…"}</p>
      {manifest.limits?.maxCostPerAgentUsd || manifest.limits?.maxCostPerDayUsd ? <p className="pc-almanac-mute">方法论预算：每个 Agent 不超过 ${manifest.limits.maxCostPerAgentUsd ?? "未设置"}，每天不超过 ${manifest.limits.maxCostPerDayUsd ?? "未设置"}（美元）。</p> : <p className="pc-almanac-mute">这套方法论没有设定预算。</p>}
      <button type="button" className="pc-almanac-link" onClick={onUsage}>查看花费详情</button>
    </section>
    <section><h3>记忆</h3><p className="pc-almanac-mute">还没有。Agent 积累下来的经验以后会显示在这里。</p></section>
  </div>;
}
