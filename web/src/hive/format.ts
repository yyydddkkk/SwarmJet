// Message helpers for the Hive console: the API returns domain.Message rows in
// insertion order, but the conversation view merges two inboxes (Queen and the
// signed-in user), so it has to merge and de-duplicate by id.
import type { ConversationEntry, Message, MessageTrust } from "./types";

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

const dateTimeFormat = new Intl.DateTimeFormat("zh-CN", {
  year: "numeric",
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
});

export function formatDateTime(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return dateTimeFormat.format(date);
}

/**
 * Round-4 messages carry their provenance in `trust`; rows written before the
 * field existed default to "agent". The sender prefixes are only a fallback,
 * never a replacement for the stored value.
 */
export function messageTrust(message: Message): MessageTrust | string {
  if (message.trust) return message.trust;
  if (message.from.startsWith("local:")) return "user";
  if (message.from === "system" || message.from === "hive") return "system";
  return "agent";
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

/** Short commit display: seven hex characters, with the full SHA in a title. */
export function shortSha(sha: string | undefined): string {
  if (!sha) return "—";
  return sha.slice(0, 7);
}

const usdFormat = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
  minimumFractionDigits: 2,
  maximumFractionDigits: 4,
});

export function formatUsd(value: number | undefined): string {
  if (value === undefined || Number.isNaN(value)) return "—";
  return usdFormat.format(value);
}

const numberFormat = new Intl.NumberFormat("en-US");

export function formatTokens(value: number | undefined): string {
  if (value === undefined || Number.isNaN(value)) return "—";
  return numberFormat.format(value);
}
