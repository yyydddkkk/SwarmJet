// Hive page: conversation section with the Swarm rail, plus placeholders for the
// sections the next rounds will build (ADR section 18).
import { useEffect, useId, useRef, useState } from "react";
import {
  agentsByHive,
  conversationByHive,
  foragerDetailById,
  overviewById,
} from "../fixtures/hive-console";
import type { Agent, ConversationEntry, Hive } from "./types";
import {
  EmptyState,
  BeeLoader,
  Panel,
  StatusBadge,
  Tabs,
  stateLabels,
} from "./components";
import { Icon } from "./icons";
import { ForagerAvatar, QueenAvatar, foragerNeedsYou } from "./pixel";
import { ConversationStream } from "./ConversationStream";

const sections = [
  { id: "conversation", label: "对话" },
  { id: "swarm", label: "Swarm" },
  { id: "timeline", label: "时间线" },
  { id: "inbox", label: "Inbox" },
  { id: "memory", label: "记忆" },
  { id: "settings", label: "设置" },
] as const;

const placeholders: Record<string, { title: string; hint: string }> = {
  timeline: {
    title: "时间线",
    hint: "时间线会按 chain_id 展开因果树：哪个事件触发了哪些消息和 Forager，最后产出了哪些 PR。这一轮先放占位。",
  },
  inbox: {
    title: "Inbox（蜂巢收件箱）",
    hint: "无法确定性路由的事件会留在这里，可以认领、指派给 Forager 或忽略。这一轮先放占位。",
  },
  memory: {
    title: "记忆",
    hint: "这里会放 Situation、Queen 的私有记忆、Dream 报告和待合并的记忆 PR。这一轮先放占位。",
  },
  settings: {
    title: "设置",
    hint: "仓库、CLI 与模型凭证、Queen 开关、触发规则、Guard 与上限。这一轮先放占位。",
  },
};

export default function HivePage({ hiveId, section }: { hiveId: string; section: string }) {
  const overview = overviewById(hiveId);
  if (!overview) {
    return (
      <div className="hg-page">
        <EmptyState
          title="找不到这个 Hive。"
          hint="它可能已经被删除，或者链接不对。"
          action={
            <a className="hg-btn" href="#/hives">
              返回 Hive 列表
            </a>
          }
        />
      </div>
    );
  }

  const { hive } = overview;
  const agents = agentsByHive(hive.id);
  const queen = agents.find(agent => agent.role === "queen");
  const foragers = agents.filter(agent => agent.role === "forager");
  const entries = conversationByHive(hive.id);
  const active = section || "conversation";

  const tabs = sections.map(item => ({
    ...item,
    href: item.id === "conversation" ? `#/hives/${hive.id}` : `#/hives/${hive.id}/${item.id}`,
    count:
      item.id === "swarm" ? foragers.length : item.id === "inbox" ? overview.inboxBacklog : undefined,
  }));

  return (
    <div className="hg-page">
      <nav className="hg-crumbs" aria-label="面包屑">
        <a href="#/hives">Hive</a>
        <span aria-hidden="true">/</span>
        <span>{hive.name}</span>
      </nav>

      <header className="hg-page-head hg-hive-head">
        <span className="hg-hive-mark hg-hive-mark-0" aria-hidden="true">
          <Icon name="hive" size={32} />
        </span>
        <div className="hg-hive-head-text">
          <h1>{hive.name}</h1>
          <p className="hg-repo">
            {hive.repository} · CLI {hive.config.cli}
          </p>
        </div>
        <div className="hg-head-badges">
          <StatusBadge
            state={queen?.state ?? (hive.queenEnabled ? "active" : "disabled")}
            label={`Queen（蜂王）${hive.queenEnabled ? "已开启" : "未开启"}`}
          />
          <span className="hg-badge hg-tone-neutral">{foragers.length} 只 Forager</span>
        </div>
      </header>

      <Tabs items={tabs} active={active} label={`${hive.name} 分区`} />

      {active === "conversation" ? (
        <ConversationSection
          hive={hive}
          agents={agents}
          foragers={foragers}
          entries={entries}
          maxConcurrent={hive.config.maxConcurrentForagers}
        />
      ) : active === "swarm" ? (
        <div className="hg-conversation single">
          <SwarmPanel
            hive={hive}
            foragers={foragers}
            entries={entries}
            maxConcurrent={hive.config.maxConcurrentForagers}
          />
        </div>
      ) : (
        <Panel title={placeholders[active]?.title ?? "占位"}>
          <EmptyState
            title={`${placeholders[active]?.title ?? "这个分区"}还没实现。`}
            hint={placeholders[active]?.hint}
          />
        </Panel>
      )}
    </div>
  );
}

function ConversationSection({
  hive,
  agents,
  foragers,
  entries,
  maxConcurrent,
}: {
  hive: Hive;
  agents: Agent[];
  foragers: Agent[];
  entries: ConversationEntry[];
  maxConcurrent: number;
}) {
  const [stream, setStream] = useState<ConversationEntry[]>(entries);
  const [queenThinking, setQueenThinking] = useState(false);
  const localSeq = useRef(0);
  const replyTimer = useRef<number | undefined>(undefined);

  useEffect(() => () => window.clearTimeout(replyTimer.current), []);

  const appendLocal = (body: string) => {
    const seq = localSeq.current++;
    const now = new Date().toISOString();
    const localEntry: ConversationEntry = {
      id: `local-${seq}`,
      kind: "message",
      at: "刚刚",
      message: {
        id: `local-msg-${seq}`,
        hiveId: hive.id,
        from: "local:tok-1",
        to: "queen",
        kind: "task",
        chainId: `chain-local-${seq}`,
        hop: 0,
        body,
        state: "pending",
        createdAt: now,
      },
    };
    const queenReply: ConversationEntry = {
      id: `local-reply-${seq}`,
      kind: "message",
      at: "刚刚",
      message: {
        id: `local-reply-msg-${seq}`,
        hiveId: hive.id,
        from: "queen",
        to: "local:tok-1",
        kind: "reply",
        chainId: `chain-local-${seq}`,
        hop: 1,
        body: "收到。现在是本地假数据演示，我不会真的派活；接上 MCP 服务后，这里会显示我的判断和派活结果。",
        state: "pending",
        createdAt: now,
      },
    };
    setStream(current => [...current, localEntry]);
    setQueenThinking(true);
    replyTimer.current = window.setTimeout(() => {
      setStream(current => [...current, queenReply]);
      setQueenThinking(false);
    }, 900);
  };

  return (
    <div className="hg-conversation">
      <div className="hg-conversation-main">
        <Panel
          title="对话"
          subtitle={
            hive.queenEnabled
              ? "Queen（蜂王）在这里汇报进展、请求审批；外部事件会以卡片插入。"
              : "Queen 未开启，来自 GitHub 的事件会留在 Hive Inbox，等本地 Agent 或人处理。"
          }
        >
          <ConversationStream
            entries={stream}
            agents={agents}
            emptyTitle="对话还是空的。"
            emptyHint={hive.queenEnabled ? "在下面跟 Queen 说点什么。" : "在下面直接派一只 Forager。"}
          />
          {queenThinking ? <BeeLoader label="Queen（蜂王）正在读你的消息…" /> : null}
        </Panel>
        {hive.queenEnabled ? (
          <QueenComposer onSend={appendLocal} />
        ) : (
          <DispatchForm hive={hive} />
        )}
      </div>
      <aside className="hg-conversation-side">
        <SwarmPanel hive={hive} foragers={foragers} entries={entries} maxConcurrent={maxConcurrent} />
      </aside>
    </div>
  );
}

function QueenComposer({ onSend }: { onSend: (body: string) => void }) {
  const id = useId();
  const [text, setText] = useState("");
  const [status, setStatus] = useState("");

  return (
    <form
      className="hg-composer"
      onSubmit={event => {
        event.preventDefault();
        const body = text.trim();
        if (!body) {
          setStatus("先写下要对 Queen 说的话。");
          return;
        }
        onSend(body);
        setText("");
        setStatus("已写入本地演示会话，不会离开浏览器。");
      }}
    >
      <label className="hg-field-label" htmlFor={id}>
        对 Queen（蜂王）说话
      </label>
      <textarea
        id={id}
        rows={3}
        value={text}
        onChange={event => setText(event.target.value)}
        placeholder="例如：把这 5 个 Issue 并行修了。"
      />
      <div className="hg-composer-foot">
        <span className="hg-composer-hint">发送后 Queen 会拆成 Task 派给 Forager。</span>
        <button className="hg-btn hg-btn-primary" type="submit">
          <Icon name="send" size={16} />
          发送
        </button>
      </div>
      {status ? (
        <p className="hg-form-status" role="status">
          {status}
        </p>
      ) : null}
    </form>
  );
}

const presets = ["implementation", "review", "investigation"];

function DispatchForm({ hive }: { hive: Hive }) {
  const id = useId();
  const [task, setTask] = useState("");
  const [preset, setPreset] = useState(presets[0]);
  const [status, setStatus] = useState("");
  const [error, setError] = useState("");

  return (
    <form
      className="hg-composer"
      onSubmit={event => {
        event.preventDefault();
        if (!task.trim()) {
          setError("派活说明不能为空。");
          setStatus("");
          return;
        }
        setError("");
        setStatus(
          `已在本地演示中创建 Forager：使用 ${preset} 预设，从 main 切出 swarm/${hive.id}/forager-新 分支开始工作。`,
        );
        setTask("");
      }}
    >
      <h2 className="hg-composer-title">直接派 Forager</h2>
      <p className="hg-composer-hint">
        Queen（蜂王）未开启，任务直接派给 Forager；它的分支和进展会出现在右侧 Swarm 面板。
      </p>
      <label className="hg-field-label" htmlFor={id}>
        派活说明
      </label>
      <textarea
        id={id}
        rows={4}
        value={task}
        onChange={event => setTask(event.target.value)}
        placeholder="要修什么、验收标准是什么、有哪些注意事项。"
        aria-invalid={error ? true : undefined}
        aria-describedby={error ? `${id}-error` : undefined}
      />
      <div className="hg-composer-foot">
        <label className="hg-inline-field">
          预设
          <select value={preset} onChange={event => setPreset(event.target.value)}>
            {presets.map(option => (
              <option key={option} value={option}>
                {option}
              </option>
            ))}
          </select>
        </label>
        <button className="hg-btn hg-btn-primary" type="submit">
          <Icon name="bee" size={16} />
          派出 Forager
        </button>
      </div>
      {error ? (
        <p className="hg-form-error" id={`${id}-error`} role="alert">
          {error}
        </p>
      ) : null}
      {status ? (
        <p className="hg-form-status" role="status">
          {status}
        </p>
      ) : null}
    </form>
  );
}

function SwarmPanel({
  hive,
  foragers,
  entries,
  maxConcurrent,
}: {
  hive: Hive;
  foragers: Agent[];
  entries: ConversationEntry[];
  maxConcurrent: number;
}) {
  const [notice, setNotice] = useState("");
  const busy = foragers.filter(forager =>
    ["creating", "active", "parked"].includes(forager.state),
  ).length;

  return (
    <Panel
      title={<span className="hg-panel-title"><Icon name="swarm" size={16} />Swarm（蜂群）</span>}
      subtitle={`${busy}/${maxConcurrent} 个并发槽在用`}
      extra={
        <button
          className="hg-btn"
          type="button"
          onClick={() => setNotice("已记录：接上 MCP 服务后会真的建一只 Forager。")}
        >
          <Icon name="plus" size={16} />
          派一只 Forager
        </button>
      }
    >
      <div className="hg-queen-row">
        <QueenAvatar size={32} off={!hive.queenEnabled} />
        <div>
          <strong>Queen（蜂王）</strong>
          <p className="hg-muted">
            {hive.queenEnabled ? "常驻协调者，负责判断和派活。" : "未开启，事件留在 Hive Inbox。"}
          </p>
        </div>
      </div>

      {foragers.length === 0 ? (
        <EmptyState title="这个 Hive 还没有 Forager。" hint="派一只去干活吧。" />
      ) : (
        <ul className="hg-swarm-list">
          {foragers.map(forager => {
            const detail = foragerDetailById(forager.id);
            const content = (
              <>
                <ForagerAvatar
                  id={forager.id}
                  state={forager.state}
                  needsYou={foragerNeedsYou(forager, entries)}
                  size={32}
                />
                <div className="hg-swarm-item-main">
                  <span className="hg-swarm-id">{forager.id}</span>
                  <p className="hg-swarm-task">{forager.task}</p>
                  {forager.branch ? (
                    <code className="hg-branch">
                      <Icon name="branch" size={16} />
                      {forager.branch}
                    </code>
                  ) : null}
                </div>
                <StatusBadge state={forager.state} label={stateLabels[forager.state]} />
              </>
            );
            return (
              <li key={forager.id}>
                {detail ? (
                  <a className="hg-swarm-item is-link" href={`#/hives/${hive.id}/foragers/${forager.id}`}>
                    {content}
                  </a>
                ) : (
                  <div className="hg-swarm-item">{content}</div>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {notice ? (
        <p className="hg-form-status" role="status">
          {notice}
        </p>
      ) : null}
    </Panel>
  );
}
