import { TerminalWindow } from "./TerminalWindow";
import { useCallback, useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { useToast } from "./ToastProvider";
import "./runtime-settings.css";

type ConnectionState = "disconnected" | "connecting" | "connected";

type Session = {
  socket: WebSocket;
  terminal: Terminal;
  fit: FitAddon;
  observer: ResizeObserver;
  disposables: { dispose(): void }[];
  frame: number;
};

const commands = [
  { command: "opencode auth login", label: "登录 OpenCode" },
  { command: "opencode auth list", label: "查看已有登录配置" },
  { command: "opencode models", label: "查看可用模型" },
];

// TerminalPanel reuses the existing authenticated runtime terminal service and
// WebSocket protocol. It never persists or records terminal content.
export function TerminalPanel({
  available,
  onConnected,
}: {
  available: boolean;
  onConnected?: (connected: boolean) => void;
}) {
  const toast = useToast();
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
    current.disposables.forEach((item) => item.dispose());
    current.socket.close();
    current.terminal.reset();
    current.terminal.dispose();
    host.current?.replaceChildren();
  }, []);

  useEffect(() => () => {
    dispose();
  }, [dispose]);

  const disconnect = useCallback(
    (message = "连接已断开，当前终端内容已清空。") => {
      dispose();
      setConnection("disconnected");
      setSelection(false);
      setNotice(message);
      onConnected?.(false);
    },
    [dispose, onConnected],
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
        fontFamily: '"SFMono-Regular", Consolas, "Liberation Mono", monospace',
        fontSize: 13,
        lineHeight: 1.35,
        scrollback: 1200,
        convertEol: false,
        screenReaderMode: true,
        theme: {
          background: "#172331",
          foreground: "#e2e9f3",
          cursor: "#c1d3ef",
          selectionBackground: "#42628b",
        },
      });
      pendingTerminal = terminal;
      const fit = new FitAddon();
      terminal.loadAddon(fit);
      terminal.open(host.current);
      terminal.textarea?.setAttribute("aria-label", "OpenCode 配置终端输入");
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
        if (socket.readyState === WebSocket.OPEN)
          socket.send(JSON.stringify(data));
      };
      const disposables = [
        terminal.onData((data) => send({ type: "input", data })),
        terminal.onResize(({ cols, rows }) =>
          send({ type: "resize", cols, rows }),
        ),
        terminal.onSelectionChange(() => setSelection(terminal.hasSelection())),
      ];
      session.current = {
        socket,
        terminal,
        fit,
        observer,
        disposables,
        frame: 0,
      };
      pendingTerminal = null;
      pendingSocket = null;
      observer.observe(host.current);
      socket.onopen = () => {
        if (session.current?.socket !== socket) return;
        setConnection("connected");
        fit.fit();
        send({ type: "resize", cols: terminal.cols, rows: terminal.rows });
        terminal.focus();
        onConnected?.(true);
      };
      socket.onmessage = (event: MessageEvent) => {
        if (session.current?.socket !== socket) return;
        if (event.data instanceof ArrayBuffer) {
          terminal.write(new Uint8Array(event.data));
          return;
        }
        if (typeof event.data !== "string") return;
        try {
          const message = JSON.parse(event.data) as {
            type?: string;
            message?: string;
          };
          if (message.type === "error") {
            setTerminalError(
              message.message || "终端连接失败，请检查执行器服务。",
            );
            disconnect("终端连接已结束。修复后可重新连接。");
            return;
          }
          if (message.type === "exit") {
            disconnect("配置进程已退出，当前终端内容已清空。");
            return;
          }
        } catch {
          // Plain text output is supported alongside the binary protocol.
        }
        terminal.write(event.data);
      };
      socket.onerror = () => {
        if (session.current?.socket !== socket) return;
        setTerminalError("无法连接配置终端，请确认登录会话和执行器服务可用。");
        disconnect("连接未建立。");
      };
      socket.onclose = (event) => {
        if (session.current?.socket !== socket) return;
        if (!event.wasClean)
          setTerminalError("终端连接意外断开，请检查网络或执行器状态后重连。");
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

  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      toast.success("已复制所选终端文字。");
    } catch {
      toast.error("浏览器未允许复制，请选中文字后使用系统复制快捷键。");
    }
  }

  return (
    <TerminalWindow title="OpenCode 配置终端" available={available} connection={connection}
      onOpen={connect} onClose={() => disconnect()} onRestore={() => requestAnimationFrame(() => session.current?.fit.fit())}>
    <div className="runtime-terminal-panel wizard-terminal">
      <div className="panel-head">
        <div>
          <h2>OpenCode 配置终端</h2>
          <p>在隔离的配置环境中执行登录命令，退出后配置仍会保留。</p>
        </div>
        <div className="actions">
          <span
            className={
              "status " + (connection === "connected" ? "completed" : "paused")
            }
          >
            {connection === "connected"
              ? "已连接"
              : connection === "connecting"
                ? "连接中"
                : "未连接"}
          </span>
          {connection === "disconnected" ? (
            <button
              className="primary"
              disabled={!available}
              onClick={connect}
              type="button"
            >
              重新连接
            </button>
          ) : (
            <button onClick={() => disconnect()} type="button">
              断开连接
            </button>
          )}
        </div>
      </div>
      {!available && (
        <div className="setup-warning" role="status">
          Runner 配置终端当前不可用。请确认所选 Node 已连接且镜像就绪。
        </div>
      )}
      {terminalError && (
        <div className="error runtime-inline-error" role="alert">
          {terminalError}
        </div>
      )}
      <div className="runtime-commands">
        <span>填入命令</span>
        <div>
          {commands.map(({ command, label }) => (
            <button
              type="button"
              key={command}
              disabled={connection !== "connected"}
              title={label + "；仅填入，不自动执行"}
              onClick={() => fillCommand(command)}
            >
              <code>{command}</code>
            </button>
          ))}
        </div>
        <small>点击后追加到当前终端输入；确认后按 Enter 执行。</small>
      </div>
      <div className="runtime-terminal-stage">
        <div
          ref={host}
          className="runtime-terminal-host"
          data-testid="wizard-terminal"
        />
        {connection !== "connected" && (
          <div className="runtime-terminal-placeholder">
            <div className="runtime-terminal-symbol" aria-hidden="true">
              $ _
            </div>
            <strong>
              {connection === "connecting"
                ? "正在连接执行器…"
                : "准备好后，重新连接"}
            </strong>
            <p>打开终端并执行 opencode auth login，完成后再点击“检查并继续”。</p>
          </div>
        )}
      </div>
      <div className="runtime-terminal-footer">
        <span role="status">
          {notice || "终端内容仅保留在当前页面内存中，不会被录制或保存。"}
        </span>
        <button
          type="button"
          disabled={!selection || connection !== "connected"}
          onClick={() => {
            const text = session.current?.terminal.getSelection();
            if (text) void copy(text);
          }}
        >
          复制选中内容
        </button>
      </div>
    </div>
    </TerminalWindow>
  );
}
