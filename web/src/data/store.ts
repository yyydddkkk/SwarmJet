import { api, APIError, fetchProjectSnapshot, loadProjectResources, isActiveAgent, type DataAPI, type ResourceName, type ResourceUpdate } from "./api";
import { deriveProject, messageEndpoint, type LightPulse } from "./derive";
import type { ProjectSnapshot, ProjectEvent } from "./types";
export type ConnectionState =
  "idle" | "connecting" | "syncing" | "connected" | "reconnecting" | "closed";
export type EventConnection = { close(): void };
export type DataSource = {
  api: DataAPI;
  snapshot(id: string, signal?: AbortSignal): Promise<ProjectSnapshot>;
  load?: (id: string, update: ResourceUpdate, signal?: AbortSignal) => Promise<void>;
  connect(
    id: string,
    onReady: () => void,
    onEvent: (event: ProjectEvent) => void,
    onError: () => void,
  ): EventConnection;
};
type BrowserSubscriber = {
  id: string;
  ready(): void;
  event(event: ProjectEvent): void;
  error(): void;
};
const browserSubscribers = new Set<BrowserSubscriber>();
let browserStream: EventSource | undefined;
let browserReady = false;
function openBrowserStream() {
  if (browserStream) return;
  // A single all-project stream avoids the six HTTP/1.1 connections per origin
  // limit, which would otherwise prevent project snapshot requests from running.
  const stream = new EventSource("/api/events", { withCredentials: true });
  browserStream = stream;
  const failed = () => {
    if (browserStream !== stream) return;
    [...browserSubscribers].forEach((client) => client.error());
    void api.getSession().catch((error) => {
      if (error instanceof APIError && error.status === 401)
        window.dispatchEvent(new Event("swarmjet:session-expired"));
    });
  };
  stream.addEventListener("ready", () => {
    if (browserStream !== stream) return;
    browserReady = true;
    [...browserSubscribers].forEach((client) => client.ready());
  });
  for (const type of [
    "message",
    "agent",
    "pr",
    "methodology",
    "queue",
    "hive",
  ] as const) {
    stream.addEventListener(type, (event) => {
      if (browserStream !== stream) return;
      try {
        const data = JSON.parse((event as MessageEvent).data);
        // These two payloads intentionally have no hiveId in backend JSON. We
        // cannot safely attribute them: resnapshot canonical per-project lists.
        if (type === "queue" || type === "methodology") {
          [...browserSubscribers].forEach((client) => client.ready());
          return;
        }
        const hiveId = type === "hive" ? data.id : data.hiveId;
        [...browserSubscribers]
          .filter((client) => client.id === hiveId)
          .forEach((client) => client.event({ type, data } as ProjectEvent));
      } catch {
        failed();
      }
    });
  }
  stream.onerror = failed;
}
export const browserDataSource: DataSource = {
  api,
  snapshot: fetchProjectSnapshot,
  load: loadProjectResources,
  connect(id, ready, event, error) {
    const client = { id, ready, event, error };
    browserSubscribers.add(client);
    const alreadyReady = browserReady;
    openBrowserStream();
    if (alreadyReady)
      queueMicrotask(() => {
        if (browserSubscribers.has(client)) ready();
      });
    return {
      close() {
        browserSubscribers.delete(client);
        if (!browserSubscribers.size) {
          browserStream?.close();
          browserStream = undefined;
          browserReady = false;
        }
      },
    };
  },
};
export type ProjectState = {
  snapshot?: ProjectSnapshot;
  derived?: ReturnType<typeof deriveProject>;
  connection: ConnectionState;
  error?: string;
  pulses: LightPulse[];
  revision: number;
  retryDelay?: number;
  errorStatus?: number;
  resources: Record<ResourceName, { status: "loading" | "ready" | "error"; error?: string; stale?: boolean; unavailable?: boolean }>;
};
const resourceNames: ResourceName[] = ["hive", "agents", "queen", "prs", "queue", "signals", "methodology", "methodologyEvents", "messages"];
const loadingResources = () => Object.fromEntries(resourceNames.map((name) => [name, { status: "loading" }])) as ProjectState["resources"];
const emptySnapshot = (patch: Partial<ProjectSnapshot>): ProjectSnapshot => ({
  hive: patch.hive!, localAddress: patch.localAddress || "", agents: [],
  queen: { enabled: false, state: "disabled", model: "", agentId: "", lastTurnAt: null },
  prs: [], queue: [], signals: [], methodology: { manifest: { merge: { auto: false } } } as ProjectSnapshot["methodology"],
  methodologyEvents: [], messages: [], ...patch,
});
const upsert = <T>(items: T[], item: T, key: (item: T) => unknown): T[] => {
  const index = items.findIndex((i) => key(i) === key(item));
  return index < 0
    ? [...items, item]
    : items.map((old, i) => (i === index ? { ...old, ...item } : old));
};
export class ProjectStore {
  private state: ProjectState = { connection: "idle", pulses: [], revision: 0, resources: loadingResources() };
  private listeners = new Set<() => void>();
  private stream?: EventConnection;
  private timer?: ReturnType<typeof setTimeout>;
  private abort?: AbortController;
  private stopped = true;
  private pending: ProjectEvent[] | undefined;
  private generation = 0;
  private retries = 0;
  private signalSequence = 0;
  private methodologySequence = 0;
  private prSequence = 0;
  private clock?: ReturnType<typeof setInterval>;
  private pulsed = new Set<string>();
  private loadedMessageInboxes = new Set<string>();
  private messageHydrations = new Map<string, Promise<void>>();
  private firstReady = true;
  private observedPRs = new Map<
    number,
    import("./types").EventPayloads["pr"]
  >();
  constructor(
    readonly hiveId: string,
    readonly source: DataSource = browserDataSource,
  ) {}
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(patch: Partial<ProjectState>) {
    this.state = { ...this.state, ...patch, revision: this.state.revision + 1 };
    if (this.state.snapshot)
      this.state.derived = deriveProject(this.state.snapshot);
    this.listeners.forEach((fn) => fn());
  }
  start = () => {
    if (!this.stopped) return;
    this.stopped = false;
    this.clock = setInterval(() => this.publish({}), 60000);
    void this.refresh();
    this.connect();
  };
  stop = () => {
    this.stopped = true;
    clearInterval(this.clock);
    ++this.generation;
    this.abort?.abort();
    this.stream?.close();
    clearTimeout(this.timer);
    this.pending = undefined;
    this.publish({ connection: "closed" });
  };
  private connect() {
    if (this.stopped) return;
    this.publish({ connection: this.retries ? "reconnecting" : "connecting" });
    this.stream = this.source.connect(
      this.hiveId,
      () => {
        if (this.firstReady) {
          this.firstReady = false;
          this.publish({ connection: "connected", retryDelay: undefined });
        } else void this.refresh(true);
      },
      (event) => this.receive(event),
      () => this.disconnected(),
    );
  }
  private disconnected() {
    if (this.stopped) return;
    this.stream?.close();
    clearTimeout(this.timer);
    // Invalidate a snapshot from the dead connection; the next ready owns recovery.
    ++this.generation;
    this.abort?.abort();
    this.pending = undefined;
    this.firstReady = false;
    const delay = Math.min(1000 * 2 ** this.retries++, 30000);
    this.publish({
      connection: "reconnecting",
      retryDelay: delay,
      error: "连接已断开，正在重连",
    });
    this.timer = setTimeout(() => this.connect(), delay);
  }
  refresh = async (ready = false) => {
    const generation = ++this.generation;
    this.abort?.abort();
    this.abort = new AbortController();
    this.loadedMessageInboxes.clear();
    this.pending ??= [];
    const observedNumbers = [...this.observedPRs.keys()];
    this.publish({ connection: ready ? "syncing" : this.state.connection, resources: loadingResources() });
    try {
      let snapshot: ProjectSnapshot;
      if (this.source.load) {
        let partial: Partial<ProjectSnapshot> = {};
        let authError: APIError | undefined;
        await this.source.load(this.hiveId, (name, patch, error, meta) => {
          if (generation !== this.generation || this.stopped) return;
          if (error instanceof APIError && error.status === 401) authError = error;
          const status = error ? { status: "error" as const, error: error instanceof Error ? error.message : String(error), unavailable: name === "prs" } : { status: "ready" as const, ...(name === "prs" ? { stale: meta?.stale || patch?.prs?.some((pr) => pr.stale), unavailable: meta?.unavailable || patch?.prs?.some((pr) => pr.unavailable) } : {}) };
          const resources = { ...this.state.resources, [name]: status };
          if (patch) partial = { ...partial, ...patch };
          if (name === "messages" && patch) {
            ["queen", "hive", partial.localAddress, ...(partial.agents || []).filter(isActiveAgent).map((agent) => `forager:${agent.id}`)]
              .filter(Boolean).forEach((address) => this.loadedMessageInboxes.add(address!));
          }
          if (partial.hive) {
            const current = this.state.snapshot;
            // A fast resource may settle before the hive on a reconnect.
            // Merge every result from this generation when the hive arrives.
            const next = current ? { ...current, ...partial } : emptySnapshot(partial);
            this.publish({ snapshot: next, resources });
          } else this.publish({ resources });
        }, this.abort.signal);
        if (authError) throw authError;
        if (!partial.hive) throw new Error(this.state.resources.hive.error || "无法读取项目");
        snapshot = this.state.snapshot || emptySnapshot(partial);
      } else {
        snapshot = await this.source.snapshot(this.hiveId, this.abort.signal);
        ["queen", "hive", snapshot.localAddress, ...snapshot.agents.filter(isActiveAgent).map((agent) => `forager:${agent.id}`)]
          .filter(Boolean).forEach((address) => this.loadedMessageInboxes.add(address));
        this.publish({ resources: Object.fromEntries(resourceNames.map((name) => [name, { status: "ready" }])) as ProjectState["resources"] });
      }
      // List readiness is cached server-side for 30 seconds. Before discarding
      // stream observations, calibrate those PRs against the uncached status
      // endpoint, so a queue/methodology resync cannot revert a newly ready PR.
      const statuses = await Promise.all(
        observedNumbers.map((number) =>
          this.source.api.getPRStatus(this.hiveId, number, this.abort?.signal),
        ),
      );
      for (const status of statuses) {
        const existing = snapshot.prs.find(
          (pr) => pr.number === status.prNumber,
        );
        snapshot.prs = upsert(
          snapshot.prs,
          {
            ...existing,
            number: status.prNumber,
            headSha: status.headSha ?? existing?.headSha ?? "",
            state: status.state ?? existing?.state ?? "",
            ready: status.ready,
            merged: status.merged,
            reasons: status.reasons,
            attestations: status.attestations,
            url: status.url ?? existing?.url,
          },
          (pr) => pr.number,
        );
      }
      if (generation !== this.generation || this.stopped) return;
      const queued = this.pending ?? [];
      this.observedPRs.clear();
      for (const status of statuses) {
        const pr = snapshot.prs.find((pr) => pr.number === status.prNumber)!;
        this.observedPRs.set(pr.number, {
          hiveId: this.hiveId,
          number: pr.number,
          headSha: pr.headSha,
          state: pr.state,
          ready: pr.ready,
          merged: pr.merged,
        });
      }
      this.pending = undefined;
      this.publish({
        snapshot,
        error: undefined,
        errorStatus: undefined,
        ...(ready
          ? { connection: "connected" as const, retryDelay: undefined }
          : {}),
      });
      if (ready) this.retries = 0;
      queued.forEach((e) => this.apply(e));
    } catch (cause) {
      if (generation !== this.generation || this.stopped) return;
      this.pending = undefined;
      this.publish({
        error: cause instanceof Error ? cause.message : String(cause),
        errorStatus: cause instanceof APIError ? cause.status : undefined,
      });
      if (cause instanceof APIError && cause.status === 401) {
        this.stream?.close();
        return;
      }
      // A failed resync creates a gap; reconnect instead of applying increments
      // to an obsolete snapshot. Retry even when the transport itself is healthy.
      this.disconnected();
    }
  };
  private receive(event: ProjectEvent) {
    if ("hiveId" in event.data && event.data.hiveId !== this.hiveId) return;
    if (this.pending) this.pending.push(event);
    else this.apply(event);
  }
  private apply(event: ProjectEvent) {
    const snapshot = this.state.snapshot;
    if (!snapshot) return;
    const next = { ...snapshot };
    switch (event.type) {
      case "message": {
        next.messages = upsert(snapshot.messages, event.data, (m) => m.id);
        if (!this.pulsed.has(event.data.id)) {
          this.pulsed.add(event.data.id);
          this.publish({
            pulses: [
              ...this.state.pulses.slice(-99),
              {
                id: event.data.id,
                hiveId: this.hiveId,
                from: messageEndpoint(event.data.from, this.hiveId),
                to: messageEndpoint(event.data.to, this.hiveId),
                createdAt: event.data.createdAt,
              },
            ],
          });
        }
        // Signals have no SSE event type. Refresh their canonical parsed/claimed
        // representation when external traffic or an agent association changes.
        if (event.data.trust === "external") void this.refreshSignals();
        break;
      }
      case "agent":
        if (event.data.role === "queen") {
          next.queenAgent = event.data;
          next.queen = {
            ...next.queen,
            state: event.data.state,
            agentId: event.data.id,
          };
        } else {
          next.agents = upsert(snapshot.agents, event.data, (a) => a.id);
          if (["creating", "active", "parked", "hibernated"].includes(event.data.state))
            void this.loadAgentMessages(event.data.id).catch((error) => this.reportError(error));
        }
        void this.refreshSignals();
        break;
      case "pr":
        this.observedPRs.set(event.data.number, event.data);
        next.prs = upsert(snapshot.prs, event.data, (p) => p.number);
        void this.refreshPRs();
        break;
      case "queue":
        next.queue =
          event.data.state === "waiting"
            ? upsert(snapshot.queue, event.data, (q) => q.agentId).sort(
                (a, b) => a.position - b.position,
              )
            : snapshot.queue.filter((q) => q.agentId !== event.data.agentId);
        break;
      case "hive":
        next.hive = event.data;
        next.queen = { ...next.queen, enabled: event.data.queenEnabled };
        break;
      case "methodology":
        next.methodologyEvents = upsert(
          snapshot.methodologyEvents,
          event.data,
          (e) => `${e.createdAt}:${e.version}:${e.action}:${e.actor}`,
        );
        void this.refreshMethodology();
        break;
    }
    this.publish({ snapshot: next });
  }
  private reportError(error: unknown) {
    if (!this.stopped)
      this.publish({
        error: error instanceof Error ? error.message : String(error),
        errorStatus: error instanceof APIError ? error.status : undefined,
      });
  }
  private async refreshSignals() {
    const sequence = ++this.signalSequence,
      generation = this.generation;
    try {
      const result = await this.source.api.listSignals(this.hiveId);
      if (
        !this.stopped &&
        generation === this.generation &&
        sequence === this.signalSequence &&
        this.state.snapshot
      )
        this.publish({
          snapshot: { ...this.state.snapshot, signals: result.signals },
        });
    } catch (e) {
      if (generation === this.generation && sequence === this.signalSequence)
        this.reportError(e);
    }
  }
  private async refreshMethodology() {
    const sequence = ++this.methodologySequence,
      generation = this.generation;
    try {
      const methodology = await this.source.api.getMethodology(this.hiveId);
      if (
        !this.stopped &&
        generation === this.generation &&
        sequence === this.methodologySequence &&
        this.state.snapshot
      )
        this.publish({ snapshot: { ...this.state.snapshot, methodology } });
    } catch (e) {
      if (
        generation === this.generation &&
        sequence === this.methodologySequence
      )
        this.reportError(e);
    }
  }
  private async refreshPRs() {
    const sequence = ++this.prSequence,
      generation = this.generation;
    try {
      const result = await this.source.api.listPRs(this.hiveId);
      if (
        !this.stopped &&
        generation === this.generation &&
        sequence === this.prSequence &&
        this.state.snapshot
      )
        this.publish({
          snapshot: {
            ...this.state.snapshot,
            prs: result.prs
              .map((pr) => ({ ...pr, ...this.observedPRs.get(pr.number) }))
              .concat(
                this.state.snapshot.prs.filter(
                  (pr) =>
                    !result.prs.some(
                      (canonical) => canonical.number === pr.number,
                    ) && this.observedPRs.has(pr.number),
                ),
              ),
          },
        });
    } catch (e) {
      if (generation === this.generation && sequence === this.prSequence)
        this.reportError(e);
    }
  }
  mergePR = async (number: number, headSha: string) => {
    try {
      const result = await this.source.api.mergePR(
        this.hiveId,
        number,
        headSha,
      );
      await this.refresh();
      return result;
    } catch (error) {
      if (error instanceof APIError && error.status === 409) {
        await this.refresh();
        const message = "PR 有新提交，请重新查看";
        this.publish({ error: message });
        throw new APIError(message, 409);
      }
      throw error;
    }
  };
  /** Recompute local visibility after acknowledgement without fetching resources. */
  rederive = () => this.publish({});
  acknowledgePulses = () => this.publish({ pulses: [] });
  /** Opened historical Agents are fetched once, on demand. */
  loadAgentMessages = async (agentId: string) => {
    const address = `forager:${agentId}`;
    if (!this.state.snapshot || this.loadedMessageInboxes.has(address)) return;
    this.loadedMessageInboxes.add(address);
    try { await this.hydrateInbox(address); }
    catch (error) { this.loadedMessageInboxes.delete(address); throw error; }
  };
  /** Hydrate only previews visible in the currently opened conversation. */
  hydrateMessages = async (messageIds: string[]) => {
    const wanted = new Set(messageIds);
    const previews = this.state.snapshot?.messages.filter((message) => wanted.has(message.id) && "preview" in message) ?? [];
    const addresses = [...new Set(previews.map((message) => message.to))];
    await Promise.all(addresses.map(async (address) => {
      await this.hydrateInbox(address);
      // A preview may arrive after an already-running page read took its snapshot.
      const missing = this.state.snapshot?.messages.some((message) => wanted.has(message.id) && message.to === address && "preview" in message);
      if (missing && !this.stopped) await this.hydrateInbox(address);
    }));
  };
  private hydrateInbox = (address: string): Promise<void> => {
    const pending = this.messageHydrations.get(address);
    if (pending) return pending;
    const request = this.readInbox(address).finally(() => this.messageHydrations.delete(address));
    this.messageHydrations.set(address, request);
    return request;
  };
  private readInbox = async (address: string) => {
    const messages: import("./types").Message[] = [];
    let after: string | undefined;
    do {
      const page = await this.source.api.listMessages(this.hiveId, { to: address, after, limit: 200 });
      messages.push(...page.messages);
      if (!page.truncated || !page.cursor || page.cursor === after) break;
      after = page.cursor;
    } while (!this.stopped);
    if (this.stopped || !this.state.snapshot) return;
    const merged = new Map(this.state.snapshot.messages.map((message) => [message.id, message]));
    for (const message of messages) {
      const current = merged.get(message.id);
      if (!current || "preview" in current) merged.set(message.id, message);
    }
    this.publish({ snapshot: { ...this.state.snapshot, messages: [...merged.values()].sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt)) } });
  };
}
export const createProjectStore = (id: string, source?: DataSource) =>
  new ProjectStore(id, source);
