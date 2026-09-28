import { expect, test } from "@playwright/test";
import { presentIssueNote } from "../src/issue-note-presentation";
import type { Note } from "../src/workbench-api";

const note = (body: string, system = false): Note => ({ id: 1, body, system, createdAt: "2026-09-28T00:00:00Z" });
const digest = "a".repeat(64);

test("recognizes a review guide and preserves its Markdown after the control marker", () => {
  const body = "## 人工复核指引 · SwarmJet 第 2 轮\n\n任务：`task-123`。\n\n### 本轮 MR\n- [MR !53](https://example.test/53)";
  expect(presentIssueNote(note(`<!-- swarmjet-review:task-123:${digest} -->\n${body}`))).toEqual({
    kind: "review", body, taskId: "task-123", round: 2, title: "人工复核指引",
  });
});

test("recognizes a progress note without exposing its opaque hash", () => {
  const body = "**implementing** · task-123 · 第 2 轮\n\n正在修改代码。\n- 详情";
  expect(presentIssueNote(note(`<!-- swarmjet-progress:${digest} -->\n${body}`))).toEqual({
    kind: "progress", body, title: "implementing", round: 2, taskId: "task-123",
  });
});

test("recognizes a follow-up command and keeps multiline user content", () => {
  expect(presentIssueNote(note(" /swarmjet run\n补充 `特殊` 空格\n\n- 再加一项"))).toEqual({
    kind: "followup", body: "补充 `特殊` 空格\n\n- 再加一项", title: "下一轮补充需求",
  });
  expect(presentIssueNote(note("/swarmjet run"))).toMatchObject({ kind: "followup", body: "" });
});

test("does not hide unrecognized or non-leading HTML comments and commands", () => {
  for (const body of [
    `前言\n<!-- swarmjet-review:task-123:${digest} -->\n内容`,
    "<!-- some-other-marker:value -->\n正文",
    "<!-- swarmjet-progress:x -->\n正文",
    "<!-- swarmjet-review:task-123:short -->\n正文",
    "说明 /swarmjet run 再试一次",
    "/swarmjet runner 再试一次",
  ]) {
    expect(presentIssueNote(note(body))).toEqual({ kind: "comment", body });
  }
});

test("system notes remain activities even if their text resembles a command", () => {
  const body = "/swarmjet run 系统活动";
  expect(presentIssueNote(note(body, true))).toEqual({ kind: "activity", body });
});
