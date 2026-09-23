import { useEffect, useId, useMemo, useRef, useState } from "react";
import { APIError, getLog } from "./api";
import type { Execution, Task } from "./types";
import "./task-terminal.css";

const liveStatuses = new Set(["starting", "running"]);
const visibleLines = 2000;

function roleName(role: string) {
  return ({ dev: "开发 Agent", development: "开发 Agent", review: "审查 Agent", merge: "合并 Agent" } as Record<string, string>)[role] || `${role} Agent`;
}

function eventText(value: unknown): string {
  if (typeof value === "string") return value;
  if (Array.isArray(value)) return value.map(eventText).filter(Boolean).join("\n");
  if (!value || typeof value !== "object") return "";
  const part = value as Record<string, unknown>;
  for (const key of ["text", "aggregated_output", "content"]) {
    const text = eventText(part[key]);
    if (text) return text;
  }
  return "";
}

function readableLine(line: string) {
  try {
    const event = JSON.parse(line) as Record<string, unknown>;
    if (!event || typeof event !== "object" || Array.isArray(event)) return line;
    const item = event.item && typeof event.item === "object" ? event.item as Record<string, unknown> : null;
    const kind = [event.type, event.event, item?.type].filter((v): v is string => typeof v === "string").join(" · ");
    const command = typeof item?.command === "string" ? `$ ${item.command}` : "";
    const message = eventText(event.part) || eventText(item) || eventText(event.message) || eventText(event.text) || eventText(event.content);
    if (!command && !message) return line;
    return `${kind ? `[${kind}] ` : ""}${[command, message].filter(Boolean).join("\n")}`;
  } catch {
    return line;
  }
}

function displayOutput(raw: string, showRaw: boolean) {
  const lines = raw.split(/\r?\n/);
  if (lines[lines.length - 1] === "") lines.pop();
  const hidden = Math.max(0, lines.length - visibleLines);
  const tail = lines.slice(hidden);
  return `${hidden ? `仅显示最近 ${visibleLines} 行；完整内容可在“执行记录”中查看。\n\n` : ""}${(showRaw ? tail : tail.map(readableLine)).join("\n")}`;
}

function preferredRole(entries: [string, Execution][]) {
  const live = entries.find(([, execution]) => liveStatuses.has(execution.status));
  if (live) return live[0];
  for (const role of ["merge", "review", "dev", "development"]) {
    if (entries.some(([key]) => key === role)) return role;
  }
  return entries[0]?.[0] || "";
}

export function TaskTerminal({ task }: { task: Task }) {
  const [open, setOpen] = useState(false);
  const [selectedRole, setSelectedRole] = useState("");
  const [raw, setRaw] = useState(false);
  const [body, setBody] = useState("");
  const [loading, setLoading] = useState(false);
  const [waiting, setWaiting] = useState(false);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  const dialog = useRef<HTMLDialogElement>(null);
  const output = useRef<HTMLPreElement>(null);
  const followOutput = useRef(true);
  const titleId = useId();
  const entries = Object.entries(task.executions || {});
  const role = selectedRole && task.executions?.[selectedRole] ? selectedRole : preferredRole(entries);
  const execution = task.executions?.[role];
  const live = !!execution && liveStatuses.has(execution.status);
  const rendered = useMemo(() => displayOutput(body, raw), [body, raw]);

  useEffect(() => {
    if (!open) return;
    dialog.current?.showModal();
    const previous = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      dialog.current?.close();
      document.body.style.overflow = previous;
    };
  }, [open]);

  useEffect(() => {
    if (!open) return;
    followOutput.current = true;
    setBody("");
    setError("");
    setWaiting(false);
    if (!execution) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let controller: AbortController | undefined;
    const load = async () => {
      if (document.hidden) {
        if (live) timer = setTimeout(load, 3000);
        return;
      }
      controller = new AbortController();
      setLoading(true);
      try {
        const next = await getLog(task.id, execution.id, controller.signal);
        if (stopped) return;
        setBody(next);
        setWaiting(false);
        setError("");
      } catch (cause) {
        if (stopped || (cause instanceof DOMException && cause.name === "AbortError")) return;
        if (cause instanceof APIError && cause.status === 404 && live) {
          setWaiting(true);
          setError("");
        } else {
          setWaiting(false);
          setError((cause as Error).message);
        }
      } finally {
        if (!stopped) {
          setLoading(false);
          if (live) timer = setTimeout(load, 3000);
        }
      }
    };
    void load();
    return () => {
      stopped = true;
      clearTimeout(timer);
      controller?.abort();
    };
  }, [open, task.id, execution?.id, live, retry]);

  useEffect(() => {
    const host = output.current;
    if (host && followOutput.current) host.scrollTop = host.scrollHeight;
  }, [rendered]);

  return (
    <>
      <button type="button" className="button task-terminal-open" aria-haspopup="dialog" onClick={() => {
        setSelectedRole("");
        setRaw(false);
        setOpen(true);
      }}>打开终端</button>
      {open && <dialog ref={dialog} className="task-terminal-window" aria-labelledby={titleId}
        onCancel={() => setOpen(false)} onClose={() => setOpen(false)}>
        <header className="task-terminal-bar">
          <div>
            <strong id={titleId}>任务终端 · 只读</strong>
            <small>{task.issue.title}</small>
          </div>
          <button type="button" onClick={() => dialog.current?.close()}>关闭终端</button>
        </header>
        <div className="task-terminal-controls">
          {entries.length > 0 && <label>Agent 执行 <select value={role} onChange={(event) => setSelectedRole(event.target.value)}>
            {entries.map(([key, item]) => <option key={key} value={key}>{roleName(key)} · {item.status}</option>)}
          </select></label>}
          {execution && <span role="status">{live ? "运行中 · 自动更新" : `已结束 · ${execution.status}`}</span>}
          <div className="task-terminal-control-actions">
            <button type="button" aria-pressed={raw} onClick={() => setRaw(!raw)}>{raw ? "易读视图" : "原始日志"}</button>
            {execution && <button type="button" onClick={() => setRetry((value) => value + 1)}>刷新</button>}
          </div>
        </div>
        <div className="task-terminal-stage">
          {error && <div className="task-terminal-error" role="alert">日志读取失败：{error}</div>}
          <pre ref={output} data-testid="task-terminal-output" tabIndex={0} aria-label="Agent 只读运行输出"
            onScroll={(event) => {
              const host = event.currentTarget;
              followOutput.current = host.scrollHeight - host.scrollTop - host.clientHeight < 100;
            }}>{rendered || (error ? "" : !execution ? "Agent 尚未启动，等待运行输出。" : waiting ? "Agent 已启动，等待首批运行输出。" : loading ? "正在读取运行输出…" : "暂无运行输出。")}</pre>
        </div>
        <footer className="task-terminal-foot">这里只展示 Agent 已输出的脱敏日志。可以选择并复制，不能输入命令。</footer>
      </dialog>}
    </>
  );
}
