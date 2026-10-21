import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { messagePreview, prSummary, useDataAPI, weightedTruncate } from "../data";
import type { StarsSlotProps } from "./slots";
import { StarMap, type MapHistoryHover, type MapNote } from "./star-map";
import "./canvas.css";
export default function CanvasSlot({ projects, stores, ui }: StarsSlotProps) {
  const canvas = useRef<HTMLCanvasElement>(null),
    api = useDataAPI();
  const observer = useRef<HTMLButtonElement>(null),
    decisionPanel = useRef<HTMLElement>(null);
  const [feedback, setFeedback] = useState(""),
    [deciding, setDeciding] = useState(false),
    [note, setNote] = useState<MapNote>(),
    [historyHover, setHistoryHover] = useState<MapHistoryHover>();
  const idKey = projects.map((p) => p.hive.id).join("\0");
  useSyncExternalStore(
    (fn) => {
      const off = [...stores.values()].map((s) => s.subscribe(fn));
      return () => off.forEach((fn) => fn());
    },
    () => [...stores.values()].map((s) => s.getSnapshot().revision).join(":"),
  );
  const decisions = [...stores.entries()].flatMap(([hiveId, s]) =>
    (s.getSnapshot().derived?.decisions ?? []).map((d) => ({
      ...d,
      hiveId,
      name: s.getSnapshot().snapshot?.hive.name ?? hiveId,
    })),
  );
  useEffect(() => {
    const element = canvas.current!;
    const map = new StarMap(
      element,
      projects,
      stores,
      ui,
      (id, number) => api.assignIssue(id, number),
      setFeedback,
      setNote,
      setHistoryHover,
    );
    Object.defineProperty(element, "starMapMetrics", {
      configurable: true,
      get: () => map.metrics,
    });
    return () => {
      map.destroy();
      delete (element as HTMLCanvasElement & { starMapMetrics?: unknown })
        .starMapMetrics;
    };
  }, [idKey, stores, ui, api]);
  useEffect(() => {
    if (!feedback) return;
    const timer = setTimeout(() => setFeedback(""), 6000);
    return () => clearTimeout(timer);
  }, [feedback]);
  useEffect(
    () =>
      ui.subscribe(() => {
        if (ui.getSnapshot().overlay || ui.getSnapshot().platformPanel) {
          setDeciding(false);
          setNote(undefined);
          setHistoryHover(undefined);
        }
      }),
    [ui],
  );
  useEffect(() => {
    if (deciding)
      decisionPanel.current
        ?.querySelector<HTMLButtonElement>("button")
        ?.focus();
  }, [deciding]);
  const closeDecisions = () => {
    setDeciding(false);
    observer.current?.focus();
  };
  return (
    <>
      <canvas
        ref={canvas}
        className="stars-canvas"
        aria-label="项目星图；使用列表可访问所有对象和操作"
      />
      {historyHover && !note && <div
        className="stars-history-hover"
        role="tooltip"
        style={{ left: Math.max(24, Math.min(innerWidth - 360, historyHover.x + 24)), top: Math.max(24, Math.min(innerHeight - 320, historyHover.y - 24)) }}
        onPointerLeave={() => setHistoryHover(undefined)}
      ><p>{historyHover.summary}</p><small>{historyHover.result} · {historyHover.time}</small></div>}
      <button
        ref={observer}
        onKeyDown={(e) => {
          if (e.key === "Escape" && deciding) {
            e.preventDefault();
            e.stopPropagation();
            closeDecisions();
          }
        }}
        className="stars-observer"
        data-attention={decisions.length > 0}
        aria-label={
          decisions.length
            ? `${decisions.length} 件事等你决定`
            : "没有等你决定的事"
        }
        aria-expanded={deciding}
        aria-controls="stars-decisions"
        onClick={() => setDeciding((v) => !v)}
      >
        <svg viewBox="-10 -10 20 20" aria-hidden="true"><circle r="7" fill="none" stroke="currentColor" strokeWidth="1"/><circle r="1.6" fill="currentColor"/></svg>
      </button>
      <div className={`stars-note${note ? " open" : ""}`} style={note ? { left: Math.max(40, Math.min(innerWidth - 300, note.x + 26)), top: Math.max(40, Math.min(innerHeight - 320, note.y - 24)) } : undefined} aria-live="polite">
        {note && <><h4>{note.title}</h4><p>{note.body}</p><div className="acts"><button onClick={() => { ui.openOverlay("list"); window.dispatchEvent(new CustomEvent("swarmjet:catalogue-open", { detail: { scope: note.scope, query: note.query } })); setNote(undefined); }}>{note.kind === "history" ? "在列表中查看" : "在列表中查看全部"}</button><button onClick={() => setNote(undefined)}>关闭</button></div></>}
      </div>
      {deciding && (
        <section
          ref={decisionPanel}
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              e.preventDefault();
              e.stopPropagation();
              closeDecisions();
            }
          }}
          id="stars-decisions"
          className="stars-decisions"
          aria-label="等你决定的事"
        >
          <button
            className="stars-decisions-close"
            aria-label="关闭等你决定的事"
            onClick={closeDecisions}
          >
            关闭
          </button>
          <h4>等你决定</h4>
          {decisions.length === 0 && <p>暂时没有需要你决定的事。</p>}
          {decisions.map((d) => (
            <button
              key={`${d.hiveId}:${d.id}`}
              onClick={() => {
                if (d.kind === "pr")
                  ui.selectObject({
                    hiveId: d.hiveId,
                    kind: "pr",
                    id: d.id,
                    number: d.pr.number,
                  });
                else {
                  const from = d.message.from;
                  ui.selectObject({
                    hiveId: d.hiveId,
                    kind: from.startsWith("forager:") ? "agent" : "queen",
                    id: from.startsWith("forager:")
                      ? from.slice(8)
                      : `${d.hiveId}:queen`,
                  });
                }
                closeDecisions();
              }}
            >
              {d.name}：
              {d.kind === "pr"
                ? `PR #${d.pr.number} 等你决定：${prSummary(d.pr, 30)}`
                : `回答${d.message.from === "queen" ? "主 Agent" : " Agent"}的问题：${weightedTruncate(messagePreview(d.message), 30)}`}
            </button>
          ))}
        </section>
      )}
      {feedback && (
        <div className="stars-canvas-feedback" role="status">
          {feedback}
        </div>
      )}
    </>
  );
}
