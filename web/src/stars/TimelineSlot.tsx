import { useEffect, useRef } from "react";
import type { StarsSlotProps } from "./slots";
import { useStarsUI } from "./ui";
import { timelineFor, useTimeline } from "./timeline-state";
import "./canvas.css";
export default function TimelineSlot({ ui }: StarsSlotProps) {
  const open = useStarsUI(ui).overlay === "timeline";
  const clock = timelineFor(ui),
    state = useTimeline(ui);
  const range = useRef<HTMLInputElement>(null);
  const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    if (open) {
      clock.begin();
      range.current?.focus();
    } else clock.reset();
    return () => {
      clock.reset();
      if (open && previous?.isConnected) previous.focus();
    };
  }, [open, clock]);
  useEffect(() => {
    if (!open || !state.playing) return;
    const start = performance.now(),
      from = state.fraction;
    const timer = setInterval(
      () => {
        const fraction = Math.min(
          1,
          from + (performance.now() - start) / (reduced ? 1600 : 8000),
        );
        clock.set(fraction, fraction < 1);
      },
      reduced ? 200 : 40,
    );
    return () => clearInterval(timer);
  }, [open, state.playing, clock, reduced]);
  if (!open) return null;
  const time = (f: number) =>
    new Date(state.anchor - (1 - f) * 86400000).toLocaleTimeString([], {
      hour: "2-digit",
      minute: "2-digit",
      hour12: false,
    });
  return (
    <section className="stars-timeline" aria-label="示意时间轴">
      <p className="stars-timeline-notice">
        示意回放，不代表真实历史；画面使用打开时间轴时的快照。
      </p>
      <div className="stars-timeline-controls">
        <button
          onClick={() =>
            clock.set(state.fraction >= 1 ? 0 : state.fraction, !state.playing)
          }
        >
          {state.playing ? "暂停" : "曝光"}
        </button>
        <div className="stars-timeline-track">
          <input
            ref={range}
            type="range"
            min="0"
            max="1000"
            value={Math.round(state.fraction * 1000)}
            aria-label="示意时间"
            aria-valuetext={time(state.fraction)}
            onChange={(e) => clock.set(Number(e.target.value) / 1000)}
          />
          <div aria-hidden="true">
            {[0, 0.25, 0.5, 0.75, 1].map((f) => (
              <span key={f}>{f === 1 ? "现在" : time(f)}</span>
            ))}
          </div>
        </div>
        <button onClick={() => clock.reset()}>现在</button>
        <button aria-label="关闭时间轴" onClick={() => ui.close()}>
          关闭
        </button>
      </div>
      {state.fraction < 1 && (
        <output className="stars-replay-clock">
          {time(state.fraction)}
          <small>示意</small>
        </output>
      )}
    </section>
  );
}
