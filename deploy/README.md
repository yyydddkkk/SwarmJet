# 部署 Go / Temporal 版 swarmjet

部署包含控制台 API、Temporal Worker、PostgreSQL 18、Temporal Server 1.32.0 和 Temporal UI 2.54.1。业务执行器是独立的 Agent CLI 镜像，默认不会作为常驻服务启动。这里使用持久化生产 Server，不使用 `temporal server start-dev`。

所有部署文件集中在 `deploy/`：`compose.yaml` 是唯一的服务定义，`compose.build.yaml` 只补充源码构建配置，两个 Dockerfile 分别构建应用与执行器镜像。构建上下文仍是仓库根目录。

## 单机快速部署（源码构建）

服务器先安装 Docker Engine、Docker Compose 插件、Git 和 Python 3。克隆本仓库后，在仓库根目录执行：

```sh
sudo python3 deploy/install.py
# 需要多个执行器时，在首次部署指定（初始化始终需要 opencode）：
# sudo python3 deploy/install.py --agent-clis opencode,codex,claude
# 局域网有多个地址时，明确指定仓库地址：
# sudo python3 deploy/install.py --registry-host 10.0.0.20
```

脚本自动生成权限为 `0600` 的 `deploy/.env`、独立的数据库和管理员密码、运行终端密钥及连接加密主密钥，还会生成 `deploy/.env.registry`、局域网 TLS 证书和独立随机的仓库推送/拉取密码，创建 `/srv/swarmjet/data`，构建镜像并通过 Compose 完成迁移、启动和健康检查。仓库证书会加入本机 Docker 信任目录，远程节点仍需安装生成的 CA。宿主机需要 Docker、Python 3、OpenSSL 和 `ip` 命令，不需要 Go、Node.js 或 Agent CLI。可用 `--data-dir /绝对路径` 修改首次部署的数据目录。脚本要求本机 Docker socket，支持 `deploy/compose.override.yaml`。

控制台默认只监听服务器本机；镜像仓库网关监听安装时选定的局域网 IP。在自己的电脑执行 `ssh -N -L 8180:127.0.0.1:8180 用户名@服务器`，然后打开 `http://localhost:8180`。账号为 `admin`，密码在服务器 `deploy/.env` 的 `ADMIN_PASSWORD` 中。登录后在“Runner 环境”选择 Node、启用已安装的 Agent 并配置模型认证，再添加 GitLab 项目。仓库所用 IP 应通过 DHCP 保留或静态配置保持稳定。

初始化验证默认使用 `swarmjet-agent:local`。如果业务项目需要额外工具链或离线依赖，应准备相应镜像并修改 `INIT_VERIFY_IMAGE`；服务启动成功不代表项目验收已通过。

失败后可以重新运行脚本；已有 `.env` 不会被覆盖或补写，仓库密码和 CA 也不会轮换。`--data-dir` 和 `--agent-clis` 仅用于首次生成配置，之后直接编辑 `.env`。脚本会重新构建并更新服务，不删除数据；升级前仍应备份 PostgreSQL、数据目录、`.env.registry` 和主密钥。已发布镜像的更新使用下文的 `update.sh`。

## 手动本地构建

从仓库根目录进入 `deploy/` 后执行：

```sh
cd deploy
cp .env.example .env
chmod 600 .env
# 编辑 .env：独立随机 PostgreSQL / 管理员密码、实际凭证、DATA_DIR。
mkdir -p /srv/swarmjet/data
chmod 700 /srv/swarmjet/data
python3 registry_setup.py --root /srv/swarmjet/data/registry-config --host 10.0.0.20 --env-file .env.registry
docker compose --env-file .env --env-file .env.registry -f compose.yaml -f compose.build.yaml -f compose.builder.yaml -f compose.registry.yaml --profile tools build
docker compose --env-file .env --env-file .env.registry -f compose.yaml -f compose.builder.yaml -f compose.registry.yaml up -d --wait
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

## 页面管理 Agent 镜像与跨机分发

侧边栏“镜像管理”管理 Agent 执行镜像。独立 builder 调用 Compose 管理的持久化 BuildKit，发布到自建 Distribution。“系统设置 → 开发者选项”启用后，该页面还显示平台程序镜像：可为当前平台源码填写唯一版本标签，一键构建并发布到 `swarmjet/platform`，查看日志、取消构建、复制摘要及删除未使用版本。业务应用镜像仍使用各自发布流程。

源码安装脚本会组合 `compose.yaml`、`compose.build.yaml`、`compose.builder.yaml` 和 `compose.registry.yaml`。手动部署也应包含 builder 与 registry 配置；“系统设置 → 镜像仓库”默认使用局域网 TLS 仓库，可按需查看自动生成的账号密码或切换自定义仓库。概览页可检查控制面到仓库的 TLS 与认证连接，部署地址仍需在服务器环境文件中修改并重启。其他节点需信任仓库 CA 后才能拉取镜像。构建成功不会自动应用或启用 Agent；选择节点分发后，节点按摘要预拉取并检查 CLI，准备完成才可接单。

本机原生开发的 `scripts/dev start` 也会自动使用同一份 `compose.registry.yaml`，私有配置保存在 `DATA_DIR/registry-config`。

节点支持默认镜像及多个额外预加载镜像，项目显式角色镜像优先于节点默认值。已有任务保留原摘要。未启用独立构建服务的旧单机部署仍保留原本地镜像管理路径；本地 image ID 不能直接用于跨机器分发。


## 服务器使用发布镜像

发布流水线将主镜像推送为 `$CI_REGISTRY_IMAGE:<tag>`，执行器镜像为 `$CI_REGISTRY_IMAGE/agent:<tag>`；main 分支使用 `main` 标签，版本标签使用对应 Git tag。私有仓库需先执行 `docker login`。

将 `deploy/` 目录复制到服务器，进入该目录，复制其中的 `.env.example` 为 `.env`，填写发布流水线产生的完整 `IMAGE` 和 `AGENT_IMAGE` 地址。创建绝对 `DATA_DIR` 后执行：

```sh
chmod 600 .env
./update.sh
# 或仅本次覆盖镜像；要持久保存版本仍应编辑 .env。
./update.sh registry.example/swarmjet:v1 registry.example/swarmjet-agent:v1
```

脚本拉取镜像，先执行 Temporal schema 更新和 namespace 初始化、再执行应用 migration，最后更新 API、Worker、builder、BuildKit、本地节点和内置仓库。它不会执行 `compose down` 或删除数据卷。已有执行容器和 Temporal 历史不会因为替换 Worker 而删除。

首次初始化由 PostgreSQL 建立 `swarmjet`、`temporal`、`temporal_visibility` 三个数据库，随后对应工具建立 schema。升级已有数据库前先备份；不要给新版 Temporal 强行降级数据库 schema。更换主版本应遵循官方升级路径，不能仅修改镜像 tag 跳过中间升级要求。`TEMPORAL_NAMESPACE` 默认 `swarmjet`，历史保留 7 天。

## 网络、凭证与访问

默认 API 与 Temporal UI 仅绑定宿主 `127.0.0.1`。生产反向代理应提供 HTTPS，并把 `PUBLIC_ORIGIN` 设成用户访问的精确 origin、`SECURE_COOKIES=true`。不要代理绕过登录的 `/api` 路由。Temporal UI 位于 `http://localhost:8333`，没有本应用的登录保护；远程运维使用 SSH 转发或带认证的代理。Temporal gRPC 和 PostgreSQL 不发布宿主端口，Temporal 无认证模式只用于这个隔离的 Compose 网络。

只把显式模型变量放入 `AGENT_ENV_ALLOWLIST`。API / Worker 需要 GitLab 读取与状态同步权限；开发 token 可以创建任务分支/MR，但应由 GitLab 保护分支规则禁止合并目标。另一个 merge token 只在合并阶段注入 agent。Worker 的 Docker socket 属于高权限边界；部署主机和管理员必须可信，不能把它挂入 API 或 agent。

Git HTTPS 认证使用 `/usr/local/bin/swarmjet-askpass`，用户名为 `oauth2`、密码从 `GITLAB_TOKEN` 读取。控制台只保存 credential reference。OpenCode provider key 可以从显式环境变量读取，也可以用 `OPENCODE_CONFIG_CONTENT` 设置包含 `{env:VARIABLE_NAME}` 的配置。

## 额外环境变量与保留期

### 执行器配置终端

设置 `RUNTIME_ADMIN_SECRET` 为独立随机值（至少 32 个字符，例如 `openssl rand -hex 32`），重启 API/Worker 后，控制台的“执行器环境”可以打开配置终端。API 通过私有网络连接 Worker 的 8091 端口；该端口不发布到宿主机，API 本身仍然没有 Docker socket。

配置终端初始使用部署的 `AGENT_IMAGE`，在页面应用镜像后使用所选镜像摘要，只挂载 `DATA_DIR/runtime-profiles/default/home`。按需打包后，在“执行器环境”勾选需要启用的 Agent 并保存。新安装/升级后没有隐式启用项，管理员需要明确选择；已有任务继续运行，新的接单使用新列表。

```sh
# 在 deploy/.env 设置 AGENT_CLIS=opencode,codex,claude 后构建
# 若需要 dst 或 agy，将对应 ID 加入逗号分隔列表。
docker compose --env-file deploy/.env -f deploy/compose.yaml -f deploy/compose.build.yaml --profile tools build agent-image
```

重建完成后，按正常发布流程让配置终端和所有执行节点使用新的 `AGENT_IMAGE`。执行器环境页的勾选只控制平台准入，不会构建镜像，也不验证账号额度；镜像管理页的“保存并构建”才会提交构建。项目默认 Agent 和 Issue 的 `agent:*` 标签都必须位于已启用列表中。

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

Worker 执行保留期清理：`FAILED_KEEP_DAYS=7` 用于已取消任务的现场，`REPORT_KEEP_DAYS=30` 用于已完成任务。运行中、暂停中和 blocked 任务不会因这些配置自动清除，任务记录与审计信息仍在 PostgreSQL。GitLab 19.1+ 新 webhook 推荐设置 `GITLAB_WEBHOOK_SIGNING_TOKEN`（`whsec_...`），并在 GitLab 使用相同的 signing token；接收端验证 HMAC 与 10 分钟时间窗。旧 secret token 使用 `GITLAB_WEBHOOK_SECRET` 和 `X-Gitlab-Token`，两者可在迁移期并存。开启 Issue 与 Note 事件。认证通过后，API 将原始事件按平台 delivery ID 持久发布到 RabbitMQ；只有 Broker 确认且消息成功路由后才返回 202。缺少稳定 delivery ID 返回 400；Broker 不可用、确认超时、无法路由或队列达到容量限制时返回 503，GitLab 侧应保留重试。消费者按仓库合并扫描，周期 120 秒扫描负责补漏。后续轮次只接受 Issue 中以 `/swarmjet run` 开头的新公开评论。

## RabbitMQ 消息与运维

Compose 使用 RabbitMQ 4.3 单节点 quorum queue 和持久化数据卷，分别缓冲 webhook、命令和死信。此配置适用于开发与单机部署，覆盖容器/Broker 重启后的持久恢复，不提供节点故障高可用。生产部署应使用三台独立故障域中的 RabbitMQ 节点，为 webhook、command 与 DLQ 建立三副本 quorum queues，并配置 TLS、认证、受限网络访问及节点磁盘告警。单节点 Compose 不构成生产 HA。

新安装由 `install.py` 生成独立 `RABBITMQ_PASSWORD`；旧安装执行 `update.sh` 时会为缺少该键的 `.env` 追加随机密码。已有外部集群可向应用提供 `RABBITMQ_URL`，地址应指向可切换节点的 TLS 入口；当前 Compose 仍会启动本地 Broker，生产三节点集群需在独立部署中管理。检查积压可在单节点运行 `docker compose exec rabbitmq rabbitmqctl list_queues name messages_ready messages_unacknowledged message_bytes consumers`；死信重放前先停止或隔离相关消费者，核对消息 ID 与 PostgreSQL 收据，再按原 ID 重新发布并等待确认。实际验证数据与未通过的验收项见 [RabbitMQ 接入验证](../docs/rabbitmq-validation.md)。

生产集群先按 [RabbitMQ 集群指南](https://www.rabbitmq.com/docs/clustering) 建立三台独立持久磁盘节点，共享集群 cookie，并在应用首次声明队列前确认三个节点在线；默认 quorum 初始成员数为三。把应用 `RABBITMQ_URL` 指向带 TLS 的集群入口，限制 vhost/user 权限。若从单节点队列迁移，先备份并排空或逐条确认迁移中的消息，随后用 `rabbitmq-queues grow <node> all --queue-pattern 'swarmjet.*'` 为新节点增加成员，逐队列核对成员数、leader 和积压，再切换发布与消费。新增集群节点不会自动成为既有 quorum queue 的成员；官方 [成员管理说明](https://www.rabbitmq.com/docs/quorum-queues#managing-members)列出扩容命令和多数派要求。生产切换前演练断开一个节点后的发布确认、消费和恢复。

主队列配置 `reject-publish` 与消息条数、字节上限；队列满时发布被拒绝，API 返回 503。并发在途发布可能使 quorum queue 限额少量超出，限额不是精确的瞬时硬上限。发布端使用 publisher confirms 和 mandatory 路由检查；消费者手动 ACK、有限 prefetch。失败消息按延迟策略重试，超过策略或出现相同 delivery ID 不同内容时转入死信。可靠死信转移依赖 quorum queue 的 at-least-once dead-lettering 配置；死信和源队列都要纳入容量与磁盘监控。

Webhook 的 PostgreSQL `(provider, delivery ID)` 收据用于确认丢失后的去重；不同内容复用同一 ID 应告警并进入死信。命令的数据库发送意图与任务变更保持同一事务；命令只有在 Temporal 调用成功后才标记 delivered。消费者 ACK、RabbitMQ 发布确认都不等于业务操作只执行一次，处理依赖稳定 ID、唯一约束及 Workflow 去重。周期对账和队列消费者共用仓库租约；接单容量不足时消息延迟重试，不应删除或 ACK 掉待准入事件。切换期间旧 PostgreSQL inbox 需排空，不能清除未处理记录。

监控 RabbitMQ ready/unacked 数量、队列字节与磁盘空间、发布确认延迟/拒绝、消费者数量、重试和死信速率，并分别告警；与 `/api/operations` 的 Task 准入 waiting 数及全局 1000、每项目 100 的准入上限分开观察。RabbitMQ 数据目录属于备份对象。配置 RabbitMQ definitions 备份并定期做恢复演练；单独保护 PostgreSQL、`DATA_DIR`、RabbitMQ 持久卷与 `.env` 中的 Broker 凭据。不要在未核实原因、delivery ID 和对应 PostgreSQL 收据前直接重放死信。确认可以重放后，通过受控操作将原消息重新发布到主交换机并等待发布确认，保留原稳定 ID，依靠幂等处理消除重复。隔离环境万条 webhook 排空与单节点重启结果见验证记录；真实平台重试、可接单万条事件和三节点故障切换仍需验收。

## 分布式执行节点

控制面（API、Worker、PostgreSQL、Temporal）保持集中；资源消耗大的任务工作区、Agent、Compose 联调和报告可以放在独立执行节点。任务在创建时固定归属到某个在线且有余量的节点，后续阶段不会换节点。

### 内网快速添加 Node

内置 Distribution 同时存放程序镜像与 Agent 镜像。程序镜像使用独立路径 `swarmjet/platform`，Agent 镜像继续使用 `swarmjet/agent`。首选在 **系统设置 → 开发者选项**启用“管理平台程序镜像”，然后在 **镜像管理 → 平台镜像**填写新的版本标签，点击“构建并发布”。构建服务使用与当前部署打包的同版本源码；本机原生开发使用当前工作区源码。发布不会自动替换运行中的 API、Worker 或 Node，升级仍按部署流程进行。

也可在**运行控制面 API 容器的主机**使用 CLI 发布当前程序镜像（tag 使用本次发布的唯一版本号）：

```sh
sudo python3 deploy/publish_platform.py --tag v1.2.3
```

脚本核对本机源镜像与正在运行的 API、Worker 容器使用相同镜像，再以仓库 builder 凭据推送并输出不可变摘要。它只发布程序镜像，不更新运行中的服务。内置仓库通过安装时固定的内网地址 `:5443` 提供 TLS。

若控制面通过本仓库的 `scripts/dev start` 原生运行，程序镜像不会自动构建。开发主机使用 `python3 deploy/publish_platform.py --dev --build --tag dev-20260924` 一次构建并发布；脚本读取 `.cache/dev/.env.registry`，检查本机 API/Worker 进程，并将当前源码构建为 `swarmjet:local`。正式 Compose 部署继续使用上面的命令。

原生开发 API 默认只监听回环地址。要让另一台内网主机注册 Node，在 `deploy/.env.development` 设置 `DEV_API_HOST` 为控制面主机的内网 IPv4，再运行 `./scripts/dev restart`；目标机的 `CONTROL_PLANE_URL` 填 `http://<该内网 IP>:8180`。浏览器仍可通过本机 Vite 页面访问。完成测试后可移除该变量并重启，恢复回环监听。

在控制台 **Node 管理 → 添加节点**填写唯一 ID、已发布的程序镜像、两端内网地址、数据目录和端口，下载私密部署包。`CONTROL_PLANE_URL` 必须从目标服务器访问到 API；默认安装的 `LISTEN_HOST=127.0.0.1` 仅允许本机访问，跨机器部署前应为 API 配置可达的内网监听地址或 HTTPS 反向代理。`NODE_ADVERTISE_URL` 必须能从控制面访问目标服务器；默认包使用目标机 8092 端口。限制该端口只接受控制面访问。生产网络按前文要求使用 HTTPS。

将下载的 `swarmjet-node-<ID>.tar.gz` 安全复制到目标服务器（不要上传到公开位置），执行：

```sh
mkdir -m 700 swarmjet-node-<ID>
tar -xzf swarmjet-node-<ID>.tar.gz -C swarmjet-node-<ID>
cd swarmjet-node-<ID>
python3 install_node.py --check
sudo python3 install_node.py
```

部署包包含 Node 专属注册凭据、仓库只读凭据和 CA 公钥证书。脚本先检查 Linux、程序镜像引用、控制面和仓库连通性；目标机缺少 Docker 时，在 Ubuntu/Debian 上从 Docker 官方 apt 仓库安装 Engine 与 Compose 插件。已有 Docker 只补齐缺失的 Compose 插件或启动 daemon，不清理旧容器、镜像或卷。随后脚本安装仓库 CA、用临时 Docker 配置拉取指定摘要的程序镜像、创建独立数据目录并启动 Node。重复运行保留数据目录；`node.env` 保存 Node 注册凭据，权限为 `0600`。部署包自身含明文凭据，也必须保存在受限目录。

Node 容器启动后，回到 Node 管理页确认“在线”，选择默认 Agent 镜像并等待“镜像就绪”，在该 Node 的 Runner 环境中完成登录，最后启用“接收新任务”。安装脚本验证容器启动；跨机器回连、Agent 镜像准备和可调度状态仍以页面显示为准。首次下载部署包会生成注册凭据，已注册的 ID 不可重复下载部署包。程序镜像升级可发布新 tag 和摘要，再按维护安排更新节点的 `node.json` 并重跑安装脚本；当前页面尚不提供程序镜像一键升级。

控制面把 `EXECUTION_MODE` 设为 `distributed`；在执行节点页面为每个 Node 创建独立注册凭据，作为该节点的 `NODE_SECRET`。旧部署的共享 `NODE_SECRET` 保留兼容入口。`local` 是默认的单机回退：没有注册节点时任务在本机 Worker 执行。

执行节点在独立主机运行 `swarmjet node`（可使用 `deploy/compose.node.yaml`）。它需要自己的绝对 `NODE_DATA_DIR`、Docker socket、与项目一致的 Agent 镜像，以及该节点需要的模型环境变量或持久化 CLI 登录配置。GitLab 阶段凭据由控制面按需发送。`NODE_ADVERTISE_URL` 必须是控制面可访问的地址，生产使用 HTTPS。节点向控制面的 `POST /api/nodes` 心跳注册，`GET /api/nodes` 返回在线状态与负载。

同一任务的准备、开发、验证、审查、合并、取消和清理都通过节点协议在归属节点完成；报告由 API 按节点代理读取；保留期退役在该节点删除本机任务目录。节点失联时任务进入 blocked 并保留 `nodeId` 与现场，不会自动改派到其他节点；节点恢复后重试会用相同执行 ID 接管已有容器。

本地开发不受影响：不注册节点时任务 `nodeId` 为空，继续使用宿主机进程内的 workspace / agent / integration 管理器，也不要求 `NODE_SECRET`。

## 检查与备份

以下命令均在 `deploy/` 目录执行。

```sh
docker compose ps
docker compose logs --tail=100 api worker temporal-schema temporal
docker compose exec -T api swarmjet doctor
# PostgreSQL 导出包含任务、配置、session 和 Temporal 历史，输出到受限目录。
docker compose exec -T postgres pg_dumpall -U swarmjet > backup.sql
```

备份 PostgreSQL、`DATA_DIR` 与 RabbitMQ 持久卷；后者包含尚未消费的消息。另行备份 RabbitMQ definitions（交换机、队列、绑定与策略）、`.env` 中的 Broker 凭据和 `SWARMJET_MASTER_KEY`。恢复前停止生产者和消费者，恢复 Broker 数据与 definitions 后再启动应用，避免未确认消息与数据库收据处于不一致状态。`DATA_DIR` 包含任务工作区、受管 Meta-Repo、初始化副本、Git 对象、执行日志和 Agent 会话与配置快照。备份中可能有私有源代码和认证会话，按凭证数据管理。没有主密钥，数据库中保存的连接 Token 无法恢复。恢复时保留 `DATA_DIR` 的绝对路径，并恢复相同 Temporal namespace。旧版本 `state.json` 只能通过 `swarmjet legacy-inventory <file>` 生成人工接管清单；不要直接启用旧 Issue 的 `autonomous` 标签造成重复任务。

在仓库根目录执行 `VALIDATE_DEPLOY=1 scripts/test.sh`：Go race tests、React build、发布镜像与源码构建两种 Compose 配置验证。有 `TEST_DATABASE_URL` 时运行独立临时 schema 的 PostgreSQL 集成测试；`RUN_BROWSER_TESTS=1` 额外运行浏览器测试。测试脚本要求本地安装 Go 和 Node.js。可用 `SWARMJET_TEMPORAL_ADDRESS` 与 `SWARMJET_TEMPORAL_NAMESPACE` 指向独立测试 Temporal，运行持久化/Worker 重启测试；该测试使用专属 task queue，不需要模型 key。原生工具链下还可用 `SWARMJET_DOCKER_SMOKE=1` 和 `SWARMJET_META_BIN` 启用实际 Docker / Meta 检查。

版本依据：[RabbitMQ 4.3 quorum queues](https://www.rabbitmq.com/docs/quorum-queues)、[RabbitMQ 发布确认](https://www.rabbitmq.com/docs/confirms)、[RabbitMQ 消费确认](https://www.rabbitmq.com/docs/confirms#acknowledgement-modes)、[Temporal Server release](https://github.com/temporalio/temporal/releases/tag/v1.32.0)、[生产 PostgreSQL 示例](https://github.com/temporalio/samples-server/blob/main/compose/docker-compose-postgres.yml)、[UI Server release](https://github.com/temporalio/ui-server/releases/tag/v2.54.1)、[Meta release](https://github.com/gitkb/meta/releases/tag/v0.2.22)。

## 从原目录布局切换

以前在仓库根目录保存配置的环境，将 `.env` 和可选的 `.env.development` 移入 `deploy/`；若目标文件已存在，先核对内容，不要直接覆盖。自定义 `compose.override.yaml` 也移入 `deploy/`，其中相对挂载路径需要按新目录调整。保留原 `COMPOSE_PROJECT_NAME`、`DATA_DIR` 和数据库配置，默认项目名仍为 `swarmjet`，无需删除或重建数据卷。

日常 Compose 命令改为在 `deploy/` 执行。本地源码构建显式使用两个 `-f` 时，如有自定义覆盖文件，还需追加 `-f compose.override.yaml`。原生开发命令仍在仓库根目录执行 `./scripts/dev start`，环境配置统一从 `deploy/` 读取。

节点配置与心跳观测分开保存。暂停节点不终止已有任务，归档前检查未完成任务与初始化。网络配置修改影响新任务；正在使用的历史版本保留。Docker 拉取镜像代理仍属于 daemon 部署配置，页面中的节点代理用于准备代码、配置终端与任务容器。
