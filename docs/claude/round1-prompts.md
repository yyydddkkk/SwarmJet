# 第一轮并行任务提示词

对应 ADR（`docs/claude/adr-hive-queen-drone.md`）第 20 节。开工前请先把 PR #9 合并进 dev，这样每个 worktree 都能读到 ADR。

| 任务 | 内容 | 主要改动区域 | 依赖 |
|---|---|---|---|
| A | OpenCode 驱动方式技术验证（ACP 或 OpenCode 自带服务） | `docs/claude/spikes/`、`internal/cell/acp`（实验性） | 无 |
| B | 数据层与 Inbox：新表、迁移、存储方法、消息投递、Token | `internal/domain/hive.go`、`internal/store`、`internal/jobs`、`cmd/swarmjet/main.go` | 实现公共契约 |
| C | MCP 服务（先接内存假后端） | 新包 `internal/mcp` | 消费公共契约 |
| D | Router 与 GitHub 事件归一化（先接内存假后端） | 新包 `internal/router` | 消费公共契约 |
| E（可选） | 网页控制台的视觉体系与静态页面（假数据） | `web/src` 新文件 | 无 |

**合并顺序**：B → D → C → A → E。B、C、D 都会新增同一个文件 `internal/domain/hive.go`，内容必须与契约逐字一致，合并时就不会冲突。

**本轮不做**：接线（把 C、D 接到 B 的真实存储、挂进 API 服务）、Cell 运行时、Queen、Guard、Dream、删除旧代码。这些放到第二轮。

每个任务的提示词整段复制即可。B、C、D 会让模型读取本文件的"公共契约"一节，所以本文件需要先随 PR #9 合入 dev。

---

## 公共契约（B、C、D 共用，已嵌入下方各提示词）

B、C、D 三个任务都以本节为准，逐字使用。

```go
// internal/domain/hive.go
package domain

import (
	"errors"
	"time"
)

// Errors returned by the Hive store. Callers match them with errors.Is.
var (
	ErrNotFound        = errors.New("not found")
	ErrConflict        = errors.New("conflict")
	ErrHiveLimit       = errors.New("hive concurrent drone limit reached")
	ErrChainLimit      = errors.New("chain drone limit reached")
	ErrHopLimit        = errors.New("message hop limit reached")
	ErrPeerMessage     = errors.New("drones cannot message other drones")
	ErrMessageTooLarge = errors.New("message body too large")
)

const (
	MaxMessageBody = 8000 // characters
	MaxEventDigest = 2000 // characters
)

// Hive is one project: a GitHub repository with its Queen and Swarm.
type Hive struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Repository   string     `json:"repository"` // owner/name
	QueenEnabled bool       `json:"queenEnabled"`
	Config       HiveConfig `json:"config"`
	Version      int        `json:"version"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// HiveConfig holds per-Hive limits. Zero values mean "use the default".
type HiveConfig struct {
	CLI                   string        `json:"cli"`                   // default "opencode"
	MaxConcurrentDrones   int           `json:"maxConcurrentDrones"`   // default 4
	MaxHops               int           `json:"maxHops"`               // default 10
	MaxDronesPerChain     int           `json:"maxDronesPerChain"`     // default 10
	IdleParkMinutes       int           `json:"idleParkMinutes"`       // default 30
	HibernateHours        int           `json:"hibernateHours"`        // default 24
	TurnTimeoutMinutes    int           `json:"turnTimeoutMinutes"`    // default 120
	DroneMaxLifetimeHours int           `json:"droneMaxLifetimeHours"` // default 168
	Triggers              []TriggerRule `json:"triggers"`
}

// TriggerRule starts a Drone for a matching GitHub event.
type TriggerRule struct {
	Event         string `json:"event"`                   // "issue_labeled" or "issue_comment"
	Label         string `json:"label,omitempty"`         // for issue_labeled
	CommentPrefix string `json:"commentPrefix,omitempty"` // for issue_comment, e.g. "/swarmjet run"
	Preset        string `json:"preset,omitempty"`
}

type AgentRole string

const (
	RoleQueen AgentRole = "queen"
	RoleDrone AgentRole = "drone"
)

type AgentState string

const (
	AgentCreating   AgentState = "creating"
	AgentActive     AgentState = "active"
	AgentParked     AgentState = "parked"
	AgentHibernated AgentState = "hibernated"
	AgentCompleted  AgentState = "completed"
	AgentFailed     AgentState = "failed"
	AgentCancelled  AgentState = "cancelled"
	AgentDisabled   AgentState = "disabled" // Queen only: not enabled
)

// Agent is a Queen or a Drone.
type Agent struct {
	ID          string     `json:"id"`
	HiveID      string     `json:"hiveId"`
	Role        AgentRole  `json:"role"`
	State       AgentState `json:"state"`
	Preset      string     `json:"preset,omitempty"`
	CLI         string     `json:"cli"`
	Task        string     `json:"task,omitempty"` // Drone only
	Branch      string     `json:"branch,omitempty"`
	IssueNumber int64      `json:"issueNumber,omitempty"`
	PRNumber    int64      `json:"prNumber,omitempty"`
	NodeID      string     `json:"nodeId,omitempty"`
	ChainID     string     `json:"chainId,omitempty"`
	Handoff     *Handoff   `json:"handoff,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// Handoff is the fixed-field summary a Drone updates after each turn.
type Handoff struct {
	Goal      string        `json:"goal"`
	Done      string        `json:"done"`
	Remaining string        `json:"remaining"`
	Risks     string        `json:"risks"`
	Next      string        `json:"next"`
	Artifacts []ArtifactRef `json:"artifacts,omitempty"`
}

type ArtifactRef struct {
	Kind string `json:"kind"` // branch, commit, pr, report, handoff
	Ref  string `json:"ref"`
}

type MessageKind string

const (
	MessageEvent    MessageKind = "event"
	MessageTask     MessageKind = "task"
	MessageReport   MessageKind = "report"
	MessageQuestion MessageKind = "question"
	MessageReply    MessageKind = "reply"
	MessageControl  MessageKind = "control"
)

// Address is "queen", "drone:<agentID>", "hive", "local:<tokenID>" or "system".
type Address string

const (
	AddressQueen  Address = "queen"
	AddressHive   Address = "hive"
	AddressSystem Address = "system"
)

func DroneAddress(id string) Address { return Address("drone:" + id) }
func LocalAddress(tokenID string) Address { return Address("local:" + tokenID) }

type MessageState string

const (
	MessagePending   MessageState = "pending"
	MessageDelivered MessageState = "delivered"
	MessageHandled   MessageState = "handled"
)

type Message struct {
	ID        string        `json:"id"` // idempotency key; empty means "generate"
	HiveID    string        `json:"hiveId"`
	From      Address       `json:"from"`
	To        Address       `json:"to"`
	Kind      MessageKind   `json:"kind"`
	ChainID   string        `json:"chainId"`
	Hop       int           `json:"hop"`
	Body      string        `json:"body"`
	Artifacts []ArtifactRef `json:"artifacts,omitempty"`
	State     MessageState  `json:"state"`
	CreatedAt time.Time     `json:"createdAt"`
}

type TokenScope string

const (
	TokenDefault TokenScope = "default" // spawn and view
	TokenAdmin   TokenScope = "admin"   // also configure Hives and credentials
)

type Token struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Scope     TokenScope `json:"scope"`
	CreatedAt time.Time  `json:"createdAt"`
}
```

存储方法（B 在 `*store.Store` 上实现；C、D 在各自包里按需定义接口子集）：

```go
CreateHive(ctx context.Context, h domain.Hive) (domain.Hive, error)        // also creates the Queen record in state disabled
Hive(ctx context.Context, id string) (domain.Hive, error)
HiveByRepository(ctx context.Context, repository string) (domain.Hive, error)
Hives(ctx context.Context) ([]domain.Hive, error)
UpdateHive(ctx context.Context, h domain.Hive) (domain.Hive, error)        // optimistic version check -> ErrConflict

CreateDrone(ctx context.Context, a domain.Agent) (domain.Agent, error)     // enforces ErrHiveLimit and ErrChainLimit
Agent(ctx context.Context, id string) (domain.Agent, error)
Queen(ctx context.Context, hiveID string) (domain.Agent, error)
Drones(ctx context.Context, hiveID string, states ...domain.AgentState) ([]domain.Agent, error)
DroneByIssue(ctx context.Context, hiveID string, issue int64) (domain.Agent, error)   // newest non-terminal Drone
DroneByPR(ctx context.Context, hiveID string, pr int64) (domain.Agent, error)
DroneByBranch(ctx context.Context, hiveID, branch string) (domain.Agent, error)
TransitionAgent(ctx context.Context, id string, from, to domain.AgentState) error  // compare-and-set -> ErrConflict
UpdateHandoff(ctx context.Context, id string, h domain.Handoff) error

SendMessage(ctx context.Context, m domain.Message) (domain.Message, error)  // validates, idempotent by ID, River job in the same transaction
Inbox(ctx context.Context, hiveID string, to domain.Address, afterID string, limit int) ([]domain.Message, error)
ClaimMessage(ctx context.Context, id, owner string, ttl time.Duration) (bool, error)
MarkMessageHandled(ctx context.Context, id string) error
WaitInbox(ctx context.Context, hiveID string, to domain.Address, afterID string) error  // returns when a newer message exists or ctx ends

CreateToken(ctx context.Context, name string, scope domain.TokenScope) (plaintext string, t domain.Token, err error)
TokenByPlaintext(ctx context.Context, plaintext string) (domain.Token, error)
```

**消息校验规则**（B 负责强制执行，C、D 的假后端也要照做）：
- `drone:*` 发给 `drone:*` → `ErrPeerMessage`；
- `Hop` 大于 Hive 的 `MaxHops` → `ErrHopLimit`；
- `Body` 超过 `MaxMessageBody` 个字符 → `ErrMessageTooLarge`；
- `ID` 相同且内容相同 → 返回已有消息，不重复插入；`ID` 相同但内容不同 → `ErrConflict`；
- 并发上限只计算 `creating`、`active`、`parked` 三种状态的 Drone；同一个 `ChainID` 下的 Drone 总数不超过 `MaxDronesPerChain`。

---

## 任务 A：OpenCode 驱动方式技术验证

````text
# 任务：验证如何在 Docker 中长期运行并驱动 OpenCode

## 背景
SwarmJet 是一个自托管的云端 Agent 集群（Go + PostgreSQL + React），正在按 docs/claude/adr-hive-queen-drone.md 重构。新架构里，每个云端 Agent（称为 Drone）是一个在 Docker 容器（称为 Cell）里**长期运行**的 CLI Agent，平台通过**结构化协议**和它进行多轮对话，而不是模拟终端，也不是"每一步起一次容器、等结果文件"。第一个支持的 CLI 是 OpenCode。

ADR 第 8 节倾向使用 ACP（Agent Client Protocol，Zed 发起，stdio 上的 JSON-RPC，有 initialize、session/new、session/prompt、session/cancel、session/load 等方法），后备是 CLI 自带的长连接协议。资料显示 OpenCode 支持 ACP，也有自己的服务模式。本任务就是动手验证，给出选型结论和一个最小原型。

请先读 ADR 第 8 节和第 13 节。现有代码里 OpenCode 的镜像和登录配置在 deploy/Dockerfile.agent、scripts/install-agent-clis.sh、internal/agent（配置快照与凭证白名单），可以参考。

## 要验证的问题
在 Docker 容器里（基于 deploy/Dockerfile.agent，AGENT_CLIS=opencode）分别尝试 ACP 和 OpenCode 自带的服务模式，回答：
1. 能否在同一个进程里进行多轮对话，并拿到结构化的流式输出（文本增量、工具调用、结束原因）？
2. 容器重启后，能否加载原来的会话继续对话？会话文件存在哪里，挂持久卷是否就够？
3. 能否中途取消正在进行的一轮，取消后进程是否保持可用？
4. 工具权限请求（执行命令、写文件）以什么形式出现？平台能否按策略自动批准或拒绝？
5. 能否给 OpenCode 挂一个外部 MCP 服务（平台以后要通过 MCP 给 Drone 提供读 Inbox、汇报等工具）？
6. 模型凭证如何注入（环境变量、配置文件），能否避免写进镜像？
7. 资源占用：空闲时的内存和 CPU。

## 交付物
1. 报告：docs/claude/spikes/opencode-driver.md。逐条回答上面 7 个问题，附上实际命令和关键输出片段；给出推荐方案（ACP 或 OpenCode 自带协议）和理由；列出已知坑和待确认事项。
2. 最小原型：放在 internal/cell/acp（或按结论命名，例如 internal/cell/opencode），包注释标明"实验性"。至少包含：
   - 启动并连接容器内的 OpenCode；
   - 发送一轮消息并流式读取结果；
   - 取消当前一轮；
   - 重启后加载会话。
   需要真实容器和模型凭证的测试，用环境变量开关（例如 SWARMJET_OPENCODE_SPIKE=1）控制，默认跳过；协议编解码部分要有不依赖外部环境的单元测试。
3. 如果需要修改 deploy/Dockerfile.agent，改动保持最小，并在 PR 里说明。

## 约束
- 只修改 docs/claude/spikes/、internal/cell/ 和（必要时）deploy/Dockerfile.agent。
- 不要对整个文件运行 gofmt 或 go fmt，这个仓库本身不是 gofmt 干净的。
- 不要把任何真实凭证写进代码、测试或报告。

## 验收标准
- go build ./... 和 go vet ./... 通过；默认的 go test ./... 通过（真实环境测试跳过）。
- 报告覆盖全部 7 个问题，结论明确。

## 交付
从 dev 拉新分支，提交信息用英文约定式前缀（docs: 或 feat:）。向 dev 开 PR，不要合并。
````

---

## 任务 B：数据层与 Inbox

````text
# 任务：实现 Hive / Agent / Message / Token 的数据层与 Inbox 投递

## 背景
SwarmJet 是一个自托管的云端 Agent 集群（Go 1.26+、PostgreSQL 18、River 任务队列、React），正在按 docs/claude/adr-hive-queen-drone.md 重构。请先读 ADR 第 3、4、6、8、9、12、16 节。

本任务实现新架构的数据层。另有两个任务在并行开发 MCP 服务（internal/mcp）和事件路由（internal/router），它们会按下面的"公共契约"调用你的存储方法，所以**契约里的类型和方法签名必须逐字实现，不得改名或改参数**。如果确实需要扩展，只能新增，不能修改已有定义，并在 PR 里说明。

## 公共契约
1. 新建文件 internal/domain/hive.go，内容与下方代码**逐字一致**（另外两个任务也会新增同一个文件，内容一致才能无冲突合并）：

读取仓库中 docs/claude/round1-prompts.md 的"公共契约"一节，把其中第一段 Go 代码（从 `// internal/domain/hive.go` 到 `Token` 结构体结束）逐字写入该文件。

2. 在 *store.Store 上实现以下方法：

见同一节的第二段代码"存储方法"。

3. 消息校验规则：

见同一节的"消息校验规则"。

## 要做的事
1. 数据库：按现有方式新增一个版本化迁移 internal/store/schema_upgrade_5.sql（当前最新是第 4 版，参考 store.Migrate），新建表 hives、agents、messages、mcp_tokens。字段参考 ADR 第 16 节和契约；建必要的索引（例如 messages 按 hive_id + to + id，agents 按 hive_id + state、hive_id + issue_number、hive_id + pr_number、hive_id + branch）。旧表一律不动。
2. 实现契约中的全部存储方法：
   - CreateHive 同时创建一条 role=queen、state=disabled 的 Agent 记录；HiveConfig 的零值字段在读取时填入默认值（见契约注释）。
   - CreateDrone 在一个事务里校验并发上限和同链上限（需要加锁防止并发超限），生成 ID，未指定分支时生成 swarm/<hive-id>/<drone-id> 形式的分支名。
   - TransitionAgent 是比较并交换：当前状态不是 from 时返回 ErrConflict。
   - SendMessage 在同一个事务里写入 messages 并插入 River Job；按 ID 幂等；执行全部校验规则；未指定 ID 时生成。
   - WaitInbox 基于 PostgreSQL LISTEN/NOTIFY 实现：写入消息时 NOTIFY，等待方收到通知或 ctx 结束时返回；要能正确处理"开始等待前消息已经到了"的竞态。
   - Token 只存哈希，明文只在 CreateToken 时返回一次。
3. 消息投递 Job：在 internal/jobs 里新增一种 Job（沿用现有的封装方式，River 类型不得外泄到业务代码）。本轮它只负责把消息状态从 pending 改为 delivered 并发出通知；真正把消息送进 CLI 是下一轮 Cell 运行时的工作，请在代码里留出清晰的扩展点。
4. 在 cmd/swarmjet/main.go 中注册新的 Job 处理器（只做最小改动）。

## 约束
- 只修改 internal/domain/hive.go（新建）、internal/store、internal/jobs、cmd/swarmjet/main.go。不要修改 internal/pipeline、internal/service、internal/api 等现有业务代码。
- 现有 store 包里已有 ErrNotFound 和 ErrConflict，供旧代码使用，不要改动它们；新方法返回 domain 包里的错误。
- 不要对整个文件运行 gofmt 或 go fmt，这个仓库本身不是 gofmt 干净的。

## 验收标准
- go build ./... 和 go vet ./... 通过。
- 起一个临时 postgres:18.1-alpine3.23 容器，设置 TEST_DATABASE_URL，go test -race ./cmd/... ./internal/... 全部通过。
- 新增测试至少覆盖：
  - CreateHive 同时创建 disabled 的 Queen；UpdateHive 的版本冲突；
  - 并发创建 Drone 时不超过上限（用并发 goroutine 测）；同链上限；
  - TransitionAgent 的比较并交换；
  - SendMessage 的每一条校验规则、幂等和冲突；
  - Inbox 分页顺序；ClaimMessage 租约（同时认领只有一方成功，租约过期后可重新认领）；
  - WaitInbox 能被新消息唤醒，也能在消息已存在时立即返回；
  - Token 只存哈希，明文能查到，错误明文查不到。
- 已知 internal/observability 的 TestHTTPMetricsKeepWebSocketUpgrade 本来就偶发失败，与本任务无关。

## 交付
从 dev 拉新分支，提交信息用英文约定式前缀（feat:）。向 dev 开 PR，不要合并。PR 描述列出表结构、各方法的并发与幂等保证，以及对契约的任何扩展。
````

---

## 任务 C：MCP 服务

````text
# 任务：实现 SwarmJet 的 MCP 服务（先接内存假后端）

## 背景
SwarmJet 是一个自托管的云端 Agent 集群（Go 1.26+）。用户本地的 Claude Code、Codex 等 Agent 通过 MCP 连接 SwarmJet，选择一个项目（称为 Hive），默认和项目的协调者（称为 Queen）对话，也可以直接派出和查看云端 Agent（称为 Drone）。请先读 docs/claude/adr-hive-queen-drone.md 第 3、4、7、9、12、13 节。

另有一个任务在并行实现数据层（*store.Store 上的方法）。本任务**不接真实存储**：在 internal/mcp 包里按需定义一个后端接口（方法签名必须与下方契约中的存储方法一致，以便下一轮直接用 *store.Store 接上），并提供一个内存假实现用于测试。

## 公共契约
1. 新建文件 internal/domain/hive.go，内容与下方代码**逐字一致**，不要修改（另外两个任务也会新增同一个文件）：

读取仓库中 docs/claude/round1-prompts.md 的"公共契约"一节，把其中第一段 Go 代码（从 `// internal/domain/hive.go` 到 `Token` 结构体结束）逐字写入该文件。

2. 存储方法签名（你的后端接口取其子集，签名逐字一致）：

见同一节的第二段代码"存储方法"。

3. 消息校验规则（内存假后端必须同样执行）：

见同一节的"消息校验规则"。

## 要做的事
1. 选型：优先使用官方 Go SDK（github.com/modelcontextprotocol/go-sdk），先确认其当前版本和对 Streamable HTTP 传输的支持；如果不合适，在 PR 里说明理由后再选其他方案。
2. 在 internal/mcp 中实现 `func NewHandler(backend Backend, opts Options) http.Handler`，提供 Streamable HTTP 传输。本轮不挂进 API 服务，接线留给下一轮。
3. 认证：请求头 Authorization: Bearer <token>，用后端的 TokenByPlaintext 校验；按 Token 的 Scope 控制工具权限（ADR 第 12 节表格）。
4. 实现以下工具（参数和返回值用结构化 JSON；所有返回内容都要限长，超出时截断并标明）：
   - list_hives、get_hive
   - create_hive、update_hive（仅 admin）
   - send：默认发给 Queen；可指定 drone:<id> 或 hive；From 为 local:<token-id>；未指定 chain_id 时新建
   - wait：基于 WaitInbox 长轮询，最多约 60 秒，返回调用方地址（local:<token-id>）的新消息
   - spawn_drone：创建 Drone，并向它发送一条 kind=task 的消息作为派活说明
   - cancel_drone：把非终态的 Drone 迁移到 cancelled
   - get_drone：状态、Task、Handoff、分支、PR
   - read_drone_detail：本轮只返回 Handoff 与 Inbox 往来消息，分页；日志和 diff 留给后续
   - inbox、claim：查看 Hive Inbox（to=hive）的消息，认领
   - offline_digest：给定游标，返回自那以后发给本 Token 的消息、状态发生变化的 Drone 数量与列表摘要，并返回新游标
5. 内存假后端：实现完整的 Backend 接口，执行全部消息校验规则，WaitInbox 用 channel 实现。

## 约束
- 只新增 internal/mcp 包和 internal/domain/hive.go。不要修改 internal/store、internal/api、cmd 等现有代码。
- 工具描述（description）写清楚用途和限制，它们会被本地 Agent 读取。
- 不要对整个文件运行 gofmt 或 go fmt，这个仓库本身不是 gofmt 干净的。

## 验收标准
- go build ./... 和 go vet ./... 通过；go test -race ./internal/mcp/... 通过。
- 测试用 MCP 客户端（SDK 自带或 httptest）端到端调用每个工具，至少覆盖：未认证被拒；默认 Token 不能调用 create_hive；send 的各种校验错误能正确返回给调用方；wait 能被新消息唤醒也能超时返回；spawn_drone 同时产生 task 消息；offline_digest 的游标前后一致；超长返回会被截断。

## 交付
从 dev 拉新分支，提交信息用英文约定式前缀（feat:）。向 dev 开 PR，不要合并。PR 描述列出工具清单、每个工具的参数和返回格式、选用的 SDK 与版本。
````

---

## 任务 D：Router 与 GitHub 事件归一化

````text
# 任务：实现事件 Router，把 GitHub 事件确定性地分发给 Drone、新建 Drone 或进入 Hive Inbox

## 背景
SwarmJet 是一个自托管的云端 Agent 集群（Go 1.26+）。每个项目称为 Hive（对应一个 GitHub 仓库），里面有一个可选的协调者 Queen 和多个云端 Agent（称为 Drone，每个 Drone 拥有一个分支、对应一个 Issue 或 PR）。外部事件先经过确定性的 Router，不经过 LLM。请先读 docs/claude/adr-hive-queen-drone.md 第 6、9、13 节。

现有代码里有 GitHub webhook 的签名校验和接单逻辑（internal/service 的 GitHubWebhook、webhook_queue.go，以及 internal/github），可以参考，但本任务**不修改它们**：新写一个独立的 internal/router 包，下一轮再把 webhook 入口接过来。

另有一个任务在并行实现数据层。本任务**不接真实存储**：在 internal/router 中按需定义后端接口（签名与下方契约中的存储方法逐字一致），并提供内存假实现用于测试。

## 公共契约
1. 新建文件 internal/domain/hive.go，内容与下方代码**逐字一致**，不要修改：

读取仓库中 docs/claude/round1-prompts.md 的"公共契约"一节，把其中第一段 Go 代码（从 `// internal/domain/hive.go` 到 `Token` 结构体结束）逐字写入该文件。

2. 存储方法签名（你的后端接口取其子集，签名逐字一致）：

见同一节的第二段代码"存储方法"。

3. 消息校验规则（内存假后端必须同样执行）：

见同一节的"消息校验规则"。

## 要做的事
1. 事件归一化：`func Normalize(eventType string, payload []byte) (Event, bool)`，把 GitHub webhook 原始负载转成结构化的 Event。至少支持：
   - issues：opened、labeled
   - issue_comment：created
   - pull_request：opened、synchronize、closed（区分是否 merged）
   - pull_request_review：submitted
   - pull_request_review_comment：created
   - check_suite、check_run：completed
   - status
   Event 只保留结构化字段：类型与动作、仓库（owner/name）、Issue 或 PR 编号、分支（head ref）、标签、作者登录名、作者与仓库的关系（author_association）、标题、链接，以及一段限长摘录。不认识的事件返回 false。
2. 摘要：`func Digest(e Event) string`，生成给 Agent 阅读的固定格式文本，长度不超过 domain.MaxEventDigest 个字符。
3. 路由：`func (r *Router) Route(ctx context.Context, e Event) (Decision, error)`，规则如下（ADR 第 9 节）：
   - 按仓库查找 Hive（HiveByRepository），找不到则忽略；
   - 能找到归属 Drone 的事件（按 PR 编号、分支或 Issue 编号查找，且 Drone 处于非终态）→ 向该 Drone 发 kind=event 的消息；
   - 匹配 Hive 的触发规则（issue_labeled 匹配 Label；issue_comment 匹配 CommentPrefix）且作者关系为 OWNER、MEMBER 或 COLLABORATOR → 新建 Drone（Task 中带上摘要和链接，设置 IssueNumber，新建 ChainID），再向它发一条 kind=task 的消息；如果该 Issue 已有非终态的 Drone，则改为向它发消息，不重复新建；
   - 作者关系不满足的触发 → 不新建 Drone，转入 Hive Inbox，并在消息里注明原因；
   - 其余事件 → 向 Hive Inbox（to=hive）发 kind=event 的消息；
   - 消息的 ID 由"仓库 + 事件 delivery ID + 目标"确定性生成，保证 webhook 重投时不会重复投递；
   - 新建 Drone 时遇到 ErrHiveLimit 或 ErrChainLimit，不丢事件：转入 Hive Inbox 并注明原因。
   Decision 描述本次路由的结果（投递给谁、新建了哪个 Drone、或忽略及原因），用于日志和测试断言。
4. 安全：Router 只依据结构化字段做判断，绝不解析正文里的"指令"。摘录中的内容在 Digest 里明确标记为外部内容。
5. 内存假后端：实现 Router 需要的接口子集，执行全部消息校验规则。

## 约束
- 只新增 internal/router 包和 internal/domain/hive.go。不要修改 internal/service、internal/github、internal/store 等现有代码。
- 测试用的 GitHub 负载放在 internal/router/testdata，用真实格式的 JSON（可以从 GitHub 文档示例裁剪，去掉无关字段），不要包含真实的个人信息。
- 不要对整个文件运行 gofmt 或 go fmt，这个仓库本身不是 gofmt 干净的。

## 验收标准
- go build ./... 和 go vet ./... 通过；go test -race ./internal/router/... 通过。
- 测试至少覆盖：每种事件的归一化；Digest 长度上限；有归属的 CI 结果和评审意见投给对应 Drone；标签触发新建 Drone 并发出 task 消息；同一个 Issue 不重复新建 Drone；作者关系不满足时转入 Hive Inbox；达到并发上限时转入 Hive Inbox；同一个 delivery 重投不会产生重复消息；无 Hive 的仓库被忽略。

## 交付
从 dev 拉新分支，提交信息用英文约定式前缀（feat:）。向 dev 开 PR，不要合并。PR 描述列出支持的事件、路由规则表和后端接口。
````

---

## 任务 E（可选）：网页控制台的视觉体系与静态页面

````text
# 任务：按"蜂蜜花园"风格搭建网页控制台的视觉体系，并用假数据做出新页面

## 背景
SwarmJet 是一个自托管的云端 Agent 集群，前端是 React + Vite + TypeScript（web/ 目录）。新架构的术语：项目叫 Hive，每个 Hive 有一个协调者 Queen 和多个云端 Agent（Drone）。网页控制台的设计见 docs/claude/adr-hive-queen-drone.md 第 18 节，请先完整阅读，尤其是页面结构和视觉风格两部分。

后端接口本轮还没定，所以本任务**只用本地假数据**做出页面和组件，验证视觉风格和交互。旧页面（任务、Issue 工作台、MR 工作台）由后续任务删除，本任务不要动它们。

## 要做的事
1. 视觉体系：
   - 在新的 CSS 文件中用 CSS 变量定义 ADR 第 18 节的配色（奶白 #FBF8F1、浅叶 #E9EFE6、蜂蜜 #F2B53A、薄荷 #7CC4A4、墨色 #3A3A33，以及深琥珀和柔和珊瑚红，后两者自行选色并保证 WCAG AA 对比度）、圆角、字体栈（中文思源黑体、英文 Inter、代码 JetBrains Mono、Queen 的话用衬线体）。不要从外网加载字体，只写字体栈并提供系统字体回退。
   - 组件：六边形头像（Queen 实心蜂蜜色加皇冠图标，Drone 描边六边形、颜色表示状态）、六边形状态点、状态徽章、动作卡片（Queen 的派活、取消、请求审批）、外部事件卡片、空状态（淡蜂巢底纹加线条小蜜蜂）、加载动画（小蜜蜂沿六边形路径飞行，支持 prefers-reduced-motion）。
2. 页面（挂在新路由下，例如 #/hives，不替换现有首页）：
   - Hive 列表：卡片显示仓库、Queen 状态、活跃 Drone 数、待审批数、Inbox 积压；有空状态。
   - Hive 页：顶部是 Hive 名称和分区标签（对话、Swarm、时间线、Inbox、记忆、设置）。本轮实现"对话"分区：左侧是与 Queen 的对话（Queen 的话用正文，动作用卡片，外部事件用卡片插入），右侧是 Swarm 实时面板；Queen 关闭时，对话区改为"直接派 Drone"表单。其他分区先放占位。
   - Drone 详情页：顶部状态、分支、PR、CI、Guard 检查；分区为对话、交接摘要、Diff、时间线、终端，本轮实现"对话"和"交接摘要"，其余占位。
3. 假数据：放在 web/src/fixtures/，类型定义与后端领域模型对应（Hive、Agent、Message、Handoff 等，字段名使用 camelCase，参考 ADR 第 16 节）。
4. 术语：界面直接使用 Hive、Queen、Drone，中文首次出现时写作"Queen（蜂王）"这类形式。文案遵循：句子式大小写、动词开头的按钮、不用感叹号、空状态是邀请而不是道歉。

## 约束
- 只新增文件，并在路由入口做最小改动以挂上新页面。不要修改或删除现有页面。
- 只做桌面端，不做手机适配；不做深色模式，但所有颜色必须来自 CSS 变量。
- 不引入新的 UI 框架；图标优先使用项目现有的图标方案。

## 验收标准
- npm run build 通过。
- 新增 Playwright 测试：Hive 列表渲染与空状态；Hive 页对话分区（Queen 开启与关闭两种状态）；Drone 详情页的两个分区；在 1440px 宽度下截图保存到 test-results/。
- 原有 Playwright 测试全部通过。

## 交付
从 dev 拉新分支，提交信息用英文约定式前缀（feat:）。向 dev 开 PR，不要合并。PR 描述附上截图。
````
