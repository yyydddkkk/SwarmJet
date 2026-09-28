export interface Repository {
  id: string;
  projectId: number;
  url: string;
  path: string;
  targetBranch: string;
  createFrom?: string;
  dependsOn: string[] | null;
  sensitivePaths: string[] | null;
}
export interface AgentConfig {
  adapter: string;
  model: string;
  image: string;
  timeoutSeconds: number;
}
export interface Check {
  name: string;
  service: string;
  command: string[] | null;
  required: boolean;
}
export interface Connection {
  provider?: "gitlab" | "github";
  githubInstallationId?: number;
  id: string;
  name: string;
  gitlabUrl: string;
  username: string;
  version: number;
  hasMergeToken: boolean;
  fingerprint: string;
  createdAt: string;
  updatedAt: string;
}
export interface ConnectionRepository {
  id: number;
  name: string;
  pathWithNamespace: string;
  defaultBranch: string;
  httpUrlToRepo: string;
  webUrl: string;
  archived: boolean;
  description: string;
}
export interface RepositoryPage {
  projects: ConnectionRepository[] | null;
  page: number;
  perPage: number;
  hasNext: boolean;
  hasPrev: boolean;
}
export interface InitQuestion {
  id: string;
  question: string;
  answer?: string;
  answered: boolean;
}
export interface InitReport {
  path: string;
  description: string;
  verified: boolean;
}
export interface InitVerification {
  name: string;
  command: string[] | null;
  passed: boolean;
  exitCode: number;
  report: string;
  dryRun?: boolean;
}
export interface Initialization {
  publication?: { status: string; mode: string; path: string; url?: string; ref: string; sha: string; error?: string };
  id: string;
  projectId: string;
  status: string;
  stage: string;
  progress: number;
  message: string;
  repositoriesConnected: boolean;
  analysisComplete: boolean;
  readyForTasks: boolean;
  questions: InitQuestion[] | null;
  reports: InitReport[] | null;
  verification: InitVerification[] | null;
  metaSha: string;
  repositoryShas: Record<string, string> | null;
  executionId: string;
  sessionId?: string;
  attempts: number;
  error?: string;
  version: number;
  createdAt: string;
  updatedAt: string;
  agentStopped?: boolean;
}
export interface Project {
  myRole?: "viewer" | "operator" | "owner";
  provider?: "gitlab" | "github";
  mergeMethod?: "merge" | "squash" | "rebase";
  nodes?: {mode:string;ids?:string[]};
  acceptanceStrategy?: "none" | "repository" | "gitlab_ci" | "platform_ci" | "custom";
  reviewMode?: "none" | "independent";
  id: string;
  name: string;
  metaUrl: string;
  metaRef: string;
  version: number;
  enabled: boolean;
  gitlabUrl: string;
  credentialRef: string;
  mergeCredentialRef: string;
  connectionId?: string;
  metaMode?: string;
  autoMerge?: boolean;
  initStatus?: string;
  repositories: Repository[] | null;
  development: AgentConfig;
  review: AgentConfig;
  integration: {
    mode?: "compose" | "commands";
    image?: string;
    commands?: { name: string; path: string; argv: string[]; evidence: string }[];
    composeFile: string;
    checks: Check[] | null;
    startupTimeoutSeconds: number;
  };
  maxReworks: number;
  requireCI: boolean;
}
export interface IssueRef {
  projectId: number;
  iid: number;
}
export interface Issue extends IssueRef {
  title: string;
  description: string;
  url: string;
  state: string;
  labels: string[] | null;
}
export interface Revision {
  repositoryId: string;
  projectId: number;
  path: string;
  baseSha: string;
  headSha: string;
  targetBranch: string;
  branch: string;
  changed: boolean;
  sensitive: boolean;
  files: string[] | null;
  mrIid: number;
  mrUrl: string;
  merged: boolean;
}
export interface Verification {
  name: string;
  required: boolean;
  passed: boolean;
  exitCode: number;
  report: string;
  revisionDigest: string;
}
export interface Finding {
  repositoryId: string;
  path: string;
  message: string;
}
export interface AgentResult {
  outcome: string;
  summary: string;
  assumptions: string[] | null;
  findings: Finding[] | null;
  needsHelp: string;
}
export interface Execution {
  id: string;
  sessionId: string;
  containerId: string;
  status: string;
  exitCode: number;
  logPath: string;
  result?: AgentResult | null;
  artifacts?: ArtifactRef[];
}
export interface ArtifactRef {
  taskId: string;
  executionId: string;
  kind: "result" | "log" | "report";
  name: string;
  sha256: string;
  size: number;
}
export interface Task {
  humanAcceptance?: { prerequisites: string[] | null; steps: { action: string; expected: string }[] | null; notes: string[] | null };
  acceptanceIncomplete?: boolean;
  round?: number;
  previousTaskId?: string;
  triggerNoteIds?: number[];
  triggerText?: string;
  dependencyTasks?: Record<string, string>;
  id: string;
  admissionStatus?: "waiting" | "assigned";
  admissionBackend?: "local" | "node" | "kubernetes";
  executionPool?: string;
  authorityVersion?: number;
  project: Project;
  autoMergeOverride?: boolean | null;
  issue: Issue;
  dependencies: IssueRef[] | null;
  stage: string;
  message: string;
  workspace: {
    taskId: string;
    root: string;
    metaSha: string;
    revisions: Revision[] | null;
  };
  executions: Record<string, Execution> | null;
  verification: Verification[] | null;
  assumptions: string[] | null;
  notes: string[] | null;
  reworks: number;
  reviewDigest: string;
  approvalDigest: string;
  paused: boolean;
  createdAt: string;
  updatedAt: string;
}
export interface TaskPage {
  items: Task[];
  nextCursor?: string;
  total: number;
  counts: { all: number; active: number; waiting: number; attention: number; ended: number; completed: number };
}
export interface TaskEvent {
  id: number;
  taskId: string;
  kind: string;
  message: string;
  createdAt: string;
}
export interface Command {
  id: string;
  kind: string;
  text: string;
  autoMerge?: boolean;
  digest: string;
  dependencies?: IssueRef[];
}
