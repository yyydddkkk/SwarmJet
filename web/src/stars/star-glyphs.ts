/** Shapes from the current stars design. The same definitions serve the legend and canvas. */
export const GLY = {
  star: '<circle r="4.5" fill="currentColor"/>',
  busy: '<circle r="3" fill="currentColor"/><path d="M3 -1.5l7 -3M3 0l8 -1.5M3 1.5l7 0" stroke="currentColor" stroke-width=".8"/>',
  parked: '<circle r="3" fill="none" stroke="currentColor"/><path d="M4.5 -1.5l5 -2" stroke="currentColor" stroke-dasharray="1.5 1.5"/>',
  failed: '<path d="M-3.5 -3.5l7 7M3.5 -3.5l-7 7" stroke="currentColor"/>',
  moon: '<ellipse rx="7" ry="4" fill="none" stroke="currentColor" stroke-dasharray="1 1.5"/><circle cx="7" r="1.8" fill="currentColor"/>',
  pr: '<ellipse rx="9" ry="2.6" transform="rotate(-20)" fill="none" stroke="currentColor"/><circle r="4" fill="var(--paper)" stroke="currentColor"/>',
  signal: '<path d="M-5 0h10M0 -5v10" stroke="currentColor"/>',
  ci: '<path d="M0 -5l5 5-5 5-5-5z" fill="none" stroke="currentColor"/>',
  queue: '<circle r="3" fill="none" stroke="currentColor"/>',
} as const;

export const LGLY = {
  ...GLY,
  hist: '<circle r="1.6" fill="currentColor"/><path d="M-5 0h10M0 -5v10" stroke="currentColor" stroke-width=".5"/>',
  cluster: '<circle r="2.6" cx="-3.5" fill="none" stroke="currentColor"/><circle r="2.6" cx="3" cy="-1.5" fill="none" stroke="currentColor"/><circle r="2.6" cy="3" fill="none" stroke="currentColor"/>',
  ready: GLY.pr + '<circle r="9" fill="none" stroke="var(--accent)" stroke-dasharray="2 2"/>',
  you: '<circle r="7" fill="none" stroke="currentColor"/><circle r="1.6" fill="currentColor"/>',
} as const;

export type StarGlyph = keyof typeof LGLY;
export type CanvasGlyphOptions = { moonAngle?: number; moonOrbitX?: number; moonOrbitY?: number; moonRadius?: number; accent?: string };

/** The common circle/cross geometry used by the legend and dense history field. */
export function drawCanvasStar(ctx: CanvasRenderingContext2D, x: number, y: number, radius: number, cross = false) {
  ctx.beginPath(); ctx.arc(x, y, radius, 0, Math.PI * 2); ctx.fill();
  if (cross) {
    const arm = radius * (5 / 1.6);
    ctx.beginPath(); ctx.moveTo(x - arm, y); ctx.lineTo(x + arm, y);
    ctx.moveTo(x, y - arm); ctx.lineTo(x, y + arm); ctx.stroke();
  }
}

/** Agent silhouettes share the GLY coordinates, with radius and direction for the live map. */
export function drawCanvasAgent(ctx: CanvasRenderingContext2D, kind: "busy" | "parked" | "failed", x: number, y: number, radius: number, angle = 0) {
  const scale = radius / 3, cosine = Math.cos(angle), sine = Math.sin(angle);
  const p = (px: number, py: number) => ({ x: x + (px * cosine - py * sine) * scale, y: y + (px * sine + py * cosine) * scale });
  const segment = (ax: number, ay: number, bx: number, by: number) => {
    const a = p(ax, ay), b = p(bx, by); ctx.moveTo(a.x, a.y); ctx.lineTo(b.x, b.y);
  };
  if (kind === "busy") {
    drawCanvasStar(ctx, x, y, radius);
    ctx.beginPath(); segment(3, -1.5, 10, -4.5); segment(3, 0, 11, -1.5); segment(3, 1.5, 10, 1.5); ctx.stroke();
  } else if (kind === "parked") {
    ctx.beginPath(); ctx.arc(x, y, radius, 0, Math.PI * 2); ctx.stroke();
    ctx.save(); ctx.setLineDash([1.5, 1.5]); ctx.beginPath(); segment(4.5, -1.5, 9.5, -3.5); ctx.stroke(); ctx.restore();
  } else {
    ctx.beginPath(); segment(-3.5, -3.5, 3.5, 3.5); segment(3.5, -3.5, -3.5, 3.5); ctx.stroke();
  }
}

/** Draw the small canonical silhouette at the current canvas origin. */
export function drawCanvasGlyph(ctx: CanvasRenderingContext2D, kind: StarGlyph, options: CanvasGlyphOptions = {}) {
  const circle = (x: number, y: number, r: number, fill = false) => {
    ctx.beginPath(); ctx.arc(x, y, r, 0, Math.PI * 2); fill ? ctx.fill() : ctx.stroke();
  };
  const path = (...points: number[]) => {
    ctx.beginPath(); ctx.moveTo(points[0], points[1]);
    for (let i = 2; i < points.length; i += 2) ctx.lineTo(points[i], points[i + 1]);
    ctx.stroke();
  };
  switch (kind) {
    case "star": drawCanvasStar(ctx, 0, 0, 4.5); break;
    case "busy": drawCanvasAgent(ctx, "busy", 0, 0, 3); break;
    case "parked": drawCanvasAgent(ctx, "parked", 0, 0, 3); break;
    case "failed": drawCanvasAgent(ctx, "failed", 0, 0, 3); break;
    case "signal": path(-5, 0, 5, 0); path(0, -5, 0, 5); break;
    case "ci": path(0, -5, 5, 0, 0, 5, -5, 0, 0, -5); break;
    case "queue": circle(0, 0, 3); break;
    case "hist": drawCanvasStar(ctx, 0, 0, 1.6, true); break;
    case "cluster": circle(-3.5, 0, 2.6); circle(3, -1.5, 2.6); circle(0, 3, 2.6); break;
    case "you": circle(0, 0, 7); circle(0, 0, 1.6, true); break;
    case "moon":
      ctx.save(); ctx.setLineDash([1, 1.5]); ctx.beginPath(); ctx.ellipse(0, 0, options.moonOrbitX ?? 7, options.moonOrbitY ?? 4, 0, 0, Math.PI * 2); ctx.stroke(); ctx.restore();
      circle(Math.cos(options.moonAngle ?? 0) * (options.moonOrbitX ?? 7), Math.sin(options.moonAngle ?? 0) * (options.moonOrbitY ?? 4), options.moonRadius ?? 1.8, true); break;
    case "pr": case "ready":
      ctx.beginPath(); ctx.ellipse(0, 0, 9, 2.6, -Math.PI / 9, 0, Math.PI * 2); ctx.stroke();
      circle(0, 0, 4, true); circle(0, 0, 4);
      if (kind === "ready") { ctx.save(); ctx.strokeStyle = options.accent ?? ctx.strokeStyle; ctx.setLineDash([2, 2]); circle(0, 0, 9); ctx.restore(); }
      break;
  }
}
