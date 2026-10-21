import type { Agent, AgentState, ProjectSnapshot } from "./types";
import { agentSummary, agentVisibility, issueVisible, prSummary, prVisible, readAcknowledged, shortAgentId, weightedTruncate } from "./rules";

export const agentStateLabel = (state: AgentState) => ({
  active: "工作中", creating: "正在准备", parked: "空闲", hibernated: "已休眠",
  completed: "已完成", failed: "失败", cancelled: "已取消", disabled: "已停用",
})[state];
export const addressLabel = (address: string) => address === "queen" ? "主 Agent" :
  address === "hive" || address === "system" ? "系统" : address.startsWith("local:") ? "你" :
  address.startsWith("forager:") ? `Agent ${shortAgentId(address.slice(8))}` : address;
export const messageTrustLabel = (trust: string) => ({ user: "来自管理员", agent: "来自 Agent", external: "外部内容，未经核实", system: "来自系统" } as Record<string, string>)[trust] || trust;
export const activeAgents = (agents: Agent[]) => agents.filter((agent) => agent.state === "active" || agent.state === "creating");
export function completedLastNight(agents: Agent[], now = Date.now()): number {
  const start = new Date(now), end = new Date(now);
  start.setHours(-6, 0, 0, 0); end.setHours(6, 0, 0, 0);
  return agents.filter((agent) => agent.state === "completed" && Date.parse(agent.updatedAt) >= start.getTime() && Date.parse(agent.updatedAt) < Math.min(end.getTime(), now)).length;
}
export function attentionAgents(snapshot: ProjectSnapshot, now = Date.now(), acknowledged: (id: string) => boolean = (id) => readAcknowledged(snapshot.hive.id, id)): Agent[] {
  return snapshot.agents.filter((agent) => {
    const age = now - Date.parse(agent.updatedAt);
    return age >= 0 && age < 86400000 && !acknowledged(agent.id) &&
      (agent.state === "failed" || agent.state === "parked" && age >= 6 * 3600000);
  });
}
export type CatalogueScope = "active" | "today" | "history";
export type CatalogueEntry = {
  id: string;
  glyph: "star" | "busy" | "parked" | "failed" | "moon" | "pr" | "signal" | "ci" | "queue" | "hist" | "ready";
  summary: string;
  status: string;
  cost?: number;
  scopes: CatalogueScope[];
  search: string;
  selection: { hiveId: string; kind: "queen" | "agent" | "pr" | "signal"; id: string; number?: number };
};
/** Today is the local calendar day; history also includes things completed today. */
export function catalogueRows(snapshot: ProjectSnapshot, scope?: CatalogueScope, query = "", now = Date.now(), acknowledged: (id: string) => boolean = (id) => readAcknowledged(snapshot.hive.id, id)): CatalogueEntry[] {
  const { hive, agents, prs, signals } = snapshot;
  const start = new Date(now); start.setHours(0, 0, 0, 0);
  const scopes = (active: boolean, historical: boolean, at?: string): CatalogueScope[] => {
    const time = Date.parse(at || "");
    return [...(active ? ["active" as const] : []), ...(time >= start.getTime() && time <= now ? ["today" as const] : []), ...(historical ? ["history" as const] : [])];
  };
  const rows: CatalogueEntry[] = [{
    id: `${hive.id}:queen`, glyph: "star", summary: "主 Agent", status: snapshot.queen.enabled ? "运行中" : "已停用",
    scopes: scopes(true, false, snapshot.queen.lastTurnAt || hive.updatedAt), search: `${hive.name} 主 Agent`,
    selection: { hiveId: hive.id, kind: "queen", id: `${hive.id}:queen` },
  }];
  for (const agent of agents) {
    const ended = ["completed", "cancelled", "failed", "disabled"].includes(agent.state);
    const summary = agentSummary(agent, { prs, issues: signals }, 40);
    const status = agentStateLabel(agent.state);
    rows.push({ id: agent.id, glyph: agent.state === "failed" ? "failed" : ended ? "hist" : agent.roleName === "verifier" ? "moon" : agent.state === "parked" ? "parked" : "busy",
      summary, status, cost: agent.costUsd, scopes: scopes(agentVisibility(agent, now, acknowledged(agent.id)) === "orbit", ended, agent.updatedAt),
      search: `${hive.name} ${agent.id} ${shortAgentId(agent.id)} ${summary} ${agent.issueNumber ?? ""} ${status}`,
      selection: { hiveId: hive.id, kind: "agent", id: agent.id },
    });
  }
  for (const pr of prs) {
    const summary = `PR #${pr.number} ${prSummary(pr, 40)}`;
    const status = pr.unavailable ? "状态暂时无法获取" : pr.merged ? "已合并" : pr.state === "closed" ? "已关闭" : pr.ready ? "等你决定" : "等待验证";
    rows.push({ id: `${hive.id}:pr:${pr.number}`, glyph: pr.ready && prVisible(pr) ? "ready" : "pr", summary, status,
      scopes: scopes(prVisible(pr) || !!pr.unavailable, pr.merged || pr.state === "closed", pr.updatedAt), search: `${hive.name} ${summary} ${status}`,
      selection: { hiveId: hive.id, kind: "pr", id: `${hive.id}:pr:${pr.number}`, number: pr.number },
    });
  }
  for (const item of snapshot.queue) rows.push({
    id: `queue:${item.agentId}`, glyph: "queue", summary: weightedTruncate(item.task || item.role, 40), status: `排队第 ${item.position} 位`,
    scopes: scopes(true, false, item.createdAt), search: `${hive.name} ${item.task} ${item.agentId}`,
    selection: { hiveId: hive.id, kind: "agent", id: item.agentId },
  });
  for (const signal of signals) {
    const summary = `${signal.kind === "issue" ? "Issue" : signal.kind === "pull_request" ? "PR" : "检查"} #${signal.number} ${weightedTruncate(signal.title, 40)}`;
    const current = signal.kind === "issue" ? issueVisible(signal, agents, now) : !signal.claimed;
    const status = signal.claimed ? "已被接走" : "待处理";
    rows.push({ id: signal.messageId, glyph: signal.kind === "check" ? "ci" : "signal", summary, status,
      scopes: scopes(current, !current, signal.createdAt), search: `${hive.name} ${summary} ${signal.messageId}`,
      selection: { hiveId: hive.id, kind: "signal", id: signal.messageId, number: signal.number },
    });
  }
  const needle = query.trim().toLowerCase();
  return rows.filter((row) => (!scope || row.scopes.includes(scope)) && (!needle || row.search.toLowerCase().includes(needle)));
}
