# SwarmJet 企业级架构蓝图（讨论稿）

日期：2026-09-25。本文描述未来目标与演进门槛，不表示功能已经交付。暂按“企业私有部署优先，保留多企业 SaaS 扩展能力”设计；部署形态仍待确认。

## 1. 产品定位与边界

SwarmJet 是 AI 开发任务的控制与执行平台：接收需求，编排 Agent、工作区、验收和审查，治理容量与成本，并记录从需求到变更交付的证据。GitLab、GitHub 等代码平台继续负责仓库、Issue、MR/PR、CI 和分支权限；SwarmJet 通过托管平台 adapter 与它们协作，不建设代码托管服务。

生产级能力以可验证结果定义：突发接入可排队、任务状态可恢复、外部写入可核对、团队间资源可隔离、历史任务可跨版本运行、运维人员能沿同一任务链路定位故障。大规模的两个量需要分别设计：短时间进入的需求数，以及同时占用 CPU、内存、磁盘和模型额度的 Runner 数。

## 2. 现状与主要演进接缝

- Temporal 管理任务流程，PostgreSQL 保存配置、查询投影、命令与租约；任务快照固定项目配置。已有单机 Docker 和远程 Node 实现，`execution.Node` 封装工作区、Agent、验收、报告与清理。
- Webhook 目前主要唤醒扫描；扫描与 outbox 投递共用服务循环。建单使用全局分配锁；节点容量按非终态任务计数。这些是高突发负载下首先测量的接入与调度接缝。
- 任务固定归属 Node。节点失联后保留现场并阻断；工作区、会话跨节点恢复和旧节点恢复后的执行权校验尚未完成。历史报告依赖原节点在线。
- 控制台当前以单管理员会话为主，已有结构化日志和健康检查；尚需完整的身份授权、指标、追踪、成本计量及告警体系。

现有实现分别见 [实现约定](architecture.md)、[执行节点](execution-nodes.md)、[托管平台接口](../internal/forge/client.go) 和 [建单逻辑](../internal/store/store.go)。

## 3. 目标架构

| 模块 | 长期职责与数据归属 |
| --- | --- |
| 接入与控制 API | 身份认证、项目配置、权限、查询和人工命令；可部署多副本，不依赖本机任务现场。 |
| 事件接入与对账 | 验证 webhook，先持久化再应答，按平台事件 ID 去重；周期对账补漏，独立扩容。 |
| 准入与调度 | 区分已接收、排队、已分配和正在占用资源的任务；执行团队/项目配额、优先级、公平性与背压。 |
| Temporal Workflow/Worker | 保存长任务决策与等待，按能力分配 Worker；外部副作用仍由可核对、幂等的 Activity 执行。 |
| 执行后端 | 本地 Docker 供开发和单机部署；Kubernetes 后端管理生产 Runner Pod/Job、资源申请、网络和清理。 |
| 持久数据 | PostgreSQL 管理配置、准入、审计和查询；Temporal 管理流程历史；对象存储保存报告及可恢复的工作区/会话检查点；代码平台保存代码和 MR/PR。 |
| 观测与治理 | 统一追踪、指标、日志、审计、资源用量与模型用量，按任务和团队查询。 |

控制面与执行面独立扩缩。Kubernetes 接入时重新审视 `execution.Node` 的固定节点语义：项目选择执行池和能力，Kubernetes 负责 Pod 放置；任务记录执行后端、镜像摘要、工作区和执行权版本，不重复实现一套物理节点调度器。

## 4. 优先设计的四个能力

### 4.1 突发接入、容量与成本

接入层将事件写入有界、可观测的 inbox，ACK 不依赖完整 GitLab/GitHub 扫描。周期对账读取远端事实；持久化去重阻止重复接单。准入层限制每个团队、项目及平台连接的排队量和并发量，按 Runner 的 CPU、内存、临时存储、镜像及模型能力选择执行池。等待人工操作或依赖的任务不占 Runner 槽。

成本账本分别记录 CPU 秒、内存占用时间、存储字节时间、模型 token/费用与外部 API 请求。未提供精确用量的 Agent 或模型标记为“未知”，不估造账单。软额度提示与硬额度拒绝的行为分别配置，并保留管理员覆盖及审计。

### 4.2 不可信代码隔离与执行恢复

Agent 与仓库脚本视为不可信执行负载，但按代码来源区分威胁等级：企业私有部署只接入获授权的内部仓库；多租户 SaaS 可能执行互不信任客户提交的任意脚本。Kubernetes Runner 不挂宿主 Docker socket，不获得集群管理权限；使用最小 ServiceAccount、资源限制、默认拒绝网络策略、短期凭证和固定镜像摘要。多企业共用集群时再要求沙箱运行时或专属节点；Kubernetes 通过 RuntimeClass 选择运行时，SwarmJet 设计准入、权限和恢复规则，不自研内核级沙箱。

为每种任务定义可审计的执行配置：允许读写的任务目录、持久化的 CLI 会话与临时目录、模型与 Git/依赖源的网络出口、可用的测试服务和资源上限。内部仓库使用受限容器配置；不可信客户代码使用经过验证的 gVisor/Kata 等沙箱或专属执行池。需要嵌套容器、FUSE、特殊系统调用或额外网络的验收任务，进入单独配置并经权限审批；能力不足时报告具体原因，不自动退回较弱隔离。

隔离可能改变工具兼容性和性能，因此以真实任务语料分别测试当前 Docker、受限 Pod 与候选沙箱：覆盖 Git clone/提交、各 Agent CLI 的登录与会话恢复、依赖安装、常见语言构建测试、Compose 联调替代方案以及失败清理。比较任务完成率、检查通过率、启动时间、文件 I/O、网络耗时和单位任务成本。保留相同任务输入与镜像版本，明确区分“代码或模型失败”和“隔离环境不支持”；未通过兼容验收的配置不进入默认调度。

远端写入逐步归平台可信阶段所有：Agent 在受限工作区修改并提交，平台核对固定 SHA 后推送、创建 MR/PR 与合并。GitHub 已部分采用此模式；GitLab 仍有 Agent 经 `glab` 操作远端的路径，应在不可信代码接入前收拢凭证与写入权。

Kubernetes Job/Pod 重试不能代表业务操作只执行一次。每次执行固定执行 ID 和执行权版本；远端推送、创建 MR/PR、合并之前及重试之后都核对预期提交、目标与远端状态。旧 Node/Pod 失去执行权后不能继续产生外部写入。工作区和会话需有可重建方案，报告集中保存；失败任务能在受控条件下转移执行位置。

### 4.3 高可用、升级与灾难恢复

API、接入器、Worker 可多副本部署；扫描任务需要租约或分片，防止各副本重复全量扫描。PostgreSQL 的主备、备份与时间点恢复服务于已定义的 RPO/RTO；只读副本仅在查询压力达到门槛后引入，涉及任务分配与租约的请求仍读取主库。Temporal 服务与持久化也需按故障域部署和演练。

长时间运行的 Workflow 必须跨版本安全：部署使用 Workflow 版本化或 Worker Versioning，CI 回放代表性历史；数据库采用兼容的分阶段迁移，Node 控制协议声明兼容窗口。滚动升级期间旧任务、旧节点和新 Worker 的组合进入验收矩阵。

### 4.4 可观测性与企业治理

传播 `traceId`、`taskId`、`executionId`、项目与代码平台标识，贯通 webhook → inbox → 准入 → Temporal → Runner → 远端 MR/PR。指标至少覆盖接入 ACK、接单等待、队列长度、准入拒绝、各阶段耗时、Runner 资源、节点失联、数据库锁等待、外部 API 限流与成本。高基数任务 ID 放在追踪和日志中，指标标签受基数预算约束；凭证不进入任何观测输出。

企业治理增加 SSO/RBAC、项目和团队边界、操作审计、凭证轮换、数据保留及导出。私有部署以一个组织为默认边界；若提供 SaaS，身份、资源、密钥、数据与观测都要按租户隔离。

## 5. 基础设施引入条件

| 能力 | 决定 |
| --- | --- |
| Temporal Task Queue | 继续承担 Workflow 与 Activity 调度；监控 backlog、Worker 槽位、任务率和历史大小，再扩 Worker/分队列。 |
| PostgreSQL 主备与备份 | 进入高可用阶段；明确故障切换、RPO/RTO 和恢复演练。 |
| Kubernetes | 作为生产执行后端，先以小流量验证 Pod/Job、工作区与安全隔离；保留 Docker 本地后端。 |
| 沙箱运行时 | 通过 RuntimeClass 或专属执行池选择；由威胁模型、兼容性语料和成本测试决定默认配置，不自行实现 Linux 系统调用隔离。 |
| 对象存储 | 报告脱离 Node、工作区/会话可恢复时引入，定义保留期和恢复校验。 |
| OpenTelemetry 与指标后端 | 在性能优化前落地，形成跨服务诊断和容量基线。 |
| Kafka/RabbitMQ 等事件队列 | 当事件需要多个独立消费者、长时间重放或 PostgreSQL inbox 无法满足已测吞吐时引入；任务编排仍由 Temporal 负责。 |
| Redis 与读副本 | 分别由可量化的缓存热点、只读查询压力触发；不承载任务执行权或最新写入判定。 |

实施时序：**现在**明确威胁模型、盘点 Agent 所需能力并建立兼容性测试语料，保留现有 Docker 后端及最小权限；**Kubernetes 试运行时**设计执行配置接口，接入候选 RuntimeClass、工作区/会话持久化与故障测试；**开放任意客户代码前**完成强隔离、平台拥有远端写入、租户级配额与真实攻击面验收。企业内部仓库也持续使用最小权限和资源限制。

## 6. 演进路线与阶段验收

1. **基线与可观测性：** 建立可重复的假 Git 平台、模型和 Runner 压测环境，并收集真实仓库的工具兼容性语料；记录吞吐、延迟、错误、资源和成本，定义目标 SLO、RPO/RTO 与容量模型。验收是能够解释瓶颈、等待原因和隔离造成的工具失败。
2. **接入与准入：** 分离 webhook、扫描、outbox 投递；持久化事件与去重，加入配额、公平队列、背压和排队可见性。验收是突发需求不丢失、不重复接单，也不无界启动 Workflow/Runner。
3. **可恢复执行后端：** 集中报告，定义检查点与执行权 fencing；以少量任务试运行 Kubernetes 后端及候选沙箱，验证工具兼容性、取消、超时、Pod 重试、节点故障与远端写入恢复。验收是故障后无重复推送/合并，任务能在明确规则下恢复，并达到预先设定的任务完成率。
4. **高可用与升级：** 多副本控制面、Temporal 与 PostgreSQL 故障演练、备份恢复、历史 replay、兼容迁移和灰度发布。验收以预先确定的 RPO/RTO 和升级期间任务连续性为准。
5. **企业治理与按需扩展：** SSO/RBAC、租户/团队边界、资源与模型预算、审计；开放不可信客户仓库前完成沙箱与凭证隔离门槛；依据压测结果决定事件队列、读副本和缓存的引入。

建议的演练场景包括：短时间注入一万个 Issue、不同团队争用有限 Runner、Git 平台返回 429、Pod 在推送成功后失联、旧 Node 恢复、数据库主库故障、Worker 带旧 Workflow 历史滚动升级。具体并发与延迟数值在确定部署形态和预算后填写，不把模拟测试当作真实外部服务验收。

## 7. 待确认的架构决策

- 部署形态：企业私有部署优先、双形态同等优先，或 SaaS 优先。
- 信任边界：任务仅运行企业内部仓库，还是运行互不信任客户的任意代码。
- 规模目标：需求突发量、同时活跃 Runner 数、单任务典型 CPU/内存/时长、项目及团队数。
- 故障目标：接入 ACK 延迟、排队时长、RPO/RTO、允许的人工恢复范围与预算。

参考：[Temporal Task Queue](https://docs.temporal.io/task-queue)、[Temporal 安全部署与 replay](https://docs.temporal.io/develop/safe-deployments)、[Kubernetes Job](https://kubernetes.io/docs/concepts/workloads/controllers/job/)、[Kubernetes RuntimeClass](https://kubernetes.io/docs/concepts/containers/runtime-class/)、[Kubernetes 多租户隔离](https://kubernetes.io/docs/concepts/security/multi-tenancy/)、[Kubernetes 资源配额](https://kubernetes.io/docs/concepts/policy/resource-quotas/)、[gVisor 兼容性](https://gvisor.dev/docs/user_guide/compatibility/)、[PostgreSQL 高可用](https://www.postgresql.org/docs/current/high-availability.html)、[OpenTelemetry 可观测性概述](https://opentelemetry.io/docs/concepts/observability-primer/)。
