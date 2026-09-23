import { useEffect, useId, useRef, useState, type ReactNode } from "react";

/** Opens the existing terminal host before connecting so xterm can measure it. */
export function TerminalWindow({ title, available, connection, onOpen, onClose, onRestore, children }: {
  title: string;
  available: boolean;
  connection: "connected" | "connecting" | "disconnected";
  onOpen: () => void;
  onClose: () => void;
  onRestore?: () => void;
  children: ReactNode;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  const [open, setOpen] = useState(false);
  const [maximized, setMaximized] = useState(false);
  const [minimized, setMinimized] = useState(false);
  const minimizing = useRef(false);

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
            : "所选 Node 的 Runner 配置终端不可用，请检查节点和镜像状态。"}</p>
        </div>
        <button type="button" className="primary" disabled={!available && !minimized}
          aria-haspopup="dialog" onClick={() => {
            dialog.current?.showModal();
            setOpen(true);
            if (minimized) {
              setMinimized(false);
              onRestore?.();
            } else onOpen();
          }}>{minimized ? "恢复终端" : "打开终端"}</button>
      </section>
      <dialog ref={dialog} className={`terminal-window${maximized ? " is-maximized" : ""}`}
        aria-labelledby={titleId}
        onCancel={(event) => {
          // Escape is a terminal key (for example in vim), not a close shortcut.
          event.preventDefault();
        }}
        onClose={() => {
          if (minimizing.current) {
            minimizing.current = false;
            setOpen(false);
            setMinimized(true);
            return;
          }
          onClose();
          setOpen(false);
          setMinimized(false);
          setMaximized(false);
        }}>
        <header className="terminal-window-bar">
          <strong id={titleId}>{title}</strong>
          <div className="actions">
            <button type="button" aria-pressed={maximized}
              onClick={() => setMaximized(!maximized)}>
              {maximized ? "还原窗口" : "最大化"}
            </button>
            <button type="button" onClick={() => {
              minimizing.current = true;
              dialog.current?.close();
            }}>最小化</button>
            <button type="button" onClick={() => dialog.current?.close()}>
              关闭终端
            </button>
          </div>
        </header>
        <div className="terminal-window-content">{children}</div>
      </dialog>
      {minimized && <div className="terminal-minimized" role="status">
        <span>{title}已最小化，{connection === "connected" ? "连接保持中" : connection === "connecting" ? "正在连接" : "连接已断开"}。</span>
        <button type="button" onClick={() => {
          dialog.current?.showModal();
          setOpen(true);
          setMinimized(false);
          onRestore?.();
        }}>恢复终端</button>
        <button type="button" onClick={() => {
          onClose();
          setMinimized(false);
          setMaximized(false);
        }}>关闭终端</button>
      </div>}
    </>
  );
}
