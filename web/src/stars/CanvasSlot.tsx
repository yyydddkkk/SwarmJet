import { useEffect, useRef } from "react";
import { useTheme } from "./theme";
import type { StarsSlotProps } from "./slots";
// Task B owns this component and TimelineSlot. No network calls in the canvas.
export default function CanvasSlot({ projects, stores }: StarsSlotProps) {
  const canvas = useRef<HTMLCanvasElement>(null);
  const theme = useTheme();
  useEffect(() => {
    const draw = () => {
      const element = canvas.current;
      if (!element) return;
      const { width, height } = element.getBoundingClientRect();
      element.width = width * devicePixelRatio;
      element.height = height * devicePixelRatio;
      const ctx = element.getContext("2d");
      if (!ctx) return;
      ctx.scale(devicePixelRatio, devicePixelRatio);
      ctx.clearRect(0, 0, width, height);
      ctx.strokeStyle = theme.colors.hair;
      ctx.fillStyle = theme.colors.ink;
      ctx.font = '18px "IM Fell English","Noto Serif SC",serif';
      projects.forEach((p, i) => {
        const x = (width * (i + 1)) / (projects.length + 1),
          y = height * 0.45;
        ctx.beginPath();
        ctx.ellipse(x, y, 120, 65, -0.24, 0, Math.PI * 2);
        ctx.stroke();
        ctx.beginPath();
        ctx.arc(x, y, p.hive.queenEnabled ? 6 : 4, 0, Math.PI * 2);
        ctx.stroke();
        ctx.fillText(p.hive.name, x - 45, y + 100);
        const state = stores.get(p.hive.id)?.getSnapshot();
        ctx.font = '14px "Source Serif 4","Noto Serif SC",serif';
        ctx.fillText(
          `${state?.snapshot?.agents.length ?? 0} 个 Agent`,
          x - 35,
          y + 123,
        );
      });
    };
    draw();
    const observer = new ResizeObserver(draw);
    if (canvas.current) observer.observe(canvas.current);
    const unsub = [...stores.values()].map((s) => s.subscribe(draw));
    return () => {
      observer.disconnect();
      unsub.forEach((fn) => fn());
    };
  }, [projects, stores, theme]);
  return <canvas ref={canvas} className="stars-canvas" aria-label="项目星图" />;
}
