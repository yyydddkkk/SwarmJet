// Shared "honey garden" building blocks for the Hive console.
import {
  useEffect,
  useId,
  useRef,
  useState,
  type ReactNode,
} from "react";
import type {
  AgentState,
  AttestResult,
  Check,
  CheckStatus,
  ExternalEvent,
  MessageTrust,
  QueenAction,
} from "./types";
import { GithubMark, Icon } from "./icons";
import { EmptyHiveArt, ForagerAvatar, LoaderBee } from "./pixel";

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
  if (kind === "spawn_forager") return <Icon name="bee" size={16} />;
  return <Icon name={kind === "cancel_forager" ? "close" : "shield"} size={16} />;
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
            size={16}
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
            <Icon name={action.artifact.kind === "pr" ? "pr" : "branch"} size={16} />
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
          <GithubMark size={16} />
          GitHub
        </span>
        <span className="hg-muted hg-event-kind">
          {event.event} · {event.action}
        </span>
        <time className="hg-time">{event.at}</time>
      </header>
      <a className="hg-event-title" href={event.url} target="_blank" rel="noreferrer">
        {event.title}
        <Icon name="external" size={16} />
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
      <div className="hg-empty-body">
        <span className="hg-empty-art" aria-hidden="true">
          <EmptyHiveArt />
        </span>
        <p className="hg-empty-title">{title}</p>
        {hint ? <p className="hg-empty-hint">{hint}</p> : null}
        {action ? <div className="hg-empty-action">{action}</div> : null}
      </div>
    </div>
  );
}

/** Readable failure surface for a failed read or write. */
export function ErrorNotice({
  message,
  onRetry,
  auth = false,
  admin = false,
}: {
  message: string;
  onRetry?: () => void;
  auth?: boolean;
  admin?: boolean;
}) {
  return (
    <div className="hg-error" role="alert">
      <div className="hg-error-text">
        <strong>{admin ? "需要平台管理员权限。" : "请求没有成功。"}</strong>
        <p>{message}</p>
      </div>
      <div className="hg-error-actions">
        {auth ? (
          <a className="hg-btn" href="/">
            去登录
          </a>
        ) : null}
        {admin ? (
          <a className="hg-btn" href="/">
            返回主控制台
          </a>
        ) : null}
        {onRetry ? (
          <button className="hg-btn" type="button" onClick={onRetry}>
            <Icon name="refresh" size={16} />
            重试
          </button>
        ) : null}
      </div>
    </div>
  );
}

/**
 * Pixel loader: the 32-grid bee in two wing positions, flipped by CSS `steps()`
 * with a 2px bob on top. `prefers-reduced-motion` parks it on the first frame.
 */
export function BeeLoader({ label = "正在飞行…" }: { label?: string }) {
  return (
    <div className="hg-loader" role="status" aria-live="polite">
      <span className="hg-loader-bee" aria-hidden="true">
        <span className="hg-loader-frame hg-loader-frame-up">
          <LoaderBee frame="up" size={32} />
        </span>
        <span className="hg-loader-frame hg-loader-frame-down">
          <LoaderBee frame="down" size={32} />
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

/** Native modal used by the onboarding dialogs, styled like a pixel panel. */
export function Dialog({
  title,
  children,
  onClose,
  wide = false,
}: {
  title: string;
  children: ReactNode;
  onClose: () => void;
  wide?: boolean;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    if (!dialog.open) dialog.showModal();
    return () => dialog.close();
  }, []);
  return (
    <dialog
      ref={ref}
      className={wide ? "hg-dialog hg-dialog-wide" : "hg-dialog"}
      aria-labelledby={titleId}
      onCancel={event => {
        event.preventDefault();
        onClose();
      }}
    >
      <header className="hg-dialog-head">
        <h2 id={titleId}>{title}</h2>
        <button className="hg-dialog-close" type="button" onClick={onClose} aria-label="关闭对话框">
          <Icon name="close" size={16} />
        </button>
      </header>
      <div className="hg-dialog-body">{children}</div>
    </dialog>
  );
}

export function ConfirmDialog({
  title,
  confirmLabel,
  danger = false,
  busy = false,
  error = "",
  children,
  onConfirm,
  onCancel,
}: {
  title: string;
  confirmLabel: string;
  danger?: boolean;
  busy?: boolean;
  error?: string;
  children: ReactNode;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <Dialog title={title} onClose={onCancel}>
      <div className="hg-confirm-copy">{children}</div>
      {error ? (
        <p className="hg-form-error" role="alert">
          {error}
        </p>
      ) : null}
      <div className="hg-dialog-actions">
        <button className="hg-btn" type="button" onClick={onCancel} disabled={busy}>
          取消
        </button>
        <button
          className={danger ? "hg-btn hg-btn-danger" : "hg-btn hg-btn-primary"}
          type="button"
          onClick={onConfirm}
          disabled={busy}
        >
          {busy ? "正在处理…" : confirmLabel}
        </button>
      </div>
    </Dialog>
  );
}

/** Copy-to-clipboard button with an inline (testable) result. */
export function CopyButton({
  text,
  label = "复制",
  ariaLabel,
}: {
  text: string;
  label?: string;
  ariaLabel?: string;
}) {
  const [result, setResult] = useState("");
  useEffect(() => {
    if (!result) return;
    const timer = window.setTimeout(() => setResult(""), 2500);
    return () => window.clearTimeout(timer);
  }, [result]);
  return (
    <span className="hg-copy">
      <button
        className="hg-btn hg-btn-small"
        type="button"
        aria-label={ariaLabel ?? label}
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(text);
            setResult("已复制");
          } catch {
            setResult("复制失败，请手动选择");
          }
        }}
      >
        {label}
      </button>
      {result ? (
        <span className="hg-copy-result" role="status">
          {result}
        </span>
      ) : null}
    </span>
  );
}

/** A labelled read-only value with its own copy button. */
export function CopyField({
  label,
  text,
  mono = true,
}: {
  label: string;
  text: string;
  mono?: boolean;
}) {
  return (
    <div className="hg-copy-field">
      <div className="hg-copy-field-head">
        <span>{label}</span>
        <CopyButton text={text} ariaLabel={`复制 ${label}`} />
      </div>
      <pre className={mono ? "hg-copy-value" : undefined}>{text}</pre>
    </div>
  );
}

/** Quiet inline explanation, used for "not enabled" states. */
export function Callout({
  tone = "neutral",
  title,
  children,
}: {
  tone?: "neutral" | "mint" | "amber" | "coral";
  title?: string;
  children: ReactNode;
}) {
  return (
    <div className={`hg-callout hg-callout-${tone}`}>
      {title ? <strong>{title}</strong> : null}
      <div>{children}</div>
    </div>
  );
}

/** Definition rows used by the setup and model pages. */
export function StatusGrid({ children }: { children: ReactNode }) {
  return <dl className="hg-status-grid">{children}</dl>;
}

export function StatusItem({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <div>
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

/** Provenance marker for a message (round-4 contract section 5). */
export const trustLabels: Record<MessageTrust, string> = {
  user: "用户",
  agent: "Agent",
  external: "外部",
  system: "系统",
};

export function TrustBadge({ trust }: { trust: MessageTrust }) {
  return (
    <span className={`hg-trust hg-trust-${trust}`} title={`来源：${trustLabels[trust]}`}>
      {trust === "external" ? <Icon name="alert" size={16} /> : null}
      {trustLabels[trust]}
    </span>
  );
}

const attestLabels: Record<AttestResult, string> = {
  pass: "PASS",
  pass_notes: "PASS+NOTES",
  fail: "FAIL",
  blocked: "BLOCKED",
};

const attestTones: Record<AttestResult, Tone> = {
  pass: "mint",
  pass_notes: "amber",
  fail: "coral",
  blocked: "neutral",
};

export function AttestBadge({ result }: { result: AttestResult }) {
  return (
    <span className={`hg-badge hg-tone-${attestTones[result] ?? "neutral"}`}>
      {attestLabels[result] ?? result}
    </span>
  );
}

/** Horizontal spend bar; the track makes small amounts readable next to big ones. */
export function UsageBar({ value, max }: { value: number; max: number }) {
  const width = max > 0 ? Math.max(2, Math.round((value / max) * 100)) : 0;
  return (
    <span className="hg-usage-bar" aria-hidden="true">
      <span className="hg-usage-bar-fill" style={{ width: `${width}%` }} />
    </span>
  );
}
