# 本地开发

前端、API 和 Worker 可以直接在宿主机运行。Vite 提供页面热更新；开发启动器在 Go 源码变化时重新编译并重启进程。Agent 和联调验收仍通过容器执行。

## 准备环境

Linux 开发环境需要 Node.js、npm、Go 1.25+、Git、`meta` 和可访问的 Docker daemon。先在仓库根目录安装前端依赖：

```bash
npm ci --prefix web
```

复制 `deploy/.env.example` 为本项目的 `deploy/.env`，设置数据库凭据、`DATA_DIR`、执行器配置和 Temporal namespace。可复制 `deploy/.env.development.example` 为 `deploy/.env.development`，覆盖本机工具路径和端口。启动器把 dotenv 内容作为数据解析，不作为 shell 脚本执行。不要提交配置文件中的密钥。

PostgreSQL 和 Temporal 可以使用仓库的 Compose 服务。首次准备依赖：

```bash
(cd deploy && docker compose up -d postgres temporal-namespace temporal-ui)
```

全新数据库还需从仓库根目录执行迁移：

```bash
(cd deploy && docker compose -f compose.yaml -f compose.build.yaml run --build --rm migrate)
```

开发启动器也会在启动进程前执行幂等迁移。若使用其他数据库或 Temporal，请在 `deploy/.env.development` 中填写相应地址。Agent 镜像的构建与配置见[部署指南](../deploy/README.md)。

## 启动与访问

```bash
./scripts/dev start
./scripts/dev status
./scripts/dev restart  # 修改开发环境配置后使用
./scripts/dev stop
```

Go 不在 `PATH` 时，可用 `GO_BINARY=/path/to/go ./scripts/dev start`。启动器会检查依赖，并停止旧的容器版 API/Worker；不要同时运行容器版与本地版应用进程。编译失败时保留旧进程，错误记录在 `.cache/dev/supervisor.log`。

开发页面默认位于 <http://localhost:5273>。API 监听 `127.0.0.1:8181`，Worker 管理接口监听 `127.0.0.1:8191`，构建服务监听 `127.0.0.1:8193`。进程日志保存在 `.cache/dev/`。Vite 代理 API、事件流和终端 WebSocket。

从容器运行切换到宿主机运行时，保持 `DATA_DIR` 的绝对路径不变，并将其中由 root 创建的旧文件交给本机运行用户；不要将目录改为所有人可读。启动脚本会为宿主机 Git 配置 `scripts/askpass.sh`，手动启动 Worker 时可通过 `SWARMJET_GIT_ASKPASS` 指定该文件的绝对路径。

## 验证

```bash
./scripts/test.sh
```

脚本默认运行 Go race tests、安装前端依赖并构建前端。设置 `VALIDATE_DEPLOY=1` 还会检查部署 Compose 配置；设置 `RUN_BROWSER_TESTS=1` 会安装 Chromium 并运行 Playwright 浏览器测试。

需要单独运行检查时：

```bash
go test -race ./cmd/... ./internal/...
npm run build --prefix web
```

可选集成测试需要专用数据库或 Docker 环境：

```bash
TEST_DATABASE_URL='postgres://postgres:test@localhost:5432/swarmjet_test?sslmode=disable' go test -race ./internal/store ./internal/api
SWARMJET_DOCKER_TEST=1 go test ./internal/integration
```

测试数据库会创建临时 schema。更多真实环境验收结果与尚未覆盖的项目见[验证记录](validation.md)。
