# SwarmJet 部署指南

SwarmJet 使用单机 Docker Compose。`api` 运行控制台、MCP、Router、River 消费者与 Cell 监管者，PostgreSQL 保存状态与消息；启用 Cells 后同时启动 omp 的凭证服务。Cell 内统一使用 oh-my-pi，不需要在容器里登录其他 Agent CLI。

本文面向 v0.1.0。Queen 运行时、默认入口路由、模板目录与自动回滚、网页创建向导和 Queen 对话标为 **v0.1.0 起提供**；发布前需等待相应实现合入。

## 安装

需要 Linux x86_64、Docker Engine 24+、Compose 插件 2.20+、Python 3，以及可用的模型提供方凭证。当前发布 workflow 仅构建 `linux/amd64`。

在 v0.1.0 标签及 GHCR 包公开后，一键安装固定版本：

```sh
curl -fsSL https://raw.githubusercontent.com/yyydddkkk/SwarmJet/v0.1.0/deploy/install.py \
  | python3 - --image-source ghcr --ref v0.1.0 --version v0.1.0
```

发布前在仓库内本地构建：

```sh
python3 deploy/install.py
```

脚本会生成数据库密码、管理员密码、`SWARMJET_MASTER_KEY`，写入权限 `0600` 的 `.env`；创建数据目录和模型代理 Token，检查 Docker 与端口，构建或拉取镜像，执行迁移并等待服务健康。默认启用 Cells 与 Cloudflare webhook 隧道；按交互提示可关闭。

默认安装目录在仓库内是 `deploy/`，独立脚本是 `~/swarmjet`；数据目录默认 `~/swarmjet/data`。控制台默认 <http://localhost:8180>，只监听 `127.0.0.1`。管理员用户名为 `admin`，密码在 `.env` 的 `ADMIN_PASSWORD`，加 `--show-password` 才会在终端显示。远程访问：

```sh
ssh -N -L 8180:127.0.0.1:8180 用户名@服务器
```

首次登录后在 **接入** 页一键创建 GitHub App，并安装到目标仓库；在 **模型** 页填写凭证。**v0.1.0 起提供**：创建 Hive 后与 Queen 对话选方法论，再交代任务；也可以通过本地 Agent 的 MCP 连接派活。没有免费默认模型。

### 脚本参数与运维命令

```sh
python3 deploy/install.py install --help
python3 deploy/install.py install --yes --cells --tunnel \
  --image-source ghcr --ref v0.1.0 --version v0.1.0
python3 deploy/install.py status
python3 deploy/install.py logs -f api
python3 deploy/install.py connect --client claude
python3 deploy/install.py stop
python3 deploy/install.py uninstall
```

省略子命令等于 `install`。`stop` 与 `uninstall` 保留数据；`uninstall --purge` 经确认后删除 Compose 管理的数据卷与 `DATA_DIR`。动态创建的 Cell 容器和 `swarmjet-cell-<agent-id>-data` 会话卷不由 Compose 管理，需另行检查与清理；不要将 purge 视为已清空全部 Cell 数据。

| 参数 | 用途 |
| --- | --- |
| `--dir` | 安装目录；运维子命令用它定位已有配置。 |
| `--data-dir`、`--port`、`--listen-host`、`--public-origin` | install 时指定数据路径和控制台地址。 |
| `--cells` / `--no-cells`、`--tunnel` / `--no-tunnel` | install 时选择 Cell 和 webhook 隧道。 |
| `--project-name` | install 时指定 Compose 项目名，默认 `swarmjet`；不同项目使用不同数据卷。 |
| `--image-source repo\|ghcr` | 仓库内默认本地构建，独立运行默认 GHCR。 |
| `--ref` / `SWARMJET_REF` | 下载 Compose 的 Git 分支或标签；未指定时 release `--version` 可决定 ref，否则默认 `main`。 |
| `--version` | GHCR 镜像 tag，默认 `latest`；建议与 `--ref` 一起固定到同一发布版本。 |
| `--image`、`--agent-image` | install 时覆盖平台与 Agent 镜像。 |

GHCR 平台镜像为 `ghcr.io/yyydddkkk/swarmjet:<tag>`，Agent 镜像为 `ghcr.io/yyydddkkk/swarmjet/agent:<tag>`。正式 `vX.Y.Z` tag 的发布流程同时推送版本 tag 和 `latest`。auth-broker、auth-gateway 与 Cells 共用 `AGENT_IMAGE`，启用 Cells 时 `compose pull` 会拉取该镜像。两个 GHCR 包都必须设为公开，匿名安装才能成功。

## 服务与端口

| 服务 | 启用条件与职责 | 端口 |
| --- | --- | --- |
| `api` | 始终；控制台、MCP、事件与 Agent 管理、模型代理 | 默认宿主 `127.0.0.1:8180` → `8080`；webhook 独立监听 `8081`，不直接映射宿主 |
| `postgres` | 始终；业务状态、消息、River 队列 | `5432`，不映射宿主 |
| `migrate` | 启动时一次性数据库迁移，成功后 api 才启动 | 无 |
| `auth-broker` | `compose.cells.yaml`；保管模型凭证、刷新 OAuth | `8765`，仅 llm 网络 |
| `auth-gateway` | `compose.cells.yaml`；经 broker 解析凭证、调用模型 | `8766`，仅 llm 网络 |
| `tunnel` | 可选 `tunnel` profile；仅转发 webhook 到 `api:8081` | 指标默认 `127.0.0.1:2000` |
| `prometheus`、`postgres-exporter`、`alertmanager` | 可选 `observability` profile | UI 默认 `127.0.0.1:9090`、`127.0.0.1:9093` |

`compose.build.yaml` 的 `agent-image` 是本地构建目标（`tools` profile），不作为运行服务。

公网控制台需要自行配置 HTTPS 反向代理，将 `PUBLIC_ORIGIN` 设为浏览器实际访问的精确 origin，并设置 `SECURE_COOKIES=true`。临时隧道不能用来访问控制台或 MCP。

## 环境变量

`.env` 用于 Compose 插值；运行时变量由 Compose 显式传给服务。下表区分部署设置与镜像构建参数，不要把模型 API key 写进 Cell 环境。

| 变量 | 默认 / 要求 | 用途 |
| --- | --- | --- |
| `COMPOSE_PROJECT_NAME` | `swarmjet` | 网络与数据卷的项目命名空间 |
| `POSTGRES_PASSWORD` | 必填，脚本生成 | 数据库密码 |
| `DATABASE_URL` | 必填，脚本生成 | 容器内 DSN，主机 `postgres:5432`；密码与上一项一致 |
| `ADMIN_USERNAME`、`ADMIN_PASSWORD` | `admin` / 必填且至少 12 字符 | 控制台单管理员账号 |
| `SWARMJET_MASTER_KEY` | 必填，32 字节十六进制 | 加密数据库中的 GitHub App 私钥与 webhook secret；必须备份 |
| `DATA_DIR` | 必填，绝对路径 | 宿主与 api 容器内路径相同，保存 Cell 与 omp 状态 |
| `IMAGE`、`AGENT_IMAGE` | 本地 `swarmjet:local`、`swarmjet-agent:local` | GHCR 安装时由脚本写入发布镜像 |
| `OMP_VERSION` | `18.12.2`，构建参数 | 本地构建的 omp 版本；GHCR 镜像版本由发布 workflow 固定，改此值不会改变预构建镜像 |
| `AGENT_CLIS` | 空，构建参数 | 本地镜像额外 CLI；Cell 仍统一使用 omp |
| `PUBLIC_ORIGIN` | `http://localhost:8180` | 登录与 App 回调的控制台 origin，不能用临时隧道地址 |
| `LISTEN_HOST`、`HTTP_PORT` | `127.0.0.1`、`8180` | 控制台宿主监听地址与端口 |
| `SECURE_COOKIES` | `false` | HTTPS 部署设为 `true` |
| `SWARMJET_CELLS` | 基础配置 `0`，Cell 叠加文件强制 `1` | 仅设变量不能替代 Docker socket 等挂载 |
| `SWARMJET_DEFAULT_MODEL` | `deepseek/deepseek-v4-flash` | 未指定模型时的兜底标识，不包含凭证 |
| `SWARMJET_CELL_MCP_URL` | `http://swarmjet:8080/mcp` | Cell 访问平台的内部地址 |
| `SWARMJET_CELL_LLM_URL` | 空 | 空时从 MCP URL 推导 `/llm` 地址 |
| `SWARMJET_CELL_NETWORK` | Cell 叠加文件默认 `<项目名>_default` | Cell 加入的 Docker 网络 |
| `SWARMJET_LLM_GATEWAY_URL` | Cell 叠加文件默认 `http://auth-gateway:8766` | api 调用模型 gateway |
| `SWARMJET_AUTH_BROKER_URL` | Cell 叠加文件默认 `http://auth-broker:8765` | api 管理模型凭证 |
| `SWARMJET_OMP_CONFIG_DIR` | Cell 叠加文件设为 `DATA_DIR/omp` | api 读取 broker/gateway Token 的位置 |
| `GITHUB_APP_ID`、`GITHUB_APP_PRIVATE_KEY_FILE`、`GITHUB_WEBHOOK_SECRET` | 空 | 可选手动 App 配置，设置后优先于数据库配置；PEM 应位于 DATA_DIR 内的容器可见路径 |
| `SWARMJET_TUNNEL_URL` | 空；启用隧道时 `http://tunnel:2000` | 平台读取 quick tunnel 地址的指标入口 |
| `WEBHOOK_LISTEN_ADDRESS` | `:8081` | api 的 webhook 专用监听地址 |
| `TUNNEL_METRICS_PORT` | `2000` | cloudflared 的宿主回环端口 |
| `PROMETHEUS_PORT`、`ALERTMANAGER_PORT` | `9090`、`9093` | 可选监控 UI 的回环端口 |

`METRICS_LISTEN_ADDRESS` 在 Compose 中固定为 `:9090`，不发布 api 的指标端口。broker/gateway 内部使用 `HOME=DATA_DIR`、`PI_CONFIG_DIR=omp`，gateway 使用 `OMP_AUTH_BROKER_URL=http://auth-broker:8765`，这些由叠加文件设置。

## Cell 与模型凭证

安装脚本会按配置选择叠加文件。手动操作已有安装时，在安装目录使用同样的文件集合：

```sh
docker compose --env-file .env -f compose.yaml -f compose.cells.yaml config --quiet
docker compose --env-file .env -f compose.yaml -f compose.cells.yaml up -d --wait
```

基础文件不给 api 挂 Docker socket，DATA_DIR 只读；Cell 叠加文件使其可写，挂载 socket、配置 Cell 网络并启动凭证服务。挂载 socket 等同宿主 root 权限；只部署在管理员可信的服务器。Cell 自身不挂 socket，以非 root 用户运行，工作区在 `DATA_DIR/cells/<agent-id>/workspace`。

auth-broker 和 auth-gateway 只在专用 llm 网络，api 同时连接默认网络和 llm 网络；Cell 不加入 llm 网络。安装脚本创建 `DATA_DIR/omp`（`0700`）与 `auth-broker.token`、`auth-gateway.token`（`0600`），重复安装或升级不会轮换已有 Token。

在网页 **模型** 页写入凭证，或由 admin MCP Token 调用 `set_model_credential`。HTTP 接口为 `PUT /api/models/credentials/{provider}`，需要管理员会话及同源 CSRF 头；`GET /api/models` 只返回脱敏凭证状态与模型列表。DeepSeek 是首个支持的提供方。

Cell 通过自身 Token 调用 api 的 `/llm/`，平台换成 gateway Token 转发，按 Agent 记录用量。方法论声明 `maxCostPerAgentUsd` / `maxCostPerDayUsd` 时，请求前检查预算，超限返回 `402` 并发送系统通知。`GET /api/usage?hive=&agent=` 可查看统计。**v0.1.0 起提供**：Queen 开启时系统通知默认交给 Queen。

GitHub 凭证只在平台侧用于检出、发布与合并。仓库扩展、hook、工具、项目 MCP 默认禁用；规则、skill、独立的 AGENTS.md / CLAUDE.md 仍加载。外部 GitHub 内容按带可信度标记的数据传递。不要向 `.env`、镜像或文档提交真实凭证。

## GitHub App、隧道与 webhook 补投

推荐在 **接入** 页一键创建 GitHub App。App ID、私钥和 webhook secret 加密入库，配置后无需重启；随后在 GitHub 安装到目标仓库。未配置 App 时 api 可启动，但需要 GitHub 的操作不可用。

无公网域名可启用 `tunnel` profile，并设置 `SWARMJET_TUNNEL_URL=http://tunnel:2000`。安装脚本会完成这两项。对已有 Cell 安装手动启用：

```sh
docker compose --env-file .env -f compose.yaml -f compose.cells.yaml --profile tunnel up -d --wait
```

平台读取 cloudflared 的 `/quicktunnel`，用 GitHub App 身份更新 webhook URL 为 `https://<临时域名>/api/webhooks/github`。隧道只转发 api 的 `8081`；该监听器只服务 `/api/webhooks/github` 和 `/healthz`，不开放控制台与 `/mcp`。

临时域名重启后会变化，平台会重新同步。不要把 `PUBLIC_ORIGIN` 改成这个域名；旧配置若这样设置，运行 install 或 upgrade 会修复为本机 origin。自有公网入口可将 webhook 路径转发到 api，并在 GitHub App 设置 webhook；订阅项见平台的一键创建配置。

平台在启动、隧道地址变化或恢复以及每 10 分钟查询最近 72 小时的 App webhook deliveries，对尚无成功投递的失败事件请求重投，每次最多 20 条，遇到限流停止。事件按 delivery ID 幂等处理；状态见接入页及 `/api/setup/status` 的 `tunnel.redelivery`。更早或超出补投窗口的事件仍需人工检查 GitHub 投递记录。

## 本地 Agent 接入

在 **接入** 页新建连接，复制客户端配置；Token 仅显示一次，数据库只保存哈希。`default` 可派活、查看和管理方法论，`admin` 还能创建/修改 Hive 和模型凭证。只把相应权限的 Token 发给可信客户端。

```sh
python3 deploy/install.py connect --client claude
python3 deploy/install.py connect --client codex
```

命令在 api 容器内执行 `swarmjet connect`。本地客户端需要能访问控制台同一主机的 `/mcp`；远程部署可使用前述 SSH 转发，webhook 隧道不承载 MCP。

## 升级与第三轮配置迁移

先备份数据库、DATA_DIR 与 `.env`，另存 `SWARMJET_MASTER_KEY`。GHCR 安装固定到同一版本的 Compose 和镜像：

```sh
python3 deploy/install.py upgrade --image-source ghcr --ref v0.1.0 --version v0.1.0
```

仓库本地构建则先更新 checkout，再运行 `python3 deploy/install.py upgrade --image-source repo`。数据库迁移由 migrate 在 api 启动前执行，升级保留数据卷。需要回退时同时恢复相应镜像、配置与数据库备份，不要假定旧镜像能读取新 schema。

**从第三轮升级同样使用新版 `upgrade`**，沿用原安装目录、项目名和 DATA_DIR。升级会保留 Cell 与隧道的启用状态，自动迁移：

- 从已有 `AGENT_CLIS` 列表移除 `opencode`，保留其他额外 CLI；omp 始终随 Agent 镜像安装。
- 把旧默认值 `deepseek/deepseek-chat`、`deepseek/deepseek-reasoner` 改为 `deepseek/deepseek-v4-flash`，保留其他自定义值。
- 保留已有数据库密码、主密钥及代理 Token，缺失的凭证服务 Token 会生成。

不要删除原 `.env` 后重新生成数据库密码。旧 Cell 会话来自不同 CLI，不能假定可恢复为 omp 会话；升级后检查任务与会话状态，再恢复派活。

更早部署若还留有 Temporal、Worker、执行节点或 GitLab 配置，安装脚本只提示旧容器/卷，不会删除；旧变量不再作为当前功能入口。保留原 PostgreSQL 卷和主密钥，确认备份后再清理旧服务。

## 备份与监控

在安装目录备份数据库：

```sh
docker compose --env-file .env -f compose.yaml exec -T postgres pg_dumpall -U swarmjet > backup.sql
```

同时备份 DATA_DIR（Cell 工作区、平台 omp 凭证状态、手动 PEM）、Cell 的 `swarmjet-cell-<agent-id>-data` 命名卷（CLI 会话与 HOME）和 `.env`，将主密钥另行安全保存。主密钥丢失后数据库中的 App 凭证无法解密。恢复时停止服务、恢复数据库、数据目录与 Cell 命名卷，保持绝对路径和项目名一致，再用原文件集合启动。管理员密码遗失可修改 `.env` 并重新创建 api。

监控可在原 Compose 命令追加 `--profile observability`；Prometheus 抓取 api 与 PostgreSQL exporter，默认告警仅覆盖抓取失败和数据库不可用，Alertmanager 默认不向外发送通知。

## 故障排查

| 现象 | 检查与处理 |
| --- | --- |
| GHCR `denied` / `manifest unknown` | 两个包是否公开、版本 tag 是否已发布；检查 IMAGE 与 AGENT_IMAGE。仅有源码分支不代表已有发布镜像。 |
| ARM 主机无法拉取 | 当前发布仅 linux/amd64；换 x86_64 主机或自行评估本地构建。 |
| 端口冲突 | 安装时改 `--port`；检查已有部署，避免误启动第二套相同项目。 |
| migrate 密码错误 | 已有数据卷密码与新 `.env` 不一致；恢复原配置，或用新项目名建立独立部署。 |
| api 提示缺少主密钥 | 恢复原 `SWARMJET_MASTER_KEY`；不可随意换新密钥。 |
| App 回调 / 登录失败 | PUBLIC_ORIGIN 与浏览器 origin 是否一致，SECURE_COOKIES 是否符合 HTTPS；不要用 webhook 隧道登录。 |
| webhook 不到达 | 查看 `logs -f tunnel` 和接入页状态，检查 App 安装范围、临时 URL 同步与补投错误。 |
| Cell 创建失败 | 是否启用 compose.cells.yaml、Docker socket 是否可用、Agent 镜像是否存在。 |
| 模型不可用 / 401 | 模型页凭证状态，auth-broker / auth-gateway 日志，DATA_DIR/omp Token 文件权限；确认模型 ID 有效。 |
| 模型返回 402 | 检查当前方法论预算与用量；调整方法论或等待预算窗口恢复。 |
| 第三轮升级后 Unknown model / 构建拒绝旧 CLI | 使用新版 upgrade 执行 AGENT_CLIS 与默认模型迁移。 |
| Queen 无响应 / 找不到内置模板 | 当前分支是否已合入 v0.1.0 的 Queen、模板与网页依赖；检查 Queen 开关、模型凭证与 Cell 状态。 |

部署测试与 Compose 校验可在仓库执行（示例配置只用于结构校验，不能直接部署）：

```sh
(cd deploy && python3 -m unittest)
docker compose --env-file deploy/.env.example -f deploy/compose.yaml -f deploy/compose.cells.yaml config --quiet
```
