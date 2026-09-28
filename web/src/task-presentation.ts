import type { Task } from "./types";

export type TaskView = "all" | "active" | "waiting" | "attention" | "ended";

export function taskStageLabel(stage: string): string {
  const labels: Record<string, string> = {
    queued: "等待执行", waiting_dependencies: "等待前置任务", implementing: "开发中",
    reviewing: "审查中", integrating: "集成中", waiting_approval: "等待批准",
    waiting_manual_merge: "等待合并", manual_merging: "合并中", partial_merge: "部分已合并",
    completed: "已完成", blocked: "需要处理", cancelled: "已取消",
  };
  return labels[stage] || stage.replaceAll("_", " ");
}

export function taskView(task: Task): Exclude<TaskView, "all"> {
  if (["completed", "cancelled"].includes(task.stage)) return "ended";
  if (task.stage === "queued" && task.admissionStatus === "waiting") return "waiting";
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
  if (task.stage === "queued" && task.admissionStatus === "waiting") return task.message || "需求已接收，等待可用的执行容量。";
  switch (task.stage) {
    case "waiting_approval":
      return "审查已完成，请核对变更并批准当前版本，或退回修改。";
    case "waiting_manual_merge":
      return task.project.provider === "github" ? "请在 GitHub 合并下方 PR，系统确认全部合并后会完成任务。" : "确认人工验收后，使用“合并整轮”依次合并各仓库 MR。";
    case "manual_merging":
      return "整轮合并正在执行，请查看各仓库状态。";
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
    manual_merging: 4,
    partial_merge: 4,
    finalizing: 5,
    completed: 5,
  };
  return phases[task.stage] ?? null;
}
