import { api, APIError } from "./api";

export type Assignee = { id: number; name: string; username?: string; avatarUrl?: string };
export type Note = { id: number; body: string; author?: Assignee; createdAt: string; updatedAt?: string; system?: boolean };
export type WorkbenchIssue = {
  projectId: string; repositoryId: string; gitlabProjectId: number; iid: number;
  title: string; description: string; state: string; labels: string[];
  assignees: Assignee[]; webUrl: string; updatedAt: string; taskIds: string[];
  notes?: Note[]; tasks?: { id: string; stage: string; round?: number; createdAt?: string; triggerNoteIds?: number[]; workspace?: { revisions?: { repositoryId?: string; path?: string; mrUrl?: string }[] } }[];
};
export type WorkbenchMR = {
  projectId: string; repositoryId: string; gitlabProjectId: number; iid: number;
  title: string; description: string; state: string; webUrl: string; updatedAt: string;
  labels?: string[]; assignees?: Assignee[];
  sourceBranch: string; targetBranch: string; sha: string; mergeStatus?: string;
  draft?: boolean; taskId?: string; approved?: boolean; approvals?: { username: string; name?: string }[];
  diffRefs?: { baseSha: string; startSha: string; headSha: string };
  diffs?: MRDiff[]; discussions?: Discussion[]; pipelines?: Pipeline[];
  diffTruncated?: boolean; diffError?: string;
};
export type MRDiff = { oldPath: string; newPath: string; diff: string; newFile?: boolean; deletedFile?: boolean; renamedFile?: boolean; tooLarge?: boolean; collapsed?: boolean };
export type Discussion = { id: string; notes: Note[]; resolved?: boolean; resolvable?: boolean; position?: { newPath?: string; oldPath?: string; newLine?: number; oldLine?: number } };
export type Pipeline = { id: number; status: string; webUrl?: string; ref?: string; sha?: string };
export type Page<T> = { items: T[]; nextCursor?: string };
export type DiffPage = { diffs: MRDiff[]; hasNext: boolean; incomplete: boolean; page: number; version: { baseCommitSha: string; startCommitSha: string; headCommitSha: string } };
export const workbenchPath = (kind: "issues" | "merge-requests", projectId: string, repositoryId: string, iid: number) =>
  `/workbench/${kind}/${encodeURIComponent(projectId)}/${encodeURIComponent(repositoryId)}/${iid}`;
export const detailHash = (kind: "issues" | "merge-requests", projectId: string, repositoryId: string, iid: number) =>
  `#/${kind}/${encodeURIComponent(projectId)}/${encodeURIComponent(repositoryId)}/${iid}`;

export async function uploadMarkdown(path: string, file: File): Promise<{ markdown: string; url?: string }> {
  const body = new FormData();
  body.append("file", file);
  const response = await fetch(`/api${path}/uploads`, {
    method: "POST", credentials: "same-origin", headers: { "X-Requested-With": "swarmjet" }, body,
  });
  if (!response.ok) {
    let message = `上传失败 (${response.status})`;
    try { message = (await response.json()).error || message; } catch { /* server response has no JSON */ }
    if (response.status === 401) window.dispatchEvent(new Event("session-expired"));
    throw new APIError(message, response.status);
  }
  return response.json();
}

export function getProjects() {
  return api<import("./types").Project[] | null>("/projects").then(items => (items || []).filter(p => p.provider !== "github"));
}

type Raw = Record<string, any>;
const normalizeNote = (note: Raw): Note => ({ id: note.id, body: note.body || "", author: note.author, system: note.system, createdAt: note.createdAt || note.created_at, updatedAt: note.updatedAt || note.updated_at });
export function normalizeIssue(raw: WorkbenchIssue & Raw): WorkbenchIssue {
  return { ...raw, webUrl: raw.webUrl || raw.web_url, updatedAt: raw.updatedAt || raw.updated_at, labels: raw.labels || [], assignees: raw.assignees || [], taskIds: raw.taskIds || [], notes: raw.notes?.map(normalizeNote) };
}
export function normalizeMR(raw: WorkbenchMR & Raw): WorkbenchMR {
  const approvals = raw.approvals as Raw | undefined;
  return { ...raw, webUrl: raw.webUrl || raw.web_url, updatedAt: raw.updatedAt || raw.updated_at, sourceBranch: raw.sourceBranch || raw.source_branch, targetBranch: raw.targetBranch || raw.target_branch,
    mergeStatus: raw.detailedMergeStatus || raw.detailed_merge_status || raw.merge_status,
    approved: Boolean(raw.approvedByCurrentUser),
    approvals: approvals?.approved_by?.map((entry: Raw) => ({ username: entry.user?.username || "", name: entry.user?.name })),
    discussions: raw.discussions?.map((d: Raw): Discussion => { const pos = d.notes?.find((n: Raw) => n.position)?.position; return { id: d.id, notes: (d.notes || []).map(normalizeNote), resolvable: d.notes?.some((n: Raw) => n.resolvable), resolved: d.notes?.some((n: Raw) => n.resolved), position: pos ? { oldPath: pos.old_path, newPath: pos.new_path, oldLine: pos.old_line, newLine: pos.new_line } : undefined }; }),
    pipelines: raw.pipelines?.map((p: Raw): Pipeline => ({ id: p.id, status: p.status, ref: p.ref, sha: p.sha, webUrl: p.webUrl || p.web_url })),
  };
}
export function normalizeDiffPage(raw: DiffPage & Raw): DiffPage {
  return { ...raw, diffs: (raw.diffs || []).map((d: Raw): MRDiff => ({ diff: d.diff || "", oldPath: d.oldPath || d.old_path, newPath: d.newPath || d.new_path, tooLarge: d.tooLarge || d.too_large, collapsed: d.collapsed })), };
}
