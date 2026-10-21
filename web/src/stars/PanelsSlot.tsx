import { useEffect, useLayoutEffect, useState } from "react";
import type { StarsSlotProps } from "./slots";
import { useStarsUI, type ProjectTab, type PlatformTab } from "./ui";
import { useProjectState } from "../data";
import Methodology from "./panels/MethodologyPage";
import Settings from "./panels/HiveSettings";
import Models from "./panels/ModelsPage";
import Setup from "./panels/SetupPage";
import { HiveUsagePanel } from "./panels/UsagePanel";
import { ObjectPanel, Ledger } from "./panels/ObjectPanel";
import AlmanacOverview from "./panels/AlmanacOverview";
import { useFocus, ErrorNotice } from "./panels/primitives";
import "./panels/panels.css";
const platformTabs: [PlatformTab, string][] = [
  ["setup", "接入"],
  ["models", "模型"],
  ["connections", "连接"],
  ["ledger", "账簿"],
  ["account", "账号"],
];
function usePanelPresence<T>(value: T | undefined, duration = 750) {
  const [rendered, setRendered] = useState(value);
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (value !== undefined) {
      setRendered(value);
      const frame = requestAnimationFrame(() => setOpen(true));
      return () => cancelAnimationFrame(frame);
    }
    setOpen(false);
    const timeout = window.setTimeout(() => setRendered(undefined), duration);
    return () => window.clearTimeout(timeout);
  }, [value, duration]);
  return { rendered, open };
}
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
  const ref = useFocus(active && !ui.getSnapshot().selection);
  const data = state.snapshot;
  const [methodologyAdvanced, setMethodologyAdvanced] = useState(false);
  const [methodologyVersion, setMethodologyVersion] = useState<number | undefined>();
  const overview = tab === "methodology" && !methodologyAdvanced;
  const viewIdentity = tab === "methodology" ? overview ? "overview" : `methodology:${methodologyVersion ?? "current"}` : tab;
  useLayoutEffect(() => {
    if (ref.current) ref.current.scrollTop = 0;
  }, [viewIdentity, ref]);
  const resetForInternalVersion = (target: EventTarget | null) => {
    if (!(target instanceof Element)) return;
    const select = target.closest<HTMLSelectElement>('select[aria-label="查看方法论版本"]');
    const versionButton = target.closest<HTMLButtonElement>(".pc-version-table .pc-row-actions button");
    if (select || versionButton?.textContent?.trim() === "查看") {
      if (ref.current) ref.current.scrollTop = 0;
    }
  };
  return (
    <div
      ref={ref}
      onChangeCapture={(event) => resetForInternalVersion(event.target)}
      onClickCapture={(event) => resetForInternalVersion(event.target)}
      className={`pc-sheet pc-left ${active ? "pc-open" : ""}`}
      role="region"
      aria-label="项目面板"
      aria-hidden={!active}
      inert={!active}
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
      {!overview && <button type="button" className="pc-almanac-back" onClick={() => { setMethodologyAdvanced(false); setMethodologyVersion(undefined); ui.openProject("methodology"); }}>返回项目面板</button>}
      {!data ? (
        state.error ? (
          <ErrorNotice
            message={state.error}
            onRetry={() => void store.refresh()}
          />
        ) : (
          <p>正在读取项目…</p>
        )
      ) : overview ? (
        <AlmanacOverview snapshot={data} methodologyResource={state.resources.methodology} onRetryMethodology={() => void store.refresh()} onMethodology={(version) => { setMethodologyVersion(version); setMethodologyAdvanced(true); }} onSettings={() => ui.openProject("settings")} onUsage={() => ui.openProject("usage")} onSaved={() => void store.refresh()} />
      ) : tab === "methodology" ? (
        <Methodology key={`${data.hive.id}:${methodologyVersion ?? "current"}`} hiveId={data.hive.id} initialVersion={methodologyVersion} />
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
            queenId={data.queenAgent?.id ?? data.queen.agentId}
            onAgent={(id) => ui.selectObject(id === (data.queenAgent?.id ?? data.queen.agentId)
              ? { hiveId: data.hive.id, kind: "queen", id: `${data.hive.id}:queen` }
              : { hiveId: data.hive.id, kind: "agent", id })}
          />
          <p>7 天走势、本周花费：暂时无法获取。</p>
          <h3>方法论预算</h3>
          {state.resources.methodology.status === "ready" && Number.isInteger(data.methodology.version) ? <p>
            单 Agent：
            {data.methodology.manifest.limits?.maxCostPerAgentUsd || "未设置"}
            ；每天：
            {data.methodology.manifest.limits?.maxCostPerDayUsd || "未设置"}
            （美元）
          </p> : <p>{state.resources.methodology.status === "loading" ? "正在读取方法论预算…" : "方法论预算暂时无法读取。"}</p>}
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
  const project = usePanelPresence(state.projectPanel, 700);
  const object = usePanelPresence(state.selection, 700);
  const platform = usePanelPresence(state.platformPanel);
  const ref = useFocus(platform.open);
  const store = state.hiveId ? stores.get(state.hiveId) : undefined;
  const objectStore = object.rendered ? stores.get(object.rendered.hiveId) : undefined;
  return (
    <>
      {project.rendered && store && (
        <ProjectPanel
          key={state.hiveId}
          store={store}
          ui={ui}
          tab={project.rendered}
          active={project.open}
        />
      )}
      {object.rendered && objectStore && (
        <ObjectPanel
          key={`${object.rendered.hiveId}:${object.rendered.kind}:${object.rendered.id}`}
          {...props}
          store={objectStore}
          selection={object.rendered}
          active={object.open}
        />
      )}
      {platform.rendered && (
        <div
          ref={ref}
          role="dialog"
          aria-modal="true"
          aria-label="平台设置"
          className={`pc-sheet pc-platform ${platform.open ? "pc-open" : ""}`}
          aria-hidden={!platform.open}
          inert={!platform.open}
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
                aria-pressed={platform.rendered === tab}
                onClick={() => ui.openPlatform(tab)}
              >
                {label}
              </button>
            ))}
          </nav>
          <div className="pc-platform-body" key={platform.rendered}>
            {platform.rendered === "setup" ||
            platform.rendered === "connections" ? (
              <Setup
                query={new URLSearchParams(state.setupQuery)}
                mode={platform.rendered}
                onModels={() => ui.openPlatform("models")}
              />
            ) : platform.rendered === "models" ? (
              <Models />
            ) : platform.rendered === "ledger" ? (
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
