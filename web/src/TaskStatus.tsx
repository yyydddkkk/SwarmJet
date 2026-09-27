import type { Task } from "./types";

export const stages: Record<string, string> = {
  queued: "排队中",
  waiting_dependencies: "等待依赖",
  preparing: "准备工作区",
  developing: "开发中",
  verifying: "验证中",
  publishing: "发布变更",
  reviewing: "审查中",
  waiting_approval: "等待批准",
  waiting_manual_merge: "等待人工合并",
  acquiring_merge: "等待合并锁",
  merging: "合并中",
  partial_merge: "部分已合并",
  finalizing: "收尾中",
  completed: "已完成",
  blocked: "需要处理",
  cancelled: "已取消",
};
export function stageLabel(stage: string, provider?: Task["project"]["provider"]): string {
  return stage === "publishing" ? (provider === "github" ? "发布 PR" : "发布 MR") : stages[stage] || stage;
}
export const date = (value: string) =>
  value
    ? new Date(value).toLocaleString("zh-CN", {
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
      })
    : "—";
export function Status({ task }: { task: Task }) {
  const paused =
    task.paused && !["completed", "cancelled"].includes(task.stage);
  return (
    <span className={`status ${paused ? "paused" : task.stage}`}>
      {paused ? "已暂停" : task.stage === "queued" && task.admissionStatus === "waiting" ? "等待准入" : stageLabel(task.stage, task.project.provider)}
    </span>
  );
}
