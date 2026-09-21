# SwarmJet

可自托管的 AI 开发任务平台，支持并发任务和跨仓库自动化。当前提供 GitLab + OpenCode 集成，默认通过单机 Docker Compose 部署；尚不支持跨宿主机调度 Agent 容器。一个开发 Agent 在独立工作区内修改一组仓库，完成联调后由独立审查 Agent 审查，并自动合并到**各仓库配置的目标分支**。Go + Temporal + PostgreSQL 管理可恢复流程，React 控制台提供项目配置、执行记录及人工操作。

## 架构

```mermaid
flowchart LR
  GitLab[GitLab Issue / MR] --> Ingress[Webhook + 轮询]
  Console[Web 控制台] --> API[Go 管理 API]
  Ingress --> Outbox[PostgreSQL 任务与命令]
  API --> Outbox
  Outbox --> Temporal[Temporal Server]
  Temporal <--> Workflow[Go Workflow Worker]
  Temporal --> Activity[Go Activity Worker]
  Activity --> Workspace[meta + 任务私有 Git 副本]
  Activity --> Agents[OpenCode 开发 / 审查 / 合并]
  Activity --> Tests[隔离 Compose 联调]
  Agents --> GitLab
  Activity --> Reports[报告与执行记录]
  Reports --> Console
```

Temporal 是运行流程的事实源；业务 PostgreSQL 保存配置、查询投影、操作记录和审计。GitLab 是分支、MR、CI 和合并结果的事实源。控制台操作先写持久化命令，再交给 Workflow，重复命令按 ID 去重。

## 工作流程

1. 登记仓库的 opened Issue 添加 `autonomous` 标签，Webhook 或轮询发现任务。
2. 等待明确的 Issue 阻塞依赖；普通关联关系不阻塞。
3. 固定配置与仓库基线，在长期缓存外建立任务私有源仓库和 meta worktree 集合。
4. 一个开发 Agent 跨仓库实现、补充测试并提交；Worker 执行联调基线及必需测试。
5. 开发 Agent 推送任务分支并创建 MR，独立审查 Agent 检查整组改动。
6. 审查失败有限次返工。迁移、CI、部署配置和 `no-auto-close` 需要人工审核，批准绑定提交集合。
7. 审查 Agent 顺序合并到配置目标分支。允许中间版本暂时不兼容，部分合并保留记录并核实后恢复。
8. 全部完成后清理任务工作区与联调环境，统一关闭 Issue。

需求歧义记录假设、添加 `needs-clarification` 并**继续执行**。补充信息在后续阶段或会话恢复时注入，只有具体障碍无法解决才 blocked。提交/MR 只引用来源 Issue，不使用提前关单的 `Closes/Fixes/Resolves` 指令。

## 项目接入

普通用户不需要理解 Meta-Repo。新建项目时填写 GitLab 地址和 Access Token，验证后搜索并选择子仓库、确认目标分支，随后在页面内嵌终端完成 `opencode auth login` 并选择模型做一次实际调用验证。系统据此创建受管的内部 Meta-Repo，运行初始化 Agent 分析项目，并在独立副本中执行验证。GitLab 连接加密保存（`SWARMJET_MASTER_KEY`）并可跨项目复用；Token 不回显，也不写入日志、URL、任务快照或 Temporal 历史。模型和验收未就绪时项目保持草稿，不接单；初始化未通过验证时也不会自动启用。

控制台的“执行器环境”提供持久化配置终端，可执行 `opencode auth login`，也可使用 `codex login --device-auth` 预先配置 Codex。当前任务执行仅支持 OpenCode；Codex 登录入口不代表已经接入 Codex 任务适配器。终端登录配置保存在独立 profile 中，任务使用私有副本，开发与审查不共享会话。

自动合并是项目级开关，新项目默认关闭：关闭时系统仍会开发、验证、创建 MR 和审查，但只在全部 MR 被人工合并后才收尾关单。

“导入已有 Meta-Repo”仍是高级入口。此时填写 Meta-Repo URL/ref 并读取清单，仓库列表与 Meta-Repo 的 `.meta.yaml`、`.meta.yml` 或 `.meta` 校验一致：

```yaml
projects:
  backend:
    repo: https://gitlab.example.com/team/backend.git
  client:
    repo: https://gitlab.example.com/team/client.git
    depends_on: [backend]
```

各仓库独立配置 `targetBranch`。不存在时须提供 `createFrom`，从明确起点初始化一次。配置修改默认仅影响新任务。Meta-Repo 自身需要改码时，将它登记为 `path: "."` 的仓库，目标分支与 `metaRef` 对齐。

第一版适配器为 `opencode`，开发和审查分别选择模型。模型填写实际可用的 `provider/model`，其他尚未实现的执行器会被明确拒绝。GitLab 凭证可以来自页面保存的连接，也可以来自部署环境变量；项目与任务快照只保存引用。

联调至少包含一条 required 检查。Compose 使用任务私有网络和卷，不使用固定容器名、共享外部卷、Docker socket、宿主机网络或固定宿主机端口。Worker 凭证不会整体传入联调服务。

公共 HTTP 联调示例见 [examples/http-check/README.md](examples/http-check/README.md)，可直接验证服务启动及必需检查，不依赖公司项目。

## 本地开发（推荐）

前端、API 和 Worker 直接在宿主机运行，修改页面由 Vite 热更新；Go 代码修改后重启本地进程即可，不构建应用镜像。`deploy/` 中的 Dockerfile 和 Compose 用于部署；现阶段 Agent、联调验收仍通过容器隔离执行。

Linux 开发环境需要 Python 3、Node.js、Go 1.25+、Git、`meta` 和可访问的 Docker daemon。先执行 `npm ci --prefix web`。从 `deploy/.env.example` 创建本项目独立的 `deploy/.env`，设置数据库凭据、`DATA_DIR`、执行器配置和 Temporal namespace；可复制 `deploy/.env.development.example` 为 `deploy/.env.development` 覆盖本机工具路径和端口。dotenv 内容作为数据解析，不作为 shell 脚本执行，文件不可提交。

PostgreSQL、Temporal 使用 SwarmJet 自己的 Compose 容器。启动脚本会动态查询 Compose 网络中的地址，不重建依赖、不清空数据；若使用其他数据库或 Temporal，在 `deploy/.env.development` 显式填写地址。首次准备依赖可执行 `(cd deploy && docker compose up -d postgres temporal-namespace temporal-ui)`，不必启动容器版 API、Worker。全新数据库需先在仓库根目录执行 `(cd deploy && docker compose -f compose.yaml -f compose.build.yaml run --build --rm migrate)`；开发启动不会隐式迁移数据库。Agent 镜像的构建见部署文档。

```bash
./scripts/dev.py start    # 编译 Go，检查依赖，停止旧容器版 API/Worker，启动本地三进程
./scripts/dev.py status
./scripts/dev.py restart  # Go 代码修改后重新编译并重启；不构建镜像
./scripts/dev.py stop     # 只停止本地进程，保留依赖和数据
```

开发页面默认 **http://localhost:5273**，API 为 `127.0.0.1:8180`，Worker 管理接口为 `127.0.0.1:8191`。Vite 代理 API、事件流和终端 WebSocket；开发环境的登录 Origin 使用 5273。进程日志位于 `.cache/dev/`，不提交到 Git。启动后项目状态与任务记录保存在 SwarmJet 数据库，已阻塞的任务不会被脚本自动重试。

启动脚本会为宿主机 Git 设置仓库内的 `scripts/askpass.sh`；手动启动 Worker 时可用 `SWARMJET_GIT_ASKPASS` 指定该绝对路径。Agent 容器继续使用镜像内的凭据助手。

从容器切换时，先停止旧 API、Worker 及执行器配置终端，再将 `DATA_DIR` 中旧的 root 所有文件交给本机运行用户（不要更改权限为全员可读）。之后 Agent 容器以 Worker 的 UID/GID 写入任务文件，私有 HOME 位于任务目录。保持 `DATA_DIR` 的绝对路径不变，否则旧任务工作区、凭据和日志无法复用。不要同时运行容器版与本地版应用进程。

## 开发验证

`./scripts/test.sh` 默认只使用本地工具链。需要检查部署 Compose 时显式设置 `VALIDATE_DEPLOY=1`；浏览器测试用 `RUN_BROWSER_TESTS=1` 启用。

```bash
go test -race ./cmd/... ./internal/...
cd web
npm ci
npm run build
npm test
```

可选集成测试使用专用数据库，测试自行创建临时 schema：

```bash
TEST_DATABASE_URL='postgres://postgres:test@localhost:5432/swarmjet_test?sslmode=disable' go test -race ./internal/store ./internal/api
SWARMJET_DOCKER_TEST=1 go test ./internal/integration
```

测试包括 Temporal 时间跳跃与故障恢复、GitLab HTTP 契约、真实 Git 工作区、Runtime Docker 协议、PostgreSQL/API，以及真实双任务 Docker 隔离。控制台 Playwright 使用隔离 API fixture。

## 部署与迁移

步骤见 [deploy/README.md](deploy/README.md)。管理程序支持 `serve`、`worker`、`migrate`、`doctor`、`legacy-inventory`。

Worker 经 Docker socket 创建同宿主机的任务容器，`DATA_DIR` 必须是宿主机与容器内一致的绝对路径。Agent 仅挂载自己的私有 Git 副本、当前执行目录、角色会话及只读报告；共享缓存和调度记录不暴露给 Agent。

本仓库从独立源码起步，不包含旧实现的 Git 历史。切换前停止旧版取单、备份数据，用 `swarmjet legacy-inventory /path/to/state.json` 生成手工接管清单。扫描跳过 `ai:working`、`ai:mr-opened`、`ai:needs-human`，确认存量分支/MR 后再调整标签。两套调度器不同时处理同一批任务。

## 排障

- 控制台查看阶段、会话、假设、测试及 MR；Temporal UI 查看执行历史。
- 暂停停止当前执行并保留现场，继续恢复确切会话；取消不回滚已合并代码或删除远端 MR。
- 自动扫描只首次接单，取消后不会因为保留 `autonomous` 标签再次自动启动；重跑已取消或完成的 Issue，使用控制台创建任务。
- 停止容器失败会反馈阻断，不误报取消成功。
- 提交变化使原审查/批准失效；目标分支推进需要同步和重新验证。
- 报告位于 `DATA_DIR/runs/<task>/`；监督记录与任务目录隔离。
- 真实模型认证、GitLab 权限或测试夹具缺失会明确失败，不用模拟测试代替验收。

接口见 [api/contract.md](api/contract.md)，内部约定见 [docs/architecture.md](docs/architecture.md)。

已执行的检查及真实环境验收缺口见 [docs/validation.md](docs/validation.md)。

## 发布与贡献

从源码安装见 [部署指南](deploy/README.md)，贡献与检查见 [CONTRIBUTING.md](CONTRIBUTING.md)。CI 使用标准 GitLab Container Registry，不依赖私有构建工具。当前尚未指定许可证；公开发布前需要明确代码授权与许可证。
