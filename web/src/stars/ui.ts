import { useSyncExternalStore } from "react";
export type ObjectSelection = {
  hiveId: string;
  kind: "queen" | "agent" | "pr" | "signal";
  id: string;
  number?: number;
};
export type PlatformTab =
  "setup" | "models" | "connections" | "ledger" | "account";
export type ProjectTab = "methodology" | "settings" | "usage" | "memory";
export type Overlay = "brief" | "list" | "legend" | "display" | "timeline";
export type StarsUIState = {
  hiveId?: string;
  selection?: ObjectSelection;
  projectPanel?: ProjectTab;
  platformPanel?: PlatformTab;
  overlay?: Overlay;
  setupQuery?: string;
};
export function createStarsUI(initial: StarsUIState = {}) {
  let state = initial;
  const listeners = new Set<() => void>();
  const update = (patch: Partial<StarsUIState>) => {
    state = { ...state, ...patch };
    listeners.forEach((fn) => fn());
  };
  return {
    getSnapshot: () => state,
    subscribe(fn: () => void) {
      listeners.add(fn);
      return () => {
        listeners.delete(fn);
      };
    },
    selectProject(hiveId?: string) {
      update({ hiveId, selection: undefined });
    },
    selectObject(selection: ObjectSelection) {
      update({
        hiveId: selection.hiveId,
        selection,
        platformPanel: undefined,
        overlay: undefined,
      });
    },
    openProject(tab: ProjectTab = "methodology") {
      update({
        projectPanel: tab,
        platformPanel: undefined,
        overlay: undefined,
      });
    },
    openPlatform(tab: PlatformTab = "setup", setupQuery?: string) {
      update({
        platformPanel: tab,
        setupQuery,
        projectPanel: undefined,
        selection: undefined,
        overlay: undefined,
      });
    },
    openOverlay(overlay: Overlay) {
      update({
        overlay,
        platformPanel: undefined,
        ...(overlay === "list" || overlay === "brief"
          ? { selection: undefined, projectPanel: undefined }
          : {}),
      });
    },
    closeProject() {
      update({ projectPanel: undefined });
    },
    closeObject() {
      update({ selection: undefined });
    },
    closePlatform() {
      update({ platformPanel: undefined, setupQuery: undefined });
    },
    closeOverlay() {
      update({ overlay: undefined });
    },
    close() {
      if (state.overlay) update({ overlay: undefined });
      else if (state.platformPanel) update({ platformPanel: undefined });
      else if (state.selection) update({ selection: undefined });
      else if (state.projectPanel) update({ projectPanel: undefined });
      else update({ hiveId: undefined });
    },
  };
}
export type StarsUI = ReturnType<typeof createStarsUI>;
export const useStarsUI = (ui: StarsUI) =>
  useSyncExternalStore(ui.subscribe, ui.getSnapshot, ui.getSnapshot);
