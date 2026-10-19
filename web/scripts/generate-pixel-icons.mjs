#!/usr/bin/env node
// Generates web/src/hive/pixel-icons.ts from the design spec.
//
// The spec (docs/claude/design/pixel-icons.json) is the single source of truth:
// grids and colours are copied verbatim, never re-drawn here. This script only
// reshapes the JSON into a typed module and refuses to emit anything that does
// not satisfy the grid rules.
//
//   cd web && node scripts/generate-pixel-icons.mjs
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, "../..");
const specPath = resolve(root, "docs/claude/design/pixel-icons.json");
const outPath = resolve(here, "../src/hive/pixel-icons.ts");

const spec = JSON.parse(readFileSync(specPath, "utf8"));
const legend = spec.legend;
const legendChars = Object.keys(legend);
const gridSize = spec.grid;

if (gridSize !== 16) throw new Error(`expected a 16px grid, got ${gridSize}`);

/** Every grid must be square, gridSize x gridSize, and use legend characters only. */
function validate(label, rows) {
  if (!Array.isArray(rows) || rows.length !== gridSize)
    throw new Error(`${label}: expected ${gridSize} rows, got ${rows?.length}`);
  rows.forEach((row, index) => {
    if (row.length !== gridSize)
      throw new Error(`${label}: row ${index} has ${row.length} columns, expected ${gridSize}`);
    for (const char of row) {
      if (!legendChars.includes(char)) throw new Error(`${label}: unknown character ${JSON.stringify(char)}`);
    }
  });
  return rows;
}

const forager = Object.fromEntries(Object.entries(spec.forager).map(([k, v]) => [k, validate(`forager.${k}`, v)]));
const queen = validate("queen", spec.queen);
const icons = Object.fromEntries(Object.entries(spec.icons).map(([k, v]) => [k, validate(`icons.${k}`, v)]));

const quote = value => JSON.stringify(value);
/** Object keys like "." and "#" must be quoted; plain words stay readable. */
const key = name => (/^[A-Za-z_$][A-Za-z0-9_$]*$/.test(name) ? name : quote(name));
const rowsLiteral = (rows, indent) => rows.map(row => `${indent}${quote(row)},`).join("\n");
const objectLiteral = (entries, indent = "  ") =>
  Object.entries(entries).map(([k, v]) => `${indent}${key(k)}: ${quote(v)},`).join("\n");
const gridObject = (label, grids) =>
  `export const ${label} = {\n` +
  Object.entries(grids)
    .map(([name, rows]) => `  ${key(name)}: [\n${rowsLiteral(rows, "    ")}\n  ],`)
    .join("\n") +
  `\n} as const;\n`;

const iconNames = Object.keys(icons);
const foragerSprites = Object.keys(forager);
const bodyColors = spec.foragerBodyColors;

const source = `// GENERATED FILE — do not edit by hand.
// Source of truth: docs/claude/design/pixel-icons.json
// Regenerate from web/ with: node scripts/generate-pixel-icons.mjs
//
// Every drawing is a ${gridSize}x${gridSize} character grid, one character per pixel.
// Colours are resolved from the --hg-px-* tokens in hive-console.css, so light
// and dark surfaces are a token swap and the artwork stays colour-free — except
// for the state overrides below, which the spec pins to literal values.
import type { IconName } from "./icons";

export const GRID = ${gridSize};

/** How each grid character is meant to be read. */
export const LEGEND = {
${objectLiteral(legend)}
} as const;

export type LegendChar = keyof typeof LEGEND;
/** Every character that actually paints a pixel. */
export type PaintChar = Exclude<LegendChar, ".">;

export const PALETTE = {
  light: {
${objectLiteral(spec.palette.light, "    ")}
  },
  dark: {
${objectLiteral(spec.palette.dark, "    ")}
  },
} as const;

/** State-specific colour replacements, applied on top of the palette. */
export const STATE_OVERRIDES: Record<string, Partial<Record<LegendChar, string>>> = {
${Object.entries(spec.stateOverrides)
  .map(([state, colors]) => `  ${key(state)}: {\n${objectLiteral(colors, "    ")}\n  },`)
  .join("\n")}
};

/** The Queen switched off keeps the silhouette and drops the gold. */
export const QUEEN_OFF: Partial<Record<LegendChar, string>> = {
${objectLiteral(spec.queenOff)}
};

/** Bodies a Forager can be hashed into; replaces "y" only. */
export const FORAGER_BODY_COLORS = [
${bodyColors.map(color => `  ${quote(color)},`).join("\n")}
] as const;

export type ForagerSprite = ${foragerSprites.map(name => quote(name)).join(" | ")};

${gridObject("FORAGER_GRIDS", forager)}
export const QUEEN_GRID = [
${rowsLiteral(queen, "  ")}
] as const;

${gridObject("ICON_GRIDS", icons)}
/** The 29 names the icon set exports, in spec order. */
export const ICON_NAMES = [
${iconNames.map(name => `  ${quote(name)},`).join("\n")}
] as const satisfies readonly IconName[];
`;

writeFileSync(outPath, source);
console.log(
  `pixel-icons.ts: ${foragerSprites.length} forager states, ${iconNames.length} icons, ` +
    `${bodyColors.length} body colours, ${legendChars.length} legend characters`,
);
