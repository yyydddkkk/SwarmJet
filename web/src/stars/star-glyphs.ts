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
    case "star": circle(0, 0, 4.5, true); break;
    case "busy": circle(0, 0, 3, true); path(3, -1.5, 10, -4.5); path(3, 0, 11, -1.5); path(3, 1.5, 10, 1.5); break;
    case "parked": circle(0, 0, 3); ctx.save(); ctx.setLineDash([1.5, 1.5]); path(4.5, -1.5, 9.5, -3.5); ctx.restore(); break;
    case "failed": path(-3.5, -3.5, 3.5, 3.5); path(3.5, -3.5, -3.5, 3.5); break;
    case "signal": path(-5, 0, 5, 0); path(0, -5, 0, 5); break;
    case "ci": path(0, -5, 5, 0, 0, 5, -5, 0, 0, -5); break;
    case "queue": circle(0, 0, 3); break;
    case "hist": circle(0, 0, 1.6, true); path(-5, 0, 5, 0); path(0, -5, 0, 5); break;
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
