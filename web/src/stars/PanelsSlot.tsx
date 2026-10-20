import type { StarsSlotProps } from "./slots";
import { useStarsUI } from "./ui";
// Task C owns this file and its panel components.
export default function PanelsSlot({ ui, projects }: StarsSlotProps) {
  const state = useStarsUI(ui);
  if (!state.projectPanel && !state.platformPanel && !state.selection)
    return null;
  return (
    <section className="stars-panel" aria-label="详情面板">
      <button onClick={() => ui.close()}>关闭</button>
      <h2>
        {state.platformPanel
          ? "平台设置"
          : (projects.find((p) => p.hive.id === state.hiveId)?.hive.name ??
            "项目")}
      </h2>
      <a
        href={
          state.platformPanel
            ? `#/legacy/${state.platformPanel === "models" ? "models" : "setup"}`
            : `#/legacy/hives/${state.hiveId}`
        }
      >
        打开完整页面
      </a>
    </section>
  );
}
