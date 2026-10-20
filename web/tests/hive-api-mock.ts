// Playwright mock for the Hive REST API. It is the only remaining consumer of
// web/src/fixtures: the UI itself always talks to /api. Each call gets fresh
// state, so tests can mutate it and watch the five-second polls pick it up.
// Round-4 responses copy the json tags of the Go handlers exactly (see
// web/src/fixtures/round4.ts for the struct each one mirrors).
import { methodologyTemplates } from "../src/fixtures/round5";
import type { Page, Route } from "@playwright/test";
import { hiveAgents, hiveOverviews } from "../src/fixtures/hive-console";
import {
  builtinMethodology,
  methodologyChanges,
  methodologyResponses,
  methodologyVersions,
  modelCatalog,
  ordersUsage,
  pr88Status,
  swarmjetUsage,
  swarmjetUsageRows,
} from "../src/fixtures/round4";
import type {
  Agent,
  CreatedToken,
  GithubRepository,
  Hive,
  HiveOverview,
  Message,
  MessageKind,
  Methodology,
  MethodologyChange,
  MethodologyFile,
  ModelCredential,
  ModelsResponse,
  Principal,
  PRStatusResponse,
  SetupStatus,
  TokenScope,
  TokenSummary,
  UsageGroup,
  UsageResponse,
  UsageTotals,
} from "../src/hive/types";

export type MockFailure = Partial<
  Record<
    | "hives"
    | "hive"
    | "foragers"
    | "forager"
    | "messages"
    | "send"
    | "spawn"
    | "cancel"
    | "queen"
    | "updateHive"
    | "models"
    | "methodology"
    | "usage"
    | "pr",
    number
  >
>;

export type MockOptions = {
  failures?: MockFailure;
  errorMessage?: string;
  /** Per-section overrides for GET /api/setup/status. */
  setup?: {
    github?: Partial<SetupStatus["github"]>;
    tunnel?: Partial<SetupStatus["tunnel"]>;
    cells?: Partial<SetupStatus["cells"]>;
    models?: Partial<SetupStatus["models"]>;
  };
  repositories?: GithubRepository[];
  tokens?: TokenSummary[];
  /** Start signed out; POST /api/login flips it. */
  unauthenticated?: boolean;
  manifestAction?: string;
  principal?: Principal;
  tokenConnect?: Partial<CreatedToken["connect"]>;
  loginFailure?: { status: number; message: string };
};

export type MockCall = {
  method: string;
  path: string;
  /** The query string without the leading "?". */
  query: string;
  body?: unknown;
  headers: Record<string, string>;
};

export type MockState = {
  hives: HiveOverview[];
  agents: Record<string, Agent[]>;
  messages: Message[];
  localAddress: string;
  nextID: number;
  calls: MockCall[];
  setup: SetupStatus;
  repositories: GithubRepository[];
  tokens: TokenSummary[];
  authenticated: boolean;
  manifestAction: string;
  principal: Principal;
  nextTokenID: number;
  models: ModelsResponse;
  modelCredentials: ModelCredential[];
  methodologies: Record<string, Methodology[]>;
  methodologyFiles: Record<string, Record<number, MethodologyFile[]>>;
  methodologyChanges: Record<string, MethodologyChange[]>;
  usage: Record<string, UsageResponse>;
  /** Per-Agent totals and byModel rows, used for ?agent= filters. */
  usageRows: Record<string, Record<string, { totals: UsageTotals; byModel: UsageGroup[] }>>;
  prStatuses: Record<string, PRStatusResponse>;
};

const clone = <T>(value: T): T => JSON.parse(JSON.stringify(value)) as T;

function seedMessages(): Message[] {
  return [
    {
      id: "evt-142",
      hiveId: "swarmjet",
      from: "system",
      to: "hive",
      kind: "event",
      chainId: "chain-142",
      hop: 0,
      body: "GitHub issues.opened：#142 Forager 列表需要按状态筛选。外部内容：Swarm 面板里 Forager 一多就找不到失败的那只。",
      trust: "external",
      state: "pending",
      createdAt: "2026-10-16T09:02:00Z",
    },
    {
      id: "msg-1",
      hiveId: "swarmjet",
      from: "local:web-admin",
      to: "queen",
      kind: "task",
      chainId: "chain-142",
      hop: 0,
      body: "把 #142 派出去，顺便让一只独立的 Forager 审一下 #88 的改动。",
      trust: "user",
      state: "handled",
      createdAt: "2026-10-16T09:04:00Z",
    },
    {
      id: "msg-2",
      hiveId: "swarmjet",
      from: "queen",
      to: "local:web-admin",
      kind: "reply",
      chainId: "chain-142",
      hop: 1,
      body: "收到。我先把 #142 交给一只使用 implementation 预设的 Forager，工作分支从 main 切出；同时把 #88 的审查任务派给另一只 Forager。",
      trust: "agent",
      state: "handled",
      createdAt: "2026-10-16T09:05:00Z",
    },
    {
      id: "dmsg-1",
      hiveId: "swarmjet",
      from: "queen",
      to: "forager:forager-7f3a",
      kind: "task",
      chainId: "chain-142",
      hop: 0,
      body: "处理 #142：在 Swarm 面板增加按状态筛选。只改 web/src，别动旧的任务页；完成一轮后更新交接摘要。",
      trust: "agent",
      state: "handled",
      createdAt: "2026-10-16T09:07:00Z",
    },
    {
      id: "dmsg-2",
      hiveId: "swarmjet",
      from: "forager:forager-7f3a",
      to: "queen",
      kind: "report",
      chainId: "chain-142",
      hop: 1,
      body: "筛选接口和样式完成，单测 6 个用例通过。前端的状态筛选交互还没做，下一轮继续。",
      trust: "agent",
      artifacts: [{ kind: "branch", ref: "swarm/swarmjet/forager-7f3a" }],
      state: "handled",
      createdAt: "2026-10-16T10:48:00Z",
    },
    {
      id: "omsg-1",
      hiveId: "orders",
      from: "system",
      to: "hive",
      kind: "event",
      chainId: "chain-51",
      hop: 0,
      body: "Queen 未开启，这条事件已存入 Hive Inbox（蜂巢收件箱），等待本地 Agent 或人来处理。",
      trust: "system",
      state: "pending",
      createdAt: "2026-10-15T20:11:00Z",
    },
    {
      id: "evt-88-review",
      hiveId: "swarmjet",
      from: "router",
      to: "queen",
      kind: "event",
      chainId: "chain-142",
      hop: 0,
      body: "GitHub pull_request_review 已提交：forager-2b91 批准了 a41c9de。外部内容：错误处理与测试覆盖没问题。",
      trust: "external",
      state: "pending",
      createdAt: "2026-10-16T11:02:00Z",
    },
    {
      id: "sys-142",
      hiveId: "swarmjet",
      from: "system",
      to: "local:web-admin",
      kind: "event",
      chainId: "chain-142",
      hop: 0,
      body: "平台提醒：这条链已经运行 2 小时，仍未收到合并决定。",
      trust: "system",
      state: "pending",
      createdAt: "2026-10-16T11:10:00Z",
    },
  ];
}

const defaultSetup: SetupStatus = {
  github: {
    configured: true,
    source: "database",
    appId: 12345,
    appSlug: "swarmjet-demo",
    htmlUrl: "https://github.com/apps/swarmjet-demo",
    installUrl: "https://github.com/apps/swarmjet-demo/installations/new",
    installations: 2,
  },
  tunnel: { enabled: false, url: "", webhookSynced: false, lastError: "" },
  cells: {
    enabled: true,
    image: "ghcr.io/yyydddkkk/swarmjet:test",
    model: "opencode/free",
    dockerAvailable: true,
  },
  models: { available: false, configured: false, defaultModel: "deepseek/deepseek-chat" },
};

const defaultRepositories: GithubRepository[] = [
  { fullName: "yyydddkkk/SwarmJet", private: false, defaultBranch: "dev" },
  { fullName: "yyydddkkk/orders-api", private: true, defaultBranch: "main" },
];

const defaultTokens: TokenSummary[] = [
  {
    id: "a1b2c3d4e5f60718293a4b5c6d7e8f90",
    name: "工作笔记本",
    scope: "default",
    createdAt: "2026-10-16T09:00:00Z",
  },
];

/** Token IDs and plaintexts follow the store: 16 random bytes / 32 bytes in hex. */
function opaqueID(n: number): string {
  return n.toString(16).padStart(32, "0");
}

function tokenSecret(n: number): string {
  return n.toString(16).padStart(64, "0");
}

/** The server only ever returns this masked form; the mock mirrors that. */
function maskKey(key: string): string {
  if (key.length <= 8) return `${key.slice(0, 2)}…${key.slice(-2)}`;
  return `${key.slice(0, 3)}…${key.slice(-4)}`;
}

export function createMockState(options: MockOptions = {}): MockState {
  return {
    hives: clone(hiveOverviews),
    agents: clone(hiveAgents),
    messages: seedMessages(),
    localAddress: "local:web-admin",
    nextID: 1,
    calls: [],
    setup: {
      github: { ...defaultSetup.github, ...options.setup?.github },
      tunnel: { ...defaultSetup.tunnel, ...options.setup?.tunnel },
      cells: { ...defaultSetup.cells, ...options.setup?.cells },
      models: { ...defaultSetup.models, ...options.setup?.models },
    },
    repositories: clone(options.repositories ?? defaultRepositories),
    tokens: clone(options.tokens ?? defaultTokens),
    authenticated: !options.unauthenticated,
    manifestAction: options.manifestAction ?? "https://github.example/apps/new?state=test-state",
    principal: clone(
      options.principal ?? {
        id: "admin-1",
        username: "admin",
        displayName: "管理员",
        platformAdmin: true,
      },
    ),
    nextTokenID: 2,
    models: clone(modelCatalog),
    modelCredentials: clone(modelCatalog.providers),
    methodologies: { swarmjet: clone(methodologyVersions) },
    methodologyFiles: {
      swarmjet: Object.fromEntries(
        methodologyVersions.map(version => [
          version.version,
          clone(methodologyResponses[version.version].files),
        ]),
      ),
    },
    methodologyChanges: { swarmjet: clone(methodologyChanges) },
    usage: { swarmjet: clone(swarmjetUsage), orders: clone(ordersUsage) },
    usageRows: {
      swarmjet: clone(swarmjetUsageRows) as unknown as MockState["usageRows"][string],
      orders: {
        "forager-1a2b": {
          totals: usageTotals(ordersUsage.byAgent[0]),
          byModel: clone(ordersUsage.byModel),
        },
      },
    },
    prStatuses: { "swarmjet:88": clone(pr88Status) },
  };
}

function usageTotals(row: UsageTotals): UsageTotals {
  return {
    requests: row.requests,
    inputTokens: row.inputTokens,
    outputTokens: row.outputTokens,
    cacheReadTokens: row.cacheReadTokens,
    cacheWriteTokens: row.cacheWriteTokens,
    costUsd: row.costUsd,
    unknownPriceRequests: row.unknownPriceRequests,
  };
}

const emptyTotals: UsageTotals = {
  requests: 0,
  inputTokens: 0,
  outputTokens: 0,
  cacheReadTokens: 0,
  cacheWriteTokens: 0,
  costUsd: 0,
  unknownPriceRequests: 0,
};

/** Mirrors setupModels: available means at least one configured credential. */
function syncSetupModels(state: MockState) {
  const providers = [
    ...new Set(state.modelCredentials.filter(item => item.configured).map(item => item.provider)),
  ].sort();
  state.setup.models = {
    available: providers.length > 0,
    configured: providers.length > 0,
    defaultModel: state.models.defaultModel,
    ...(providers.length ? { providers } : {}),
  };
}

function overviews(state: MockState): HiveOverview[] {
  return state.hives.map(({ hive }) => {
    const agents = state.agents[hive.id] ?? [];
    const activeForagers = agents.filter(
      agent => agent.role === "forager" && ["creating", "active", "parked"].includes(agent.state),
    ).length;
    const inboxBacklog = state.messages.filter(
      message => message.hiveId === hive.id && message.to === "hive" && message.state !== "handled",
    ).length;
    return { hive, activeForagers, pendingApprovals: 0, inboxBacklog };
  });
}

function now(): string {
  return new Date().toISOString();
}

export async function mockHiveAPI(page: Page, options: MockOptions = {}): Promise<MockState> {
  const state = createMockState(options);
  const failure = options.failures ?? {};
  const failureMessage = options.errorMessage ?? "模拟的接口故障";

  const json = (route: Route, status: number, body: unknown) =>
    route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });

  await page.route("**/api/**", async route => {
    const request = route.request();
    const method = request.method();
    const url = new URL(request.url());
    const path = url.pathname;
    state.calls.push({
      method,
      path,
      query: url.search.replace(/^\?/, ""),
      body: request.postData() ? request.postDataJSON() : undefined,
      headers: request.headers(),
    });
    const fail = (status: number) => json(route, status, { error: failureMessage });

    if (path === "/api/session" && method === "GET") {
      if (!state.authenticated) return json(route, 401, { error: "not signed in" });
      return json(route, 200, state.principal);
    }
    if (path === "/api/login" && method === "POST") {
      if (options.loginFailure) {
        return json(route, options.loginFailure.status, { error: options.loginFailure.message });
      }
      state.authenticated = true;
      return route.fulfill({ status: 204 });
    }
    if (path === "/api/logout" && method === "POST") {
      state.authenticated = false;
      return route.fulfill({ status: 204 });
    }
    if (path === "/api/setup/status" && method === "GET") {
      return json(route, 200, state.setup);
    }
    if (path === "/api/models" && method === "GET") {
      if (failure.models) return fail(failure.models);
      const body: ModelsResponse = {
        defaultModel: state.models.defaultModel,
        models: state.models.gatewayAvailable ? state.models.models : [],
        providers: [...state.modelCredentials].sort((a, b) => a.provider.localeCompare(b.provider)),
        gatewayAvailable: state.models.gatewayAvailable,
      };
      return json(route, 200, body);
    }
    const modelCredentialMatch = path.match(/^\/api\/models\/credentials\/([^/]+)$/);
    if (modelCredentialMatch) {
      const provider = decodeURIComponent(modelCredentialMatch[1]);
      if (method === "PUT") {
        const body = request.postDataJSON() as { apiKey?: string };
        const apiKey = (body.apiKey ?? "").trim();
        if (!apiKey) return json(route, 400, { error: "apiKey is required" });
        const credential: ModelCredential = {
          provider,
          type: "api_key",
          masked: maskKey(apiKey),
          configured: true,
        };
        state.modelCredentials = [
          ...state.modelCredentials.filter(item => item.provider !== provider),
          credential,
        ];
        syncSetupModels(state);
        return json(route, 200, credential);
      }
      if (method === "DELETE") {
        const before = state.modelCredentials.length;
        state.modelCredentials = state.modelCredentials.filter(item => item.provider !== provider);
        if (state.modelCredentials.length === before) {
          return json(route, 404, { error: "not found" });
        }
        syncSetupModels(state);
        return route.fulfill({ status: 204 });
      }
    }
    if (path === "/api/setup/github/manifest" && method === "POST") {
      return json(route, 200, {
        action: state.manifestAction,
        manifest: {
          name: "swarmjet-test",
          url: "http://127.0.0.1:8180",
          redirect_url: "http://127.0.0.1:8180/api/setup/github/callback",
          public: false,
          hook_attributes: {
            url: "http://127.0.0.1:8180/api/webhooks/github",
            active: true,
          },
          default_permissions: {
            contents: "write",
            pull_requests: "write",
            issues: "write",
            checks: "read",
            metadata: "read",
          },
          default_events: [
            "pull_request",
            "pull_request_review",
            "pull_request_review_comment",
            "issue_comment",
            "issues",
            "check_run",
          ],
        },
      });
    }
    if (path === "/api/setup/github/repositories" && method === "GET") {
      return json(route, 200, { repositories: state.repositories });
    }
    if (path === "/api/tokens") {
      if (method === "GET") return json(route, 200, { tokens: state.tokens });
      if (method === "POST") {
        const body = request.postDataJSON() as { name: string; scope: TokenScope };
        const sequence = state.nextTokenID++;
        const id = opaqueID(0x1000 + sequence);
        const token: TokenSummary = {
          id,
          name: body.name,
          scope: body.scope,
          createdAt: now(),
        };
        state.tokens.push(token);
        const plaintext = tokenSecret(0x2000 + sequence);
        const connect = {
          mcpUrl: "http://127.0.0.1:8180/mcp",
          claudeCode: `claude mcp add --transport http swarmjet http://127.0.0.1:8180/mcp --header "Authorization: Bearer ${plaintext}"`,
          codex: `[mcp_servers.swarmjet]\nurl = "http://127.0.0.1:8180/mcp"\nhttp_headers = { Authorization = "Bearer ${plaintext}" }\n`,
          json: {
            mcpServers: {
              swarmjet: {
                type: "http",
                url: "http://127.0.0.1:8180/mcp",
                headers: { Authorization: `Bearer ${plaintext}` },
              },
            },
          },
          ...options.tokenConnect,
        };
        return json(route, 201, { token, plaintext, connect });
      }
    }
    const tokenMatch = path.match(/^\/api\/tokens\/([^/]+)$/);
    if (tokenMatch && method === "DELETE") {
      const tokenID = decodeURIComponent(tokenMatch[1]);
      const before = state.tokens.length;
      state.tokens = state.tokens.filter(token => token.id !== tokenID);
      if (state.tokens.length === before) return json(route, 404, { error: "not found" });
      return route.fulfill({ status: 204 });
    }

    if (path === "/api/hives") {
      if (method === "GET") {
        if (failure.hives) return fail(failure.hives);
        return json(route, 200, {
          hives: overviews(state),
          localAddress: state.localAddress,
          truncated: false,
        });
      }
      if (method === "POST") {
        const body = request.postDataJSON() as {
          name: string;
          repository: string;
          queenEnabled?: boolean;
          config?: Hive["config"];
        };
        const id = `hive-new-${state.nextID++}`;
        const hive: Hive = {
          id,
          name: body.name,
          repository: body.repository,
          queenEnabled: body.queenEnabled ?? true,
          config: {
            cli: "opencode",
            maxConcurrentForagers: 4,
            queenModel: "",
            maxHops: 50,
            maxForagersPerChain: 50,
            idleParkMinutes: 30,
            hibernateHours: 24,
            turnTimeoutMinutes: 120,
            foragerMaxLifetimeHours: 168,
            triggers: [],
            ...body.config,
          } as Hive["config"],
          version: 1,
          createdAt: now(),
          updatedAt: now(),
        };
        state.hives.push({
          hive,
          activeForagers: 0,
          pendingApprovals: 0,
          inboxBacklog: 0,
        });
        state.agents[id] = [
          {
            id: `queen-${id}`,
            hiveId: id,
            role: "queen",
            state: hive.queenEnabled ? "parked" : "disabled",
            roleName: "queen",
            cli: "opencode",
            createdAt: now(),
            updatedAt: now(),
          },
        ];
        return json(route, 201, { hive, localAddress: state.localAddress });
      }
    }

    const hiveMatch = path.match(/^\/api\/hives\/([^/]+)$/);
    if (hiveMatch && method === "PATCH") {
      if (failure.updateHive) return fail(failure.updateHive);
      const hiveID = decodeURIComponent(hiveMatch[1]);
      const hive = state.hives.find(item => item.hive.id === hiveID)?.hive;
      if (!hive) return json(route, 404, { error: "not found" });
      const input = request.postDataJSON() as { queenEnabled?: boolean; config?: Partial<Hive["config"]> };
      hive.config = { ...hive.config, ...input.config };
      if (input.queenEnabled !== undefined) hive.queenEnabled = input.queenEnabled;
      hive.version++;
      hive.updatedAt = now();
      const queen = state.agents[hiveID]?.find(agent => agent.role === "queen");
      if (queen) queen.state = hive.queenEnabled ? "parked" : "disabled";
      return json(route, 200, { hive, localAddress: state.localAddress });
    }
    const queenMatch = path.match(/^\/api\/hives\/([^/]+)\/queen$/);
    if (queenMatch && method === "GET") {
      if (failure.queen) return fail(failure.queen);
      const hiveID = decodeURIComponent(queenMatch[1]);
      const hive = state.hives.find(item => item.hive.id === hiveID)?.hive;
      if (!hive) return json(route, 404, { error: "not found" });
      const queen = state.agents[hiveID]?.find(agent => agent.role === "queen");
      return json(route, 200, {
        enabled: hive.queenEnabled, state: queen?.state ?? "disabled",
        model: hive.config.queenModel || state.models.defaultModel,
        agentId: queen?.id ?? "", ...(queen ? { lastTurnAt: queen.updatedAt } : {}),
      });
    }
    if (path === "/api/methodology-templates" && method === "GET") {
      return json(route, 200, methodologyTemplates);
    }
    const installMatch = path.match(/^\/api\/hives\/([^/]+)\/methodology\/install$/);
    if (installMatch && method === "POST") {
      if (failure.methodology) return fail(failure.methodology);
      const hiveID = decodeURIComponent(installMatch[1]);
      if (!state.hives.some(item => item.hive.id === hiveID)) return json(route, 404, { error: "not found" });
      const input = request.postDataJSON() as { name: string; note?: string };
      const template = methodologyTemplates.find(item => item.name === input.name);
      if (!template) return json(route, 404, { error: "template not found" });
      const versions = state.methodologies[hiveID] ?? [];
      const versionNumber = Math.max(0, ...versions.map(item => item.version)) + 1;
      const response = clone(input.name === "minimal" ? builtinMethodology(hiveID) : methodologyResponses[3]);
      const { files, builtin: _builtin, ...base } = response;
      const version: Methodology = {
        ...base, id: `methodology-${hiveID}-${versionNumber}`, hiveId: hiveID,
        version: versionNumber, active: true, source: { url: `builtin:${input.name}`, ref: template.version },
        createdBy: state.localAddress, createdAt: now(), note: input.note,
      };
      versions.forEach(item => { item.active = false; });
      state.methodologies[hiveID] = [...versions, version];
      state.methodologyFiles[hiveID] = { ...state.methodologyFiles[hiveID], [versionNumber]: files };
      state.methodologyChanges[hiveID] = [...(state.methodologyChanges[hiveID] ?? []), {
        id: state.nextID++, hiveId: hiveID, action: "publish", version: versionNumber,
        actor: state.localAddress, note: input.note, createdAt: now(),
      }];
      return json(route, 200, { ...version, files });
    }
    if (hiveMatch && method === "GET") {
      if (failure.hive) return fail(failure.hive);
      const hiveID = decodeURIComponent(hiveMatch[1]);
      const overview = state.hives.find(item => item.hive.id === hiveID);
      if (!overview) return json(route, 404, { error: "not found" });
      return json(route, 200, { hive: overview.hive, localAddress: state.localAddress });
    }

    const methodologyVersionsMatch = path.match(/^\/api\/hives\/([^/]+)\/methodology\/versions$/);
    if (methodologyVersionsMatch && method === "GET") {
      if (failure.methodology) return fail(failure.methodology);
      const hiveID = decodeURIComponent(methodologyVersionsMatch[1]);
      if (!state.hives.some(item => item.hive.id === hiveID)) {
        return json(route, 404, { error: "not found" });
      }
      const versions = state.methodologies[hiveID] ?? [];
      return json(route, 200, {
        hiveId: hiveID,
        activeVersion: versions.find(item => item.active)?.version ?? 0,
        versions,
        changes: state.methodologyChanges[hiveID] ?? [],
      });
    }

    const methodologyActivateMatch = path.match(/^\/api\/hives\/([^/]+)\/methodology\/activate$/);
    if (methodologyActivateMatch && method === "POST") {
      const hiveID = decodeURIComponent(methodologyActivateMatch[1]);
      const versions = state.methodologies[hiveID];
      if (!versions) return json(route, 404, { error: "not found" });
      const body = request.postDataJSON() as { version?: number };
      const target = versions.find(version => version.version === body.version);
      if (!target) return json(route, 404, { error: "version not found" });
      for (const version of versions) version.active = version.version === target.version;
      const existing = state.methodologyChanges[hiveID] ?? [];
      const change: MethodologyChange = {
        id: Math.max(0, ...existing.map(item => item.id)) + 1,
        hiveId: hiveID,
        action: "activate",
        version: target.version,
        actor: state.localAddress,
        createdAt: now(),
      };
      state.methodologyChanges[hiveID] = [...existing, change];
      return json(route, 200, {
        ...target,
        files: state.methodologyFiles[hiveID]?.[target.version] ?? [],
      });
    }

    const methodologyMatch = path.match(/^\/api\/hives\/([^/]+)\/methodology$/);
    if (methodologyMatch && method === "GET") {
      if (failure.methodology) return fail(failure.methodology);
      const hiveID = decodeURIComponent(methodologyMatch[1]);
      if (!state.hives.some(item => item.hive.id === hiveID)) {
        return json(route, 404, { error: "not found" });
      }
      const versions = state.methodologies[hiveID] ?? [];
      const requested = url.searchParams.get("version");
      const version = requested
        ? versions.find(item => String(item.version) === requested)
        : versions.find(item => item.active);
      // No active version: the server answers with the built-in methodology.
      if (!version && !requested) return json(route, 200, builtinMethodology(hiveID));
      if (!version) return json(route, 404, { error: "version not found" });
      return json(route, 200, {
        ...version,
        files: state.methodologyFiles[hiveID]?.[version.version] ?? [],
      });
    }

    const prMatch = path.match(/^\/api\/hives\/([^/]+)\/prs\/(\d+)$/);
    if (prMatch && method === "GET") {
      if (failure.pr) return fail(failure.pr);
      const hiveID = decodeURIComponent(prMatch[1]);
      const prNumber = Number(prMatch[2]);
      const pr = state.prStatuses[`${hiveID}:${prNumber}`];
      if (!pr) return json(route, 404, { error: "not found" });
      return json(route, 200, pr);
    }

    if (path === "/api/usage" && method === "GET") {
      if (failure.usage) return fail(failure.usage);
      const hiveID = url.searchParams.get("hive") ?? "";
      const agentID = url.searchParams.get("agent") ?? "";
      const period = url.searchParams.get("period") ?? "";
      if (period !== "" && period !== "all" && period !== "today") {
        return json(route, 400, { error: 'period must be "all" or "today"' });
      }
      // Like LLMUsageSummary, an unknown filter is an empty summary, not a 404.
      const base: UsageResponse = state.usage[hiveID] ?? {
        period: "all",
        totals: emptyTotals,
        byAgent: [],
        byModel: [],
      };
      const echo = {
        ...(hiveID ? { hiveId: hiveID } : {}),
        ...(agentID ? { agentId: agentID } : {}),
        period: period === "today" ? "today" : "all",
      };
      if (!agentID) return json(route, 200, { ...base, ...echo });
      const row = state.usageRows[hiveID]?.[agentID];
      const summary: UsageResponse = {
        ...echo,
        totals: row ? row.totals : emptyTotals,
        byAgent: row ? [{ agentId: agentID, ...row.totals }] : [],
        byModel: row ? row.byModel : [],
      };
      return json(route, 200, summary);
    }

    const foragersMatch = path.match(/^\/api\/hives\/([^/]+)\/foragers$/);
    if (foragersMatch) {
      const hiveID = decodeURIComponent(foragersMatch[1]);
      const agents = state.agents[hiveID] ?? [];
      if (method === "GET") {
        if (failure.foragers) return fail(failure.foragers);
        return json(route, 200, {
          queen: agents.find(agent => agent.role === "queen"),
          foragers: agents.filter(agent => agent.role === "forager"),
          localAddress: state.localAddress,
          truncated: false,
        });
      }
      if (method === "POST") {
        if (failure.spawn) return fail(failure.spawn);
        const body = request.postDataJSON() as {
          task: string;
          preset?: string;
          role?: string;
          cli?: string;
          branch?: string;
          issueNumber?: number;
          chainId?: string;
        };
        const id = `forager-new-${state.nextID++}`;
        const chainID = body.chainId ?? `chain-new-${state.nextID}`;
        const forager: Agent = {
          id,
          hiveId: hiveID,
          role: "forager",
          state: "creating",
          preset: body.preset,
          roleName: body.role ?? body.preset ?? "worker",
          cli: body.cli ?? "opencode",
          task: body.task,
          branch: body.branch ?? `swarm/${hiveID}/${id}`,
          issueNumber: body.issueNumber,
          chainId: chainID,
          createdAt: now(),
          updatedAt: now(),
        };
        state.agents[hiveID] = [...(state.agents[hiveID] ?? []), forager];
        const message: Message = {
          id: `task-${id}`,
          hiveId: hiveID,
          from: state.localAddress,
          to: `forager:${id}`,
          kind: "task",
          chainId: chainID,
          hop: 0,
          body: body.task,
          state: "pending",
          createdAt: now(),
        };
        state.messages.push(message);
        return json(route, 201, { forager, message, localAddress: state.localAddress });
      }
    }

    const messagesMatch = path.match(/^\/api\/hives\/([^/]+)\/messages$/);
    if (messagesMatch) {
      const hiveID = decodeURIComponent(messagesMatch[1]);
      if (method === "GET") {
        if (failure.messages) return fail(failure.messages);
        const to = url.searchParams.get("to") ?? "";
        const after = url.searchParams.get("after") ?? "";
        const limit = Math.min(Number(url.searchParams.get("limit") ?? 50) || 50, 100);
        const all = state.messages
          .filter(message => message.hiveId === hiveID && message.to === to)
          .sort((left, right) =>
            left.createdAt === right.createdAt
              ? left.id.localeCompare(right.id)
              : left.createdAt.localeCompare(right.createdAt),
          );
        const start = after ? all.findIndex(message => message.id === after) + 1 : 0;
        if (after && start === 0) return json(route, 404, { error: "not found" });
        const pageItems = all.slice(start, start + limit);
        return json(route, 200, {
          messages: pageItems,
          cursor: pageItems.length ? pageItems[pageItems.length - 1].id : "",
          truncated: all.length - start > limit,
          to,
        });
      }
      if (method === "POST") {
        if (failure.send) return fail(failure.send);
        const body = request.postDataJSON() as {
          to: string;
          body: string;
          kind?: MessageKind;
          chainId?: string;
        };
        const id = `msg-web-${state.nextID++}`;
        const message: Message = {
          id,
          hiveId: hiveID,
          from: state.localAddress,
          to: body.to,
          kind: body.kind ?? "task",
          chainId: body.chainId ?? `chain-web-${state.nextID}`,
          hop: 0,
          body: body.body,
          state: "pending",
          createdAt: now(),
        };
        state.messages.push(message);
        return json(route, 201, { message, localAddress: state.localAddress });
      }
    }

    const foragerMatch = path.match(/^\/api\/foragers\/([^/]+)$/);
    if (foragerMatch && method === "GET") {
      if (failure.forager) return fail(failure.forager);
      const foragerID = decodeURIComponent(foragerMatch[1]);
      for (const [hiveID, agents] of Object.entries(state.agents)) {
        const forager = agents.find(agent => agent.id === foragerID && agent.role === "forager");
        if (!forager) continue;
        const overview = state.hives.find(item => item.hive.id === hiveID);
        if (!overview) break;
        return json(route, 200, {
          forager,
          hive: overview.hive,
          localAddress: state.localAddress,
        });
      }
      return json(route, 404, { error: "not found" });
    }

    const cancelMatch = path.match(/^\/api\/foragers\/([^/]+)\/cancel$/);
    if (cancelMatch && method === "POST") {
      if (failure.cancel) return fail(failure.cancel);
      const foragerID = decodeURIComponent(cancelMatch[1]);
      for (const agents of Object.values(state.agents)) {
        const forager = agents.find(agent => agent.id === foragerID && agent.role === "forager");
        if (!forager) continue;
        if (["completed", "failed", "cancelled"].includes(forager.state)) {
          return json(route, 409, { error: `forager ${forager.id} is already terminal` });
        }
        forager.state = "cancelled";
        forager.updatedAt = now();
        const message: Message = {
          id: `cancel-${foragerID}`,
          hiveId: forager.hiveId,
          from: state.localAddress,
          to: `forager:${foragerID}`,
          kind: "control",
          chainId: forager.chainId ?? `chain-cancel-${state.nextID++}`,
          hop: 0,
          body: "cancel",
          state: "pending",
          createdAt: now(),
        };
        state.messages.push(message);
        return json(route, 201, { message, localAddress: state.localAddress });
      }
      return json(route, 404, { error: "not found" });
    }

    return json(route, 404, { error: `unmocked API route: ${method} ${path}` });
  });

  return state;
}
