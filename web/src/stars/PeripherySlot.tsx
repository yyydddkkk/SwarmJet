import { useState } from "react";
import { useDataAPI } from "../data";
import type { StarsSlotProps } from "./slots";
import { THEMES, themeStore, useTheme, type ThemeChoice } from "./theme";
import { useStarsUI, type Overlay } from "./ui";
// Task D owns this file, LoginSlot and all peripheral components.
export default function PeripherySlot({ ui, projects }: StarsSlotProps) {
  const state = useStarsUI(ui);
  const theme = useTheme();
  const api = useDataAPI();
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const current = projects.find((p) => p.hive.id === state.hiveId)?.hive;
  return (
    <>
      <header className="stars-mark">
        <b>SwarmJet</b>
        <button onClick={() => ui.selectProject()}>全部项目</button>
        {current && <span> / {current.name}</span>}
      </header>
      <section className="stars-cartouche">
        <h1>{current?.name ?? "你的项目"}</h1>
        <span>{projects.length} 个项目</span>
        <select
          aria-label="选择项目"
          value={state.hiveId ?? ""}
          onChange={(e) => ui.selectProject(e.target.value || undefined)}
        >
          <option value="">全部项目</option>
          {projects.map((p) => (
            <option key={p.hive.id} value={p.hive.id}>
              {p.hive.name}
            </option>
          ))}
        </select>
      </section>
      <nav className="stars-tools" aria-label="星图工具">
        {(["brief", "list", "timeline", "legend", "display"] as Overlay[]).map(
          (item, i) => (
            <button key={item} onClick={() => ui.openOverlay(item)}>
              {["简报", "列表", "时间", "图例", "显示"][i]}
            </button>
          ),
        )}
        <button onClick={() => ui.openPlatform()}>平台设置</button>
        {current && <button onClick={() => ui.openProject()}>项目面板</button>}
      </nav>
      {state.overlay && (
        <section
          className="stars-panel"
          aria-label={state.overlay === "display" ? "显示设置" : "星图工具面板"}
        >
          <button onClick={() => ui.close()}>关闭</button>
          {state.overlay === "display" ? (
            <>
              <h2>显示设置</h2>
              <label>
                主题
                <select
                  aria-label="主题"
                  value={theme.choice}
                  onChange={(e) =>
                    themeStore.setTheme(e.target.value as ThemeChoice)
                  }
                >
                  {Object.entries(THEMES).map(([id, t]) => (
                    <option key={id} value={id}>
                      {t.label}
                    </option>
                  ))}
                  <option value="system">跟随系统</option>
                </select>
              </label>
              <label>
                风格
                <select
                  aria-label="风格"
                  value={theme.style}
                  onChange={(e) =>
                    themeStore.setStyle(e.target.value as "classic" | "simple")
                  }
                >
                  <option value="classic">经典</option>
                  <option value="simple">简洁</option>
                </select>
              </label>
              <label>
                <input
                  type="checkbox"
                  checked={theme.render.grid}
                  onChange={(e) => themeStore.setGrid(e.target.checked)}
                />
                经纬线
              </label>
            </>
          ) : (
            <>
              <h2>
                {
                  {
                    brief: "简报",
                    list: "列表",
                    timeline: "时间",
                    legend: "图例",
                  }[state.overlay]
                }
              </h2>
              {projects.map((p) => (
                <p key={p.hive.id}>
                  <button
                    onClick={() => {
                      ui.selectProject(p.hive.id);
                      ui.close();
                    }}
                  >
                    {p.hive.name}
                  </button>
                </p>
              ))}
            </>
          )}
        </section>
      )}
      <form
        className="stars-say"
        onSubmit={async (e) => {
          e.preventDefault();
          if (!current || !text.trim()) return;
          try {
            await api.talkToQueen(current.id, text);
            setText("");
            setError("");
          } catch (e) {
            setError(String(e));
          }
        }}
      >
        <span aria-hidden="true">⊙</span>
        <input
          aria-label="对主 Agent 说"
          placeholder={current ? "对主 Agent 说" : "先选择一个项目"}
          value={text}
          onChange={(e) => setText(e.target.value)}
          disabled={!current}
        />
        <button disabled={!current || !text.trim()}>发送</button>
        {error && <span role="alert">{error}</span>}
      </form>
    </>
  );
}
