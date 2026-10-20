import { hiveAPI, request } from '../hive/api';
import type { PullRequest, QueueItem, Signal, MethodologyEvent, ProjectSnapshot } from './types';
export { HiveAPIError as APIError } from '../hive/api';
const project = (id: string) => `/hives/${encodeURIComponent(id)}`;
// Every old page operation remains available here, including credentials, setup,
// tokens, usage, conversation pagination, PR details and methodology versions.
export const api = {
  ...hiveAPI,
  listPRs: (id: string, state: 'open' | 'all' = 'all', signal?: AbortSignal) => request<{prs: PullRequest[]}>(`${project(id)}/prs?state=${state}`, 'GET', undefined, signal),
  listQueue: (id: string, signal?: AbortSignal) => request<{queue: QueueItem[]}>(`${project(id)}/queue`, 'GET', undefined, signal),
  listSignals: (id: string, since?: string, signal?: AbortSignal) => request<{signals: Signal[]}>(`${project(id)}/signals${since ? `?since=${encodeURIComponent(since)}` : ''}`, 'GET', undefined, signal),
  listMethodologyEvents: (id: string, limit = 50, signal?: AbortSignal) => request<{events: MethodologyEvent[]}>(`${project(id)}/methodology/events?limit=${limit}`, 'GET', undefined, signal),
  mergePR: (id: string, number: number, headSha: string, signal?: AbortSignal) => request<{merged: boolean; queued: boolean; method: string; headSha: string; ready: boolean; reasons: string[]; message: string}>(`${project(id)}/prs/${number}/merge`, 'POST', {headSha}, signal),
  activateMethodology: (id: string, version: number, signal?: AbortSignal, note?: string) => request<import('./types').MethodologyVersion>(`${project(id)}/methodology/activate`, 'POST', {version, note}, signal),
  rollbackMethodology: (id: string, version: number, note?: string) => request<import('./types').MethodologyVersion>(`${project(id)}/methodology/activate`, 'POST', {version, note}),
  talkToQueen: (id: string, body: string) => hiveAPI.sendMessage(id, {to: 'queen', body}),
  interject: (id: string, agentId: string, body: string, chainId?: string) => hiveAPI.sendMessage(id, {to: `forager:${agentId}`, body, chainId}),
  assignIssue: (id: string, number: number) => hiveAPI.sendMessage(id, {to: 'queen', body: `请处理 Issue #${number}`}),
};
export type DataAPI = typeof api;

// Inbox pagination is oldest-first. Drain each inbox so a busy project doesn't
// silently show only its first 50 messages. Preserve full bodies for panels.
async function inbox(id: string, to: string, signal?: AbortSignal) {
  const messages: import('./types').Message[] = [];
  let after: string | undefined;
  do {
    const page = await api.listMessages(id, {to, after, limit: 200}, signal);
    messages.push(...page.messages);
    if (!page.truncated || !page.cursor || page.cursor === after) break;
    after = page.cursor;
  } while (!signal?.aborted);
  return messages;
}
export async function fetchProjectSnapshot(id: string, signal?: AbortSignal): Promise<ProjectSnapshot> {
  const [detail, agents, queen, prs, queue, signals, methodology, events] = await Promise.all([
    api.getHive(id, signal), api.listForagers(id, signal), api.getQueen(id, signal), api.listPRs(id, 'all', signal), api.listQueue(id, signal), api.listSignals(id, undefined, signal), api.getMethodology(id, undefined, signal), api.listMethodologyEvents(id, 50, signal),
  ]);
  const addresses = ['queen', 'hive', detail.localAddress, ...agents.foragers.map(a => `forager:${a.id}`)];
  const pages = await Promise.all([...new Set(addresses)].filter(Boolean).map(to => inbox(id, to, signal)));
  const messages = [...new Map(pages.flat().map(m => [m.id, m])).values()].sort((a,b) => a.createdAt.localeCompare(b.createdAt));
  return {hive: detail.hive, localAddress: detail.localAddress, agents: agents.foragers, queen, queenAgent: agents.queen, prs: prs.prs, queue: queue.queue, signals: signals.signals, methodology, methodologyEvents: events.events, messages};
}
