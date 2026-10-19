// Message helpers for the Hive console: the API returns domain.Message rows in
// insertion order, but the conversation view merges two inboxes (Queen and the
// signed-in user), so it has to merge and de-duplicate by id.
import type { ConversationEntry, Message } from "./types";

export function mergeMessages(...lists: Message[][]): Message[] {
  const byID = new Map<string, Message>();
  for (const list of lists) {
    for (const message of list) {
      if (!byID.has(message.id)) byID.set(message.id, message);
    }
  }
  return [...byID.values()].sort((left, right) => {
    if (left.createdAt !== right.createdAt) return left.createdAt.localeCompare(right.createdAt);
    return left.id.localeCompare(right.id);
  });
}

const timestampFormat = new Intl.DateTimeFormat("zh-CN", {
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
});

export function formatTimestamp(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return timestampFormat.format(date);
}

export function messageEntries(messages: readonly Message[]): ConversationEntry[] {
  return messages.map(message => ({
    id: message.id,
    kind: "message" as const,
    at: formatTimestamp(message.createdAt),
    message,
  }));
}

export function isUnhandled(message: Message): boolean {
  return message.state !== "handled";
}
