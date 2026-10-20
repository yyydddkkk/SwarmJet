import { useState } from "react";
import type { StarsSlotProps } from "./slots";
import { useStarsUI, type ProjectTab, type PlatformTab } from "./ui";
import { useProjectState } from "../data";
import Methodology from "./panels/MethodologyPage";
import Settings from "./panels/HiveSettings";
import Models from "./panels/ModelsPage";
import Setup from "./panels/SetupPage";
import { HiveUsagePanel } from "./panels/UsagePanel";
import { ObjectPanel, Ledger } from "./panels/ObjectPanel";
import { useFocus } from "./panels/primitives";
import "./panels/panels.css";
const projectTabs: [ProjectTab, string][] = [
  ["methodology", "方法论"],
  ["settings", "设置"],
  ["usage", "花费"],
  ["memory", "记忆"],
];
const platformTabs: [PlatformTab, string][] = [
  ["setup", "接入"],
  ["models", "模型"],
  ["connections", "连接"],
  ["ledger", "账簿"],
  ["account", "账号"],
];
function ProjectPanel({
  store,
  ui,
  tab,
  active,
}: {
  store: NonNullable<ReturnType<StarsSlotProps["stores"]["get"]>>;
  ui: StarsSlotProps["ui"];
  tab: ProjectTab;
  active: boolean;
}) {
  const state = useProjectState(store);
  const ref = useFocus(active);
  const data = state.snapshot;
  return (
    <div
      ref={ref}
      className="pc-sheet pc-left"
      role="region"
      aria-label="项目面板"
    >
      <button
        className="pc-close"
        aria-label="关闭项目面板"
        onClick={() => {
          ui.closeProject();
          document.querySelector<HTMLElement>(".pc-right button")?.focus();
        }}
      >
        ×
      </button>
      <h2>{data?.hive.name ?? "项目"}</h2>
      <nav aria-label="项目分区">
        {projectTabs.map(([key, label]) => (
          <button
            aria-pressed={tab === key}
            key={key}
            onClick={() => ui.openProject(key)}
          >
            {label}
          </button>
        ))}
      </nav>
      {!data ? (
        <p>正在读取项目…</p>
      ) : tab === "methodology" ? (
        <Methodology hiveId={data.hive.id} />
      ) : tab === "settings" ? (
        <Settings
          key={data.hive.id}
          hive={data.hive}
          onSaved={() => void store.refresh()}
        />
      ) : tab === "usage" ? (
        <>
          <HiveUsagePanel
            hiveId={data.hive.id}
            agents={data.agents}
            onAgent={(id) =>
              ui.selectObject({ hiveId: data.hive.id, kind: "agent", id })
            }
          />
          <p>7 天走势、本周花费：暂时无法获取。</p>
          <h3>方法论预算</h3>
          <p>
            单 Agent：
            {data.methodology.manifest.limits?.maxCostPerAgentUsd || "未设置"}
            ；每天：
            {data.methodology.manifest.limits?.maxCostPerDayUsd || "未设置"}
            （美元）
          </p>
        </>
      ) : (
        <p>记忆功能暂未提供。</p>
      )}
    </div>
  );
}
export default function PanelsSlot(props: StarsSlotProps) {
  const { ui, stores } = props;
  const state = useStarsUI(ui);
  const ref = useFocus(!!state.platformPanel);
  const store = state.hiveId ? stores.get(state.hiveId) : undefined;
  return (
    <>
      {state.projectPanel && store && (
        <ProjectPanel
          store={store}
          ui={ui}
          tab={state.projectPanel}
          active={!state.selection}
        />
      )}{" "}
      {state.selection && store && (
        <ObjectPanel
          key={`${state.selection.kind}:${state.selection.id}`}
          {...props}
          store={store}
          selection={state.selection}
        />
      )}{" "}
      {state.platformPanel && (
        <div
          ref={ref}
          role="dialog"
          aria-modal="true"
          aria-label="平台设置"
          className="pc-sheet pc-platform"
        >
          <button
            className="pc-close"
            aria-label="关闭平台设置"
            onClick={() => ui.closePlatform()}
          >
            ×
          </button>
          <nav aria-label="平台分区">
            <h2>平台设置</h2>
            {platformTabs.map(([tab, label]) => (
              <button
                key={tab}
                aria-pressed={state.platformPanel === tab}
                onClick={() => ui.openPlatform(tab)}
              >
                {label}
              </button>
            ))}
          </nav>
          <div className="pc-platform-body" key={state.platformPanel}>
            {state.platformPanel === "setup" ||
            state.platformPanel === "connections" ? (
              <Setup
                query={new URLSearchParams(state.setupQuery)}
                mode={state.platformPanel}
                onModels={() => ui.openPlatform("models")}
              />
            ) : state.platformPanel === "models" ? (
              <Models />
            ) : state.platformPanel === "ledger" ? (
              <Ledger projects={props.projects} />
            ) : (
              <Account principal={props.principal} onLogout={props.onLogout} />
            )}
          </div>
        </div>
      )}
    </>
  );
}

function Account({
  principal,
  onLogout,
}: Pick<StarsSlotProps, "principal" | "onLogout">) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return (
    <>
      <h3>账号</h3>
      <p>{principal.displayName || principal.username}</p>
      <button
        disabled={busy}
        onClick={async () => {
          setBusy(true);
          setError("");
          try {
            await onLogout();
          } catch (cause) {
            setError(cause instanceof Error ? cause.message : String(cause));
          } finally {
            setBusy(false);
          }
        }}
      >
        {busy ? "正在退出…" : "退出登录"}
      </button>
      {error && <p role="alert">退出失败：{error}。请重试。</p>}
    </>
  );
}
