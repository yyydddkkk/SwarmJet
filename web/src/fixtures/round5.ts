// Round-5 HTTP fixtures; names mirror domain.MethodologyTemplate and Queen status.
import type { MethodologyTemplate } from "../hive/types";

export const methodologyTemplates: MethodologyTemplate[] = [
  { name: "pstack", title: "PStack", description: "Worker 开发，独立 Verifier 验证，适合需要审查与验证的团队。", version: "1.0.0" },
  { name: "minimal", title: "极简 Worker", description: "只有一个 Worker，不要求验证，不自动合并，适合从小任务开始。", version: "1.0.0" },
];
