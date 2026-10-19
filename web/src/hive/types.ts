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
  /** Methodology role that governs this Agent, e.g. "worker" or "verifier". */
  roleName?: string;
  /** Commit checked out detached for this Agent, if any. */
  revision?: string;
  /** The Agent that spawned this one, if any. */
  parentId?: string;
  handoff?: Handoff;
  createdAt: string;
  updatedAt: string;
};

/** Where a message's content came from (round-4 contract section 5). */
export type MessageTrust = "user" | "agent" | "external" | "system";

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
  /** Provenance; an empty value from older rows means "agent". */
  trust?: MessageTrust;
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

/** Round 4 replaced the CLI login block with the model credential summary. */
export type ModelsSetup = {
  /** Whether at least one provider has a usable credential. */
  available: boolean;
  defaultModel: string;
};

export type SetupStatus = {
  github: GithubSetup;
  tunnel: TunnelSetup;
  cells: CellsSetup;
  models: ModelsSetup;
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

// ---- Round 4: model credentials, methodology, attestations and usage ----
//
// These shapes follow the round-4 public contract. The backend tasks land in
// parallel; until they merge the console reads them through /api and the
// Playwright mock supplies the fixtures.

export type ModelOption = {
  /** provider/model, e.g. "deepseek/deepseek-chat". */
  id: string;
  provider: string;
  name: string;
  contextWindow?: number;
  inputPerMillionUsd?: number;
  outputPerMillionUsd?: number;
  /** False when the provider has no credential yet. */
  available?: boolean;
};

export type ModelCredential = {
  provider: string;
  /** Masked by the server; the plaintext key never reaches the console. */
  maskedKey: string;
  updatedAt: string;
};

export type ModelsResponse = {
  models: ModelOption[];
  defaultModel: string;
  credentials: ModelCredential[];
};

/** The contract only promises the masked value, so accept both shapes. */
export type ModelCredentialResponse = {
  credential?: ModelCredential;
  provider?: string;
  maskedKey?: string;
  updatedAt?: string;
};

export type MethodologySource = {
  url: string;
  ref: string;
  path?: string;
  sha?: string;
};

export type MethodologyFile = {
  path: string;
  content: string;
};

export type RolePermissions = {
  publish: boolean;
  message: boolean;
  spawn?: string[];
  attest: boolean;
  merge: boolean;
  methodology: boolean;
};

export type MethodologyRole = {
  description?: string;
  prompt?: string;
  model?: string;
  lifecycle?: string;
  permissions: RolePermissions;
  denyTools?: string[];
};

export type EventRule = {
  event: string;
  label?: string;
  commentPrefix?: string;
  role: string;
};

export type MergePolicy = {
  requireAttestation: boolean;
  acceptResults?: AttestResult[];
  requireNonAuthor: boolean;
  requireChecks: boolean;
  auto: boolean;
  method?: string;
};

export type MethodologyLimits = {
  maxConcurrentAgents?: number;
  maxHops?: number;
  maxAgentsPerChain?: number;
  maxCostPerAgentUsd?: number;
  maxCostPerDayUsd?: number;
};

export type MethodologyManifest = {
  defaultRole: string;
  roles: Record<string, MethodologyRole>;
  standingOrders?: string[];
  events?: EventRule[];
  merge: MergePolicy;
  limits?: MethodologyLimits;
};

export type Methodology = {
  id: string;
  hiveId: string;
  version: number;
  active: boolean;
  source?: MethodologySource | null;
  manifest: MethodologyManifest;
  digest: string;
  note?: string;
  createdBy: string;
  createdAt: string;
};

export type MethodologyResponse = {
  methodology: Methodology;
  files: MethodologyFile[];
};

export type MethodologyEvent = {
  id: string;
  hiveId: string;
  version: number;
  action: "publish" | "activate" | string;
  actor: string;
  note?: string;
  source?: MethodologySource | null;
  createdAt: string;
};

export type MethodologyVersionsResponse = {
  versions: Methodology[];
  events: MethodologyEvent[];
};

export type AttestResult = "pass" | "pass_notes" | "fail" | "blocked";

export type Finding = {
  severity: "critical" | "warning" | "nit" | string;
  location?: string;
  finding: string;
  evidence?: string;
};

export type Attestation = {
  id: string;
  hiveId: string;
  prNumber: number;
  headSha: string;
  /** The Agent that gave the conclusion. */
  attesterId: string;
  /** The Agent that owns the PR revision. */
  authorId: string;
  /** Convenience copy of AttesterID == AuthorID from the server. */
  isAuthor: boolean;
  result: AttestResult;
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

export type PullRequest = {
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
  reasons: string[];
  pull: PullRequest;
  attestation?: Attestation | null;
  attestationFound?: boolean;
  checks?: MergeCheck[];
};

export type PRStatusResponse = {
  hiveId: string;
  prNumber: number;
  pull: PullRequest;
  readiness: MergeReadiness;
  attestations: Attestation[];
};

export type ModelUsage = {
  model: string;
  inputTokens: number;
  outputTokens: number;
  costUsd: number;
  /** False when the model has no price entry; its cost is recorded as zero. */
  priceKnown?: boolean;
};

export type AgentUsage = {
  agentId: string;
  hiveId: string;
  roleName?: string;
  inputTokens: number;
  outputTokens: number;
  costUsd: number;
  models?: ModelUsage[];
};

export type UsageLimits = {
  maxCostPerAgentUsd?: number;
  maxCostPerDayUsd?: number;
};

export type UsageResponse = {
  hiveId: string;
  /** Local calendar day, YYYY-MM-DD. */
  date: string;
  totalCostUsd: number;
  totalInputTokens: number;
  totalOutputTokens: number;
  /** Per-Agent totals for the day, highest cost first. */
  agents: AgentUsage[];
  limits?: UsageLimits;
};
