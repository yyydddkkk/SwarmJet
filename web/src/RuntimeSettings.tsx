import { TerminalWindow } from "./TerminalWindow";
import { useCallback, useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { api } from "./api";
import { useAgentCatalog, type AgentCatalog } from "./agents";
import type { NodeView } from "./NodeSettings";
import "@xterm/xterm/css/xterm.css";
import "./runtime-settings.css";

type RuntimeStatus = {
 agents?: {id:string;installed:boolean;configured:boolean;executionSupported:boolean}[];
  profile: string;
  image: string;
  available: boolean;
  toolsChecked?: boolean;
  opencode: {
    installed: boolean;
    configured: boolean;
    providers: string[] | null;
  };
  codex: {
    installed: boolean;
    configured: boolean;
    executionSupported: boolean;
  };
  message?: string;
};
type ModelEntry = string | { id: string; name?: string; provider?: string };
type RuntimeModels = {
  models: ModelEntry[] | null;
  providers: string[] | null;
  warnings: string[] | null;
};
type ConnectionState = "disconnected" | "connecting" | "connected";
type TerminalSession = {
  socket: WebSocket;
  terminal: Terminal;
  fit: FitAddon;
  observer: ResizeObserver;
  disposables: { dispose(): void }[];
  frame: number;
};
const commands = [
  { command: "opencode auth login", label: "登录 OpenCode" },
  { command: "opencode auth list", label: "查看 OpenCode 登录配置" },
  { command: "opencode models", label: "查看模型" },
  { command: "codex login --device-auth", label: "登录 Codex" },
  { command: "codex login status", label: "查看 Codex 登录状态" },
  {command:"dst",label:"配置 DeepSeek Harness TUI"},
  {command:"agy",label:"配置 Antigravity"},
  {command:"claude",label:"配置 Claude Code"},
];

function modelID(model: ModelEntry): string {
  if (typeof model === "string") return model;
  if (!model?.id) return "";
  return model.provider && !model.id.includes("/")
    ? `${model.provider}/${model.id}`
    : model.id;
}

const requestedNode=()=>new URLSearchParams(location.hash.split('?')[1]||'').get('node')||'';
const nodePath=(path:string,nodeID:string)=>`${path}?node=${encodeURIComponent(nodeID)}`;
export default function RuntimeSettings() {
  const [nodes,setNodes] = useState<NodeView[]>([]);
  const [nodeID,setNodeID] = useState("");
  const [nodesLoading,setNodesLoading] = useState(true);
  const [nodesError,setNodesError] = useState("");
  const [testingRunner,setTestingRunner] = useState(false);
  const [runnerResult,setRunnerResult] = useState<{ok:boolean;image:string;agents:string[]}|null>(null);
  const [runnerError,setRunnerError] = useState("");
  const {catalog,setCatalog,error:agentsError,setError:setAgentsError} = useAgentCatalog();
  const [savingAgents,setSavingAgents] = useState(false);
  const [expandedAgents, setExpandedAgents] = useState<string[]>([]);
  const [agentsNotice,setAgentsNotice] = useState("");
  const [status, setStatus] = useState<RuntimeStatus | null>(null);
  const [models, setModels] = useState<RuntimeModels | null>(null);
  const [loading, setLoading] = useState(false);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [statusError, setStatusError] = useState("");
  const [modelsError, setModelsError] = useState("");
  const [terminalError, setTerminalError] = useState("");
  const [connection, setConnection] = useState<ConnectionState>("disconnected");
  const [terminalNotice, setTerminalNotice] = useState("");
  const [selection, setSelection] = useState(false);
  const [search, setSearch] = useState("");
  const [copyNotice, setCopyNotice] = useState("");
  const host = useRef<HTMLDivElement>(null);
  const session = useRef<TerminalSession | null>(null);
  const mounted = useRef(true);
  const requests = useRef(new Set<AbortController>());
  const noticeTimer = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined,
  );

  const refreshStatus = useCallback(async () => {
    const controller = new AbortController();
    requests.current.add(controller);
    setLoading(true);
    setStatusError("");
    try {
      const result = await api<RuntimeStatus>(
        nodePath("/runtime/status",nodeID),
        "GET",
        undefined,
        controller.signal,
      );
      if (!controller.signal.aborted) setStatus(result);
    } catch (error) {
      if (!controller.signal.aborted) setStatusError((error as Error).message);
    } finally {
      requests.current.delete(controller);
      if (!controller.signal.aborted) setLoading(false);
    }
  }, [nodeID]);
  const refreshModels = useCallback(async () => {
    const controller = new AbortController();
    requests.current.add(controller);
    setModelsLoading(true);
    setModelsError("");
    try {
      const result = await api<RuntimeModels>(
        nodePath("/runtime/models",nodeID),
        "GET",
        undefined,
        controller.signal,
      );
      if (!controller.signal.aborted) setModels(result);
    } catch (error) {
      if (!controller.signal.aborted) setModelsError((error as Error).message);
    } finally {
      requests.current.delete(controller);
      if (!controller.signal.aborted) setModelsLoading(false);
    }
  }, [nodeID]);

  useEffect(() => {
    const controller = new AbortController();
    api<NodeView[]>("/nodes", "GET", undefined, controller.signal)
      .then((list) => {
        setNodes(list);
        const requested = requestedNode();
        if (requested && !list.some((item) => item.node.id === requested)) {
          setNodesError(`Node ${requested} 不存在，请重新选择。`);
        } else {
          setNodeID(requested || list.find((item) => item.online)?.node.id || list[0]?.node.id || "");
        }
      })
      .catch((error) => { if (!controller.signal.aborted) setNodesError((error as Error).message); })
      .finally(() => { if (!controller.signal.aborted) setNodesLoading(false); });
    return () => controller.abort();
  }, []);

  const disposeSession = useCallback(() => {
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

  useEffect(() => {
    mounted.current = true;
    if (nodeID) void refreshStatus();
    return () => {
      mounted.current = false;
      requests.current.forEach((request) => request.abort());
      requests.current.clear();
      clearTimeout(noticeTimer.current);
      disposeSession();
    };
  }, [disposeSession, refreshStatus, nodeID]);

  function disconnect(message = "连接已断开，当前终端内容已清空。") {
    disposeSession();
    setConnection("disconnected");
    setSelection(false);
    setTerminalNotice(message);
  }

  function connect() {
    if (!host.current || session.current || !status?.available) return;
    setTerminalError("");
    setTerminalNotice("");
    setCopyNotice("");
    setConnection("connecting");
    let pendingTerminal: Terminal | null = null;
    let pendingSocket: WebSocket | null = null;
    try {
      const terminal = new Terminal({
        cursorBlink: true,
        fontFamily: '"SFMono-Regular", Consolas, "Liberation Mono", monospace',
        fontSize: 13,
        lineHeight: 1.35,
        scrollback: 1500,
        convertEol: false,
        screenReaderMode: true,
        theme: {
          background: "#172331",
          foreground: "#e2e9f3",
          cursor: "#c1d3ef",
          selectionBackground: "#42628b",
          black: "#172331",
          red: "#f2a399",
          green: "#9cd0ab",
          yellow: "#efd28d",
          blue: "#8fb6f4",
          magenta: "#c2ace8",
          cyan: "#95d8de",
          white: "#e2e9f3",
        },
      });
      pendingTerminal = terminal;
      const fit = new FitAddon();
      terminal.loadAddon(fit);
      terminal.open(host.current);
      terminal.textarea?.setAttribute("aria-label", "Runner 配置终端输入");
      terminal.attachCustomKeyEventHandler((event) => {
        // Let the browser copy selected output without sending Ctrl-C to the shell.
        if (
          (event.ctrlKey || event.metaKey) &&
          event.key.toLowerCase() === "c" &&
          terminal.hasSelection()
        )
          return false;
        return true;
      });
      const url = new URL(nodePath("/api/runtime/terminal",nodeID), window.location.href);
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
        void refreshStatus();
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
              message.message || "终端连接失败，请检查所选 Node。",
            );
            disconnect("终端连接已结束。修复后可重新连接。");
            return;
          }
          if (message.type === "exit") {
            disconnect("配置进程已退出，当前终端内容已清空。");
            void refreshStatus();
            return;
          }
        } catch {
          // Plain text output is supported alongside the binary terminal protocol.
        }
        terminal.write(event.data);
      };
      socket.onerror = () => {
        if (session.current?.socket !== socket) return;
        setTerminalError(
          "无法连接配置终端。请确认登录会话和所选 Node 可用，然后重新连接。",
        );
        disconnect("连接未建立。");
      };
      socket.onclose = (event) => {
        if (session.current?.socket !== socket) return;
        if (!event.wasClean)
          setTerminalError(
            "终端连接意外断开，请检查网络或所选 Node 状态后重新连接。",
          );
        disconnect("连接已结束，当前终端内容已清空。");
      };
    } catch {
      disposeSession();
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
    // No newline: the operator reviews the input and presses Enter themselves.
    current.terminal.paste(command);
    current.terminal.focus();
    setTerminalNotice("命令已填入终端，请确认当前输入后按 Enter 执行。");
  }
  async function copy(text: string, message: string) {
    try {
      await navigator.clipboard.writeText(text);
      if (!mounted.current) return;
      setCopyNotice(message);
    } catch {
      if (!mounted.current) return;
      setCopyNotice("浏览器未允许复制，请选中文字后使用系统复制快捷键。");
    }
    clearTimeout(noticeTimer.current);
    noticeTimer.current = setTimeout(() => setCopyNotice(""), 4000);
  }

  const allModels = [
    ...new Set((models?.models || []).map(modelID).filter(Boolean)),
  ];
  const filteredModels = allModels.filter((model) =>
    model.toLowerCase().includes(search.toLowerCase()),
  );
  const providers = models?.providers || status?.opencode?.providers || [];
  const node = nodes.find((item) => item.node.id === nodeID);

  return (
    <div className="runtime-page">
      <div className="page-head">
        <div>
          <div className="eyebrow">RUNNER ENVIRONMENT</div>
          <h1>Runner 环境</h1>
          <p>选择 Node，检查它用于启动 Runner 的镜像和 Agent CLI，并配置该节点的登录信息。</p>
        </div>
        <button onClick={() => void refreshStatus()} disabled={!nodeID || loading}>
          {loading ? "正在检查…" : "刷新状态"}
        </button>
      </div>

      <section className="panel panel-padding runtime-node-panel" aria-label="Node 选择与测试">
        <label htmlFor="runtime-node">选择 Node</label>
        <select id="runtime-node" value={nodeID} disabled={nodesLoading || !nodes.length}
          onChange={(event) => { location.hash = `#/runtime?node=${encodeURIComponent(event.target.value)}`; }}>
          {!nodeID && <option value="">{nodesLoading ? "正在读取 Node…" : "请选择 Node"}</option>}
          {nodes.map((item) => <option key={item.node.id} value={item.node.id}>
            {item.config?.name || item.node.id}（{item.node.id}）{item.online ? "" : "· 离线"}
          </option>)}
        </select>
        {nodesError && <p role="alert">{nodesError}</p>}
        {!nodesLoading && !nodes.length && !nodesError && <p>暂无 Node，请先到<a href="#/nodes">执行节点</a>配置。</p>}
        {node && <p>Node：{node.online ? "在线" : "离线"} · 镜像：{node.node.readiness?.status === "ready" ? "就绪" : node.node.readiness?.status || "未就绪"} · Runner 镜像：<code>{node.config?.image || "未配置"}</code></p>}
        <div className="runtime-runner-check">
          <button type="button" disabled={!nodeID || !node?.online || testingRunner}
            onClick={async () => {
              setTestingRunner(true); setRunnerResult(null); setRunnerError("");
              try {
                const result = await api<{ok:boolean;image:string;agents:string[]}>(nodePath("/runtime/runner-test",nodeID),"POST");
                setRunnerResult(result);
                void refreshStatus();
              } catch (error) { setRunnerError((error as Error).message); }
              finally { setTestingRunner(false); }
            }}>{testingRunner ? "正在测试 Runner…" : "测试此 Node 的 Runner"}</button>
          <span>启动临时 Runner 容器并检查 Agent CLI；不发起模型调用。</span>
        </div>
        {runnerError && <p role="alert">Runner 测试失败：{runnerError}</p>}
        {runnerResult && <p role="status">{runnerResult.ok ? "Runner 镜像启动成功" : "Runner 镜像已启动，但未检测到受支持的 Agent CLI"}：{runnerResult.agents.length ? runnerResult.agents.join("、") : "无"}。镜像：<code>{runnerResult.image}</code>。登录与模型可用性需在配置终端单独验证。</p>}
      </section>

      {statusError && (
        <div className="error" role="alert">
          {statusError}
          <button onClick={() => void refreshStatus()}>重试</button>
        </div>
      )}
      {status?.message && (
        <div className="runtime-message">{status.message}</div>
      )}
      <section className="panel panel-padding" aria-label="启用 Agent">
        <h2>启用 Agent</h2>
        <p>启用列表对所有 Node 生效；下方安装和配置状态来自当前选中的 Node。未安装的工具可前往<a href="#/images">镜像管理</a>构建。</p>
        {agentsError && <p role="alert">{agentsError}</p>}
        <div className="runtime-agent-list" aria-label="可用 Agent">
          {catalog.agents.map((agent) => {
            const info = status?.agents?.find((item) => item.id === agent.id);
            const expanded = expandedAgents.includes(agent.id);
            return (
              <section className="runtime-agent-card" key={agent.id} aria-label={`${agent.name} 环境状态`}>
                <div className="runtime-agent-card-header">
                  <label className="checkbox runtime-agent-option">
                    <input
                      type="checkbox"
                      checked={catalog.enabled.includes(agent.id)}
                      disabled={savingAgents}
                      onChange={(e) => {
                        setAgentsNotice("");
                        setCatalog({
                          ...catalog,
                          enabled: e.target.checked
                            ? [...catalog.enabled, agent.id]
                            : catalog.enabled.filter((id) => id !== agent.id),
                        });
                      }}
                    />
                    <span>{agent.name}</span>
                  </label>
                  <button
                    type="button"
                    className="runtime-agent-toggle"
                    aria-label={`${expanded ? "收起" : "展开"} ${agent.name} 详情`}
                    aria-expanded={expanded}
                    aria-controls={`agent-details-${agent.id}`}
                    onClick={() => setExpandedAgents((ids) => expanded
                      ? ids.filter((id) => id !== agent.id)
                      : [...ids, agent.id])}
                  >
                    <span aria-hidden="true">{expanded ? "⌃" : "⌄"}</span>
                  </button>
                </div>
                <div id={`agent-details-${agent.id}`} className="runtime-agent-card-details" hidden={!expanded}>
                  <p><code>agent:{agent.id}</code></p>
                  <div className="runtime-tool-state"><span>工具安装</span><strong>{status?.toolsChecked ? info?.installed ? "已安装" : "未检测到工具" : runnerResult ? runnerResult.agents.includes(agent.id) ? "已安装" : "未检测到工具" : "测试 Runner 或打开终端后检测"}</strong></div>
                  <div className="runtime-tool-state"><span>配置文件</span><strong>{!status ? "尚未读取" : info?.configured ? "已检测到（不代表认证有效）" : "尚未检测到"}</strong></div>
                  <p>配置入口：<code>{agent.setupCommand}</code></p>
                  <p>勾选只控制启用范围，安装或扩展工具请前往<a href="#/images">镜像管理</a>。模型使用 CLI 保存的默认配置；终端 export 不会自动传给任务容器。</p>
                  {agent.id === "agy" && <p>任务使用 GEMINI_API_KEY 或 ADC 文件认证；系统钥匙串登录不会复制到任务容器。</p>}
                </div>
              </section>
            );
          })}
        </div>
        <button type="button" disabled={savingAgents || !catalog.agents.length} onClick={async () => {
          setSavingAgents(true); setAgentsError(""); setAgentsNotice("");
          try { setCatalog(await api<AgentCatalog>("/runtime/agents","PUT",{enabled:catalog.enabled})); setAgentsNotice("启用列表已保存，作用于新任务；已有任务继续使用原 Agent。"); }
          catch(e) { setAgentsError((e as Error).message); }
          finally { setSavingAgents(false); }
        }}>{savingAgents ? "保存中…" : "保存启用列表"}</button>
        {agentsNotice && <p role="status">{agentsNotice}</p>}
      </section>

      <TerminalWindow title="Runner 配置终端" available={!!nodeID && !!status?.available && !loading} connection={connection}
        onOpen={connect} onClose={() => disconnect()} onRestore={() => requestAnimationFrame(() => session.current?.fit.fit())}>
      <section
        className="panel runtime-terminal-panel"
        aria-labelledby="runtime-terminal-title"
      >
        <div className="panel-head">
          <div>
            <h2 id="runtime-terminal-title">配置终端</h2>
            <p>在隔离的配置环境中完成登录，退出后配置仍会保留。</p>
          </div>
          <div className="actions">
            <span
              className={`status ${connection === "connected" ? "completed" : "paused"}`}
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
                disabled={!status?.available || loading}
                onClick={connect}
              >
                重新连接
              </button>
            ) : (
              <button onClick={() => disconnect()}>断开连接</button>
            )}
          </div>
        </div>
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
                title={`${label}；仅填入，不自动执行`}
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
            data-testid="runtime-terminal"
          />
          {connection !== "connected" && (
            <div className="runtime-terminal-placeholder">
              <div className="runtime-terminal-symbol" aria-hidden="true">
                $ _
              </div>
              <strong>
                {connection === "connecting"
                  ? "正在连接 Node…"
                  : "准备好后，重新连接"}
              </strong>
              <p>
                {!status?.available && status
                  ? "当前 Node 的 Runner 环境不可用，请检查镜像和节点状态。"
                  : "通过工具的交互式登录流程配置服务。"}
              </p>
            </div>
          )}
        </div>
        <div className="runtime-terminal-footer">
          <span role="status">
            {copyNotice || terminalNotice ||
              "选中终端文字可复制登录链接。终端内容仅保留在当前页面内存中。"}
          </span>
          <button
            disabled={!selection || connection !== "connected"}
            onClick={() => {
              const text = session.current?.terminal.getSelection();
              if (text) void copy(text, "已复制所选终端文字。");
            }}
          >
            复制选中内容
          </button>
        </div>
      </section>
      </TerminalWindow>

      <section
        className="panel runtime-models-panel"
        aria-labelledby="runtime-models-title"
      >
        <div className="panel-head">
          <div>
            <h2 id="runtime-models-title">
              模型标识 <span className="count">{allModels.length}</span>
            </h2>
            <p>登录后刷新列表，复制标识到项目的模型配置。</p>
          </div>
          <button
            onClick={() => void refreshModels()}
            disabled={modelsLoading || !status?.available}
          >
            {modelsLoading ? "正在读取…" : "刷新模型列表"}
          </button>
        </div>
        {modelsError && (
          <div className="error runtime-inline-error" role="alert">
            {modelsError}
          </div>
        )}
        {(models?.warnings || []).map((warning, index) => (
          <p className="runtime-message" key={index}>
            {warning}
          </p>
        ))}
        <div className="runtime-model-toolbar">
          <div className="runtime-providers">
            <span>服务商</span>
            {providers.length ? (
              providers.map((provider) => (
                <span className="tag" key={provider}>
                  {provider}
                </span>
              ))
            ) : (
              <span className="muted">尚未读取</span>
            )}
          </div>
          <label className="runtime-search">
            <span className="runtime-visually-hidden">搜索模型标识</span>
            <input
              placeholder="搜索 provider/model…"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </label>
        </div>
        {filteredModels.length ? (
          <ul className="runtime-model-list">
            {filteredModels.map((id) => (
              <li key={id}>
                <code>{id}</code>
                <button
                  aria-label={`复制模型 ${id}`}
                  onClick={() => void copy(id, `已复制模型标识 ${id}`)}
                >
                  复制
                </button>
              </li>
            ))}
          </ul>
        ) : (
          <div className="runtime-model-empty">
            {modelsLoading
              ? "正在读取模型列表…"
              : !models
                ? "模型列表尚未读取。完成登录后点击“刷新模型列表”。"
                : search
                  ? "没有匹配的模型标识。"
                  : "未发现可用模型标识，请在配置终端中检查登录和服务商配置。"}
          </div>
        )}
        <p className="runtime-model-help">
          检测到配置或模型名称，不代表凭据仍有效、账号有余额或当前请求可用。
        </p>
      </section>
      <div className="runtime-copy-notice" role="status" aria-live="polite">
        {copyNotice}
      </div>
      <details className="runtime-advanced">
        <summary>环境详情</summary>
        <dl>
          <dt>配置档案</dt>
          <dd>{status?.profile || "default"}</dd>
          <dt>Runner 镜像</dt>
          <dd>
            <code>{status?.image || "尚未读取"}</code>
          </dd>
        </dl>
        <p>
          镜像由当前 Node 配置管理。新任务读取更新后的登录配置，已有任务继续使用自己的会话。
        </p>
      </details>
    </div>
  );
}
