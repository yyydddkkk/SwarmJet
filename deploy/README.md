# SwarmJet 部署指南

SwarmJet 是单机 Docker Compose 部署：默认只有 `postgres`、`migrate`、`api` 三个服务。控制台、MCP、Cell 监管者和 River 队列都在 `api`（`swarmjet serve`）里。默认不需要公网域名、Temporal、单独 Worker 或执行节点。

## 一条命令安装

在 Linux 服务器上准备 Docker Engine 24+、Compose 插件 2.20+ 和 Python 3。任选一种方式：

```sh
# 在仓库中（本地构建平台和 Agent 镜像）
python3 deploy/install.py

# 不下载仓库（拉取 GHCR 发布镜像；需要 v* 标签）
curl -fsSL https://raw.githubusercontent.com/yyydddkkk/SwarmJet/main/deploy/install.py | python3 -

# 正式发布前用 SWARMJET_REF=dev：新代码只在 dev 上，让脚本和 Compose 都从 dev 下载
curl -fsSL https://raw.githubusercontent.com/yyydddkkk/SwarmJet/dev/deploy/install.py \
  | SWARMJET_REF=dev python3 -
```

`SWARMJET_REF` 决定下载 Compose 文件的 Git 分支或标签，默认 `main`；也可以用 `--ref` 覆盖。它不决定镜像版本：GHCR 镜像仍由 `--version` 选择（默认 `latest`）。`dev` 分支还没有 GHCR 发布镜像时，请在仓库内用 `python3 deploy/install.py` 本地构建。

安装脚本会生成随机数据库密码、管理员密码和 `SWARMJET_MASTER_KEY`，写入权限 `0600` 的 `.env`；创建数据目录；检查 Docker、Compose、端口；构建或拉取镜像；启动迁移和服务；等待健康检查。

默认安装目录：仓库内是 `deploy/`，单独运行时是 `~/swarmjet`。默认数据目录是 `~/swarmjet/data`。需要 root 权限的端口或 `/srv` 路径时再加 `sudo`，并确保 Docker 对当前用户可用。

交互问题都有默认值：

| 问题 | 默认 | 说明 |
|---|---|---|
| 安装目录 | 仓库内 `deploy/`，否则 `~/swarmjet` | 保存 `.env` 和 Compose 文件 |
| 数据目录 | `~/swarmjet/data` | `DATA_DIR` 必须是绝对路径，容器内外相同 |
| 控制台端口 | `8180` | 只监听 `127.0.0.1` |
| 启用 Cell | 是 | 会叠加 `compose.cells.yaml`，挂载 Docker socket |
| 启用隧道 | 是 | 没有公网域名时用 Cloudflare 临时隧道同步 webhook |
| 镜像来源 | 仓库内本地构建，否则 GHCR | GHCR 需要选择版本，默认 `latest` |

完全非交互（CI 或脚本）示例：

```sh
python3 deploy/install.py --yes \
  --data-dir /srv/swarmjet/data --port 8180 \
  --cells --tunnel --image-source ghcr --version v0.3.0
```

结束时脚本打印控制台地址、管理员账号和密码位置；密码默认不显示，加 `--show-password` 才在终端输出。密码也可以从 `.env` 的 `ADMIN_PASSWORD` 读取。

> 用隧道安装时，脚本会把 `PUBLIC_ORIGIN` 自动设为读取到的临时隧道地址；地址变化后需要更新 `.env` 并重启 `api`。

## 安装脚本子命令

```sh
python3 deploy/install.py install     # 默认；安装并启动
python3 deploy/install.py upgrade     # 重新构建/拉取镜像并滚动更新
python3 deploy/install.py status      # 服务、健康状态和控制台地址
python3 deploy/install.py logs -f api # 日志（默认 tail 200 行）
python3 deploy/install.py connect --client claude
python3 deploy/install.py stop        # 停止服务，保留容器以外的一切
python3 deploy/install.py uninstall   # 删除容器和网络，保留数据
python3 deploy/install.py uninstall --purge  # 二次确认后删除数据卷和 DATA_DIR
```

其他常用参数：`--dir` 指定安装目录，`--ref` 指定下载 Compose 的分支或标签（也可用 `SWARMJET_REF`），`--project-name` 指定 Compose 项目名（默认 `swarmjet`，可用于避开旧数据卷），`--image-source repo|ghcr`、`--version`、`--image`、`--agent-image`、`--show-password`。脚本检测到上一轮部署留下的 Temporal/Worker 容器或旧数据卷时只提示，不会自动删除。

`connect` 会在 `api` 容器里执行 `swarmjet connect`，打印一次性 Token 和对应客户端配置。如果本机有 `claude` 命令，会询问是否直接执行 `claude mcp add`。

## 手动安装

不使用安装脚本时：

```sh
cd deploy
cp .env.example .env
chmod 600 .env
# 至少填写 POSTGRES_PASSWORD、DATABASE_URL、ADMIN_PASSWORD、
# SWARMJET_MASTER_KEY、DATA_DIR 和 PUBLIC_ORIGIN。
mkdir -p /srv/swarmjet/data && chmod 700 /srv/swarmjet/data
docker compose --env-file .env -f compose.yaml -f compose.build.yaml --profile tools build
docker compose --env-file .env -f compose.yaml up -d --wait
```

`POSTGRES_PASSWORD` 建议 `openssl rand -hex 24`，`SWARMJET_MASTER_KEY` 用 `openssl rand -hex 32`。`DATABASE_URL` 中的密码必须与 `POSTGRES_PASSWORD` 一致。

需要 Cell 时叠加 `-f compose.cells.yaml`；需要隧道时再加 `--profile tunnel` 并在 `.env` 设置 `SWARMJET_TUNNEL_URL=http://tunnel:2000`。

## 服务与端口

| 服务 | 作用 | 端口 |
|---|---|---|
| `postgres` | 业务数据和 River 队列 | 不发布 |
| `migrate` | 一次性数据库迁移 | 无 |
| `api` | 控制台、MCP、Router、River 消费者、Cell 监管者 | `${LISTEN_HOST}:${HTTP_PORT}` |
| `tunnel`（profile） | cloudflared 临时隧道 | 指标仅 `127.0.0.1:2000` |
| `prometheus`、`alertmanager`、`postgres-exporter`（profile） | 可选监控 | 分别绑定 `127.0.0.1` |

默认 `LISTEN_HOST=127.0.0.1`。远程访问用 SSH 隧道：

```sh
ssh -N -L 8180:127.0.0.1:8180 用户名@服务器
```

生产环境建议在反向代理上终止 HTTPS，把 `PUBLIC_ORIGIN` 设为用户访问的精确 origin，并设置 `SECURE_COOKIES=true`。

## 环境变量

| 变量 | 必填 | 说明 |
|---|---|---|
| `DATABASE_URL` | 是 | PostgreSQL DSN，容器内使用 `postgres:5432` |
| `POSTGRES_PASSWORD` | 是 | 数据库密码；`DATABASE_URL` 必须与之一致 |
| `ADMIN_USERNAME`、`ADMIN_PASSWORD` | 是 | 唯一管理员账号，密码至少 12 位 |
| `SWARMJET_MASTER_KEY` | 是 | 32 字节十六进制，加密数据库里的 GitHub App 凭证 |
| `DATA_DIR` | 是 | 宿主机绝对路径，容器内同路径 |
| `PUBLIC_ORIGIN` | 否 | 控制台地址，默认 `http://localhost:8180` |
| `LISTEN_HOST`、`HTTP_PORT`、`SECURE_COOKIES` | 否 | 监听地址、端口、HTTPS Cookie |
| `IMAGE`、`AGENT_IMAGE` | 否 | 平台镜像和 Cell 使用的 Agent 镜像 |
| `SWARMJET_CELLS`、`SWARMJET_FORAGER_MODEL`、`SWARMJET_CELL_MCP_URL`、`SWARMJET_CELL_NETWORK` | 否 | Cell 运行时 |
| `OPENCODE_AUTH_CONTENT` | 否 | 可选 OpenCode 凭证；CLI 登录终端的结果优先 |
| `GITHUB_APP_ID`、`GITHUB_APP_PRIVATE_KEY_FILE`、`GITHUB_WEBHOOK_SECRET` | 否 | 第二轮手动方式；设置后优先于数据库里的 App |
| `SWARMJET_TUNNEL_URL` | 否 | 隧道指标地址，例如 `http://tunnel:2000` |
| `TUNNEL_METRICS_PORT`、`PROMETHEUS_PORT`、`ALERTMANAGER_PORT` | 否 | 本机回环端口 |

第三轮已删除 `TEMPORAL_*`、`GITLAB_*`、`SMTP_*`、`NODE_*`、`EXECUTION_MODE`、`MIHOMO_*`、`RUNTIME_ADMIN_*`、`TASK_CONCURRENCY`、`INTEGRATION_ENV_ALLOWLIST`、`AGENT_ENV_ALLOWLIST`、`INIT_VERIFY_IMAGE`、`FAILED_KEEP_DAYS`、`REPORT_KEEP_DAYS`、`PLATFORM_IMAGE`。旧 `.env` 中残留这些键不会生效，可以删除。

`.env` 只用于 Compose 插值，但平台镜像会读取上面列出的变量。新增变量时在 `compose.yaml` 的 `api.environment` 中显式传入，不要依赖 `env_file`。

## GitHub App

推荐在控制台的 **接入** 页点击“一键创建 GitHub App”：浏览器跳到 GitHub 确认，回调后 App ID、私钥和 webhook secret 以密文存进数据库，无需重启。App 创建后还要在 GitHub 上把它安装到目标仓库。

没有公网入口时：

- 启用隧道 profile，平台会定期读取 cloudflared 的 `/quicktunnel` 地址并自动把 App 的 webhook URL 同步为 `https://<隧道地址>/api/webhooks/github`。
- 或者手动创建 App，把 webhook 指向 `https://<你的域名>/api/webhooks/github`，并订阅 Pull request、Pull request review、Pull request review comment、Issue comment、Issues、Check run、Status。

第二轮的环境变量方式仍兼容：把 PEM 放在 `DATA_DIR` 下（例如 `${DATA_DIR}/github-app-key.pem`，权限 `0600`），设置 `GITHUB_APP_ID`、`GITHUB_APP_PRIVATE_KEY_FILE`、`GITHUB_WEBHOOK_SECRET`。环境变量优先于数据库里的 App；两种方式都不要把私钥提交到仓库或放进 Agent 镜像。

没有配置 GitHub App 时 `serve` 仍能启动，Cell 可以创建但需要 GitHub 的操作会给出明确错误。在接入页创建 App 后无需重启即可生效。

## 连接本地 Agent

在控制台的 **接入** 页新建连接，选择名称和作用域；创建后一次性显示明文和三种配置。`default` 可以派活和查看，`admin` 还能创建/修改 Hive 和凭证。关闭对话框后无法再次查看明文。

也可以在服务器上生成：

```sh
python3 deploy/install.py connect --client claude
# 或：docker compose --env-file deploy/.env -f deploy/compose.yaml exec -T api \
#       swarmjet connect --client codex
```

Claude Code 等价于：

```sh
claude mcp add --transport http swarmjet http://127.0.0.1:8180/mcp \
  --header "Authorization: Bearer <TOKEN>"
```

Codex 使用 `~/.codex/config.toml` 的 `[mcp_servers.swarmjet]` HTTP 配置（含 bearer token）；JSON 客户端使用 `.mcp.json` 的 `mcpServers.swarmjet`。Token 只显示一次，数据库只保存哈希。

## Cell 与 Docker socket 安全

基础 `compose.yaml` **不会**给 `api` 挂 Docker socket，`DATA_DIR` 是只读。启用 Cell 必须叠加 `compose.cells.yaml`：

```sh
docker compose --env-file .env -f compose.yaml -f compose.cells.yaml up -d --wait
```

叠加文件做四件事：

- 把 `SWARMJET_CELLS` 设为 `1`；
- 挂载 `/var/run/docker.sock`；
- 把 `DATA_DIR` 覆盖为可写（最终仍只有一个同路径挂载）；
- 给 `api` 加 `swarmjet` 网络别名，并设置 `SWARMJET_CELL_NETWORK` 的默认值。

**权限变化**：挂载 Docker socket 等同于给 `api` 宿主 root 级权限，可以创建、停止、删除宿主容器并读写宿主路径。只在自己控制、管理员可信的服务器上启用；不要把 socket 再挂进任何 Agent 容器；MCP Token 只发给可信的本地 Agent。

Cell 工作区位于 `DATA_DIR/cells/<agent-id>/workspace`。Cell 内的 CLI 以固定非 root 用户运行，发布器在平台侧推送受控分支，Cell 内不持有 Git 凭证。

关闭 Cells 时用不带叠加文件的基础 compose 重新创建 `api`：

```sh
docker compose --env-file .env -f compose.yaml up -d api
```

已经存在的 Cell 容器不会自动删除，可按标签 `swarmjet.cell=true` 查找。

## 隧道

`tunnel` profile 运行固定版本的 `cloudflare/cloudflared`：

```sh
docker compose --env-file .env -f compose.yaml --profile tunnel up -d
```

并在 `.env` 设置 `SWARMJET_TUNNEL_URL=http://tunnel:2000`。平台后台读取 `http://tunnel:2000/quicktunnel` 得到公网地址，然后用 App JWT 调用 GitHub 的 `PATCH /app/hook/config` 更新 webhook URL。快速隧道地址是临时的，重启 cloudflared 后会变化；平台检测到变化会再次同步。隧道只把 `api:8080` 暴露出去，PostgreSQL 和指标端口不公开。

没有 GitHub App 时隧道仍可启动，但同步状态会显示未配置；创建 App 后自动重试。

## 升级

先备份 PostgreSQL、`DATA_DIR` 和 `SWARMJET_MASTER_KEY`，然后：

```sh
python3 deploy/install.py upgrade
```

仓库内默认重新构建本地镜像；单独运行时默认拉取当前 tag 的 GHCR 镜像，可用 `--version v0.3.0` 指定新版本。升级不会删除数据卷；`migrate` 在 `api` 之前完成。回滚需要同时回滚镜像和数据库备份，因为迁移只新增、不删除。

## 备份与恢复

需要备份三样东西：

1. PostgreSQL：`docker compose --env-file .env -f compose.yaml exec -T postgres pg_dumpall -U swarmjet > backup.sql`
2. `DATA_DIR`：包含 Cell 工作区、会话和上传的 App 私钥文件。
3. `SWARMJET_MASTER_KEY`：单独保存，不能只和数据库备份放在一起。

`SWARMJET_MASTER_KEY` 丢失后，数据库里的 GitHub App 私钥和 webhook secret 无法解密，需要重新创建 App。`ADMIN_PASSWORD` 丢失时可以直接改 `.env` 后重启 `api`（密码只在启动时读取）。

恢复时先 `down`，恢复数据库和 `DATA_DIR`，保持相同的绝对路径，再启动。`postgres-data` 卷必须保留；`uninstall --purge` 会删除它。

## 监控（可选）

```sh
docker compose --env-file .env -f compose.yaml --profile observability up -d
```

Prometheus 抓取 `api:9090` 和 `postgres-exporter:9187`，规则只对抓取失败和 PostgreSQL 不可用告警。Prometheus/Alertmanager UI 只绑定 `127.0.0.1`，默认 receiver 不在 UI 之外发送通知。

## 从第二轮升级

第三轮删除了 Temporal、Worker、执行节点、mihomo 和 GitLab webhook。升级时：

- 停止旧的 `worker`/`temporal` 容器；Compose 不再包含它们。安装脚本只提示，不会自动删除旧容器或旧数据卷。
- 保留 `swarmjet_postgres-data` 卷和原 `DATA_DIR`；新版本会迁移到最新 schema，旧表保留为历史。
- `SWARMJET_MASTER_KEY` 必须沿用，否则已保存的 GitHub App 凭证无法解密。
- 第二轮设置的 `GITHUB_APP_ID`、`GITHUB_APP_PRIVATE_KEY_FILE`、`GITHUB_WEBHOOK_SECRET` 继续有效；之后可以改成在接入页创建 App。
- `deploy/compose.cells.yaml` 的用法不变。

## 排障

| 现象 | 检查 |
|---|---|
| 安装脚本提示端口被占用 | `ss -ltnp \| grep 8180`；换 `--port` 或停掉占用进程 |
| `migrate` 报 `password authentication failed` | 同名的旧 `swarmjet_postgres-data` 卷与新生成的密码不匹配。恢复原 `.env`，或安装时加 `--project-name <新名字>`；不要删除旧卷 |
| `api` 启动即退出，提示缺少 `SWARMJET_MASTER_KEY` | 在 `.env` 填写 64 位十六进制；不要用空值 |
| 控制台能登录但 MCP 连不上 | Token 是否有效、URL 是否为 `http://127.0.0.1:8180/mcp`、SSH 隧道是否在 |
| 接入页创建 App 后回调报错 | `PUBLIC_ORIGIN` 是否与浏览器地址一致；`SECURE_COOKIES` 是否与 HTTPS 匹配 |
| webhook 没有同步 | `docker compose logs tunnel`；`SWARMJET_TUNNEL_URL` 是否为 `http://tunnel:2000`；接入页的最近错误 |
| Cell 创建失败，提示 Docker socket | 是否叠加 `compose.cells.yaml`；`docker info` 是否正常 |
| 升级后旧的 Temporal 容器还在 | 手工 `docker rm -f <name>`；脚本不会删除 |
