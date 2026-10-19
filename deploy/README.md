# 部署 Go / Temporal 版 swarmjet

部署包含控制台 API、Temporal Worker、PostgreSQL 18、Temporal Server 1.32.0 和 Temporal UI 2.54.1。业务执行器是独立的 Agent CLI 镜像，默认不会作为常驻服务启动。这里使用持久化生产 Server，不使用 `temporal server start-dev`。

所有部署文件集中在 `deploy/`：`compose.yaml` 是唯一的服务定义，`compose.build.yaml` 只补充源码构建配置，`compose.local-node.yaml` 在本机运行一个执行节点，两个 Dockerfile 分别构建应用与执行器镜像。构建上下文仍是仓库根目录。

## 单机快速部署（源码构建）

服务器先安装 Docker Engine、Docker Compose 插件、Git 和 Python 3。克隆本仓库后，在仓库根目录执行：

```sh
sudo python3 deploy/install.py
# 需要多个执行器时，在首次部署指定（初始化始终需要 opencode）：
# sudo python3 deploy/install.py --agent-clis opencode,codex,claude
```

脚本自动生成权限为 `0600` 的 `deploy/.env`、独立的数据库和管理员密码、运行终端密钥及连接加密主密钥，创建 `/srv/swarmjet/data`，构建应用与 Agent 镜像，并通过 Compose 完成迁移、启动和健康检查。宿主机需要 Docker、Python 3 和 OpenSSL，不需要 Go、Node.js 或 Agent CLI。可用 `--data-dir /绝对路径` 修改首次部署的数据目录。脚本要求本机 Docker socket，支持 `deploy/compose.override.yaml`。

脚本组合 `compose.yaml`、`compose.build.yaml` 和 `compose.local-node.yaml`：任务在同一主机上的执行节点 `local` 中运行。新节点默认使用 `AGENT_IMAGE`（源码安装时为本机构建的 `swarmjet-agent:local`），可在 Node 管理中改为任意镜像地址。

控制台默认只监听服务器本机。在自己的电脑执行 `ssh -N -L 8180:127.0.0.1:8180 用户名@服务器`，然后打开 `http://localhost:8180`。应急管理员账号为 `admin`，密码在服务器 `deploy/.env` 的 `ADMIN_PASSWORD` 中。登录后在“Runner 环境”选择 Node、启用已安装的 Agent 并配置模型认证，再添加 GitLab 项目。

成员自助申请及密码重置需要在 `deploy/.env` 配置 `SMTP_ADDR`（`host:port`）、`SMTP_FROM`、可选的 `SMTP_USERNAME` 和 `SMTP_PASSWORD`。服务只接受支持 STARTTLS 且证书有效的 SMTP 服务器；注册邮件发送 6 位验证码，密码重置邮件的链接使用 `PUBLIC_ORIGIN`。缺少 SMTP 配置时，应急管理员仍可登录，申请和重置接口返回不可用。升级到多人权限版本会清除旧会话；已有项目和连接仍在，平台管理员需为项目指定负责人，并显式启用需要的 GitLab/GitHub 自动接单策略。

初始化验证默认使用 `swarmjet-agent:local`。如果业务项目需要额外工具链或离线依赖，应准备相应镜像并修改 `INIT_VERIFY_IMAGE`；服务启动成功不代表项目验收已通过。

失败后可以重新运行脚本；已有 `.env` 不会被覆盖。`--data-dir` 和 `--agent-clis` 仅用于首次生成配置，之后直接编辑 `.env`。脚本会重新构建并更新服务，不删除数据；升级前仍应备份 PostgreSQL、数据目录和主密钥。使用已发布镜像的更新方式见下文。

## 手动本地构建

从仓库根目录进入 `deploy/` 后执行：

```sh
cd deploy
cp .env.example .env
chmod 600 .env
# 编辑 .env：独立随机 PostgreSQL / 管理员密码、实际凭证、DATA_DIR。
mkdir -p /srv/swarmjet/data
chmod 700 /srv/swarmjet/data
docker compose -f compose.yaml -f compose.build.yaml -f compose.local-node.yaml --profile tools build
docker compose -f compose.yaml -f compose.build.yaml -f compose.local-node.yaml up -d --wait
```

`deploy/Dockerfile` 默认使用 Docker Hub 官方基础镜像，并固定版本。可在仓库根目录通过 build args 覆盖镜像来源：

```sh
docker build -f deploy/Dockerfile -t swarmjet:local \
  --build-arg GO_IMAGE=golang:1.27.1-bookworm \
  --build-arg NODE_IMAGE=node:24.21.0-bookworm-slim \
  --build-arg RUNTIME_IMAGE=ubuntu:24.04 \
  --build-arg DOCKER_IMAGE=docker:29.8.1-cli .
docker build -f deploy/Dockerfile.agent -t swarmjet-agent:local \
  --build-arg AGENT_BASE_IMAGE=node:22.23.1-bookworm-slim .
```

`DATA_DIR` 必须是存在的绝对宿主机路径。API、Worker 和 Docker 创建的执行容器使用同一路径；不能把宿主机 `/srv/swarmjet/data` 改挂成容器 `/data`。Worker 通过宿主 Docker socket 创建同级容器，绝对 `.git` 路径也依赖这个约定。API 的数据挂载只读，且没有 Docker socket。

访问 `http://localhost:8180`，使用 `.env` 的管理员账号登录。项目配置需要真实 GitLab URL、Meta-Repo、仓库与目标分支、模型 `provider/model`、验收 Compose 与必需检查。项目中配置的 agent image 必须与已构建的 `AGENT_IMAGE` 一致。保存凭证的环境变量名，例如 `GITLAB_TOKEN` 与 `GITLAB_MERGE_TOKEN`，不要把 token 粘贴进项目字段。

独立执行镜像保留 Node `22.23.1`、glab `1.115.0`、uv、pnpm、Git、Python 与编译依赖。`AGENT_CLIS` 构建参数决定安装哪些 CLI，默认仅 `opencode`（受管项目静态初始化需要它），可选 `opencode,codex,dst,agy,claude`，不会默认安装全部。版本固定在 `deploy/Dockerfile.agent` 和 `scripts/install-agent-clis.sh`；Antigravity 使用官方固定版本归档及 SHA-512 校验。应用主镜像和 Agent 镜像独立。

## Agent 镜像与跨机分发

控制台不构建镜像。Agent 镜像有两种来源：

- 发布流水线推送的 `ghcr.io/yyydddkkk/swarmjet/agent:<tag>`；
- 用 `deploy/Dockerfile.agent` 自行构建并推送到自己的镜像仓库，例如需要额外工具链或其他 CLI 组合时。

在 Node 管理中为每个节点填写 Agent 镜像地址。节点先尝试 `docker pull`；拉取失败时，若节点上已有同名镜像（例如本机构建的 `swarmjet-agent:local`）则直接使用。随后节点检查镜像架构和其中的 CLI，准备完成才可接单。私有仓库需在节点主机预先执行 `docker login`，Docker daemon 的镜像代理也属于主机配置。

节点支持默认镜像及多个额外预加载镜像，项目显式角色镜像优先于节点默认值。已有任务保留原镜像引用。镜像标签可变；需要固定版本时使用 `@sha256:` 摘要引用。

## 服务器使用发布镜像

发布流水线在 `vX.Y.Z` 标签通过检查后，将主镜像推送为 `ghcr.io/yyydddkkk/swarmjet:<tag>`，执行器镜像为 `ghcr.io/yyydddkkk/swarmjet/agent:<tag>`；两者同时附加 `latest` 标签，首次部署可直接引用。私有仓库需先执行 `docker login`。

发布的 Agent 镜像以 `AGENT_CLIS=opencode,codex,claude,dst,agy` 构建，因此已包含 `opencode`、`codex`、`claude`、`dst`、`agy` 五个 CLI 及其固定版本，节点无需自行安装。`dst` 对应 DSH/dsh-TUI，`agy` 对应 Antigravity。若需其他工具链或裁剪 CLI，请按上文自行构建派生镜像。镜像标签可变；固定版本请使用 `@sha256:` 摘要。

将 `deploy/` 目录复制到服务器，进入该目录，复制其中的 `.env.example` 为 `.env`，填写发布流水线产生的完整 `IMAGE` 和 `AGENT_IMAGE` 地址。创建绝对 `DATA_DIR` 后执行：

```sh
chmod 600 .env
docker compose -f compose.yaml -f compose.local-node.yaml --profile tools pull
docker compose -f compose.yaml -f compose.local-node.yaml up -d --wait
```

升级时修改 `.env` 中的 `IMAGE` 和 `AGENT_IMAGE`，重复上面两条命令。Compose 按依赖顺序先完成 Temporal schema 更新、namespace 初始化和应用 migration，再更新 API、Worker 和本地节点。它不会删除数据卷；已有执行容器和 Temporal 历史不会因为替换 Worker 而删除。节点的 Agent 镜像在 Node 管理中单独更新。远程 Node 的程序镜像在其部署目录中修改 `node.env` 的 `IMAGE` 后执行 `docker compose --env-file node.env -f compose.node.yaml pull` 和 `up -d`。

首次初始化由 PostgreSQL 建立 `swarmjet`、`temporal`、`temporal_visibility` 三个数据库，随后对应工具建立 schema。升级已有数据库前先备份；不要给新版 Temporal 强行降级数据库 schema。更换主版本应遵循官方升级路径，不能仅修改镜像 tag 跳过中间升级要求。`TEMPORAL_NAMESPACE` 默认 `swarmjet`，历史保留 7 天。

## 网络、凭证与访问

默认 API 与 Temporal UI 仅绑定宿主 `127.0.0.1`。生产反向代理应提供 HTTPS，并把 `PUBLIC_ORIGIN` 设成用户访问的精确 origin、`SECURE_COOKIES=true`。不要代理绕过登录的 `/api` 路由。Temporal UI 位于 `http://localhost:8333`，仅用于查看 Workflow 历史和排障，Compose 已禁用其中的写操作；日常任务命令应通过 SwarmJet 控制台执行。该 UI 没有本应用的登录保护，远程运维使用 SSH 转发或带认证的代理。Temporal gRPC 和 PostgreSQL 不发布宿主端口，Temporal 无认证模式只用于这个隔离的 Compose 网络。

只把显式模型变量放入 `AGENT_ENV_ALLOWLIST`。API / Worker 需要 GitLab 读取与状态同步权限；开发 token 可以创建任务分支/MR，但应由 GitLab 保护分支规则禁止合并目标。另一个 merge token 只在合并阶段注入 agent。Worker 的 Docker socket 属于高权限边界；部署主机和管理员必须可信，不能把它挂入 API 或 agent。

Git HTTPS 认证使用 `/usr/local/bin/swarmjet-askpass`，用户名为 `oauth2`、密码从 `GITLAB_TOKEN` 读取。控制台只保存 credential reference。OpenCode provider key 可以从显式环境变量读取，也可以用 `OPENCODE_CONFIG_CONTENT` 设置包含 `{env:VARIABLE_NAME}` 的配置。

## 给本地 Claude Code / Codex 配置 MCP

第一版 MCP 服务挂在同一个 API 的 `/mcp`，只接受 `Authorization: Bearer <token>`，不经过网页登录，也不直接暴露公网（ADR 第 12 节）。先用 SSH 隧道把服务器的控制台端口映射到本机：

```sh
ssh -N -L 8180:127.0.0.1:8180 用户名@服务器
```

在服务器上创建 Token。明文只在创建时打印一次，数据库里只保存哈希：

```sh
cd deploy
docker compose exec -T api swarmjet token create --name claude-code --scope default
# 需要创建/修改 Hive 和凭证时用 --scope admin
```

`default` 作用域只能派活和查看，`admin` 还能配置 Hive。Agent 作用域（`scope=agent`）由平台为每只 Forager 自动签发，不需要手工创建，也不能通过命令行创建。

Claude Code（`~/.claude.json` 或项目里的 `.mcp.json`）：

```json
{
  "mcpServers": {
    "swarmjet": {
      "type": "http",
      "url": "http://127.0.0.1:8180/mcp",
      "headers": { "Authorization": "Bearer <TOKEN>" }
    }
  }
}
```

Codex（`~/.codex/config.toml`，具体字段以所用版本为准）：

```toml
[mcp_servers.swarmjet]
url = "http://127.0.0.1:8180/mcp"
bearer_token_env_var = "SWARMJET_MCP_TOKEN"
```

Token 放在本地环境变量或密码管理器里，不要提交到仓库。本轮还没有撤销 Token 的命令；停用一条 Token 目前需要删除 `mcp_tokens` 中对应的行。`/mcp` 与网页控制台共用进程和数据库，但不需要网页会话，也不需要 `ADMIN_PASSWORD`，只要数据库可连、迁移已执行。

## 额外环境变量与保留期

### 执行器配置终端

设置 `RUNTIME_ADMIN_SECRET` 为独立随机值（至少 32 个字符，例如 `openssl rand -hex 32`），重启 API/Worker 后，控制台的“执行器环境”可以打开配置终端。API 通过私有网络连接 Worker 的 8091 端口；该端口不发布到宿主机，API 本身仍然没有 Docker socket。

配置终端使用部署的 `AGENT_IMAGE`，只挂载 `DATA_DIR/runtime-profiles/default/home`。按需打包后，在“执行器环境”勾选需要启用的 Agent 并保存。新安装/升级后没有隐式启用项，管理员需要明确选择；已有任务继续运行，新的接单使用新列表。

```sh
# 在 deploy/.env 设置 AGENT_CLIS=opencode,codex,claude 后构建
# 若需要 dst 或 agy，将对应 ID 加入逗号分隔列表。
docker compose --env-file deploy/.env -f deploy/compose.yaml -f deploy/compose.build.yaml --profile tools build agent-image
```

重建完成后，让配置终端使用新的 `AGENT_IMAGE`，并在 Node 管理中为各节点填写新的 Agent 镜像。执行器环境页的勾选只控制平台准入，不会构建镜像，也不验证账号额度。项目默认 Agent 和 Issue 的 `agent:*` 标签都必须位于已启用列表中。

| Agent ID | 终端入口 | 复制到任务的配置 |
|---|---|---|
| opencode | `opencode auth login` / `opencode` | auth、JSON/JSONC 配置、模型状态文件 |
| codex | `codex login --device-auth` / `codex` | `.codex/auth.json`、`config.toml` |
| dst | `dst` | `.dsh/settings.yaml`、`.credentials.yaml`、`.env` |
| agy | `agy` | Antigravity settings、config、ADC 凭据文件 |
| claude | `claude` | `.claude/settings.json`、`.credentials.json`、`.claude.json` |

`dst` 固定使用 DSH `0.1.6-alpha.2` 预发行版，已静态核对其发布包支持 `--json` 和 `--session-id`；`0.1.5-rc.2` 不支持，不能直接降级。

`dst` 是 [dsh-TUI](https://github.com/ccch1mneyyy/dsh-TUI) 的交互入口，任务使用同一 Harness 的 `dsh --profile headless`，共享持久化设置但不加载 TUI 插件，也不复制插件目录、用户补丁、会话数据库或历史。Codex 应使用文件凭据存储；任意主机钥匙串都不会挂载到任务。

Antigravity 的系统钥匙串 OAuth 不能通过复制 Home 在隔离任务间复用。本接入支持 [Gemini API key](https://antigravity.google/docs/cli/install)（设置 `modelProvider: "gemini"` 并通过部署传入 `GEMINI_API_KEY`）或 [ADC](https://antigravity.google/docs/enterprise)（保存 `.config/gcloud/application_default_credentials.json` 并传入 `AGY_ADC_AUTH=true`）。不会将钥匙串登录显示为已验证的任务认证。其他 OAuth 也应采用已安装 CLI 支持的设备码等方式，配置容器不额外暴露 localhost 回调端口。

退出终端会停止配置容器，Home 内的登录和默认模型配置保留。终端里临时 `export` 的变量不会传到新容器，请使用部署环境及 `AGENT_ENV_ALLOWLIST`。新任务首次启动时取得配置快照，开发/审查独立复制，后续配置变化和凭据刷新不会覆盖已有角色；修复失败任务的配置需新建任务取得新快照。GitLab 访问仍通过部署或连接的凭据引用管理。

管理终端不保存录屏、输入内容或 shell 历史，只记录打开/关闭操作。登录配置检测与模型清单不代表模型额度或实际请求权限已经通过验证。

## GitHub.com App 接入

在 GitHub 创建一个安装型 App，授予仓库权限：Contents（读写）、Issues（读写）、Pull requests（读写）、Checks（只读）、Commit statuses（只读）、Administration（只读）和 Metadata（只读）。Administration 只读用于读取[传统分支保护的必需检查](https://docs.github.com/en/rest/branches/branch-protection#get-status-checks-protection)；rulesets 通过[分支规则接口](https://docs.github.com/en/rest/repos/rules#get-rules-for-a-branch)读取。把 App 安装到要由同一 SwarmJet 项目管理的仓库；一个项目只使用一个安装。GitHub 项目目前只支持受管 Meta-Repo，暂不支持远程 Meta-Repo 发布及任务后增删仓库。

将 App 私钥 PEM 放在 `DATA_DIR` 下，例如 `${DATA_DIR}/github-app-key.pem`，权限设为 `0600`。在 `deploy/.env` 设置 `GITHUB_APP_ID`、`GITHUB_APP_PRIVATE_KEY_FILE`（容器内相同的绝对路径）以及可选的 `GITHUB_WEBHOOK_SECRET`，然后重启 API 与 Worker。控制台新建项目时选择 GitHub 安装和已授权仓库；平台只保存安装 ID，不保存安装令牌。安装令牌按项目仓库范围签发并在需要时更新；Agent Runner 不接收 GitHub 写入凭证。

若有可从 GitHub 访问的 HTTPS 入口，将 App webhook 指向 `/api/webhooks/github`，配置同一个密钥，订阅 Issues、Issue comment、Pull request、Check run、Status 和 Merge group 事件。Webhook 仅唤醒扫描，120 秒轮询会补漏；没有公网入口也可接单。开启 CI 要求时，目标分支必须配置必需检查。自动合并的 `merge`、`squash`、`rebase` 方式由项目选择；启用 merge queue 的仓库使用队列策略决定最终提交方式，平台持续等待 PR 实际合并。

## GitLab 连接与初始化

保存用户在 Web 输入的 GitLab Token 需要 `SWARMJET_MASTER_KEY`。生成一次并长期保持不变：

```sh
openssl rand -hex 32
```

把结果填入 `deploy/.env` 的 `SWARMJET_MASTER_KEY`。API 用它加密新 Token，Worker 用同一把密钥解密以执行克隆；两边必须一致。密钥只存在于部署配置和备份中，不写入数据库。丢失或更换密钥后旧密文无法解密，必须在控制台重新录入 Token。备份 PostgreSQL 的同时单独备份这把密钥，不要把密钥和备份放在同一份文件中。留空时浏览器连接的保存接口返回“连接存储未启用”，使用 `GITLAB_TOKEN` / `GITLAB_MERGE_TOKEN` 的已有项目不受影响。

初始化验证命令必须在隔离容器中执行：构建包含项目所需工具链的派生 agent 镜像，并在 `.env` 中设置 `INIT_VERIFY_IMAGE`。未配置时验证被直接阻断（项目停在“未完成验证”状态），绝不会以 Worker 进程身份运行模型生成的命令。验证容器无网络、只读根文件系统、丢弃全部 capability，只挂载独立的可写副本。验证失败、缺少命令或缺少联调配置时项目保持可重试的 blocked，不能启用或接单。

受管 Meta-Repo 位于 `DATA_DIR/managed-meta/<projectID>/work`，初始化分析副本位于 `DATA_DIR/initializations/<projectID>`；两者都必须纳入 `DATA_DIR` 的备份，且不会被单任务保留期清理删除。删除项目前应确认不再需要这些仓库。

`.env` 用于 Compose 插值，不会自动把所有变量透传进容器。新增模型 key 或验收环境变量时，既要填写对应 allowlist，也要在同目录 `compose.override.yaml` 中显式传值，例如：

```yaml
services:
  worker:
    environment:
      CUSTOM_MODEL_KEY: ${CUSTOM_MODEL_KEY:?set in .env}
      INTEGRATION_TEST_TOKEN: ${INTEGRATION_TEST_TOKEN:?set in .env}
```

随后在 `.env` 的 `AGENT_ENV_ALLOWLIST` 中追加 `CUSTOM_MODEL_KEY`，将 `INTEGRATION_ENV_ALLOWLIST` 设为 `INTEGRATION_TEST_TOKEN`。GitLab 项目使用额外 credential reference 时，需要在 API 和 Worker 两个服务都显式传入该变量；不要将数据库或管理员密码加入 agent / integration allowlist。

Worker 执行保留期清理：`FAILED_KEEP_DAYS=7` 用于已取消任务的现场，`REPORT_KEEP_DAYS=30` 用于已完成任务。运行中、暂停中和 blocked 任务不会因这些配置自动清除，任务记录与审计信息仍在 PostgreSQL。GitLab 19.1+ 新 webhook 推荐设置 `GITLAB_WEBHOOK_SIGNING_TOKEN`（`whsec_...`），并在 GitLab 使用相同的 signing token；接收端验证 HMAC 与 10 分钟时间窗。旧 secret token 使用 `GITLAB_WEBHOOK_SECRET` 和 `X-Gitlab-Token`，两者可在迁移期并存。开启 Issue 与 Note 事件。认证通过后，API 在同一个 PostgreSQL 事务里按平台 delivery ID 写入收据并插入 River Job，提交成功后返回 202。缺少稳定 delivery ID 返回 400；同一 ID 内容不同返回 409；数据库不可用或 Job 插入失败返回 503，GitLab 侧应保留重试。队列不再按条数或字节拒绝发布，积压留在 PostgreSQL。消费者按仓库合并扫描，周期 120 秒扫描负责补漏。后续轮次只接受 Issue 中以 `/swarmjet run` 开头的新公开评论。

## 任务队列

Webhook 与命令 Job 存在 PostgreSQL 的 `river_job` 表中，由 `swarmjet migrate` 创建。应用启动时会为尚未标记 delivered 的 commands 补插 Job。同一命令 ID 不会插入第二个有效 Job。非 start 命令等到对应 start 命令送达 Temporal 之后才发送。命令只有在 Temporal 调用成功后才标记 delivered。Job 执行成功、数据库唯一约束和 Workflow 内去重共同保证业务操作按稳定 ID 幂等，而不是恰好执行一次。接单容量不足时 webhook Job 延后重试，不删除收据。旧 PostgreSQL inbox 仍需排空，不能清除未处理记录。

查看积压可以查询 `river_job` 中 `state IN ('available','scheduled','retryable','running')` 的行，并对照 `commands` 里 `delivered = false` 的命令。`discarded` 与 `cancelled` 行表示不再重试，需要按 delivery ID 或命令 ID 核对收据和 Temporal Workflow 后再决定是否重新入队。队列与业务数据在同一次 PostgreSQL 备份里。升级前先停止旧版本的写入和消费，确认在途 webhook 已形成收据、在途命令已标记 delivered，再执行迁移并启动新版本。不要让新旧两个消费者同时处理同一批命令。

## 分布式执行节点

控制面（API、Worker、PostgreSQL、Temporal）保持集中；资源消耗大的任务工作区、Agent、Compose 联调和报告可以放在独立执行节点。任务在创建时固定归属到某个在线且有余量的节点，后续阶段不会换节点。

### 内网快速添加 Node

原生开发 API 默认只监听回环地址。要让另一台内网主机注册 Node，在 `deploy/.env.development` 设置 `DEV_API_HOST` 为控制面主机的内网 IPv4，再运行 `./scripts/dev restart`；目标机的 `CONTROL_PLANE_URL` 填 `http://<该内网 IP>:8181`。浏览器仍可通过本机 Vite 页面访问。完成测试后可移除该变量并重启，恢复回环监听。

在控制台 **Node 管理 → 添加节点**填写唯一 ID、程序镜像地址（例如 `ghcr.io/yyydddkkk/swarmjet:<tag>`，默认填入控制面当前使用的 `PLATFORM_IMAGE`）、两端内网地址、数据目录和端口，下载私密部署包。`CONTROL_PLANE_URL` 必须从目标服务器访问到 API；默认安装的 `LISTEN_HOST=127.0.0.1` 仅允许本机访问，跨机器部署前应为 API 配置可达的内网监听地址或 HTTPS 反向代理。`NODE_ADVERTISE_URL` 必须能从控制面访问目标服务器；默认包使用目标机 8092 端口。限制该端口只接受控制面访问。生产网络按前文要求使用 HTTPS。

将下载的 `swarmjet-node-<ID>.tar.gz` 安全复制到目标服务器（不要上传到公开位置），执行：

```sh
mkdir -m 700 swarmjet-node-<ID>
tar -xzf swarmjet-node-<ID>.tar.gz -C swarmjet-node-<ID>
cd swarmjet-node-<ID>
python3 install_node.py --check
sudo python3 install_node.py
```

部署包包含 Node 专属注册凭据。脚本先检查 Linux、程序镜像引用、控制面连通性和端口；目标机缺少 Docker 时，在 Ubuntu/Debian 上从 Docker 官方 apt 仓库安装 Engine 与 Compose 插件。已有 Docker 只补齐缺失的 Compose 插件或启动 daemon，不清理旧容器、镜像或卷。随后脚本拉取程序镜像、创建独立数据目录并启动 Node。私有仓库的镜像需先在目标机执行 `docker login`。重复运行保留数据目录；`node.env` 保存 Node 注册凭据，权限为 `0600`。部署包自身含明文凭据，也必须保存在受限目录。

Node 容器启动后，回到 Node 管理页确认“在线”，填写 Agent 镜像并等待“镜像就绪”，在该 Node 的 Runner 环境中完成登录，最后启用“接收新任务”。安装脚本验证容器启动；跨机器回连、Agent 镜像准备和可调度状态仍以页面显示为准。首次下载部署包会生成注册凭据，已注册的 ID 不可重复下载部署包。

控制面把 `EXECUTION_MODE` 设为 `distributed`；在执行节点页面为每个 Node 创建独立注册凭据，作为该节点的 `NODE_SECRET`。旧部署的共享 `NODE_SECRET` 保留兼容入口。`local` 是默认的单机回退：没有注册节点时任务在本机 Worker 执行。

执行节点在独立主机运行 `swarmjet node`（可使用 `deploy/compose.node.yaml`）。它需要自己的绝对 `NODE_DATA_DIR`、Docker socket、与项目一致的 Agent 镜像，以及该节点需要的模型环境变量或持久化 CLI 登录配置。GitLab 阶段凭据由控制面按需发送。`NODE_ADVERTISE_URL` 必须是控制面可访问的地址，生产使用 HTTPS。节点向控制面的 `POST /api/nodes` 心跳注册，`GET /api/nodes` 返回在线状态与负载。

同一任务的准备、开发、验证、审查、合并、取消和清理都通过节点协议在归属节点完成；报告由 API 按节点代理读取；保留期退役在该节点删除本机任务目录。节点失联时任务进入 blocked 并保留 `nodeId` 与现场，不会自动改派到其他节点；节点恢复后重试会用相同执行 ID 接管已有容器。

本地开发不受影响：不注册节点时任务 `nodeId` 为空，继续使用宿主机进程内的 workspace / agent / integration 管理器，也不要求 `NODE_SECRET`。

## 检查与备份

API 与 Worker 的内部 Prometheus 指标监听、可选 Prometheus profile 和指标解释见[可观测性说明](../docs/observability.md)。

可选监控栈同时采集 API、Worker、Temporal 与 PostgreSQL。Temporal 在私有网络的 `:8000/metrics` 输出原生指标；`postgres-exporter` 使用现有数据库凭据在私有网络提供 `:9187/metrics`。这些端口均不发布到宿主机。相关配置在 `observability/`。启动或更新时在 `deploy` 目录执行：

```sh
docker compose --profile observability up -d temporal postgres-exporter alertmanager prometheus
docker compose --profile observability ps
```

Prometheus 与 Alertmanager UI 分别只绑定宿主回环地址 `127.0.0.1:${PROMETHEUS_PORT:-9090}` 和 `127.0.0.1:${ALERTMANAGER_PORT:-9093}`。前者在 `/targets` 检查各采集目标，`/rules` 和 `/alerts` 检查告警；后者在 `/` 查看收到的告警。初始规则只对采集失败、PostgreSQL exporter 连接失败和应用数据库采样失败发出告警，`evidence` 标签说明直接观测到的信号；两分钟保持时间仅用于抑制短暂启动抖动，不是实测 SLO。默认 Alertmanager receiver `local-only` 仅在 UI 中展示告警，不发送通知；生产使用前需在受保护的 `observability/alertmanager.yml` 中配置实际通知接收端，并验证到达。命令积压与准入等待阈值需结合真实负载与故障演练设定，不能直接套用当前规则。

诊断时先查看 Prometheus 的 `up`、`pg_up` 和 `swarmjet_state_snapshot_success`，再用 `docker compose logs --tail=100 prometheus alertmanager postgres-exporter temporal api worker` 定位。API HTTP 入口及 API/Worker 内部指标监听的 `/healthz` 只表示进程存活；`/readyz` 检查 PostgreSQL 和 Temporal。监控栈配置可用 `docker compose --profile observability config --quiet` 检查；有 Prometheus 镜像时用 `docker compose --profile observability run --rm --no-deps --entrypoint promtool prometheus check config /etc/prometheus/prometheus.yml` 检查采集与规则语法。告警路由可用 `docker compose --profile observability run --rm --no-deps --entrypoint amtool alertmanager check-config /etc/alertmanager/alertmanager.yml` 检查。告警触发逻辑可在 `deploy/` 目录执行 `docker run --rm --entrypoint promtool -v "$PWD/observability:/etc/prometheus:ro" prom/prometheus:v3.15.0 test rules /etc/prometheus/alerts_test.yml` 验证。

以下命令均在 `deploy/` 目录执行。

```sh
docker compose ps
docker compose logs --tail=100 api worker temporal-schema temporal
docker compose exec -T api swarmjet doctor
# PostgreSQL 导出包含任务、配置、session 和 Temporal 历史，输出到受限目录。
docker compose exec -T postgres pg_dumpall -U swarmjet > backup.sql
```

备份 PostgreSQL 与 `DATA_DIR`。PostgreSQL 备份同时包含业务状态和尚未执行的 Job。另行备份 `SWARMJET_MASTER_KEY`。恢复前停止 API 和 Worker，恢复数据库后再启动，避免新写入和旧 Job 交错。`DATA_DIR` 包含任务工作区、受管 Meta-Repo、初始化副本、Git 对象、执行日志和 Agent 会话与配置快照。备份中可能有私有源代码和认证会话，按凭证数据管理。没有主密钥，数据库中保存的连接 Token 无法恢复。恢复时保留 `DATA_DIR` 的绝对路径，并恢复相同 Temporal namespace。旧版本 `state.json` 只能通过 `swarmjet legacy-inventory <file>` 生成人工接管清单；不要直接启用旧 Issue 的 `autonomous` 标签造成重复任务。

在仓库根目录执行 `VALIDATE_DEPLOY=1 scripts/test.sh`：Go race tests、React build、发布镜像与源码构建两种 Compose 配置验证。有 `TEST_DATABASE_URL` 时运行独立临时 schema 的 PostgreSQL 集成测试；`RUN_BROWSER_TESTS=1` 额外运行浏览器测试。测试脚本要求本地安装 Go 和 Node.js。可用 `SWARMJET_TEMPORAL_ADDRESS` 与 `SWARMJET_TEMPORAL_NAMESPACE` 指向独立测试 Temporal，运行持久化/Worker 重启测试；该测试使用专属 task queue，不需要模型 key。原生工具链下还可用 `SWARMJET_DOCKER_SMOKE=1` 和 `SWARMJET_META_BIN` 启用实际 Docker / Meta 检查。

版本依据：[River v0.49.0](https://github.com/riverqueue/river/releases/tag/v0.49.0)、[Temporal Server release](https://github.com/temporalio/temporal/releases/tag/v1.32.0)、[生产 PostgreSQL 示例](https://github.com/temporalio/samples-server/blob/main/compose/docker-compose-postgres.yml)、[UI Server release](https://github.com/temporalio/ui-server/releases/tag/v2.54.1)、[Meta release](https://github.com/gitkb/meta/releases/tag/v0.2.22)。

## 从原目录布局切换

以前在仓库根目录保存配置的环境，将 `.env` 和可选的 `.env.development` 移入 `deploy/`；若目标文件已存在，先核对内容，不要直接覆盖。自定义 `compose.override.yaml` 也移入 `deploy/`，其中相对挂载路径需要按新目录调整。保留原 `COMPOSE_PROJECT_NAME`、`DATA_DIR` 和数据库配置，默认项目名仍为 `swarmjet`，无需删除或重建数据卷。

日常 Compose 命令改为在 `deploy/` 执行。本地源码构建显式使用两个 `-f` 时，如有自定义覆盖文件，还需追加 `-f compose.override.yaml`。原生开发命令仍在仓库根目录执行 `./scripts/dev start`，环境配置统一从 `deploy/` 读取。

节点配置与心跳观测分开保存。暂停节点不终止已有任务，归档前检查未完成任务与初始化。网络配置修改影响新任务；正在使用的历史版本保留。Docker 拉取镜像代理仍属于 daemon 部署配置，页面中的节点代理用于准备代码、配置终端与任务容器。
