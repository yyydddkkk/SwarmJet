import type { Note } from "./workbench-api";

export type IssueNotePresentation = {
  kind: "comment" | "followup" | "review" | "progress" | "activity";
  body: string;
  taskId?: string;
  round?: number;
  title?: string;
};

// These match the prefixes written by GitLab Client.UpsertIssueReview and
// UpsertProgress. Unknown HTML comments remain ordinary note content.
const reviewPrefix = /^<!-- swarmjet-review:([^:\r\n<>]+):[0-9a-f]{64} -->\r?\n/;
const progressPrefix = /^<!-- swarmjet-progress:[0-9a-f]{64} -->\r?\n/;
const reviewHeading = /^## 人工复核指引 · SwarmJet 第 (\d+) 轮(?:\r?\n|$)/;
const progressHeading = /^\*\*([^\r\n*]+)\*\* · ([^\r\n]+)(?:\r?\n|$)/;
const followupPrefix = /^\s*\/swarmjet run(?=$|[ \t\r\n])/;

export function presentIssueNote(note: Note): IssueNotePresentation {
  const body = note.body;
  if (note.system) return { kind: "activity", body };

  const review = reviewPrefix.exec(body);
  if (review) {
    const content = body.slice(review[0].length);
    const round = reviewHeading.exec(content)?.[1];
    return {
      kind: "review",
      body: content,
      taskId: review[1],
      title: "人工复核指引",
      ...(round ? { round: Number(round) } : {}),
    };
  }

  const progress = progressPrefix.exec(body);
  if (progress) {
    const content = body.slice(progress[0].length);
    const heading = progressHeading.exec(content);
    const round = heading?.[2].match(/ · 第 (\d+) 轮$/)?.[1];
    const taskId = heading?.[2].replace(/ · 第 \d+ 轮$/, "");
    return {
      kind: "progress",
      body: content,
      ...(heading ? { title: heading[1] } : {}),
      ...(round ? { round: Number(round) } : {}),
      ...(taskId ? { taskId } : {}),
    };
  }

  const followup = followupPrefix.exec(body);
  if (followup) {
    const rest = body.slice(followup[0].length);
    // The command's first separator belongs to the command. Everything after
    // it is the user's Markdown, including subsequent whitespace and lines.
    return { kind: "followup", body: rest.replace(/^[ \t\r\n]/, ""), title: "下一轮补充需求" };
  }

  return { kind: "comment", body };
}
