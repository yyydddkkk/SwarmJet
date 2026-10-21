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

const methodologyDescriptions: Record<string, string> = {
  "A worker implements each task and an independent verifier checks every PR head. Merging requires a non-author attestation and passing CI; the user decides when to merge.": "开发 Agent 处理任务，另一位 Agent 独立验证每个 PR 的当前提交。验证和 GitHub CI 通过后，由你决定是否合并。",
  "A single worker implements tasks and publishes its branch and PR. There is no independent verifier or automatic merge.": "由一个开发 Agent 完成任务并提交分支和 PR，不另派验证 Agent，也不自动合并。",
  "Owns one task end to end. Builds the change on its own branch, keeps it green and answers review findings.": "负责一项任务，在自己的分支上开发、完成检查，并处理审查意见。",
  "Independent, read-only check of one PR head commit it did not write. Runs the change and attests PASS, PASS_NOTES, FAIL or BLOCKED.": "只读核对其他 Agent 的 PR 当前提交，运行检查并给出通过、附建议通过、失败或受阻的结论。",
  "Implements one task, validates the result, and publishes its branch and PR.": "完成一项任务，验证结果后提交分支和 PR。",
  "Engineering principles every SwarmJet Agent follows (adapted from pstack).": "所有 Agent 遵循的工程原则，参考 pstack。",
  "Use for a defect, regression or failing test. Reproduce first, find the root cause with evidence, fix minimally, prove the repro now passes.": "先复现缺陷并找到原因，再做最小修复并验证。",
  "Use for new or changed behaviour. Understand the subsystem, settle the data shape first, build in small verified commits, prove it end to end.": "先了解模块和数据，再分步实现功能并完成验证。",
  "Use as a verifier. Independently prove or disprove one PR head commit you did not write, then attest a verdict for that exact SHA.": "独立核对指定提交，给出与该提交对应的验证结论。",
  "Use for a behaviour-preserving change to structure. Pin current behaviour, change in small steps, prove nothing observable changed.": "先固定现有行为，再分步调整结构并验证行为没有变化。",
  "Use as a PR owner when CI fails, a verifier reports findings or a reviewer comments. Classify before you change code; never churn code to quiet a bot.": "先区分 CI、验证与审查的问题，再按证据修复并验证。",
  "Use for a read-only question (how does X work, why is Y like this, is Z safe). Answer with evidence; change no code.": "只读调查系统行为并提供证据，不改代码。",
};
/** Translate known bundled descriptions; preserve unknown source prose in details. */
export function methodologyDescription(raw = ""): { text: string; detail?: string } {
  const value = raw.trim();
  if (!value) return { text: "暂无说明。" };
  if (methodologyDescriptions[value]) return { text: methodologyDescriptions[value] };
  if (/[\u3400-\u9fff]/u.test(value)) return { text: value };
  return { text: "这项说明尚未提供中文。", detail: raw };
}
