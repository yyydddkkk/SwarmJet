# SwarmJet

SwarmJet 是一个可自托管的云端 Agent 集群。一个 **Hive** 绑定一个 GitHub 仓库；你在本地用 Claude Code 或 Codex 通过 MCP 派活；**Forager** 在长期 Docker **Cell** 里修改代码、提交并开 PR；PR 上的评论经 **Router** 回到 Forager，继续下一轮。可选的 **Queen** 维护 Hive 的长期上下文。

## 快速开始

需要 Linux、Docker Engine 24+、Docker Compose 插件 2.20+、Python 3。

```sh
curl -fsSL https://raw.githubusercontent.com/yyydddkkk/SwarmJet/main/deploy/install.py | python3 -
```

在仓库里也可以本地构建：

```sh
python3 deploy/install.py
```

脚本会生成配置和密钥、检查端口、构建或拉取镜像、执行迁移并等待健康检查。完成后打开：

- 本机：<http://localhost:8180>
- 服务器：先执行 `ssh -N -L 8180:127.0.0.1:8180 用户名@服务器`，再打开 <http://localhost:8180>

管理员用户名默认是 `admin`，密码保存在服务器的 `deploy/.env`（或安装目录的 `.env`）中的 `ADMIN_PASSWORD`。安装时加 `--show-password` 可直接显示。

首次登录后：

1. 打开 **接入** 页，点击“一键创建 GitHub App”，在 GitHub 上确认并把 App 安装到目标仓库。
2. 仍在 **接入** 页新建连接，复制 Claude Code 或 Codex 的 MCP 配置；也可以运行 `python3 deploy/install.py connect --client claude`。
3. 新建 Hive，选择仓库，派第一个 Forager。它会创建分支并开 PR。
4. 在 PR 上留下评论或 review，事件会回到同一个 Forager；也可以在本地 Agent 里用 `wait` 等汇报。

默认启用 Forager Cell（需要给 `api` 挂 Docker socket，等同宿主 root 权限）；不需要时安装时选“不启用 Cell”。没有公网域名时安装脚本可以启用 Cloudflare 临时隧道，并自动同步 GitHub webhook 地址。详见[部署指南](deploy/README.md)。

## 它怎么工作

- **Hive**：一个仓库、一套配置和一个长期上下文。
- **Queen**：可选的常驻 Agent，维护记忆、整理情境、发起工作。
- **Forager**：被派活的一次性 Agent；在 Cell 里执行、提交、发布分支并开 PR，PR 事件会让它继续干活。
- **Cell**：持久 Docker 容器和可写工作区；CLI 以非 root 用户运行。
- **Router**：把 GitHub webhook 结构化地投递给正确的 Forager，忽略平台自己引起的事件。
- **MCP**：本地 Agent 和 Forager 共用的工具接口，包括 `create_hive`、`spawn_forager`、`send`、`wait`、`get_forager`、`cancel_forager`。

默认只有 `postgres`、`migrate`、`api` 三个服务；River 队列和 Cell 监管者都在 `serve` 进程里。没有 Temporal、独立 Worker、执行节点或 GitLab 接入。

## 文档

| 主题 | 文档 |
| --- | --- |
| 安装、服务、环境变量、Cell 安全、隧道、升级、备份、卸载 | [部署指南](deploy/README.md) |
| 第三轮从零验收步骤 | [第三轮 Runbook](docs/claude/runbook-round3.md) |
| 架构决策 | [ADR](docs/claude/adr-hive-queen-forager.md) |
| 接口契约 | [API 契约](api/contract.md) |
| 贡献方式 | [CONTRIBUTING.md](CONTRIBUTING.md) |

## 开发

需要 Go 1.27+、Node.js 24+ 和 Docker。后端检查：

```sh
go test -race ./cmd/... ./internal/...
go vet ./cmd/... ./internal/...
```

前端：

```sh
npm ci --prefix web
npm run build --prefix web
```

部署测试不启动 Docker：

```sh
cd deploy && python3 -m unittest
```

完整的本地检查入口是 `./scripts/test.sh`；`VALIDATE_DEPLOY=1` 会额外校验 Compose 配置。

## 许可证

本项目以 [MIT 许可证](LICENSE) 发布。
