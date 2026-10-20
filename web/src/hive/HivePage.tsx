// Hive page: conversation with the Queen, the Swarm rail, the Inbox, the Hive
// methodology and spend, all backed by GET /api/hives/{id} and its endpoints.
// The conversation and the Swarm rail poll every five seconds (ADR section 18).
import { useId, useState } from "react";
import { hiveAPI } from "./api";
import type { Agent, ConversationEntry, Hive } from "./types";
import {
  BeeLoader,
  EmptyState,
  ErrorNotice,
  Panel,
  StatusBadge,
  Tabs,
  stateLabels,
} from "./components";
import { Icon } from "./icons";
import { ForagerAvatar, QueenAvatar, foragerNeedsYou } from "./pixel";
import { ConversationStream } from "./ConversationStream";
import HiveSettings from "./HiveSettings";
import MethodologySection from "./MethodologyPage";
import { HiveUsagePanel } from "./UsagePanel";
import { formatDateTime, isUnhandled, messageEntries, mergeMessages } from "./format";
import { useResource } from "./use-resource";

const sections = [
  { id: "conversation", label: "对话" },
  { id: "swarm", label: "Swarm" },
  { id: "methodology", label: "方法论" },
  { id: "usage", label: "用量" },
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
  memory: {
    title: "记忆",
    hint: "这里会放 Situation、Queen 的私有记忆、Dream 报告和待合并的记忆 PR。这一轮先放占位。",
  },
  settings: {
    title: "设置",
    hint: "仓库、Queen 开关、触发规则与上限。模型凭证在顶部的「模型」页，方法论在同级「方法论」分区。",
  },
};

export default function HivePage({ hiveId, section }: { hiveId: string; section: string }) {
  const [dispatchOpen, setDispatchOpen] = useState(false);
  const hiveRes = useResource(signal => hiveAPI.getHive(hiveId, signal), [hiveId]);
  const queenRes = useResource(signal => hiveAPI.getQueen(hiveId, signal), [hiveId], 5000);
  const localAddress = hiveRes.data?.localAddress ?? "";
  const agentsRes = useResource(
    signal => hiveAPI.listForagers(hiveId, signal),
    [hiveId],
    5000,
  );
  const conversationRes = useResource(
    async signal => {
      if (!localAddress) return [];
      const [toQueen, toLocal] = await Promise.all([
        hiveAPI.listMessages(hiveId, { to: "queen" }, signal),
        hiveAPI.listMessages(hiveId, { to: localAddress }, signal),
      ]);
      return messageEntries(mergeMessages(toQueen.messages, toLocal.messages));
    },
    [hiveId, localAddress],
    5000,
  );
  const inboxRes = useResource(
    signal => hiveAPI.listMessages(hiveId, { to: "hive" }, signal),
    [hiveId],
    5000,
  );

  if (hiveRes.loading && !hiveRes.data) {
    return (
      <div className="hg-page">
        <BeeLoader label="正在读取 Hive…" />
      </div>
    );
  }
  if (hiveRes.errorStatus === 404) {
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
  if (!hiveRes.data) {
    return (
      <div className="hg-page">
        <ErrorNotice
          message={hiveRes.error}
          onRetry={hiveRes.reload}
          auth={hiveRes.errorStatus === 401}
          admin={hiveRes.errorStatus === 403}
        />
      </div>
    );
  }

  const { hive } = hiveRes.data;
  const queenStatus = queenRes.data;
  const queenEnabled = queenStatus?.enabled ?? hive.queenEnabled;
  const queen = agentsRes.data?.queen ? {
    ...agentsRes.data.queen,
    state: queenStatus?.state ?? agentsRes.data.queen.state,
  } : undefined;
  const foragers = agentsRes.data?.foragers ?? [];
  const entries = conversationRes.data ?? [];
  const inboxMessages = inboxRes.data?.messages ?? [];
  const inboxEntries = messageEntries(inboxMessages);
  const inboxBacklog = inboxMessages.filter(isUnhandled).length;
  const active = section || "conversation";
  const refreshAfterSend = () => {
    conversationRes.reload();
    inboxRes.reload();
  };
  const refreshAfterSpawn = () => {
    agentsRes.reload();
    conversationRes.reload();
  };
  const refreshAfterCancel = () => {
    agentsRes.reload();
    inboxRes.reload();
  };

  const tabs = sections.map(item => ({
    ...item,
    href: item.id === "conversation" ? `#/hives/${hive.id}` : `#/hives/${hive.id}/${item.id}`,
    count: item.id === "swarm" ? foragers.length : item.id === "inbox" ? inboxBacklog : undefined,
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
            state={queenStatus?.state ?? "disabled"}
            label={`Queen（蜂王）${queenEnabled ? "已开启" : "未开启"}`}
          />
          <span className="hg-badge hg-tone-neutral">{foragers.length} 只 Forager</span>
        </div>
      </header>

      {queenRes.error ? <ErrorNotice message={queenRes.error} onRetry={queenRes.reload} auth={queenRes.errorStatus === 401} admin={queenRes.errorStatus === 403} /> : null}
      {queenStatus ? <p className="hg-muted" role="status">Queen 状态：{stateLabels[queenStatus.state]} · 模型：{queenStatus.model || "部署默认模型"}{queenStatus.lastTurnAt ? ` · 最近运行：${formatDateTime(queenStatus.lastTurnAt)}` : ""}</p> : null}
      <Tabs items={tabs} active={active} label={`${hive.name} 分区`} />

      {active === "conversation" ? (
        <div className="hg-conversation">
          <div className="hg-conversation-main">
            <Panel
              title="对话"
              subtitle={
                queenEnabled
                  ? "Queen（蜂王）在这里汇报进展、请求审批；外部事件会以消息卡片插入。"
                  : "Queen 未开启，来自 GitHub 的事件会留在 Hive Inbox，等本地 Agent 或人处理。"
              }
            >
              {conversationRes.error ? (
                <ErrorNotice
                  message={conversationRes.error}
                  onRetry={conversationRes.reload}
                  auth={conversationRes.errorStatus === 401}
                  admin={conversationRes.errorStatus === 403}
                />
              ) : null}
              <ConversationStream
                entries={entries}
                agents={foragers}
                emptyTitle="对话还是空的。"
                emptyHint={
                  queenEnabled
                    ? "在下面跟 Queen 说点什么。"
                    : "在下面直接派一只 Forager。"
                }
              />
            </Panel>
            {queenEnabled ? (
              <QueenComposer hiveId={hive.id} onSent={refreshAfterSend} />
            ) : null}
            {!queenEnabled ? <EmptyState title="Queen 未开启" hint="打开 Queen 后，就可以在这里对话并选择方法论。" action={<a className="hg-btn hg-btn-primary" href={`#/hives/${hive.id}/settings`}>去设置页开启 Queen</a>} /> : null}
            {!queenEnabled || dispatchOpen ? (
              <DispatchForm
                hive={hive}
                onSpawned={refreshAfterSpawn}
                onClose={queenEnabled ? () => setDispatchOpen(false) : undefined}
              />
            ) : null}
          </div>
          <aside className="hg-conversation-side">
            <SwarmPanel
              hive={{ ...hive, queenEnabled }}
              queen={queen}
              foragers={foragers}
              entries={entries}
              maxConcurrent={hive.config.maxConcurrentForagers}
              onSpawnRequest={() => setDispatchOpen(true)}
              onCancelled={refreshAfterCancel}
            />
          </aside>
        </div>
      ) : active === "swarm" ? (
        <div className="hg-conversation single">
          <SwarmPanel
            hive={{ ...hive, queenEnabled }}
            queen={queen}
            foragers={foragers}
            entries={entries}
            maxConcurrent={hive.config.maxConcurrentForagers}
            onSpawnRequest={() => setDispatchOpen(true)}
            onCancelled={refreshAfterCancel}
          />
          {dispatchOpen ? (
            <DispatchForm hive={hive} onSpawned={refreshAfterSpawn} onClose={() => setDispatchOpen(false)} />
          ) : null}
        </div>
      ) : active === "methodology" ? (
        <MethodologySection hiveId={hive.id} />
      ) : active === "settings" ? (
        <HiveSettings key={hive.id} hive={hive} onSaved={() => { hiveRes.reload(); queenRes.reload(); agentsRes.reload(); }} />
      ) : active === "usage" ? (
        <HiveUsagePanel hiveId={hive.id} agents={queen ? [queen, ...foragers] : foragers} />
      ) : active === "inbox" ? (
        <Panel
          title="Inbox（蜂巢收件箱）"
          subtitle="无法确定性路由的事件会留在这里，等 Queen 或人来处理。"
        >
          {inboxRes.error ? (
            <ErrorNotice
              message={inboxRes.error}
              onRetry={inboxRes.reload}
              auth={inboxRes.errorStatus === 401}
              admin={inboxRes.errorStatus === 403}
            />
          ) : null}
          <ConversationStream
            entries={inboxEntries}
            agents={foragers}
            emptyTitle="Inbox 是空的。"
            emptyHint="没有待处理的外部事件。"
          />
        </Panel>
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

function QueenComposer({ hiveId, onSent }: { hiveId: string; onSent: () => void }) {
  const id = useId();
  const [text, setText] = useState("");
  const [status, setStatus] = useState("");
  const [error, setError] = useState("");
  const [sending, setSending] = useState(false);

  return (
    <form
      className="hg-composer"
      onSubmit={event => {
        event.preventDefault();
        const body = text.trim();
        if (!body) {
          setError("先写下要对 Queen 说的话。");
          setStatus("");
          return;
        }
        setSending(true);
        setError("");
        setStatus("");
        hiveAPI
          .sendMessage(hiveId, { to: "queen", body })
          .then(() => {
            setText("");
            setStatus("已发送给 Queen（蜂王）。");
            onSent();
          })
          .catch(cause => setError(cause instanceof Error ? cause.message : String(cause)))
          .finally(() => setSending(false));
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
        <span className="hg-composer-hint">消息会写进 Inbox，由 Queen 的下一轮处理。</span>
        <button className="hg-btn hg-btn-primary" type="submit" disabled={sending}>
          <Icon name="send" size={16} />
          {sending ? "发送中…" : "发送"}
        </button>
      </div>
      {error ? (
        <p className="hg-form-error" role="alert">
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

const presets = ["implementation", "review", "investigation"];

function DispatchForm({
  hive,
  onSpawned,
  onClose,
}: {
  hive: Hive;
  onSpawned: () => void;
  onClose?: () => void;
}) {
  const id = useId();
  const [task, setTask] = useState("");
  const [preset, setPreset] = useState(presets[0]);
  const [status, setStatus] = useState("");
  const [error, setError] = useState("");
  const [sending, setSending] = useState(false);

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
        setSending(true);
        setError("");
        setStatus("");
        hiveAPI
          .spawnForager(hive.id, { task: task.trim(), preset })
          .then(result => {
            setTask("");
            setStatus(`已派出 ${result.forager.id}，使用 ${preset} 预设。`);
            onSpawned();
          })
          .catch(cause => setError(cause instanceof Error ? cause.message : String(cause)))
          .finally(() => setSending(false));
      }}
    >
      <div className="hg-composer-head">
        <h2 className="hg-composer-title">直接派 Forager</h2>
        {onClose ? (
          <button className="hg-btn" type="button" onClick={onClose}>
            <Icon name="close" size={16} />
            收起
          </button>
        ) : null}
      </div>
      <p className="hg-composer-hint">
        Queen（蜂王）未开启或你想绕过它时，任务直接派给 Forager；它的分支和进展会出现在 Swarm 面板。
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
        <button className="hg-btn hg-btn-primary" type="submit" disabled={sending}>
          <Icon name="bee" size={16} />
          {sending ? "派出中…" : "派出 Forager"}
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
  queen,
  foragers,
  entries,
  maxConcurrent,
  onSpawnRequest,
  onCancelled,
}: {
  hive: Hive;
  queen: Agent | undefined;
  foragers: Agent[];
  entries: ConversationEntry[];
  maxConcurrent: number;
  onSpawnRequest: () => void;
  onCancelled: () => void;
}) {
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [cancelling, setCancelling] = useState("");
  const busy = foragers.filter(forager =>
    ["creating", "active", "parked"].includes(forager.state),
  ).length;

  const cancel = (forager: Agent) => {
    setCancelling(forager.id);
    setNotice("");
    setError("");
    hiveAPI
      .cancelForager(forager.id)
      .then(() => {
        setNotice(`已向 ${forager.id} 发送取消指令，等监管者处理。`);
        onCancelled();
      })
      .catch(cause => setError(cause instanceof Error ? cause.message : String(cause)))
      .finally(() => setCancelling(""));
  };

  return (
    <Panel
      title={
        <span className="hg-panel-title">
          <Icon name="swarm" size={16} />
          Swarm（蜂群）
        </span>
      }
      subtitle={`${busy}/${maxConcurrent} 个并发槽在用`}
      extra={
        <button className="hg-btn" type="button" onClick={onSpawnRequest}>
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
            {hive.queenEnabled
              ? `常驻协调者，负责判断和派活${queen ? `（${stateLabels[queen.state]}）` : ""}。`
              : "未开启，事件留在 Hive Inbox。"}
          </p>
        </div>
      </div>

      {foragers.length === 0 ? (
        <EmptyState title="这个 Hive 还没有 Forager。" hint="派一只去干活吧。" />
      ) : (
        <ul className="hg-swarm-list">
          {foragers.map(forager => {
            const canCancel = !["completed", "failed", "cancelled"].includes(forager.state);
            return (
              <li key={forager.id}>
                <div className="hg-swarm-item">
                  <a className="hg-swarm-item-link" href={`#/hives/${hive.id}/foragers/${forager.id}`}>
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
                  </a>
                  <div className="hg-swarm-item-foot">
                    <StatusBadge state={forager.state} label={stateLabels[forager.state]} />
                    {canCancel ? (
                      <button
                        className="hg-btn hg-btn-danger"
                        type="button"
                        onClick={() => cancel(forager)}
                        disabled={cancelling === forager.id}
                      >
                        {cancelling === forager.id ? "取消中…" : "取消"}
                      </button>
                    ) : null}
                  </div>
                </div>
              </li>
            );
          })}
        </ul>
      )}
      {error ? (
        <p className="hg-form-error" role="alert">
          {error}
        </p>
      ) : null}
      {notice ? (
        <p className="hg-form-status" role="status">
          {notice}
        </p>
      ) : null}
    </Panel>
  );
}
