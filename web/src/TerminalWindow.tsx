import { useEffect, useId, useRef, useState, type ReactNode } from "react";

/** Opens the existing terminal host before connecting so xterm can measure it. */
export function TerminalWindow({ title, available, onOpen, onClose, children }: {
  title: string;
  available: boolean;
  onOpen: () => void;
  onClose: () => void;
  children: ReactNode;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  const [open, setOpen] = useState(false);
  const [maximized, setMaximized] = useState(false);

  useEffect(() => {
    if (!open) return;
    const previous = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => { document.body.style.overflow = previous; };
  }, [open]);

  return (
    <>
      <section className="terminal-launcher" aria-label={title}>
        <div>
          <h2>{title}</h2>
          <p>{available
            ? "在独立的大窗口中完成登录与配置，关闭后已保存的配置仍会保留。"
            : "执行器配置终端当前不可用，请检查服务后刷新状态。"}</p>
        </div>
        <button type="button" className="primary" disabled={!available}
          aria-haspopup="dialog" onClick={() => {
            dialog.current?.showModal();
            setOpen(true);
            onOpen();
          }}>打开终端</button>
      </section>
      <dialog ref={dialog} className={`terminal-window${maximized ? " is-maximized" : ""}`}
        aria-labelledby={titleId}
        onCancel={(event) => {
          // Escape is a terminal key (for example in vim), not a close shortcut.
          event.preventDefault();
        }}
        onClose={() => {
          onClose();
          setOpen(false);
          setMaximized(false);
        }}>
        <header className="terminal-window-bar">
          <strong id={titleId}>{title}</strong>
          <div className="actions">
            <button type="button" aria-pressed={maximized}
              onClick={() => setMaximized(!maximized)}>
              {maximized ? "还原窗口" : "最大化"}
            </button>
            <button type="button" onClick={() => dialog.current?.close()}>
              关闭终端
            </button>
          </div>
        </header>
        <div className="terminal-window-content">{children}</div>
      </dialog>
    </>
  );
}
