import type { Agent, Message, ProjectSnapshot, PullRequest, Signal } from "../data/types";
import { hives } from "./hive-console";
import { methodologyResponses } from "./round4";

/** Realistic density for round-eight visual and transport tests. */
export function round8Project(now = Date.now()): ProjectSnapshot {
  const at = (hoursAgo: number) => new Date(now - hoursAgo * 3600000).toISOString();
  const longTask = "修复批量导入时的边界条件。" + "需要保留现有行为，补充失败路径的回归测试，并说明验证结果。".repeat(115);
  const agents: Agent[] = Array.from({ length: 48 }, (_, i) => ({
    id: `forager-${String(i).padStart(8, "0")}`,
    hiveId: "swarmjet",
    role: "forager",
    roleName: i % 9 === 0 ? "verifier" : "worker",
    cli: "opencode",
    state: i < 3 ? "active" : i === 3 ? "parked" : i % 11 === 0 ? "cancelled" : i % 7 === 0 ? "failed" : "completed",
    task: i === 0 ? longTask : `处理 Issue #${i + 100} 的输入兼容，并补充测试`,
    issueNumber: i + 100,
    prNumber: i < 17 ? i + 1 : undefined,
    createdAt: at(i * 5 + 1),
    updatedAt: at(i < 4 ? .1 : i * 5),
  }));
  const prs: PullRequest[] = Array.from({ length: 19 }, (_, i) => ({
    number: i + 1,
    title: `修复第 ${i + 1} 批输入兼容问题，并验证旧数据迁移`,
    headSha: `${String(i).padStart(2, "0")}${"a".repeat(38)}`,
    state: i < 16 ? "open" : "closed",
    merged: i >= 16,
    ready: i % 5 === 0,
    reasons: i === 1 ? ["no attestation for the current head", "verifier cancelled by control"] : [],
    ownerId: agents[i]?.id,
    updatedAt: at(i * 12),
  }));
  const signals: Signal[] = Array.from({ length: 13 }, (_, i) => ({
    messageId: `signal-${i}`,
    source: "github",
    kind: "issue",
    number: i + 100,
    title: `Issue #${i + 100}：处理超长字段与重复导入`,
    url: `https://example.test/issues/${i + 100}`,
    claimed: i % 3 === 0,
    claimedBy: i % 3 === 0 ? agents[i].id : "",
    createdAt: at(i + 1),
  }));
  const messages: Message[] = Array.from({ length: 900 }, (_, i) => ({
    id: `message-${String(i).padStart(4, "0")}`,
    hiveId: "swarmjet",
    from: i % 3 === 0 ? "queen" : `forager:${agents[i % agents.length].id}`,
    to: i % 5 === 0 ? "local:web-admin" : i % 2 === 0 ? "queen" : `forager:${agents[i % agents.length].id}`,
    kind: i % 17 === 0 ? "question" : "report",
    chainId: `chain-${i % 17}`,
    hop: 0,
    body: i === 0 ? longTask : `第 ${i + 1} 条历史记录：完成一次检查。`,
    trust: i % 11 === 0 ? "external" : "agent",
    state: "handled",
    createdAt: at((900 - i) / 10),
  }));
  return {
    hive: structuredClone(hives[0]), localAddress: "local:web-admin", agents,
    queen: { enabled: true, state: "active", model: "test/model", agentId: "queen-swarmjet", lastTurnAt: at(.1) },
    prs, queue: [], signals, methodology: structuredClone(methodologyResponses[3]), methodologyEvents: [], messages,
  };
}
