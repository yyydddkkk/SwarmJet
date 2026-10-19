# 第三轮并行任务提示词：清理、简单部署、简单连接

对应 ADR（`docs/claude/adr-hive-queen-forager.md`）第 15 节"删除与复用"、第 20 节第 6 项，以及第二轮真实验收（`docs/claude/runbook-round2.md`）发现的问题。

**本轮目标**：
1. **清理**：删除旧流水线、Temporal、GitLab、初始化与 Meta 仓库、多用户与权限、多机节点、mihomo 网络代理和旧网页，只保留新架构需要的部分。
2. **简单部署**：一个交互式安装脚本，一条命令装好并启动；默认只有 3 个服务。
3. **简单连接**：网页上一键创建 GitHub App；可选内置隧道并自动同步 webhook 地址；网页和命令行都能一键生成本地 Agent 的 MCP 连接配置；CLI 登录终端登录一次，Cell 自动使用。
4. **修复第二轮验收发现的三个问题**。

## 已定决策（2026-10-19）

| 事项 | 决定 |
|---|---|
| 多用户、团队、项目角色、注册与重置密码（SMTP） | **删除**，只保留一个管理员账号登录 |
| 多机执行节点（Node、`compose.node*.yaml`） | **删除**，以后按 Cell 的需求重写 |
| mihomo 网络代理与网络配置页面 | **删除**，Cell 直接使用宿主机网络 |
| CLI 登录终端（runtimeadmin） | **保留**，挪进 serve，登录结果自动给 Cell 用 |
| 安装脚本的镜像来源 | 在仓库里运行时本地构建；单独下载脚本运行时拉 GHCR 镜像（需要打 v* 标签才会发布） |
| GitHub 接入 | 网页一键创建 App（GitHub App Manifest 流程），凭证加密存进数据库 |
| 本机部署的 webhook | 可选内置 cloudflared 隧道，平台自动读取隧道地址并更新 App 的 webhook URL |

| 任务 | 内容 | 主要改动区域 | 依赖 |
|---|---|---|---|
| A | 后端清理 | 删除大量 `internal/*`、`internal/api`、`internal/service`、`internal/store` 的旧代码、`cmd/swarmjet/main.go`、CI | 公共契约 |
| B | 网页清理与新的控制台外壳 | `web/` | 公共契约（接口先用模拟） |
| C | 第二轮验收问题修复 | `internal/router`、`internal/forager`、`internal/mcp`、`internal/cell/docker/runtime.go` | 无 |
| D | GitHub App 一键创建与隧道 | 新包 `internal/onboarding`、`internal/githubapp`、`internal/store` 新文件、`internal/api` 新文件、`cmd/swarmjet` 的 Cell 接线 | 公共契约 |
| E | 连接与 CLI 登录：Token 接口、`swarmjet connect`、CLI 登录终端挪进 serve | `internal/runtimeadmin`、`internal/api` 新文件、`cmd/swarmjet` | 公共契约 |
| F | 部署与安装脚本、文档 | `deploy/`、`README.md`、`docs/`（`docs/claude` 除外）、`scripts/` | 公共契约 |

**合并顺序**：A → C → D → E → B → F。A 删得最多，先合；D、E 会和 A 在 `cmd/swarmjet/main.go`、`internal/api/api.go` 上冲突，合并时由审查方解决。B、F 依赖 D、E 的接口和环境变量，按契约开发，最后合。

---

## 公共约束（每个任务都适用，已写进各提示词）

- 开工前 `git fetch origin`，从 `origin/dev` 新建分支；先读 ADR 和本文件的"公共契约"一节。
- 不要对整个文件运行 gofmt 或 go fmt（仓库本身不是 gofmt 干净的），只格式化你新建或大改的文件。
- 代码、测试、文档里不得出现真实凭证。
- 数据库迁移只新增，不修改、不删除已有的 `schema_upgrade_*.sql`；旧表保留为历史，不删表。
- 提交信息用英文约定式前缀；向 dev 开 PR，不要合并。
- 用临时 `postgres:18.1-alpine3.23` 容器设置 `TEST_DATABASE_URL` 跑 `go test -race ./...`；前端用 `npm run build` 和 `npx playwright test`。

---

## 公共契约

### 1. 保留与删除

**保留**（本轮之后仓库里应当只剩这些 Go 包，外加 D、E 新增的包）：
`internal/domain`（只留 Hive/Agent/Message/Token 及仍被使用的类型）、`internal/store`（Hive 数据层、管理员会话所需的部分、全部迁移文件）、`internal/jobs`、`internal/mcp`、`internal/router`、`internal/forager`、`internal/cell`、`internal/cell/acp`、`internal/cell/docker`、`internal/github`（PR 客户端）、`internal/githubapp`、`internal/forge`（如果 `internal/github` 仍需要它的类型）、`internal/secretbox`、`internal/observability`、`internal/runtimeadmin`、`internal/agentcli`、`internal/api`、`internal/service`（只留 GitHub webhook 接收与 Router 分发）。

**删除**：`internal/pipeline`、`internal/intake`、`internal/labels`、`internal/gitlab`、`internal/initflow`、`internal/managedmeta`、`internal/repochange`、`internal/integration`、`internal/execution`、`internal/nodeenvironment`、`internal/maintenance`、`internal/workspace`、`internal/agent`（含 `fakeagent`；`internal/cell/docker` 用到的 `ContainerPolicyArgs`、`ContainerUser` 移到 `internal/cell/docker/policy.go`）、`internal/credentials`、`internal/mihomo`、`internal/networkconfig`、`internal/buildproxy`；`go.temporal.io/*` 依赖；运行模式 `worker`、`node`、`mihomo`、`legacy-inventory`；GitLab webhook。

### 2. 运行模式与命令

`swarmjet serve | migrate | version | token create --name <n> --scope default|admin | connect --client claude|codex|json [--name <n>] [--url <mcp-url>]`

- `connect`：生成一个 default 作用域的 Token，打印对应客户端的配置（Claude Code 打印一条 `claude mcp add --transport http ...` 命令；Codex 打印 `~/.codex/config.toml` 片段；json 打印 `.mcp.json` 片段）。`--url` 默认 `http://127.0.0.1:${HTTP_PORT:-8180}/mcp`。由 E 实现。

### 3. 环境变量（第三轮之后的完整列表）

| 变量 | 必填 | 说明 |
|---|---|---|
| `DATABASE_URL`、`POSTGRES_PASSWORD` | 是 | 数据库 |
| `ADMIN_USERNAME`、`ADMIN_PASSWORD` | 是 | 唯一的管理员账号；密码至少 12 位 |
| `SWARMJET_MASTER_KEY` | 是 | 32 字节十六进制，加密数据库里的 GitHub App 凭证；丢失后需要重新创建 App |
| `DATA_DIR` | 是 | 宿主机绝对路径，容器内同路径 |
| `PUBLIC_ORIGIN`、`LISTEN_HOST`、`HTTP_PORT`、`SECURE_COOKIES` | 否 | 控制台地址与监听 |
| `IMAGE`、`AGENT_IMAGE` | 否 | 平台镜像与 Agent 镜像 |
| `SWARMJET_CELLS`、`SWARMJET_FORAGER_MODEL`、`SWARMJET_CELL_MCP_URL`、`SWARMJET_CELL_NETWORK` | 否 | 同第二轮 |
| `OPENCODE_AUTH_CONTENT` | 否 | 仍然支持；CLI 登录终端登录过时优先用登录结果 |
| `GITHUB_APP_ID`、`GITHUB_APP_PRIVATE_KEY_FILE`、`GITHUB_WEBHOOK_SECRET` | 否 | 设置了就优先于数据库里的 App（兼容第二轮的手动方式） |
| `SWARMJET_TUNNEL_URL` | 否 | 隧道的指标地址，例如 `http://tunnel:2000`；设置后平台自动读取隧道公网地址并同步 webhook |

删除的变量：所有 `TEMPORAL_*`、`GITLAB_*`、`SMTP_*`、`NODE_*`、`EXECUTION_MODE`、`MIHOMO_*`、`RUNTIME_ADMIN_*`、`TASK_CONCURRENCY`、`INTEGRATION_ENV_ALLOWLIST`、`AGENT_ENV_ALLOWLIST`、`INIT_VERIFY_IMAGE`、`FAILED_KEEP_DAYS`、`REPORT_KEEP_DAYS`、`PLATFORM_IMAGE`。

### 4. Compose 服务

- 默认：`postgres`、`migrate`、`api`。
- `compose.cells.yaml`（叠加）：给 `api` 挂 Docker socket、可写的 `DATA_DIR`、`SWARMJET_CELLS=1`、网络别名 `swarmjet`（同第二轮）。
- profile `tunnel`：服务 `tunnel`，cloudflared 临时隧道，命令为 `tunnel --no-autoupdate --metrics 0.0.0.0:2000 --url http://api:8080`，镜像固定版本；启用时给 `api` 设置 `SWARMJET_TUNNEL_URL=http://tunnel:2000`。
- profile `observability`：保留 prometheus、alertmanager、postgres-exporter。

### 5. HTTP 接口（D、E 实现，B 调用；全部要求管理员会话登录，回调除外）

```
GET  /api/session                       已有，返回当前登录用户（保持现有形状）
POST /api/login, POST /api/logout       已有

GET  /api/setup/status
  → {
      "github": {"configured": bool, "source": "env"|"database"|"", "appId": int, "appSlug": str,
                 "htmlUrl": str, "installUrl": str, "installations": int},
      "tunnel": {"enabled": bool, "url": str, "webhookSynced": bool, "lastError": str},
      "cells":  {"enabled": bool, "image": str, "model": str, "dockerAvailable": bool},
      "cliLogin": {"available": bool, "loggedIn": bool, "cli": "opencode"}
    }

POST /api/setup/github/manifest   body {"owner": "" | "<org>"}
  → {"action": "https://github.com/settings/apps/new?state=..." 或组织版地址,
     "manifest": {...}}            前端用隐藏表单把 manifest POST 到 action
GET  /api/setup/github/callback?code=&state=
  → 交换凭证、加密保存，302 跳回 /#/setup?github=connected（state 校验失败跳回并带 error）
GET  /api/setup/github/repositories
  → {"repositories": [{"fullName": str, "private": bool, "defaultBranch": str}]}
     App 已安装的仓库，供新建 Hive 时选择

GET    /api/tokens                → {"tokens": [{"id","name","scope","createdAt"}]}（不含明文，不列 agent 作用域）
POST   /api/tokens  body {"name", "scope": "default"|"admin"}
  → {"token": {...}, "plaintext": str,
     "connect": {"mcpUrl": str, "claudeCode": str, "codex": str, "json": {...}}}
DELETE /api/tokens/{id}          吊销

/api/runtime/*                    CLI 登录终端，沿用现有 runtimeadmin 的路由与形状（status、terminal 等）
```

---

## 任务 A：后端清理

````text
# 任务：删除旧架构的后端代码，只保留新架构

## 背景
SwarmJet 是一个自托管的云端 Agent 集群（Go 1.26+、PostgreSQL、River），已经按 docs/claude/adr-hive-queen-forager.md 完成了新架构（Hive / Queen / Forager、MCP、Router、Cell）。旧的流水线、Temporal、GitLab、初始化、多用户、多机节点、mihomo 代理还都留在仓库里。先读 ADR 第 15、17 节，再读 docs/claude/round3-prompts.md 的"已定决策"和"公共契约"（尤其第 1～3 节）。

## 要做的事
1. 按契约第 1 节删除包，并清理所有引用；把 internal/cell/docker 用到的 ContainerPolicyArgs、ContainerUser 移到新文件 internal/cell/docker/policy.go（runtime.go 只改 import 和调用处，不改别的逻辑——另一个任务正在改 runtime.go 的容器用户）。
2. 去掉 go.temporal.io 依赖，go mod tidy。
3. cmd/swarmjet/main.go：只保留契约第 2 节的模式（connect 由另一个任务实现，这里不要加）；删除 worker、node、mihomo、legacy-inventory 模式及其全部接线；serve 里去掉 Temporal 客户端、旧扫描、旧标签同步、旧流水线、节点池、初始化、仓库变更、mihomo、runtimeadmin 的旧接线（runtimeadmin 包本身保留，由另一个任务接进 serve；这里删掉它对 networkconfig 的依赖，让它能独立编译）。保留：River 队列（webhook 与 message 两种 Job；command Job 若只服务旧流水线就删掉）、GitHub webhook → Router、MCP、Cell 栈、静态网页。
4. internal/service：只保留 GitHub webhook 的接收（签名校验、入队）和 Router 分发；Hive 之外的仓库事件直接忽略并记日志（旧扫描逻辑删除）。
5. internal/api：删除项目、任务、Issue/MR 工作台、节点、网络、mihomo、GitLab 身份、成员/团队/注册/重置密码、初始化、仓库变更、旧的 agents/runtime 检测等接口及其测试；保留登录、登出、/api/session、/api/hives* 和 /api/foragers*、静态文件服务，以及 /api/runtime/* 的路由入口（实现由另一个任务接上，这里可以暂时返回 503 "CLI login is not configured"）。登录改为只认 ADMIN_USERNAME/ADMIN_PASSWORD 这一个管理员；Hive 接口的鉴权改为"已登录即可"（只有一个管理员）。
6. internal/store：删除只被已删除代码使用的方法和文件；保留全部 schema_upgrade_*.sql 和 Migrate 的升级链（旧表留作历史）；保留会话存储。store 包里旧的 ErrNotFound/ErrConflict 如果已无人使用就删掉。
7. internal/domain：删除只被已删除代码使用的类型；internal/domain/hive.go 一个字都不要改。
8. 其他：删除 cmd/dev（如果只服务旧架构）、scripts 里只服务旧架构的脚本、.github/workflows 里引用已删除代码或 Temporal 的步骤（保证 CI 仍能跑 Go 测试和前端测试）；observability 指标里只保留仍有数据来源的。

## 约束
- 不要改 internal/router、internal/forager、internal/mcp、internal/cell/acp 的行为，也不要改 internal/cell/docker/runtime.go 除 import/调用之外的内容（另一个任务在改）。
- 不碰 web/、deploy/、docs/（另有任务负责）。deploy/compose.yaml 里的 Temporal 和 worker 服务也由另一个任务删，你这边只保证 serve 不再需要 Temporal 就能启动。
- 不要删除或修改任何 schema_upgrade_*.sql。

## 验收标准
- go build ./... 和 go vet ./... 通过；go list -m all 里没有 go.temporal.io。
- 起临时 Postgres，go test -race ./... 全部通过；从第 6 版数据库启动 serve 正常（旧表还在，不再被读写）。
- 用一个只设置了契约第 3 节必填变量的环境，`swarmjet serve` 能启动，/healthz 正常，/mcp 和 /api/hives 正常工作。
- PR 描述列出删除的包、接口、运行模式、环境变量，以及净删除的行数。

## 交付
提交信息用英文约定式前缀（refactor: / chore:），可以分几个提交。向 dev 开 PR，不要合并。
````

---

## 任务 B：网页清理与新的控制台外壳

````text
# 任务：删除旧网页，Hive 控制台成为默认首页，并加上"接入"页面

## 背景
SwarmJet 的网页（web/，React + Vite + TypeScript）里，新的 Hive 控制台在 web/src/hive（路由 #/hives），旧的任务、Issue/MR 工作台、项目向导、节点、网络、mihomo、成员团队等页面还在。先读 docs/claude/adr-hive-queen-forager.md 第 18 节，再读 docs/claude/round3-prompts.md 的"已定决策"和"公共契约"第 5 节。像素图标体系的规范在 docs/claude/design/pixel-icons.json，生成文件 web/src/hive/pixel-icons.ts 不要手改，需要新图标时改规范再运行 cd web && node scripts/generate-pixel-icons.mjs。

## 要做的事
1. 删除旧页面、旧样式和旧测试：任务、任务详情、Issue/MR 工作台、工作台原型、项目列表/向导/编辑/接入、仪表盘、节点、网络、mihomo、成员与团队、个人 GitLab 身份、系统设置里的旧项目，以及只被它们使用的组件、类型和 CSS。
2. 应用外壳：登录页保留（只有管理员一个账号）；登录后默认进入 Hive 控制台；顶部导航只剩"Hive"、"接入"、"CLI 登录"三项。#/ 和未知路由跳到 #/hives。
3. 新的"接入"页（#/setup），按契约第 5 节的接口实现，分三块：
   - GitHub：未配置时显示"一键创建 GitHub App"（可选填组织名）。点击后调用 POST /api/setup/github/manifest，用隐藏表单把 manifest POST 到返回的 action 地址。回调回来（#/setup?github=connected 或带 error）时给出结果提示。已配置时显示 App 名称、链接、安装链接和已安装数量；来源是 env 时注明"由环境变量配置"。
   - Webhook 隧道：显示是否启用、公网地址、webhook 是否已同步、最近的错误；未启用时给出一句启用方法（安装脚本里选择启用隧道）。
   - 连接本地 Agent：Token 列表（名称、作用域、创建时间、吊销按钮）；"新建连接"对话框选择名称和作用域，创建后一次性展示明文，以及 Claude Code、Codex、JSON 三种配置的复制按钮，并提示"关闭后无法再次查看"。
4. "CLI 登录"页（#/cli-login）：复用现有的 RuntimeSettings / TerminalPanel 里与 CLI 登录终端相关的部分，去掉节点、网络、镜像等旧选项；显示 GET /api/setup/status 的 cliLogin 状态。Cell 未启用时显示说明而不是空白终端。
5. 新建 Hive 的表单：仓库字段改为从 GET /api/setup/github/repositories 里选择（仍允许手动输入 owner/name）；GitHub 未配置时提示先去"接入"页。
6. Playwright：删除旧测试；为接入页（GitHub 未配置/已配置/回调成功/回调失败、隧道状态、Token 创建与吊销、复制按钮）、CLI 登录页、新建 Hive 选仓库、路由跳转写测试，接口用 page.route 模拟（扩展 web/tests/hive-api-mock.ts）。

## 约束
- 只改 web/（以及 docs/claude/round1-web-console/ 下的截图）。不碰后端。
- 视觉沿用"蜂蜜花园"加像素点缀的规范（ADR 第 18 节）。

## 验收标准
- cd web && npm run build 和 npx playwright test 全部通过；构建产物体积明显下降（在 PR 描述里给出前后对比）。
- web/src 下不再有任何引用已删除接口（/api/projects、/api/tasks、/api/nodes、/api/networks、/api/mihomo、/api/gitlab-*、/api/access、/api/teams 等）的代码。
- 更新截图：接入页（三种状态）、CLI 登录页、新建 Hive 表单。

## 交付
提交信息用英文约定式前缀。向 dev 开 PR，不要合并。PR 描述附截图和删除的页面清单。
````

---

## 任务 C：第二轮验收问题修复

````text
# 任务：修复第二轮真实验收发现的三个问题

## 背景
SwarmJet 第二轮在真实 GitHub 仓库上做了验收（见 docs/claude/runbook-round2.md），闭环跑通了，但发现三个问题。先读 docs/claude/adr-hive-queen-forager.md 第 8、9 节和 internal/router、internal/forager、internal/mcp、internal/cell/docker 的代码。

## 要做的事
1. **Forager 自己推送会唤醒自己**：平台发布（推送分支、开 PR）后，GitHub 送回的 pull_request opened/synchronize（以及平台自己开 PR 引起的其他事件）会被 Router 投给这个 Forager，让它白跑一轮。修法：Router 识别"由平台自己引起的事件"并忽略（记一条 ignored 日志，原因写清楚）。判断依据用结构化字段，不解析正文：事件的 sender 是本平台 GitHub App 的 bot 账号（login 形如 <app-slug>[bot]，或 sender.type == "Bot" 且与配置的 App 匹配），并且动作是 opened、reopened、synchronize、edited 之一。App 的 slug 通过 Router 的一个可选配置项注入（拿不到时退化为"sender.type == Bot 且分支属于该 Forager"）。人类在 PR 上的评论、评审、CI 结果必须照常投递。用 testdata 里的真实格式 payload 写表驱动测试。
2. **汇报默认发给已关闭的 Queen**：Forager 通过 MCP 的 send 不指定 to 时默认发给 queen，Hive 没开 Queen 时消息会一直堆着。修法：agent 作用域的 Forager 调用 send 且未指定 to 时，默认发给"给它派活的人"（它最近一条 kind=task 消息的发送方；没有就发给 hive）；Hive 开了 Queen 时仍默认发给 queen。更新工具描述并补测试。监管者自动发的汇报逻辑不变。
3. **Cell 以 root 运行**：api 容器以 root 运行时，Cell 也用 0:0，宿主机上的工作区文件属于 root。修法：Cell 内的 CLI 固定用一个非 root 的 uid:gid（默认 1000:1000，可通过 Runtime 配置覆盖）；创建工作区目录和数据卷时把属主设为这个 uid:gid（数据卷已有一次性 chown 的逻辑，复用；工作区目录在宿主机侧创建后 chown）；确认 Publisher 在容器里执行的 git 和 bundle 仍然正常。补单元测试；如有条件，用 SWARMJET_CELL_E2E=1 的真实测试验证一遍并在 PR 描述里说明结果。

## 约束
- 只改 internal/router、internal/forager、internal/mcp、internal/cell/docker/runtime.go 及对应测试。另一个任务会新建 internal/cell/docker/policy.go 并改 runtime.go 的 import，你不要新建同名文件，runtime.go 只改容器用户相关的部分。
- 不碰 cmd/swarmjet、internal/api、internal/store、web/、deploy/。

## 验收标准
- go build ./... 和 go vet ./... 通过；go test -race ./internal/router/... ./internal/forager/... ./internal/mcp/... ./internal/cell/... 通过。

## 交付
提交信息用英文约定式前缀（fix:）。向 dev 开 PR，不要合并。PR 描述逐条写根因与修法。
````

---

## 任务 D：GitHub App 一键创建与隧道

````text
# 任务：网页一键创建 GitHub App，可选隧道自动同步 webhook

## 背景
SwarmJet 现在要求用户手动在 GitHub 上建 App、下载私钥、填 .env，本机部署还要自己开隧道、手动改 webhook 地址（见 docs/claude/runbook-round2.md）。本任务把它变成网页上点一下。先读 docs/claude/round3-prompts.md 的"已定决策"和"公共契约"第 3～5 节，以及 internal/githubapp、cmd/swarmjet/cells.go、internal/secretbox。

## 要做的事
1. GitHub App Manifest 流程（新包 internal/onboarding，接口挂在 internal/api 的新文件里）：
   - POST /api/setup/github/manifest：生成 manifest（name 形如 swarmjet-<随机短串>，url 和 redirect_url 基于 PUBLIC_ORIGIN，public=false，权限 contents/pull_requests/issues 读写、checks 只读、metadata 只读，事件 pull_request、pull_request_review、pull_request_review_comment、issue_comment、issues、check_run；hook_attributes 用隧道地址或 PUBLIC_ORIGIN 的 /api/webhooks/github，没有公网地址时让 webhook 先处于未激活状态），生成一次性 state（存库，10 分钟有效），返回 action 地址（个人：https://github.com/settings/apps/new?state=…；组织：https://github.com/organizations/<org>/settings/apps/new?state=…）。
   - GET /api/setup/github/callback：校验 state，调用 POST https://api.github.com/app-manifests/{code}/conversions，拿到 id、slug、pem、webhook_secret、html_url，用 secretbox（SWARMJET_MASTER_KEY）加密后存进新表（新增迁移 schema_upgrade_7.sql），然后 302 跳回 /#/setup?github=connected。失败时跳回并带 error。这个回调由浏览器跳转过来，要求管理员会话 + state 双重校验。
   - GET /api/setup/github/repositories：列出 App 所有安装可访问的仓库。
2. App 来源变成动态的：实现一个 Provider，环境变量（GITHUB_APP_ID 等）优先，否则用数据库里的；创建完成后不重启即可生效。webhook 签名校验、cmd/swarmjet/cells.go 的仓库 Token、Publisher 都改为从 Provider 取。SWARMJET_CELLS=1 但还没有 App 时不再启动失败：serve 正常启动并记 Warn，Forager 需要 GitHub 时报出明确错误（"GitHub App 未配置，请在接入页创建"）。
3. 隧道：设置了 SWARMJET_TUNNEL_URL 时，后台定期（例如每 30 秒）请求 <SWARMJET_TUNNEL_URL>/quicktunnel 读取公网地址（先确认 cloudflared 指标端口确实提供这个接口；如果没有，就改用其他可靠方式并在 PR 里说明）；地址变化或 App 刚创建时，用 App JWT 调用 PATCH /app/hook/config 把 webhook URL 设为 https://<地址>/api/webhooks/github、content_type 为 json、secret 为当前 App 的 webhook secret。记录同步状态与最近错误。
4. GET /api/setup/status：实现契约第 5 节里 github、tunnel、cells 三部分（cliLogin 部分由另一个任务填，你这边先返回 available=false 并留好扩展点）。

## 约束
- 新代码放在 internal/onboarding、internal/api 的新文件、internal/store 的新文件和 schema_upgrade_7.sql；internal/githubapp 只做新增；cmd/swarmjet 只改接线（另一个任务在大规模删除 main.go 的旧代码，冲突由审查方解决，你的改动尽量集中在新文件和少量接线行）。
- 不碰 web/、deploy/。
- 私钥和 webhook secret 只以密文落库，任何日志、错误信息、接口返回里都不出现明文。

## 验收标准
- go build ./... 和 go vet ./... 通过；起临时 Postgres，go test -race ./... 通过。
- 测试覆盖：manifest 内容；state 的生成、过期、一次性；回调用 httptest 模拟 GitHub 的 conversions 接口并验证加密落库；Provider 的环境变量优先级与动态生效；隧道地址读取和 webhook 同步（模拟 cloudflared 与 GitHub API）；status 接口各状态；密钥不出现在日志里。
- PR 描述写清完整的用户流程（点击 → GitHub 确认 → 回调 → 安装 App → 新建 Hive），以及没有公网地址时的行为。

## 交付
提交信息用英文约定式前缀（feat:）。向 dev 开 PR，不要合并。
````

---

## 任务 E：连接与 CLI 登录

````text
# 任务：一键生成本地 Agent 连接配置，CLI 登录终端挪进 serve 并给 Cell 使用

## 背景
SwarmJet 的本地 Agent（Claude Code、Codex）通过 MCP 连接平台，现在需要手动执行 swarmjet token create 再手写配置。CLI 登录终端（internal/runtimeadmin）原来跑在已被删除的 worker 进程里。先读 docs/claude/round3-prompts.md 的"已定决策"和"公共契约"第 2、3、5 节，以及 internal/runtimeadmin、internal/api/runtime.go、cmd/swarmjet/cells.go。

## 要做的事
1. Token 接口（internal/api 新文件）：契约第 5 节的 GET/POST/DELETE /api/tokens。创建时返回一次性明文和三种连接配置；列表不含明文，不列 agent 作用域的 Token。存储层需要的"列出 Token"和"按 ID 吊销"方法，在 internal/store 新文件里新增。
2. 连接配置生成（放在一个可复用的小包里，例如 internal/connect，接口和命令行共用）：
   - Claude Code：`claude mcp add --transport http swarmjet <url> --header "Authorization: Bearer <token>"`
   - Codex：~/.codex/config.toml 的 [mcp_servers.swarmjet] 片段（按 Codex 当前支持的 HTTP MCP 写法，写之前查清楚并在 PR 里注明依据）
   - JSON：.mcp.json 的 mcpServers.swarmjet 片段
3. 命令 `swarmjet connect --client claude|codex|json [--name] [--url]`：生成 default 作用域 Token 并打印配置（明文只打印这一次）。
4. CLI 登录终端挪进 serve：
   - 在 serve 里直接挂载 runtimeadmin 的处理器到 /api/runtime/*（经过管理员会话鉴权），不再需要 RUNTIME_ADMIN_URL/RUNTIME_ADMIN_SECRET 这种进程间转发；只在 Docker 可用时（即叠加了 compose.cells.yaml）启用，否则返回 503 并说明原因。
   - 去掉 runtimeadmin 里与节点、网络、旧 runner 检测相关的功能，只留 CLI 登录终端所需的部分（状态、终端、模型列表）。
   - 登录结果给 Cell 用：Forager 的 CLIEnv 在构造 Cell 环境时，如果 CLI 登录终端已有 OpenCode 的登录状态（auth.json），就把它作为 OPENCODE_AUTH_CONTENT 传给 Cell；否则退回环境变量 OPENCODE_AUTH_CONTENT；都没有时用免费模型。读取在每次 Start 时进行，登录后无需重启。
   - GET /api/setup/status 的 cliLogin 部分由你实现（另一个任务实现了 github、tunnel、cells 部分并留了扩展点；如果你先完成，就自己建好这个接口的骨架，冲突由审查方合并）。

## 约束
- 新代码放在新文件里；cmd/swarmjet 只改接线（另一个任务在大规模删除 main.go 的旧代码，冲突由审查方解决）。
- 不碰 web/、deploy/。
- Token 明文和 auth.json 内容不得出现在任何日志和错误信息里。

## 验收标准
- go build ./... 和 go vet ./... 通过；起临时 Postgres，go test -race ./... 通过。
- 测试覆盖：Token 接口的创建、列表、吊销与鉴权；三种连接配置的格式；connect 命令输出；/api/runtime 在 Docker 不可用时返回 503；CLIEnv 的优先级（登录结果 > 环境变量 > 无），且不泄露内容。

## 交付
提交信息用英文约定式前缀（feat:）。向 dev 开 PR，不要合并。
````

---

## 任务 F：部署与安装脚本、文档

````text
# 任务：一条命令装好 SwarmJet，交互式安装脚本，重写用户文档

## 背景
SwarmJet 清理后默认只有 postgres、migrate、api 三个服务（见 docs/claude/round3-prompts.md 的"公共契约"第 3、4 节）。现在的 deploy/compose.yaml 还包含 Temporal 和 worker，deploy/install.py 也是按旧架构写的，用户要手动准备 .env、私钥、隧道（见 docs/claude/runbook-round2.md 的繁琐步骤）。本任务的目标是：用户执行一条命令，回答几个问题，就能打开控制台；剩下的 GitHub 接入、本地 Agent 连接都在网页上一键完成。

## 要做的事
1. Compose：按契约第 4 节重写 deploy/compose.yaml（删除 Temporal、worker、agent-image 工具服务以及 deploy/temporal 目录）；保留并更新 compose.cells.yaml；新增 tunnel profile；保留 observability profile；compose.build.yaml 只构建 api 用的平台镜像和 Agent 镜像；删除 compose.node*.yaml、compose.local-node.yaml、compose.mihomo.yaml、compose.node-ingress.yaml、Dockerfile.mihomo、node-ingress.conf 等只服务已删除功能的文件；.env.example 按契约第 3 节重写。
2. 安装脚本 deploy/install.py（重写；只依赖 Python 3 标准库和 Docker）：
   - 一行安装：`curl -fsSL https://raw.githubusercontent.com/yyydddkkk/SwarmJet/main/deploy/install.py | python3 -`（脚本能在没有仓库的情况下运行：此时下载对应版本的 compose 文件并拉 GHCR 镜像；在仓库里运行时默认本地构建）。
   - 子命令：install（默认）、upgrade、status、connect、logs、stop、uninstall（默认保留数据，--purge 才删数据卷，且需要二次确认）。
   - install 的交互问题尽量少，都有默认值：安装目录、数据目录、控制台端口、是否启用 Cell（说明需要挂 Docker socket，默认是）、是否启用 webhook 隧道（服务器有公网域名时选否并填 PUBLIC_ORIGIN；默认是）、镜像来源（仓库内默认构建，否则 GHCR 并选版本）。`--yes` 加参数可完全非交互。
   - 自动生成 POSTGRES_PASSWORD、ADMIN_PASSWORD、SWARMJET_MASTER_KEY，写入 .env（权限 0600），已有 .env 时不覆盖已有值；检查 Docker、Compose 版本和端口占用；启动后等待健康检查。
   - 结束时打印：控制台地址、管理员账号、密码保存位置（不在终端里回显密码，除非用户加 --show-password）、下一步（打开控制台的"接入"页，一键创建 GitHub App）。
   - connect 子命令：调用 `docker compose exec api swarmjet connect --client ...`；检测到本机有 claude 命令时询问是否直接执行 claude mcp add。
   - 安装或启动后发现旧的 Temporal/worker 容器或同名旧数据卷时，给出提示，绝不自动删除。
3. 测试：deploy/test_install.py 覆盖参数解析、.env 生成与不覆盖、非交互模式、仓库内/外的镜像来源判断、uninstall 的确认逻辑（全部用模拟，不真的启动 Docker）。如有 CI 步骤校验 compose，同步更新。
4. 文档：
   - 重写 README.md 的快速开始（一条安装命令 → 打开控制台 → 一键创建 GitHub App → 一键连接 Claude Code → 派第一个 Forager），控制在一屏左右；
   - 重写 deploy/README.md（服务、环境变量、Cell 与 Docker socket 的安全说明、隧道、升级、备份 SWARMJET_MASTER_KEY 的重要性、卸载）；
   - 删除或改写 docs/ 下描述旧架构的文档（architecture、execution-nodes、distributed-*、validation、user-guide、getting-started、development、observability），docs/claude/ 下的文件不要动；
   - 新增 docs/claude/runbook-round3.md：用安装脚本从零开始的验收步骤。

## 约束
- 只改 deploy/、README.md、docs/（docs/claude/ 只新增 runbook-round3.md）、scripts/（只删除或改写安装相关脚本）、.github/workflows 中与部署校验相关的步骤。不碰 Go 代码和 web/。
- 环境变量、服务名、命令以契约为准；其他任务正在实现它们，你按契约写，不要等。

## 验收标准
- DATA_DIR=/tmp/sj-data docker compose --env-file deploy/.env.example -f deploy/compose.yaml config 只有 postgres、migrate、api 三个服务；叠加 compose.cells.yaml、启用 tunnel profile 后的输出符合契约。
- cd deploy && python3 -m unittest 全部通过。
- 在一台干净的机器（或干净目录）上执行一行安装命令，非交互参数加 --yes 能完成安装并打开控制台（在 PR 描述里贴出过程；如果 GHCR 镜像还没发布，就用仓库内构建的方式验证，并说明这一点）。

## 交付
提交信息用英文约定式前缀。向 dev 开 PR，不要合并。PR 描述附上安装过程的终端输出（密码打码）。
````
