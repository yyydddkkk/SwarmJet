# Kubernetes 执行架构迁移

状态：离线纵向验证代码已接入，各接缝已分别验证；完整跨组件运行和企业任务迁移尚未完成。决策见 [ADR 0001](adr/0001-kubernetes-execution-direction.md)。

当前可用范围、验证证据和剩余门槛见[接入阶段总结](kubernetes-status.md)。

## 已实现的离线纵向验证（2026-09-27）

- PostgreSQL 持久准入保存执行池、资源和镜像摘要快照；独立执行表保存稳定 execution ID、递增执行权版本和 Job 尝试身份。数据库测试覆盖并发准入、取消、撤权、接管和结果提交。
- `KubernetesTaskV1` 使用独立 Temporal task queue，与旧 Workflow 的 Worker 隔离。当前只运行确定性离线 Runner：一项执行创建一个固定摘要的 Job，`backoffLimit: 0`，最小权限、资源限制、任务 PVC 和默认拒绝网络策略。Pod 完成后，控制面核对任务、执行权、Pod 身份与输出，再保存结果、日志、报告引用。控制台按 SHA-256 校验读取，保留期清理先核对报告。
- [一次性 kind 实测](../deploy/k8s-runner/README.md)使用 Cilium：先确认 HTTP 探针可通，再确认 Runner 网络拒绝，最后确认定向放行可通。固定摘要 Job 成功运行，另一个 Pod 重新挂载 PVC 后读回同一结果。测试集群已删除。
- 使用临时测试凭证，OpenCode `1.18.31` 在两个不同 Pod 上以同一 PVC 和精确 `--session` ID 续接；第二个 Pod 正确返回首轮会话的标记。测试资源已清理。
- [可信远端写入模块](../internal/platformwrite/reconciler.go)持久化固定 SHA 意图，持有执行权行锁期间执行 GitLab push、MR 和 merge；响应丢失时先查询远端事实。一次性本机 GitLab 实例实测三步成功及各自的响应丢失对账，实例已删除。离线 Runner 不获得 GitLab 写凭证。该模块尚未接入完整企业 Workflow。
- [Kubernetes 联调执行包](../internal/k8sintegration/integration.go)以受限服务 Pod、检查 Job 和任务网络策略替代 Compose 执行资源。在一次性 kind+Cilium 集群中，单容器检查、多容器检查、预期失败与取消清理均通过；报告持久化失败时保留现场。它尚未接入项目配置和业务 Workflow。
- 新 Workflow 在独立 Temporal namespace 经 Runner 故障、Worker 更换和历史回放验证；[历史样本](../internal/pipeline/testdata/kubernetes_v1_history.json)作为后续兼容检查语料。

当前路径只在显式设置 `KUBERNETES_OFFLINE_VALIDATION=true` 时启用，默认企业任务仍使用旧 Node/本地执行路径。对象存储可使用 S3 兼容私有桶；本地 `FileStore` 仅用于隔离验证。

**尚未达到默认路径切换门槛：**受管初始化、单容器和多容器联调接入业务 Workflow、跨节点存储与节点隔离、自动接管、完整 Workflow 的 GitLab 写入收拢、混合版本滚动升级均未完成。真实会话已在隔离 Pod 中验证，但尚未作为完整任务 Agent 流程交付。即使 Job 和 Pod 从 API 消失，也不能单独证明失联节点上的进程停止，因此当前中断后阻断自动重跑。现有任务和数据不自动迁移。

## 定位与迁移边界

SwarmJet 面向企业私有部署，Kubernetes 是目标生产执行环境。代码更新不等于已验证的生产发布或数据迁移；部署升级仍须核对镜像摘要、数据库与 Temporal 版本，并完成备份和验证。

Kubernetes 迁移在隔离的数据库、Temporal namespace 与集群环境验证；现有部署中的 Task、Workflow 历史或 Node 工作区不会因升级而原地迁入新架构。旧任务的处理须由部署方制定单独方案。新执行模型不以永久保留双后端为前提。

## 现有模型与迁移阻断

| 接缝 | 当前事实 | 迁移时必须解决 |
| --- | --- | --- |
| 任务归属与准入 | `tasks.node_id` 在准入时固定；Node 以心跳、容量和镜像就绪参与分配。 | 将任务准入、执行池选择与 Pod 实际放置分开；K8s 数据模型不再把 Pod/物理节点写成 `node_id`。 |
| 执行现场 | `execution.Node` 同时拥有工作区、Agent、Compose 联调、报告和清理；Activity 按 `NodeID` 路由。 | 定义可跨 Pod 定位的任务现场及其生命周期；流程不依赖某台宿主机持续在线。 |
| Runner | 每次 Agent 执行由本机 Docker 容器承担，记录包括宿主路径、容器 ID 和日志路径。 | 保留稳定的业务执行 ID 与会话语义；明确一次 Runner 的 Pod 生命周期、观察、取消及重试接管。不能把 Pod/Job 重试当作业务恰好执行一次。 |
| 工作区与报告 | 任务目录、CLI 会话、报告主要在 Node 的 `DATA_DIR`。 | 在选择 PVC、对象存储检查点或可重建 Git 工作区前，先验证各阶段如何共享状态、如何恢复、如何清理；新任务报告需脱离单个 Node 可读取。 |
| 联调与初始化 | 联调使用 Node 上的 Compose；受管项目初始化也有独立的 Node 分配路径。 | 为多容器验收和初始化定义集群运行方式与权限，不能只迁移普通 Task 的 Agent 路径。 |
| 外部写入 | Agent/平台可能推送、创建 MR/PR 或合并。 | 旧执行失去执行权后不得继续写远端；重试前后核对固定提交、目标与远端事实。 |

以上事实可从 [`execution.Node`](../internal/execution/node.go)、[Activity 路由](../internal/pipeline/activities.go)、[准入与分配](../internal/store/store.go)、[Agent Runtime](../internal/agent/runtime.go)、[任务与执行模型](../internal/domain/types.go) 和[现有 Node 设计](execution-nodes.md)核对。现有 `AgentBackend` 只覆盖 Agent 生命周期，其 `Workspace` 和 `LogPath` 仍指向执行主机路径，不能独自承担 K8s 迁移。

## 目标执行契约

以下是新架构必须保证的行为，具体接口和存储方案在首个纵向验证后定型。

1. **任务与执行分离。** Task 记录业务状态、项目快照和选择的执行池；每次 Agent 执行有稳定的 `executionId`，每次取得执行权有单调递增的版本。Kubernetes 选择物理节点，SwarmJet 不维护第二套物理节点调度器。
2. **准入先于占用资源。** 持久队列决定何时允许任务运行、使用哪个执行池和配额；等待人工操作的任务不占活跃 Runner 槽。资源请求与限制在创建工作负载前确定，并可解释拒绝原因。
3. **执行权可核对。** 同一任务在任一时刻至多一个有效写入者。旧 Pod/Node 恢复、网络分区、取消或超时后，平台在提交远端副作用前再次核对执行权和远端状态；Pod 状态本身不是业务完成证明。
4. **现场可恢复或明确阻断。** 工作区、Agent 会话、结果和报告各自声明保存位置、校验方式与恢复边界。无法安全恢复时保留证据并阻断，不能静默从空现场重跑。
5. **凭证按阶段交付。** Runner 只获得当前任务与阶段所需权限，不挂 Docker socket，也不获得集群管理权限；平台可信阶段负责远端写入的核对与执行。现有 GitLab Agent 经 `glab` 写远端的路径必须在自动跨 Pod 恢复前收拢，否则旧 Pod 失去执行权后仍可能写入。
6. **新架构自身可升级。** K8s 版本的数据库与 Workflow 变更仍需兼容窗口、历史回放和滚动升级验证。旧 Task 不因代码升级自动迁入新的执行模型。

这里的“执行池”表示项目可选择的一组资源与安全能力，不等于一台 Node；“Runner”仍表示一次 Agent 执行的业务单位，其 K8s 承载形式要经纵向验证决定。在实现与术语确定前，[当前术语表](../CONTEXT.md)仍描述现有 Node/Runner，不提前改写为已交付语义。

## 正式接入前的决策与验收

按以下顺序做纵向验证，每一步输出可运行证据，不先扩展成完整集群平台：

1. **建立迁移基线。** 记录当前运行版本、镜像摘要、数据库和 Temporal 版本，完成备份与现有路径回归。Kubernetes 验证使用独立测试数据，不连接生产运行环境。
2. **固定恢复场景与兼容语料。** 选一个真实但可重复的项目，覆盖受管初始化、开发、审查、联调、推送、MR/PR、取消和清理；记录 CLI 登录/会话、网络、镜像及资源要求。定义故障注入点：Pod 启动前、执行中、远端写入成功但确认丢失、旧执行恢复。
3. **先实现最小纵向链路。** 从持久准入到创建一个受限 Runner、读取结果和集中报告，验证工作区与会话方案；再扩展至联调、初始化和多阶段任务。明确 Pod/Job 生命周期与 Workflow 的职责分工。
4. **证明执行权与外部写入安全。** 收拢 GitLab/GitHub 远端写入权限后，注入超时、重试、节点故障及取消；检查不会出现两个有效写入者、重复推送/合并或丢失的已完成操作。失败时任务状态与人工恢复动作必须可见。
5. **收敛旧模型。** 在默认路径切换前处理 Node 专属准入、宿主路径与 Docker 联调依赖，完成 K8s 自身的升级、报告和回滚验收。

在纵向验证前，不预设每个阶段独立 Job、单任务长寿命 Pod、PVC 类型或对象存储供应商；这些选择都必须接受会话恢复、联调和故障测试。Kubernetes Pod/Job 的重试与清理语义见[官方 Job 文档](https://kubernetes.io/docs/concepts/workloads/controllers/job/)。
