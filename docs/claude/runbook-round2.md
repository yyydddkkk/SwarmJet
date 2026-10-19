# 第二轮手动验收：Forager 在 Cell 里干活并开 PR

本文验收第二轮的最小闭环：本地 Agent 通过 MCP 派活 → Forager 在 Docker Cell 里由 OpenCode（ACP）执行 → 平台把分支推上 GitHub 并开 PR → PR 上的事件经 Router 回到 Forager。

> 本轮只支持单机部署。serve 进程会挂载宿主 Docker socket，拥有宿主机 root 级权限。只在你自己控制、管理员可信的服务器上启用。

## 0. 准备

- 一台可用的 Linux 服务器，已按 `deploy/README.md` 完成部署，`api`、`worker`、PostgreSQL 和 Temporal 正常运行。
- 服务器上已构建或拉取 `AGENT_IMAGE`（默认 `swarmjet-agent:local`），镜像内包含 `opencode`。
- 一个**测试仓库**（不要用生产仓库验收）。下面用 `acme/swarmjet-round2-demo` 表示；请替换成你自己的 `owner/name`。
- 能访问服务器的 GitHub App 管理权限，以及在该仓库上创建 webhook 的权限。
- 本地已安装 Claude Code（或 Codex），并能通过 SSH 隧道访问服务器。

下文命令都在服务器的 `deploy/` 目录执行；本地命令会特别标注。

## 1. 配置 GitHub App

1. 在 GitHub 创建安装型 App（**Settings → Developer settings → GitHub Apps → New GitHub App**）。
2. 仓库权限至少设置：
   - Contents: Read and write（克隆、推送 Forager 分支）
   - Pull requests: Read and write（开/更新 PR）
   - Metadata: Read-only（必需）
   - Issues: Read and write（如果还要处理 Issue 事件）
3. 安装到测试仓库，或安装到包含测试仓库的账号。一个 App 可以被多个仓库使用；本轮只按仓库名解析安装。
4. 生成私钥，把 PEM 放到 `DATA_DIR` 下，例如 `/srv/swarmjet/data/github-app-key.pem`，权限 `0600`。不要提交到仓库，也不要放进 agent 镜像。
5. 在 `deploy/.env` 设置（不要把私钥内容贴进变量）：

```sh
GITHUB_APP_ID=<APP_ID>
GITHUB_APP_PRIVATE_KEY_FILE=/srv/swarmjet/data/github-app-key.pem
GITHUB_WEBHOOK_SECRET=<RANDOM_WEBHOOK_SECRET>
```

`GITHUB_APP_PRIVATE_KEY_FILE` 必须是**容器内也存在的同一绝对路径**；compose 把宿主机 `DATA_DIR` 以相同路径挂进 serve。

## 2. 配置 webhook

在 GitHub App 或测试仓库的 webhook 设置中：

- Payload URL：`https://<你的域名>/api/webhooks/github`
  - 没有公网入口时，可先用 SSH 隧道 + 本地转发做联调；GitHub 必须能访问到该 URL。
- Content type：`application/json`
- Secret：与 `GITHUB_WEBHOOK_SECRET` 相同
- 订阅事件至少：**Pull request**、**Pull request review**、**Pull request review comment**、**Issue comment**、**Issues**、**Check run**、**Status**。

webhook 只负责唤醒 Router；Router 按仓库找到 Hive 后，把事件写成 Inbox 消息。没有 Hive 的仓库仍走旧扫描逻辑。

## 3. 启用 Cells

在 `deploy/.env` 增加：

```sh
SWARMJET_CELLS=1
# 沿用 AGENT_IMAGE；如镜像名不同请改成实际值
AGENT_IMAGE=swarmjet-agent:local
# Forager 模型；默认是免费模型
SWARMJET_FORAGER_MODEL=opencode/big-pickle
# Cell 内访问 serve 的 MCP；compose 中 api 有 swarmjet 别名
SWARMJET_CELL_MCP_URL=http://swarmjet:8080/mcp
# Cell 加入的 Docker 网络；默认 <COMPOSE_PROJECT_NAME>_default
SWARMJET_CELL_NETWORK=swarmjet_default
# 可选：OpenCode auth.json 的内容；只传给 Cell 内的 CLI
OPENCODE_AUTH_CONTENT=
```

然后重启 serve：

```sh
cd deploy
docker compose up -d --wait api
docker compose logs --tail=50 api | grep -E 'forager cells enabled|GitHub App'
```

如果 `GITHUB_APP_ID` / `GITHUB_APP_PRIVATE_KEY_FILE` 没有配置，serve 会在启动时报错退出，这是预期行为：不要在发布时才失败。

> 安全提醒：从这里开始，`api` 容器挂载了 `/var/run/docker.sock`，可以创建/删除宿主容器并读写宿主路径。不要把 Docker socket 再挂进任何 agent 容器；MCP Token 只发给可信的本地 Agent。

## 4. 创建管理员 Token

```sh
cd deploy
docker compose exec -T api swarmjet token create --name round2-admin --scope admin
```

命令只打印一次明文。把它保存到本地密码管理器；数据库只保存哈希。

## 5. 在本地 Claude Code 配置 MCP

先建 SSH 隧道（本地执行）：

```sh
ssh -N -L 8180:127.0.0.1:8180 <用户名>@<服务器>
```

在本地项目的 `.mcp.json` 或 `~/.claude.json` 加入（Token 用占位符，不要提交）：

```json
{
  "mcpServers": {
    "swarmjet": {
      "type": "http",
      "url": "http://127.0.0.1:8180/mcp",
      "headers": { "Authorization": "Bearer <ADMIN_TOKEN>" }
    }
  }
}
```

Codex 的等价配置见 `deploy/README.md` 的“给本地 Claude Code / Codex 配置 MCP”。

重启本地 Agent，确认能看到 `create_hive`、`spawn_forager`、`wait` 等工具。

## 6. 建 Hive 并派活

在 Claude Code 里依次让 Agent 调用 MCP 工具（或直接手工调用等价的 MCP 请求）：

1. `create_hive`

```json
{
  "name": "Round 2 demo",
  "repository": "acme/swarmjet-round2-demo",
  "config": { "cli": "opencode" }
}
```

返回的 `hive.id` 记为 `<HIVE_ID>`。

2. `spawn_forager`

```json
{
  "hiveId": "<HIVE_ID>",
  "task": "在 /workspace 新建 round2-demo.txt，内容写 BUMBLEBEE；然后运行 git add -A 和 git -c user.name=SwarmJet -c user.email=forager@swarmjet.invalid commit -m round2-demo，最后用一句话总结。不要推送，不要创建 PR，平台会发布。"
}
```

记下返回的 `forager.id` 和 `branch`。

3. 观察执行：

```sh
# 服务器执行
docker compose logs -f api | grep -E 'cell update|forager|cell:'
docker ps --filter 'label=swarmjet.cell=true'
```

也可以从本地 Agent 调 `get_forager`，状态应从 `creating` 到 `active`，最后到 `parked`。`wait` 应收到一条 `kind=report` 的汇报（发给 `local:<token-id>`）。

## 7. 在 GitHub 上确认 PR

发布成功后：

- 测试仓库应出现新分支（`branch` 字段的值，通常形如 `swarm/<hive>/<forager>`）。
- 应出现一个指向默认分支的 PR，标题取自任务第一行。
- PR 描述来自 Forager 的交接摘要或最后输出。

如果只有分支没有 PR，检查 serve 日志中的 `cell: create pull request`、`GitHub App API returned HTTP ...` 和 App 的 Pull requests 权限。

## 8. 在 PR 上评论，确认事件回到 Forager

在刚打开的 PR 上留一条 review 评论，例如：

```text
Please add a second line to round2-demo.txt and commit it.
```

GitHub 会发送 `pull_request_review` / `pull_request_review_comment` webhook。Router 按 PR 编号找到 Forager，把它写成 `kind=event` 的消息；监管者唤醒同一 Forager 的下一轮。

观察：

```sh
docker compose logs -f api | grep -E 'hive event routed|forager|cell update'
```

预期日志中 `hive event routed` 的 `forager_id` 等于第 6 步的 `<FORAGER_ID>`，`action=delivered`。Forager 状态会再次 `active → parked`，`get_forager` 能看到 PR 编号和更新后的交接摘要。

## 9. 取消与清理

在本地 Agent 调 `cancel_forager`：

```json
{ "agentId": "<FORAGER_ID>" }
```

预期：

- 监管者收到 `kind=control`、正文 `cancel`；
- 当前轮被打断（如有），状态进入 `cancelled`；
- Cell 容器被删除，工作区和命名卷被清理；
- 该 Forager 的 Agent Token 被吊销（再次用该 Token 访问 `/mcp` 应返回 401）。

服务器上确认：

```sh
docker ps -a --filter 'label=swarmjet.cell=true'
docker volume ls | grep swarmjet-cell
```

必要时手工清理测试仓库上的分支和 PR；不要删除生产数据卷。

## 10. 关闭 Cells

验收完成后，把 `deploy/.env` 的 `SWARMJET_CELLS` 改回 `0` 并重启 `api`。关闭后消息仍会写入数据库，但没有进程执行；现有 Cell 容器不会被自动删除，可按 `swarmjet.cell=true` 标签排查。

## 常见问题

| 现象 | 检查 |
|---|---|
| serve 启动即失败，提示需要 GitHub App | 配置 `GITHUB_APP_ID`、`GITHUB_APP_PRIVATE_KEY_FILE`；PEM 必须可读且是 RSA 私钥。 |
| Cell 创建成功但 MCP 连不上 | `SWARMJET_CELL_NETWORK` 是否等于 compose 默认网络名；`SWARMJET_CELL_MCP_URL` 是否用 `http://swarmjet:8080/mcp`。 |
| Cell 内没有模型输出 | 确认镜像包含 `opencode`；`SWARMJET_FORAGER_MODEL` 是 `provider/model`；需要认证时设置 `OPENCODE_AUTH_CONTENT`。 |
| 分支没有推送/没有 PR | 检查测试仓库默认分支、App 的 Contents/Pull requests 权限，以及日志中是否有 `refusing to publish the base branch`。 |
| webhook 没有路由 | 确认 Hive 的 `repository` 与 webhook 的 `repository.full_name` 完全一致；检查 webhook 签名密钥。 |
| PR 评论没有唤醒 Forager | 确认 Forager 仍是非终态且已记录 PR 编号（`get_forager` 的 `prNumber`）；检查 `pull_request_review` 事件是否订阅。 |
| 端口/网络受限 | 本轮只支持单机 Docker；Cell 必须能访问 serve 的 `/mcp`，测试环境可临时把 `SWARMJET_CELL_NETWORK` 设为 `host` 并用 `http://127.0.0.1:<port>/mcp`。 |

自动化验收命令见根目录的 `scripts/test.sh`；真实 Docker + 免费模型的可选测试用 `SWARMJET_ROUND2_E2E=1` 打开，默认跳过。
