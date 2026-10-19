// Domain types for the Hive / Queen / Forager console (ADR docs/claude/adr-hive-queen-forager.md).
// Field names mirror the Go contract in docs/claude/round1-prompts.md as camelCase JSON.
// The UI talks to /api; the fake fixtures in web/src/fixtures are used by Playwright only.

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
  verify?: VerifyConfig;
};

/** Round-4 verification and merge policy. Missing fields use the server defaults. */
export type VerifyConfig = {
  mode?: "on" | "off" | string;
  model?: string;
  maxRounds?: number;
  autoMerge?: boolean;
  mergeMethod?: "squash" | "merge" | "rebase" | string;
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

// ---- Round-4 verdicts and merge readiness ----

export type VerdictResult = "pass" | "pass_notes" | "fail" | "blocked";

export type Finding = {
  severity: "critical" | "warning" | "nit" | string;
  location?: string;
  finding: string;
  evidence?: string;
};

export type Verdict = {
  id: string;
  hiveId: string;
  prNumber: number;
  headSha: string;
  ownerId?: string;
  verifierId?: string;
  result: VerdictResult;
  summary: string;
  findings?: Finding[];
  commentUrl?: string;
  superseded: boolean;
  createdAt: string;
};

export type MergeCheck = {
  name: string;
  status: string;
  conclusion: string;
  url?: string;
  output?: string;
  outputTruncated?: boolean;
  untrusted?: boolean;
};

export type MergePull = {
  number: number;
  state: string;
  draft: boolean;
  merged: boolean;
  url?: string;
  headSha?: string;
  baseRef?: string;
  baseRepository?: string;
  mergeable?: boolean | null;
  mergeableState?: string;
};

export type MergeReadiness = {
  ready: boolean;
  headSha?: string;
  pull: MergePull;
  verdict?: Verdict;
  verdictFound?: boolean;
  checks: MergeCheck[];
  reasons: string[];
};

export type ForagerMergeResponse = {
  foragerId: string;
  hiveId: string;
  prNumber: number;
  readiness?: MergeReadiness | null;
  verdicts: Verdict[];
  error?: string;
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

// ---- Session and onboarding (round 3, public contract section 5) ----

export type Principal = {
  id: string;
  username: string;
  email?: string;
  displayName?: string;
  platformAdmin?: boolean;
  breakGlass?: boolean;
};

export type GithubSetup = {
  configured: boolean;
  source: "env" | "database" | "";
  appId: number;
  appSlug: string;
  htmlUrl: string;
  installUrl: string;
  installations: number;
};

export type TunnelSetup = {
  enabled: boolean;
  url: string;
  webhookSynced: boolean;
  lastError: string;
};

export type CellsSetup = {
  enabled: boolean;
  image: string;
  model: string;
  dockerAvailable: boolean;
};

export type CliLoginSetup = {
  available: boolean;
  loggedIn: boolean;
  cli: string;
};

export type SetupStatus = {
  github: GithubSetup;
  tunnel: TunnelSetup;
  cells: CellsSetup;
  cliLogin: CliLoginSetup;
};

export type GithubRepository = {
  fullName: string;
  private: boolean;
  defaultBranch: string;
};

export type TokenScope = "default" | "admin";

export type TokenSummary = {
  id: string;
  name: string;
  scope: TokenScope;
  createdAt: string;
};

export type TokenConnect = {
  mcpUrl: string;
  claudeCode: string;
  codex: string;
  json: unknown;
};

export type CreatedToken = {
  token: TokenSummary;
  plaintext: string;
  connect: TokenConnect;
};
