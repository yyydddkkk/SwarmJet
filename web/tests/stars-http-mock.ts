/** Complete default StarsApp transport fixture. It extends legacy HTTP coverage
 * without changing legacy tests. The in-browser fake stream can emit real SSE
 * payloads and failures; snapshots still travel through normal HTTP requests. */
import type { Page } from "@playwright/test";
import { mockHiveAPI, type MockOptions } from "./hive-api-mock";
import type { PullRequest, Signal } from "../src/data";
export async function mockStarsHTTP(
  page: Page,
  options: MockOptions & { empty?: boolean } = {},
) {
  const state = await mockHiveAPI(page, options);
  state.hives = options.empty
    ? []
    : state.hives.filter((p) => p.hive.id === "swarmjet");
  state.setup.github = {
    ...state.setup.github,
    configured: true,
    installations: 1,
    ...options.setup?.github,
  };
  state.setup.models = {
    ...state.setup.models,
    available: true,
    configured: true,
    ...options.setup?.models,
  };
  state.modelCredentials = [
    ...new Set(state.models.models.map((m) => m.provider)),
  ].map((provider) => ({ provider, configured: true }));
  state.agents.swarmjet = state.agents.swarmjet.map((a, i) =>
    a.role === "queen"
      ? a
      : {
          ...a,
          state:
            i === 1
              ? "active"
              : i === 2
                ? "failed"
                : i === 3
                  ? "parked"
                  : a.state,
          updatedAt:
            i === 3
              ? new Date(Date.now() - 8 * 3600000).toISOString()
              : a.updatedAt,
        },
  );
  state.messages.push({
    id: "question-ui",
    hiveId: "swarmjet",
    from: "queen",
    to: state.localAddress,
    kind: "question",
    chainId: "decision-chain",
    hop: 0,
    body: "是否继续处理兼容问题？",
    state: "pending",
    createdAt: new Date().toISOString(),
  });
  const prs: PullRequest[] = [
    {
      number: 88,
      title: "修复兼容问题",
      headSha: "observed-head-88",
      state: "open",
      merged: false,
      ready: true,
      ownerId: "forager-7f3a",
      attestations: [],
    },
    {
      number: 87,
      title: "完成文档",
      headSha: "head87",
      state: "closed",
      merged: true,
      ready: false,
      updatedAt: new Date().toISOString(),
    },
  ];
  const signals: Signal[] = [
    {
      messageId: "signal88",
      source: "github",
      kind: "issue",
      number: 88,
      title: "处理输入兼容",
      url: "https://github.example/issues/88",
      claimed: false,
      claimedBy: "",
      createdAt: new Date().toISOString(),
    },
  ];
  let mergeStatus = 200;
  await page.addInitScript(() => {
    class Stream extends EventTarget {
      static all: Stream[] = [];
      onerror: ((event: Event) => void) | null = null;
      constructor(public url: string) {
        super();
        Stream.all.push(this);
        queueMicrotask(() => this.dispatchEvent(new Event("ready")));
      }
      close() {
        Stream.all = Stream.all.filter((s) => s !== this);
      }
    }
    Object.assign(window, {
      EventSource: Stream,
      __starsStream: {
        disconnect() {
          Stream.all.forEach((s) => s.onerror?.(new Event("error")));
        },
        push(type: string, data: unknown) {
          Stream.all.forEach((s) =>
            s.dispatchEvent(
              new MessageEvent(type, { data: JSON.stringify(data) }),
            ),
          );
        },
        ready() {
          Stream.all.forEach((s) => s.dispatchEvent(new Event("ready")));
        },
      },
    });
  });
  await page.route("**/api/**", async (route) => {
    const req = route.request(),
      url = new URL(req.url()),
      path = url.pathname,
      method = req.method();
    const match = path.match(
      /^\/api\/hives\/([^/]+)\/(prs(?:\/\d+(?:\/merge)?)?|queue|signals|methodology\/events)$/,
    );
    if (!match) return route.fallback();
    state.calls.push({
      method,
      path,
      query: url.search.slice(1),
      body: req.postData() ? req.postDataJSON() : undefined,
      headers: req.headers(),
    });
    const json = (status: number, body: unknown) =>
      route.fulfill({
        status,
        contentType: "application/json",
        body: JSON.stringify(body),
      });
    if (match[2] === "prs")
      return json(200, { prs: match[1] === "swarmjet" ? prs : [] });
    if (match[2] === "queue")
      return json(200, {
        queue: [
          {
            agentId: "waiting-worker",
            role: "worker",
            task: "等待处理 Issue #91",
            position: 1,
            createdAt: new Date().toISOString(),
            expiresAt: new Date(Date.now() + 3600000).toISOString(),
          },
        ],
      });
    if (match[2] === "signals")
      return json(200, { signals: match[1] === "swarmjet" ? signals : [] });
    if (match[2] === "methodology/events") return json(200, { events: [] });
    const pr = prs.find((p) => p.number === Number(match[2].split("/")[1]));
    if (!pr) return json(404, { error: "PR不存在" });
    if (method === "POST") {
      if (mergeStatus !== 200)
        return json(mergeStatus, {
          error: mergeStatus === 409 ? "head changed" : "merge busy",
        });
      pr.merged = true;
      pr.ready = false;
      pr.state = "closed";
      return json(200, {
        merged: true,
        queued: false,
        headSha: pr.headSha,
        ready: false,
        reasons: [],
        message: "merged",
        method: "squash",
      });
    }
    return json(200, {
      hiveId: match[1],
      prNumber: pr.number,
      ...pr,
      checks: [],
      attestations: [],
      reasons: [],
    });
  });
  return {
    state,
    prs,
    signals,
    setMergeStatus(status: number) {
      mergeStatus = status;
    },
    disconnect: () =>
      page.evaluate(() => {
        (
          window as unknown as { __starsStream: { disconnect(): void } }
        ).__starsStream.disconnect();
      }),
  };
}
