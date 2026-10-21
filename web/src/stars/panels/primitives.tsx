import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { shouldCollapseLongText } from "../../data";
export function useFocus(active = true) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!active) return;
    const previous = document.activeElement as HTMLElement | null;
    const node = ref.current;
    node
      ?.querySelector<HTMLElement>(
        'button,input,select,textarea,[tabindex="0"]',
      )
      ?.focus();
    const trap = (e: KeyboardEvent) => {
      if (e.key !== "Tab" || !node || node.querySelector('[aria-modal="true"]'))
        return;
      const items = [
        ...node.querySelectorAll<HTMLElement>(
          'button:not(:disabled),a[href],input:not(:disabled),select:not(:disabled),textarea:not(:disabled),[tabindex="0"]',
        ),
      ].filter((el) => el.getClientRects().length);
      const first = items[0],
        last = items.at(-1);
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last?.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first?.focus();
      }
    };
    node?.addEventListener("keydown", trap);
    return () => {
      node?.removeEventListener("keydown", trap);
      if (previous?.isConnected && (node?.contains(document.activeElement) || document.activeElement === document.body)) previous.focus();
    };
  }, [active]);
  return ref;
}
export function Panel({
  title,
  subtitle,
  extra,
  children,
}: {
  title: ReactNode;
  subtitle?: ReactNode;
  extra?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <section className="pc-section">
      <header>
        <h3>{title}</h3>
        {extra}
      </header>
      {subtitle && <p className="pc-muted">{subtitle}</p>}
      {children}
    </section>
  );
}
export function BeeLoader({ label }: { label?: string }) {
  return <p role="status">{label ?? "正在读取…"}</p>;
}
export function Callout({
  title,
  children,
}: {
  title?: ReactNode;
  children?: ReactNode;
  tone?: string;
}) {
  return (
    <div className="pc-callout">
      {title && <strong>{title}</strong>}
      {children}
    </div>
  );
}
export function ErrorNotice({
  message,
  onRetry,
}: {
  message?: string;
  onRetry?: () => void;
  auth?: boolean;
  admin?: boolean;
}) {
  return (
    <div role="alert">
      <p>{message}</p>
      {onRetry && <button onClick={onRetry}>重新读取</button>}
    </div>
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
    <div className="pc-muted">
      <p>{title}</p>
      {hint && <p>{hint}</p>}
      {action}
    </div>
  );
}
export function StatusGrid({ children }: { children: ReactNode }) {
  return <dl className="pc-facts">{children}</dl>;
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
export function UsageBar({ value, max }: { value: number; max: number }) {
  return <meter aria-label="相对花费" value={value} max={max || 1} />;
}
export function CopyButton({
  text,
  ariaLabel,
}: {
  text: string;
  ariaLabel?: string;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      aria-label={ariaLabel}
      onClick={() =>
        void navigator.clipboard.writeText(text).then(() => setCopied(true))
      }
    >
      {copied ? "已复制" : "复制"}
    </button>
  );
}
export function CopyField({ label, text }: { label: string; text: string }) {
  return (
    <div>
      <p>{label}</p>
      <pre>{text}</pre>
      <CopyButton text={text} />
    </div>
  );
}
export function ShortCopy({ value, short, label }: { value: string; short: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return <button type="button" className="pc-short-copy" title={`${label}：${value}，点击复制完整值`} aria-label={`复制完整${label}：${short}，完整值 ${value}`} onClick={() => {
    void navigator.clipboard.writeText(value).then(() => setCopied(true)).catch(() => setCopied(false));
  }}><code>{short}</code>{copied && <span className="pc-copy-status">已复制</span>}</button>;
}
export function LongText({ text, label }: { text: string; label: string }) {
  const [expanded, setExpanded] = useState(false);
  const collapsible = shouldCollapseLongText(text);
  return <div className="pc-long-text">
    <p className={collapsible && !expanded ? "pc-clamped" : undefined}>{text}</p>
    {collapsible && <button type="button" className="pc-italic-action" aria-expanded={expanded} aria-label={`${expanded ? "收起" : "展开"}${label}`} onClick={() => setExpanded(!expanded)}>{expanded ? "收起" : "展开"}</button>}
  </div>;
}
export function Dialog({
  title,
  children,
  onClose,
}: {
  title: ReactNode;
  children: ReactNode;
  onClose: () => void;
  wide?: boolean;
}) {
  const ref = useFocus();
  const id = useId();
  return (
    <div
      className="pc-dialog-backdrop"
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.stopPropagation();
          onClose();
        }
      }}
    >
      <div
        ref={ref}
        className="pc-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={id}
      >
        <header>
          <h3 id={id}>{title}</h3>
          <button onClick={onClose} aria-label="关闭对话框">
            ×
          </button>
        </header>
        {children}
      </div>
    </div>
  );
}
export function ConfirmDialog({
  title,
  confirmLabel,
  children,
  onConfirm,
  onCancel,
  busy,
  error,
}: {
  title: ReactNode;
  confirmLabel: string;
  children?: ReactNode;
  onConfirm: () => void;
  onCancel: () => void;
  busy?: boolean;
  error?: string;
  danger?: boolean;
}) {
  return (
    <Dialog title={title} onClose={onCancel}>
      {children}
      {error && <p role="alert">{error}</p>}
      <footer>
        <button disabled={busy} onClick={onCancel}>
          取消
        </button>
        <button disabled={busy} onClick={onConfirm}>
          {busy ? "正在处理…" : confirmLabel}
        </button>
      </footer>
    </Dialog>
  );
}
