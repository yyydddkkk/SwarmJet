import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { Icon } from "./ConsoleIcon";
import "./toast.css";

type Tone = "success" | "info" | "error";
type Toast = { id: number; tone: Tone; message: string };
type ToastAPI = {
  success: (message: string) => void;
  info: (message: string) => void;
  error: (message: string) => void;
};

const ToastContext = createContext<ToastAPI | null>(null);

// Use for brief action feedback; keep progress, detailed results, and field errors in context.
export function useToast(): ToastAPI {
  const toast = useContext(ToastContext);
  if (!toast) throw new Error("useToast requires ToastProvider");
  return toast;
}

function ToastItem({ toast, dismiss }: { toast: Toast; dismiss: (id: number) => void }) {
  const duration = toast.tone === "success" ? 6000 : toast.tone === "info" ? 8000 : 0;
  const remaining = useRef(duration);
  const started = useRef(0);
  const timer = useRef<number | undefined>(undefined);
  const hovering = useRef(false);
  const focused = useRef(false);
  const pause = useCallback(() => {
    if (timer.current === undefined) return;
    window.clearTimeout(timer.current);
    timer.current = undefined;
    remaining.current = Math.max(0, remaining.current - (performance.now() - started.current));
  }, []);
  const resume = useCallback(() => {
    if (!duration || timer.current !== undefined || document.hidden || hovering.current || focused.current) return;
    started.current = performance.now();
    timer.current = window.setTimeout(() => dismiss(toast.id), remaining.current);
  }, [dismiss, duration, toast.id]);
  useEffect(() => {
    resume();
    const visibility = () => { if (document.hidden) pause(); else resume(); };
    document.addEventListener("visibilitychange", visibility);
    return () => { pause(); document.removeEventListener("visibilitychange", visibility); };
  }, [pause, resume]);
  return <div
    className={`toast toast-${toast.tone}`}
    onMouseEnter={() => { hovering.current = true; pause(); }}
    onMouseLeave={() => { hovering.current = false; resume(); }}
    onFocusCapture={() => { focused.current = true; pause(); }}
    onBlurCapture={(event) => { if (!event.currentTarget.contains(event.relatedTarget)) { focused.current = false; resume(); } }}
  >
    <span className="toast-icon"><Icon name={toast.tone === "success" ? "check" : toast.tone === "error" ? "alert" : "activity"} /></span>
    <span className="toast-message" role={toast.tone === "error" ? "alert" : "status"}>{toast.message}</span>
    <button type="button" className="toast-dismiss" aria-label="关闭通知" onClick={() => dismiss(toast.id)} />
  </div>;
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Toast[]>([]);
  const [portalHost, setPortalHost] = useState<HTMLElement>(document.body);
  const nextId = useRef(0);
  const viewport = useRef<HTMLDivElement>(null);
  const dismiss = useCallback((id: number) => setItems(previous => previous.filter(item => item.id !== id)), []);
  const push = useCallback((tone: Tone, message: string) => {
    const id = ++nextId.current;
    setItems(previous => [...previous, { id, tone, message }].slice(-4));
  }, []);
  const api = useMemo<ToastAPI>(() => ({
    success: message => push("success", message),
    info: message => push("info", message),
    error: message => push("error", message),
  }), [push]);
  useEffect(() => {
    const chooseHost = () => {
      const dialogs = document.querySelectorAll<HTMLDialogElement>("dialog[open]:modal");
      setPortalHost(dialogs[dialogs.length - 1] || document.body);
    };
    chooseHost();
    const containsDialog = (node: Node) => node instanceof HTMLDialogElement || node instanceof Element && !!node.querySelector("dialog");
    const observer = new MutationObserver(records => {
      if (records.some(record => record.type === "attributes" ||
        [...record.addedNodes, ...record.removedNodes].some(containsDialog))) chooseHost();
    });
    observer.observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ["open"] });
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    const host = viewport.current;
    if (!host || typeof host.showPopover !== "function") return;
    try {
      if (host.matches(":popover-open")) host.hidePopover();
      if (items.length) host.showPopover();
    } catch { /* The modal may have closed during this render. */ }
  }, [items, portalHost]);
  return <ToastContext.Provider value={api}>
    {children}
    {createPortal(<div ref={viewport} className="toast-viewport" popover="manual" aria-label="通知消息">
      {items.map(item => <ToastItem key={item.id} toast={item} dismiss={dismiss} />)}
    </div>, portalHost)}
  </ToastContext.Provider>;
}
