# SwarmJet

SwarmJet 是一个可自托管的 AI 开发任务平台。它从 GitLab 或 GitHub.com 的 Issue 接收任务，让 Agent 在独立工作区修改一个或多个仓库，并通过验证、审查和 MR/PR 交付改动。管理员和团队成员可以在 Web 控制台配置项目、查看执行记录并处理需要人工介入的步骤。

## 功能概览

- **跨仓库任务**：一次任务可修改多个仓库，各仓库独立指定目标分支。
- **可恢复的执行流程**：使用 Temporal 编排开发、验证、审查和合并；任务状态和操作记录持久化。
- **可选的 Agent 与检查**：支持 OpenCode、Codex、DeepSeek Harness TUI（dst）、Antigravity（agy）和 Claude Code；项目可配置独立审查、额外验收与自动合并。
- **独立执行环境**：每轮任务拥有私有 Git 工作区和角色会话；可在单机运行，也可分配给独立执行节点。
- **团队管理**：提供成员与项目权限、连接管理、执行节点管理和人工操作入口。

平台负责协调任务和记录结果；仓库、Issue、MR/PR、CI 与分支规则仍由 GitLab 或 GitHub 管理。默认部署方式是单机 Docker Compose。

## 快速开始

在 **Linux** 主机上准备 Docker Engine、Docker Compose 插件、Git、Python 3、OpenSSL 和 `ip` 命令。获取源码后，在仓库根目录运行：

```bash
sudo python3 deploy/install.py
```

安装脚本生成部署配置和密钥、构建镜像、初始化数据库并启动服务。默认控制台只监听服务器本机；从自己的电脑通过 SSH 隧道访问：

```bash
ssh -N -L 8180:127.0.0.1:8180 用户名@服务器
```

然后打开 <http://localhost:8180>。管理员用户名默认为 `admin`，密码保存在服务器 `deploy/.env` 的 `ADMIN_PASSWORD` 中。

首次登录后，在控制台启用已安装的 Agent、配置模型认证、添加 GitLab 或 GitHub 项目，并完成项目初始化。项目验证通过且启用后才会接收任务。GitLab 的逐步接入流程见[项目快速接入](docs/getting-started.md)；GitHub App 的配置见[部署指南](deploy/README.md#githubcom-app-接入)。更多安装选项、备份与升级说明也在[部署指南](deploy/README.md)。

## 任务如何运行

1. 项目启用相应触发策略后，在已登记仓库的 Issue 上添加 `autonomous` 标签，启动第一轮任务。
2. SwarmJet 固定本轮的项目配置、仓库基线和执行节点，在独立工作区运行选定的 Agent。
3. Agent 提交改动并创建 MR/PR；按项目配置执行额外验收和独立审查。
4. 检查通过后自动合并，或等待团队在托管平台人工合并。来源 Issue 保持打开，并附上按提交版本生成的人工复核指引。

需要继续开发时，在 Issue 中发布以 `/swarmjet run` 开头的评论，启动下一轮。运行中的新命令会排队，不改动当前轮已审查的提交集合。自动接单还受项目策略和成员权限约束；GitHub 项目默认关闭自动接单，需要管理员逐项目开启。

## 本地开发

需要 Go 1.25+、Node.js、npm、Git、`meta` 和可访问的 Docker daemon。前端依赖安装后，通过仓库提供的开发启动器运行：

```bash
npm ci --prefix web
./scripts/dev start
```

首次启动前需要准备 `deploy/.env`、PostgreSQL、Temporal 和 Agent 镜像。完整步骤、默认端口及常见问题见[本地开发指南](docs/development.md)。

运行主要检查：

```bash
./scripts/test.sh
```

部署配置检查和浏览器测试可分别通过 `VALIDATE_DEPLOY=1`、`RUN_BROWSER_TESTS=1` 启用；集成测试的环境要求见[本地开发指南](docs/development.md)。

## 文档

| 主题 | 文档 |
| --- | --- |
| 单机安装、GitHub App、执行节点、升级与运维 | [部署指南](deploy/README.md) |
| GitLab 项目接入与任务验收策略 | [项目快速接入](docs/getting-started.md) |
| 成员权限、Agent 选择与任务规则 | [使用指南](docs/user-guide.md) |
| 开发环境与测试 | [本地开发指南](docs/development.md) |
| 系统架构与执行流程 | [架构说明](docs/architecture.md) · [架构图](docs/architecture-diagram.md) |
| 接口契约 | [API 契约](api/contract.md) |
| 贡献方式 | [CONTRIBUTING.md](CONTRIBUTING.md) |

## 贡献与许可证

欢迎按[贡献指南](CONTRIBUTING.md)提交改动。本项目以 [MIT 许可证](LICENSE) 发布。
