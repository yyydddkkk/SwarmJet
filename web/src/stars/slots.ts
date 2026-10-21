import type { HiveOverview, Principal, ProjectStore } from "../data";
import type { StarsUI } from "./ui";
export type StarsSlotProps = {
  ui: StarsUI;
  projects: HiveOverview[];
  stores: ReadonlyMap<string, ProjectStore>;
  principal: Principal;
  onLogout(): void;
  reloadProjects(): void;
};
