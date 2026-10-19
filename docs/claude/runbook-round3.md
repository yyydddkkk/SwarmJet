# 第三轮验收：一条命令安装、网页接入、派第一个 Forager

本文从一台干净机器开始，用 `deploy/install.py` 完成安装，然后走完“创建 GitHub App → 连接 Claude Code → 建 Hive → Forager 开 PR → PR 评论唤醒”的闭环。第二轮的详细手动流程见 `docs/claude/runbook-round2.md`；本轮的手动 GitHub App、Temporal、Worker 步骤都不再需要。

> 默认安装会启用 Forager Cell，给 `api` 挂载宿主 Docker socket（等同 root 权限）。只在你控制、管理员可信的机器上验收。不启用 Cell 时加 `--no-cells`。

## 0. 准备

- 一台 Linux 服务器（或干净的 Linux 虚拟机），已安装 Docker Engine 24+、Docker Compose 插件 2.20+、Python 3。
- 一个**测试仓库**（例如 `acme/swarmjet-round3-demo`），不要用生产仓库。
- 能访问服务器的 GitHub App 管理权限，以及在该仓库上安装 App 的权限。
- 本地安装 Claude Code（或 Codex），并能通过 SSH 或隧道访问控制台。
- 如果 GHCR 还没发布 v* 镜像，用仓库内构建方式验收（见 1.2）。

下文用 `<INSTALL_DIR>` 表示安装目录：仓库内默认是 `deploy/`，单独运行默认是 `~/swarmjet`。命令在服务器上执行。

## 1. 安装

### 1.1 发布镜像方式

```sh
curl -fsSL https://raw.githubusercontent.com/yyydddkkk/SwarmJet/main/deploy/install.py | python3 - --yes --cells --tunnel
```

`--yes` 使用全部默认值：数据目录 `~/swarmjet/data`、端口 `8180`、启用 Cell 和隧道、拉取 GHCR `latest`。想交互选择时去掉 `--yes`。

正式发布前，新代码只在 `dev` 上；用 `SWARMJET_REF=dev` 让脚本和 Compose 文件从 `dev` 下载：

```sh
curl -fsSL https://raw.githubusercontent.com/yyydddkkk/SwarmJet/dev/deploy/install.py \
  | SWARMJET_REF=dev python3 - --yes --cells --tunnel
```

`dev` 还没有 GHCR 发布镜像时，改用下面的仓库内构建方式。发布后把 URL 和 `SWARMJET_REF` 换回 `main`。

### 1.2 仓库内构建方式

```sh
git clone https://github.com/yyydddkkk/SwarmJet.git
cd SwarmJet
python3 deploy/install.py --yes --cells --tunnel --image-source repo
```

首次构建会下载 Go/Node 基础镜像并编译前端，耗时较长。

### 1.3 安装结果检查

预期最后几行类似（密码默认不显示）：

```text
SwarmJet 安装完成。
控制台：https://<随机>.trycloudflare.com
管理员：admin
密码：保存在 /home/<user>/swarmjet/.env 的 ADMIN_PASSWORD（加 --show-password 可显示）
下一步：打开控制台的“接入”页，一键创建 GitHub App；再为 Claude Code / Codex 创建连接。
数据目录：/home/<user>/swarmjet/data；SWARMJET_MASTER_KEY 请单独备份。
隧道地址：https://<随机>.trycloudflare.com（临时地址，重启后可能变化）
```

检查配置权限和服务：

```sh
cd <INSTALL_DIR>
stat -c '%a %n' .env          # 600
docker compose --env-file .env -f compose.yaml -f compose.cells.yaml ps
curl -fsS http://127.0.0.1:8180/healthz
docker compose --env-file .env -f compose.yaml -f compose.cells.yaml logs --tail=50 api
```

预期只有 `postgres`、`migrate`（已退出）、`api` 三个基础服务，加上 `tunnel`；`api` 为 `healthy`，日志里有 `forager cells enabled`。`GITHUB_APP_ID` 未配置时不应启动失败。

确认 `migrate` 已成功退出：

```sh
docker compose --env-file .env -f compose.yaml ps -a migrate
```

## 2. 登录控制台

- 有公网域名/隧道：打开安装脚本打印的地址。
- 仅本机：`ssh -N -L 8180:127.0.0.1:8180 用户名@服务器`，打开 <http://localhost:8180>。

用 `admin` 和 `.env` 里的 `ADMIN_PASSWORD` 登录。预期直接进入 Hive 控制台；顶部只有“Hive”“接入”“CLI 登录”。

`curl` 直接检查（可选，先登录拿 Cookie）：

```sh
curl -fsS -c /tmp/sj-cookies -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<ADMIN_PASSWORD>"}' \
  http://127.0.0.1:8180/api/login
curl -fsS -b /tmp/sj-cookies http://127.0.0.1:8180/api/setup/status
```

`/api/setup/status` 的 `github.configured` 初始为 `false`，`cells.enabled` 为 `true`，`tunnel.enabled` 为 `true`，`cliLogin.available` 在 Docker 可用时为 `true`。

## 3. 一键创建 GitHub App

1. 打开 **接入** 页。
2. 在 GitHub 区块点击“一键创建 GitHub App”；组织仓库可在“组织名”里填写组织，留空则创建在个人账号下。
3. GitHub 打开确认页，App 名称形如 `swarmjet-<随机串>`。确认后 GitHub 回调 `/#/setup?github=connected`。
4. 回到接入页，应显示 App 名称、HTML 链接、安装链接和安装数量。
5. 点击安装链接，把 App 安装到测试仓库（或包含测试仓库的账号）。

检查：

```sh
curl -fsS -b /tmp/sj-cookies http://127.0.0.1:8180/api/setup/status | python3 -m json.tool
```

预期 `github.configured=true`、`source="database"`、`appSlug` 非空、`installations>=1`。如果 `.env` 里设置了 `GITHUB_APP_*`，`source` 会是 `env`，且环境变量优先。

如果回调失败，确认：

- `PUBLIC_ORIGIN` 与浏览器实际地址一致（隧道安装时脚本已自动写入隧道地址）。
- `SECURE_COOKIES` 与实际协议一致（HTTPS 时设 `true`）。
- 浏览器仍保留管理员会话。

## 4. 确认隧道与 webhook

隧道安装时，接入页的“Webhook 隧道”区块应显示：

- 启用：是
- 公网地址：`https://<随机>.trycloudflare.com`
- webhook 已同步：是（创建 App 后几十秒内）
- 最近错误：空

检查 cloudflared 和平台日志：

```sh
cd <INSTALL_DIR>
curl -fsS http://127.0.0.1:2000/quicktunnel
docker compose --env-file .env -f compose.yaml --profile tunnel logs --tail=50 tunnel
docker compose --env-file .env -f compose.yaml logs --tail=100 api | grep -E 'tunnel|webhook'
```

在 GitHub 上打开 App 设置，确认 webhook URL 是 `https://<隧道地址>/api/webhooks/github`，Content type 为 `application/json`，事件包含 Pull request、Pull request review、Pull request review comment、Issue comment、Issues、Check run、Status。

没有启用隧道时，手动把 webhook 指向上面的 `/api/webhooks/github`；`/api/setup/status` 的 `tunnel.enabled` 会是 `false`。

## 5. 连接本地 Claude Code（或 Codex）

1. 在接入页点击“新建连接”，名称例如 `cli-local`，作用域选 `default`。
2. 创建后一次性显示明文，复制 Claude Code 的配置，或直接点“复制命令”。
3. 在本地执行（示例）：

```sh
claude mcp add --transport http swarmjet http://127.0.0.1:8180/mcp \
  --header "Authorization: Bearer <TOKEN>"
```

4. 重启 Claude Code，确认能看到 `create_hive`、`spawn_forager`、`send`、`wait`、`get_forager`、`cancel_forager`。

也可以在服务器上生成配置：

```sh
cd <INSTALL_DIR>
python3 install.py connect --client claude
python3 install.py connect --client codex
python3 install.py connect --client json
```

`connect` 会新建一个 default 作用域的 Token；明文只打印一次。关闭接入页对话框后无法再次查看，只能吊销后重建。Token 列表里不显示明文，也不显示 Forager 自动签发的 agent 作用域 Token。

## 6. 创建 Hive 并派第一个 Forager

在本地 Claude Code 中依次调用：

1. `create_hive`

```json
{
  "name": "Round 3 demo",
  "repository": "acme/swarmjet-round3-demo",
  "config": { "cli": "opencode" }
}
```

记下返回的 `hive.id` 为 `<HIVE_ID>`。新建 Hive 时仓库可以从 App 已安装仓库里选择，也可以手动输入 `owner/name`。

2. `spawn_forager`

```json
{
  "hiveId": "<HIVE_ID>",
  "task": "在 /workspace 新建 round3-demo.txt，内容写 HONEY；然后运行 git add -A 和 git -c user.name=SwarmJet -c user.email=forager@swarmjet.invalid commit -m round3-demo，最后用一句话总结。不要推送，不要创建 PR，平台会发布。"
}
```

记下 `forager.id` 和 `branch`。

观察：

```sh
cd <INSTALL_DIR>
docker ps --filter 'label=swarmjet.cell=true'
docker compose --env-file .env -f compose.yaml -f compose.cells.yaml logs -f api | grep -E 'cell update|forager|cell:'
```

也可以从本地 Agent 调 `get_forager`，状态应从 `creating` 到 `active`，最后到 `parked`；`wait` 应收到一条 `kind=report` 的汇报。

## 7. 在 GitHub 上确认 PR

预期测试仓库出现新分支（通常形如 `swarm/<hive>/<forager>`），以及一个指向默认分支的 PR：

- 标题取自任务第一行；
- 描述来自 Forager 的交接摘要或最后输出；
- 平台自己推送分支/开 PR 引起的事件不会唤醒该 Forager（日志中应有一条 ignored 记录，原因为平台自身事件）。

如果只有分支没有 PR，检查日志中的 `cell: create pull request`、`GitHub App API returned HTTP ...` 和 App 的 Contents/Pull requests 权限。

## 8. PR 评论回到 Forager

在刚打开的 PR 上留一条 review 评论：

```text
Please add a second line to round3-demo.txt and commit it.
```

GitHub 会发送 `pull_request_review` / `pull_request_review_comment` webhook。Router 按 PR 编号找到 Forager，把它写成 `kind=event` 的消息并唤醒同一 Forager。

观察：

```sh
docker compose --env-file .env -f compose.yaml -f compose.cells.yaml logs -f api | grep -E 'hive event routed|forager|cell update'
```

预期 `hive event routed` 的 `forager_id` 等于第 6 步的 `<FORAGER_ID>`，`action=delivered`。Forager 状态再次 `active → parked`，`get_forager` 能看到 PR 编号和更新后的交接摘要。

## 9. 取消与清理

在本地 Agent 调 `cancel_forager`：

```json
{ "agentId": "<FORAGER_ID>" }
```

预期：

- 监管者收到 `kind=control`、正文 `cancel`；
- 当前轮被打断，状态进入 `cancelled`；
- Cell 容器被删除，工作区和命名卷被清理；
- 该 Forager 的 Agent Token 被吊销（再用该 Token 访问 `/mcp` 返回 401）。

服务器确认：

```sh
docker ps -a --filter 'label=swarmjet.cell=true'
docker volume ls | grep swarmjet-cell
```

必要时手工清理测试仓库上的分支和 PR；不要删除生产数据卷。

## 10. 升级、停止与卸载

升级前先备份（见 `deploy/README.md`）：

```sh
cd <INSTALL_DIR>
python3 install.py upgrade
python3 install.py status
```

停止但保留数据：

```sh
python3 install.py stop
```

删除容器但保留数据卷和 `DATA_DIR`：

```sh
python3 install.py uninstall
```

彻底删除（二次确认；`--yes` 可跳过交互，适合自动化）：

```sh
python3 install.py uninstall --purge
```

## 常见问题

| 现象 | 检查 |
|---|---|
| 安装脚本提示端口被占用 | `ss -ltnp \| grep 8180`；换 `--port`，或确认不是残留容器 |
| `api` 启动即退出，提示缺少 `SWARMJET_MASTER_KEY` | `.env` 里必须是 64 位十六进制；不要留空 |
| 接入页创建 App 后回调失败 | `PUBLIC_ORIGIN` 是否与浏览器地址一致；`SECURE_COOKIES` 是否匹配协议；state 是否超过 10 分钟 |
| 隧道没有地址 | `docker compose --profile tunnel logs tunnel`；`SWARMJET_TUNNEL_URL` 是否为 `http://tunnel:2000` |
| webhook 已同步仍收不到事件 | GitHub 上 App 的 webhook URL、secret、事件订阅；Router 日志中的签名错误 |
| MCP 连不上 | Token 是否有效、URL 是否为 `/mcp`、SSH 隧道/公网入口是否可达 |
| Cell 创建失败 | 是否叠加 `compose.cells.yaml`；`docker info` 是否正常；`AGENT_IMAGE` 是否包含 `opencode` |
| 分支没有推送/没有 PR | App 的 Contents/Pull requests 权限；仓库默认分支；日志中是否有 `refusing to publish the base branch` |
| PR 评论没有唤醒 Forager | Forager 是否仍是终态、是否已记录 PR 编号；`pull_request_review` 事件是否订阅 |
| GHCR 镜像拉取失败 | 镜像只在 `v*.*.*` 标签发布；改用 `--image-source repo` 本地构建，或先 `docker login ghcr.io` |
