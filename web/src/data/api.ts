import { hiveAPI, request } from "./http";
import type {
  PullRequest,
  QueueItem,
  Signal,
  MethodologyEvent,
  ProjectSnapshot,
} from "./types";
export { HiveAPIError as APIError } from "./http";
const project = (id: string) => `/hives/${encodeURIComponent(id)}`;
// Every old page operation remains available here, including credentials, setup,
// tokens, usage, conversation pagination, PR details and methodology versions.
export const api = {
  ...hiveAPI,
  listPRs: (id: string, state: "open" | "all" = "all", signal?: AbortSignal) =>
    request<{ prs: PullRequest[] }>(
      `${project(id)}/prs?state=${state}`,
      "GET",
      undefined,
      signal,
    ),
  listQueue: (id: string, signal?: AbortSignal) =>
    request<{ queue: QueueItem[] }>(
      `${project(id)}/queue`,
      "GET",
      undefined,
      signal,
    ),
  listSignals: (id: string, since?: string, signal?: AbortSignal) =>
    request<{ signals: Signal[] }>(
      `${project(id)}/signals${since ? `?since=${encodeURIComponent(since)}` : ""}`,
      "GET",
      undefined,
      signal,
    ),
  listMethodologyEvents: (id: string, limit = 50, signal?: AbortSignal) =>
    request<{ events: MethodologyEvent[] }>(
      `${project(id)}/methodology/events?limit=${limit}`,
      "GET",
      undefined,
      signal,
    ),
  mergePR: (
    id: string,
    number: number,
    headSha: string,
    signal?: AbortSignal,
  ) =>
    request<{
      merged: boolean;
      queued: boolean;
      method: string;
      headSha: string;
      ready: boolean;
      reasons: string[];
      message: string;
    }>(`${project(id)}/prs/${number}/merge`, "POST", { headSha }, signal),
  activateMethodology: (
    id: string,
    version: number,
    signal?: AbortSignal,
    note?: string,
  ) =>
    request<import("./types").MethodologyVersion>(
      `${project(id)}/methodology/activate`,
      "POST",
      { version, note },
      signal,
    ),
  rollbackMethodology: (id: string, version: number, note?: string) =>
    request<import("./types").MethodologyVersion>(
      `${project(id)}/methodology/activate`,
      "POST",
      { version, note },
    ),
  talkToQueen: (id: string, body: string) =>
    hiveAPI.sendMessage(id, { to: "queen", body }),
  interject: (id: string, agentId: string, body: string, chainId?: string) =>
    hiveAPI.sendMessage(id, { to: `forager:${agentId}`, body, chainId }),
  assignIssue: (id: string, number: number) =>
    hiveAPI.sendMessage(id, { to: "queen", body: `请处理 Issue #${number}` }),
};
export type DataAPI = typeof api;
export type ResourceName = "hive" | "agents" | "queen" | "prs" | "queue" | "signals" | "methodology" | "methodologyEvents" | "messages";
export type ResourceUpdate = (name: ResourceName, patch?: Partial<ProjectSnapshot>, error?: unknown) => void;

// Inbox pagination is oldest-first. Drain each inbox so a busy project doesn't
// silently show only its first 50 messages. Preserve full bodies for panels.
async function inbox(id: string, to: string, signal?: AbortSignal) {
  const messages: import("./types").Message[] = [];
  let after: string | undefined;
  do {
    const page = await api.listMessages(id, { to, after, limit: 200 }, signal);
    messages.push(...page.messages);
    if (!page.truncated || !page.cursor || page.cursor === after) break;
    after = page.cursor;
  } while (!signal?.aborted);
  return messages;
}
export const isActiveAgent = (agent: import("./types").Agent) =>
  ["creating", "active", "parked", "hibernated"].includes(agent.state) ||
  (agent.state === "failed" && Date.now() - Date.parse(agent.updatedAt) < 86400000);

/** Full message bodies are fetched only for a selected historical Agent. */
export function fetchAgentMessages(id: string, agentId: string, signal?: AbortSignal) {
  return inbox(id, `forager:${agentId}`, signal);
}

/** Each resource publishes as soon as its own request settles. */
export async function loadProjectResources(id: string, update: ResourceUpdate, signal?: AbortSignal): Promise<void> {
  const run = async <T>(name: ResourceName, load: () => Promise<T>, patch: (value: T) => Partial<ProjectSnapshot>) => {
    try { update(name, patch(await load())); }
    catch (error) { if (!signal?.aborted) update(name, undefined, error); }
  };
  const detail = api.getHive(id, signal);
  const foragers = api.listForagers(id, signal);
  const independent = [
    run("hive", () => detail, (value) => ({ hive: value.hive, localAddress: value.localAddress })),
    run("agents", () => foragers, (value) => ({ agents: value.foragers, queenAgent: value.queen, localAddress: value.localAddress })),
    run("queen", () => api.getQueen(id, signal), (queen) => ({ queen })),
    run("prs", () => api.listPRs(id, "all", signal), (value) => ({ prs: value.prs })),
    run("queue", () => api.listQueue(id, signal), (value) => ({ queue: value.queue })),
    run("signals", () => api.listSignals(id, undefined, signal), (value) => ({ signals: value.signals })),
    run("methodology", () => api.getMethodology(id, undefined, signal), (methodology) => ({ methodology })),
    run("methodologyEvents", () => api.listMethodologyEvents(id, 50, signal), (value) => ({ methodologyEvents: value.events })),
  ];
  const messages = run("messages", async () => {
    const [hive, agents] = await Promise.all([detail, foragers]);
    const addresses = ["queen", "hive", hive.localAddress, ...agents.foragers.filter(isActiveAgent).map((agent) => `forager:${agent.id}`)];
    const pages = await Promise.all([...new Set(addresses)].filter(Boolean).map((to) => inbox(id, to, signal)));
    return [...new Map(pages.flat().map((message) => [message.id, message])).values()]
      .sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt));
  }, (messages) => ({ messages }));
  await Promise.all([...independent, messages]);
}
export async function fetchProjectSnapshot(
  id: string,
  signal?: AbortSignal,
): Promise<ProjectSnapshot> {
  let partial: Partial<ProjectSnapshot> = {};
  let failure: unknown;
  await loadProjectResources(id, (_name, patch, error) => {
    if (patch) partial = { ...partial, ...patch };
    if (error && !failure) failure = error;
  }, signal);
  if (failure) throw failure;
  return partial as ProjectSnapshot;
}
