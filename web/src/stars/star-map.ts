import type {
  HiveOverview,
  ProjectState,
  ProjectStore,
  StarNode,
  LightPulse,
  PulseEndpoint,
} from "../data";
import type { StarsUI, ObjectSelection } from "./ui";
import { themeStore } from "./theme";
import { timelineFor } from "./timeline-state";
type Point = { x: number; y: number };
type Body = Point & {
  node: StarNode;
  angle: number;
  radius: number;
  annotation: string;
  signal?: { kind: string; claimed: boolean };
};
type System = Point & {
  id: string;
  name: string;
  on: boolean;
  state: ProjectState;
  bodies: Body[];
  decisionIds: Set<string>;
  needsAttention: boolean;
};
type Camera = Point & { scale: number };
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
export type MapMetrics = {
  frames: number;
  durations: number[];
  pulses: number;
  merges: number;
  nodes: number;
  mode: string;
  positions: Record<string, Point>;
  trails: number;
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
  private merging = new Map<string, { start: number; body: Body }>();
  private previous = new Map<string, string>();
  private trails = new Map<string, Body>();
  private hover?: Body;
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
  private grain?: CanvasPattern | null;
  private lastView = "";
  constructor(
    private canvas: HTMLCanvasElement,
    private projects: HiveOverview[],
    private stores: ReadonlyMap<string, ProjectStore>,
    private ui: StarsUI,
    private assign: (id: string, number: number) => Promise<unknown>,
    private feedback: (message: string) => void,
  ) {
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
    this.cleanups.push(
      ...[...stores.values()].map((store) =>
        store.subscribe(() => this.sync()),
      ),
    );
    this.cleanups.push(
      ui.subscribe(() => {
        if (ui.getSnapshot().overlay === "timeline") {
          if (!this.frozen) {
            this.frozen = this.systems;
            this.frozenTrails = new Map(this.trails);
          }
        } else {
          this.frozen = undefined;
          this.merging.clear();
        }
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
    this.cleanups.push(timelineFor(ui).subscribe(() => this.schedule()));
    const motion = () => {
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
    on("pointerleave", () => {
      if (!this.dragging) {
        this.hover = undefined;
        this.schedule();
      }
    });
    on("dblclick", () => {
      clearTimeout(this.createTimer);
      this.ui.selectProject();
      this.updateCamera(true);
      this.schedule();
    });
    on("wheel", (e) => this.wheel(e), { passive: false });
    this.sync();
  }
  destroy() {
    this.destroyed = true;
    clearTimeout(this.createTimer);
    if (this.frame !== undefined) cancelAnimationFrame(this.frame);
    this.cleanups.forEach((fn) => fn());
  }
  private sync() {
    const now = performance.now();
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
      };
      for (const node of state.derived?.nodes ?? []) {
        const angle = hash(node.id) * TAU;
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
        const annotation =
          node.kind === "comet"
            ? `${agent?.issueNumber ? `#${agent.issueNumber}` : (agent?.roleName ?? "Agent")}  ${node.label}`
            : node.kind === "signal"
              ? signal?.kind === "issue"
                ? `Issue #${signal.number}`
                : node.label
              : node.kind === "satellite"
                ? `verifier ${node.label}`
                : node.kind === "queue"
                  ? "排队中"
                  : node.label;
        const body: Body = {
          ...ellipse(system, angle, radius),
          node,
          angle,
          radius,
          annotation,
          signal: signal && { kind: signal.kind, claimed: signal.claimed },
        };
        system.bodies.push(body);
        const old = this.previous.get(node.id);
        if (old && old !== "merged" && node.state === "merged") {
          this.metrics.merges++;
          if (!this.reduced.matches)
            this.merging.set(node.id, { start: now, body });
        }
        if (node.kind === "comet" && node.state === "completed")
          this.trails.set(node.id, body);
        this.previous.set(node.id, node.state ?? "");
      }
      for (const b of system.bodies.filter(
        (b) => b.node.kind === "satellite",
      )) {
        const parent = system.bodies.find((p) => p.node.id === b.node.parentId);
        if (parent) {
          b.x = parent.x + Math.cos(b.angle) * 18;
          b.y = parent.y + Math.sin(b.angle) * 11;
        }
      }
      return system;
    });
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
            0.24,
            Math.min((this.width - 120) / 850, (this.height - 260) / 440, 1.8),
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
      .filter((b) => b.node.state !== "merged")
      .map((b) => ({
        b,
        d: Math.hypot(this.screen(b).x - p.x, this.screen(b).y - p.y),
      }))
      .filter((v) => v.d < 17)
      .sort((a, b) => a.d - b.d)[0]?.b;
  }
  private selection(body: Body): ObjectSelection | undefined {
    const n = body.node;
    if (n.kind === "queue") return;
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
    } else this.hover = this.hit(p);
    this.canvas.style.cursor = d?.moved
      ? d.body
        ? "grabbing"
        : "move"
      : this.hover
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
      } else if (body) {
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
    if (!this.destroyed && this.frame === undefined)
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
      this.camera.x += (this.target.x - this.camera.x) * 0.2;
      this.camera.y += (this.target.y - this.camera.y) * 0.2;
      this.camera.scale += (this.target.scale - this.camera.scale) * 0.2;
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
    for (let i = 0; i < 380; i++) {
      const x = hash(`star-x${i}`) * this.width,
        y = hash(`star-y${i}`) * this.height,
        r = hash(`star-r${i}`);
      if (simple && r < 0.8) continue;
      ctx.globalAlpha = R.starA * (0.2 + r * 0.5);
      circle({ x, y }, 0.3 + r * 0.8, true);
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
        const node = body.node,
          merge = this.frozen ? undefined : this.merging.get(node.id);
        let world: Point = body;
        if (node.state === "merged" && !merge) continue;
        if (merge) {
          const t = Math.min(1, (now - merge.start) / 1400);
          world = ellipse(
            system,
            body.angle + t * TAU * 2,
            body.radius * (1 - t) ** 2,
          );
          if (t >= 1) this.merging.delete(node.id);
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
        ctx.globalAlpha = dim * (node.state === "completed" ? 0.32 : 1);
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
            circle(q, sr, true);
            for (const k of [1.8, 2.6, 3.6]) {
              ctx.globalAlpha = (dim * 0.55) / k;
              circle(q, sr * k);
            }
            ctx.globalAlpha = dim * 0.5;
            for (let k = 0; k < 24; k++) {
              const a = (k * TAU) / 24;
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
          const dx = body.x - system.x,
            dy = body.y - system.y,
            len = Math.hypot(dx, dy) || 1,
            ux = dx / len,
            uy = dy / len;
          if (!view.hiveId) {
            circle(q, r, true);
          } else if (node.state === "failed") {
            line({ x: q.x - r, y: q.y - r }, { x: q.x + r, y: q.y + r });
            line({ x: q.x + r, y: q.y - r }, { x: q.x - r, y: q.y + r });
            ctx.setLineDash([2, 5]);
            line(
              { x: q.x + ux * 6, y: q.y + uy * 6 },
              { x: q.x + ux * 34, y: q.y + uy * 34 },
            );
            ctx.setLineDash([]);
          } else if (
            ["parked", "hibernated", "cancelled"].includes(node.state ?? "")
          ) {
            circle(q, r);
            ctx.setLineDash([1.5, 2.5]);
            line(
              { x: q.x + ux * (r + 2), y: q.y + uy * (r + 2) },
              { x: q.x + ux * (r + 12), y: q.y + uy * (r + 12) },
            );
            if (node.label.includes("仍占着名额")) circle(q, r + 5);
            ctx.setLineDash([]);
          } else {
            const length =
              (node.state === "completed" ? 16 : 38) *
              Math.min(Math.max(s, 0.5), 1.8);
            for (let k = 0; k < 7; k++) {
              const a = (k - 3) * 0.055,
                vx = ux * Math.cos(a) - uy * Math.sin(a),
                vy = ux * Math.sin(a) + uy * Math.cos(a);
              ctx.globalAlpha = dim * (0.55 - Math.abs(k - 3) * 0.11);
              line(q, {
                x: q.x + vx * length * (1 - Math.abs(k - 3) * 0.13),
                y: q.y + vy * length * (1 - Math.abs(k - 3) * 0.13),
              });
            }
            ctx.globalAlpha = dim;
            circle(q, r, true);
            ctx.globalAlpha = dim * 0.35;
            circle(q, r + 3.5);
          }
        } else if (node.kind === "planet") {
          const pr = Math.max(3, 7 * Math.min(s, 1.5)) * R.sz;
          ctx.beginPath();
          ctx.ellipse(q.x, q.y, pr * 2.15, pr * 0.62, -0.38, Math.PI, TAU);
          ctx.stroke();
          ctx.fillStyle = merge ? C.ink : C.paper;
          circle(q, pr, true);
          circle(q, pr);
          ctx.beginPath();
          ctx.ellipse(q.x, q.y, pr * 2.15, pr * 0.62, -0.38, 0, Math.PI);
          ctx.stroke();
          if (system.decisionIds.has(node.id)) {
            ctx.strokeStyle = C.accent;
            ctx.setLineDash([2, 3]);
            circle(q, pr + 7);
            ctx.setLineDash([]);
          }
        } else if (node.kind === "satellite") {
          const parent = system.bodies.find((b) => b.node.id === node.parentId);
          if (parent && view.hiveId) {
            const pp = this.screen(parent);
            ctx.strokeStyle = C.ink3;
            ctx.setLineDash([1, 2]);
            ctx.beginPath();
            ctx.ellipse(pp.x, pp.y, 18 * s, 11 * s, 0, 0, TAU);
            ctx.stroke();
            ctx.setLineDash([]);
          }
          ctx.fillStyle = C.ink;
          circle(q, Math.max(1.5, 2.5 * s), true);
        } else if (node.kind === "signal") {
          const signal = body.signal,
            k = Math.max(2.8, 5 * Math.min(s, 1.4));
          ctx.strokeStyle = signal?.claimed ? C.ink2 : C.ink;
          if (signal?.kind !== "issue") {
            ctx.beginPath();
            ctx.moveTo(q.x, q.y - k);
            ctx.lineTo(q.x + k, q.y);
            ctx.lineTo(q.x, q.y + k);
            ctx.lineTo(q.x - k, q.y);
            ctx.closePath();
            ctx.stroke();
          } else {
            line({ x: q.x - k, y: q.y }, { x: q.x + k, y: q.y });
            line({ x: q.x, y: q.y - k }, { x: q.x, y: q.y + k });
            if (!signal.claimed) {
              ctx.globalAlpha = 0.45;
              circle(q, k + 6);
            }
          }
        } else circle(q, Math.max(1.8, 3 * s));
        if (view.hiveId && node.kind !== "star" && node.state !== "completed") {
          ctx.globalAlpha = dim;
          const comet = node.kind === "comet",
            satellite = node.kind === "satellite",
            right = body.x > system.x;
          label(
            body.annotation,
            {
              x: q.x + (comet ? (right ? -15 : 15) : satellite ? -12 : 0),
              y: q.y + (comet ? 4 : satellite ? -22 : 22),
            },
            comet ? 13 : 12,
            !comet,
            comet ? C.ink2 : C.ink3,
            comet ? (right ? "right" : "left") : satellite ? "right" : "center",
          );
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
        if (system.needsAttention) {
          ctx.fillStyle = C.accent;
          circle(
            {
              x: p.x + ctx.measureText(system.name).width / 2 + 10,
              y: p.y + 300 * AS * s + 25,
            },
            2.5,
            true,
          );
        }
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
    this.metrics.frames++;
    this.metrics.durations.push(performance.now() - started);
    if (this.metrics.durations.length > 2000) this.metrics.durations.shift();
    this.canvas.dataset.frames = String(this.metrics.frames);
    this.canvas.dataset.nodes = String(this.metrics.nodes);
    this.canvas.dataset.pulses = String(this.metrics.pulses);
    this.canvas.dataset.merges = String(this.metrics.merges);
    this.canvas.dataset.trails = String(this.metrics.trails);
    this.canvas.dataset.mode = this.metrics.mode;
    if (moving || (!this.frozen && (this.pulses.length || this.merging.size)))
      this.schedule();
  };
}
