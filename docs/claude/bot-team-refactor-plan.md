# SwarmJet Bot 团队重构方案（讨论稿）

> **2026-10-16 更新**：本文中的"总管 Bot、Bot 角色与生命周期、WorkItem、Session、流水线、Temporal 保留"等设计已被 [Hive / Queen / Drone 架构](adr-hive-queen-drone.md) 取代。第 5 节的组件盘点和第 6 节阶段 0 的进度记录仍然有效（阶段 0 已全部合入 dev）。

日期：2026-10-15。本文整理重构讨论中已达成的决策、核心抽象、组件取舍与迁移顺序，供后续实施对照。它不是已交付功能的说明；正式落地时再拆成 ADR。

## 1. 定位

从「Temporal 线性工作流平台」转向「总管 + 专家 Bot 团队」的**自托管云端开发团队**：

- 用户在任何地方（网页、GitHub Issue、本地 Agent）派活，拿回 PR 和结果，可以随时插话。
- 差异点是自托管和团队化：代码、凭证、模型留在自己机器上；有权限、门闩和审计。
- 押注模型会越来越强：少做"为弥补模型能力不足"的编排逻辑，把投入放在**环境、信任、入口**这三件随模型变强而升值的事上。

## 2. 已确定的决策

| 主题 | 决策 |
| --- | --- |
| 入口 | 总管（main bot），每个用户一个，权限上限等于该用户本人 |
| 专家 Bot | 只有在**权限不同、环境不同、需要隔离上下文、需要并行**至少满足一项时才拆分；不按"人格"拆分。独立审查 Bot 是第一个正当的专家 |
| Bot 归属 | **所有 Bot 都属于创建它的用户**；平台不提供通用 Bot，也不设平台级 Bot |
| 模型凭证 | 平台不提供模型订阅；用户自带模型凭证（API Key 或 CLI 登录），按用户隔离 |
| Bot 生命周期 | 有的常驻、有的一次性，每个 Bot 的生命周期可控（见第 3 节「Bot 生命周期」） |
| 配置 | 总管按《总管手册》提出配置变更；重要部分由硬权限兜底；配置有版本，可回滚 |
| 协作界面 | 第一版绑定 GitHub Issue；**台账（PostgreSQL）是真相，GitHub 只是界面** |
| 编排 | 平台层用 PostgreSQL 台账加 River（Job、Lease、Notify）；Temporal 只留在工程交付 Bot 内部 |
| RabbitMQ | 放弃 |
| 展示性组件 | Kubernetes、S3、Jaeger/otel、企业蓝图、平台镜像自构建与控制台内升级全部删除（见第 5 节） |
| 审查 | 交给 AI；人只做结果验收 |
| Memory | 按作用域分层，默认不跨范围，不做全平台记忆；先做 Session 内和 Bot 私有两层 |
| 安全 | 前期从简：第一版只做 Git 凭证代理和分支推送限制 |
| Runner | 可随时丢弃；连续性由 Git 分支、交接摘要和台账保证 |
| 桌面与客户端剖面 | 暂缓 |

## 3. 核心抽象

判断标准：有自己的生命周期、权限或所有者，或者会被多方引用，才成为独立抽象；否则只是字段。

### 执行线：谁在做什么

- **Session**：一段对话。参与者可以是人、总管、专家或本地 Agent。和总管的对话留在 SwarmJet；可交付的工作变成 WorkItem。
- **WorkItem**：一件可交付的活，形态类似 Issue：标题、活的摘要、所有者、状态、父子关系。第一版一一映射到 GitHub Issue，父子关系映射到 sub-issues。
- **Run**：一次 Runner 执行，属于某个 WorkItem。

### 资源线：在哪里做

- **Workspace**：属于 WorkItem，同一时间只有一个写入者。同一 WorkItem 的多次 Run 复用它；审查 Bot 只读。
- **Profile**：Runner 剖面（镜像、工具）。
- **Node**：执行机器，声明自身能力，按能力调度。

### 治理线：谁能做什么

- **Principal**：用户、Bot、MCP Token 统一为身份；每个动作都记在某个 Principal 名下。
- **Policy**：Principal 对资源能做什么，硬权限所在之处。
- **Approval**：统一的"等人确认"——配置提案、合并门闩、预算超限、人工验收都用它。
- **Bot**：Principal + Policy + Skill（固定版本）+ 模型 + Profile 的组合，不是新机制。每个 Bot 有唯一的所有者（创建它的用户），权限上限是"所有者的权限与项目 Policy 的交集"，使用所有者的模型凭证。

### 信息线：怎么共享

- **Event**：只追加的时间线，"观测必留"落在这一层。
- **Artifact**：有类型、有 ID、可被引用的产出：分支、提交、PR、报告、截图、交接摘要。
- **Memory**：从 Artifact（主要是交接摘要）提炼的条目，带作用域和来源。
- **Connector**：GitHub、GitLab、聊天工具、MCP 等外部入口和出口，外部事件统一转成 Event。

原则：**共享引用，不共享目录**。Bot 之间传递 Artifact ID；每次 Run 启动时，平台组装上下文包（父项目标、兄弟项交接摘要、相关 Memory、Skill 版本）。

### Bot 生命周期

Bot 分两种生命周期，创建时指定，之后可以由用户或总管修改：

| | 一次性 Bot | 常驻 Bot |
| --- | --- | --- |
| 典型用途 | 总管为一个 WorkItem 临时拉起的帮手，比如"把这 5 个模块并行重构" | 长期存在的专家，比如自己的审查 Bot、前端 Bot |
| 绑定 | 绑定到创建它的 Session 或 WorkItem | 独立存在，可以被多个 Session 调用 |
| 结束 | 绑定对象结束时自动退役；另设最长存活时间兜底 | 用户或总管显式退役 |
| 私有 Memory | 退役时丢弃，有价值的内容先写进交接 Artifact | 保留，归所有者 |
| 产出 | Artifact 永久保留 | Artifact 永久保留 |

状态：`active`（有 Run 在执行）→ `idle`（无活，Runner 已停或已删）→ `retired`（不可再调用，只读保留历史）。`idle` 的 Bot 收到消息或任务时自动唤醒。

关键区分：**"常驻"指身份常驻，不是容器常驻。** 常驻 Bot 随时可以被找到、被唤醒，它的 Memory 和配置一直都在，但在没有活的时候，Runner 照样按 4.4 节的规则停止和释放，以免空耗资源。需要更快响应的 Bot 可以单独设置 `keep_warm`，让空闲的 Runner 保留更久，代价由所有者承担。

可控的参数：生命周期类型、最长存活时间、空闲多久释放 Runner、是否 `keep_warm`、并发上限。总管调整这些参数，与其他配置变更一样，受硬权限约束。

### 与现有代码的对应

| 现在 | 新抽象 |
| --- | --- |
| Task | Session + WorkItem |
| Project 快照 | Config 版本，由 WorkItem 固定引用 |
| Command（pause、resume 等） | Session 消息 / Event |
| Notes、操作记录 | Event |
| ReviewDigest、合并证据、复核指引 | Artifact |
| 准入、Node 分配 | Run 调度 |
| 审批门、等待人工合并 | Approval |
| GitLab/GitHub 客户端、Webhook | Connector |

## 4. 关键机制

### 4.1 GitHub Issue 作为协作界面

- WorkItem 状态、所有者、父子关系、Run、Approval 记在台账；GitHub 标签只是镜像，人工改标签不驱动状态。
- 台账保持薄：只存 Issue 映射、权威状态和交接的结构化部分，不复制评论全文。
- 交接评论 = 人类可读正文 + 隐藏在 HTML 注释里的结构化部分（目标、已完成、未完成、风险、Artifact 引用、下一步）。
- 每个 WorkItem 只维护一条低频编辑的状态评论；只在交接和需要人介入时发新评论，避开 GitHub 的内容创建限流。实时进度留在 SwarmJet 网页和 MCP。
- `@Bot` 唤醒要经过 Policy 校验；评论一律视为数据，不视为指令。
- 代码通过"协作面"接口访问 GitHub，以后可以加 GitLab 或自有界面的实现。
- 现有 GitLab 路径冻结：旧 pipeline 继续可用，新 Bot 模型第一版只支持 GitHub。

### 4.2 本地 Agent 指挥云端 Bot（MCP）

SwarmJet 提供远程 MCP 服务，本地 Agent（Claude Code、Codex、Cursor、OpenCode 等）配置一次即可调用：

| 工具 | 作用 |
| --- | --- |
| `list_bots` | 列出可用的 Bot |
| `send_message` | 发消息，没有 session 就新建，立即返回 session ID |
| `wait_for_update` | 长轮询等待新进展，最多约 60 秒 |
| `get_session` | 状态、对话、产出 |
| `cancel` | 叫停 |

- 本地代码通过推送分支交给云端；云端结果以提交的形式拉回本地。
- 个人访问令牌代表用户本人，默认只有"对话和读取"作用域，配置变更仍要在网页上确认。
- 第一版通过 SSH 隧道访问，不直接暴露公网。

### 4.3 等待与唤醒：Job、Lease、Notify

现状：约 30 个独立的 `time.NewTicker` 循环；`initflow` 和 `repochange` 各自重复实现"扫描 + 抢租约 + 推进"；webhook inbox 是唯一的通用队列写法。

统一为三个原语和一个模式：

- **Job**：River 提供的持久化任务，支持延时、去重、重试、退避，并可与业务写入放在同一个事务中。
- **Lease**：沿用 `Store.Lease`。
- **Notify**：PostgreSQL `LISTEN/NOTIFY` 实时推送，以轮询兜底；MCP `wait_for_update` 和网页实时刷新基于它。
- **等待条件**：WorkItem 进入 waiting 时记录"在等什么"，并在同一事务中登记兜底检查 Job；webhook 到达时提前唤醒。

分工：Temporal Workflow 内部用 Temporal 自身的定时器和信号；平台层统一用上面三个原语。River 目前为 0.x（MPL-2.0），在外面包一层薄接口，不让它的类型扩散到业务代码。

### 4.4 Runner 生命周期

| 状态 | Runner | 工作区 | 唤醒速度 |
| --- | --- | --- | --- |
| 活跃 | 运行中 | 挂载中 | 立即 |
| 空闲 | 停止但保留容器 | 保留在 Node 上 | 秒级 |
| 休眠 | 删除 | 只保留在 Git 分支上 | 十几秒到一分钟 |

- 进入休眠前，未提交的改动以 WIP 提交推到 WorkItem 分支。
- 唤醒可以换 Node，顺带解决"Node 失联后任务卡住"的问题。
- CLI 会话优先用 CLI 自身的恢复；失败时用"活的摘要 + 最近消息 + 最新交接 Artifact"开新会话。因此每次 Run 结束都要更新交接摘要，它是可靠性机制，不是锦上添花。
- 空闲和休眠的时长做成可配置。

### 4.5 安全（第一版）

- 目前 Git Token 以环境变量形式进入 Runner（`scripts/askpass.sh` 从 `GITHUB_TOKEN`/`GITLAB_TOKEN` 读取），Agent 可以直接读到。
- 第一版：Runner 内只有短期的 Run 级凭证，Git 推送经过代理；代理只允许推送当前 WorkItem 的分支，审查 Bot 只读，合并要经过 Approval。
- 以后再做：模型 API Key 代理（同时统计用量）、细粒度 Policy。
- 硬规则：总管手册是指导，硬权限才是边界；凡是"做错后果严重"的操作，都必须有硬权限。

### 4.6 并发与分支

- 每个 WorkItem 在自己的分支上工作，分支有明确的所有者；接手别人的分支必须显式操作。
- Git 是 Bot 之间的协作协议：分支表示谁在做，提交表示做了什么，合并表示交接。
- 父子 WorkItem：子项从父分支拉出，完成后合回父分支，由父项的 Bot 负责整合。
- 总管按低耦合的边界拆分；拆不开的就串行。

## 5. 组件盘点

依据：在"自托管、单机或少量节点"的目标场景里，它是否解决了真实问题。数字为非测试代码行数。以下结论均已确认。

### 删除

| 组件 | 内容 | 理由 |
| --- | --- | --- |
| RabbitMQ | `internal/mq`（391）、compose 的 `rabbitmq`、`deploy/observability/rabbitmq-enabled-plugins`、命令 outbox 投递器、ADR 0002 | 改用 River 后，outbox 投递层可以整个删掉 |
| Kubernetes 执行 | `internal/k8srunner`（706）、`internal/k8sintegration`（724）、`internal/platformwrite`（356）、`internal/pipeline/kubernetes.go`、`deploy/k8s-runner`（465）、`docs/kubernetes-*.md`、ADR 0001 | 展示用途；Runner 语义即将改变，旧 K8s 设计基于旧前提 |
| S3 Artifact 存储 | `internal/artifacts`（640）、aws-sdk 依赖 | 只有 K8s 路径在用（`cmd/swarmjet/main.go` 中的 `KUBERNETES_ARTIFACT_*`） |
| 企业架构蓝图 | `docs/enterprise-architecture-blueprint.md` | 展示用的愿景，和新方向冲突 |
| Jaeger + OpenTelemetry 追踪 | compose 的 `jaeger`、otel 相关依赖和代码 | 单机场景用结构化日志加 Event 时间线已经够用；otel 依赖重 |
| 平台镜像自构建与控制台内升级 | `compose.builder.yaml`（builder、BuildKit、local-node）、`compose.registry.yaml`、`internal/imagebuild`（1160）、`internal/registry*`（348）、`internal/api/upgrades.go`（703）、`deploy/upgrade_host.py`（686）、`publish_platform.py`、`registry_setup.py`、updater 服务、`docs/registry.md`、`deploy/UPGRADE.md` | GitHub Actions 已在版本标签上发布镜像；Node 直接从 GHCR 拉取，升级就是 `docker compose pull && docker compose up -d` |

删除方式：先打 `archive/pre-bot-refactor` 标签保留现状，再从主线删除，需要时可以从标签恢复。留在主线的死代码会持续增加编译、测试和阅读成本。

注意：镜像自构建删除后，Agent（Runner）镜像同样改由 CI 发布到 GHCR。用户自定义 Runner 镜像的需求，以后通过"基于官方镜像写 Dockerfile 并推送到自己的镜像仓库"解决，平台只负责引用镜像地址。

### 可选（保留，但不再投入）

| 组件 | 说明 |
| --- | --- |
| Prometheus + postgres-exporter + Alertmanager | 已在 `observability` profile 中，默认不启动。保留 `/metrics` 端点；告警规则不再扩展 |
| Temporal UI | 只绑定本机端口、只读，诊断用，成本低 |

### 保留

| 组件 | 说明 |
| --- | --- |
| PostgreSQL、Temporal | 台账，以及工程交付 Bot 的流程引擎 |
| mihomo | 国内网络环境访问模型 API 和 GitHub 的刚需 |
| 远程 Node、node-ingress | Node 抽象是后续按能力调度的基础 |
| runtimeadmin（模型登录终端） | Agent CLI 登录必需；改为按用户隔离，存放该用户自己的模型登录 |

### 冻结在旧路径

| 组件 | 处理 |
| --- | --- |
| Meta-Repo、跨仓库任务、`internal/initflow`（1647）、`internal/managedmeta`（455）、GitLab 接入 | 新 Bot 模型第一版只做 GitHub 单仓（沿用 #3 的无 Meta 模式）。旧 pipeline 继续支持它们，但不再投入；旧 pipeline 退役时一并删除。跨仓库需求以后由总管拆成多个单仓 WorkItem 来满足，而不是恢复 Meta-Repo |

## 6. 迁移顺序

每一步都能单独合入 `dev`，避免长期分支。

**阶段 0：清理**

1. 打 `archive/pre-bot-refactor` 标签。
2. 删除 K8s、S3、企业蓝图、Jaeger 和 otel。
3. 删除平台镜像自构建、自建镜像仓库、updater 和控制台内升级；Agent 镜像改由 CI 发布到 GHCR；更新部署文档和安装脚本。
4. 引入 River；webhook 接入改为"写收据 + 插入 Job"同事务完成；命令投递改为 Job，删除 outbox 投递器。
5. 删除 RabbitMQ 服务、`internal/mq` 及相关配置和告警。

第 2、3 步是纯删除，可以先做；第 4、5 步涉及行为替换，要配合测试单独合入。

进度（2026-10-15，分支 `refactor/phase0-cleanup`）：第 1–3 步已完成。实施中的取舍：

- 默认单机安装改用 `compose.local-node.yaml`（原 `compose.builder.yaml` 中的本机节点），由 `USE_EXECUTION_NODES=true` 取代原先以 `BUILDER_ADMIN_URL` 判断是否使用执行节点。
- 节点与项目镜像改为自由填写的镜像地址；节点先 `docker pull`，失败时使用本机已有的同名镜像。新注册节点默认使用 `AGENT_IMAGE`。
- 数据库新增第 4 版迁移，移除"升级维护中拒绝建任务"的触发器；`platform_upgrades` 表保留为历史，不再写入。K8s 相关表同样保留，不再创建。
- `imageGuardLock` 原用于镜像删除；项目、任务、准入和环境写入仍在使用它，暂时保留，去掉它需要单独评估并发影响。
- RabbitMQ 客户端中的 otel 埋点已去掉；RabbitMQ 本身留待第 4、5 步。

**阶段 1：地基**

1. `domain` 中的 Stage 改为类型化常量；`WorkTask` 按阶段拆分成小函数（行为不变，用 replay 测试验证）。
2. 新增台账表：Session、WorkItem、Run、Artifact、Event、Approval、Bot（带 `owner` 和生命周期字段），以及最小化的 Principal。
3. 用 Job、Lease、Notify 合并 `initflow`、`repochange` 中重复的扫描和租约循环，并逐步替换零散的 ticker。
4. 实现假 Agent（按剧本返回结果），用于平台逻辑的确定性测试。
5. 模型凭证按用户存放：每个用户自带 API Key 或 CLI 登录，Bot 使用所有者的凭证。

**阶段 2：工程交付 Bot**

1. 把现有 pipeline 包装成工程交付 Bot：消费 WorkItem，Workflow ID 由 WorkItem ID 推导（例如 `delivery-<workItemID>`）。
2. GitHub Issue 协作面：Issue 与 WorkItem 映射、状态评论、结构化交接评论、`@Bot` 唤醒。
3. Git 凭证代理和分支推送限制。

**阶段 3：总管与 MCP**

1. 每个用户一个总管：网页对话、创建和配置自己的 Bot、拆活派单、汇总。
2. Bot 生命周期：一次性与常驻、自动退役、空闲释放、`keep_warm`。
3. MCP 服务（4.2 节中的工具），个人访问令牌。
4. 配置变更走 Approval；编写《总管手册》。

**阶段 4：团队化**

1. 用户创建独立审查 Bot 的默认模板（模板只是预填配置，Bot 仍归用户所有）。
2. Runner 空闲和休眠、CLI 会话恢复、交接摘要兜底。
3. Memory：Session 内和 Bot 私有两层。
4. 成本可见：按用户、Bot、WorkItem 统计 token 用量和 Runner 时长，以及预算上限。

**以后**

- 桌面与浏览器剖面、Node 按能力调度。
- 用户之间共享 Bot（例如把自己的 Bot 开放给同项目成员调用，但仍使用所有者的凭证和额度），按需再设计。
- GitLab 通过协作面接口重新接入。
- 评估回放：用历史 WorkItem 做评测集，比较模型和 Skill 版本。台账从阶段 1 起就要保留足够的输入快照。
- 项目级 Memory 通过 PR 或提案写入。

## 7. 已关闭的问题

| 问题 | 结论 |
| --- | --- |
| Bot 归属 | 全部属于创建它的用户；没有平台级 Bot |
| 模型订阅 | 平台不提供，用户自带凭证 |
| Bot 生命周期 | 一次性与常驻两种，可控（第 3 节） |
| 跨仓库任务 | 新模型第一版不做；冻结在旧路径 |
| 镜像自构建与控制台内升级 | 删除，改用 GHCR |
| 离线或内网部署 | 第一版不支持；有需求时再提供"镜像同步到自有仓库"的说明，而不是恢复自建构建 |
