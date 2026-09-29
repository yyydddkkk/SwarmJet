import { useMemo, useState } from "react";
import MarkdownEditor, { MarkdownContent } from "./MarkdownEditor";
import { presentIssueNote, type IssueNotePresentation } from "./issue-note-presentation";
import { taskStageLabel } from "./task-presentation";
import type { Note, WorkbenchIssue } from "./workbench-api";
import "./issue-conversation.css";

type Filter = "all" | "comment" | "swarmjet" | "activity";
type RoundTask = NonNullable<WorkbenchIssue["tasks"]>[number];
type Entry = { note: Note; view: IssueNotePresentation; group: string; task?: RoundTask };

const date = (value: string) => value ? new Date(value).toLocaleString("zh-CN") : "—";
const shortId = (value: string) => value.slice(0, 8);
const noteAuthor = (note: Note) => note.author?.name || note.author?.username || "GitLab 用户";

function noteGroup(note: Note, view: IssueNotePresentation, tasks: RoundTask[]): { group: string; task?: RoundTask } {
  const task = tasks.find(item => item.id === view.taskId || item.triggerNoteIds?.includes(note.id));
  if (task?.round) return { group: `round:${task.round}`, task };
  if (view.round) return { group: `round:${view.round}` };
  if (view.kind === "followup") return { group: "pending" };
  const created = Date.parse(note.createdAt);
  if (Number.isFinite(created)) {
    const during = [...tasks].filter(item => item.round && item.createdAt && Date.parse(item.createdAt) <= created)
      .sort((a, b) => Date.parse(b.createdAt || "") - Date.parse(a.createdAt || ""))[0];
    if (during) return { group: `round:${during.round}`, task: during };
  }
  return { group: "general" };
}

function groupLabel(group: string) {
  if (group === "pending") return "等待下一轮";
  if (group === "general") return "其他讨论";
  return `第 ${group.split(":")[1]} 轮期间`;
}

function groupOrder(group: string) {
  if (group === "pending") return Number.POSITIVE_INFINITY;
  if (group === "general") return -1;
  return Number(group.split(":")[1]);
}

function reviewStepCount(body: string) {
  return (body.match(/^\d+\.\s/gm) || []).length;
}

function structuredBody(view: IssueNotePresentation) {
  if (view.kind === "progress") return view.body.replace(/^\*\*[^\r\n*]+\*\* · [^\r\n]+\r?\n(?:\r?\n)?/, "");
  if (view.kind === "review") {
    const withoutHeading = view.body.replace(/^## 人工复核指引 · SwarmJet 第 \d+ 轮\r?\n(?:\r?\n)?/, "");
    const withShortTask = view.taskId ? withoutHeading.replaceAll(`\`${view.taskId}\``, `\`${shortId(view.taskId)}\``) : withoutHeading;
    const shortHashes = withShortTask.replace(/`([0-9a-f]{16,64})`/gi, (_, hash: string) => `\`${shortId(hash)}\``);
    return shortHashes.replace(/(^- [^：\r\n]+：)(https?:\/\/[^\s]+)( · )/gm, (_, lead: string, url: string, separator: string) => {
      const iid = /\/merge_requests\/(\d+)/.exec(url)?.[1];
      return `${lead}[MR${iid ? ` !${iid}` : " 链接"}](${url})${separator}`;
    });
  }
  return view.body;
}

function systemActivity(body: string) {
  const commit = /^mentioned in commit ([0-9a-f]{12,40})$/.exec(body.trim());
  return commit ? `在提交 ${shortId(commit[1])} 中提及此 Issue` : body;
}

function safeMRLinks(task?: RoundTask) {
  return (task?.workspace?.revisions || []).flatMap(revision => {
    if (!revision.mrUrl) return [];
    try {
      const url = new URL(revision.mrUrl);
      if (url.protocol !== "http:" && url.protocol !== "https:") return [];
      const iid = /\/merge_requests\/(\d+)/.exec(url.pathname)?.[1];
      return [{ url: url.href, label: `${revision.path || revision.repositoryId || "仓库"} MR${iid ? ` !${iid}` : ""}` }];
    } catch { return []; }
  });
}

function NoteSource({ body }: { body: string }) {
  return <details className="issue-note-source"><summary>查看原始评论</summary><pre>{body}</pre></details>;
}

function ConversationEntry({ entry, issue, path, active }: { entry: Entry; issue: WorkbenchIssue; path: string; active: boolean }) {
  const { note, view, task } = entry;
  const author = noteAuthor(note);
  if (view.kind === "activity") return <article className="issue-activity" aria-label="系统活动">
    <span className="issue-activity-dot" aria-hidden="true" /><span><strong>{author}</strong> {systemActivity(view.body)}</span><time dateTime={note.createdAt}>{date(note.createdAt)}</time>
  </article>;

  const isSwarmJet = view.kind === "review" || view.kind === "progress";
  const title = view.kind === "review" ? `人工复核指引${view.round ? ` · 第 ${view.round} 轮` : ""}`
    : view.kind === "progress" ? `任务进展${view.title ? ` · ${taskStageLabel(view.title)}` : ""}` : view.kind === "followup" ? "下一轮补充需求" : "";
  const status = view.kind === "followup" ? task?.round ? `已纳入第 ${task.round} 轮` : active ? "排队等待当前轮结束" : "等待系统创建任务" : "";
  const mrLinks = view.kind === "review" ? safeMRLinks(task) : [];
  const displayBody = structuredBody(view);
  const superseded = view.kind === "review" && view.body.includes("此版本已失效");
  const longProgress = view.kind === "progress" && (displayBody.length > 240 || displayBody.split("\n").length > 7);
  return <article className={`issue-note issue-note-${view.kind}`}>
    <header className="issue-note-header">
      <span className={`issue-note-avatar${isSwarmJet ? " issue-note-avatar-system" : ""}`} aria-hidden="true">{isSwarmJet ? "S" : author.slice(0, 1)}</span>
      <div className="issue-note-author"><strong>{author}</strong>{title && <span className="issue-note-kind">{title}</span>}{status && <span className="issue-note-status">{status}</span>}</div>
      <time dateTime={note.createdAt}>{date(note.createdAt)}</time>
    </header>
    {view.kind === "review" ? <div className="issue-review-summary">
      <p>{superseded ? "此版本已失效，请查看最新复核指引。" : `本轮复核指引${reviewStepCount(view.body) ? ` · ${reviewStepCount(view.body)} 个操作步骤` : ""}。合并状态以关联 MR 为准。`}</p>
      <div className="issue-review-links">{view.taskId && <a href={`#/tasks/${encodeURIComponent(view.taskId)}`}>查看任务 {shortId(view.taskId)}</a>}{mrLinks.map(link => <a key={link.url} href={link.url} target="_blank" rel="noreferrer">{link.label}</a>)}</div>
      <details className="issue-note-expanded"><summary>查看完整复核指引</summary><MarkdownContent value={displayBody} baseUrl={issue.webUrl} uploadPath={path} /></details>
    </div> : longProgress ? <details className="issue-note-expanded"><summary>查看完整进展记录</summary><MarkdownContent value={displayBody} baseUrl={issue.webUrl} uploadPath={path} /></details>
      : <div className="issue-note-body"><MarkdownContent value={displayBody} baseUrl={issue.webUrl} uploadPath={path} /></div>}
    {view.kind === "progress" && view.taskId && <div className="issue-progress-task"><a href={`#/tasks/${encodeURIComponent(view.taskId)}`}>查看任务 {shortId(view.taskId)}</a></div>}
    {isSwarmJet && <NoteSource body={note.body} />}
  </article>;
}

export default function IssueConversation({ issue, path, note, setNote, busy, canOperate, canRequestNextRound, hasActiveTask, hasPreviousTask, submitNote, requestCloseIssue }: {
  issue: WorkbenchIssue;
  path: string;
  note: string;
  setNote: (value: string) => void;
  busy: boolean;
  canOperate: boolean;
  canRequestNextRound: boolean;
  hasActiveTask: boolean;
  hasPreviousTask: boolean;
  submitNote: (startNextRound: boolean) => void;
  requestCloseIssue: () => void;
}) {
  const [filter, setFilter] = useState<Filter>("all");
  const entries = useMemo(() => [...(issue.notes || [])].map(note => {
    const view = presentIssueNote(note);
    return { note, view, ...noteGroup(note, view, issue.tasks || []) };
  }).sort((a, b) => Date.parse(b.note.createdAt) - Date.parse(a.note.createdAt) || b.note.id - a.note.id), [issue.notes, issue.tasks]);
  const counts = {
    all: entries.length,
    comment: entries.filter(entry => entry.view.kind === "comment" || entry.view.kind === "followup").length,
    swarmjet: entries.filter(entry => entry.view.kind === "review" || entry.view.kind === "progress").length,
    activity: entries.filter(entry => entry.view.kind === "activity").length,
  };
  const visible = entries.filter(entry => filter === "all" || filter === "comment" && (entry.view.kind === "comment" || entry.view.kind === "followup")
    || filter === "swarmjet" && (entry.view.kind === "review" || entry.view.kind === "progress") || filter === "activity" && entry.view.kind === "activity");
  const groups = new Map<string, Entry[]>();
  for (const entry of visible) groups.set(entry.group, [...(groups.get(entry.group) || []), entry]);

  return <section className="panel workbench-comments"><div className="panel-head workbench-discussion-head"><div><h2>讨论与流转记录</h2><p>补充需求、任务进展和系统活动集中在这里。</p></div></div>
    <div className="workbench-pad"><div className="issue-conversation-width">
      {canOperate && <div className="issue-comment-composer"><MarkdownEditor value={note} onChange={setNote} uploadPath={path} baseUrl={issue.webUrl} label="评论内容或补充需求" rows={4} />
        <div className="form-actions"><button disabled={busy || !note.trim()} onClick={() => submitNote(false)}>发表评论</button>{issue.state !== "closed" && <button className="danger" disabled={busy || hasActiveTask} title={hasActiveTask ? "关联任务仍在进行中" : undefined} onClick={requestCloseIssue}>关闭 Issue</button>}<button className="primary" disabled={busy || !note.trim() || !canRequestNextRound} onClick={() => submitNote(true)}>评论并发起下一轮</button></div>
        {hasActiveTask && canRequestNextRound && <small>当前轮仍在运行，补充需求会排队等待本轮结束后创建下一轮任务。</small>}
        {!hasPreviousTask && <small>首次任务请先点击“开始任务”；完成后可评论并发起下一轮。</small>}
        {issue.state === "closed" && <small>请先重开 Issue，再评论并发起下一轮。</small>}
        {hasPreviousTask && !(issue.labels || []).includes("autonomous") && <small>发起下一轮需要 autonomous 标签。</small>}
      </div>}
      <div className="issue-conversation-toolbar"><strong>记录</strong><div className="issue-note-filters" role="group" aria-label="筛选记录">{([
        ["all", "全部"], ["comment", "讨论"], ["swarmjet", "SwarmJet"], ["activity", "系统活动"],
      ] as const).map(([value, label]) => <button key={value} type="button" aria-pressed={filter === value} onClick={() => setFilter(value)}>{label}<span>{counts[value]}</span></button>)}</div></div>
      {visible.length ? [...groups.entries()].sort(([a], [b]) => groupOrder(b) - groupOrder(a)).map(([group, items]) => <section className="issue-note-group" key={group} aria-label={groupLabel(group)}><h3>{groupLabel(group)}</h3>{items.map(entry => <ConversationEntry key={entry.note.id} entry={entry} issue={issue} path={path} active={hasActiveTask} />)}</section>)
        : <p className="workbench-muted issue-notes-empty">{entries.length ? "此类别暂无记录。" : "暂无记录。"}</p>}
    </div></div>
  </section>;
}
