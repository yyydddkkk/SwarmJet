# 部署 Go / Temporal 版 swarmjet

部署包含控制台 API、Temporal Worker、PostgreSQL 18、Temporal Server 1.32.0 和 Temporal UI 2.54.1。业务执行器是独立的 Agent CLI 镜像，默认不会作为常驻服务启动。这里使用持久化生产 Server，不使用 `temporal server start-dev`。

所有部署文件集中在 `deploy/`：`compose.yaml` 是唯一的服务定义，`compose.build.yaml` 只补充源码构建配置，两个 Dockerfile 分别构建应用与执行器镜像。构建上下文仍是仓库根目录。

## 单机快速部署（源码构建）

服务器先安装 Docker Engine、Docker Compose 插件、Git 和 Python 3。克隆本仓库后，在仓库根目录执行：

```sh
sudo python3 deploy/install.py
# 需要多个执行器时，在首次部署指定（初始化始终需要 opencode）：
# sudo python3 deploy/install.py --agent-clis opencode,codex,claude
```

脚本自动生成权限为 `0600` 的 `deploy/.env`、独立的数据库和管理员密码、运行终端密钥及连接加密主密钥，创建 `/srv/swarmjet/data`，构建镜像并通过 Compose 完成迁移、启动和健康检查。宿主机不需要安装 Go、Node.js 或 Agent CLI。可用 `--data-dir /绝对路径` 修改首次部署的数据目录。脚本要求本机 Docker socket，支持 `deploy/compose.override.yaml`。

默认只监听服务器本机。在自己的电脑执行 `ssh -N -L 8180:127.0.0.1:8180 用户名@服务器`，然后打开 `http://localhost:8180`。账号为 `admin`，密码在服务器 `deploy/.env` 的 `ADMIN_PASSWORD` 中。登录后在“执行器环境”启用已安装的 Agent、配置模型认证，再添加 GitLab 项目。

初始化验证默认使用 `swarmjet-agent:local`。如果业务项目需要额外工具链或离线依赖，应准备相应镜像并修改 `INIT_VERIFY_IMAGE`；服务启动成功不代表项目验收已通过。

失败后可以重新运行脚本；已有 `.env` 不会被覆盖或补写，密码和主密钥不会轮换。`--data-dir` 和 `--agent-clis` 仅用于首次生成配置，之后直接编辑 `.env`。脚本会重新构建并更新服务，不删除数据；升级前仍应备份 PostgreSQL、数据目录和主密钥。已发布镜像的更新使用下文的 `update.sh`。

## 手动本地构建

从仓库根目录进入 `deploy/` 后执行：

```sh
cd deploy
cp .env.example .env
chmod 600 .env
# 编辑 .env：独立随机 PostgreSQL / 管理员密码、实际凭证、DATA_DIR。
mkdir -p /srv/swarmjet/data
chmod 700 /srv/swarmjet/data
docker compose -f compose.yaml -f compose.build.yaml --profile tools build
docker compose up -d --wait
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

侧边栏“镜像配置”管理 Agent 执行镜像。独立 builder 调用 Compose 管理的持久化 BuildKit，发布到自建 Distribution。平台程序镜像和业务应用镜像仍使用各自发布流程。

源码安装脚本会组合 `compose.yaml`、`compose.build.yaml`、`compose.builder.yaml`。手动部署也应包含 builder 配置。先按 [Distribution 部署说明](../docs/registry.md) 准备仓库，再在页面保存仓库账号和构建参数。构建成功不会自动应用或启用 Agent；选择节点分发后，节点按摘要预拉取并检查 CLI，准备完成才可接单。

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

脚本拉取镜像，先执行 Temporal schema 更新和 namespace 初始化、再执行应用 migration，最后更新 API / Worker / UI。它不会执行 `compose down` 或删除数据卷。已有执行容器和 Temporal 历史不会因为替换 Worker 而删除。

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

重建完成后，按正常发布流程让配置终端和所有执行节点使用新的 `AGENT_IMAGE`。执行器环境页的勾选只控制平台准入，不会构建镜像，也不验证账号额度；镜像配置页的“保存并构建”才会提交构建。项目默认 Agent 和 Issue 的 `agent:*` 标签都必须位于已启用列表中。

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

Worker 执行保留期清理：`FAILED_KEEP_DAYS=7` 用于已取消任务的现场，`REPORT_KEEP_DAYS=30` 用于已完成任务。运行中、暂停中和 blocked 任务不会因这些配置自动清除，任务记录与审计信息仍在 PostgreSQL。设置 `GITLAB_WEBHOOK_SECRET` 后可接收 `/api/webhooks/gitlab`；GitLab webhook 配置使用相同的 secret token。

## 分布式执行节点

控制面（API、Worker、PostgreSQL、Temporal）保持集中；资源消耗大的任务工作区、Agent、Compose 联调和报告可以放在独立执行节点。任务在创建时固定归属到某个在线且有余量的节点，后续阶段不会换节点。

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

备份 PostgreSQL 与 `DATA_DIR`；后者包含任务工作区、受管 Meta-Repo、初始化副本、Git 对象、执行日志和 Agent 会话与配置快照。备份中可能有私有源代码和认证会话，按凭证数据管理。单独备份 `SWARMJET_MASTER_KEY`：没有它，数据库中保存的连接 Token 无法恢复。恢复时保留 `DATA_DIR` 的绝对路径，并恢复相同 Temporal namespace。旧版本 `state.json` 只能通过 `swarmjet legacy-inventory <file>` 生成人工接管清单；不要直接启用旧 Issue 的 `autonomous` 标签造成重复任务。

在仓库根目录执行 `VALIDATE_DEPLOY=1 scripts/test.sh`：Go race tests、React build、发布镜像与源码构建两种 Compose 配置验证。有 `TEST_DATABASE_URL` 时运行独立临时 schema 的 PostgreSQL 集成测试；`RUN_BROWSER_TESTS=1` 额外运行浏览器测试。测试脚本要求本地安装 Go 和 Node.js。可用 `SWARMJET_TEMPORAL_ADDRESS` 与 `SWARMJET_TEMPORAL_NAMESPACE` 指向独立测试 Temporal，运行持久化/Worker 重启测试；该测试使用专属 task queue，不需要模型 key。原生工具链下还可用 `SWARMJET_DOCKER_SMOKE=1` 和 `SWARMJET_META_BIN` 启用实际 Docker / Meta 检查。

版本依据：[Temporal Server release](https://github.com/temporalio/temporal/releases/tag/v1.32.0)、[生产 PostgreSQL 示例](https://github.com/temporalio/samples-server/blob/main/compose/docker-compose-postgres.yml)、[UI Server release](https://github.com/temporalio/ui-server/releases/tag/v2.54.1)、[Meta release](https://github.com/gitkb/meta/releases/tag/v0.2.22)。

## 从原目录布局切换

以前在仓库根目录保存配置的环境，将 `.env` 和可选的 `.env.development` 移入 `deploy/`；若目标文件已存在，先核对内容，不要直接覆盖。自定义 `compose.override.yaml` 也移入 `deploy/`，其中相对挂载路径需要按新目录调整。保留原 `COMPOSE_PROJECT_NAME`、`DATA_DIR` 和数据库配置，默认项目名仍为 `swarmjet`，无需删除或重建数据卷。

日常 Compose 命令改为在 `deploy/` 执行。本地源码构建显式使用两个 `-f` 时，如有自定义覆盖文件，还需追加 `-f compose.override.yaml`。原生开发命令仍在仓库根目录执行 `./scripts/dev.py start`，环境配置统一从 `deploy/` 读取。

节点配置与心跳观测分开保存。暂停节点不终止已有任务，归档前检查未完成任务与初始化。网络配置修改影响新任务；正在使用的历史版本保留。Docker 拉取镜像代理仍属于 daemon 部署配置，页面中的节点代理用于准备代码、配置终端与任务容器。
