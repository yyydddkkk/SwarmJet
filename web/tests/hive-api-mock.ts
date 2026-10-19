// Playwright mock for the Hive REST API. It is the only remaining consumer of
// web/src/fixtures: the UI itself always talks to /api. Each call gets fresh
// state, so tests can mutate it and watch the five-second polls pick it up.
import type { Page, Route } from "@playwright/test";
import { hiveAgents, hiveOverviews } from "../src/fixtures/hive-console";
import type { Agent, Hive, HiveOverview, Message, MessageKind } from "../src/hive/types";

export type MockFailure = Partial<
  Record<"hives" | "hive" | "foragers" | "forager" | "messages" | "send" | "spawn" | "cancel", number>
>;

export type MockOptions = {
  failures?: MockFailure;
  errorMessage?: string;
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

export function createMockState(): MockState {
  return {
    hives: clone(hiveOverviews),
    agents: clone(hiveAgents),
    messages: seedMessages(),
    localAddress: "local:web-admin",
    nextID: 1,
    calls: [],
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
  const state = createMockState();
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
