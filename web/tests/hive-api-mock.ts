// Playwright mock for the Hive REST API. It is the only remaining consumer of
// web/src/fixtures: the UI itself always talks to /api. Each call gets fresh
// state, so tests can mutate it and watch the five-second polls pick it up.
import type { Page, Route } from "@playwright/test";
import { hiveAgents, hiveOverviews } from "../src/fixtures/hive-console";
import type {
  Agent,
  CreatedToken,
  GithubRepository,
  Hive,
  HiveOverview,
  Message,
  MessageKind,
  Principal,
  SetupStatus,
  TokenScope,
  TokenSummary,
} from "../src/hive/types";

export type MockFailure = Partial<
  Record<"hives" | "hive" | "foragers" | "forager" | "messages" | "send" | "spawn" | "cancel", number>
>;

export type MockOptions = {
  failures?: MockFailure;
  errorMessage?: string;
  /** Per-section overrides for GET /api/setup/status. */
  setup?: {
    github?: Partial<SetupStatus["github"]>;
    tunnel?: Partial<SetupStatus["tunnel"]>;
    cells?: Partial<SetupStatus["cells"]>;
    cliLogin?: Partial<SetupStatus["cliLogin"]>;
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
  body?: unknown;
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
      state: "pending",
      createdAt: "2026-10-15T20:11:00Z",
    },
  ];
}

const defaultVerify = {
  mode: "on",
  model: "",
  maxRounds: 3,
  autoMerge: false,
  mergeMethod: "squash",
};

const defaultSetup: SetupStatus = {  github: {
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
  cliLogin: { available: true, loggedIn: false, cli: "opencode" },
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
      cliLogin: { ...defaultSetup.cliLogin, ...options.setup?.cliLogin },
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
      body: request.postData() ? request.postDataJSON() : undefined,
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
          queenEnabled: Boolean(body.queenEnabled),
          config: {
            cli: "opencode",
            maxConcurrentForagers: 4,
            maxHops: 10,
            maxForagersPerChain: 10,
            idleParkMinutes: 30,
            hibernateHours: 24,
            turnTimeoutMinutes: 120,
            foragerMaxLifetimeHours: 168,
            triggers: [],
            verify: defaultVerify,
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
            state: body.queenEnabled ? "active" : "disabled",
            cli: "opencode",
            createdAt: now(),
            updatedAt: now(),
          },
        ];
        return json(route, 201, { hive, localAddress: state.localAddress });
      }
    }

    const verifyMatch = path.match(/^\/api\/hives\/([^/]+)\/verify$/);
    if (verifyMatch) {
      const hiveID = decodeURIComponent(verifyMatch[1]);
      const overview = state.hives.find(item => item.hive.id === hiveID);
      if (!overview) return json(route, 404, { error: "not found" });
      const current = { ...defaultVerify, ...(overview.hive.config.verify ?? {}) };
      if (method === "GET") {
        return json(route, 200, { hiveId: hiveID, verify: current });
      }
      if (method === "PATCH") {
        const body = request.postDataJSON() as Partial<typeof defaultVerify>;
        overview.hive.config.verify = { ...current, ...body };
        overview.hive.version += 1;
        overview.hive.updatedAt = now();
        return json(route, 200, {
          hive: overview.hive,
          verify: { ...current, ...body },
        });
      }
    }

    const hiveMatch = path.match(/^\/api\/hives\/([^/]+)$/);
    if (hiveMatch && method === "GET") {
      if (failure.hive) return fail(failure.hive);
      const hiveID = decodeURIComponent(hiveMatch[1]);
      const overview = state.hives.find(item => item.hive.id === hiveID);
      if (!overview) return json(route, 404, { error: "not found" });
      return json(route, 200, { hive: overview.hive, localAddress: state.localAddress });
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

    const foragerMergeMatch = path.match(/^\/api\/foragers\/([^/]+)\/merge$/);
    if (foragerMergeMatch && method === "GET") {
      const foragerID = decodeURIComponent(foragerMergeMatch[1]);
      for (const [hiveID, agents] of Object.entries(state.agents)) {
        const forager = agents.find(agent => agent.id === foragerID && agent.role === "forager");
        if (!forager) continue;
        if (!forager.prNumber) {
          return json(route, 200, {
            foragerId: forager.id,
            hiveId: hiveID,
            prNumber: 0,
            readiness: null,
            verdicts: [],
          });
        }
        const headSHA = "a41c9de9c3b5e2f1074c0a2b8d6f4e1c9a7b3d20";
        const verdicts = [
          {
            id: `verdict-${forager.prNumber}-current`,
            hiveId: hiveID,
            prNumber: forager.prNumber,
            headSha: headSHA,
            ownerId: forager.id,
            verifierId: "verifier-current",
            result: "pass",
            summary: "构建、单测和手动调用都通过；没有发现阻塞问题。",
            commentUrl: `https://github.com/${forager.hiveId}/pull/${forager.prNumber}#issuecomment-1`,
            superseded: false,
            createdAt: "2026-10-16T10:40:00Z",
          },
          {
            id: `verdict-${forager.prNumber}-old`,
            hiveId: hiveID,
            prNumber: forager.prNumber,
            headSha: "7be22d1f0c9a4b6e8d2f5a1c3e7b9d0f2a4c6e80",
            ownerId: forager.id,
            verifierId: "verifier-old",
            result: "pass_notes",
            summary: "行为正确，但建议补一条空结果提示。",
            commentUrl: `https://github.com/${forager.hiveId}/pull/${forager.prNumber}#issuecomment-0`,
            superseded: true,
            createdAt: "2026-10-15T22:15:00Z",
          },
        ];
        return json(route, 200, {
          foragerId: forager.id,
          hiveId: hiveID,
          prNumber: forager.prNumber,
          readiness: {
            ready: true,
            headSha: headSHA,
            pull: {
              number: forager.prNumber,
              state: "open",
              draft: false,
              merged: false,
              url: `https://github.com/${hiveID}/pull/${forager.prNumber}`,
              headSha: headSHA,
              baseRef: "dev",
              baseRepository: `yyydddkkk/${hiveID}`,
              mergeable: true,
              mergeableState: "clean",
            },
            verdict: verdicts[0],
            verdictFound: true,
            checks: [
              { name: "lint", status: "completed", conclusion: "success" },
              { name: "unit", status: "completed", conclusion: "success" },
            ],
            reasons: [],
          },
          verdicts,
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
