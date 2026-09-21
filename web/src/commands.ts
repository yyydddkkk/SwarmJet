// The API accepts argv, not shell programs. Quoting here only groups arguments.
export function formatCommand(args: string[]): string {
  return args
    .map((arg) =>
      /^[\w./:@%+=,-]+$/.test(arg)
        ? arg
        : "'" + arg.replace(/'/g, "'\\''") + "'",
    )
    .join(" ");
}
export function parseCommand(text: string): string[] {
  const out: string[] = [];
  let word = "",
    quote = "",
    started = false,
    escape = false;
  for (const c of text) {
    if (escape) {
      word += c;
      started = true;
      escape = false;
      continue;
    }
    if (c === "\\" && quote !== "'") {
      escape = true;
      started = true;
      continue;
    }
    if (quote) {
      if (c === quote) quote = "";
      else word += c;
      started = true;
      continue;
    }
    if (c === '"' || c === "'") {
      quote = c;
      started = true;
      continue;
    }
    if (/\s/.test(c)) {
      if (started) {
        out.push(word);
        word = "";
        started = false;
      }
      continue;
    }
    if (/[|&;<>`]/.test(c))
      throw new Error("检查命令不支持管道或命令连接符；请将多步操作写入脚本。");
    word += c;
    started = true;
  }
  if (quote || escape)
    throw new Error("检查命令的引号或转义未结束，请补全后保存。");
  if (started) out.push(word);
  return out;
}
