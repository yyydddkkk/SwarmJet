import type { Task } from "./types";

export type TaskView = "all" | "active" | "attention" | "ended";

export function taskView(task: Task): Exclude<TaskView, "all"> {
  if (["completed", "cancelled"].includes(task.stage)) return "ended";
  if (
    task.paused ||
    [
      "blocked",
      "waiting_approval",
      "waiting_manual_merge",
      "partial_merge",
    ].includes(task.stage)
  )
    return "attention";
  return "active";
}

export function nextAction(task: Task): string {
  if (["completed", "cancelled"].includes(task.stage)) return task.stage === "completed" && task.round ? "已交付；Issue 保持打开，可复核或发起下一轮" : "查看交付记录";
  if (task.paused) return "任务已暂停，工作现场已保留。确认后可继续执行。";
  switch (task.stage) {
    case "waiting_approval":
      return "审查已完成，请核对变更并批准当前版本，或退回修改。";
    case "waiting_manual_merge":
      return `请在 ${task.project.provider === "github" ? "GitHub" : "GitLab"} 合并下方 ${task.project.provider === "github" ? "PR" : "MR"}，系统确认全部合并后会完成任务。`;
    case "partial_merge":
      return "部分仓库已经合并，请先核对各仓库状态，再处理剩余合并。";
    case "blocked":
      return "查看执行记录和阻塞原因，解决问题后重试任务。";
    case "waiting_dependencies":
      return "等待前置任务完成后继续执行。";
    default:
      return "";
  }
}

// This is a location in the workflow, not a success/progress percentage.
// Blocked/cancelled do not identify which phase was reached; never infer one.
export function taskPhase(task: Task): number | null {
  const phases: Record<string, number> = {
    queued: 0,
    waiting_dependencies: 0,
    preparing: 0,
    developing: 1,
    verifying: 2,
    publishing: 3,
    reviewing: 3,
    waiting_approval: 4,
    waiting_manual_merge: 4,
    acquiring_merge: 4,
    merging: 4,
    partial_merge: 4,
    finalizing: 5,
    completed: 5,
  };
  return phases[task.stage] ?? null;
}
