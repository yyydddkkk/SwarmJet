// Conversation stream: Queen prose, structured action cards and external event cards.
import type { Agent, AgentState, ConversationEntry, Message, MessageKind } from "./types";
import { ActionCard, EmptyState, EventCard, HexAvatar } from "./components";
import { Icon } from "./icons";

const kindLabels: Record<MessageKind, string> = {
  event: "事件",
  task: "派活",
  report: "汇报",
  question: "提问",
  reply: "回复",
  control: "控制",
};

function MessageRow({ message, agents, at }: { message: Message; agents: Agent[]; at: string }) {
  const from = message.from;
  const isLocal = from.startsWith("local:");
  const isQueen = from === "queen";
  const droneId = from.startsWith("drone:") ? from.slice("drone:".length) : "";
  const agent = droneId ? agents.find(candidate => candidate.id === droneId) : undefined;
  const state: AgentState = agent?.state ?? "active";

  if (isLocal) {
    return (
      <div className="hg-msg hg-msg-local">
        <div className="hg-msg-meta">
          <span className="hg-msg-author">你（本地 Agent）</span>
          <span className="hg-msg-kind">{kindLabels[message.kind]}</span>
          <time className="hg-time">{at}</time>
        </div>
        <p className="hg-msg-body">{message.body}</p>
      </div>
    );
  }

  const author = isQueen ? "Queen（蜂王）" : `Drone（工蜂）· ${droneId}`;
  return (
    <div className={`hg-msg ${isQueen ? "hg-msg-queen" : "hg-msg-drone"}`}>
      <HexAvatar role={isQueen ? "queen" : "drone"} state={state} size={34} />
      <div className="hg-msg-main">
        <div className="hg-msg-meta">
          <span className="hg-msg-author">{author}</span>
          <span className="hg-msg-kind">{kindLabels[message.kind]}</span>
          {message.hop > 0 ? <span className="hg-msg-hop">第 {message.hop} 跳</span> : null}
          <time className="hg-time">{at}</time>
        </div>
        <p className="hg-msg-body">{message.body}</p>
        {message.artifacts?.length ? (
          <ul className="hg-artifacts">
            {message.artifacts.map(artifact => (
              <li key={`${artifact.kind}-${artifact.ref}`}>
                <Icon name={artifact.kind === "pr" ? "pr" : artifact.kind === "commit" ? "check" : "branch"} size={13} />
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
              <MessageRow message={entry.message} agents={agents} at={entry.at} />
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
