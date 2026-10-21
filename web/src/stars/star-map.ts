import type {
  HiveOverview,
  HistoryStar,
  ProjectState,
  ProjectStore,
  StarNode,
  LightPulse,
  PulseEndpoint,
} from "../data";
import { oneLineSummary, relativeTime, weightedTruncate } from "../data";
import type { StarsUI, ObjectSelection } from "./ui";
import { themeStore } from "./theme";
import { timelineFor } from "./timeline-state";
import { drawCanvasGlyph, type CanvasGlyphOptions, type StarGlyph } from "./star-glyphs";
type Point = { x: number; y: number };
type Body = Point & {
  node: StarNode;
  angle: number;
  radius: number;
  annotation: string;
  signal?: { kind: string; claimed: boolean };
  cluster?: { kind: "pr" | "issue"; items: { id: string; label: string }[] };
  ci?: { source: "message" | "pr-reason"; signalId?: string };
  stale?: boolean;
};
type System = Point & {
  id: string;
  name: string;
  on: boolean;
  state: ProjectState;
  bodies: Body[];
  decisionIds: Set<string>;
  needsAttention: boolean;
  history: HistoryStar[];
};
type Camera = Point & { scale: number };
export type MapNote = { kind: "history" | "cluster"; x: number; y: number; title: string; body: string; scope: "history" | "active"; query?: string };
export type MapHistoryHover = { id: string; x: number; y: number; summary: string; result: string; time: string };
const TAU = Math.PI * 2,
  TILT = -0.24,
  AS = 0.55;
const hash = (s: string) => {
  let h = 2166136261;
  for (const c of s) h = Math.imul(h ^ c.charCodeAt(0), 16777619);
  h = Math.imul(h ^ (h >>> 16), 0x85ebca6b);
  h = Math.imul(h ^ (h >>> 13), 0xc2b2ae35);
  return ((h ^ (h >>> 16)) >>> 0) / 4294967296;
};
const ellipse = (p: Point, angle: number, radius: number): Point => {
  const x = Math.cos(angle) * radius,
    y = Math.sin(angle) * radius * AS;
  return {
    x: p.x + x * Math.cos(TILT) - y * Math.sin(TILT),
    y: p.y + x * Math.sin(TILT) + y * Math.cos(TILT),
  };
};
const signalIsClaimed = (body: Body) => !!body.signal?.claimed;
const backgroundStars = (() => {
  let seed = 7;
  const rnd = () => (seed = (seed * 16807) % 2147483647) / 2147483647;
  return Array.from({ length: 520 }, () => ({ x: (rnd() - .5) * 5200, y: (rnd() - .5) * 3400,
    r: rnd() < .9 ? .4 + rnd() * .8 : 1.2 + rnd() * .8,
    alpha: .25 + rnd() * .6, cross: rnd() < .06 }));
})();
export type MapMetrics = {
  frames: number;
  durations: number[];
  pulses: number;
  merges: number;
  nodes: number;
  mode: string;
  positions: Record<string, Point>;
  trails: number;
  history: number;
  clusters: number;
  completing: number;
  flares: number;
};
export class StarMap {
  readonly metrics: MapMetrics = {
    frames: 0,
    durations: [],
    pulses: 0,
    merges: 0,
    nodes: 0,
    mode: "all",
    positions: {},
    trails: 0,
    history: 0,
    clusters: 0,
    completing: 0,
    flares: 0,
  };
  private ctx: CanvasRenderingContext2D;
  private width = 1;
  private height = 1;
  private dpr = 1;
  private camera: Camera = { x: 0, y: 0, scale: 0.4 };
  private target: Camera = { ...this.camera };
  private systems: System[] = [];
  private frozen?: System[];
  private frozenTrails = new Map<string, Body>();
  private pulses: { pulse: LightPulse; start: number }[] = [];
  private merging = new Map<string, { start: number; body: Body; systemId: string }>();
  private completing = new Map<string, { start: number; from: Point; to: Point; body: Body; systemId: string }>();
  private growing = new Map<string, number>();
  private flares: { systemId: string; start: number }[] = [];
  private previous = new Map<string, string>();
  private trails = new Map<string, Body>();
  private hover?: Body;
  private hoverHistory?: { history: HistoryStar; systemId: string };
  private note?: MapNote;
  private onNoteChange?: (note?: MapNote) => void;
  private onHistoryHoverChange?: (hover?: MapHistoryHover) => void;
  private dragging?: {
    body?: Body;
    start: Point;
    origin: Camera;
    moved: boolean;
    point: Point;
  };
  private frame?: number;
  private destroyed = false;
  private lock = 0;
  private createTimer?: ReturnType<typeof setTimeout>;
  private cleanups: (() => void)[] = [];
  private reduced = matchMedia("(prefers-reduced-motion: reduce)");
  private lastFrame = 0;
  private grain?: CanvasPattern | null;
  private lastView = "";
  constructor(
    private canvas: HTMLCanvasElement,
    private projects: HiveOverview[],
    private stores: ReadonlyMap<string, ProjectStore>,
    private ui: StarsUI,
    private assign: (id: string, number: number) => Promise<unknown>,
    private feedback: (message: string) => void,
    onNoteChange?: (note?: MapNote) => void,
    onHistoryHoverChange?: (hover?: MapHistoryHover) => void,
  ) {
    this.onNoteChange = onNoteChange;
    this.onHistoryHoverChange = onHistoryHoverChange;
    this.ctx = canvas.getContext("2d")!;
    const resize = new ResizeObserver(() => {
      const r = canvas.getBoundingClientRect();
      this.width = r.width;
      this.height = r.height;
      this.dpr = devicePixelRatio || 1;
      canvas.width = Math.round(r.width * this.dpr);
      canvas.height = Math.round(r.height * this.dpr);
      this.updateCamera(true);
      this.schedule();
    });
    resize.observe(canvas);
    this.cleanups.push(() => resize.disconnect());
    // Canvas text does not repaint itself when a downloaded font replaces the
    // fallback face. Coalesce font completion with other invalidations, then idle.
    void document.fonts.ready.then(this.schedule);
    document.fonts.addEventListener("loadingdone", this.schedule);
    this.cleanups.push(() =>
      document.fonts.removeEventListener("loadingdone", this.schedule),
    );
    this.cleanups.push(
      ...[...stores.values()].map((store) =>
        store.subscribe(() => this.sync()),
      ),
    );
    this.cleanups.push(
      ui.subscribe(() => {
        this.hover = undefined;
        this.hoverHistory = undefined;
        this.onHistoryHoverChange?.();
        this.syncTimeline();
        this.updateCamera();
        this.schedule();
      }),
    );
    this.cleanups.push(
      themeStore.subscribe(() => {
        this.grain = undefined;
        this.schedule();
      }),
    );
    this.cleanups.push(timelineFor(ui).subscribe(this.syncTimeline));
    const visibility = () => { if (document.hidden && this.frame !== undefined) cancelAnimationFrame(this.frame); if (document.hidden) this.frame = undefined; else this.schedule(); };
    document.addEventListener("visibilitychange", visibility);
    this.cleanups.push(() => document.removeEventListener("visibilitychange", visibility));
    const motion = () => {
      if (this.reduced.matches) {
        this.pulses = [];
        this.merging.clear();
        this.completing.clear();
        this.growing.clear();
        this.flares = [];
      }
      this.camera = { ...this.target };
      this.schedule();
    };
    this.reduced.addEventListener("change", motion);
    this.cleanups.push(() =>
      this.reduced.removeEventListener("change", motion),
    );
    const on = <K extends keyof HTMLElementEventMap>(
      type: K,
      fn: (event: HTMLElementEventMap[K]) => void,
      options?: AddEventListenerOptions,
    ) => {
      canvas.addEventListener(type, fn, options);
      this.cleanups.push(() => canvas.removeEventListener(type, fn));
    };
    on("pointerdown", (e) => this.down(e));
    on("pointermove", (e) => this.move(e));
    on("pointerup", (e) => this.up(e));
    on("pointercancel", () => {
      this.dragging = undefined;
      this.schedule();
    });
    on("pointerleave", (event) => {
      if (event.relatedTarget instanceof Element && event.relatedTarget.closest(".stars-history-hover")) return;
      if (!this.dragging) {
        this.hover = undefined;
        this.hoverHistory = undefined;
        this.onHistoryHoverChange?.();
        this.schedule();
      }
    });
    on("dblclick", () => {
      clearTimeout(this.createTimer);
      const view = this.ui.getSnapshot();
      if (view.selection) this.ui.selectProject(view.hiveId);
      else this.ui.selectProject();
      this.updateCamera(true);
      this.schedule();
    });
    on("wheel", (e) => this.wheel(e), { passive: false });
    this.sync();
    this.syncTimeline();
  }
  private syncTimeline = () => {
    const clock = timelineFor(this.ui).getSnapshot();
    const past =
      this.ui.getSnapshot().overlay === "timeline" &&
      (clock.fraction < 1 || clock.playing);
    if (past && !this.frozen) {
      this.frozen = this.systems;
      this.frozenTrails = new Map(this.trails);
    } else if (!past && this.frozen) {
      this.frozen = undefined;
      // Returning to the present shows the latest shared state immediately,
      // without replaying transitions received while inspecting the past.
      this.merging.clear();
      this.completing.clear();
    }
    this.schedule();
  };
  destroy() {
    this.destroyed = true;
    clearTimeout(this.createTimer);
    if (this.frame !== undefined) cancelAnimationFrame(this.frame);
    this.cleanups.forEach((fn) => fn());
  }
  private sync() {
    const now = performance.now();
    const oldBodies = new Map(this.systems.flatMap((s) => s.bodies.map((b) => [b.node.id, { body: b, systemId: s.id }] as const)));
    const wasReady = this.systems.some((s) => !!s.state.snapshot);
    this.systems = this.projects.map((project, index) => {
      const state = this.stores.get(project.hive.id)!.getSnapshot();
      const system: System = {
        x: ((index % 3) - Math.min(this.projects.length - 1, 2) / 2) * 960,
        y:
          Math.floor(index / 3) * 600 +
          ((index % 3) - Math.min(this.projects.length - 1, 2) / 2) * -150,
        id: project.hive.id,
        name: state.snapshot?.hive.name ?? project.hive.name,
        on: state.snapshot?.queen.enabled ?? project.hive.queenEnabled,
        state,
        bodies: [],
        decisionIds: new Set(state.derived?.decisions.map((d) => d.id)),
        needsAttention: state.derived?.needsAttention ?? false,
        history: state.derived?.history ?? [],
      };
      for (const node of state.derived?.mapNodes ?? []) {
        const prIndex = node.kind === "planet" ? state.derived?.prGroups.visible.findIndex((pr) => pr.number === node.prNumber) ?? -1 : -1;
        const issueIndex = node.kind === "signal" ? state.derived?.issueGroups.visible.findIndex((issue) => issue.messageId === node.id) ?? -1 : -1;
        const angle = prIndex >= 0 ? .15 + prIndex * TAU / (state.derived!.prGroups.cluster.length ? 9 : Math.max(1, state.derived!.prGroups.visible.length))
          : issueIndex >= 0 ? 3.05 + issueIndex * .2 : hash(node.id) * TAU;
        const radius =
          node.kind === "star"
            ? 0
            : node.kind === "planet"
              ? 212
              : node.kind === "signal"
                ? 352
                : node.kind === "queue"
                  ? 300
                  : 118 + hash(node.id + "r") * 65;
        const agent = state.snapshot?.agents.find((a) => a.id === node.id);
        const signal = state.snapshot?.signals.find(
          (signal) => signal.messageId === node.id,
        );
        const annotation = node.kind === "comet" ? node.label
          : node.kind === "signal" ? node.label
          : node.kind === "satellite" ? "验证中"
          : node.kind === "queue" ? "排队中" : node.label;
        const pr = state.snapshot?.prs.find((p) => p.number === node.prNumber);
        const body: Body = {
          ...ellipse(system, angle, radius),
          node,
          angle,
          radius,
          annotation,
          signal: signal && { kind: signal.kind, claimed: signal.claimed },
          stale: !!(pr && !pr.ready && Date.now() - Date.parse(pr.updatedAt || "") >= 7 * 86400000),
        };
        system.bodies.push(body);
        const old = this.previous.get(node.id);
        if (!oldBodies.has(node.id) && wasReady && !this.reduced.matches && ["comet", "planet"].includes(node.kind)) this.growing.set(node.id, now);
        this.previous.set(node.id, node.state ?? "");
      }
      for (const ci of state.derived?.failedCI ?? []) {
        const parent = system.bodies.find((b) => b.node.prNumber === ci.prNumber && b.node.kind === "planet");
        if (!parent) continue;
        system.bodies.push({ ...ellipse(parent, 0.65, 27), node: { id: ci.id, kind: "signal", hiveId: system.id, prNumber: ci.prNumber, label: ci.reason }, angle: .65, radius: 27, annotation: "CI 未通过", ci: { source: ci.source, signalId: ci.signalId } });
      }
      const addCluster = (kind: "pr" | "issue", label: string | undefined, items: { id: string; label: string }[], angle: number, radius: number) => {
        if (!label) return;
        system.bodies.push({ ...ellipse(system, angle, radius), node: { id: `${system.id}:cluster:${kind}`, kind: "queue", hiveId: system.id, label }, angle, radius, annotation: label, cluster: { kind, items } });
      };
      addCluster("pr", state.derived?.prGroups.clusterLabel, (state.derived?.prGroups.clusterPreview ?? []).map((pr) => ({ id: String(pr.number), label: `PR #${pr.number}  ${weightedTruncate(pr.title || "", 20)}` })), .15 + 8 * TAU / 9, 212);
      addCluster("issue", state.derived?.issueGroups.clusterLabel, (state.derived?.issueGroups.clusterPreview ?? []).map((issue) => ({ id: String(issue.number), label: `Issue #${issue.number}  ${weightedTruncate(issue.title || "", 20)}` })), 3.05 + 5 * .2, 352);
      for (const b of system.bodies.filter(
        (b) => b.node.kind === "satellite",
      )) {
        const parent = system.bodies.find((p) => p.node.id === b.node.parentId);
        if (parent) {
          b.x = parent.x + Math.cos(b.angle) * 15;
          b.y = parent.y + Math.sin(b.angle) * 9;
        }
      }
      return system;
    });
    if (wasReady) for (const [id, old] of oldBodies) {
      const next = this.systems.find((s) => s.id === old.systemId);
      if (!next || next.bodies.some((b) => b.node.id === id)) continue;
      if (old.body.node.kind === "comet" && next.history.some((h) => h.id === id)) {
        const history = next.history.find((h) => h.id === id)!;
        this.trails.set(id, old.body);
        if (!this.reduced.matches) this.completing.set(id, { start: now, from: this.metrics.positions[id] ? this.world(this.metrics.positions[id]) : old.body, to: { x: next.x + history.x, y: next.y + history.y }, body: old.body, systemId: next.id });
      }
      if (old.body.node.kind === "planet" && next.state.snapshot?.prs.some((p) => `${next.id}:pr:${p.number}` === id && p.merged)) {
        this.metrics.merges++;
        if (!this.reduced.matches) this.merging.set(id, { start: now, body: old.body, systemId: next.id });
      }
    }
    // Consume synchronously and acknowledge afterwards. Acknowledge publishes again;
    // clearing the queue first prevents reentrant consumption and duplicate animation.
    for (const store of this.stores.values()) {
      const pulses = store.getSnapshot().pulses;
      if (pulses.length) {
        this.metrics.pulses += pulses.length;
        if (!this.reduced.matches)
          this.pulses.push(...pulses.map((pulse) => ({ pulse, start: now })));
        store.acknowledgePulses();
      }
    }
    this.metrics.nodes = this.systems.reduce((n, s) => n + s.bodies.length, 0);
    this.metrics.trails = this.trails.size;
    this.metrics.history = this.systems.reduce((n, s) => n + s.history.length, 0);
    this.metrics.clusters = this.systems.reduce((n, s) => n + s.bodies.filter((b) => b.cluster).length, 0);
    this.metrics.completing = this.completing.size;
    this.updateCamera();
    this.schedule();
  }
  private updateCamera(force = false) {
    const view = this.ui.getSnapshot(),
      key = `${view.hiveId}:${view.selection?.id}:${view.projectPanel}`;
    if (!force && key === this.lastView) return;
    this.lastView = key;
    const system = this.systems.find((s) => s.id === view.hiveId);
    if (!system) {
      const rows = Math.max(1, Math.ceil(this.projects.length / 3));
      this.target = {
        x: 0,
        y: (rows - 1) * 300,
        scale: Math.max(
          0.08,
          Math.min(
            (this.width - 120) /
              (Math.min(3, Math.max(1, this.projects.length)) * 960),
            (this.height - 300) / (rows * 600),
            0.8,
          ),
        ),
      };
      this.metrics.mode = "all";
    } else {
      const selected = system.bodies.find(
        (b) =>
          b.node.id === view.selection?.id ||
          (view.selection?.kind === "queen" && b.node.kind === "star"),
      );
      const scale = selected
        ? Math.min(2.4, this.height / 420)
        : Math.max(
            0.3,
            Math.min((this.height - 250) / ((352 * AS + 30) * 2), (this.width - 320) / ((352 + 50) * 2), 1.8),
          );
      this.target = {
        x:
          (selected?.x ?? system.x) +
          (selected && this.width > 800 ? 180 / scale : 0) -
          (view.projectPanel && this.width > 800 ? 170 / scale : 0),
        y: selected?.y ?? system.y,
        scale,
      };
      this.metrics.mode = selected ? "object" : "project";
    }
    if (force || this.reduced.matches) this.camera = { ...this.target };
  }
  private screen(p: Point): Point {
    return {
      x: (p.x - this.camera.x) * this.camera.scale + this.width / 2,
      y: (p.y - this.camera.y) * this.camera.scale + this.height / 2,
    };
  }
  private world(p: Point): Point {
    return {
      x: (p.x - this.width / 2) / this.camera.scale + this.camera.x,
      y: (p.y - this.height / 2) / this.camera.scale + this.camera.y,
    };
  }
  private local(e: MouseEvent) {
    const r = this.canvas.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  }
  private hit(p: Point): Body | undefined {
    const view = this.ui.getSnapshot();
    if (!view.hiveId)
      return this.systems
        .find((s) => {
          const q = this.screen(s);
          return (
            Math.hypot((p.x - q.x) / 330, (p.y - q.y) / 200) < this.camera.scale
          );
        })
        ?.bodies.find((b) => b.node.kind === "star");
    return this.systems
      .filter((s) => s.id === view.hiveId)
      .flatMap((s) => s.bodies)
      .map((b) => ({
        b,
        d: Math.hypot((this.metrics.positions[b.node.id] ?? this.screen(b)).x - p.x, (this.metrics.positions[b.node.id] ?? this.screen(b)).y - p.y),
      }))
      .filter((v) => v.d < 17)
      .sort((a, b) => a.d - b.d)[0]?.b;
  }
  private hitHistory(p: Point) {
    const id = this.ui.getSnapshot().hiveId;
    if (!id) return;
    const system = this.systems.find((s) => s.id === id);
    if (!system) return;
    for (const history of system.history) {
      if (history.dust || this.completing.has(history.id)) continue;
      const q = this.screen({ x: system.x + history.x, y: system.y + history.y });
      if (Math.hypot(q.x - p.x, q.y - p.y) < 7) return { history, systemId: id };
    }
  }
  private selection(body: Body): ObjectSelection | undefined {
    const n = body.node;
    if (n.kind === "queue") return;
    if (body.ci) {
      if (body.ci.source === "message" && body.ci.signalId)
        return { hiveId: n.hiveId, kind: "signal", id: body.ci.signalId, number: n.prNumber };
      return { hiveId: n.hiveId, kind: "pr", id: `${n.hiveId}:pr:${n.prNumber}`, number: n.prNumber };
    }
    return {
      hiveId: n.hiveId,
      id: n.id,
      kind:
        n.kind === "star"
          ? "queen"
          : n.kind === "planet"
            ? "pr"
            : n.kind === "signal"
              ? "signal"
              : "agent",
      number: n.prNumber,
    };
  }
  private down(e: PointerEvent) {
    if (e.button !== 0 || this.ui.getSnapshot().overlay === "timeline") return;
    const point = this.local(e),
      hit = this.hit(point),
      system = this.systems.find((s) => s.id === hit?.node.hiveId);
    const signal = system?.state.snapshot?.signals.find(
      (s) => s.messageId === hit?.node.id,
    );
    this.dragging = {
      body:
        hit?.node.kind === "signal" &&
        signal &&
        !signal.claimed &&
        signal.kind === "issue" &&
        system?.on
          ? hit
          : undefined,
      start: point,
      point,
      origin: { ...this.target },
      moved: false,
    };
    this.canvas.setPointerCapture(e.pointerId);
  }
  private move(e: PointerEvent) {
    const p = this.local(e),
      d = this.dragging;
    if (d) {
      d.point = p;
      d.moved ||= Math.hypot(p.x - d.start.x, p.y - d.start.y) > 4;
      if (d.moved && !d.body) {
        this.camera = this.target = {
          ...d.origin,
          x: d.origin.x - (p.x - d.start.x) / this.camera.scale,
          y: d.origin.y - (p.y - d.start.y) / this.camera.scale,
        };
      }
    } else {
      this.hover = this.hit(p);
      const next = this.hover ? undefined : this.hitHistory(p);
      if (next?.history.id !== this.hoverHistory?.history.id) {
        this.hoverHistory = next;
        const system = next && this.systems.find((s) => s.id === next.systemId);
        const q = next && system && this.screen({ x: system.x + next.history.x, y: system.y + next.history.y });
        this.onHistoryHoverChange?.(next && q ? {
          id: next.history.id, x: q.x, y: q.y,
          summary: next.history.summary,
          result: next.history.result === "merged" ? "已合并" : next.history.result === "failed" ? "失败" : "已完成",
          time: relativeTime(next.history.endedAt),
        } : undefined);
      }
    }
    this.canvas.style.cursor = d?.moved
      ? d.body
        ? "grabbing"
        : "move"
      : this.hover || this.hoverHistory
        ? "pointer"
        : "default";
    this.schedule();
  }
  private up(e: PointerEvent) {
    const d = this.dragging;
    this.dragging = undefined;
    if (!d) return;
    if (d.body && d.moved) {
      const system = this.systems.find((s) => s.id === d.body!.node.hiveId)!;
      const point = this.world(this.local(e)),
        signal = system.state.snapshot?.signals.find(
          (s) => s.messageId === d.body!.node.id,
        );
      if (signal && Math.hypot(point.x - system.x, point.y - system.y) < 90) {
        this.feedback(`正在请主 Agent 处理 Issue #${signal.number}…`);
        void this.assign(system.id, signal.number).then(
          () => this.feedback(`已请主 Agent 处理 Issue #${signal.number}`),
          (error) =>
            this.feedback(
              error instanceof Error ? error.message : String(error),
            ),
        );
      }
    } else if (!d.moved) {
      const body = this.hit(this.local(e));
      const history = body ? undefined : this.hitHistory(this.local(e));
      if (!this.ui.getSnapshot().hiveId && body)
        this.ui.selectProject(body.node.hiveId);
      else if (!this.ui.getSnapshot().hiveId && !body) {
        clearTimeout(this.createTimer);
        this.createTimer = setTimeout(() => {
          if (
            !this.ui.getSnapshot().hiveId &&
            !this.ui.getSnapshot().overlay &&
            !this.ui.getSnapshot().platformPanel
          )
            window.dispatchEvent(new CustomEvent("swarmjet:create-project"));
        }, 280);
      } else if (history) {
        this.onHistoryHoverChange?.();
        const s = this.systems.find((x) => x.id === history.systemId)!;
        const q = this.screen({ x: s.x + history.history.x, y: s.y + history.history.y });
        this.note = { kind: "history", x: q.x, y: q.y, title: `${history.history.result === "merged" ? "已合并" : history.history.result === "failed" ? "失败，已看过" : "已完成"}，${relativeTime(history.history.endedAt)}`, body: `${history.history.summary}\n结束时间按 Agent 最近更新时间估算。`, scope: "history", query: history.history.id };
        this.onNoteChange?.(this.note);
      } else if (body?.cluster) {
        const q = this.screen(body);
        this.note = { kind: "cluster", x: q.x, y: q.y, title: body.annotation, body: body.cluster.items.map((item) => item.label).join("\n") + (body.cluster.items.length >= 6 ? "\n……" : ""), scope: "active", query: body.cluster.kind === "pr" ? "PR" : "Issue" };
        this.onNoteChange?.(this.note);
      } else if (body) {
        this.onNoteChange?.();
        const selection = this.selection(body);
        if (selection) this.ui.selectObject(selection);
        else {
          const system = this.systems.find((s) => s.id === body.node.hiveId);
          const item = system?.state.snapshot?.queue.find(
            (q) => q.agentId === body.node.agentId,
          );
          if (item)
            this.feedback(
              `排队中，第 ${item.position} 位：${item.task}。等待可用的 Agent 名额。`,
            );
        }
      } else if (this.ui.getSnapshot().selection)
        this.ui.selectProject(this.ui.getSnapshot().hiveId);
    }
    this.schedule();
  }
  private wheel(e: WheelEvent) {
    e.preventDefault();
    if (
      performance.now() < this.lock ||
      Math.abs(e.deltaY) < 4 ||
      this.ui.getSnapshot().overlay === "timeline"
    )
      return;
    this.lock = performance.now() + 450;
    const view = this.ui.getSnapshot(),
      point = this.local(e);
    if (e.deltaY < 0) {
      if (!view.hiveId) {
        const p = this.world(point);
        const nearest = [...this.systems].sort(
          (a, b) =>
            Math.hypot(a.x - p.x, a.y - p.y) - Math.hypot(b.x - p.x, b.y - p.y),
        )[0];
        if (nearest) this.ui.selectProject(nearest.id);
      } else {
        const b = this.hit(point),
          selection = b && this.selection(b);
        if (selection) this.ui.selectObject(selection);
      }
    } else if (view.selection) this.ui.selectProject(view.hiveId);
    else this.ui.selectProject();
  }
  private schedule = () => {
    if (!this.destroyed && !document.hidden && this.frame === undefined)
      this.frame = requestAnimationFrame(this.draw);
  };
  private endpoint(endpoint: PulseEndpoint, system: System): Point {
    if (endpoint.kind === "observer")
      return this.world({
        x: this.width / 2,
        y: this.height - (this.width < 650 ? 153 : 107),
      });
    if (endpoint.kind === "edge") return ellipse(system, -2.6, 300);
    return system.bodies.find((b) => b.node.id === endpoint.id) ?? system;
  }
  private draw = (now: number) => {
    this.frame = undefined;
    if (document.hidden) return;
    if (!this.reduced.matches && now - this.lastFrame < 32) { this.schedule(); return; }
    this.lastFrame = now;
    const started = performance.now(),
      ctx = this.ctx,
      theme = themeStore.getSnapshot(),
      { colors: C, render: R } = theme,
      simple = theme.style === "simple";
    const delta =
      Math.abs(this.camera.x - this.target.x) +
      Math.abs(this.camera.y - this.target.y) +
      Math.abs(this.camera.scale - this.target.scale) * 1000;
    const moving = delta > 0.15 && !this.reduced.matches;
    if (moving) {
      this.camera.x += (this.target.x - this.camera.x) * 0.06;
      this.camera.y += (this.target.y - this.camera.y) * 0.06;
      this.camera.scale = Math.exp(Math.log(this.camera.scale) + (Math.log(this.target.scale) - Math.log(this.camera.scale)) * .06);
    } else this.camera = { ...this.target };
    const s = this.camera.scale;
    ctx.setTransform(this.dpr, 0, 0, this.dpr, 0, 0);
    ctx.globalAlpha = 1;
    ctx.fillStyle = C.paper;
    ctx.fillRect(0, 0, this.width, this.height);
    const circle = (p: Point, r: number, fill = false) => {
      ctx.beginPath();
      ctx.arc(p.x, p.y, Math.max(0.1, r), 0, TAU);
      fill ? ctx.fill() : ctx.stroke();
    };
    const line = (a: Point, b: Point) => {
      ctx.beginPath();
      ctx.moveTo(a.x, a.y);
      ctx.lineTo(b.x, b.y);
      ctx.stroke();
    };
    const glyph = (kind: StarGlyph, at: Point, scale = 1, angle = 0, options?: CanvasGlyphOptions) => {
      ctx.save(); ctx.translate(at.x, at.y); ctx.rotate(angle); ctx.scale(scale, scale);
      drawCanvasGlyph(ctx, kind, options); ctx.restore();
    };
    const orbit = (p: Point, radius: number, a = 0, b = TAU) => {
      const q = this.screen(p);
      ctx.beginPath();
      ctx.ellipse(
        q.x,
        q.y,
        radius * this.camera.scale,
        radius * AS * this.camera.scale,
        TILT,
        a,
        b,
      );
      ctx.stroke();
    };
    const label = (
      text: string,
      p: Point,
      size = 13,
      italic = true,
      color = C.ink2,
      align: CanvasTextAlign = "center",
    ) => {
      ctx.fillStyle = color;
      ctx.font = `${italic && !simple ? "italic " : ""}${size}px ${simple ? '"IBM Plex Sans","Noto Sans SC",sans-serif' : '"Source Serif 4","Noto Serif SC",serif'}`;
      ctx.textAlign = align;
      ctx.fillText(text, p.x, p.y);
    };
    ctx.fillStyle = C.ink;
    ctx.strokeStyle = C.ink;
    ctx.lineWidth = 0.6 * R.lw;
    for (const star of backgroundStars) {
      if (simple && star.r < 1.15) continue;
      const parallax = Math.max(.35, s * .55);
      const x = (star.x - this.camera.x * .35) * parallax + this.width / 2;
      const y = (star.y - this.camera.y * .35) * parallax + this.height / 2;
      if (x < 0 || y < 0 || x > this.width || y > this.height) continue;
      if (this.systems.some((system) => {
        const p = this.screen(system);
        return Math.hypot((x - p.x) / (840 * s), (y - p.y) / (650 * s)) < 1;
      })) continue;
      ctx.globalAlpha = R.starA * star.alpha;
      if (star.cross) {
        ctx.strokeStyle = C.ink; ctx.lineWidth = .6 * R.lw;
        line({ x: x - 3, y }, { x: x + 3, y });
        line({ x, y: y - 3 }, { x, y: y + 3 });
      } else circle({ x, y }, star.r, true);
    }
    const view = this.ui.getSnapshot(),
      clock = timelineFor(this.ui).getSnapshot(),
      systems = this.frozen ?? this.systems;
    if (R.grid && !simple && !view.hiveId) {
      ctx.strokeStyle = C.hair;
      ctx.globalAlpha = 0.8;
      for (let r = 420; r < 3000; r += 420) orbit({ x: 0, y: 0 }, r);
      for (let k = 0; k < 12; k++) {
        const a = (k * TAU) / 12;
        line(
          this.screen(ellipse({ x: 0, y: 0 }, a, 420)),
          this.screen(ellipse({ x: 0, y: 0 }, a, 2800)),
        );
      }
    }
    this.metrics.positions = {};
    const pendingLabels: { text: string; p: Point; size: number; italic: boolean; color: string; align: CanvasTextAlign; priority: number; alpha: number }[] = [];
    for (const system of systems) {
      const dim = view.hiveId && system.id !== view.hiveId ? .3 : 1;
      for (const h of system.history) {
        if (this.completing.has(h.id) && !this.frozen) continue;
        const q = this.screen({ x: system.x + h.x, y: system.y + h.y });
        if (!h.dust) this.metrics.positions[`history:${h.id}`] = q;
        if (q.x < -30 || q.y < -30 || q.x > this.width + 30 || q.y > this.height + 30) continue;
        if (h.dust) {
          ctx.fillStyle = C.ink; ctx.globalAlpha = .16 * dim; ctx.fillRect(q.x, q.y, .9, .9); continue;
        }
        const z = Math.max(.7, Math.min(1.6, .55 + s * .8)) * R.sz;
        const r = (h.result === "merged" ? 1.35 : h.result === "failed" ? .8 : .95) * z;
        ctx.fillStyle = C.ink;
        ctx.globalAlpha = (h.result === "merged" ? .85 : h.result === "failed" ? .32 : .6) * h.brightness * dim;
        glyph(h.fresh && h.result !== "failed" && !simple ? "hist" : "star", q, h.fresh && h.result !== "failed" && !simple ? r / 1.6 : r / 4.5);
        if (this.hoverHistory?.history.id === h.id) {
          ctx.strokeStyle = C.ink2; ctx.globalAlpha = 1; circle(q, 8);
        }
      }
    }
    for (const system of systems) {
      const far = !!view.hiveId && system.id !== view.hiveId,
        dim = far ? 0.1 : 1,
        p = this.screen(system);
      ctx.globalAlpha = dim;
      if (R.halo && !far) {
        ctx.fillStyle = R.haloColor;
        ctx.globalAlpha = R.halo;
        ctx.beginPath();
        ctx.ellipse(p.x, p.y, 300 * s, 300 * AS * s, TILT, 0, TAU);
        ctx.fill();
      }
      ctx.globalAlpha = dim * 0.7;
      ctx.strokeStyle = C.ink3;
      ctx.lineWidth = 0.8 * R.lw;
      ctx.setLineDash(R.dotted ? [1, 5] : []);
      orbit(system, 300);
      ctx.setLineDash([]);
      ctx.strokeStyle = C.hair;
      orbit(system, 130);
      orbit(system, 212);
      if (clock.fraction < 1) {
        ctx.strokeStyle = C.ink;
        ctx.lineWidth = 0.8 * R.lw;
        ctx.globalAlpha = 0.23 * dim;
        for (let i = 0; i < Math.floor((1 - clock.fraction) * 35); i++)
          orbit(
            system,
            118 + hash(`trail${i}`) * 70,
            hash(`angle${i}`) * TAU,
            hash(`angle${i}`) * TAU + 0.2 + hash(`len${i}`) * 2,
          );
      }
      for (const trail of (this.frozen
        ? this.frozenTrails
        : this.trails
      ).values())
        if (trail.node.hiveId === system.id) {
          ctx.globalAlpha = 0.35 * dim;
          ctx.strokeStyle = C.ink2;
          ctx.setLineDash([1, 3]);
          orbit(system, trail.radius, trail.angle - 1.5, trail.angle);
          ctx.setLineDash([]);
        }
      for (const body of system.bodies) {
        const node = body.node;
        let opacity = 1;
        let world: Point = body;
        if (!this.reduced.matches && !this.frozen) {
          if (node.kind === "comet" && ["active", "creating"].includes(node.state ?? ""))
            world = ellipse(system, body.angle + now * .00008, body.radius);
          else if (node.kind === "planet" || body.cluster)
            world = ellipse(system, body.angle + now * .000011, body.radius);
          else if (node.kind === "satellite") {
            const parent = system.bodies.find((b) => b.node.id === node.parentId);
            if (parent) {
              const pp = ellipse(system, parent.angle + now * .000011, parent.radius);
              world = { x: pp.x + Math.cos(now * .0018 + body.angle) * 15, y: pp.y + Math.sin(now * .0018 + body.angle) * 9 };
            }
          } else if (body.ci) {
            const parent = system.bodies.find((candidate) => candidate.node.kind === "planet" && candidate.node.prNumber === node.prNumber);
            if (parent) world = ellipse(ellipse(system, parent.angle + now * .000011, parent.radius), .65, 27);
          }
          const born = this.growing.get(node.id);
          if (born !== undefined) {
            const t = Math.min(1, (now - born) / (node.kind === "planet" ? 1100 : 1800));
            if (node.kind === "comet") world = { x: system.x + (world.x - system.x) * (1 - (1 - t) ** 3), y: system.y + (world.y - system.y) * (1 - (1 - t) ** 3) };
            else opacity = Math.max(.05, t);
            if (t >= 1) this.growing.delete(node.id);
          }
        }
        if (this.dragging?.body === body)
          world = this.world(this.dragging.point);
        const q = this.screen(world);
        this.metrics.positions[node.id] = q;
        if (
          q.x < -80 ||
          q.x > this.width + 80 ||
          q.y < -80 ||
          q.y > this.height + 80
        )
          continue;
        ctx.globalAlpha = dim * opacity * (body.stale ? .55 : 1);
        ctx.strokeStyle = C.ink;
        ctx.fillStyle = C.ink;
        ctx.lineWidth = R.lw;
        const r = Math.max(2.2, Math.min(s, 1.6) * 4) * R.sz;
        if (far) {
          circle(q, 1.2, true);
          continue;
        }
        if (node.kind === "star") {
          const sr = Math.max(3.5, 9 * s) * R.sz;
          if (system.on) {
            glyph("star", q, sr / 4.5);
            for (const k of [1.8, 2.6, 3.6]) {
              ctx.globalAlpha = (dim * 0.55) / k;
              circle(q, sr * k);
            }
            ctx.globalAlpha = dim * opacity * 0.5;
            for (let k = 0; k < 24; k++) {
              const a = (k * TAU) / 24 + (this.reduced.matches || this.frozen ? 0 : now * .00009);
              line(
                {
                  x: q.x + Math.cos(a) * sr * 4.3,
                  y: q.y + Math.sin(a) * sr * 4.3,
                },
                {
                  x: q.x + Math.cos(a) * sr * (k % 2 ? 4.9 : 5.6),
                  y: q.y + Math.sin(a) * sr * (k % 2 ? 4.9 : 5.6),
                },
              );
            }
          } else {
            circle(q, sr);
            line(
              { x: q.x - sr * 0.6, y: q.y - sr * 0.6 },
              { x: q.x + sr * 0.6, y: q.y + sr * 0.6 },
            );
          }
        } else if (node.kind === "comet") {
          const dx = world.x - system.x,
            dy = world.y - system.y,
            len = Math.hypot(dx, dy) || 1,
            ux = dx / len,
            uy = dy / len;
          if (!view.hiveId) {
            circle(q, r, true);
          } else if (node.state === "failed") {
            glyph("failed", q, r / 3.5);
            ctx.setLineDash([2, 5]);
            line(
              { x: q.x + ux * 6, y: q.y + uy * 6 },
              { x: q.x + ux * 34, y: q.y + uy * 34 },
            );
            ctx.setLineDash([]);
          } else if (
            ["parked", "hibernated", "cancelled"].includes(node.state ?? "")
          ) {
            glyph("parked", q, r / 3, Math.atan2(uy, ux));
            if (node.label.includes("仍占着名额")) circle(q, r + 5);
          } else {
            const length =
              (node.state === "completed" ? 16 : 38) *
              Math.min(Math.max(s, 0.5), 1.8);
            const rays = this.metrics.nodes > 200 ? [-3, 0, 3] : [-3, -2, -1, 0, 1, 2, 3];
            for (const offset of rays) {
              const a = offset * 0.055,
                vx = ux * Math.cos(a) - uy * Math.sin(a),
                vy = ux * Math.sin(a) + uy * Math.cos(a);
              ctx.globalAlpha = dim * opacity * (0.55 - Math.abs(offset) * 0.11);
              line(q, {
                x: q.x + vx * length * (1 - Math.abs(offset) * 0.13),
                y: q.y + vy * length * (1 - Math.abs(offset) * 0.13),
              });
            }
            ctx.globalAlpha = dim * opacity;
            glyph("busy", q, r / 3, Math.atan2(uy, ux));
            ctx.globalAlpha = dim * opacity * 0.35;
            circle(q, r + 3.5);
          }
        } else if (node.kind === "planet") {
          const born = this.growing.get(node.id);
          const grow = born === undefined ? 1 : Math.max(.05, Math.min(1, (now - born) / 1100));
          const pr = Math.max(3, 7 * Math.min(s, 1.5)) * R.sz * grow * (body.stale ? .72 : 1);
          ctx.fillStyle = C.paper;
          glyph("pr", q, pr / 4);
          if (system.decisionIds.has(node.id)) {
            ctx.strokeStyle = C.accent;
            ctx.setLineDash([2, 3]);
            circle(q, pr + 7);
            ctx.setLineDash([]);
          }
        } else if (node.kind === "satellite") {
          const parent = system.bodies.find((b) => b.node.id === node.parentId);
          if (parent && view.hiveId) {
            const parentWorld = this.reduced.matches || this.frozen ? parent : ellipse(system, parent.angle + now * .000011, parent.radius);
            const pp = this.screen(parentWorld);
            ctx.strokeStyle = C.ink3;
            ctx.fillStyle = C.ink;
            glyph("moon", pp, 1, 0, { moonAngle: this.reduced.matches || this.frozen ? body.angle : now * .0018 + body.angle, moonOrbitX: 15 * s, moonOrbitY: 9 * s, moonRadius: Math.max(1.5, 2.5 * s) });
          }
          else glyph("star", q, Math.max(1.5, 2.5 * s) / 4.5);
        } else if (body.cluster) {
          glyph("cluster", q, Math.max(.5, s));
          ctx.setLineDash([1, 3]); circle(q, Math.max(7, s * 9)); ctx.setLineDash([]);
        } else if (node.kind === "signal") {
          const signal = body.signal,
            k = Math.max(2.8, 5 * Math.min(s, 1.4));
          ctx.strokeStyle = signal?.claimed ? C.ink2 : C.ink;
          if (body.ci) {
            glyph("ci", q, k / 5);
          } else {
            glyph("signal", q, k / 5);
            if (!signal?.claimed) {
              ctx.globalAlpha = 0.45;
              circle(q, k + 6);
            }
          }
        } else if (node.kind === "queue") {
          glyph("queue", q, Math.max(.6, s));
        }
        if (view.hiveId && node.kind !== "star") {
          ctx.globalAlpha = dim * opacity;
          const comet = node.kind === "comet",
            satellite = node.kind === "satellite",
            right = body.x > system.x;
          pendingLabels.push({ text: body.cluster ? body.annotation : node.kind === "queue" ? weightedTruncate(body.annotation, 14) : body.annotation, p: {
              x: q.x + (comet ? (right ? -15 : 15) : satellite ? -12 : 0),
              y: q.y + (comet ? 4 : satellite ? -22 : 22),
            }, size: comet ? 13 : 12, italic: !comet && !body.cluster, color: body.cluster ? C.ink2 : comet ? C.ink2 : C.ink3,
            align: comet ? (right ? "right" : "left") : satellite ? "right" : "center",
            priority: body.cluster ? 3 : node.state === "failed" ? 2 : node.kind === "comet" ? node.state === "parked" ? 8 : 4 : node.kind === "satellite" ? 5 : node.kind === "signal" ? signalIsClaimed(body) ? 9 : 6 : node.kind === "planet" ? system.decisionIds.has(node.id) ? 1 : 7 : 10,
            alpha: body.stale ? .4 : 1 });
        }
        if (this.hover?.node.id === node.id || view.selection?.id === node.id) {
          ctx.globalAlpha = 1;
          ctx.strokeStyle = C.ink2;
          ctx.lineWidth = 0.8 * R.lw;
          circle(q, node.kind === "star" ? 40 : 15);
        }
      }
      if (!view.hiveId) {
        ctx.globalAlpha = 1;
        ctx.fillStyle = C.ink;
        ctx.font = `${simple ? "" : "italic "}19px ${simple ? '"IBM Plex Sans","Noto Sans SC",sans-serif' : '"IM Fell English","Noto Serif SC",serif'}`;
        ctx.textAlign = "center";
        ctx.fillText(system.name, p.x, p.y + 300 * AS * s + 30);
        if (system.decisionIds.size) label(`${system.decisionIds.size} 件事等你`, { x: p.x, y: p.y + 300 * AS * s + 49 }, 13, true, C.accent);
      }
    }
    // Existing labels occupy space in strict data-layer priority order.
    if (view.hiveId) {
      const focus = systems.find((system) => system.id === view.hiveId);
      const q = focus && this.screen(focus);
      const r = Math.max(9 * s, 3.5) * 4.2;
      const occupied: [number, number, number, number][] = q ? [[q.x - r, q.y - r, q.x + r, q.y + r]] : [];
      for (const item of pendingLabels.sort((a, b) => a.priority - b.priority)) {
        ctx.font = `${item.italic && !simple ? "italic " : ""}${item.size}px ${simple ? '"IBM Plex Sans","Noto Sans SC",sans-serif' : '"Source Serif 4","Noto Serif SC",serif'}`;
        const w = ctx.measureText(item.text).width;
        const left = item.align === "center" ? item.p.x - w / 2 : item.align === "right" ? item.p.x - w : item.p.x;
        const box: [number, number, number, number] = [left - 4, item.p.y - item.size - 1, left + w + 4, item.p.y + 5];
        if (occupied.some((other) => box[0] < other[2] && box[2] > other[0] && box[1] < other[3] && box[3] > other[1])) continue;
        occupied.push(box);
        ctx.globalAlpha = item.alpha;
        label(item.text, item.p, item.size, item.italic, item.color, item.align);
      }
    }
    if (this.hover && view.hiveId === this.hover.node.hiveId) {
      const system = systems.find((item) => item.id === view.hiveId);
      const node = this.hover.node;
      const agent = system?.state.snapshot?.agents.find((item) => item.id === node.agentId);
      const pr = system?.state.snapshot?.prs.find((item) => item.number === node.prNumber);
      const issue = system?.state.snapshot?.signals.find((item) => item.messageId === node.id);
      const full = agent ? oneLineSummary(agent, { prs: system?.state.snapshot?.prs, issues: system?.state.snapshot?.signals })
        : pr?.title || issue?.title || node.label;
      const q = this.metrics.positions[node.id];
      if (q) { ctx.globalAlpha = 1; label(full, { x: q.x, y: q.y - 18 }, 14, true, C.ink); }
    }
    if (!this.frozen) {
      for (const [id, leaving] of this.completing) {
        const t = Math.min(1, (now - leaving.start) / 2600), ease = 1 - (1 - t) ** 3;
        const q = this.screen({ x: leaving.from.x + (leaving.to.x - leaving.from.x) * ease, y: leaving.from.y + (leaving.to.y - leaving.from.y) * ease });
        ctx.fillStyle = C.ink; ctx.strokeStyle = C.ink; ctx.globalAlpha = 1 - .45 * ease;
        const radius = Math.max(.8, (1 - .7 * ease) * 4 * s);
        circle(q, radius, true);
        if (t < 1) { ctx.globalAlpha *= .45; line(q, { x: q.x + 16 * (1 - t), y: q.y + 5 * (1 - t) }); }
        else {
          const age = (now - leaving.start - 2600) / 800;
          if (age < 1) { ctx.globalAlpha = (1 - age) * .7; circle(q, radius + 3 + age * 24); }
          else this.completing.delete(id);
        }
      }
      for (const [id, merge] of this.merging) {
        const sys = systems.find((system) => system.id === merge.systemId);
        if (!sys) continue;
        const t = Math.min(1, (now - merge.start) / 1400);
        const world = ellipse(sys, merge.body.angle + t * TAU * 2, merge.body.radius * (1 - t) ** 2);
        const q = this.screen(world);
        ctx.save(); ctx.translate(q.x, q.y); ctx.scale(Math.max(.4, s * (1 - t)), Math.max(.4, s * (1 - t)));
        ctx.globalAlpha = 1 - t; ctx.strokeStyle = C.ink; ctx.fillStyle = C.paper; drawCanvasGlyph(ctx, "pr"); ctx.restore();
        if (t >= 1) { this.merging.delete(id); this.flares.push({ systemId: sys.id, start: now }); }
      }
      this.flares = this.flares.filter((flare) => now - flare.start < 1200);
      for (const flare of this.flares) {
        const sys = systems.find((system) => system.id === flare.systemId);
        if (!sys) continue;
        const t = (now - flare.start) / 1200;
        ctx.strokeStyle = C.ink; ctx.globalAlpha = (1 - t) * .8;
        circle(this.screen(sys), (14 + t * 90) * s);
      }
    }
    this.pulses = this.pulses.filter((p) => now - p.start < 800);
    if (!this.frozen)
      for (const { pulse, start } of this.pulses) {
        const system = this.systems.find((s) => s.id === pulse.hiveId);
        if (!system) continue;
        const from = this.screen(this.endpoint(pulse.from, system)),
          to = this.screen(this.endpoint(pulse.to, system)),
          t = (now - start) / 800,
          dx = to.x - from.x,
          dy = to.y - from.y,
          len = Math.hypot(dx, dy) || 1,
          p = { x: from.x + dx * t, y: from.y + dy * t };
        ctx.globalAlpha = 0.8;
        ctx.strokeStyle =
          pulse.from.kind === "observer" || pulse.to.kind === "observer"
            ? C.accent
            : C.ink;
        ctx.lineWidth = 1.3;
        line(p, { x: p.x - (dx / len) * 12, y: p.y - (dy / len) * 12 });
      }
    if (!simple && R.grain) {
      if (!this.grain) {
        const tile = document.createElement("canvas");
        tile.width = tile.height = 128;
        const c = tile.getContext("2d")!,
          image = c.createImageData(128, 128);
        for (let i = 0; i < image.data.length; i += 4) {
          const v = hash(`grain${i}`) * 255;
          image.data[i] = image.data[i + 1] = image.data[i + 2] = v;
          image.data[i + 3] = 128;
        }
        c.putImageData(image, 0, 0);
        this.grain = ctx.createPattern(tile, "repeat");
      }
      ctx.globalAlpha = R.grain;
      ctx.fillStyle = this.grain!;
      ctx.fillRect(0, 0, this.width, this.height);
    }
    if (!simple) {
      ctx.globalAlpha = 1;
      ctx.strokeStyle = C.hair;
      ctx.lineWidth = R.lw;
      for (const m of [26, 32])
        ctx.strokeRect(
          m + 0.5,
          m + 0.5,
          this.width - 2 * m - 1,
          this.height - 2 * m - 1,
        );
      ctx.beginPath();
      for (let x = 32 + R.tick, k = 1; x < this.width - 32; x += R.tick, k++) {
        const l = k % 5 ? 3 : 7;
        ctx.moveTo(x, 32);
        ctx.lineTo(x, 32 + l);
        ctx.moveTo(x, this.height - 32);
        ctx.lineTo(x, this.height - 32 - l);
      }
      for (let y = 32 + R.tick, k = 1; y < this.height - 32; y += R.tick, k++) {
        const l = k % 5 ? 3 : 7;
        ctx.moveTo(32, y);
        ctx.lineTo(32 + l, y);
        ctx.moveTo(this.width - 32, y);
        ctx.lineTo(this.width - 32 - l, y);
      }
      ctx.stroke();
    }
    ctx.globalAlpha = 1;
    this.metrics.flares = this.flares.length;
    this.metrics.frames++;
    this.metrics.durations.push(performance.now() - started);
    if (this.metrics.durations.length > 2000) this.metrics.durations.shift();
    this.canvas.dataset.frames = String(this.metrics.frames);
    this.canvas.dataset.nodes = String(this.metrics.nodes);
    this.canvas.dataset.pulses = String(this.metrics.pulses);
    this.canvas.dataset.merges = String(this.metrics.merges);
    this.canvas.dataset.trails = String(this.metrics.trails);
    this.canvas.dataset.history = String(this.metrics.history);
    this.canvas.dataset.clusters = String(this.metrics.clusters);
    this.canvas.dataset.completing = String(this.completing.size);
    this.canvas.dataset.flares = String(this.flares.length);
    this.canvas.dataset.mode = this.metrics.mode;
    this.canvas.dataset.hover = this.hover?.node.id ?? this.hoverHistory?.history.id ?? "";
    if (
      moving || (!this.reduced.matches && !this.frozen) ||
      (!this.reduced.matches && !this.frozen &&
        (this.pulses.length || this.merging.size || this.completing.size || this.growing.size || this.flares.length))
    )
      this.schedule();
  };
}
