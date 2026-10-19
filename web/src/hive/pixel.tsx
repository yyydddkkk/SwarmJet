// Pixel rendering for the Hive console (ADR section 18, "pixel accents").
//
// Rules, all of them enforced by the shape of the data in pixel-icons.ts:
//   - every drawing is a 16x16 character grid; one character paints one 1x1 <rect>
//     inside an SVG with viewBox="0 0 16 16" and shape-rendering="crispEdges";
//   - "." is transparent and "#" is currentColor;
//   - every other character resolves to a --hg-px-* token from hive-console.css,
//     so the dark surface is a token swap and the artwork never carries colours
//     (the state overrides the spec pins to literal values are the only exception);
//   - a sprite is only ever drawn at 16, 32, 48 or 64px. Anything else warns in
//     development instead of quietly going blurry.
import { useMemo, type CSSProperties, type ReactNode } from "react";
import {
  FORAGER_BODY_COLORS,
  FORAGER_GRIDS,
  GRID,
  ICON_GRIDS,
  LEGEND,
  QUEEN_GRID,
  QUEEN_OFF,
  STATE_OVERRIDES,
  type ForagerSprite,
  type LegendChar,
  type PaintChar,
} from "./pixel-icons";
import type { AgentState } from "./types";

/** The only sizes pixel art is allowed to be drawn at (16px grid x 1/2/3/4). */
export const PIXEL_SIZES = [16, 32, 48, 64] as const;
export type PixelSize = (typeof PIXEL_SIZES)[number];

/** Character -> token. "#" follows the text colour, everything else is themed. */
const CHAR_TOKEN: Record<PaintChar, string> = {
  "#": "currentColor",
  y: "var(--hg-px-y)",
  d: "var(--hg-px-d)",
  a: "var(--hg-px-a)",
  w: "var(--hg-px-w)",
  z: "var(--hg-px-z)",
  r: "var(--hg-px-r)",
  g: "var(--hg-px-g)",
  o: "var(--hg-px-o)",
  h: "var(--hg-px-h)",
  m: "var(--hg-px-m)",
};

export type ColorOverrides = Partial<Record<LegendChar, string>>;

const warned = new Set<string>();

/** Integer-scale guard: a 16px grid only lines up on multiples of 16. */
export function assertPixelSize(label: string, size: number): void {
  if (!import.meta.env?.DEV) return;
  if ((PIXEL_SIZES as readonly number[]).includes(size)) return;
  const key = `${label}@${size}`;
  if (warned.has(key)) return;
  warned.add(key);
  console.warn(
    `[pixel] ${label} is drawn on a ${GRID}x${GRID} grid; use ${PIXEL_SIZES.join("/")}px ` +
      `so every grid cell stays a whole pixel. Received ${size}px.`,
  );
}

type Cell = [x: number, y: number];
type CharCells = [PaintChar, Cell[]];

/** Groups painted cells by character, dropping transparent ones. */
export function groupCells(rows: readonly string[]): CharCells[] {
  const groups = new Map<PaintChar, Cell[]>();
  rows.forEach((row, y) => {
    for (let x = 0; x < row.length; x += 1) {
      const char = row[x] as LegendChar;
      if (char === "." || !(char in LEGEND)) continue;
      const paint = char as PaintChar;
      const list = groups.get(paint) ?? [];
      list.push([x, y]);
      groups.set(paint, list);
    }
  });
  return [...groups];
}

/**
 * One <rect> per grid character. Cells are grouped per character so the fill is
 * declared once per colour instead of ~150 times per sprite.
 */
export function PixelArt({
  rows,
  size,
  colors,
  className,
  title,
}: {
  rows: readonly string[];
  size: number;
  colors?: ColorOverrides;
  className?: string;
  title?: string;
}) {
  const groups = useMemo(() => groupCells(rows), [rows]);
  return (
    <svg
      className={className}
      width={size}
      height={size}
      viewBox={`0 0 ${GRID} ${GRID}`}
      shapeRendering="crispEdges"
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
      focusable="false"
    >
      {groups.map(([char, cells]) => (
        <g key={char} fill={colors?.[char] ?? CHAR_TOKEN[char]}>
          {cells.map(([x, y]) => (
            <rect key={`${x}-${y}`} x={x} y={y} width="1" height="1" />
          ))}
        </g>
      ))}
    </svg>
  );
}

/* -------------------------------------------------------------------------- */
/* Foragers                                                                   */
/* -------------------------------------------------------------------------- */

/**
 * FNV-1a over the Forager id: stable across sessions and machines, so a Forager
 * always comes back as the same bee.
 */
export function fnv1a(input: string): number {
  let hash = 0x811c9dc5;
  for (let index = 0; index < input.length; index += 1) {
    hash ^= input.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return hash >>> 0;
}

/** The body colour a Forager id hashes to; replaces "y" only. */
export function foragerBodyColor(id: string): string {
  return FORAGER_BODY_COLORS[fnv1a(id) % FORAGER_BODY_COLORS.length];
}

type StreamEntry = {
  kind: string;
  message?: { from: string; kind: string; state: string };
  action?: {
    kind: string;
    title: string;
    summary: string;
    artifact?: { ref: string };
    details: { label: string; value: string }[];
  };
};

/**
 * A Forager needs you when the hive stream is waiting on a human: a pending
 * question from that Forager, or a pending approval that names it (by id, or by
 * the pull request it owns).
 */
export function foragerNeedsYou(
  agent: { id: string; prNumber?: number },
  entries: readonly StreamEntry[],
): boolean {
  const pr = agent.prNumber ? `#${agent.prNumber}` : "";
  const named = (text: string) => text.includes(agent.id) || (pr.length > 0 && text.includes(pr));
  return entries.some(entry => {
    if (entry.kind === "message" && entry.message) {
      const message = entry.message;
      return (
        message.from === `forager:${agent.id}` &&
        message.kind === "question" &&
        message.state !== "handled"
      );
    }
    if (entry.kind === "action" && entry.action?.kind === "request_approval") {
      const action = entry.action;
      return [
        action.title,
        action.summary,
        action.artifact?.ref ?? "",
        ...action.details.map(detail => `${detail.label} ${detail.value}`),
      ].some(named);
    }
    return false;
  });
}

/** The eight drawings the spec ships for a bee: seven states plus needs-you. */
export const FORAGER_STATES: ForagerSprite[] = [
  "creating",
  "active",
  "parked",
  "needs_you",
  "hibernated",
  "completed",
  "failed",
  "cancelled",
];

/** "disabled" is a Queen-only state in this console; a greyed bee is closest. */
function spriteFor(state: AgentState, needsYou: boolean): ForagerSprite {
  if (needsYou) return "needs_you";
  if (state === "disabled") return "cancelled";
  return state;
}

/**
 * Hash colour first, then the state's overrides, so cancelled greys out and
 * creating stays pale whatever the id hashes to.
 */
export function foragerColors(id: string, state: AgentState): ColorOverrides {
  return {
    y: foragerBodyColor(id),
    ...(STATE_OVERRIDES[state] ?? {}),
  };
}

export function ForagerAvatar({
  id,
  state,
  needsYou = false,
  size = 32,
  className,
  label,
}: {
  id: string;
  state: AgentState;
  needsYou?: boolean;
  size?: PixelSize;
  className?: string;
  label?: string;
}) {
  const sprite = spriteFor(state, needsYou);
  assertPixelSize(`ForagerAvatar(${id})`, size);
  return (
    <span
      className={`hg-px-avatar${className ? ` ${className}` : ""}`}
      style={{ width: size, height: size } as CSSProperties}
      role="img"
      aria-label={label ?? `Forager ${id}`}
    >
      <PixelArt rows={FORAGER_GRIDS[sprite]} size={size} colors={foragerColors(id, state)} />
    </span>
  );
}

/** The Queen is the crown; switching it off keeps the shape and drops the gold. */
export function QueenAvatar({
  size = 32,
  off = false,
  className,
  label = "Queen（蜂王）",
}: {
  size?: PixelSize;
  off?: boolean;
  className?: string;
  label?: string;
}) {
  assertPixelSize("QueenAvatar", size);
  return (
    <span
      className={`hg-px-avatar hg-px-avatar-queen${off ? " is-off" : ""}${className ? ` ${className}` : ""}`}
      style={{ width: size, height: size } as CSSProperties}
      role="img"
      aria-label={label}
    >
      <PixelArt rows={QUEEN_GRID} size={size} colors={off ? QUEEN_OFF : undefined} />
    </span>
  );
}

/* -------------------------------------------------------------------------- */
/* Loader                                                                     */
/* -------------------------------------------------------------------------- */

const WING_ROWS = [3, 4, 5, 6];

/**
 * Second flap frame: the wing pixels of rows 3-6 move up one row, everything
 * else stays put. Derived from forager.active so the loader cannot drift away
 * from the spec.
 */
export function flapRows(rows: readonly string[]): string[] {
  const next = rows.map(row => [...row]);
  const moved: Cell[] = [];
  for (const y of WING_ROWS) {
    for (let x = 0; x < next[y].length; x += 1) {
      if (next[y][x] === "w") {
        moved.push([x, y - 1]);
        next[y][x] = ".";
      }
    }
  }
  for (const [x, y] of moved) next[y][x] = "w";
  return next.map(row => row.join(""));
}

/** The two loader frames: wings lifted, then wings level. */
export const LOADER_FRAMES = {
  up: flapRows(FORAGER_GRIDS.active),
  down: FORAGER_GRIDS.active,
} as const;

/** Loader drawing: the active Forager in two wing positions. */
export function LoaderBee({ frame, size = 32 }: { frame: "up" | "down"; size?: PixelSize }) {
  assertPixelSize(`LoaderBee(${frame})`, size);
  return <PixelArt rows={LOADER_FRAMES[frame]} size={size} colors={{ y: "var(--hg-px-y)" }} />;
}

/* -------------------------------------------------------------------------- */
/* Pieces that combine spec grids                                             */
/* -------------------------------------------------------------------------- */

/** Empty state: the hive icon with a parked Forager tucked beside it. */
export function EmptyHiveArt({ size = 64 }: { size?: PixelSize }) {
  const beeSize: PixelSize = size >= 64 ? 48 : 32;
  return (
    <span className="hg-empty-pixels">
      <PixelArt rows={ICON_GRIDS.hive} size={size} />
      <span className="hg-empty-pixels-bee">
        <PixelArt rows={FORAGER_GRIDS.parked} size={beeSize} colors={{ y: "var(--hg-px-y)" }} />
      </span>
    </span>
  );
}

/** Small helper for the reference page: a labelled pixel cell. */
export function PixelCell({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <div className="hg-px-cell">
      <span className="hg-px-cell-art">{children}</span>
      <span className="hg-px-cell-label">{label}</span>
    </div>
  );
}
