import {
  cloneElement,
  isValidElement,
  useEffect,
  useId,
  useRef,
  type ReactNode,
} from "react";

export function ErrorBox({ error, retry }: { error: string; retry?: () => void }) {
  return error ? (
    <div className="error" role="alert">
      <span>{error}</span>
      {retry && <button type="button" onClick={retry}>重试</button>}
    </div>
  ) : null;
}

export function Field({ label, children, hint }: {
  label: string;
  children: ReactNode;
  hint?: string;
}) {
  const id = useId();
  return (
    <label className="field">
      <span id={`${id}-label`}>{label}</span>
      {isValidElement<Record<string, unknown>>(children)
        ? cloneElement(children, {
            "aria-labelledby": `${id}-label`,
            "aria-describedby": hint ? `${id}-hint` : undefined,
          })
        : children}
      {hint && <small id={`${id}-hint`}>{hint}</small>}
    </label>
  );
}

export function Panel({ title, children, extra }: {
  title: string;
  children: ReactNode;
  extra?: ReactNode;
}) {
  return (
    <section className="panel">
      <div className="panel-head">
        <h2>{title}</h2>
        {extra}
      </div>
      {children}
    </section>
  );
}

export function Modal({ title, children, close, className = "" }: {
  title: string;
  children: ReactNode;
  close: () => void;
  className?: string;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  useEffect(() => {
    const dialog = ref.current;
    dialog?.showModal();
    return () => dialog?.close();
  }, []);
  return (
    <dialog ref={ref} className={className} onCancel={(event) => {
      event.preventDefault();
      close();
    }} aria-labelledby={titleId}>
      <div className="panel-head">
        <h2 id={titleId}>{title}</h2>
        <button type="button" onClick={close} aria-label="关闭对话框">×</button>
      </div>
      {children}
    </dialog>
  );
}

export function ConfirmDialog({ title, children, confirmLabel, cancelLabel = "取消",
  onConfirm, onCancel, busy = false, disabled = false, danger = false, error = "",
}: {
  title: string;
  children: ReactNode;
  confirmLabel: string;
  cancelLabel?: string;
  onConfirm: () => void;
  onCancel: () => void;
  busy?: boolean;
  disabled?: boolean;
  danger?: boolean;
  error?: string;
}) {
  return (
    <Modal title={title} close={onCancel} className="confirm-dialog">
      <div className="confirm-dialog-content">{children}</div>
      <ErrorBox error={error} />
      <div className="form-actions confirm-dialog-actions">
        <button type="button" autoFocus onClick={onCancel} disabled={busy}>{cancelLabel}</button>
        <button type="button" className={danger ? "danger" : "primary"}
          onClick={onConfirm} disabled={busy || disabled}>
          {busy ? "正在处理…" : confirmLabel}
        </button>
      </div>
    </Modal>
  );
}
