// Domain types for the Hive / Queen / Forager console (ADR docs/claude/adr-hive-queen-drone.md).
// Field names mirror the Go contract in docs/claude/round1-prompts.md as camelCase JSON.
// This round is front-end only: the pages below read the fake fixtures in web/src/fixtures.

export type HiveConfig = {
  cli: string;
  maxConcurrentForagers: number;
  maxHops: number;
  maxForagersPerChain: number;
  idleParkMinutes: number;
  hibernateHours: number;
  turnTimeoutMinutes: number;
  foragerMaxLifetimeHours: number;
  triggers: TriggerRule[];
};

export type TriggerEvent = "issue_labeled" | "issue_comment";

export type TriggerRule = {
  event: TriggerEvent;
  label?: string;
  commentPrefix?: string;
  preset?: string;
};

export type Hive = {
  id: string;
  name: string;
  repository: string;
  queenEnabled: boolean;
  config: HiveConfig;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type AgentRole = "queen" | "forager";

export type AgentState =
  | "creating"
  | "active"
  | "parked"
  | "hibernated"
  | "completed"
  | "failed"
  | "cancelled"
  | "disabled";

export type ArtifactKind = "branch" | "commit" | "pr" | "report" | "handoff";

export type ArtifactRef = {
  kind: ArtifactKind;
  ref: string;
};

export type Handoff = {
  goal: string;
  done: string;
  remaining: string;
  risks: string;
  next: string;
  artifacts?: ArtifactRef[];
};

export type Agent = {
  id: string;
  hiveId: string;
  role: AgentRole;
  state: AgentState;
  preset?: string;
  cli: string;
  task?: string;
  branch?: string;
  issueNumber?: number;
  prNumber?: number;
  nodeId?: string;
  chainId?: string;
  handoff?: Handoff;
  createdAt: string;
  updatedAt: string;
};

export type MessageKind =
  | "event"
  | "task"
  | "report"
  | "question"
  | "reply"
  | "control";

export type MessageState = "pending" | "delivered" | "handled";

export type Message = {
  id: string;
  hiveId: string;
  from: string;
  to: string;
  kind: MessageKind;
  chainId: string;
  hop: number;
  body: string;
  artifacts?: ArtifactRef[];
  state: MessageState;
  createdAt: string;
};

// ---- View-only shapes (not part of the backend contract) ----

/** Aggregates shown on a Hive card (ADR section 18). */
export type HiveOverview = {
  hive: Hive;
  activeForagers: number;
  pendingApprovals: number;
  inboxBacklog: number;
};

export type QueenActionKind = "spawn_forager" | "cancel_forager" | "request_approval";

/** A structured card for a Queen action inside the conversation stream. */
export type QueenAction = {
  id: string;
  kind: QueenActionKind;
  title: string;
  summary: string;
  at: string;
  details: { label: string; value: string }[];
  artifact?: ArtifactRef;
};

/** A GitHub event, surfaced as a card with its source marked. */
export type ExternalEvent = {
  id: string;
  source: "github";
  event: string;
  action: string;
  title: string;
  url: string;
  excerpt: string;
  at: string;
};

export type ConversationEntry =
  | { id: string; kind: "message"; at: string; message: Message }
  | { id: string; kind: "action"; at: string; action: QueenAction }
  | { id: string; kind: "event"; at: string; event: ExternalEvent };

export type CheckStatus = "passed" | "failed" | "pending";

export type Check = {
  name: string;
  status: CheckStatus;
  detail: string;
};

export type ForagerDetail = {
  agent: Agent;
  ci: Check[];
  guards: Check[];
  diff: { files: number; additions: number; deletions: number };
  conversation: ConversationEntry[];
};
