// Inline CLI-login terminal. Adapted from the old RuntimeSettings/TerminalPanel:
// only the OpenCode login shell is kept (no Node, network or image options). The
// terminal still talks to /api/runtime/terminal with the existing protocol, and
// never stores its contents.
import { useCallback, useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { Icon } from "./icons";
import "@xterm/xterm/css/xterm.css";

type ConnectionState = "disconnected" | "connecting" | "connected";

type Session = {
  socket: WebSocket;
  terminal: Terminal;
  fit: FitAddon;
  observer: ResizeObserver;
  disposables: { dispose(): void }[];
  frame: number;
};

const loginCommands = [
  { command: "opencode auth login", label: "登录 OpenCode" },
  { command: "opencode auth list", label: "查看已有登录配置" },
  { command: "opencode models", label: "查看可用模型" },
];

export default function CliTerminal({ available }: { available: boolean }) {
  const [connection, setConnection] = useState<ConnectionState>("disconnected");
  const [terminalError, setTerminalError] = useState("");
  const [notice, setNotice] = useState("");
  const [selection, setSelection] = useState(false);
  const host = useRef<HTMLDivElement>(null);
  const session = useRef<Session | null>(null);

  const dispose = useCallback(() => {
    const current = session.current;
    session.current = null;
    if (!current) return;
    current.socket.onopen = null;
    current.socket.onmessage = null;
    current.socket.onerror = null;
    current.socket.onclose = null;
    current.observer.disconnect();
    cancelAnimationFrame(current.frame);
    current.disposables.forEach(item => item.dispose());
    current.socket.close();
    current.terminal.reset();
    current.terminal.dispose();
    host.current?.replaceChildren();
  }, []);

  useEffect(() => () => dispose(), [dispose]);

  const disconnect = useCallback(
    (message = "连接已断开，当前终端内容已清空。") => {
      dispose();
      setConnection("disconnected");
      setSelection(false);
      setNotice(message);
    },
    [dispose],
  );

  function connect() {
    if (!host.current || session.current || !available) return;
    setTerminalError("");
    setNotice("");
    setConnection("connecting");
    let pendingTerminal: Terminal | null = null;
    let pendingSocket: WebSocket | null = null;
    try {
      const terminal = new Terminal({
        cursorBlink: true,
        fontFamily: '"JetBrains Mono", "SFMono-Regular", Consolas, monospace',
        fontSize: 13,
        lineHeight: 1.35,
        scrollback: 1200,
        convertEol: false,
        screenReaderMode: true,
        theme: {
          background: "#23231f",
          foreground: "#fbf8f1",
          cursor: "#f2b53a",
          selectionBackground: "#5a4a1f",
        },
      });
      pendingTerminal = terminal;
      const fit = new FitAddon();
      terminal.loadAddon(fit);
      terminal.open(host.current);
      terminal.textarea?.setAttribute("aria-label", "CLI 登录终端输入");
      const url = new URL("/api/runtime/terminal", window.location.href);
      url.protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
      const socket = new WebSocket(url);
      pendingSocket = socket;
      socket.binaryType = "arraybuffer";
      const observer = new ResizeObserver(() => {
        const current = session.current;
        if (!current || current.socket !== socket) return;
        cancelAnimationFrame(current.frame);
        current.frame = requestAnimationFrame(() => {
          if (session.current?.socket === socket) fit.fit();
        });
      });
      const send = (data: unknown) => {
        if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify(data));
      };
      const disposables = [
        terminal.onData(data => send({ type: "input", data })),
        terminal.onResize(({ cols, rows }) => send({ type: "resize", cols, rows })),
        terminal.onSelectionChange(() => setSelection(terminal.hasSelection())),
      ];
      session.current = { socket, terminal, fit, observer, disposables, frame: 0 };
      pendingTerminal = null;
      pendingSocket = null;
      observer.observe(host.current);
      socket.onopen = () => {
        if (session.current?.socket !== socket) return;
        setConnection("connected");
        fit.fit();
        send({ type: "resize", cols: terminal.cols, rows: terminal.rows });
        terminal.focus();
      };
      socket.onmessage = (event: MessageEvent) => {
        if (session.current?.socket !== socket) return;
        if (event.data instanceof ArrayBuffer) {
          terminal.write(new Uint8Array(event.data));
          return;
        }
        if (typeof event.data !== "string") return;
        try {
          const message = JSON.parse(event.data) as { type?: string; message?: string };
          if (message.type === "error") {
            setTerminalError(message.message || "终端连接失败，请检查 CLI 登录服务。");
            disconnect("终端连接已结束。修复后可重新连接。");
            return;
          }
          if (message.type === "exit") {
            disconnect("登录进程已退出，当前终端内容已清空。");
            return;
          }
        } catch {
          // Plain text output is supported alongside the binary protocol.
        }
        terminal.write(event.data);
      };
      socket.onerror = () => {
        if (session.current?.socket !== socket) return;
        setTerminalError("无法连接 CLI 登录终端，请确认登录会话和 Docker 可用。");
        disconnect("连接未建立。");
      };
      socket.onclose = event => {
        if (session.current?.socket !== socket) return;
        if (!event.wasClean) setTerminalError("终端连接意外断开，请重新连接。");
        disconnect("连接已结束，当前终端内容已清空。");
      };
    } catch {
      dispose();
      pendingSocket?.close();
      pendingTerminal?.dispose();
      host.current?.replaceChildren();
      setConnection("disconnected");
      setTerminalError("无法初始化终端，请刷新页面重试。");
    }
  }

  function fillCommand(command: string) {
    const current = session.current;
    if (!current || current.socket.readyState !== WebSocket.OPEN) return;
    current.terminal.paste(command);
    current.terminal.focus();
    setNotice("命令已填入终端，请确认后按 Enter 执行。");
  }

  async function copySelection() {
    const text = session.current?.terminal.getSelection();
    if (!text) return;
    try {
      await navigator.clipboard.writeText(text);
      setNotice("已复制所选终端文字。");
    } catch {
      setNotice("浏览器未允许复制，请选中文字后使用系统复制快捷键。");
    }
  }

  return (
    <section className="hg-terminal" aria-labelledby="hg-terminal-title">
      <header className="hg-terminal-head">
        <div>
          <h2 id="hg-terminal-title">
            <Icon name="terminal" size={16} />
            CLI 登录终端
          </h2>
          <p>在隔离环境里完成登录；结果会保留，并自动交给新的 Cell 使用。</p>
        </div>
        <div className="hg-terminal-actions">
          <span className={`hg-badge ${connection === "connected" ? "hg-tone-mint" : "hg-tone-neutral"}`}>
            <span
              className={`hg-dot ${connection === "connected" ? "hg-tone-mint" : "hg-tone-neutral"}`}
              aria-hidden="true"
            />
            {connection === "connected" ? "已连接" : connection === "connecting" ? "连接中" : "未连接"}
          </span>
          {connection === "disconnected" ? (
            <button className="hg-btn hg-btn-primary" type="button" disabled={!available} onClick={connect}>
              连接终端
            </button>
          ) : (
            <button className="hg-btn" type="button" onClick={() => disconnect()}>
              断开连接
            </button>
          )}
        </div>
      </header>

      {!available ? (
        <p className="hg-callout hg-callout-amber" role="status">
          CLI 登录终端当前不可用。启用 Cell 并挂载 Docker socket 后才能打开。
        </p>
      ) : null}
      {terminalError ? (
        <p className="hg-form-error" role="alert">
          {terminalError}
        </p>
      ) : null}

      <div className="hg-terminal-commands">
        <span>填入命令</span>
        <div>
          {loginCommands.map(({ command, label }) => (
            <button
              className="hg-btn hg-btn-small"
              type="button"
              key={command}
              disabled={connection !== "connected"}
              title={`${label}；仅填入，不自动执行`}
              onClick={() => fillCommand(command)}
            >
              <code>{command}</code>
            </button>
          ))}
        </div>
        <small>点击后追加到当前终端输入；确认后按 Enter 执行。</small>
      </div>

      <div className="hg-terminal-stage">
        <div ref={host} className="hg-terminal-host" data-testid="cli-terminal" />
        {connection !== "connected" ? (
          <div className="hg-terminal-placeholder">
            <strong>
              {connection === "connecting" ? "正在连接…" : "点“连接终端”开始登录"}
            </strong>
            <p>执行 opencode auth login，按提示完成授权。终端内容只留在当前页面内存里。</p>
          </div>
        ) : null}
      </div>

      <footer className="hg-terminal-foot">
        <span role="status">
          {notice || "登录后无需重启；新的 Forager 会自动使用这次登录的凭证。"}
        </span>
        <button className="hg-btn hg-btn-small" type="button" disabled={!selection} onClick={() => void copySelection()}>
          复制选中内容
        </button>
      </footer>
    </section>
  );
}
