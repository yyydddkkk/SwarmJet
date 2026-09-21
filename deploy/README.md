# 部署 Go / Temporal 版 swarmjet

部署包含控制台 API、Temporal Worker、PostgreSQL 18、Temporal Server 1.32.0 和 Temporal UI 2.54.1。业务执行器是独立的 OpenCode 镜像，默认不会作为常驻服务启动。这里使用持久化生产 Server，不使用 `temporal server start-dev`。

所有部署文件集中在 `deploy/`：`compose.yaml` 是唯一的服务定义，`compose.build.yaml` 只补充源码构建配置，两个 Dockerfile 分别构建应用与执行器镜像。构建上下文仍是仓库根目录。

## 本地构建

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

独立执行镜像固定 OpenCode `1.18.31`、Node `22.23.1`、glab `1.115.0`、uv 和 pnpm，包含 Git、Python 与编译依赖。主镜像编译 Go `1.27.1` 与 React 控制台，并通过官方 `.sha256` 校验安装 Meta `0.2.22` 的所有运行组件。Meta 发布二进制要求 glibc 2.39，因此主镜像运行层使用 Ubuntu 24.04；agent 保持 Node 22 / Debian bookworm。需要额外业务系统依赖时，构建派生 agent 镜像并在项目中指定。

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

配置终端使用固定的 `AGENT_IMAGE`，只挂载 `DATA_DIR/runtime-profiles/default/home`。运行 `opencode auth login` 完成 provider 配置；当前镜像也包含 Codex CLI，可以执行 `codex login --device-auth`，但 Codex 的任务执行适配器尚未实现。容器没有额外暴露 OAuth localhost 回调端口，应使用设备码或 API key 登录方式。

退出终端会停止配置容器，登录与配置文件保留。新任务角色会获得独立的 OpenCode 配置副本；已有角色的会话和刷新后的凭据不会被覆盖。这里管理模型服务登录，GitLab 访问仍使用部署环境中的凭据引用。

管理终端不保存录屏、输入内容或 shell 历史，只记录打开/关闭操作。登录配置检测与模型清单不代表模型额度或实际请求权限已经通过验证。

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

## 检查与备份

以下命令均在 `deploy/` 目录执行。

```sh
docker compose ps
docker compose logs --tail=100 api worker temporal-schema temporal
docker compose exec -T api swarmjet doctor
# PostgreSQL 导出包含任务、配置、session 和 Temporal 历史，输出到受限目录。
docker compose exec -T postgres pg_dumpall -U swarmjet > backup.sql
```

备份 PostgreSQL 与 `DATA_DIR`；后者包含任务工作区、Git 对象、执行日志和 OpenCode 会话。备份中可能有私有源代码和认证会话，按凭证数据管理。恢复时保留 `DATA_DIR` 的绝对路径，并恢复相同 Temporal namespace。旧版本 `state.json` 只能通过 `swarmjet legacy-inventory <file>` 生成人工接管清单；不要直接启用旧 Issue 的 `autonomous` 标签造成重复任务。

在仓库根目录执行 `VALIDATE_DEPLOY=1 scripts/test.sh`：Go race tests、React build、发布镜像与源码构建两种 Compose 配置验证。有 `TEST_DATABASE_URL` 时运行独立临时 schema 的 PostgreSQL 集成测试；`RUN_BROWSER_TESTS=1` 额外运行浏览器测试。测试脚本要求本地安装 Go 和 Node.js。可用 `SWARMJET_TEMPORAL_ADDRESS` 与 `SWARMJET_TEMPORAL_NAMESPACE` 指向独立测试 Temporal，运行持久化/Worker 重启测试；该测试使用专属 task queue，不需要模型 key。原生工具链下还可用 `SWARMJET_DOCKER_SMOKE=1` 和 `SWARMJET_META_BIN` 启用实际 Docker / Meta 检查。

版本依据：[Temporal Server release](https://github.com/temporalio/temporal/releases/tag/v1.32.0)、[生产 PostgreSQL 示例](https://github.com/temporalio/samples-server/blob/main/compose/docker-compose-postgres.yml)、[UI Server release](https://github.com/temporalio/ui-server/releases/tag/v2.54.1)、[Meta release](https://github.com/gitkb/meta/releases/tag/v0.2.22)。

## 从原目录布局切换

以前在仓库根目录保存配置的环境，将 `.env` 和可选的 `.env.development` 移入 `deploy/`；若目标文件已存在，先核对内容，不要直接覆盖。自定义 `compose.override.yaml` 也移入 `deploy/`，其中相对挂载路径需要按新目录调整。保留原 `COMPOSE_PROJECT_NAME`、`DATA_DIR` 和数据库配置，默认项目名仍为 `swarmjet`，无需删除或重建数据卷。

日常 Compose 命令改为在 `deploy/` 执行。本地源码构建显式使用两个 `-f` 时，如有自定义覆盖文件，还需追加 `-f compose.override.yaml`。原生开发命令仍在仓库根目录执行 `./scripts/dev.py start`，环境配置统一从 `deploy/` 读取。
