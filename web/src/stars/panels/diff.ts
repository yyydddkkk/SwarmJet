// Line diff for the methodology version comparison. The files are small (the
// contract caps a version at 500 files / 2 MB), but the LCS table is guarded so
// a pasted large file cannot freeze the page.
export type DiffLine = {
  kind: "same" | "add" | "del";
  text: string;
  /** 1-based line number in the old file, when the line exists there. */
  oldLine?: number;
  /** 1-based line number in the new file, when the line exists there. */
  newLine?: number;
};

export type FileDiff = {
  path: string;
  kind: "added" | "removed" | "changed";
  lines: DiffLine[];
  truncated?: boolean;
};

const MAX_DIFF_CELLS = 4_000_000;
const MAX_FILE_LINES = 2_000;

export function diffLines(before: string, after: string): DiffLine[] {
  const oldLines = before === "" ? [] : before.split("\n");
  const newLines = after === "" ? [] : after.split("\n");
  const n = oldLines.length;
  const m = newLines.length;

  // Very large inputs would need a huge table; show a coarse replacement diff.
  if (n * m > MAX_DIFF_CELLS || n > MAX_FILE_LINES || m > MAX_FILE_LINES) {
    return [
      ...oldLines.map((text, index) => ({
        kind: "del" as const,
        text,
        oldLine: index + 1,
      })),
      ...newLines.map((text, index) => ({
        kind: "add" as const,
        text,
        newLine: index + 1,
      })),
    ];
  }

  // lcs[i][j] = length of the longest common subsequence of oldLines[i:] and
  // newLines[j:]. A Uint32Array per row keeps the table compact.
  const lcs: Uint32Array[] = Array.from(
    { length: n + 1 },
    () => new Uint32Array(m + 1),
  );
  for (let i = n - 1; i >= 0; i -= 1) {
    for (let j = m - 1; j >= 0; j -= 1) {
      lcs[i][j] =
        oldLines[i] === newLines[j]
          ? lcs[i + 1][j + 1] + 1
          : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
    }
  }

  const result: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (oldLines[i] === newLines[j]) {
      result.push({
        kind: "same",
        text: oldLines[i],
        oldLine: i + 1,
        newLine: j + 1,
      });
      i += 1;
      j += 1;
    } else if (lcs[i + 1][j] >= lcs[i][j + 1]) {
      result.push({ kind: "del", text: oldLines[i], oldLine: i + 1 });
      i += 1;
    } else {
      result.push({ kind: "add", text: newLines[j], newLine: j + 1 });
      j += 1;
    }
  }
  while (i < n) {
    result.push({ kind: "del", text: oldLines[i], oldLine: i + 1 });
    i += 1;
  }
  while (j < m) {
    result.push({ kind: "add", text: newLines[j], newLine: j + 1 });
    j += 1;
  }
  return result;
}

export type MethodologyFileLike = { path: string; content: string };

export function diffFiles(
  before: readonly MethodologyFileLike[],
  after: readonly MethodologyFileLike[],
): FileDiff[] {
  const oldByPath = new Map(before.map((file) => [file.path, file.content]));
  const newByPath = new Map(after.map((file) => [file.path, file.content]));
  const paths = [...new Set([...oldByPath.keys(), ...newByPath.keys()])].sort();
  const diffs: FileDiff[] = [];

  for (const path of paths) {
    const oldContent = oldByPath.get(path);
    const newContent = newByPath.get(path);
    if (oldContent === newContent) continue;
    if (oldContent === undefined) {
      const lines = diffLines("", newContent ?? "");
      diffs.push({ path, kind: "added", lines, truncated: lines.length > 600 });
    } else if (newContent === undefined) {
      const lines = diffLines(oldContent, "");
      diffs.push({
        path,
        kind: "removed",
        lines,
        truncated: lines.length > 600,
      });
    } else {
      const lines = diffLines(oldContent, newContent);
      diffs.push({
        path,
        kind: "changed",
        lines,
        truncated: lines.length > 600,
      });
    }
  }
  return diffs;
}

export type DiffStats = { files: number; added: number; removed: number };

export function diffStats(diffs: readonly FileDiff[]): DiffStats {
  let added = 0;
  let removed = 0;
  for (const file of diffs) {
    for (const line of file.lines) {
      if (line.kind === "add") added += 1;
      if (line.kind === "del") removed += 1;
    }
  }
  return { files: diffs.length, added, removed };
}

/**
 * Trim a unified diff to the changed hunks plus a little context, so a small
 * edit in a long file does not print the whole file.
 */
export function compactDiff(
  lines: readonly DiffLine[],
  context = 3,
): DiffLine[] {
  const keep = new Set<number>();
  lines.forEach((line, index) => {
    if (line.kind === "same") return;
    for (let cursor = index - context; cursor <= index + context; cursor += 1) {
      if (cursor >= 0 && cursor < lines.length) keep.add(cursor);
    }
  });
  return lines.filter((_, index) => keep.has(index));
}
