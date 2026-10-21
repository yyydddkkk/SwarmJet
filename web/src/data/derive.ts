import type { Agent, LiveMessage, ProjectSnapshot } from "./types";
import { agentVisibility, aggregateIssues, aggregatePRs, failedCI, historyStars, nodeSummary, readAcknowledged } from "./rules";
export type StarNode = {
  id: string;
  kind: "star" | "comet" | "satellite" | "planet" | "signal" | "queue";
  hiveId: string;
  agentId?: string;
  prNumber?: number;
  parentId?: string;
  label: string;
  state?: string;
};
export type PulseEndpoint = { kind: "node" | "edge" | "observer"; id: string };
export type LightPulse = {
  id: string;
  hiveId: string;
  from: PulseEndpoint;
  to: PulseEndpoint;
  createdAt: string;
};
export function messageEndpoint(
  address: string,
  hiveId: string,
): PulseEndpoint {
  if (address === "queen") return { kind: "node", id: `${hiveId}:queen` };
  if (address.startsWith("forager:"))
    return { kind: "node", id: address.slice(8) };
  if (address.startsWith("local:")) return { kind: "observer", id: "observer" };
  return { kind: "edge", id: hiveId };
}
export const messagePreview = (m: LiveMessage) =>
  "preview" in m ? m.preview : m.body;
export function agentStatus(
  agent: Agent,
  messages: LiveMessage[],
  now = Date.now(),
) {
  const hours = Math.floor((now - Date.parse(agent.updatedAt)) / 3600000);
  if (agent.state === "parked" && hours >= 6)
    return `空闲 ${hours} 小时，仍占着名额`;
  const latest = messages
    .filter((m) => m.from === `forager:${agent.id}`)
    .sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt))[0];
  if (latest)
    return [...messagePreview(latest).split(/[\n。！？]/)[0]]
      .slice(0, 24)
      .join("");
  return {
    active: "工作中",
    creating: "工作中",
    parked: "空闲",
    hibernated: "休眠中",
    failed: "失败",
    completed: "已完成",
    cancelled: "已取消",
    disabled: "已停用",
  }[agent.state];
}
export function deriveProject(snapshot: ProjectSnapshot, now = Date.now()) {
  const { hive, agents, prs, messages } = snapshot;
  const nodes: StarNode[] = [
    {
      id: `${hive.id}:queen`,
      kind: "star",
      hiveId: hive.id,
      label: "主 Agent",
      state: snapshot.queen.state,
    },
  ];
  agents.forEach((a) =>
    nodes.push({
      id: a.id,
      kind: a.roleName === "verifier" ? "satellite" : "comet",
      hiveId: hive.id,
      agentId: a.id,
      prNumber: a.prNumber,
      parentId:
        a.roleName === "verifier" && a.prNumber
          ? `${hive.id}:pr:${a.prNumber}`
          : undefined,
      label: agentStatus(a, messages, now),
      state: a.state,
    }),
  );
  prs.forEach((p) =>
    nodes.push({
      id: `${hive.id}:pr:${p.number}`,
      kind: "planet",
      hiveId: hive.id,
      prNumber: p.number,
      label: `PR #${p.number}`,
      state: p.merged ? "merged" : p.state,
    }),
  );
  snapshot.signals.forEach((s) =>
    nodes.push({
      id: s.messageId,
      kind: "signal",
      hiveId: hive.id,
      label: s.title,
    }),
  );
  snapshot.queue.forEach((q) =>
    nodes.push({
      id: `queue:${q.agentId}`,
      kind: "queue",
      hiveId: hive.id,
      agentId: q.agentId,
      label: q.task,
    }),
  );
  const decisions = [
    ...prs
      .filter(
        (p) =>
          p.ready && !p.merged && !snapshot.methodology.manifest.merge.auto,
      )
      .map((pr) => ({
        kind: "pr" as const,
        id: `${hive.id}:pr:${pr.number}`,
        pr,
      })),
    ...messages
      .filter(
        (m) =>
          m.kind === "question" &&
          !!snapshot.localAddress &&
          m.to === snapshot.localAddress &&
          !messages.some(
            (r) =>
              !!m.chainId &&
              r.kind === "reply" &&
              r.from === snapshot.localAddress &&
              r.to === m.from &&
              r.chainId === m.chainId &&
              Date.parse(r.createdAt) >= Date.parse(m.createdAt),
          ),
      )
      .map((message) => ({
        kind: "question" as const,
        id: message.id,
        message,
      })),
  ];
  const prGroups = aggregatePRs(prs, agents, now);
  const issueGroups = aggregateIssues(snapshot.signals, agents, now);
  const mapAgents = new Set(agents.filter((agent) => agentVisibility(agent, now, readAcknowledged(hive.id, agent.id)) === "orbit").map((agent) => agent.id));
  const mapPRs = new Set(prGroups.visible.map((pr) => pr.number));
  const mapIssues = new Set(issueGroups.visible.map((issue) => issue.messageId));
  const mapNodes = nodes.filter((node) => node.kind === "star" || node.kind === "queue" ||
    (node.agentId && mapAgents.has(node.agentId)) ||
    (node.kind === "planet" && node.prNumber !== undefined && mapPRs.has(node.prNumber)) ||
    (node.kind === "signal" && mapIssues.has(node.id)))
    .map((node) => {
      const agent = agents.find((item) => item.id === node.agentId);
      const pr = prs.find((item) => item.number === node.prNumber);
      const issue = snapshot.signals.find((item) => item.messageId === node.id);
      return { ...node, label: agent ? nodeSummary("agent", agent, { prs, issues: snapshot.signals }) : pr ? nodeSummary("pr", pr) : issue ? nodeSummary("issue", issue) : node.label };
    });
  return {
    nodes,
    mapNodes,
    history: historyStars(agents, prs, now, (id) => readAcknowledged(hive.id, id), { x: 0, y: 0 }, snapshot.signals),
    prGroups,
    issueGroups,
    failedCI: failedCI(prs, snapshot.signals, messages),
    decisions,
    needsAttention: decisions.length > 0,
    agentStatuses: Object.fromEntries(
      agents.map((a) => [a.id, agentStatus(a, messages, now)]),
    ),
  };
}
