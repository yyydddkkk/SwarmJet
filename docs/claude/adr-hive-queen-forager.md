# ADR：Hive / Queen / Forager 架构（草稿）

状态：草稿，待评审
日期：2026-10-16
取代：`bot-team-refactor-plan.md` 中关于"总管 Bot、Bot 角色、Bot 生命周期、WorkItem、流水线"的部分。阶段 0 的清理记录仍以那份文档为准。

## 1. 背景

SwarmJet 最初是一个"Issue 触发、Temporal 驱动的线性开发流水线"：开发 → 验收 → 审查 → 合并。阶段 0 已经删除了 Kubernetes、S3、OpenTelemetry、控制台内镜像构建与仓库、升级器，并用 River 替换了 RabbitMQ（均已合入 dev）。

后续讨论得出两个判断：

1. 本地 Agent（Claude Code、Codex 等）的编排能力会随模型快速进化。平台不应再自己写一个"总管"和它们竞争，而应把指挥权交给它们。
2. 但集群不能因为本地 Agent 断开就对外界失去反应：GitHub 上来了新 Issue、CI 失败、有人评审，都需要有人处理。

因此 SwarmJet 改为：**一个自托管的云端 Agent 集群。用户在本地用 Claude Code 或 Codex 指挥它；集群内每个项目有一个可选的常驻协调者，负责本地断开期间的判断和派活。**

## 2. 定位

> 在你自己的服务器上部署 SwarmJet，为每个仓库建一个 Hive。你本地的 Claude Code 选一个 Hive，和它的 Queen 对话；Queen 指挥一群 Forager，每只 Forager 在自己的 Cell 里干活；Guard 守住合并的底线；Queen 定期 Dream，把经验整理成记忆。

- **目标用户**：有自己服务器、本地已经在用 Claude Code 或 Codex、希望后台并行跑任务、又不想把代码和凭证交给 SaaS 的个人开发者和小团队。
- **性质**：开源、自托管。不是 SaaS，也不是云厂商。部署、凭证、成本和风险都由部署者自己承担。
- **差异化**：自托管；中立（任何本地 Agent 都能指挥，云端可以跑不同的 CLI）；集群内有协调者，本地离线时照常响应；Guard 强制守住底线。
- **演示场景**：晚上在终端里说"把这 5 个 Issue 并行修了"，然后合上电脑。第二天早上看到 5 个 PR，每个都经过独立审查、CI 全绿；中途 GitHub 上来的新 Issue，Queen 也已经接手。

## 3. 决策摘要

| # | 决策 |
|---|---|
| D1 | 放弃总管 Bot 和固定 Bot 角色。Agent 是通用的，专业化来自 Hive 的说明、Skill、预设和权限。 |
| D2 | 平台上有多个 Hive，彼此隔离。每个 Hive 有且只有一个 Queen，以及任意数量的 Forager。 |
| D3 | 本地 Agent 是客户端，不是 Hive 的成员。它通过 MCP 选择 Hive，默认和 Queen 对话，也可以直接和某个 Forager 对话。本地会话关闭不影响 Hive 运行。 |
| D4 | Queen 默认关闭，由用户为 Hive 开启。Queen 只能派 Forager、发消息、写自己的记忆、开 PR；不能合并 PR，不能改配置。 |
| D5 | Forager 之间不能直接发消息（初定），协作只经由 Queen 和 Git。 |
| D6 | 事件先经确定性的 Router：有归属的交给归属 Forager，匹配触发规则的新建 Forager，其余进 Hive Inbox，由 Queen（若开启）或人处理。 |
| D7 | 消息层使用 PostgreSQL（Inbox 表）+ River（投递）+ `LISTEN/NOTIFY`（唤醒），不引入新的中间件。 |
| D8 | 去掉 Temporal 和旧流水线。工作顺序由 Skill/Playbook 描述，底线由 Guard 强制。 |
| D9 | Forager 和 Queen 都是长期运行的 CLI 进程，跑在 Docker 容器（Cell）里，通过结构化协议驱动，不模拟终端，不使用一次性无头执行。第一个支持的 CLI 是 OpenCode。 |
| D10 | Queen 定期 Dream：压缩上下文、整理记忆、以 PR 形式沉淀文档。 |
| D11 | 只支持 GitHub。GitLab 和旧的任务页、Issue/MR 工作台删除。 |
| D12 | 服务器上的模型凭证与本地 Agent 的凭证分开，由部署者配置；本地 Agent 可以用管理员作用域的 Token 通过 MCP 配置它们。 |
| D13 | Skill、预设、项目说明复用 Claude Code 的仓库约定，本地和云端共用一套。权限、凭证、配额存数据库，Agent 不能通过改文件提权。 |
| D14 | 网页控制台是辅助入口：Hive 页默认打开与 Queen 的对话；网页上可以不经 Queen 直接派 Forager；第一版只做桌面端。视觉风格为"蜂蜜花园"（第 18 节）。 |

## 4. 术语

| 术语 | 中文 | 定义 |
|---|---|---|
| **Hive** | 蜂巢 | 一个项目：一个仓库（第一版）、说明、Skill、权限、配置，以及其中的 Queen 和 Swarm |
| **Queen** | 蜂王 | 每个 Hive 唯一的常驻协调者，可选开启。负责判断、派活、汇总、处理无归属事件、Dream |
| **Forager** | 采集蜂 | 一个云端 Agent 实例，领一件活：拥有一个分支，产出一个 PR，可持续对话直到完成（原名 Drone，因 drone 是雄蜂而改名） |
| **Cell** | 蜂房 | Forager（或 Queen）的隔离运行环境：Docker 容器加持久卷 |
| **Swarm** | 蜂群 | 一个 Hive 中所有的 Forager |
| **Guard** | 守卫蜂 | 平台强制执行的底线规则，任何指挥者都绕不过 |
| **Dream** | 做梦 | Queen 的定期离线整理 |
| Task | 任务 | 派给 Forager 的那件活本身（派活说明的内容），不再指旧流水线的实体 |
| Inbox | 收件箱 | 每个 Forager、Queen 以及 Hive 本身都有的持久消息队列 |
| Router | 路由器 | 把外部事件确定性地分发到 Inbox 或新建 Forager 的平台组件 |
| Situation | 态势文档 | Queen 维护的、限长的 Hive 当前状态摘要 |
| Node | 节点 | 运行 Cell 的机器 |
| Approval | 审批 | 等待人确认的事项 |
| Connector | 连接器 | 外部系统接入，第一版只有 GitHub |
| Skill / Playbook / Preset | | 仓库中的做事说明、工作流程剧本、Forager 预设 |

宣传和文档中可以用的别名（不进代码）：Queen 与 Forager 之间的消息叫 Waggle；Forager 的交付物叫 Honey；监视外部事件的连接器叫 Scout；整个 SwarmJet 部署可称 Apiary。

"session"一词只用于 CLI 内部的会话（例如 OpenCode 的会话 ID），不再是平台术语。

## 5. 架构总览

```
本地 Agent（Claude Code / Codex / …）
   │  MCP：列出/新建 Hive、对话（默认 Queen）、查看进展、离线摘要
   ▼
SwarmJet 实例
 ├─ API + MCP 服务 + 网页控制台
 ├─ Router ◀── Connector（GitHub webhook / 轮询）
 ├─ Inbox（PostgreSQL）+ River 投递 + LISTEN/NOTIFY 唤醒
 ├─ Guard（合并门闩、凭证隔离、受保护分支）
 └─ Hive × N
      ├─ Queen ── Cell（容器 + 卷）── CLI 长期进程
      └─ Swarm
           ├─ Forager ── Cell ── CLI 长期进程 ── 工作区（自己的分支）
           └─ Forager …
                         ▲
                Node × N（运行 Cell 的机器）
```

## 6. Hive

**配置分两类：**

| 存放位置 | 内容 | 理由 |
|---|---|---|
| 仓库文件 | `AGENTS.md` / `CLAUDE.md`、`.claude/skills/*/SKILL.md`、`.claude/agents/*.md`（预设）、项目文档 | 版本随代码，修改走 PR 审查，本地和云端共用 |
| 数据库 | 模型凭证、Git 凭证、权限、Guard 配置、并发与预算、生命周期参数、触发规则、Queen 开关 | 硬约束，Agent 不能通过改文件提权 |

**触发规则**（Router 使用）：事件类型 + 过滤条件 → 动作。第一版支持：
- Issue 打上指定标签 → 用指定预设新建 Forager；
- Issue 评论以 `/swarmjet run` 开头 → 交给该 Issue 归属的 Forager，没有则新建；
- 其余事件 → 归属 Forager 或 Hive Inbox。

现有接单逻辑（标签触发、`/swarmjet run`、接单权限）按这一模型改写，保留其成员权限校验。

## 7. Queen

**职责**：
- 接收本地 Agent 的指令，拆分成 Task 派给 Forager；
- 处理 Hive Inbox 中无法确定性路由的事件（例如没打标签的新 Issue）；
- 汇总 Forager 的进展，回答本地 Agent 的询问；
- 在 Forager 卡住、超时或失败时决定下一步（重派、换预设、请求人工）；
- 定期 Dream。

**权限上限**：派 Forager、取消 Forager、给 Forager 和本地 Agent 发消息、写 Situation 和私有记忆、开 PR、请求 Approval。不能合并 PR，不能修改 Hive 配置、凭证和权限。

**开关**：默认关闭。用户为 Hive 配置好模型凭证后可以开启。关闭时，Hive Inbox 中的事件保留，等本地 Agent 或人处理。

**上下文策略**（模型支持 1M 上下文）：
- Queen 保持同一个长期会话，依赖 CLI 自身的上下文压缩；
- 用量达到阈值（默认 70%）、Dream 结束或人主动要求时，轮换为新会话，以最新的 Situation 开场；
- **输入只给摘要**：GitHub 事件和 Forager 报告由平台压成固定格式（类型、对象、标题、链接、限长摘录），默认每条不超过 2,000 字符；同一批唤醒默认最多 50 条，超出部分折叠为计数；
- **细节按需拉取**：日志、diff、完整评论只能通过带分页和截断的工具获取；
- **输出只走工具**：Queen 不输出供平台解析的自由文本；派活说明和消息有长度上限，大段内容放 Artifact，消息只带引用；
- 每次唤醒和每天有 token 预算，超出后停止并通知人。

**唤醒**：Queen 的 Inbox 有新消息时被唤醒；短时间内的多条消息合并成一批。空闲时 Cell 可以停机，按第 8 节的生命周期管理。

## 8. Forager

**一个 Forager = 一件活**：创建时分配一个分支（`swarm/<hive>/<forager-id>` 之类），只有它能向该分支推送。交接给另一个 Forager 必须显式转移归属。

**运行方式**：
- Cell = 一个长期存在的 Docker 容器，加两个持久卷：工作区，以及 CLI 的 Home 目录（登录状态和 CLI 会话文件）；
- 容器内运行一个长期的 CLI 进程，平台通过结构化协议驱动：
  - 首选 **ACP（Agent Client Protocol）**：stdio 上的 JSON-RPC，提供新建会话、发送消息、取消、加载会话等方法，有适配器对接多种 CLI；
  - 后备：各 CLI 自带的长连接协议；
  - 不模拟终端（PTY 抓屏），不使用"每步起一次容器、等结果文件"的一次性执行；
- 平台的 MCP 服务挂进 CLI，Forager 通过它读 Inbox、向 Queen 汇报、请求 Approval；
- 第一个支持的 CLI 是 **OpenCode**（开源、多模型提供方、现有镜像和登录流程可复用）。Claude Code 和 Codex 随后接入。
- 协议的可行性需要先做技术验证：多轮对话、容器重启后加载会话、中途取消、工具权限请求的自动处理。

**消息送达**：Forager 空闲时，Inbox 中的新消息作为新一轮对话发给 CLI；正在执行时排队，等本轮结束再送达。只有取消类指令立即打断。

**工具权限**：容器是隔离边界，常规的读写文件和执行命令按策略自动批准；推送受保护分支、合并等操作由 Guard 处理，必要时转为 Approval。

**人工接管**：网页上可以打开 Forager 的实时会话和终端，查看并插话。

**状态机**（平台提供机制，数值由用户在 Hive 配置，括号内为默认值）：

```
creating → active ⇄ parked → hibernated
              │        │          │
              └────────┴──────────┴──→ completed / failed / cancelled
```

| 状态 | 含义 | 进入条件 |
|---|---|---|
| active | CLI 正在执行一轮 | 收到消息或刚创建 |
| parked | 本轮结束，容器运行或已停，卷保留 | 本轮结束；空闲超过阈值（30 分钟）则停容器 |
| hibernated | 容器已删，只保留分支、卷和交接摘要 | parked 超过阈值（24 小时） |
| completed | 活已完成 | PR 合并，或 Queen/人关闭 |
| failed | 无法继续 | 超过重试上限或单轮超时（2 小时） |
| cancelled | 被取消 | Queen、本地 Agent 或人取消 |

- Forager 最长存活时间（7 天）到期后进入 failed 并通知；
- 每轮结束时 Forager 更新**交接摘要**（固定字段：目标、已完成、未完成、风险、Artifact 引用、建议的下一步）。它既是给 Queen 的报告来源，也是 CLI 会话丢失后的恢复依据；
- 从 hibernated 唤醒时可以调度到另一个 Node：工作区以分支为准，未提交的改动在休眠前以 WIP 提交推送。

**调度**：每个 Hive 有并发 Forager 上限（4），每个 Node 有容量上限。本地 Agent 交互发起的 Forager 优先于事件触发的 Forager。

## 9. Inbox、Router 与消息

**消息模型**（`messages` 表）：

| 字段 | 说明 |
|---|---|
| id | 幂等 ID，重复投递不会重复处理 |
| hive_id | 所属 Hive |
| from / to | 发送方与接收方：`queen`、`forager:<id>`、`hive`（Hive Inbox）、`local:<token>`、`system` |
| kind | `event`、`task`、`report`、`question`、`reply`、`control`（取消等） |
| chain_id | 因果链 ID：同一源头引发的所有消息和 Forager 共享 |
| hop | 跳数，每转发一次加一 |
| body | 限长文本（默认 8,000 字符）；大段内容放 Artifact |
| artifacts | Artifact 引用列表（分支、提交、PR、报告、交接摘要） |
| state | `pending`、`delivered`、`handled`，以及认领租约 |

**投递语义**：写消息与插入 River Job 在同一事务中；至少投递一次，按 id 幂等；同一 Inbox 内按写入顺序送达；`LISTEN/NOTIFY` 负责即时唤醒，River 负责重试和兜底。

**通信规则**：
- Queen ⇄ Forager：允许；
- Forager ⇄ Forager：**不允许**（初定），以后可作为 Hive 级开关放开；
- 本地 Agent → Queen、Forager、Hive Inbox：允许；
- 人（网页、GitHub）→ 任意 Inbox：允许。

**Router 规则**（确定性，不经过 LLM）：

| 事件 | 去向 |
|---|---|
| 某 Forager 的 PR 上 CI 结果、评审意见、评论 | 该 Forager 的 Inbox |
| 匹配触发规则 | 新建 Forager，Task 中带上事件摘要 |
| `/swarmjet run` 评论 | 该 Issue 归属的 Forager，没有则新建 |
| 其他 | Hive Inbox → Queen（若开启），否则等待 |

Hive Inbox 中的事件有认领租约，Queen 和本地 Agent 同时在线时不会重复处理。

**防循环**：
- 消息跳数超过上限（10）即停止转发，并通知人；
- 同一 chain_id 下派生的 Forager 数量有上限（10）；
- 每个 Hive 有并发和每日预算上限。

**GitHub 是镜像，不是消息总线**：关键节点（Forager 开始、PR 就绪、需要人工）同步到 Issue 和 PR 上给人看；GitHub 上人的回复经 Connector 进入 Router。消息的权威来源始终是 Inbox。

## 10. Guard

内置以下规则。Hive 可以调整参数，但不能全部关闭：

| Guard | 规则 |
|---|---|
| 独立审查 | PR 合并前，必须有一个**不同于作者 Forager** 的审查 Forager 给出批准，且批准针对当前提交 |
| CI | 目标分支要求的检查全部通过 |
| 合并凭证隔离 | 只有平台的合并步骤持有合并凭证；Forager 和 Queen 都拿不到 |
| 分支归属 | Forager 只能推送自己的分支；禁止推送受保护分支 |
| 合并证据 | 合并前核对：审查过的提交、CI 结果和目标分支状态一致（沿用现有合并证据逻辑） |

合并本身由平台执行：满足 Guard 后，按 Hive 配置自动合并或等待人工合并。

## 11. Dream

**触发**（用户可配置）：定时（默认每天 03:00，按 Hive 时区）；自上次以来完成的 Forager 达到一定数量（20）；Queen 上下文接近阈值；手动触发。只在 Queen 空闲时开始，不打断正在处理的事。

**输入**：Queen 的近期对话和决策；Forager 的交接摘要、PR、失败记录；人对 Agent 的纠正（评审意见、被拒绝的 Approval、人工接管）；现有记忆和文档。

**产出**：

| 产出 | 去向 | 需要人审 |
|---|---|---|
| 新的 Situation | 数据库，供下一个会话开场 | 否 |
| Queen 私有记忆（经验、注意事项） | 数据库，带来源和时间，网页可查看和删除 | 否 |
| 项目记忆与文档（事实、约定、运维手册、ADR） | 向仓库开 PR（`AGENTS.md`、`docs/`） | 是 |
| Skill 与 Playbook 改进建议 | 向仓库开 PR（`.claude/skills/`） | 是 |
| 过时或矛盾记忆的清理建议 | 私有记忆直接标记；仓库内容开 PR | 仓库内容需要 |

**约束**：Dream 期间不对外行动（不派活、不发消息、不合并）；有独立预算；每次生成一份 Dream 报告，记录整理了什么、删了什么、开了哪些 PR。

凡是会影响其他 Agent 行为的记忆，都必须经人合并 PR 才生效。记忆即使被污染，也只会停在一个未合并的 PR 里。

## 12. MCP 接口

本地 Agent 和 Queen 使用同一个 MCP 服务，作用域不同。

| 工具 | 说明 | 默认 Token | 管理员 Token | Queen |
|---|---|---|---|---|
| `list_hives` / `get_hive` | 列出和查看 Hive | ✓ | ✓ | 仅本 Hive |
| `create_hive` / `update_hive` | 新建和配置 Hive（含凭证、Guard、上限） | | ✓ | |
| `send` | 发消息，默认发给 Queen；可指定 Forager 或 Hive Inbox | ✓ | ✓ | ✓ |
| `wait` | 长轮询等待新消息或状态变化，最多约 60 秒 | ✓ | ✓ | |
| `spawn_forager` / `cancel_forager` | 新建和取消 Forager | ✓ | ✓ | ✓ |
| `get_forager` | 状态、交接摘要、PR、产出 | ✓ | ✓ | ✓ |
| `read_forager_detail` | 日志、diff、对话，分页和截断 | ✓ | ✓ | ✓ |
| `inbox` / `claim` | 查看和认领 Hive Inbox 事件 | ✓ | ✓ | ✓ |
| `offline_digest` | 自上次连接以来的摘要：完成了什么、Queen 决定了什么、待办和待审批 | ✓ | ✓ | |
| `approvals` / `decide_approval` | 查看和处理审批 | 查看 | ✓ | |

- 第一版通过 SSH 隧道访问，不直接暴露公网；
- 所有返回内容都限长，避免撑爆调用方的上下文。

## 13. 安全

- **消息一律是数据**：Router 只看结构化字段（事件类型、仓库、标签、作者权限），不解析正文中的"指令"；Inbox 中的内容对 Agent 而言是待处理的信息，不是系统指令。
- **Token 作用域**：默认 Token 只能派活和查看；管理员 Token 才能改配置和凭证。
- **凭证**：Git 推送经平台代理，Cell 内只有短期的 Forager 级凭证，且只能推送自己的分支；模型凭证按 Hive 配置。
- **Guard** 是最后一道防线：无论谁在指挥，合并规则都由平台强制。

## 14. 可观测性

- 每个 Hive 有一条时间线，按 chain_id 展示因果树：哪个事件触发了哪些消息、哪些 Forager，最终产出了哪些 PR；
- Forager 详情页展示状态变化、每轮对话摘要、交接摘要、PR 与 CI 状态；
- 保留现有的 Prometheus 指标和结构化日志，新增 Inbox 积压、Forager 状态分布、Queen 和 Dream 的 token 用量。

## 15. 删除与复用

**删除**：
- Temporal（`go.temporal.io/*`、Temporal Server 与其数据库、compose 中的相关服务）；
- 旧流水线：`internal/pipeline` 的 Workflow 与阶段推进、`internal/intake` 中的 Task 建单；
- 一次性 Runner 执行模型（`internal/agent` 中"起容器、等 result.json"的部分）；
- GitLab：`internal/gitlab`、`internal/managedmeta`、`internal/initflow`、`internal/repochange` 中仅服务 GitLab 或 Meta-Repo 的部分；
- 网页：任务页、任务详情、Issue 工作台、MR 工作台及其样式与测试。

**复用**（改造后保留）：

| 现有代码 | 新用途 |
|---|---|
| `internal/workspace` | Forager 的工作区与分支管理 |
| `internal/agent` 的配置快照、凭证白名单、资源限制 | Cell 的创建参数 |
| `internal/agent/fakeagent` | 扩展为 Forager/Queen 的假 CLI，用于确定性测试 |
| `internal/runtimeadmin` | CLI 登录终端、Forager 人工接管终端 |
| `internal/github`、`internal/githubapp` | Connector 与合并操作 |
| `internal/pipeline` 中的合并证据、复核指引 | Guard 与审查流程 |
| `internal/jobs`（River） | Inbox 投递、生命周期步骤、定时任务（Dream、超时检查） |
| `internal/execution`、`nodeenvironment` | Node 协议与 Node 上的 Cell 管理 |
| `internal/secretbox`、`credentials`、`networkconfig`、`mihomo` | 凭证加密、网络出口 |
| `internal/store`、`internal/api`、`internal/service` | 改为 Hive/Queen/Forager/Inbox 的数据与接口 |

## 16. 数据模型草图

| 表 | 主要字段 |
|---|---|
| `hives` | id、name、repository、config（JSON，含生命周期参数、上限、触发规则）、queen_enabled、版本 |
| `hive_credentials` | hive_id、kind（model / git）、加密密文 |
| `agents` | id、hive_id、role（queen / forager）、state、preset、cli、branch、node_id、chain_id、cli_session_ref、handoff（JSON）、created_at、updated_at |
| `messages` | 见第 9 节 |
| `artifacts` | id、hive_id、agent_id、kind（branch / commit / pr / report / handoff）、ref、metadata |
| `approvals` | id、hive_id、agent_id、kind、payload、state、decided_by |
| `memories` | id、hive_id、owner（queen）、content、source、created_at、superseded_by |
| `situations` | hive_id、version、content、created_at |
| `dream_runs` | id、hive_id、started_at、finished_at、report、prs |
| `timeline` | id、hive_id、chain_id、kind、subject、message、created_at |

Queen 和 Forager 共用 `agents` 表，以 role 区分；每个 Hive 恰好有一条 role=queen 的记录（关闭时 state 为 disabled）。

## 17. 迁移

不做新旧并存，一次性切换：

1. 升级前跑完或取消所有进行中的旧任务；
2. 新版本启动时删除 Temporal 相关服务；旧表保留为历史，不再写入；
3. 现有 GitHub Project 迁移为 Hive：仓库、Agent 选择、接单策略、自动合并开关映射为 Hive 配置与触发规则；
4. 不迁移旧任务的执行现场。

## 18. 网页控制台

### 定位

本地 Agent 是主入口，网页控制台是辅助入口，承担四件事：

1. **看**：Hive 里在发生什么，Queen 做了哪些决定，每只 Forager 进展到哪一步；
2. **插手**：直接和 Queen 或某只 Forager 对话，处理审批，必要时接管终端；
3. **配置**：Hive 设置、凭证、Guard、触发规则、上限，以及全局的 Node 和 Token；
4. **上手**：首次部署后引导用户建 Hive、配凭证、连接本地 Agent。

第一版只做桌面端，不针对手机适配。

### 页面结构

```
顶层
 ├─ Hive 列表（首页）  每个 Hive 一张卡：仓库、Queen 状态、活跃 Forager 数、待审批、Inbox 积压
 ├─ 审批（全局）       所有 Hive 的待审批汇总，导航栏显示计数
 └─ 系统              Node、MCP Token、网络与代理、成员、CLI 登录配置

Hive 页
 ├─ 对话（默认）  左侧与 Queen 对话，右侧 Swarm 实时面板
 ├─ Swarm        所有 Forager 的列表和筛选
 ├─ 时间线        按 chain_id 展开的因果树
 ├─ Inbox        无法路由的事件：认领、指派给 Forager、忽略
 ├─ 记忆          Situation、Queen 私有记忆、Dream 报告、待合并的记忆 PR
 └─ 设置          仓库、CLI 与模型凭证、Queen 开关、触发规则、Guard、上限

Forager 详情页
 └─ 顶部：状态、分支、PR、CI、Guard 检查
    分区：对话 / 交接摘要 / Diff / 时间线 / 终端（接管）
```

**对话页的行为**：
- Queen 的话用正文展示；Queen 的动作（派活、取消、请求审批）用结构化卡片展示，可点开查看细节；
- GitHub 等外部事件以带来源标识的卡片插入对话流；
- Queen 关闭时，对话区改为"直接派 Forager"：填写派活说明、选择预设，直接创建 Forager；
- 无论 Queen 是否开启，Swarm 面板上都可以直接派 Forager，或进入某只 Forager 和它对话。

**实时**：状态变化由服务端推送（基于 `LISTEN/NOTIFY`），不靠前端轮询。

### 视觉风格："蜂蜜花园"

清新、通透、偏暖。蜜蜂采蜜要去花园，所以用蜂蜜色加叶绿色作为一组完整的意象。

| 角色 | 颜色 | 用途 |
|---|---|---|
| 底色 | 奶白 `#FBF8F1` | 页面背景 |
| 面板 | 浅叶 `#E9EFE6` / 白 `#FFFFFF` | 分区与卡片 |
| 品牌强调 | 蜂蜜 `#F2B53A` | Queen、主要按钮、品牌元素（按钮上的文字用深棕 `#3A2A05`） |
| 正常状态 | 薄荷 `#7CC4A4` | Forager 运行中、完成、检查通过 |
| 注意 | 深琥珀 | 待审批、需要注意 |
| 错误 | 柔和珊瑚红 | 失败、被 Guard 拦下 |
| 文字 | 墨色 `#3A3A33` | 正文，不用纯黑 |

具体的深琥珀和珊瑚红色值在实现时补全，要求对比度满足 WCAG AA。

**蜜蜂元素**（有，但不喧宾夺主）：

| 位置 | 做法 |
|---|---|
| Logo 与 Hive 图标 | 六边形蜂房；Hive 卡片用六边形色块标识 |
| Queen 与 Forager 头像 | Queen：实心蜂蜜色六边形加小皇冠；Forager：描边六边形，颜色表示状态 |
| 状态指示 | 小六边形代替圆点 |
| 空状态与登录页 | 淡蜂巢底纹，线条风格的小蜜蜂插画 |
| 加载动画 | 小蜜蜂沿六边形路径飞行 |
| 文案点缀 | 例如"这个 Hive 还没有 Forager，派一只去干活吧"、"Queen 昨晚整理了 3 条记忆" |

不使用：满屏卡通蜜蜂、黄黑条纹（容易联想到警告）、渐变和重阴影。

**其他规范**：
- 圆角：卡片 12px，按钮和输入框 8px；
- 字体：中文思源黑体，英文 Inter，代码 JetBrains Mono；Queen 的话用衬线体，和界面文字区分；
- 层级：细边框加浅色块，几乎不用阴影；
- 术语直接上界面，中文界面首次出现时写作"Queen（蜂王）"等形式并给出说明；
- 深色模式第一版不做，所有颜色通过 CSS 变量定义。

### 像素点缀

像素风只作点缀：正文、代码、diff、日志和表格保持原有字体与排版，只有图标、logo、头像、空状态和加载动画用像素画，不做复古游戏风。

- 所有像素图都是内联 SVG，每个像素一个 `<rect>`（相邻同色像素合并成一个 path），根节点带 `shape-rendering="crispEdges"`，viewBox 用整数网格；
- 只在整数倍尺寸下显示：16×16 网格的图用于 16/32/48px，32×32 网格用于 32/64/96px；传别的尺寸在开发模式下告警，不静默变糊；
- 调色板固定 6～8 色，全部取自 `hive-console.css` 的 token：蜂蜜黄、深黄（阴影）、深棕近黑（描边、条纹、眼睛）、奶白、浅蓝白（翅膀）、粉（腮红）、金（皇冠）。深色底只换 token：描边转奶白，眼睛高光转深色；
- 图标集是 16×16 的单色像素图，用 `currentColor`；`swarm` 画成几只极简小蜂，`hive` 画成像素六边形；
- Forager 头像由 id 的 FNV-1a 哈希决定：身体主色（6 种暖色）、条纹 2～3 条、配饰（无、眼镜、帽子、领结、耳机，每种只占 2～6 个像素）、朝向左右；状态继续由状态徽标表达，不用头像颜色；
- Queen 用像素皇冠表示，不再画戴皇冠的蜜蜂；
- 空状态是 32×32 的一格蜂巢加一只蜂，加载动画是 bee-32 的两帧扇翅加 1～2px 浮动，`prefers-reduced-motion` 时停在第一帧；
- 不用像素字体排正文；像素字体最多用在 logo 字样上，且要先评估中文显示效果，拿不准就不用；
- 圆角统一收到 2～4px，卡片和按钮用 2px 实线边框；主要按钮和选中的卡片用错位硬阴影 `box-shadow: 2px 2px 0 <深棕 token>`，按下时位移 1px、阴影减为 1px；不用渐变、不用模糊阴影；
- 配色沿用"蜂蜜花园"，不新增颜色体系。

### 与现有前端的关系

| 处理 | 内容 |
|---|---|
| 保留 | React + Vite、登录与权限、终端组件、Node 设置、Runner 环境（改为 CLI 登录配置）、网络与代理、成员管理 |
| 删除 | 任务列表、任务详情、Issue 工作台、MR 工作台及其样式与测试 |
| 新增 | Hive 列表、Hive 页六个分区、Forager 详情、全局审批、MCP Token 管理 |

## 19. 待决事项

| 事项 | 当前倾向 |
|---|---|
| Forager 之间直接通信 | 初定不允许；以后作为 Hive 级开关 |
| ACP 与各 CLI 适配器的成熟度 | 需要技术验证；不成熟则为 OpenCode 单独写协议适配 |
| Queen 开启条件 | 默认关闭，配置模型凭证后由用户开启 |
| 一个 Hive 是否支持多个仓库 | 第一版一个仓库 |
| 多 Node 调度细节 | 沿用现有 Node 协议，按容量和优先级分配 Cell |
| 深色模式 | 第一版不做；配色全部用变量管理，以后可加 |

## 20. 后续工作拆分（可并行）

在本 ADR 定稿后，以下任务可以分别交给不同的 worktree 并行开发：

1. **ACP / OpenCode 技术验证**：独立进行，结论决定第 3 项的实现方式；
2. **数据层与 Inbox**：新表、迁移、消息写入与 River 投递、认领租约、防循环；
3. **Cell 与 Forager 运行时**：长期容器、持久卷、生命周期状态机、CLI 驱动（依赖第 1 项结论）；
4. **MCP 服务**：第 12 节的工具，先用假 Forager 实现；
5. **Router 与 GitHub Connector**：把现有 webhook、轮询和接单逻辑改写为 Router；
6. **清理**：删除 Temporal、旧流水线、GitLab、旧网页（应在 2～5 合入后进行，以免冲突）；
7. **Queen 与 Dream**：在 2～5 就绪后进行；
8. **网页控制台**：按第 18 节实现；可以先用假数据和 MCP 接口的契约并行开发。

建议第一轮并行做 1、2、4、5；第 3 项等第 1 项出结论后开始。
