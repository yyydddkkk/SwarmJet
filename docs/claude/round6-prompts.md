# 第六轮：前端对接前的后端能力与平台修复

日期：2026-10-20。前端按 [frontend-stars-design.md](frontend-stars-design.md) 重做，由用户并行开发。这一轮只做后端，补齐星图需要的数据和操作，并修掉第五轮验收（[acceptance-round5.md](acceptance-round5.md) 第 10 节）留下的平台问题。

## 已定决策

- **命名**：界面文字改为大白话，对照表见设计文档第 2 节（Hive → 项目，Queen → 主 Agent，Forager → Agent）。这一轮不改代码、API、数据库里的名字，路径和 JSON 字段沿用 hive、queen、forager。
- **认证**：新接口都是给网页用的，要求管理员会话。会改状态的请求（POST、PATCH）同时要求现有的 CSRF 头，和 `PATCH /api/hives/{id}` 一致。
- **实时推送**：只做服务端到浏览器的单向推送，用 SSE，不用 WebSocket。v1 不保证补发：客户端每次连上（包括断线重连）都先重新拉一遍快照，之后靠事件增量更新。
- **迁移编号**：现在是 13。任务 A 用 14，任务 D 用 15，编号固定，和合并顺序无关。
- **合并顺序**：D → C → B → A。A 改动面最大，最后合入，统一处理冲突。

## 公共契约

各任务按这里实现，前端也按这里写 mock。字段名用 camelCase，时间用 RFC 3339 UTC。

### 1. 事件流（任务 A）

`GET /api/events?hive=<id>`

- 返回 `text/event-stream`，要求管理员会话；`hive` 可省略，省略时推送全部项目。
- 连上后先发 `event: ready`，之后每 25 秒发一行注释作为心跳（`: ping`）。
- 每条事件是 `event: <type>` 加一行 `data: <json>`。

| type | data | 何时发 |
|---|---|---|
| `message` | `{id, hiveId, from, to, kind, trust, chainId, createdAt, preview}`，`preview` 是正文前 120 个字符 | 新消息写入 |
| `agent` | 与 `GET /api/hives/{id}/foragers` 里单个 Agent 相同的对象 | Agent 创建、状态变化、PR 关联变化 |
| `pr` | `{hiveId, number, headSha, state, ready, merged}` | attest 写入、合并成功、该 PR 的 webhook 事件被路由 |
| `methodology` | 与第 4 节方法论事件的单条相同 | 发布、激活、自动回滚 |
| `queue` | 与第 4 节排队列表的单条相同，另加 `state`：waiting、started、expired | 排队、补派、过期 |
| `hive` | 与 `GET /api/hives/{id}` 的 `hive` 相同 | 项目配置或 Queen 开关变化 |

### 2. PR 列表（任务 B）

`GET /api/hives/{id}/prs?state=open|all`

- 默认 `open`；`all` 额外包含最近 7 天内合并或关闭的。
- 返回 `{prs:[{number, title, url, headSha, state, merged, ready, reasons, ownerId, attestations, updatedAt}]}`。
  - `ownerId` 是拥有这个 PR 的 Agent。
  - `attestations` 和现有 `GET /api/hives/{id}/prs/{n}` 里的同名字段一致。
- 门禁状态需要访问 GitHub，按 PR 和 head 缓存 30 秒，避免触发限流。

### 3. 网页端合并（任务 B）

`POST /api/hives/{id}/prs/{n}/merge`，请求体 `{headSha}`。

- `headSha` 必填，必须等于当前 head。不等时返回 409，说明 head 已变化，请重新查看。这样能避免用户看到的是旧提交、合并的却是新提交。
- 门禁检查和合并方式与 MCP 的 `merge_pr` 完全一致，复用同一个服务。
- 返回和 `merge_pr` 相同的结构：`{merged, queued, method, headSha, ready, reasons, message}`。
- 操作者记为当前管理员。

### 4. 列表类接口（任务 C）

| 接口 | 返回 |
|---|---|
| `GET /api/hives/{id}/queue` | `{queue:[{agentId, role, task, position, createdAt, expiresAt}]}`，只含 waiting，按先后排序。`task` 截取前 200 个字符 |
| `GET /api/hives/{id}/methodology/events?limit=50` | `{events:[{version, action, actor, note, createdAt}]}`，倒序。`action` 是 publish 或 activate；自动回滚的 actor 是 system |
| `GET /api/hives/{id}/signals?since=<RFC3339>` | `{signals:[{messageId, source, kind, number, title, url, claimed, claimedBy, createdAt}]}`，最近的外部事件（trust=external）。`source` 是 github；`kind` 是 issue、pull_request、check 等；`claimed` 表示已被 Queen 或某个 Agent 处理；`since` 默认 24 小时前 |

`GET /api/hives/{id}/foragers` 返回的每个 Agent 新增两个字段：

- `costUsd`：这个 Agent 累计的模型花费，来自 `llm_usage`。
- `issueNumber`：关联的 Issue，已有字段，任务 D 负责让它真正有值。

## 任务

### 任务 A：实时事件流

````text
# 任务：给网页加实时事件流（SSE）

## 背景
SwarmJet 的前端要改成一张实时星图（设计见 docs/claude/frontend-stars-design.md）。现在前端只能轮询（web/src/hive/use-resource.ts），做不出"系统正在运转"的效果。先读 docs/claude/round6-prompts.md 的"已定决策"和"公共契约"第 1 节，以及 internal/store/message.go、internal/store/inbox_listener.go（现有的 LISTEN/NOTIFY 用法）、internal/api。

## 要做的事
1. 实现 `GET /api/events?hive=<id>`，按公共契约第 1 节推送六类事件、ready 和心跳。要求管理员会话；客户端断开要及时释放资源。
2. 事件来源用数据库触发器，不在业务代码里到处补发通知，避免漏掉某条代码路径：
   - 在迁移 14 里给 messages、agents、attestations、methodology_events、spawn_queue、hives 加 AFTER INSERT/UPDATE 触发器，向新频道 `swarmjet_events` 发 pg_notify。
   - payload 只带 `{type, hiveId, id}`；pg_notify 有 8000 字节上限，不能放整行。
   - api 进程里一个共享的 LISTEN 连接收到通知后，从数据库读出实体，转成契约里的结构，扇出给订阅了这个项目的连接。
   - agents 只在 state、pr_number、issue_number、role_name 变化时通知，避免每次 updated_at 都推。
3. "合并成功"和"PR webhook 被路由"这两类 pr 事件没有对应的表，在 merge 服务和 Router 里显式发同频道的通知。
4. 背压：单个连接的发送缓冲满了就断开它，由客户端重连后重新拉快照。不要让慢客户端拖住整个扇出。
5. 部署：确认反向代理和现有 HTTP 服务器的写超时不会切断长连接；如有需要，只对这个路由放宽，并在 deploy/README.md 写明。

## 约束
- 不改 domain 契约类型的现有字段。
- 不碰 web/（前端由用户并行开发）。
- 不要对整个文件运行 gofmt；代码、测试、文档里不得出现真实凭证。

## 验收标准
- go build ./... 和 go vet ./... 通过；带 TEST_DATABASE_URL 的 go test -race -p 1 ./... 通过。
- 测试覆盖：
  - 每类事件至少一个"写库后订阅者收到正确结构"的测试；
  - hive 过滤；未登录返回 401；
  - 慢客户端被断开而不影响其他客户端；
  - 迁移 14 可以在已有 13 的库上重复执行。
- 在 PR 描述里给出一段用 curl 观察事件流的命令。

## 交付
提交信息用英文约定式前缀（feat:）。向 dev 开 PR，不要合并。
````

### 任务 B：PR 列表与网页端合并

````text
# 任务：网页端的 PR 列表和合并

## 背景
星图上每个 PR 是一颗带环的行星，满足合并条件时由用户在网页上确认合并。现在网页端只有 `GET /api/hives/{id}/prs/{n}`（只读），合并只能走 MCP 的 merge_pr。先读 docs/claude/round6-prompts.md 的"已定决策"和"公共契约"第 2、3 节，以及 internal/api/prs.go、internal/merge、internal/mcp/merge_tools.go。

## 要做的事
1. 实现 `GET /api/hives/{id}/prs?state=open|all`，按契约第 2 节返回：
   - 列表来源：这个项目里 Agent 关联过的 PR（agents.pr_number），加上有 attestation 的 PR。
   - 门禁状态复用 merge 服务，按 PR 号和 head 缓存 30 秒。
2. 实现 `POST /api/hives/{id}/prs/{n}/merge`，按契约第 3 节：
   - 要求管理员会话和 CSRF；`headSha` 不匹配返回 409。
   - 门禁与合并完全复用 merge_pr 的服务代码，不复制逻辑。
   - 审计记录写明操作者是网页管理员。
3. 合并成功后，现有"PR 关闭即结束 owner"的流程不变，这里不需要额外处理。

## 约束
- 只新增路由和必要的服务方法；不改 merge 门禁规则本身（规则来自方法论）。
- 不碰 web/。不要对整个文件运行 gofmt；不得出现真实凭证。

## 验收标准
- go build、go vet、带 TEST_DATABASE_URL 的 go test -race -p 1 ./... 通过。
- 测试覆盖：
  - 列表的 open/all；缓存命中时不再请求 GitHub；
  - 合并成功；门禁不满足返回 reasons 且不合并；
  - headSha 不匹配返回 409；缺 CSRF 返回 403；未登录返回 401。

## 交付
提交信息用英文约定式前缀（feat:）。向 dev 开 PR，不要合并。
````

### 任务 C：排队、方法论事件、外部事件列表与 Agent 花费

````text
# 任务：星图需要的几个列表接口

## 背景
星图上平台边界外的十字是外部事件，边界上的空心小圈是排队中的请求，项目面板里有方法论的版本历史，列表视图要显示每个 Agent 的花费。这些数据库里都有，但网页端没有接口。先读 docs/claude/round6-prompts.md 的"公共契约"第 4 节，以及 internal/api/hives.go、internal/api/methodology.go、internal/store/spawn_queue.go、internal/store/methodology_rollback.go。

## 要做的事
1. `GET /api/hives/{id}/queue`、`GET /api/hives/{id}/methodology/events`、`GET /api/hives/{id}/signals`，按契约第 4 节实现，都要求管理员会话。
2. signals 里的 claimed 判定写在一个独立的 store 方法里，并在 PR 描述里写清规则。规则是：消息已被处理（handled），或者事件对应的 Issue/PR 已经有 Agent 关联，就算已认领。
3. `GET /api/hives/{id}/foragers` 的每个 Agent 增加 `costUsd`。用一次聚合查询算出整个列表的花费，不要每个 Agent 查一次。

## 约束
- 只读接口，不改任何写路径。
- 不碰 web/。不要对整个文件运行 gofmt；不得出现真实凭证。

## 验收标准
- go build、go vet、带 TEST_DATABASE_URL 的 go test -race -p 1 ./... 通过。
- 每个接口都要有正常、空列表、未登录、项目不存在的测试。
- foragers 的花费测试要覆盖多个 Agent，并断言只发出一次聚合查询（或用等价方式证明没有 N+1）。

## 交付
提交信息用英文约定式前缀（feat:）。向 dev 开 PR，不要合并。
````

### 任务 D：第五轮验收发现的平台问题

````text
# 任务：修复第五轮验收发现的平台问题

## 背景
第五轮真实验收（docs/claude/acceptance-round5.md 第 7.1、9、10 节）发现几个平台问题，前端对接前需要修掉，否则星图上的名额、版本历史和 Issue 关联会显示错。先读验收记录这几节，以及 ADR（docs/claude/adr-hive-queen-forager.md）第 2 节。

## 要做的事
1. 并发上限：internal/store/hive.go 的 createForagerTx 和 internal/mcp/memstore.go，在方法论 limits 非零时直接替换 Hive 配置。改为两者取较小值，即方法论只能更严，maxConcurrentAgents 和 maxAgentsPerChain 都这样处理。
2. 自动回滚计数归属：
   - 迁移 15 给 agents 加一列，记录 Agent 创建时生效的方法论版本 id。
   - recordMethodologyOutcome 只把失败计入这个版本。
   - 只有该版本仍是当前生效版本时才回滚，回滚目标排除该版本。
   - 历史 Agent 这一列为空时不计数。
   - 补测试：先激活新版本，旧版本下启动的 Agent 失败，不应影响新版本，也不应触发回滚。
3. Issue 关联：MCP spawn 工具增加可选的 `issue` 参数并写入 agents.issue_number。Queen 核心提示词里补一句：任务来自某个 Issue 时，派活要带上 issue。
4. 重开 Queen 的引导文案：Queen 从关闭变为开启时，如果项目已有生效的方法论，发送"主 Agent 已重新开启"之类的说明，不再发"新 Hive，请开始首次引导"。
5. 遗留 parked Agent：启动时（或监管者扫描时）检查 parked 且关联 PR 已在 GitHub 合并或关闭的 Agent，按 PR 关闭的流程结束它们，释放名额。第四轮遗留的 forager-ef8e… 就是这种情况。

## 约束
- 第 1、2、5 条是平台底线和生命周期机制，不要写成工作流程。
- 不碰 web/。不要对整个文件运行 gofmt；不得出现真实凭证。

## 验收标准
- go build、go vet、带 TEST_DATABASE_URL 的 go test -race -p 1 ./... 通过；go test ./internal/methodology/ 通过。
- 每条修复都有对应的回归测试；迁移 15 可以在已有 14 的库上执行，也可以在只有 13 的库上与 14 一起执行。

## 交付
每条修复一个提交，提交信息用英文约定式前缀（fix:）。向 dev 开 PR，不要合并。
````

## 集成与验收

1. 按 D → C → B → A 合并到一个集成分支，每次合并后跑 `go build ./... && go vet ./...`。冲突以本文契约为准。
2. 全量测试：
   - 带 TEST_DATABASE_URL 的 `go test -race -p 1 ./...`；
   - `python3 -m unittest discover -s deploy -p 'test_*.py'`；
   - compose config 校验；
   - `cd web && npm ci && npm run build && npx playwright test`，确认旧前端没被后端改动破坏。
3. 开集成 PR，等用户批准后合并。合并后升级本机部署 sj-r3，在测试仓库上做一次真实验证：
   - 用 curl 订阅事件流，同时创建一个小 Issue。确认 message、agent、pr 事件按顺序出现。
   - 用网页端合并接口合并产生的 PR，确认 headSha 校验和 owner 结束都正常。
   - 确认遗留的 parked Agent 已被结束、名额已释放。
   - 结果补进新的验收记录 docs/claude/acceptance-round6.md。
