// Conversation stream: Queen prose, structured action cards and external event cards.
import type { Agent, AgentState, ConversationEntry, Message, MessageKind } from "./types";
import { ActionCard, EmptyState, EventCard, TrustBadge } from "./components";
import { ForagerAvatar, QueenAvatar, foragerNeedsYou } from "./pixel";
import { Icon } from "./icons";
import { messageTrust } from "./format";

const kindLabels: Record<MessageKind, string> = {
  event: "事件",
  task: "派活",
  report: "汇报",
  question: "提问",
  reply: "回复",
  control: "控制",
};

function MessageRow({
  message,
  agents,
  entries,
  at,
}: {
  message: Message;
  agents: Agent[];
  entries: ConversationEntry[];
  at: string;
}) {
  const from = message.from;
  const trust = messageTrust(message);
  const external = trust === "external";
  const isLocal = from.startsWith("local:");
  const isQueen = from === "queen";
  const isSystem = from === "system" || from === "hive";
  const foragerId = from.startsWith("forager:") ? from.slice("forager:".length) : "";
  const agent = foragerId ? agents.find(candidate => candidate.id === foragerId) : undefined;
  const state: AgentState = agent?.state ?? "active";

  if (isSystem && !external) {
    return (
      <div className="hg-msg hg-msg-system">
        <span className="hg-msg-system-mark" aria-hidden="true">
          <Icon name="inbox" size={16} />
        </span>
        <div className="hg-msg-main">
          <div className="hg-msg-meta">
            <span className="hg-msg-author">系统事件</span>
            <TrustBadge trust={trust} />
            <span className="hg-msg-kind">{kindLabels[message.kind]}</span>
            <time className="hg-time">{at}</time>
          </div>
          <p className="hg-msg-body">{message.body}</p>
        </div>
      </div>
    );
  }

  if (isLocal) {
    return (
      <div className="hg-msg hg-msg-local">
        <div className="hg-msg-meta">
          <span className="hg-msg-author">{from.startsWith("local:web-") ? "你（网页）" : "你（本地 Agent）"}</span>
          <TrustBadge trust={trust} />
          <span className="hg-msg-kind">{kindLabels[message.kind]}</span>
          <time className="hg-time">{at}</time>
        </div>
        <p className="hg-msg-body">{message.body}</p>
      </div>
    );
  }

  const author = isQueen
    ? "Queen（蜂王）"
    : foragerId
      ? `Forager · ${foragerId}`
      : external
        ? "GitHub 事件"
        : from;
  const className = external
    ? "hg-msg hg-msg-external"
    : `hg-msg ${isQueen ? "hg-msg-queen" : "hg-msg-forager"}`;
  return (
    <div className={className}>
      {isQueen ? (
        <QueenAvatar size={32} off={state === "disabled"} />
      ) : foragerId ? (
        <ForagerAvatar
          id={foragerId}
          state={state}
          needsYou={agent ? foragerNeedsYou(agent, entries) : false}
          size={32}
        />
      ) : (
        <span className="hg-msg-system-mark" aria-hidden="true">
          <Icon name={external ? "alert" : "user"} size={16} />
        </span>
      )}
      <div className="hg-msg-main">
        <div className="hg-msg-meta">
          <span className="hg-msg-author">{author}</span>
          <TrustBadge trust={trust} />
          <span className="hg-msg-kind">{kindLabels[message.kind]}</span>
          {message.hop > 0 ? <span className="hg-msg-hop">第 {message.hop} 跳</span> : null}
          <time className="hg-time">{at}</time>
        </div>
        <p className="hg-msg-body">{message.body}</p>
        {external ? <p className="hg-msg-trust-note">外部内容，未经核实</p> : null}
        {message.artifacts?.length ? (
          <ul className="hg-artifacts">
            {message.artifacts.map(artifact => (
              <li key={`${artifact.kind}-${artifact.ref}`}>
                <Icon name={artifact.kind === "pr" ? "pr" : artifact.kind === "commit" ? "check" : "branch"} size={16} />
                <code>{artifact.ref}</code>
              </li>
            ))}
          </ul>
        ) : null}
      </div>
    </div>
  );
}

export function ConversationStream({
  entries,
  agents = [],
  emptyTitle = "还没有对话。",
  emptyHint,
}: {
  entries: ConversationEntry[];
  agents?: Agent[];
  emptyTitle?: string;
  emptyHint?: string;
}) {
  if (entries.length === 0) return <EmptyState title={emptyTitle} hint={emptyHint} />;
  return (
    <ol className="hg-stream">
      {entries.map(entry => {
        if (entry.kind === "message")
          return (
            <li key={entry.id}>
              <MessageRow message={entry.message} agents={agents} entries={entries} at={entry.at} />
            </li>
          );
        if (entry.kind === "action")
          return (
            <li key={entry.id}>
              <ActionCard action={entry.action} />
            </li>
          );
        return (
          <li key={entry.id}>
            <EventCard event={entry.event} />
          </li>
        );
      })}
    </ol>
  );
}
