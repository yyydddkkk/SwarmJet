# Kubernetes 接入阶段总结

状态：**隔离验证阶段，尚未正式接入企业任务**。记录日期：2026-09-27。

## 结论

SwarmJet 已有一条显式启用的 Kubernetes 离线验证路径，并分别验证了受限 Job、持久执行权、产物保存、OpenCode 跨 Pod 会话续接和部分联调能力。**按默认配置启动时，任务仍走原来的 Node、Docker Runner 和 Docker Compose 路径。** 目前没有把 Kubernetes 设为企业主线的默认执行方式，也没有迁移公司快照中的任务和数据。

Kubernetes 集群是 SwarmJet 之外的运行基础设施。云服务商托管集群和自有服务器集群都可作为未来部署环境；但接入不只是增加连接配置，还要改变任务归属、Runner 生命周期、工作区、凭证、报告、取消和恢复机制。本文记录当前代码边界，避免把单项验证误认为完整上线。

## 当前架构与范围

| 路径 | 当前状态 | 实际用途 |
| --- | --- | --- |
| Node / Docker / Compose | 默认路径，仍保留 | 现有任务执行、受管初始化和联调 |
| Kubernetes `KubernetesTaskV1` | 需显式设置 `KUBERNETES_OFFLINE_VALIDATION=true`，并指定隔离验证项目 | 确定性离线 Runner 的准入、执行、结果与报告闭环 |
| 企业任务全面使用 Kubernetes | 未交付 | 不得作为当前部署的默认执行路径 |

Kubernetes 路径使用独立的 Temporal task queue。PostgreSQL 记录执行池、资源和镜像摘要快照，以及稳定的 execution ID、递增执行权版本和 Job 尝试状态。控制面创建受限 Job；任务 PVC 保存工作区和 CLI 会话，S3 兼容对象存储或隔离测试用 FileStore 保存结果、日志和报告。无法确认旧 Job 是否仍可能创建或旧 Pod 是否真正停止时，执行会阻断，不自动接管。

## 已完成的实现与验证

| 范围 | 已有实现 | 验证证据 |
| --- | --- | --- |
| 准入与执行权 | [持久准入、取消、冻结和版本校验](../internal/store/kubernetes_admission.go) | 隔离 PostgreSQL 的并发准入、取消、接管、结果提交和写入锁测试 |
| Runner | [受限 Job 控制器](../internal/k8srunner/controller.go)、任务 PVC、默认拒绝网络策略 | 一次性 kind+Cilium 集群中实测拒绝和定向放行；固定摘要 Job 完成，另一 Pod 读回相同 PVC 结果 |
| 结果和报告 | [产物存储接口与 S3 实现](../internal/artifacts/store.go)、[控制台读取](../internal/api/api.go) | 摘要校验、冲突与篡改测试；一次性 MinIO 实测；报告独立于 Runner Pod 读取 |
| CLI 会话 | 相同 PVC 上的精确 OpenCode session ID 续接 | 使用临时 DeepSeek 凭证，在两个不同 Pod 中实测会话记忆；测试 Secret、集群和本机凭证文件已删除 |
| 远端写入 | [固定 SHA 的可信写入与对账模块](../internal/platformwrite/reconciler.go) | 一次性本机 GitLab 实测 push、MR、merge，分别注入“远端成功但响应丢失” |
| 联调资源 | [Kubernetes 单容器和多容器检查包](../internal/k8sintegration/integration.go) | 一次性 kind+Cilium 集群中实测成功、预期失败、取消和资源清理 |
| Workflow 历史 | 独立 `KubernetesTaskV1` 与[回放样本](../internal/pipeline/testdata/kubernetes_v1_history.json) | 独立 Temporal namespace 中的 Worker 更换、离线信号和 51 条历史事件回放 |

这些结果是**各接缝的验证**；尚未在一套完整企业任务中联通初始化、开发、审查、联调、发布与合并。详细命令和观察值见 [Runner 隔离验证记录](../deploy/k8s-runner/README.md)、[联调夹具](../deploy/k8s-runner/integration-fixture/README.md) 和[迁移设计](kubernetes-migration.md)。

## 当前不足

1. **业务流程尚未整体迁移。** 现在的 Kubernetes Workflow 运行确定性离线 Runner；真实 OpenCode 调用和精确会话续接已单独验证，但还没有替换完整企业任务的 Agent 阶段。受管初始化和 Kubernetes 联调已有执行契约或资源包，尚未接入项目配置和业务 Workflow。
2. **远端写入尚未接入完整流程。** 可信 GitLab push、MR、merge 模块已完成并测试；旧 Node 路径中的 Agent 写入方式仍存在。必须完成凭证收拢和执行权核对，才能开放自动跨 Pod 恢复。
3. **跨节点恢复尚无生产级证据。** kind 测试验证了同一节点的 Pod 更换；没有验证目标 StorageClass 的跨节点重新挂载、节点隔离及失联进程终止。Job 和 Pod 从 API 消失不足以证明旧节点上的进程已停止。
4. **发布兼容性尚未验收。** 新 Workflow 的同版 Worker 更换和历史回放通过；混合版本滚动升级、完整旧历史兼容、数据库迁移窗口及回滚仍需验证。
5. **部署仍需定型。** 正式环境需要具备 NetworkPolicy 执行能力的 CNI、合适的 PVC 存储类、对象存储、镜像仓库、最小权限的控制面身份，以及隔离的 PostgreSQL 和 Temporal 配置。现有清单和文档用于验证，不代表生产部署已就绪。

## 后续路线与切换条件

1. 在隔离项目中把真实 Agent、工作区准备、审查、发布和结果报告接成完整的 Kubernetes 任务；确保 Runner 不持有代码平台写凭证。
2. 将可信远端写入模块接入 Workflow，按执行权、固定提交和目标核对 push、MR 与 merge；重复测试响应丢失和取消竞态。
3. 把受管初始化和单容器、多容器联调接入相同执行契约，覆盖成功、失败、取消、报告保留和清理。
4. 在可安全重新挂载 PVC 的**隔离多节点环境**验证节点故障与旧进程隔离；仅在旧执行确认停止、检查点有效时开放自动接管。
5. 完成数据库兼容迁移、Workflow 历史回放、混合版本滚动升级和回滚验收，再决定是否把 Kubernetes 设为企业主线默认路径并收敛 Node 专属实现。

现在不需要为现有默认运行路径准备 Kubernetes 集群。未来正式验收既可使用云托管集群，也可使用自有服务器；选择时应先确定数据边界、运维责任和跨节点存储能力。公司独立快照保持原边界，不连接公司 GitLab，也不迁入其运行中的 Task 或 Workflow 历史。临时 DeepSeek 测试密钥的本地副本和 Kubernetes Secret 已删除；**密钥所有者仍须在 DeepSeek 后台撤销该密钥**。
