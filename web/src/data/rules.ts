import type { Agent, LiveMessage, PullRequest, Signal } from "./types";

export const SUMMARY_WIDTH = { star: 14, list: 40, briefing: 30 } as const;
const wide = (character: string) => /[\u3000-\u9fff\uff00-\uffef]/u.test(character);

/** Width follows the design: a Han/fullwidth glyph is 1, everything else .55. */
export function weightedTruncate(value: string, limit: number): string {
  const text = value.trim();
  let width = 0;
  let result = "";
  for (const character of text) {
    const next = width + (wide(character) ? 1 : .55);
    if (next > limit) return result.trimEnd() + "…";
    result += character;
    width = next;
  }
  return result;
}

export function firstSentence(task = ""): string {
  return task.trim().split(/\n|[。！？!?]|\.(?=\s|$)/u, 1)[0].trim();
}

export function oneLineSummary(
  agent: Pick<Agent, "task" | "roleName" | "prNumber" | "issueNumber">,
  links: { prs?: PullRequest[]; issues?: Signal[] } = {},
  width?: number,
): string {
  const issue = links.issues?.find((item) => item.kind === "issue" && item.number === agent.issueNumber);
  const pr = links.prs?.find((item) => item.number === agent.prNumber);
  const text = issue?.title || pr?.title || firstSentence(agent.task) || agent.roleName || "Agent";
  return width === undefined ? text : weightedTruncate(text, width);
}
export const agentSummary = (agent: Agent, links: { prs?: PullRequest[]; issues?: Signal[] } = {}, width: number = SUMMARY_WIDTH.list) =>
  oneLineSummary(agent, links, width);
export const prSummary = (pr: PullRequest, width: number = SUMMARY_WIDTH.list) =>
  weightedTruncate(pr.title || `PR #${pr.number}`, width);
export const nodeSummary = (kind: "agent" | "pr" | "issue", item: Agent | PullRequest | Signal, links: { prs?: PullRequest[]; issues?: Signal[] } = {}) =>
  weightedTruncate(kind === "agent" ? `#${(item as Agent).id.slice(-8)} ${oneLineSummary(item as Agent, links)}` :
    kind === "pr" ? `#${(item as PullRequest).number} ${(item as PullRequest).title || `PR #${(item as PullRequest).number}`}` :
    `#${(item as Signal).number} ${(item as Signal).title}`, SUMMARY_WIDTH.star);

export const shouldCollapseLongText = (value: string, lines = 4, lineWidth = 16): boolean =>
  value.split("\n").reduce((used, line) =>
    used + Math.max(1, Math.ceil([...line].reduce((width, character) => width + (wide(character) ? 1 : .55), 0) / lineWidth)), 0) > lines;
export function uniqueLongTexts(values: string[]): string[] {
  const seen = new Set<string>();
  return values.filter((value) => {
    const key = value.trim().replace(/\s+/gu, " ");
    if (!key || seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

export const shortSha = (sha: string) => sha.slice(0, 7);
export const shortAgentId = (id: string) => id.slice(-8);

export function relativeTime(value: string, now = Date.now(), locale = "zh-CN"): string {
  const time = Date.parse(value);
  if (!Number.isFinite(time)) return "时间未知";
  const difference = Math.max(0, now - time);
  if (difference < 60000) return "刚刚";
  if (difference < 3600000) return `${Math.floor(difference / 60000)} 分钟前`;
  if (difference < 86400000) return `${Math.floor(difference / 3600000)} 小时前`;
  return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(time);
}

const reasonRules: [RegExp, string][] = [
  [/no attestation for the current head/i, "当前提交还没有验证结论"],
  [/no non-author attestation for the current head/i, "当前提交还没有独立验证结论"],
  [/verifier cancelled by control/i, "验证 Agent 已被取消"],
  [/check\s+.+\s+failed\s*\(|check(s| runs?)? (failed|not passing|did not pass)|ci (failed|not passing)|required check/i, "GitHub CI 未通过"],
  [/latest attestation is for .+, pull request head is .+|head (has )?changed|new commit|new head|stale head|outdated head/i, "PR 有新提交，请重新查看"],
  [/latest attestation is (fail|blocked)|attestation.*(failed|blocked)/i, "验证未通过"],
  [/latest attestation is from the pull request author/i, "需要其他 Agent 独立验证"],
  [/pull request is already merged/i, "PR 已合并"],
  [/pull request is not open/i, "PR 已关闭"],
  [/pull request has no head commit/i, "PR 还没有提交"],
  [/pull request targets .+, not the Hive repository/i, "PR 指向其他仓库"],
  [/pull request has a conflict/i, "PR 有合并冲突"],
  [/draft/i, "PR 仍是草稿"],
];
export function localizedReason(raw: string): { text: string; detail?: string } {
  const known = reasonRules.find(([pattern]) => pattern.test(raw));
  return known ? { text: known[1] } : { text: "其他原因", detail: raw };
}

export const readAcknowledged = (hiveId: string, agentId: string): boolean => {
  try { return localStorage.getItem(`sj-known:${hiveId}:${agentId}`) === "1"; }
  catch { return false; }
};
export const writeAcknowledged = (hiveId: string, agentId: string): void => {
  try { localStorage.setItem(`sj-known:${hiveId}:${agentId}`, "1"); }
  catch { /* Storage may be blocked or full. */ }
};

export function agentVisibility(agent: Agent, now = Date.now(), acknowledged = false): "orbit" | "history" | "hidden" {
  if (["active", "creating", "parked", "hibernated"].includes(agent.state)) return "orbit";
  if (agent.state === "cancelled" || agent.state === "disabled") return "hidden";
  if (agent.state === "failed" && !acknowledged && now - Date.parse(agent.updatedAt) < 86400000) return "orbit";
  return "history";
}
export const prVisible = (pr: PullRequest) => !pr.merged && pr.state === "open";
export const issueVisible = (issue: Signal, agents: Agent[], now = Date.now()) =>
  issue.kind === "issue" && (!issue.claimed || agents.some((a) => a.issueNumber === issue.number && agentVisibility(a, now) === "orbit"));
export const failedCIVisible = (status: string) => ["failed", "failure", "error", "timed_out", "cancelled", "action_required"].includes(status.toLowerCase());
export type FailedCI = { id: string; prNumber: number; source: "message" | "pr-reason"; reason: string; signalId?: string };
/** Only definite failures already present in loaded data; no extra historical inbox reads. */
export function failedCI(prs: PullRequest[], signals: Signal[], messages: LiveMessage[]): FailedCI[] {
  const open = new Set(prs.filter(prVisible).map((pr) => pr.number));
  const byId = new Map(messages.map((message) => [message.id, message]));
  const result: FailedCI[] = [];
  const latest = new Map<string, { signal: Signal; state: string; at: number }>();
  for (const signal of signals) {
    if (signal.kind !== "check" || !open.has(signal.number)) continue;
    const message = byId.get(signal.messageId);
    if (!message || !("body" in message)) continue;
    const start = message.body.indexOf("GitHub event: ");
    if (start < 0) continue;
    const header = message.body.slice(start).split("\n\n", 1)[0];
    const line = header.split("\n").find((value) => /^(Check|Status): /i.test(value));
    if (!line) continue;
    const separator = line.indexOf(": ");
    const kind = line.slice(0, separator), value = line.slice(separator + 2);
    const arrow = value.lastIndexOf(" -> ");
    const context = arrow < 0 ? "default" : value.slice(0, arrow).trim();
    const state = (arrow < 0 ? value : value.slice(arrow + 4)).trim().toLowerCase();
    const key = `${signal.number}:${kind}:${context}`;
    const at = Date.parse(message.createdAt || signal.createdAt);
    if (!latest.has(key) || at >= latest.get(key)!.at) latest.set(key, { signal, state, at });
  }
  for (const { signal, state } of latest.values())
    if (failedCIVisible(state)) result.push({ id: `ci:${signal.messageId}`, prNumber: signal.number, source: "message", reason: "GitHub CI 未通过", signalId: signal.messageId });
  const covered = new Set(result.map((item) => item.prNumber));
  for (const pr of prs) {
    if (!open.has(pr.number) || covered.has(pr.number)) continue;
    if (pr.reasons?.some((reason) => /check\s+.+\s+failed\s*\(|required check failed:|required checks are not green|ci failed/i.test(reason)))
      result.push({ id: `ci:pr:${pr.number}`, prNumber: pr.number, source: "pr-reason", reason: "GitHub CI 未通过" });
  }
  return result;
}

// These constants and hash operations are transcribed from design/stars/index.html histPos.
export const HISTORY_POSITION = { issueRadius: 352, inner: 422, outer: 760, aspect: .55, tilt: -.24, cap: 500 } as const;
function hash(value: string): number {
  let h = 2166136261;
  for (let i = 0; i < value.length; i++) { h ^= value.charCodeAt(i); h = Math.imul(h, 16777619); }
  h ^= h >>> 16; h = Math.imul(h, 2246822507);
  h ^= h >>> 13; h = Math.imul(h, 3266489909); h ^= h >>> 16;
  return (h >>> 0) / 4294967296;
}
export function historyPosition(id: string, center = { x: 0, y: 0 }) {
  const angle = hash(id + ":a") * Math.PI * 2;
  const u = hash(id + ":r");
  const r = Math.min(760, 422 + (760 - 422) * (-Math.log(1 - u * .97)) / 3.2);
  const c = Math.cos(angle) * r, d = Math.sin(angle) * r * (.55 + .12);
  return { x: center.x + c * Math.cos(-.24) - d * Math.sin(-.24), y: center.y + c * Math.sin(-.24) + d * Math.cos(-.24) };
}
export type HistoryStar = { id: string; summary: string; result: "merged" | "done" | "failed"; endedAt: string; ageHours: number; fresh: boolean; brightness: number; dust: boolean; x: number; y: number };
/** updatedAt is an approximation of the actual completion time. */
export function historyStars(agents: Agent[], prs: PullRequest[], now = Date.now(), acknowledged: (id: string) => boolean = () => false, center = { x: 0, y: 0 }, issues: Signal[] = []): HistoryStar[] {
  return agents.filter((agent) => agent.roleName !== "verifier" && agentVisibility(agent, now, acknowledged(agent.id)) === "history")
    .sort((a, b) => Date.parse(b.updatedAt) - Date.parse(a.updatedAt))
    .map((agent, index) => {
      const ageHours = Math.max(0, (now - Date.parse(agent.updatedAt)) / 3600000);
      const merged = prs.some((pr) => pr.merged && pr.ownerId === agent.id);
      const result = agent.state === "failed" ? "failed" : merged ? "merged" : "done";
      const fade = Math.max(0, Math.min(1, (ageHours - 24) / (6 * 24)));
      return { id: agent.id, summary: oneLineSummary(agent, { prs, issues }), result, endedAt: agent.updatedAt, ageHours, fresh: ageHours < 6, brightness: (result === "failed" ? .35 : result === "merged" ? 1.15 : 1) * (1 - .8 * fade), dust: index >= 500, ...historyPosition(agent.id, center) };
    });
}

export type Aggregation<T> = { visible: T[]; cluster: T[]; clusterPreview: T[]; clusterLabel?: string };
export function aggregatePRs(prs: PullRequest[], agents: Agent[], now = Date.now()): Aggregation<PullRequest> {
  const score = (pr: PullRequest) => pr.ready ? 4 : agents.some((agent) => agent.prNumber === pr.number && agent.roleName === "verifier" && agentVisibility(agent, now) === "orbit") ? 3 : now - Date.parse(pr.updatedAt || "") >= 7 * 86400000 ? 1 : 2;
  const ordered = prs.filter(prVisible).sort((a, b) => score(b) - score(a) || b.number - a.number);
  const visible = ordered.length > 9 ? ordered.slice(0, 8) : ordered;
  const cluster = ordered.length > 9 ? ordered.slice(8) : [];
  return { visible, cluster, clusterPreview: cluster.slice(0, 6), clusterLabel: cluster.length ? `还有 ${cluster.length} 个 PR` : undefined };
}
export function aggregateIssues(issues: Signal[], agents: Agent[], now = Date.now()): Aggregation<Signal> {
  const ordered = issues.filter((issue) => issueVisible(issue, agents, now)).sort((a, b) => Number(a.claimed) - Number(b.claimed) || Date.parse(b.createdAt) - Date.parse(a.createdAt));
  const visible = ordered.length > 6 ? ordered.slice(0, 5) : ordered;
  const cluster = ordered.length > 6 ? ordered.slice(5) : [];
  return { visible, cluster, clusterPreview: cluster.slice(0, 6), clusterLabel: cluster.length ? `还有 ${cluster.length} 个 Issue` : undefined };
}
export const LABEL_PRIORITY = { decision: 1, failed: 2, cluster: 3, active: 4, verifying: 5, unclaimedIssue: 6, pr: 7, idle: 8, claimedIssue: 9 } as const;
export type LabelKind = keyof typeof LABEL_PRIORITY;
export const labelPriority = (kind: LabelKind) => LABEL_PRIORITY[kind];
