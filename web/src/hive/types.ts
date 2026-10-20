// Domain types for the Hive / Queen / Forager console (ADR docs/claude/adr-hive-queen-forager.md).
// Field names mirror the Go contract in docs/claude/round1-prompts.md as camelCase JSON.
// The UI talks to /api; the fake fixtures in web/src/fixtures are used by Playwright only.

export type HiveConfig = {
  cli: string;
  queenModel?: string;
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

export type QueenStatus = {
  enabled: boolean;
  state: AgentState;
  model: string;
  agentId: string;
  lastTurnAt?: string;
};

export type MethodologyTemplate = {
  name: string;
  title: string;
  description: string;
  version: string;
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
  /** Provenance; an empty value from older rows means "agent". Unknown values are shown raw. */
  trust?: MessageTrust | string;
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
  configured: boolean;
  defaultModel: string;
  /** Providers with a configured credential. */
  providers?: string[];
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

/** One catalog entry, mirroring llmproxy.Model. */
export type ModelOption = {
  /** provider/model, e.g. "deepseek/deepseek-chat". */
  id: string;
  provider: string;
  displayName?: string;
  contextLength?: number;
  maxOutputTokens?: number;
  kind?: string;
};

/** A provider credential as the server shows it (api.modelCredentialView). */
export type ModelCredential = {
  provider: string;
  type?: string;
  /** Masked by the server; the plaintext key never reaches the console. */
  masked?: string;
  configured: boolean;
};

/** GET /api/models (api.modelsResponse). */
export type ModelsResponse = {
  defaultModel: string;
  models: ModelOption[];
  providers: ModelCredential[];
  /** False when the auth gateway did not answer; the catalog is then empty. */
  gatewayAvailable: boolean;
};

/** PUT /api/models/credentials/{provider} answers with the credential view. */
export type ModelCredentialResponse = ModelCredential;

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
  queen?: { prompt?: string; merge?: boolean };
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
  source?: MethodologySource;
  manifest: MethodologyManifest;
  digest: string;
  note?: string;
  createdBy: string;
  createdAt: string;
};

/** GET /methodology and POST /methodology/activate: the version flattened plus its files. */
export type MethodologyResponse = Methodology & {
  files: MethodologyFile[];
  /** True when the Hive has no stored version and the platform default is shown. */
  builtin?: boolean;
};

/** methodology.Change: one append-only publish or activate record. */
export type MethodologyChange = {
  id: number;
  hiveId: string;
  action: "publish" | "activate" | string;
  version: number;
  actor: string;
  note?: string;
  createdAt: string;
};

export type MethodologyVersionsResponse = {
  hiveId: string;
  activeVersion: number;
  versions: Methodology[];
  changes: MethodologyChange[];
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

/** merge.Check */
export type MergeCheck = {
  name: string;
  status: string;
  conclusion: string;
  url?: string;
  output?: string;
};

/** GET /api/hives/{id}/prs/{number} (api.prStatusResponse). */
export type PRStatusResponse = {
  hiveId: string;
  prNumber: number;
  state?: string;
  draft: boolean;
  merged: boolean;
  headSha?: string;
  url?: string;
  checks: MergeCheck[];
  attestations: Attestation[];
  ready: boolean;
  reasons: string[];
};

/** domain.LLMUsageTotals */
export type UsageTotals = {
  requests: number;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  costUsd: number;
  /** Requests whose model had no price entry; their cost counts as zero. */
  unknownPriceRequests: number;
};

/** domain.LLMUsageGroup: one row of byAgent or byModel. */
export type UsageGroup = UsageTotals & {
  agentId?: string;
  model?: string;
};

export type UsagePeriod = "all" | "today";

/** GET /api/usage (domain.LLMUsageSummary). */
export type UsageResponse = {
  hiveId?: string;
  agentId?: string;
  period: UsagePeriod | string;
  totals: UsageTotals;
  byAgent: UsageGroup[];
  byModel: UsageGroup[];
};
