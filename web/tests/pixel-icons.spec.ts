// The design spec is the single source of truth for every pixel drawing, so it
// is checked at the data level: every grid must be a 16x16 character grid that
// only uses characters the legend declares, and the generated module must carry
// the spec through unchanged.
//
// The hashing and state rules live next to the renderer, so they are checked
// here as plain functions rather than through the DOM.
import { expect, test } from "@playwright/test";
import { existsSync, readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  FORAGER_BODY_COLORS,
  FORAGER_GRIDS,
  ICON_GRIDS,
  ICON_NAMES,
  PALETTE,
  QUEEN_GRID,
  QUEEN_OFF,
  STATE_OVERRIDES,
} from "../src/hive/pixel-icons";
import { fnv1a, foragerBodyColor, foragerColors } from "../src/hive/pixel";
import type { AgentState } from "../src/hive/types";

/** Walks up from the test file until it finds the spec, so cwd does not matter. */
function findSpec(): string {
  const starts = [dirname(fileURLToPath(import.meta.url)), process.cwd()];
  for (const start of starts) {
    let dir = start;
    for (let step = 0; step < 6; step += 1) {
      for (const candidate of [
        resolve(dir, "docs/claude/design/pixel-icons.json"),
        resolve(dir, "../docs/claude/design/pixel-icons.json"),
      ]) {
        if (existsSync(candidate)) return candidate;
      }
      dir = resolve(dir, "..");
    }
  }
  throw new Error("docs/claude/design/pixel-icons.json not found");
}

const spec = JSON.parse(readFileSync(findSpec(), "utf8")) as {
  grid: number;
  legend: Record<string, string>;
  palette: { light: Record<string, string>; dark: Record<string, string> };
  stateOverrides: Record<string, Record<string, string>>;
  queenOff: Record<string, string>;
  foragerBodyColors: string[];
  forager: Record<string, string[]>;
  queen: string[];
  icons: Record<string, string[]>;
};

const FORAGER_STATES: AgentState[] = [
  "creating",
  "active",
  "parked",
  "hibernated",
  "completed",
  "failed",
  "cancelled",
];

test("every grid in the spec is 16 rows by 16 columns and uses legend characters only", () => {
  expect(spec.grid).toBe(16);
  const legend = new Set(Object.keys(spec.legend));

  const grids: [string, string[]][] = [
    ...Object.entries(spec.forager).map(([name, rows]): [string, string[]] => [`forager.${name}`, rows]),
    ["queen", spec.queen],
    ...Object.entries(spec.icons).map(([name, rows]): [string, string[]] => [`icons.${name}`, rows]),
  ];

  expect(grids).toHaveLength(Object.keys(spec.forager).length + 1 + Object.keys(spec.icons).length);
  expect(Object.keys(spec.forager)).toHaveLength(8);
  expect(Object.keys(spec.icons)).toHaveLength(29);

  // Collect instead of one assertion per character: 40 grids x 256 cells is a lot
  // of assertions, and the failure message is more useful as a list anyway.
  const problems: string[] = [];
  for (const [name, rows] of grids) {
    if (rows.length !== spec.grid) problems.push(`${name}: ${rows.length} rows, expected ${spec.grid}`);
    rows.forEach((row, index) => {
      if (row.length !== spec.grid) problems.push(`${name} row ${index}: ${row.length} columns`);
      for (const char of row) {
        if (!legend.has(char)) problems.push(`${name} row ${index}: unknown character "${char}"`);
      }
    });
  }
  expect(problems).toEqual([]);
});

test("the generated module is the spec, unchanged", () => {
  expect(Object.keys(FORAGER_GRIDS).sort()).toEqual(Object.keys(spec.forager).sort());
  for (const [name, rows] of Object.entries(spec.forager)) {
    expect(FORAGER_GRIDS[name as keyof typeof FORAGER_GRIDS], `forager.${name}`).toEqual(rows);
  }
  expect(QUEEN_GRID).toEqual(spec.queen);

  expect(Object.keys(ICON_GRIDS).sort()).toEqual(Object.keys(spec.icons).sort());
  for (const [name, rows] of Object.entries(spec.icons)) {
    expect(ICON_GRIDS[name as keyof typeof ICON_GRIDS], `icons.${name}`).toEqual(rows);
  }
  expect([...ICON_NAMES].sort()).toEqual(Object.keys(spec.icons).sort());

  expect([...FORAGER_BODY_COLORS]).toEqual(spec.foragerBodyColors);
  expect(PALETTE.light).toEqual(spec.palette.light);
  expect(PALETTE.dark).toEqual(spec.palette.dark);
  expect(QUEEN_OFF).toEqual(spec.queenOff);
  expect(STATE_OVERRIDES).toEqual(spec.stateOverrides);
});

test("a Forager id always hashes to the same body colour, and the ids spread over the palette", () => {
  expect(FORAGER_BODY_COLORS).toHaveLength(6);

  // Stable across calls, and a pure function of the id.
  for (const id of ["forager-7f3a", "forager-2b91", "queen-swarmjet", ""]) {
    expect(foragerBodyColor(id)).toBe(foragerBodyColor(id));
    expect(fnv1a(id)).toBe(fnv1a(id));
    expect(FORAGER_BODY_COLORS).toContain(foragerBodyColor(id));
  }

  // Different ids are free to collide, but 24 of them must cover most of the palette.
  const ids = Array.from({ length: 24 }, (_, index) => `forager-sample-${index}`);
  const used = new Set(ids.map(foragerBodyColor));
  expect(used.size).toBeGreaterThanOrEqual(5);
});

test("the state decides the expression, and the colour overrides follow the spec", () => {
  // Each state paints a different grid: same silhouette, different cells.
  const signatures = new Set(Object.values(FORAGER_GRIDS).map(rows => rows.join("\n")));
  expect(signatures.size).toBe(Object.keys(FORAGER_GRIDS).length);

  // The body colour is the hash colour unless the state overrides it.
  expect(foragerColors("forager-7f3a", "active")).toEqual({ y: foragerBodyColor("forager-7f3a") });

  const cancelled = foragerColors("forager-7f3a", "cancelled");
  expect(cancelled).toMatchObject(STATE_OVERRIDES.cancelled);
  expect(cancelled.y).toBe(spec.stateOverrides.cancelled.y);

  const creating = foragerColors("forager-7f3a", "creating");
  expect(creating.y).toBe(spec.stateOverrides.creating.y);

  // Every AgentState the console can show has a drawing.
  for (const state of FORAGER_STATES) {
    const sprite = state === "cancelled" ? "cancelled" : state;
    expect(FORAGER_GRIDS[sprite as keyof typeof FORAGER_GRIDS], `missing sprite for ${state}`).toBeTruthy();
  }
});
