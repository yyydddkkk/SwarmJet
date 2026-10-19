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

> 在你自己的服务器上部署 SwarmJet，为每个仓库建一个 Hive，给它配一套方法论。Hive 里的 Agent 是一个个独立的个体，按方法论协作；你本地的 Claude Code 通过 MCP 和它们对话、派活、调整方法论。平台不替 swarm 决定怎么工作，只保证它跑得稳、看得见、改得动。

- **目标用户**：有自己服务器、本地已经在用 Claude Code 或 Codex、希望后台并行跑任务、又不想把代码和凭证交给 SaaS 的个人开发者和小团队。
- **性质**：开源、自托管。不是 SaaS，也不是云厂商。部署、凭证、成本和风险都由部署者自己承担。
- **差异化**：自托管；任何本地 Agent 都能通过 MCP 指挥；swarm 的工作方式由方法论定义，可以直接用现成的（例如 pstack）；本地离线时 swarm 照常响应。
- **演示场景**：晚上在终端里说"把这 5 个 Issue 并行修了"，然后合上电脑。第二天早上看到 5 个 PR，每个都按方法论经过了独立验证、CI 全绿；中途 GitHub 上来的新 Issue，也已经有 Agent 接手。

### 平台边界（2026-10-19 修订，与下文冲突时以本节为准）

判断一条规则放在哪：**如果它是"swarm 应该怎么工作"，它属于方法论；如果它是"swarm 能不能稳定、安全、可见地工作"，它属于平台。**

**平台负责**：

| 保障 | 含义 |
|---|---|
| 可用性 | Agent 不因重启、断网、容器崩溃而丢失；会话能恢复，消息不丢不重，失败能重试或交接 |
| 易用性 | 一键部署、一键接入 GitHub、一键连接本地 Agent；默认方法论开箱可用 |
| 可观测 | 每个 Agent 做了什么、说了什么、花了多少、卡在哪，都能查到；记录只追加 |
| 可定制 | 方法论的每一项都能改，谁来改也由方法论决定 |
| 忠实执行 | 方法论里声明的权限和规则（例如"合并前需要非作者的验证结论"），平台照着强制执行 |

**平台绝不决定**：
- 有哪些角色，每个角色用什么提示词、模型和插件；
- 谁派谁、谁验证谁、什么时候合并、要不要 CI；
- 谁能改方法论；
- Agent 之间能否通信、能否派生同伴（默认放开，由角色权限收紧）。

**平台底线**（只有三条，都是"怎么做"而不是"做不做"）：
1. **凭证不进 Cell**：Git 凭证和模型凭证都由平台保管和刷新。Agent 通过平台的工具推送、开 PR、合并，通过平台的模型代理调用模型。允许谁合并仍由方法论决定。
2. **Hive 隔离**：一个 Hive 的 Agent 看不到、也改不了另一个 Hive 的东西。
3. **防失控**：并发、跳数、生命周期、花费都必须有上限；值由用户定，但不能没有。

**方法论**（每个 Hive 一套，是 swarm 的共享资产）：
- **内容**：
  - 角色：提示词、模型、插件与 skill、权限，以及生命周期（短期还是常驻）；
  - 常驻规则；
  - playbook；
  - 事件规则（什么事件交给哪个角色）；
  - 合并条件。
- **来源**：直接用现成的方法论，**pstack 是默认方法论和切入点**。本地 Agent 订阅上游（git 地址 + 版本），把它处理成 Cell 里 CLI 的原生格式，作为 swarm 的共同资产存进平台，而不是让每个 Agent 各自适配。上游更新时重新处理，生成新版本。
- **保障**：
  - 每次修改都有版本，能回滚，并记下谁、什么时候、改了什么；
  - 消息带来源可信度（来自用户、来自 Agent、来自外部 GitHub 内容），防止外部内容注入后借 Agent 之手改方法论。
- **记忆**也属于共享资产，但和方法论分开存放、分开生效：
  - 方法论是规定性的，体量小，每轮都注入；
  - 记忆是描述性的，越积越多，按需检索。

**CLI**：Cell 内统一使用 **oh-my-pi（omp）**。
- 保留 `cell.Session` 接口，以后换 CLI 只换这一层。
- 平台 API 只有 MCP 一套，omp 里用一个很薄的扩展接入。
- 模型凭证交给 omp 的 auth broker 和 auth gateway 保管。平台 api 在 gateway 前面再加一层代理，按 Agent 统计用量、执行预算；Cell 只拿访问这层代理的 Token。
- 仓库里自带的 omp 扩展、hook、工具、MCP 配置默认不加载（只加载 rules 和 skill），方法论可以放开。
- 不再提供免费的默认模型，用户自己配置模型凭证，例如 DeepSeek API key。

**接入**：平台抽象出接入层（事件进来、成果出去），现在只实现 GitHub。

## 3. 决策摘要

| # | 决策 |
|---|---|
| D1 | 放弃总管 Bot 和固定 Bot 角色。Agent 是通用的，专业化来自 Hive 的说明、Skill、预设和权限。 |
| D2 | 平台上有多个 Hive，彼此隔离。每个 Hive 有且只有一个 Queen，以及任意数量的 Forager。 |
| D3 | 本地 Agent 是客户端，不是 Hive 的成员。它通过 MCP 选择 Hive，默认和 Queen 对话，也可以直接和某个 Forager 对话。本地会话关闭不影响 Hive 运行。 |
| D4 | Queen 默认关闭，由用户为 Hive 开启。~~Queen 只能派 Forager、发消息、写自己的记忆、开 PR；不能合并 PR，不能改配置。~~ 2026-10-19 修订：Queen 只是方法论里的一个角色，能做什么由方法论决定。 |
| D5 | ~~Forager 之间不能直接发消息（初定），协作只经由 Queen 和 Git。~~ 2026-10-19 修订：Agent 之间默认可以通信、可以按角色权限派生同伴，受跳数和并发上限约束。 |
| D6 | 事件先经确定性的 Router：有归属的交给归属 Forager，匹配触发规则的新建 Forager，其余进 Hive Inbox，由 Queen（若开启）或人处理。 |
| D7 | 消息层使用 PostgreSQL（Inbox 表）+ River（投递）+ `LISTEN/NOTIFY`（唤醒），不引入新的中间件。 |
| D8 | 去掉 Temporal 和旧流水线。工作顺序由 Skill/Playbook 描述，底线由独立验证和平台合并入口强制（第 10 节）。 |
| D9 | Forager 和 Queen 都是长期运行的 CLI 进程，跑在 Docker 容器（Cell）里，通过结构化协议驱动，不模拟终端，不使用一次性无头执行。~~第一个支持的 CLI 是 OpenCode。~~ 2026-10-19 起统一使用 oh-my-pi（omp）。 |
| D10 | Queen 定期 Dream：压缩上下文、整理记忆、以 PR 形式沉淀文档。 |
| D11 | 只支持 GitHub。GitLab 和旧的任务页、Issue/MR 工作台删除。 |
| D12 | 服务器上的模型凭证与本地 Agent 的凭证分开，由部署者配置；本地 Agent 可以用管理员作用域的 Token 通过 MCP 配置它们。 |
| D13 | Skill、预设、项目说明复用 Claude Code 的仓库约定，本地和云端共用一套。权限、凭证、配额存数据库，Agent 不能通过改文件提权。2026-10-19 补充：工作方式由 Hive 的方法论定义（第 2 节「平台边界」）。 |
| D14 | 网页控制台是辅助入口：Hive 页默认打开与 Queen 的对话；网页上可以不经 Queen 直接派 Forager；第一版只做桌面端。视觉风格为"蜂蜜花园"（第 18 节）。 |

## 4. 术语

| 术语 | 中文 | 定义 |
|---|---|---|
| **Hive** | 蜂巢 | 一个项目：一个仓库（第一版）、说明、Skill、权限、配置，以及其中的 Queen 和 Swarm |
| **Queen** | 蜂王 | 每个 Hive 唯一的常驻协调者，可选开启。负责判断、派活、汇总、处理无归属事件、Dream |
| **Forager** | 采集蜂 | 一个云端 Agent 实例，领一件活：拥有一个分支，产出一个 PR，可持续对话直到完成（原名 Drone，因 drone 是雄蜂而改名） |
| **Cell** | 蜂房 | Forager（或 Queen）的隔离运行环境：Docker 容器加持久卷 |
| **Swarm** | 蜂群 | 一个 Hive 中所有的 Forager |
| **验证 Forager** | 验证蜂 | 只读的 Forager，独立验证别人的 PR 并给出结论（原 Guard，第 10 节） |
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
 ├─ 验证与合并（验证 Forager、结论记录、合并入口）
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
| 数据库 | 模型凭证、Git 凭证、权限、验证与合并配置、并发与预算、生命周期参数、触发规则、Queen 开关 | 硬约束，Agent 不能通过改文件提权 |

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
- ~~第一个支持的 CLI 是 **OpenCode**。~~ 2026-10-19 起统一使用 **oh-my-pi（omp）**，通过 `omp acp` 驱动（见第 2 节「平台边界」）。第二、三轮的 OpenCode 实现在第四轮替换。
- 协议的可行性需要先做技术验证：多轮对话、容器重启后加载会话、中途取消、工具权限请求的自动处理。

**消息送达**：Forager 空闲时，Inbox 中的新消息作为新一轮对话发给 CLI；正在执行时排队，等本轮结束再送达。只有取消类指令立即打断。

**工具权限**：容器是隔离边界，常规的读写文件和执行命令按策略自动批准；推送受保护分支、合并等操作只能经平台的合并入口完成（第 10 节）。

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

**第二轮实现说明**：本轮的主协议是 **ACP**（stdio 上的 JSON-RPC）：Cell 内运行长期的 `opencode acp` 进程，平台通过 `session/new`、`session/prompt`、`session/cancel` 和 `session/load` 驱动多轮对话；会话 ID 持久化在 `agents.cli_session_ref`，容器重启后按同一 cwd 和数据卷加载。Cell **不持有 Git 凭证**：工作区由平台在宿主机侧首次克隆；之后暂存、WIP 提交和 `git bundle create` 都在 Cell 内执行，但推送由平台侧的裸镜像用短期安装 Token 完成。发布走 **bundle 加平台镜像**：宿主只接受 Cell 产出的、恰好指向该 Forager 分支的 bundle，校验基线祖先关系后再推送一个 refspec，避免 Cell 可写的 `.git/config` 影响宿主。监管者与 River 消息投递 Job 同在 serve 进程，发布器使用 GitHub App 按仓库名解析安装并申请只覆盖该仓库的 Token。**本轮只支持单机**：Cell 跑在平台宿主机的 Docker 上，Cell 加入配置的 Docker 网络以访问 serve 的 `/mcp`；这一轮还没有多 Node 调度，也没有 Queen、Guard 和 Dream。

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

## 10. 验证与合并

> 2026-10-19 二次修订：
> - 原计划的 Guard 模块取消；
> - "新 head 就派验证者、FAIL 打回、轮次上限"这些流程也不由平台写死，改由方法论定义（第 2 节「平台边界」）。
>
> 参考了 pstack 的 Shipping、Babysit、Autopilot-full。

平台只提供机制：

| 机制 | 做法 |
|---|---|
| 结论（attest） | 任何 Agent 都可以对某个修订（PR + head SHA）给出结论。平台记下结论内容、给结论的是谁、他是不是该修订的作者；有新的推送时，旧结论作废。结论以 App 身份同步为 PR 评论，不使用 GitHub 的 Approve |
| 合并入口 | 合并只能经平台执行，执行时锁定 SHA。是否允许合并，平台按**方法论声明的合并条件**判断，例如"当前 head 有非作者给出的 PASS，且必需检查全部通过" |
| CI | 由 GitHub 运行。平台只读取结果，再按方法论把失败交给相应的 Agent |
| 分支归属 | Agent 只能经平台推送，且只能推送自己的分支 |

默认方法论（pstack）的写法是：
- worker 开 PR 后，派一个只读的 verifier 同伴验证当前 head；
- FAIL 交回 worker 修复；
- 合并前要求非作者的 PASS 和 CI 全绿；
- 默认只通知"可以合并"，不自动合并。

这些都可以由用户改掉。

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
| `create_hive` / `update_hive` | 新建和配置 Hive（含凭证、验证与合并、上限） | | ✓ | |
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
- **合并入口**是最后一道防线：无论谁在指挥，合并条件都由平台强制（第 10 节）。

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
| `internal/pipeline` 中的合并证据、复核指引 | 合并入口与验证流程 |
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
3. **配置**：Hive 设置、凭证、验证与合并、触发规则、上限，以及全局的 Node 和 Token；
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
 └─ 设置          仓库、CLI 与模型凭证、Queen 开关、触发规则、验证与合并、上限

Forager 详情页
 └─ 顶部：状态、分支、PR、CI、验证结论
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
| 错误 | 柔和珊瑚红 | 失败、验证 FAIL、无法合并 |
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

- 图案的唯一来源是 `docs/claude/design/pixel-icons.json`（网格、图例、调色板、覆盖色都在里面），`web/src/hive/pixel-icons.ts` 由它生成，改图案只改规范文件再重新生成，不在组件里手画；
- 所有图案都是 16×16 字符网格，一个字符一个 1×1 `<rect>`，SVG 用 `viewBox="0 0 16 16"` 和 `shape-rendering="crispEdges"`；`"."` 透明，`"#"` 用 `currentColor`，其余字符按图例取色；
- 只在 16/32/48/64px 下显示；传别的尺寸在开发模式下告警，不静默变糊；
- 图例共 12 个字符、10 种颜色，全部落到 `hive-console.css` 的 `--hg-px-*` token：`y` 身体、`d` 条纹与眼睛、`a` 触角与尾刺、`w` 翅膀、`z` 睡眠、`r` 待你处理、`g/o/h/m` 皇冠。深色底只换 token（`a` 转奶白，`w/z/r` 提亮），不另存一套图；
- Forager 有 8 张图：`forager.<状态>` 对应 creating、active、parked、hibernated、completed、failed、cancelled，`needs_you` 用在有待审批或待回答消息的 Forager 上；`stateOverrides` 覆盖对应状态的颜色（creating 偏淡、cancelled 转灰）；
- 头像由 id 的 FNV-1a 哈希从 6 种身体色里选一种，只替换 `y`，表情由状态决定；不再有配饰、条纹数、朝向的变体，状态仍由状态徽标表达；
- Queen 只用 `queen` 皇冠网格，未开启时套用 `queenOff` 配色；
- 通用图标是 `icons` 里的 29 个 16×16 单色像素图，沿用原来的名字和调用方式，`bee` 用作 logo 与导航里的蜂标志；
- 加载动画保留两帧扇翅：基于 `forager.active`，第二帧把第 3～6 行的翅膀像素上移一行，再加 1～2px 浮动，`prefers-reduced-motion` 时停在第一帧；
- 空状态由 `icons.hive` 加 `forager.parked` 组合，不再画单独的蜂巢插画；
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
| Forager 之间直接通信 | 2026-10-19 定：默认允许，由方法论的角色权限收紧 |
| ACP 与各 CLI 适配器的成熟度 | 2026-10-19 定：统一 omp，用其原生 ACP；不成熟时改用 omp 的 RPC 模式 |
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
