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
export interface Project {
  id: string;
  name: string;
  metaUrl: string;
  metaRef: string;
  version: number;
  enabled: boolean;
  gitlabUrl: string;
  credentialRef: string;
  mergeCredentialRef: string;
  repositories: Repository[] | null;
  development: AgentConfig;
  review: AgentConfig;
  integration: {
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
}
export interface Task {
  id: string;
  project: Project;
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
  digest: string;
  dependencies?: IssueRef[];
}
