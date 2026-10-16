// Shared "honey garden" building blocks for the Hive console.
import { useId, type ReactNode } from "react";
import type {
  AgentRole,
  AgentState,
  Check,
  CheckStatus,
  ExternalEvent,
  QueenAction,
} from "./types";
import { BeeIcon, BeeLineIcon, GithubMark, hexPoints, Icon } from "./icons";

export type Tone = "mint" | "amber" | "coral" | "neutral";

export const stateLabels: Record<AgentState, string> = {
  creating: "创建中",
  active: "运行中",
  parked: "待命",
  hibernated: "休眠",
  completed: "已完成",
  failed: "失败",
  cancelled: "已取消",
  disabled: "未开启",
};

export function stateTone(state: AgentState): Tone {
  switch (state) {
    case "active":
    case "completed":
      return "mint";
    case "failed":
      return "coral";
    case "creating":
      return "amber";
    default:
      return "neutral";
  }
}

const checkTone: Record<CheckStatus, Tone> = {
  passed: "mint",
  failed: "coral",
  pending: "amber",
};

const checkLabel: Record<CheckStatus, string> = {
  passed: "通过",
  failed: "未通过",
  pending: "等待",
};

function ActionMark({ kind }: { kind: QueenAction["kind"] }) {
  if (kind === "spawn_forager") return <BeeLineIcon size={16} />;
  return <Icon name={kind === "cancel_forager" ? "close" : "shield"} size={16} />;
}

export function HexAvatar({
  role,
  state,
  size = 40,
  label,
}: {
  role: AgentRole;
  state: AgentState;
  size?: number;
  label?: string;
}) {
  const name = label ?? (role === "queen" ? "Queen（蜂王）" : "Forager");
  const description = `${name} · ${stateLabels[state]}`;
  if (role === "queen") {
    return (
      <span
        className={`hg-avatar hg-avatar-queen${state === "disabled" ? " is-off" : ""}`}
        style={{ width: size, height: size }}
        role="img"
        aria-label={description}
      >
        <svg viewBox="0 0 40 40" aria-hidden="true" focusable="false">
          <polygon className="hg-avatar-hex" points={hexPoints(20, 20, 18)} />
          <path
            className="hg-avatar-crown"
            d="M13.2 25.4h13.6l1.4-7.6-4.5 2.9L20 14.2l-3.7 6.5-4.5-2.9 1.4 7.6Z"
          />
        </svg>
      </span>
    );
  }
  return (
    <span
      className={`hg-avatar hg-avatar-forager hg-tone-${stateTone(state)}`}
      style={{ width: size, height: size }}
      role="img"
      aria-label={description}
    >
      <svg viewBox="0 0 40 40" aria-hidden="true" focusable="false">
        <polygon className="hg-avatar-hex" points={hexPoints(20, 20, 17)} />
        <polygon className="hg-avatar-core" points={hexPoints(20, 20, 6)} />
      </svg>
    </span>
  );
}

export function StatusDot({ state, title }: { state: AgentState; title?: string }) {
  return (
    <span
      className={`hg-dot hg-tone-${stateTone(state)}`}
      role="img"
      aria-label={title ?? stateLabels[state]}
      title={title ?? stateLabels[state]}
    />
  );
}

export function StatusBadge({ state, label }: { state: AgentState; label?: string }) {
  return (
    <span className={`hg-badge hg-tone-${stateTone(state)}`}>
      <StatusDot state={state} />
      {label ?? stateLabels[state]}
    </span>
  );
}

export function CheckList({ checks, empty }: { checks: Check[]; empty: string }) {
  if (checks.length === 0) return <p className="hg-muted">{empty}</p>;
  return (
    <ul className="hg-checks">
      {checks.map(check => (
        <li key={check.name} className={`hg-check hg-tone-${checkTone[check.status]}`}>
          <Icon
            name={check.status === "passed" ? "check" : check.status === "failed" ? "alert" : "clock"}
            size={15}
          />
          <span className="hg-check-name">{check.name}</span>
          <span className="hg-check-state">{checkLabel[check.status]}</span>
          <span className="hg-check-detail">{check.detail}</span>
        </li>
      ))}
    </ul>
  );
}

export function Panel({
  title,
  subtitle,
  extra,
  children,
  className,
  headingLevel = 2,
}: {
  title: ReactNode;
  subtitle?: ReactNode;
  extra?: ReactNode;
  children: ReactNode;
  className?: string;
  headingLevel?: 2 | 3;
}) {
  const Heading = headingLevel === 2 ? "h2" : "h3";
  return (
    <section className={className ? `hg-panel ${className}` : "hg-panel"}>
      <header className="hg-panel-head">
        <div>
          <Heading>{title}</Heading>
          {subtitle ? <p className="hg-panel-subtitle">{subtitle}</p> : null}
        </div>
        {extra ? <div className="hg-panel-extra">{extra}</div> : null}
      </header>
      <div className="hg-panel-body">{children}</div>
    </section>
  );
}

export function ActionCard({ action }: { action: QueenAction }) {
  return (
    <article className={`hg-card hg-action hg-action-${action.kind}`}>
      <header className="hg-card-head">
        <span className="hg-card-mark">
          <ActionMark kind={action.kind} />
        </span>
        <div className="hg-card-title">
          <strong>{action.title}</strong>
          <p>{action.summary}</p>
        </div>
        <time className="hg-time">{action.at}</time>
      </header>
      <details className="hg-card-details">
        <summary>查看细节</summary>
        <dl>
          {action.details.map(detail => (
            <div key={detail.label}>
              <dt>{detail.label}</dt>
              <dd>{detail.value}</dd>
            </div>
          ))}
        </dl>
        {action.artifact ? (
          <p className="hg-artifact">
            <Icon name={action.artifact.kind === "pr" ? "pr" : "branch"} size={14} />
            <code>{action.artifact.ref}</code>
          </p>
        ) : null}
      </details>
    </article>
  );
}

export function EventCard({ event }: { event: ExternalEvent }) {
  return (
    <article className="hg-card hg-event">
      <header className="hg-card-head">
        <span className="hg-card-source">
          <GithubMark size={15} />
          GitHub
        </span>
        <span className="hg-muted hg-event-kind">
          {event.event} · {event.action}
        </span>
        <time className="hg-time">{event.at}</time>
      </header>
      <a className="hg-event-title" href={event.url} target="_blank" rel="noreferrer">
        {event.title}
        <Icon name="external" size={14} />
      </a>
      <blockquote className="hg-excerpt">
        <span className="hg-excerpt-tag">外部内容</span>
        {event.excerpt}
      </blockquote>
    </article>
  );
}

export function EmptyState({
  title,
  hint,
  action,
}: {
  title: string;
  hint?: string;
  action?: ReactNode;
}) {
  return (
    <div className="hg-empty">
      <HoneycombBackdrop />
      <div className="hg-empty-body">
        <span className="hg-empty-bee" aria-hidden="true">
          <BeeIcon size={32} />
        </span>
        <p className="hg-empty-title">{title}</p>
        {hint ? <p className="hg-empty-hint">{hint}</p> : null}
        {action ? <div className="hg-empty-action">{action}</div> : null}
      </div>
    </div>
  );
}

export function HoneycombBackdrop() {
  const id = useId();
  const r = 16;
  const width = Math.sqrt(3) * r;
  const height = 3 * r;
  // Four cells per tile close the pointy-top honeycomb lattice when repeated.
  const centers: [number, number][] = [
    [0, r],
    [width, r],
    [width / 2, -r / 2],
    [width / 2, 2.5 * r],
  ];
  return (
    <svg className="hg-comb" aria-hidden="true" focusable="false">
      <defs>
        <pattern id={id} width={width} height={height} patternUnits="userSpaceOnUse">
          {centers.map(([cx, cy]) => (
            <polygon key={`${cx}-${cy}`} points={hexPoints(cx, cy, r)} />
          ))}
        </pattern>
      </defs>
      <rect width="100%" height="100%" fill={`url(#${id})`} />
    </svg>
  );
}

export function BeeLoader({ label = "正在飞行…" }: { label?: string }) {
  return (
    <div className="hg-loader" role="status" aria-live="polite">
      <span className="hg-loader-track" aria-hidden="true">
        <svg viewBox="0 0 80 80" focusable="false">
          <polygon points={hexPoints(40, 40, 30)} />
        </svg>
        <span className="hg-loader-bee">
          <BeeIcon size={24} />
        </span>
      </span>
      <span className="hg-loader-label">{label}</span>
    </div>
  );
}

export function Tabs({
  items,
  active,
  label,
}: {
  items: { id: string; label: string; href: string; count?: number }[];
  active: string;
  label: string;
}) {
  return (
    <nav className="hg-tabs" aria-label={label}>
      {items.map(item => (
        <a
          key={item.id}
          href={item.href}
          className={item.id === active ? "is-active" : undefined}
          aria-current={item.id === active ? "page" : undefined}
        >
          {item.label}
          {typeof item.count === "number" ? <span className="hg-tab-count">{item.count}</span> : null}
        </a>
      ))}
    </nav>
  );
}
