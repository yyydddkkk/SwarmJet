# 技术验证：在 Docker 中长期运行并驱动 OpenCode

- 状态：验证完成，结论可用于第二轮 Cell 运行时设计
- 日期：2026-10-16
- 对应：`docs/claude/adr-hive-queen-forager.md` 第 8 节、第 19 节"ACP 与各 CLI 适配器的成熟度"
- 任务：`docs/claude/round1-prompts.md` 任务 A
- 原型：`internal/cell/acp`（实验性，包注释已标注）

## 1. 结论摘要

**推荐用 ACP（`opencode acp`，stdio 上的 JSON-RPC）作为 Cell 驱动 Forager 的主协议；`opencode serve`（自带 HTTP/SSE 服务）作为后备，以及网页端人工接管时的入口。**

理由：

- ACP 只有一条 stdio 通道，正好套进"长期容器 + `docker exec -i`"的 Cell 模型：不占端口、不暴露 HTTP、不需要额外的服务鉴权；
- ACP 是 Zed 发起的跨 CLI 协议，initialize 里声明了 `loadSession`、权限回调、MCP 注入，本次要求验证的四种能力（多轮、重启续聊、中途取消、权限）全部原生支持；
- 以后接 Claude Code、Codex 时可以复用同一个客户端，而不是每个 CLI 写一套 HTTP 客户端；
- `serve` 的 HTTP/SSE 面更宽（会话、消息、模型、事件都能单独访问），但它同时存在 `/session/*` 旧版与 `/api/*` 新版两套 API，稳定性和版本归属不明确，适合"人要看、要插话"的场景，不适合作为平台与 Forager 之间的唯一控制面。

需要注意的硬事实（第 4 节详述）：

1. OpenCode 的 ACP `session/load` 可用，会话存在 `$XDG_DATA_HOME/opencode/opencode.db`（SQLite），挂持久卷 + 固定 cwd 就能跨容器重启续聊；
2. `session/cancel` 是通知，取消后进行中的 `session/prompt` 以 `stopReason: "cancelled"` 返回，进程可继续使用；
3. 工具权限默认是**放行**的。要按策略拦截，必须在 OpenCode 配置里显式设置 `permission`（例如 `{"permission":{"bash":"ask"}}`），此时才会收到 `session/request_permission`；
4. ACP 的 `mcpServers` 支持 stdio / http / sse 三种外部 MCP 服务，但 stdio 形态**必须**带 `args` 和 `env` 两个数组，缺 `env` 会直接报 `-32602 Invalid params`；
5. 模型凭证可以用 `OPENCODE_AUTH_CONTENT` 环境变量整体注入，不落盘、不进镜像；
6. 空闲资源不低：`opencode acp` 空闲约 373 MB RSS、单核 8~10% CPU，建议 parked 时停掉 CLI 进程，唤醒时 `session/load` 恢复（并入 ADR 第 8 节生命周期）。

**没有修改 `deploy/Dockerfile.agent`。** 现有镜像（`AGENT_CLIS=opencode`，OpenCode 固定 1.18.31）已经满足全部验证，无需改动。

## 2. 验证环境

```console
$ docker version --format '{{.Server.Version}}'
29.1.3
$ go version
go version go1.27.1 linux/amd64
```

镜像由现有 `deploy/Dockerfile.agent` 构建（未改动），`--build-arg AGENT_CLIS=opencode`：

```console
$ docker build -f deploy/Dockerfile.agent --build-arg AGENT_CLIS=opencode -t swarmjet-agent-spike:latest .
$ docker run --rm --entrypoint /bin/sh swarmjet-agent-spike:latest -c \
    'node --version; opencode --version; python3 --version; git --version'
v22.23.1
1.18.31
Python 3.11.2
git version 2.39.5
```

模型使用 OpenCode 自带的免费模型 `opencode/big-pickle`，无需任何凭证即可验证协议。验证过程中用 `opencode-go/deepseek-v4-flash` 试过自有订阅模型，返回：

```json
{"name":"APIError","data":{"message":"Upstream request failed: An active OpenCode Go subscription is required to use Go models.","statusCode":403}}
```

因此本文所有"真实模型"结论都基于免费模型；凭证注入本身单独验证（第 4.6 节）。

## 3. ACP：协议握手与基本形态

启动方式（容器内）：

```console
$ docker exec -i \
    -e HOME=/root \
    -e XDG_DATA_HOME=/data/opencode-data \
    -e XDG_CONFIG_HOME=/data/opencode-config \
    -e XDG_STATE_HOME=/data/opencode-state \
    -e OPENCODE_DISABLE_AUTOUPDATE=true \
    -e 'OPENCODE_CONFIG_CONTENT={"model":"opencode/big-pickle"}' \
    <cell> opencode acp
```

stdio 上是**按行分隔**的 JSON-RPC 2.0。`initialize` 的实际往返：

```json
>>> {"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}
<<< {"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,
     "agentCapabilities":{"loadSession":true,
       "mcpCapabilities":{"http":true,"sse":true},
       "promptCapabilities":{"embeddedContext":true,"image":true},
       "sessionCapabilities":{"close":{},"fork":{},"list":{},"resume":{}}},
     "authMethods":[{"description":"Run `opencode auth login` in the terminal","name":"Login with opencode","id":"opencode-login"}],
     "agentInfo":{"name":"OpenCode","version":"1.18.31"}}}
```

要点：

- `protocolVersion` 为 `1`；
- `loadSession: true` 是"重启后续聊"的前提；
- `clientCapabilities` 可以为空对象——Cell 内的 CLI 直接读写容器文件系统，平台不需要实现 `fs/*`、`terminal/*` 回调，客户端可以做到最小；
- 我们只需要实现一个服务端→客户端的请求：`session/request_permission`。

`session/new` 的返回（截断 options）：

```json
<<< {"jsonrpc":"2.0","id":2,"result":{"sessionId":"ses_ebc1329b3ffehwwo6gApyZ0T46",
     "configOptions":[
       {"id":"model","name":"Model","category":"model","type":"select",
        "currentValue":"opencode/big-pickle","options":[{"value":"opencode/big-pickle","name":"OpenCode/Big Pickle"}, "..."]},
       {"id":"mode","name":"Session Mode","category":"mode","type":"select",
        "currentValue":"build","options":[{"value":"build",...},{"value":"plan",...}]}]}}
```

即模型与会话模式是 session 级 `configOptions`。本次用 `OPENCODE_CONFIG_CONTENT` 固定模型即可，没有依赖专门的选择方法（见第 7 节待确认）。

## 4. 逐条回答要验证的问题

### 4.1 同一进程内多轮对话 + 结构化流式输出

**可以。** 一个 `opencode acp` 进程可以创建多个会话，每个会话多轮往返；一轮的输出通过 `session/update` 通知流式推送，结束时 `session/prompt` 返回结果。

实际观察到的 `session/update` 种类：

| `sessionUpdate` | 含义 | 关键字段 |
|---|---|---|
| `agent_message_chunk` | 助手文本增量 | `content:{type:"text",text}` |
| `agent_thought_chunk` | 思考增量 | 同上 |
| `tool_call` | 工具调用开始 | `toolCallId`、`title`、`kind`、`status:"pending"`、`rawInput` |
| `tool_call_update` | 工具调用进展/结果 | `status:"in_progress"/"completed"`、`rawOutput` |
| `usage_update` | token 用量 | `used`、`size`、`cost:{amount,currency}` |
| `available_commands_update` | 可用命令列表（可能很大） | `availableCommands` |
| `user_message_chunk` | 仅 `session/load` 回放历史时出现 | 同上 |

文本增量片段（真实输出）：

```json
{"sessionId":"ses_ebc219fe7ffeZRd3tY90SQd3DY",
 "update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"OK"}}}
```

一轮结束的返回：

```json
{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn",
 "usage":{"inputTokens":3381,"outputTokens":3,"totalTokens":9536,"thoughtTokens":8,"cachedReadTokens":6144},"_meta":{}}}
```

多轮记忆也验证过（同一 session 第二问）：第一轮"我的幸运数字是 7，只回 OK"，第二轮"我的幸运数字是几？只回数字"，回答 `7`。

`stopReason` 观察值为 `end_turn` 与 `cancelled`（4.3）。

### 4.2 容器重启后加载原会话；会话文件位置；持久卷是否足够

**可以。** 容器内 `opencode debug paths`：

```console
$ docker exec -i <cell> opencode debug paths
home       /root
data       /data/opencode-data/opencode         # XDG_DATA_HOME/opencode
bin        /root/.cache/opencode/bin
log        /data/opencode-data/opencode/log
repos      /data/opencode-data/opencode/repos
cache      /root/.cache/opencode
config     /data/opencode-config/opencode       # XDG_CONFIG_HOME/opencode
state      /data/opencode-state/opencode        # XDG_STATE_HOME/opencode
tmp        /tmp/opencode
```

数据目录内容：

```console
$ docker exec -i <cell> ls /data/opencode-data/opencode
auth.json  log  opencode.db  opencode.db-shm  opencode.db-wal  repos  snapshot
```

会话历史在 **SQLite 库 `opencode.db`**（连同 `-wal`/`-shm`），不在 JSON 文件里。所以：

- **持久卷挂在数据目录（`$XDG_DATA_HOME/opencode`）就够**，配置目录与状态目录一并挂上更稳妥；
- 必须使用**相同的 cwd**：会话附着在项目（cwd）上，重启后 `session/load` 要传同样的 `cwd`。

`session/load` 会先把历史作为 `session/update` 回放，然后返回：

```json
>>> {"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"ses_...","cwd":"/workspace","mcpServers":[]}}
<<< {"jsonrpc":"2.0","id":2,"result":{"configOptions":[{"id":"model",...}]}}
```

端到端验证（集成测试第 6 步）：在会话里记住暗号 `BUMBLEBEE` → `docker rm -f` 容器 → 用**同一个卷**起新容器 → 重连、`initialize`、`session/load` → 再问暗号：

```text
replayed after restart: "pongOKBUMBLEBEE# Beekeeping: The Ancientalive"
memory survived restart: "BUMBLEBEE"
```

回放里能看到之前几轮的历史，且模型记忆保留——**休眠后换节点重连的路径成立**（前提是工作区以分支/提交为准，符合 ADR 第 8 节）。

### 4.3 中途取消，取消后进程是否可用

**可以。** ACP 把取消建模为**通知**（没有响应）：

```json
{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"ses_..."}}
```

行为：

- 进行中的 `session/prompt` 随即返回 `{"stopReason":"cancelled"}`；
- 进程保持存活。紧接着在同一会话再发一轮"只回 alive"，收到 `alive`，`stopReason: "end_turn"`。

集成测试断言了 `cancel stopReason=cancelled` 与 `reused after cancel: "alive"`。也就是说 Cell 收到 Queen 的取消指令时，不需要重启容器或 CLI，只要发 `session/cancel`。

### 4.4 工具权限请求的形式；平台能否按策略自动批准/拒绝

先说一个容易踩的点：**OpenCode 默认放行工具**。默认配置下让它跑 `echo`，只有 `tool_call`/`tool_call_update`，没有权限请求。

显式设置：

```json
{"permission":{"bash":"ask","edit":"ask","webfetch":"ask"}}
```

之后执行 bash 前，agent 发来服务端→客户端请求（真实抓包）：

```json
{"jsonrpc":"2.0","id":"...","method":"session/request_permission","params":{
  "sessionId":"ses_ebc20527affe24GdWWv6aXZhdB",
  "toolCall":{"toolCallId":"call_e8b02d7c588245c2bf89040a","title":"echo permission-check",
              "kind":"execute","status":"pending","rawInput":{"command":"echo permission-check"}},
  "options":[{"optionId":"once","kind":"allow_once","name":"Allow once"},
             {"optionId":"always","kind":"allow_always","name":"Always allow"},
             {"optionId":"reject","kind":"reject_once","name":"Reject"}]}}
```

客户端决定后回：

```json
{"jsonrpc":"2.0","id":"...","result":{"outcome":{"outcome":"selected","optionId":"once"}}}
```

或拒绝/放弃：`{"outcome":{"outcome":"cancelled"}}`（`reject_once` 走 `selected` + `optionId:"reject"`）。

结论：**平台完全可以在 Cell 侧按策略自动批准或拒绝**——容器隔离边界内的常规读写/命令按策略放行（选 `allow_once`/`allow_always`），受保护操作转成 Approval 再回填结果。集成测试里用 `allow_once` 自动批准后，那一轮助手文本里出现了 `perm-spike-check`。

### 4.5 能否挂外部 MCP 服务

**可以，而且三种传输都行。**

`initialize` 声明 `mcpCapabilities:{"http":true,"sse":true}`；实测 `session/new` 接受：

| 形态 | 参数 | 实测 |
|---|---|---|
| stdio | `{"name":"swarmjet","command":"python3","args":["/spike/mcp_server.py"],"env":[]}` | ✅ 端到端可用 |
| http | `{"name":"swarmjet","type":"http","url":"http://…/mcp","headers":[]}` | ✅ 接受 |
| sse | `{"name":"swarmjet","type":"sse","url":"http://…/sse","headers":[]}` | ✅ 接受 |

**坑：stdio 形态的 `args` 与 `env` 必须是数组。** 少写 `env` 会得到：

```json
{"jsonrpc":"2.0","id":2,"error":{"code":-32602,"message":"Invalid params",
 "data":{"env":{"_errors":["Invalid input: expected array, received undefined"]},
         "type":{"_errors":["Invalid input: expected \"http\"","Invalid input: expected \"sse\""]},
         "url":{"_errors":["Invalid input: expected string, received undefined"]}}}}
```

补上 `"env":[]` 后成功。注意这个报错会同时列出 http/sse 分支的字段，容易被误导成"不支持 stdio"——其实支持。原型里对 `MCPServer` 做了自定义 `MarshalJSON`，保证 stdio 永远输出 `args:[]`、`env:[]`。

端到端（容器内 stdio MCP）：

```text
session/new with MCP server: ok
mcp text="`MCP_PONG_FROM_SWARMJET`" tools=2
```

工具被命名空间化为 `swarmjet_swarmjet_ping`（`<server-name>_<tool-name>`）。平台后续用 MCP 给 Forager 提供"读 Inbox、汇报、请求 Approval"的工具时，**推荐按 `type:"http"` 挂 SwarmJet 的 Streamable HTTP MCP 服务**（正好对应任务 C）；stdio 也验证可用，但需要把 MCP server 的字节带进容器。

### 4.6 模型凭证如何注入；能否避免写进镜像

**能避免。** 三条路径：

1. **`OPENCODE_AUTH_CONTENT`（推荐）**：把 `auth.json` 的完整内容放进环境变量。实测在全新空 home、没有任何 `auth.json` 文件时：

   ```console
   $ OPENCODE_AUTH_CONTENT="$(cat ~/.local/share/opencode/auth.json)" \
       XDG_DATA_HOME=$H/share XDG_CONFIG_HOME=$H/config opencode auth list
   ●  OpenCode Go  api
   $ ls $H/share/opencode/          # 只有 log/ 与 opencode.db，没有 auth.json
   ```

   即凭证可以只在容器进程的环境里，既不进镜像也不落盘。`auth.json` 的结构是：

   ```json
   {"<provider-id>": {"type": "api", "key": "…"}}
   // 或 {"type":"oauth","access":"…","refresh":"…","expires":…}
   ```

2. **provider 环境变量**：OpenCode 二进制内建大量 provider 的 `*_API_KEY`（`ANTHROPIC_API_KEY`、`OPENAI_API_KEY`、`AZURE_OPENAI_API_KEY`、`OPENCODE_API_KEY` 等），适合单一 provider 的简单场景。

3. **配置快照**：沿用现有 `internal/agent` 的 profile 白名单（`internal/agentcli` 已列出 `.local/share/opencode/auth.json` 等），以只读方式挂进去；这是现状做法，和 Cell 也兼容。

配置侧：`$XDG_CONFIG_HOME/opencode/opencode.json` 或 `OPENCODE_CONFIG_CONTENT`（实测用它固定 `{"model":"opencode/big-pickle"}` 生效）。

另外建议在 Cell 里设置 **`OPENCODE_DISABLE_AUTOUPDATE=true`**，避免长期进程在容器里自升级导致版本漂移。

约束复述：本次验证没有把任何真实凭证写进代码、测试或报告；报告里的命令用 `$(cat ~/.local/share/opencode/auth.json)` 形式引用。

### 4.7 资源占用（空闲）

在容器内启动后静置 90 秒，`ps` 与 `docker stats` 实测：

| 进程 | RSS | 单核 CPU |
|---|---|---|
| `opencode serve`（空闲，监听端口） | ≈ 312 MB | ≈ 3.0%（2.9–3.4%） |
| `opencode acp`（空闲，stdin 挂起） | ≈ 373 MB | ≈ 9%（8.5–10.2%） |
| 容器总计（两个都跑） | 543 MiB | 1.54%（整机口径），PIDS=44 |

原始输出：

```console
$ docker exec <cell> ps -eo pid,pcpu,rss,etime,comm,args | grep opencode
     14  3.4 312388  01:30 opencode  opencode serve --hostname 127.0.0.1 --port 4701
     31 10.2 372772  01:30 opencode  opencode acp
$ docker stats --no-stream --format 'CPU={{.CPUPerc}} MEM={{.MemUsage}} PIDS={{.PIDs}}' <cell>
CPU=1.54% MEM=543.3MiB / 30.33GiB PIDS=44
```

结论与建议：

- 单只 Forager 的内存量级 ~300–400 MB，Node 基线本身不小；Hive 并发上限 4 时要有约 1.5 GB 的余量意识；
- **ACP 空闲仍有约 9% 单核 CPU**（文件监听/内部循环），不能把"空闲"当作零成本。ADR 第 8 节的 parked 状态应在超过阈值后**停掉 CLI 进程**（甚至停容器），唤醒时重新 `docker exec` + `session/load`。本次已验证重启后 load 可行，这条路径有支撑。

## 5. `opencode serve` 自带服务的对比

启动：

```console
$ opencode serve --hostname 127.0.0.1 --port 4601
Warning: OPENCODE_SERVER_PASSWORD is not set; server is unsecured.
opencode server listening on http://127.0.0.1:4601
```

能力面比 ACP 宽：`GET /doc` 返回 OpenAPI，共 162 个 path，且同时存在两套 API：

| 能力 | 旧版 | 新版 |
|---|---|---|
| 建会话 | `POST /session` | `POST /api/session` |
| 发消息 | `POST /session/{id}/prompt_async` | `POST /api/session/{id}/prompt` |
| 取消 | `POST /session/{id}/abort` | `POST /api/session/{id}/interrupt` |
| 事件流 | `GET /event`(SSE) | `GET /api/session/{id}/event`(SSE) |
| 权限 | `POST /session/{id}/permissions/{permissionID}` | `GET/POST /api/session/{id}/permission`、`POST /api/session/{id}/permission/{requestID}/reply` |

实测（`opencode serve --hostname 127.0.0.1 --port 4611`，模型 `opencode/big-pickle`）：

```console
$ curl -s -X POST http://127.0.0.1:4611/session -H 'Content-Type: application/json' -d '{}'
{"id":"ses_ebc108b72ffe7Iq5jD1eR0ZjZG","projectID":"global","model":{"id":"big-pickle",...}}

$ curl -s -o /dev/null -w 'HTTP %{http_code}\n' -X POST http://127.0.0.1:4611/session/$SID/prompt_async \
    -H 'Content-Type: application/json' \
    -d '{"parts":[{"type":"text","text":"Reply with exactly the word pong"}]}'
HTTP 204

$ curl -s http://127.0.0.1:4611/session/$SID/message    # （截取 text part）
user : 'Reply with exactly the word pong'
assistant : 'pong'

$ curl -s -o /dev/null -w 'HTTP %{http_code}\n' -X POST http://127.0.0.1:4611/session/$SID/abort
HTTP 200
```

`GET /event` 能推送到 `message.part.updated`（文本增量）、`session.idle`、`session.error` 等事件，abort 之后同一会话可继续发消息。

对比结论：

- `serve` 的优势是**面向人的 API 更全**（会话列表、历史、diff、模型切换、SSE 给网页），适合网页控制台和人工接管；
- 劣势是 HTTP + 端口 + 鉴权（未设密码时明确警告 unsecured）、两套重叠 API 的兼容性风险、以及事件流是全局的（客户端要自己按 session 过滤）；
- ACP 把"多轮、续聊、取消、权限、MCP"收在一条 stdio 通道里，控制面更窄、更贴合 Cell。两者可以并存：平台用 ACP 驱动，网页接管时再起/连接 `serve`。

## 6. 最小原型

代码在 `internal/cell/acp`，包注释标注 **EXPERIMENTAL**：

| 文件 | 内容 |
|---|---|
| `protocol.go` | ACP 子集类型（initialize / session/new / load / prompt / cancel / session·update / request_permission / MCP server）与编解码 |
| `jsonrpc.go` | 换行分隔 JSON-RPC 2.0：请求-响应关联、通知分发、服务端请求处理、错误对象 |
| `client.go` | `Client`：`Initialize`、`NewSession`、`LoadSession`、`Prompt`（带 `UpdateSink` 流式回调）、`Cancel`、`Close`，以及权限策略 `WithPermissionHandler` |
| `process.go` | stdio transport：`StartLocal`、`StartDockerExec`，以及 `DockerCell.Connect`（容器 + argv + `-e` 凭证注入） |
| `protocol_test.go` / `client_test.go` | 不依赖外部环境的编解码与假 agent 端到端单测 |
| `spike_docker_test.go` | 真实容器 + 真实模型的集成测试，默认跳过 |

覆盖任务要求的最小能力：启动并连接容器内的 OpenCode、发送一轮并流式读取、取消当前一轮、重启后加载会话；另外把 MCP 与权限也纳入了集成测试。

运行方式：

```console
# 单元测试（无需 Docker/凭证）
$ go test -race ./internal/cell/...

# 真实容器验证（默认 skip；需要 Docker 与模型网络）
$ SWARMJET_OPENCODE_SPIKE=1 \
  SWARMJET_OPENCODE_SPIKE_IMAGE=swarmjet-agent-spike:latest \
  go test ./internal/cell/acp/ -run TestOpenCodeACPDockerSpike -v -timeout 40m
```

可配置项：`SWARMJET_OPENCODE_SPIKE_IMAGE`、`SWARMJET_OPENCODE_SPIKE_DOCKER`、`SWARMJET_OPENCODE_SPIKE_MODEL`（默认免费的 `opencode/big-pickle`）、`SWARMJET_OPENCODE_SPIKE_AUTH_FILE`（指向 `auth.json`，内容以 `OPENCODE_AUTH_CONTENT` 注入，绝不打日志）、`SWARMJET_OPENCODE_SPIKE_KEEP=1`（保留容器便于排查）。

本次实际运行结果：

```text
=== RUN   TestOpenCodeACPDockerSpike
    agent=&{Name:OpenCode Version:1.18.31} loadSession=true protocolVersion=1
    session=ses_ebc142741ffemxvtxw875wYdlF model=opencode/big-pickle
    streamed text: "pong" usage=&{InputTokens:37 OutputTokens:3 TotalTokens:7668 ...}
    multi-turn memory: "BUMBLEBEE"
    cancel stopReason=cancelled
    reused after cancel: "alive"
    mcp text="`MCP_PONG_FROM_SWARMJET`" tools=2
    permission requested: kind=execute title="echo perm-spike-check" options=3
    permission-approved turn text: "...perm-spike-check..."
    replayed after restart: "pongOKBUMBLEBEE# Beekeeping: The Ancientalive"
    memory survived restart: "BUMBLEBEE"
--- PASS: TestOpenCodeACPDockerSpike (55.34s)
```

## 7. 已知坑与待确认事项

**已知坑**

1. **stdio MCP 的 `args`/`env` 必须显式是数组**，否则 `-32602`，且错误信息会误导成"只支持 http/sse"。
2. **权限默认放行**。不显式配置 `permission` 就没有 `session/request_permission`，平台会以为"策略生效了"其实没有。
3. **`opencode acp` 空闲 CPU ≈ 9%**，不能长期空转；parked 时应停 CLI 进程。
4. **会话是 SQLite（含 WAL）**。硬杀（SIGKILL）后 WAL 未合并；本次重启测试是正常 `docker rm -f`，`session/load` 正常，但极端崩溃下的库完整性还需要专门的崩溃测试（见待确认）。
5. **会话绑定 cwd**。重启后必须用同一路径；工作区挂载点若变化，`session/load` 行为未验证。
6. **`session/update` 里有超大通知**（`available_commands_update` 一次带上所有命令/技能描述）。客户端 sink 不能阻塞读取循环，原型里已经用独立 goroutine + 非阻塞分发处理。
7. **`docker exec -i` 断开即结束 CLI 进程**。这没问题——重连后 `session/load` 即可；但要求平台在任何重连路径上都保存 `sessionId`。
8. **`OPENCODE_AUTH_CONTENT` 在环境里**，容器内 `/proc/<pid>/environ` 可见。Cell 是隔离边界，可接受；但日志/`docker inspect` 里不要 dump 环境。
9. **默认模型可能解析到需要订阅的模型**（本次就撞到 `opencode-go/*` 的 403）。建议 Hive 配置里显式写死 model，而不是依赖默认。

**待确认（进入第二轮前要做）**

1. 是否有专门的 ACP 方法在会话中途切模型（`configOptions` 暗示存在 `session/set_config_option` 之类）？本次用配置默认值绕过了。
2. **崩溃一致性**：SIGKILL 后 `session/load` 是否仍能恢复？WAL/snapshot 目录的恢复语义要专门测。
3. 工作区挂载路径变化（休眠换节点后路径不同）时 `session/load` 是否可用，是否需要在容器内保持固定路径（建议固定 `/workspace`）。
4. HTTP/SSE MCP 只验证了 `session/new` 接受，未做端到端连通（stdio 已端到端）。任务 C 落地 Streamable HTTP 后要补一次。
5. `opencode serve` 的 `/api/*` 与 `/session/*` 哪套是稳定接口、鉴权方式（`OPENCODE_SERVER_PASSWORD` 基本认证）与网页接管的复用方式。
6. OpenCode 升级策略：镜像里 pin 1.18.31，ACP 行为是否会随版本变化（本次所有字段与能力以 1.18.31 为准）。

## 8. 对 ADR 第 8 节的回填建议

- 明确 **主协议为 ACP**，`opencode serve` 作为人工接管/网页入口的后备；
- Cell 启动参数增加：数据/配置/状态目录挂持久卷、`OPENCODE_DISABLE_AUTOUPDATE=true`、`OPENCODE_CONFIG_CONTENT` 固定模型与 `permission` 策略、凭证走 `OPENCODE_AUTH_CONTENT`；
- parked/hibernated 时**停 CLI 进程**，唤醒时 `docker exec` 重连 + `session/load`；
- 权限策略在 Cell 侧实现 `session/request_permission` 回调：常规操作按策略自动 `allow_once`，受保护操作转 Approval。
