// The pixel rendering convention for the Hive console (ADR section 18).
//
// Rules every pixel drawing in this folder follows:
//   1. inline SVG only, one <rect> per pixel — adjacent pixels of the same
//      colour are merged into a single run inside one <path>, so a whole sprite
//      is a handful of paths;
//   2. the root <svg> carries shape-rendering="crispEdges" and an integer-grid
//      viewBox, and the component is only ever drawn at an integer multiple of
//      the grid (16x16 -> 16/32/48, 32x32 -> 32/64/96). Anything else warns in
//      development instead of quietly going blurry;
//   3. colours come from the --hg-px-* tokens in hive-console.css, so dark mode
//      is a token swap and the artwork itself stays colour-free;
//   4. no pixel font: prose, code, diffs, logs and tables keep the normal fonts.
import { useMemo, type CSSProperties, type ReactNode } from "react";
import {
  BEE_16,
  BEE_16_THREE,
  BEE_32,
  BEE_32_FLAP,
  BEE_32_THREE,
  COMB_32,
  QUEEN_16,
  QUEEN_32,
  type PixelSprite,
} from "./pixel-art";

export type PixelToken =
  | "ink"
  | "body"
  | "shade"
  | "wing"
  | "wingHi"
  | "wingEdge"
  | "blush"
  | "gold"
  | "goldShade"
  | "shine";

/** One ASCII character per palette role. See pixel-art.ts for the legend. */
const GLYPH: Record<string, PixelToken> = {
  "#": "ink",
  y: "body",
  d: "shade",
  w: "wing",
  W: "wingHi",
  b: "wingEdge",
  p: "blush",
  g: "gold",
  G: "goldShade",
  i: "shine",
};

const TOKEN_VAR: Record<PixelToken, string> = {
  ink: "--hg-px-ink",
  body: "--hg-px-body",
  shade: "--hg-px-shade",
  wing: "--hg-px-wing",
  wingHi: "--hg-px-wing-hi",
  wingEdge: "--hg-px-wing-edge",
  blush: "--hg-px-blush",
  gold: "--hg-px-gold",
  goldShade: "--hg-px-gold-shade",
  shine: "--hg-px-shine",
};

export type Runner = { token: PixelToken; d: string };

/**
 * Turns a pixel grid into one merged path per palette role. Horizontal runs of
 * the same colour collapse into a single `M x y h n v1 h -n z` block, so a
 * 32x32 sprite costs a few hundred bytes instead of a thousand <rect> nodes.
 */
export function pixelRuns(rows: string[]): Runner[] {
  const runs = new Map<PixelToken, string[]>();
  rows.forEach((row, y) => {
    let x = 0;
    while (x < row.length) {
      const glyph = row[x];
      if (glyph === "." || !GLYPH[glyph]) {
        x += 1;
        continue;
      }
      let end = x;
      while (end + 1 < row.length && row[end + 1] === glyph) end += 1;
      const token = GLYPH[glyph];
      const list = runs.get(token) ?? [];
      list.push(`M${x} ${y}h${end - x + 1}v1h-${end - x + 1}z`);
      runs.set(token, list);
      x = end + 1;
    }
  });
  return [...runs].map(([token, d]) => ({ token, d: d.join("") }));
}

const warned = new Set<string>();

/** Integer-multiple guard: pixel art only lines up on whole grid steps. */
export function assertGridSize(label: string, grid: number, size: number): void {
  if (!import.meta.env?.DEV) return;
  if (size > 0 && size % grid === 0) return;
  const key = `${label}@${size}`;
  if (warned.has(key)) return;
  warned.add(key);
  console.warn(
    `[pixel] ${label} is drawn on a ${grid}x${grid} grid and only stays crisp at a multiple of ` +
      `${grid}px (16x16 -> 16/32/48, 32x32 -> 32/64/96). Received ${size}px.`,
  );
}

export function PixelSvg({
  sprite,
  size,
  className,
  title,
  monochrome = false,
  style,
}: {
  sprite: PixelSprite;
  size: number;
  className?: string;
  title?: string;
  /** Icons are single-colour and follow currentColor; sprites use the tokens. */
  monochrome?: boolean;
  style?: CSSProperties;
}) {
  const { grid, rows } = sprite;
  const runs = useMemo(() => pixelRuns(rows), [rows]);
  return (
    <svg
      className={className}
      width={size}
      height={size}
      viewBox={`0 0 ${grid} ${grid}`}
      shapeRendering="crispEdges"
      style={style}
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
      focusable="false"
    >
      {runs.map(run => (
        <path
          key={run.token}
          fill={monochrome ? "currentColor" : `var(${TOKEN_VAR[run.token]})`}
          d={run.d}
        />
      ))}
    </svg>
  );
}

export type PixelIconName = "bee-16" | "bee-32" | "queen-16" | "queen-32" | "comb-32";

const SPRITES: Record<PixelIconName, PixelSprite> = {
  "bee-16": BEE_16,
  "bee-32": BEE_32,
  "queen-16": QUEEN_16,
  "queen-32": QUEEN_32,
  "comb-32": COMB_32,
};

export function PixelIcon({
  name,
  size,
  className,
  title,
}: {
  name: PixelIconName;
  size: number;
  className?: string;
  title?: string;
}) {
  const sprite = SPRITES[name];
  assertGridSize(name, sprite.grid, size);
  return (
    <PixelSvg
      sprite={sprite}
      size={size}
      className={className ? `hg-px ${className}` : "hg-px"}
      title={title}
    />
  );
}

/* -------------------------------------------------------------------------- */
/* Forager avatars                                                            */
/* -------------------------------------------------------------------------- */

// Six coordinated warm bodies; all keep plenty of contrast against the near-black
// stripes, so the stripes and the eyes stay readable whichever one is picked.
export const FORAGER_BODIES = [
  "#f2b53a",
  "#f5c46b",
  "#efa95b",
  "#ef9450",
  "#e8c05a",
  "#e2a86a",
] as const;

export type Accessory = "none" | "glasses" | "hat" | "bowtie" | "headphones";
export type Facing = "left" | "right";

export type ForagerLook = {
  body: string;
  stripes: 2 | 3;
  accessory: Accessory;
  facing: Facing;
};

/**
 * FNV-1a over the Forager id: stable across sessions and machines, so a Forager
 * always comes back as the same bee. 32 bits is plenty for the ~120 looks below.
 */
export function fnv1a(input: string): number {
  let hash = 0x811c9dc5;
  for (let index = 0; index < input.length; index += 1) {
    hash ^= input.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return hash >>> 0;
}

const ACCESSORIES: Accessory[] = ["none", "glasses", "hat", "bowtie", "headphones"];

export function foragerLook(id: string): ForagerLook {
  const hash = fnv1a(id);
  return {
    body: FORAGER_BODIES[hash % FORAGER_BODIES.length],
    stripes: ((hash >>> 4) & 1) === 0 ? 2 : 3,
    accessory: ACCESSORIES[(hash >>> 8) % ACCESSORIES.length],
    facing: ((hash >>> 13) & 1) === 0 ? "left" : "right",
  };
}

/** [x, y, token] — every accessory is 2-6 pixels, as the spec requires. */
type PixelPatch = [number, number, PixelToken];

const ACCESSORY_PATCHES: Record<16 | 32, Record<Exclude<Accessory, "none">, PixelPatch[]>> = {
  32: {
    // a bridge across the eyes plus a rim either side
    glasses: [
      [3, 19, "ink"],
      [6, 19, "ink"],
      [7, 19, "ink"],
      [8, 19, "ink"],
      [11, 19, "ink"],
    ],
    // a little domed cap sitting straight on the head
    hat: [
      [8, 7, "wingEdge"],
      [9, 7, "wingEdge"],
      [7, 8, "wingEdge"],
      [8, 8, "wingEdge"],
      [9, 8, "wingEdge"],
      [10, 8, "wingEdge"],
    ],
    // a little bow under the chin
    bowtie: [
      [2, 25, "blush"],
      [3, 25, "blush"],
      [2, 26, "blush"],
      [3, 26, "blush"],
    ],
    // a band arching over the head, hollow underneath
    headphones: [
      [7, 7, "wingEdge"],
      [8, 7, "wingEdge"],
      [9, 7, "wingEdge"],
      [10, 7, "wingEdge"],
      [6, 8, "wingEdge"],
      [11, 8, "wingEdge"],
    ],
  },
  16: {
    glasses: [
      [2, 9, "ink"],
      [2, 10, "ink"],
      [4, 9, "ink"],
      [4, 10, "ink"],
    ],
    hat: [
      [6, 3, "wingEdge"],
      [5, 4, "wingEdge"],
      [6, 4, "wingEdge"],
      [7, 4, "wingEdge"],
    ],
    bowtie: [
      [2, 13, "blush"],
      [3, 13, "blush"],
    ],
    headphones: [
      [5, 3, "wingEdge"],
      [6, 3, "wingEdge"],
      [7, 3, "wingEdge"],
      [5, 4, "wingEdge"],
      [8, 4, "wingEdge"],
    ],
  },
};

const GLYPH_FOR_TOKEN: Record<PixelToken, string> = {
  ink: "#",
  body: "y",
  shade: "d",
  wing: "w",
  wingHi: "W",
  wingEdge: "b",
  blush: "p",
  gold: "g",
  goldShade: "G",
  shine: "i",
};

function patch(rows: string[], patches: PixelPatch[]): string[] {
  const next = [...rows];
  for (const [x, y, token] of patches) {
    const row = next[y];
    if (row === undefined || row[x] === undefined) continue;
    next[y] = row.slice(0, x) + GLYPH_FOR_TOKEN[token] + row.slice(x + 1);
  }
  return next;
}

function mirror(rows: string[]): string[] {
  return rows.map(row => [...row].reverse().join(""));
}

/** The base bee for a grid size and stripe count. */
export function beeSprite(grid: 16 | 32, stripes: 2 | 3): PixelSprite {
  if (grid === 32) return stripes === 3 ? BEE_32_THREE : BEE_32;
  return stripes === 3 ? BEE_16_THREE : BEE_16;
}

/**
 * 16-grid below 32px, 32-grid from 32px up: both stay whole-number scales, and
 * the small bee drops the mouth and blush so it still reads at 16px.
 */
export function resolveAvatar(id: string, size: number): { sprite: PixelSprite; look: ForagerLook } {
  const look = foragerLook(id);
  const grid: 16 | 32 = size >= 32 ? 32 : 16;
  assertGridSize(`ForagerAvatar(${id})`, grid, size);
  const base = beeSprite(grid, look.stripes);
  let rows = base.rows;
  if (look.accessory !== "none") rows = patch(rows, ACCESSORY_PATCHES[grid][look.accessory]);
  if (look.facing === "right") rows = mirror(rows);
  return { sprite: { grid, rows }, look };
}

export function ForagerAvatar({
  id,
  size = 32,
  className,
  label,
}: {
  id: string;
  size?: number;
  className?: string;
  label?: string;
}) {
  const { sprite, look } = useMemo(() => resolveAvatar(id, size), [id, size]);
  return (
    <span
      className={className ? `hg-px-avatar ${className}` : "hg-px-avatar"}
      style={{ width: size, height: size, "--hg-px-body": look.body } as CSSProperties}
      role="img"
      aria-label={label ?? `Forager ${id}`}
    >
      <PixelSvg sprite={sprite} size={size} />
    </span>
  );
}

/** The Queen is a crown, per the round-2 direction. */
export function QueenAvatar({
  size = 32,
  className,
  label = "Queen（蜂王）",
  off = false,
}: {
  size?: number;
  className?: string;
  label?: string;
  off?: boolean;
}) {
  const sprite = size >= 32 ? QUEEN_32 : QUEEN_16;
  assertGridSize("QueenAvatar", sprite.grid, size);
  return (
    <span
      className={`hg-px-avatar hg-px-avatar-queen${off ? " is-off" : ""}${className ? ` ${className}` : ""}`}
      style={{ width: size, height: size }}
      role="img"
      aria-label={label}
    >
      <PixelSvg sprite={sprite} size={size} />
    </span>
  );
}

/** Two wing positions for the loader; CSS flips between them with steps(). */
export function LoaderBee({ frame, size }: { frame: "up" | "down"; size: number }) {
  const sprite = frame === "up" ? BEE_32 : BEE_32_FLAP;
  assertGridSize(`LoaderBee(${frame})`, sprite.grid, size);
  return <PixelSvg sprite={sprite} size={size} />;
}

/** Small helper used by the icon reference page to render a labelled cell. */
export function PixelCell({
  label,
  children,
}: {
  label: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="hg-icon-cell">
      <span className="hg-icon-cell-box">{children}</span>
      <span className="hg-icon-cell-label">{label}</span>
    </div>
  );
}
