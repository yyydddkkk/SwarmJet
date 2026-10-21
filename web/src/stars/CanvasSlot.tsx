import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { messagePreview, useDataAPI } from "../data";
import type { StarsSlotProps } from "./slots";
import { StarMap } from "./star-map";
import "./canvas.css";
export default function CanvasSlot({ projects, stores, ui }: StarsSlotProps) {
  const canvas = useRef<HTMLCanvasElement>(null),
    api = useDataAPI();
  const observer = useRef<HTMLButtonElement>(null),
    decisionPanel = useRef<HTMLElement>(null);
  const [feedback, setFeedback] = useState(""),
    [deciding, setDeciding] = useState(false);
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
        if (ui.getSnapshot().overlay || ui.getSnapshot().platformPanel)
          setDeciding(false);
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
        ⊙{decisions.length > 0 && <small>{decisions.length}</small>}
      </button>
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
          <h2>等你决定</h2>
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
                ? `PR #${d.pr.number} 等你决定`
                : `回答${d.message.from === "queen" ? "主 Agent" : " Agent"}的问题：${messagePreview(d.message).slice(0, 40)}`}
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
