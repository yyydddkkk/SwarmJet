# 第二轮并行任务提示词

对应 ADR（`docs/claude/adr-hive-queen-forager.md`）第 8、9、12、13 节和第 20 节的第 3～5、8 项，以及技术验证报告 `docs/claude/spikes/opencode-driver.md`。

**本轮目标**：打通一条最小闭环——本地 Agent 通过 MCP 派出一个 Forager → Forager 在 Cell 里由 OpenCode（ACP）干活 → 平台把分支推上 GitHub 并开 PR → 这个 PR 上的 GitHub 事件经 Router 回到该 Forager。

| 任务 | 内容 | 主要改动区域 | 依赖 |
|---|---|---|---|
| A | Cell 运行时与发布器：长期容器、持久卷、工作区、ACP 会话、推送与开 PR | `internal/cell`（含 `acp`） | 公共契约 |
| B | Forager 监管者：状态机、消息送达、每轮执行、休眠与超时 | 新包 `internal/forager` | 公共契约（用假 Runtime 和假存储） |
| C | 接线：MCP 挂进 API、Token 命令、webhook 走 Router、Agent 作用域 | `internal/mcp`、`internal/router`、`internal/service`、`cmd/swarmjet/main.go` | 公共契约 |
| D | 存储：第 6 版迁移、新存储方法、Inbox 顺序与等待的加固 | `internal/store`、`internal/domain/hive.go` | 实现公共契约 |
| E | 网页控制台接真实数据：REST 接口与前端改造 | `internal/api` 新文件、`web/src/hive` | 已合入的存储方法 |

**合并顺序**：D → C → A → B → E。D、B、C 会对 `internal/domain/hive.go` 做同一处修改，A、B 会新增同一个文件 `internal/cell/cell.go`；内容与契约逐字一致，合并时就不会冲突。

**本轮不做**：把 A、B 接进 `main.go` 并做端到端验收（等 A～E 合入后，单独一个集成任务做）、Queen、Guard、Dream、多 Node 调度（本轮 Cell 只跑在服务端本机的 Docker 上）、删除 Temporal 和旧代码。

---

## 公共约束（每个任务都适用，已写进各提示词）

- 开工前 `git fetch origin`，从 `origin/dev` 新建分支；先读 ADR 和本文件的"公共契约"一节。
- 不要对整个文件运行 gofmt 或 go fmt（仓库本身不是 gofmt 干净的），只格式化你新建或大改的文件。
- 代码、测试、报告里不得出现真实凭证。
- 提交信息用英文约定式前缀；向 dev 开 PR，不要合并。
- 用临时 `postgres:18.1-alpine3.23` 容器设置 `TEST_DATABASE_URL` 跑 `go test -race ./...`。已知 `internal/observability` 的 `TestHTTPMetricsKeepWebSocketUpgrade` 偶发失败，与本轮无关。

---

## 公共契约

### 1. `internal/domain/hive.go` 的修改（D、B、C 逐字照做，只加这三行，别的不动）

```diff
@@ type Agent struct {
 	PRNumber    int64      `json:"prNumber,omitempty"`
 	NodeID      string     `json:"nodeId,omitempty"`
 	ChainID     string     `json:"chainId,omitempty"`
+	SessionRef  string     `json:"sessionRef,omitempty"` // CLI session to resume; empty starts a new one
 	Handoff     *Handoff   `json:"handoff,omitempty"`
 	CreatedAt   time.Time  `json:"createdAt"`
 	UpdatedAt   time.Time  `json:"updatedAt"`
@@ const (
 	TokenDefault TokenScope = "default" // spawn and view
 	TokenAdmin   TokenScope = "admin"   // also configure Hives and credentials
+	TokenAgent   TokenScope = "agent"   // minted for one Agent; acts as that Agent's address
 )
@@ type Token struct {
 	ID        string     `json:"id"`
 	Name      string     `json:"name"`
 	Scope     TokenScope `json:"scope"`
+	AgentID   string     `json:"agentId,omitempty"` // set only for TokenAgent
 	CreatedAt time.Time  `json:"createdAt"`
 }
```

### 2. 新文件 `internal/cell/cell.go`（A、B 逐字新建）

```go
// Package cell runs Agents in long-lived containers. This file is the round-2
// contract shared by the Cell runtime and the Forager supervisor; change it
// only by adding, never by editing existing declarations.
package cell

import (
	"context"
	"errors"
	"time"
)

// Spec is everything needed to start or resume one Agent's Cell.
type Spec struct {
	AgentID    string
	HiveID     string
	Repository string            // GitHub owner/name
	Branch     string            // the Agent's own branch
	BaseBranch string            // branch to start from; empty means the repository default
	CLI        string            // "opencode" this round
	Model      string            // provider/model, always pinned explicitly
	SessionRef string            // empty starts a new CLI session; otherwise it is loaded
	Env        map[string]string // CLI secrets such as OPENCODE_AUTH_CONTENT; never logged
	MCPURL     string            // SwarmJet MCP endpoint as reachable from inside the Cell
	MCPToken   string            // Agent-scoped token for MCPURL
}

// UpdateKind classifies streamed output of a turn.
type UpdateKind string

const (
	UpdateText     UpdateKind = "text"
	UpdateThought  UpdateKind = "thought"
	UpdateToolCall UpdateKind = "tool_call"
	UpdateToolDone UpdateKind = "tool_done"
	UpdatePlan     UpdateKind = "plan"
)

// Update is one streamed piece of a turn.
type Update struct {
	Kind  UpdateKind
	Text  string // truncated by the runtime to at most MaxUpdateText characters
	Title string // tool title for tool updates
	At    time.Time
}

// MaxUpdateText caps Update.Text.
const MaxUpdateText = 4000

// TurnResult is how a turn ended.
type TurnResult struct {
	StopReason   string // end_turn, cancelled, max_tokens, refusal, ...
	InputTokens  int64
	OutputTokens int64
}

// Session is a connected CLI session inside a running Cell. One turn at a time.
type Session interface {
	// Ref is the CLI session ID; persist it and pass it back as Spec.SessionRef.
	Ref() string
	// Prompt runs one turn. sink must not block; it is called from the reader.
	Prompt(ctx context.Context, text string, sink func(Update)) (TurnResult, error)
	// Cancel interrupts the current turn; the session stays usable.
	Cancel(ctx context.Context) error
	// Close stops the CLI process; the container keeps running.
	Close() error
}

// Runtime manages Cells. Every method is idempotent per AgentID.
type Runtime interface {
	// Start creates or restarts the container and its volumes, prepares the
	// workspace on Spec.Branch, starts the CLI and creates or loads the session.
	Start(ctx context.Context, spec Spec) (Session, error)
	// Stop closes the CLI and stops the container; volumes stay (parked).
	Stop(ctx context.Context, agentID string) error
	// Remove deletes the container and keeps the volumes (hibernated).
	Remove(ctx context.Context, agentID string) error
	// Destroy deletes the container and the volumes (terminal cleanup).
	Destroy(ctx context.Context, agentID string) error
}

// Publisher moves work from a Cell workspace to GitHub. It runs on the
// platform side with platform credentials; a Cell never holds Git credentials.
type Publisher interface {
	// Publish commits any uncommitted changes as a WIP commit, then, if
	// Spec.Branch is ahead of the base, pushes it and creates or updates the PR
	// and returns its number. It returns 0 and no error when there is nothing
	// to publish.
	Publish(ctx context.Context, spec Spec, title, body string) (pr int64, err error)
}

// ErrNoCell is returned for an AgentID that has no Cell.
var ErrNoCell = errors.New("cell: no such cell")
```

### 3. 新增存储方法（D 在 `*store.Store` 上实现；B、C 在各自包里按需定义接口子集，签名逐字一致）

```go
SetAgentSession(ctx context.Context, id, sessionRef string) error                          // ErrNotFound if missing
SetAgentPR(ctx context.Context, id string, pr int64) error                                 // ErrNotFound if missing
Unhandled(ctx context.Context, hiveID string, to domain.Address, limit int) ([]domain.Message, error) // pending or delivered, insertion order
CreateAgentToken(ctx context.Context, agentID string) (plaintext string, t domain.Token, err error) // scope agent, AgentID set
RevokeAgentTokens(ctx context.Context, agentID string) error                               // revoked tokens no longer resolve
```

`Agent`、`Foragers`、`ForagerBy*` 等读取方法要带上 `SessionRef`；`TokenByPlaintext` 要带上 `AgentID`，并且查不到已吊销的 Token。

---

## 任务 A：Cell 运行时与发布器

````text
# 任务：实现 Cell 运行时（cell.Runtime）和发布器（cell.Publisher）

## 背景
SwarmJet 是一个自托管的云端 Agent 集群（Go 1.26+），正在按 docs/claude/adr-hive-queen-forager.md 重构。每个 Forager 是一个在长期 Docker 容器（Cell）里运行的 CLI 进程，平台通过 ACP（stdio 上的 JSON-RPC）驱动它。第一轮的技术验证已经完成：先读 ADR 第 8、13 节，再完整读 docs/claude/spikes/opencode-driver.md（尤其第 4、6、7、8 节），以及实验性原型 internal/cell/acp。

本轮另有任务在并行开发 Forager 监管者（internal/forager），它只通过 internal/cell/cell.go 里的接口调用你。

## 公共契约
读 docs/claude/round2-prompts.md 的"公共契约"一节，把第 2 段代码逐字写入新文件 internal/cell/cell.go（另一个任务也会新建同一个文件，内容一致才能无冲突合并）。不要改这个文件；需要的辅助类型放在别的文件里。

## 要做的事
1. internal/cell/acp：把原型从"实验性"整理为正式代码（去掉 EXPERIMENTAL 标注），保留现有测试。补齐技术验证报告第 7 节里和本任务相关的坑：stdio MCP 的 args/env 必须是数组；会话 sink 不能阻塞读取循环；任何重连路径都要保留 sessionId。
2. 新包 internal/cell/docker（名字可以自定）实现 cell.Runtime：
   - 每个 Agent 一个容器，名字和标签里带 AgentID、HiveID，便于按标签查找和清理；镜像来自配置（默认沿用 AGENT_IMAGE，现有 deploy/Dockerfile.agent 已包含 OpenCode）。
   - 两个持久位置：工作区（容器内固定挂在 /workspace，宿主机上是 <DATA_DIR>/cells/<agent-id>/workspace 的绑定目录，方便平台侧推送）和 CLI 的 Home/数据目录（命名卷，保存 OpenCode 的会话库）。会话绑定 cwd，所以 CLI 必须始终在 /workspace 启动。
   - Start：容器不存在就创建，已停止就启动；工作区为空时在宿主机侧克隆仓库并切到 Spec.Branch（分支不存在就从 BaseBranch 新建）；然后 docker exec -i 启动 opencode acp，initialize；Spec.SessionRef 为空就 session/new，否则 session/load，load 失败时新建会话并在日志里记录。返回的 Session 实现 Ref/Prompt/Cancel/Close。
   - OpenCode 配置用 OPENCODE_CONFIG_CONTENT 注入：model 固定为 Spec.Model；关闭自动更新；把 Spec.MCPURL + Spec.MCPToken 作为 http 类型的 MCP 服务挂进会话（session/new 和 session/load 的 mcpServers）。Spec.Env 里的值只通过 docker exec -e 传入，绝不写进镜像、文件或日志。
   - 权限：容器就是隔离边界，在 Session 内实现 session/request_permission 回调，一律选择 allow_once；把每次批准记录为一条 Update（Kind=tool_call，Title 里注明 permission）。
   - Stop/Remove/Destroy 按契约注释实现，并且幂等（目标不存在不算错误）。Destroy 同时删除工作区目录和命名卷。
   - 资源限制、网络、用户 ID 等参数复用 internal/agent 里现有的配置快照和资源限制逻辑，不要另起一套。
3. 实现 cell.Publisher（同一个包或子包均可），在平台侧对宿主机上的工作区目录执行 git：
   - 有未提交改动时，用固定的作者身份提交一个 "wip: forager <id>" 提交；
   - 分支领先基线时，只推送 Spec.Branch，绝不推送其他分支（这是 Guard"分支归属"的雏形，用测试锁住）；
   - 推送后调用 GitHub 创建或更新 PR（复用 internal/github 客户端的 CreateOrUpdatePR），返回 PR 编号；
   - 推送凭证通过构造参数注入一个 TokenSource（func(ctx, repository string) (string, error)），用现有的 askpass 方式传给 git，不写进 remote URL、不落盘。容器里永远没有 Git 凭证。

## 约束
- 只修改 internal/cell/ 下的文件（可以新建子包）。如果确实需要 internal/agent 或 internal/workspace 暴露一个小函数，可以做最小的导出改动，并在 PR 里说明。
- 不改 internal/cell/cell.go；不碰 cmd/swarmjet/main.go（接线在集成任务里做）。

## 验收标准
- go build ./... 和 go vet ./... 通过；go test -race ./internal/cell/... 通过。
- 不依赖 Docker 的单元测试：用假 docker 执行器和假 ACP agent 覆盖 Start（新建与加载会话、load 失败回退）、Prompt 流式输出与截断、Cancel 后可继续、Stop/Remove/Destroy 幂等、Env 不出现在任何日志和错误信息里。
- Publisher 用本地临时裸仓库测试：无改动返回 0；有未提交改动会生成 WIP 提交并推送；只推送 Spec.Branch；假 GitHub 客户端收到正确的 source/target。
- 真实环境集成测试默认跳过（参照 spike_docker_test.go 的开关方式）：用 SWARMJET_CELL_E2E=1 打开后，用免费模型 opencode/big-pickle 跑完"Start → 让它在 /workspace 新建一个文件并提交 → Stop → 再 Start 用同一个 SessionRef 续聊 → Destroy"，并在 PR 描述里贴出运行结果。

## 交付
提交信息用英文约定式前缀（feat:）。向 dev 开 PR，不要合并。PR 描述写清容器、卷、目录的命名规则，凭证的流向，以及仍未解决的问题（例如崩溃一致性）。
````

---

## 任务 B：Forager 监管者

````text
# 任务：实现 Forager 监管者（internal/forager）

## 背景
SwarmJet 是一个自托管的云端 Agent 集群（Go 1.26+、PostgreSQL、River），正在按 docs/claude/adr-hive-queen-forager.md 重构。先读 ADR 第 8、9、12、13 节。

Forager 是一个云端 Agent 实例：一件活、一个分支、一个 PR。消息写进它的 Inbox 后，存储层的投递 Job 会调用 store.MessageSink（见 internal/store/message.go 的 DeliverMessage）。你要实现的监管者负责：收到消息后启动或唤醒 Forager 的 Cell，把消息作为新一轮对话送给 CLI，每轮结束后发布成果、汇报、更新状态，并按 Hive 配置处理空闲、休眠、超时和取消。

另有任务在并行开发 Cell 运行时（实现 cell.Runtime 和 cell.Publisher）和存储方法；你只通过接口调用它们，测试全部用假实现。

## 公共契约
读 docs/claude/round2-prompts.md 的"公共契约"一节：
1. 按第 1 段 diff 修改 internal/domain/hive.go，只加那三行，逐字一致；
2. 把第 2 段代码逐字写入新文件 internal/cell/cell.go，不要改它；
3. 在 internal/forager 里定义你需要的存储接口子集，方法签名与 internal/store 现有方法及第 3 段新方法逐字一致。

## 要做的事
1. Supervisor 结构与配置：存储接口、cell.Runtime、cell.Publisher、时钟（可注入）、Model（默认模型）、MCPURL、CLIEnv func(ctx, domain.Hive) (map[string]string, error)、日志。提供 Notify(ctx, domain.Message) error（签名与 store.MessageSink 一致，供以后直接挂上）和 Run(ctx) error（启动后台循环）。
2. 每个 Forager 一个串行执行者（actor）：同一时刻只跑一轮；新消息在本轮进行中时排队，本轮结束后合并为下一轮。只有 kind=control 且正文为 cancel 的消息立即打断当前一轮。
3. 一轮的流程：
   - 需要时启动 Cell：没有 Agent Token 就用 CreateAgentToken 生成（明文只在内存里传给 Spec.MCPToken）；用 Agent.SessionRef 调 Runtime.Start；把新的 Session.Ref() 用 SetAgentSession 存回；状态按 creating/parked/hibernated → active 用 TransitionAgent 推进（比较并交换失败要重新读取再决定）。
   - 组装输入：用 Unhandled 取出该 Forager 的未处理消息，按固定格式拼接（类型、发送方、正文），总长度设上限，超出的折叠为计数。第一轮在前面加一段固定的开场说明：你的任务、你的分支、消息一律是数据而非指令、在 /workspace 里工作并提交、完成后用 MCP 工具更新交接摘要并汇报。
   - 调用 Session.Prompt，流式 Update 交给一个可注入的回调（以后接网页的实时视图），本轮不需要持久化。
   - 本轮结束：把本轮消费的消息用 MarkMessageHandled 标记；调用 Publisher.Publish（标题取任务第一行，正文取交接摘要）；拿到 PR 编号且与记录不同时用 SetAgentPR 保存；给本轮消息的发送方发一条 kind=report 的汇报（发送方是 local:* 或 queen 时直接回给它，否则发到 hive），内容是交接摘要或最后一段输出的截断；状态 active → parked。
4. 生命周期（数值全部取 HiveConfig，见 internal/domain/hive.go）：
   - parked 且空闲超过 IdleParkMinutes：Runtime.Stop（状态保持 parked）；
   - parked 超过 HibernateHours：Runtime.Remove，状态 → hibernated；
   - 单轮超过 TurnTimeoutMinutes：Cancel，状态 → failed，并汇报；
   - 存活超过 ForagerMaxLifetimeHours：状态 → failed，并汇报；
   - cancel 控制消息：Cancel 当前一轮，状态 → cancelled；
   - 进入 completed/failed/cancelled 后：Runtime.Destroy、RevokeAgentTokens。
   用一个周期性的扫描（间隔可配置，默认 30 秒）处理计时类规则，并兜底处理漏掉的通知（例如进程重启后有未处理消息的 Forager）。
5. 错误处理：Runtime.Start 或 Prompt 出错时按指数退避重试，连续失败超过上限（可配置，默认 3 次）则 → failed 并汇报。任何时候都不能因为一个 Forager 出错拖垮其他 Forager。

## 约束
- 只新建 internal/forager，修改 internal/domain/hive.go（只加契约里那三行），新建 internal/cell/cell.go（逐字）。不碰 internal/store、internal/cell 的其他文件和 cmd/swarmjet/main.go（接线在集成任务里做）。
- 本轮只处理 role=forager 的 Agent，Queen 不在范围内。

## 验收标准
- go build ./... 和 go vet ./... 通过；go test -race ./internal/forager/... 通过。
- 用假存储、假 Runtime（可脚本化每轮的输出、耗时和错误）、假 Publisher 和假时钟，测试至少覆盖：
  - 首条任务消息触发 Start、生成 Token、保存 SessionRef、完成一轮、标记消息、发布、保存 PR、汇报、进入 parked；
  - 本轮进行中到达的两条消息合并成下一轮；
  - cancel 控制消息立即打断，进入 cancelled 并销毁 Cell、吊销 Token；
  - 空闲 → Stop，休眠 → Remove → hibernated，之后来消息能用原 SessionRef 唤醒；
  - 单轮超时和存活超时进入 failed；
  - Start 连续失败进入 failed；一个 Forager 卡住时其他 Forager 照常运行；
  - 重启后扫描能接上未处理的消息。

## 交付
提交信息用英文约定式前缀（feat:）。向 dev 开 PR，不要合并。PR 描述画出状态迁移表，并写清每条消息"至少送达一次"的保证从哪里来。
````

---

## 任务 C：接线——MCP、Router、Token

````text
# 任务：把 MCP 服务和 Router 接到真实存储与 API 服务上，并支持 Agent 作用域

## 背景
SwarmJet 是一个自托管的云端 Agent 集群（Go 1.26+、PostgreSQL、River），正在按 docs/claude/adr-hive-queen-forager.md 重构。先读 ADR 第 9、12、13 节。第一轮已经合入：internal/mcp（MCP 服务，目前只接了内存假后端）、internal/router（GitHub 事件归一化与确定性路由）、internal/store 的 Hive 数据层。本任务把它们接起来。

另有任务在并行实现新的存储方法（契约第 3 段）、Cell 运行时和 Forager 监管者；你需要的新存储方法先在接口里声明、测试里用假实现。

## 公共契约
读 docs/claude/round2-prompts.md 的"公共契约"一节：按第 1 段 diff 修改 internal/domain/hive.go，只加那三行，逐字一致；需要的新存储方法按第 3 段的签名声明。

## 要做的事
1. MCP 挂进服务：在 cmd/swarmjet/main.go 的路由上挂 /mcp，用 mcp.NewHandler(store, ...)；确认 *store.Store 满足 mcp.Backend（加一个编译期断言）。不经过网页的会话登录，只认 Bearer Token。
2. Token 命令：给 swarmjet 加一个运行模式 token（与现有 serve/worker 并列），支持 `swarmjet token create --name <name> --scope default|admin`，明文只打印一次。在 deploy/README.md 里补一段"给本地 Claude Code / Codex 配置 MCP"的说明（SSH 隧道 + Bearer Token，见 ADR 第 12 节）。
3. Agent 作用域（scope=agent 的 Token，AgentID 指向一个 Agent）：
   - 发送方地址固定为该 Agent 的地址（forager:<id> 或 queen），只能访问自己所在的 Hive；
   - Forager 可用：send（发给 queen、hive 或 local:*；Forager 之间由存储层拒绝）、get_forager（只限自己）、inbox（只读自己的 Inbox）、新增的 update_handoff（更新自己的交接摘要，字段见 domain.Handoff，限长）；
   - Queen 额外可用 spawn_forager、cancel_forager、get_forager、read_forager_detail、Hive Inbox 的 inbox/claim（Queen 本轮不会真正运行，但权限先按 ADR 第 12 节的表落实）；
   - 其他工具对 agent 作用域一律拒绝。用表驱动测试锁住这张权限表。
4. 幂等：spawn_forager 和 send 增加可选的 requestId。给定 requestId 时，用 Token ID + requestId 派生确定的 Forager ID / 消息 ID；重复调用返回已有结果，不重复创建。
5. Router 接进 webhook：GitHub 的 webhook Job（internal/service 的 HandleWebhookJob）里，先按仓库 owner/name 查 Hive：
   - 有 Hive：用 router.NormalizeDelivery 归一化，交给 Router.Route，并按 Decision 打结构化日志；该仓库不再走旧的扫描逻辑；
   - 没有 Hive：保持旧逻辑不变。
   - 另外，pull_request opened/reopened 事件按分支找到了 Forager、而该 Forager 还没有记录 PR 编号时，调用 SetAgentPR 记录下来（放在 Router 里，带测试）。
6. 第一轮审查留下的 MCP 小问题：wait 的超时在父 ctx 取消时要返回取消而不是超时；所有列表工具的上限保持生效。

## 约束
- 可以修改 internal/mcp、internal/router、internal/service 中处理 webhook 的部分、cmd/swarmjet/main.go、deploy/README.md、internal/domain/hive.go（只加契约里那三行）。
- 不实现存储方法本身（D 负责），不碰 internal/cell、internal/forager。
- 旧的 GitLab webhook 和旧的扫描逻辑不要删，也不要改行为。

## 验收标准
- go build ./... 和 go vet ./... 通过；go test -race ./... 通过（用临时 Postgres）。
- 新增测试至少覆盖：/mcp 无 Token、错误 Token、default、admin、agent 五种情况的工具可见性和拒绝；agent 作用域的发送方地址不可伪造；requestId 幂等；有 Hive 的仓库 webhook 走 Router，没有 Hive 的仓库仍走旧逻辑；PR 编号记录。
- 用 httptest 起一个完整的 /mcp，按 MCP 握手、list_tools、调用 spawn_forager 的顺序跑一遍端到端测试。

## 交付
提交信息用英文约定式前缀（feat:）。向 dev 开 PR，不要合并。PR 描述附上权限表和本地 Agent 的接入示例配置（Token 用占位符）。
````

---

## 任务 D：存储加固与新方法

````text
# 任务：Hive 数据层第 6 版——新方法与 Inbox 加固

## 背景
SwarmJet 是一个自托管的云端 Agent 集群（Go 1.26+、PostgreSQL 18、River），正在按 docs/claude/adr-hive-queen-forager.md 重构。先读 ADR 第 9、16 节，再读 internal/store 里的 hive.go、message.go、token.go 和 schema_upgrade_5.sql（第一轮实现）。

本轮另有任务在并行开发 Forager 监管者、MCP 接线和网页接口，它们会按契约调用你新增的方法。

## 公共契约
读 docs/claude/round2-prompts.md 的"公共契约"一节：按第 1 段 diff 修改 internal/domain/hive.go，只加那三行，逐字一致；在 *store.Store 上实现第 3 段的全部方法，签名逐字一致。

## 要做的事
1. 新增迁移 internal/store/schema_upgrade_6.sql（第 5 版已经合入 dev，不要再改它），并在 Migrate 里接上：
   - agents 加 cli_session_ref；
   - mcp_tokens 加 agent_id（可空，指向 agents）和 revoked_at。
2. 实现契约第 3 段的方法；让现有读取方法带上 SessionRef，TokenByPlaintext 带上 AgentID 并忽略已吊销的 Token。
3. 第一轮审查留下的加固：
   - Inbox 顺序：seq 是插入时分配、不是提交时分配，并发事务可能让读取方用游标跳过一条稍后才提交的消息。修法：SendMessage 在插入前对同一个 Hive 取事务级咨询锁（pg_advisory_xact_lock），保证同一 Hive 内 seq 顺序等于提交顺序。写一个并发测试：多个 goroutine 并发发送，同时一个读者用游标持续追读，最终必须一条不漏、一条不重。
   - WaitInbox 连接：现在每个等待者独占一个连接做 LISTEN，长轮询多了会耗尽连接池。改为 Store 内部只有一条共享的 LISTEN 连接（懒启动、断线自动重连），按 (hive, to) 把通知分发给等待者；等待者另外每隔几秒重新检查一次，防止漏掉通知。测试要覆盖：并发 50 个等待者只占用一个监听连接；监听连接断开后能重连并继续唤醒。
   - 投递队列未配置：现在 Store.Jobs 不支持消息投递时会静默跳过，消息永远停在 pending。改为返回明确的错误；需要不带队列的测试场景时，提供一个测试用的内存投递实现，而不是靠静默跳过。
4. 保持旧表、旧方法、旧的 store.ErrNotFound / store.ErrConflict 不变。

## 约束
- 只修改 internal/store、internal/domain/hive.go（只加契约里那三行）；如果投递队列的错误改动需要 cmd/swarmjet/main.go 配合，只做最小改动。
- 不要对整个文件运行 gofmt 或 go fmt。

## 验收标准
- go build ./... 和 go vet ./... 通过；起临时 postgres:18.1-alpine3.23，设置 TEST_DATABASE_URL，go test -race ./... 全部通过。
- 新增测试覆盖：五个新方法的正常与 ErrNotFound 路径；Token 吊销后查不到；从第 5 版数据库升级到第 6 版后旧数据可读；上面三项加固各自的测试。

## 交付
提交信息用英文约定式前缀（feat: 或 fix:）。向 dev 开 PR，不要合并。PR 描述写清第 6 版的表结构变化，以及顺序、唤醒两项保证的实现方式。
````

---

## 任务 E：网页控制台接真实数据

````text
# 任务：给网页控制台加 REST 接口，并让 #/hives 页面使用真实数据

## 背景
SwarmJet 是一个自托管的云端 Agent 集群（Go 1.26+、React + Vite + TypeScript），正在按 docs/claude/adr-hive-queen-forager.md 重构。先读 ADR 第 12、18 节。第一轮已经做好了网页控制台的静态页面（web/src/hive，路由 #/hives，目前用 web/src/fixtures 的假数据），存储层已有 Hive / Agent / Message 的方法（internal/store 的 hive.go、message.go）。

## 要做的事
1. 后端：在 internal/api 新增一个文件（例如 hives.go），挂在现有的会话登录鉴权之下（参考 api.go 里其他接口的鉴权方式），提供：
   - GET /api/hives、POST /api/hives、GET /api/hives/{id}
   - GET /api/hives/{id}/foragers、POST /api/hives/{id}/foragers（派 Forager：创建 + 写一条 kind=task 的消息，与 MCP 的 spawn_forager 行为一致）
   - GET /api/foragers/{id}、POST /api/foragers/{id}/cancel（写一条 kind=control、正文 cancel 的消息）
   - GET /api/hives/{id}/messages?to=&after=&limit=、POST /api/hives/{id}/messages（发送方地址用 local:web-<登录用户名>）
   - 所有列表有上限，正文按 domain 的限长截断；错误映射到合适的 HTTP 状态码（ErrNotFound → 404，ErrConflict → 409，上限类错误 → 429 或 422，并给出可读信息）。
2. 前端：web/src/hive 改为通过一个小的 API 客户端读写上面的接口，替换 fixtures：
   - Hive 列表、Hive 页（对话流、Swarm 面板、Inbox）、Forager 详情页都显示真实数据；
   - 对话流和 Swarm 面板每 5 秒轮询一次（本轮不做 SSE/WebSocket）；
   - 派 Forager、取消 Forager、给 Queen 或 Forager 发消息的操作接到真实接口，失败时显示可读的错误提示；
   - 加载中用现有的像素加载动画，空状态用现有的空状态插画；
   - fixtures 只保留给 Playwright 测试使用。
3. Playwright：用 page.route 模拟这些接口，覆盖列表、派活、取消、发消息、接口报错的显示。

## 约束
- 后端只新增 internal/api 下的文件，并在 api.go 里做最小的路由注册改动；不改 internal/store（需要的存储方法都已存在；如果确实缺，在 PR 里说明，不要自己加）。
- 前端只改 web/src/hive、web/src/fixtures 和 web/tests 下与 Hive 控制台相关的文件，不碰旧页面。
- 像素图标体系的规范在 docs/claude/design/pixel-icons.json，生成文件 web/src/hive/pixel-icons.ts 不要手改；需要新图标时改规范文件，再运行 cd web && node scripts/generate-pixel-icons.mjs。

## 验收标准
- go build ./... 和 go vet ./... 通过；接口用 httptest 加临时 Postgres 测试，覆盖鉴权、各接口的正常路径和错误映射。
- cd web && npm run build 和 npx playwright test 全部通过。
- 更新 docs/claude/round1-web-console/ 下受影响的截图（数据来自 Playwright 的模拟接口）。

## 交付
提交信息用英文约定式前缀（feat:）。向 dev 开 PR，不要合并。PR 描述列出接口清单和请求/响应示例。
````
