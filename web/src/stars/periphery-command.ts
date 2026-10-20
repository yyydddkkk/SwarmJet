import type { HiveOverview, ProjectStore } from "../data";
import type { StarsUI } from "./ui";
export function command(
  text: string,
  projects: HiveOverview[],
  stores: ReadonlyMap<string, ProjectStore>,
  ui: StarsUI,
) {
  const value = text.trim();
  const project = projects.find(
    (p) => p.hive.name.toLowerCase() === value.toLowerCase(),
  );
  if (project)
    return {
      label: `前往 ${project.hive.name}`,
      run: () => {
        ui.close();
        ui.selectProject(project.hive.id);
      },
    };
  const current = ui.getSnapshot().hiveId;
  const candidates = [...stores.entries()].filter(
    ([id]) => !current || id === current,
  );
  const match = value.match(/^(PR\s*#?\s*|#)(\d+)$/i);
  if (match)
    for (const [id, store] of candidates) {
      const snapshot = store.getSnapshot().snapshot;
      const n = Number(match[2]);
      const pr = snapshot?.prs.find((p) => p.number === n);
      if (pr && match[1].toLowerCase().startsWith("pr"))
        return {
          label: `前往 PR #${n}`,
          run: () =>
            ui.selectObject({
              hiveId: id,
              kind: "pr",
              id: `${id}:pr:${n}`,
              number: n,
            }),
        };
      const issue = snapshot?.signals.find(
        (s) => s.kind === "issue" && s.number === n,
      );
      if (issue && match[1] === "#")
        return {
          label: `前往 Issue #${n}`,
          run: () =>
            ui.selectObject({
              hiveId: id,
              kind: "signal",
              id: issue.messageId,
              number: n,
            }),
        };
      const agent =
        match[1] === "#" && snapshot?.agents.find((a) => a.issueNumber === n);
      if (agent)
        return {
          label: `前往 Issue #${n} 的 Agent`,
          run: () =>
            ui.selectObject({ hiveId: id, kind: "agent", id: agent.id }),
        };
    }
  const platform = {
    模型: "models",
    凭证: "models",
    接入: "setup",
    github: "setup",
    隧道: "setup",
    webhook: "setup",
    连接: "connections",
    mcp: "connections",
    令牌: "connections",
    账簿: "ledger",
    账号: "account",
    退出: "account",
    平台: "setup",
    设置: "setup",
  } as const;
  const tab = platform[value.toLowerCase() as keyof typeof platform];
  if (tab) return { label: `打开${value}`, run: () => ui.openPlatform(tab) };
  const projectTabs = {
    方法论: "methodology",
    项目设置: "settings",
    花费: "usage",
    用量: "usage",
    项目面板: "methodology",
  } as const;
  const projectTab = projectTabs[value as keyof typeof projectTabs];
  if (projectTab && projects.length)
    return {
      label: `打开${value}`,
      run: () => {
        if (!current) ui.selectProject(projects[0].hive.id);
        ui.openProject(projectTab);
      },
    };
  const overlays = {
    列表: "list",
    时间: "timeline",
    时间轴: "timeline",
    回放: "timeline",
    简报: "brief",
    图例: "legend",
    显示: "display",
  } as const;
  const overlay = overlays[value as keyof typeof overlays];
  if (overlay)
    return { label: `打开${value}`, run: () => ui.openOverlay(overlay) };
  return null;
}
export const preference = {
  read(key: string) {
    try {
      return localStorage.getItem(key);
    } catch {
      return null;
    }
  },
  write(key: string, value: string) {
    try {
      localStorage.setItem(key, value);
    } catch {
      /* optional preference */
    }
  },
};
