# SwarmJet 迁移验证

本文件只记录新目录中的检查，不沿用来源项目的通过记录。

迁移将程序、模块、环境变量前缀、镜像名、容器标签、任务队列及默认 namespace 改为 SwarmJet，并使用独立 Compose 项目和数据目录。没有复制旧 Git 历史、凭据、数据库、工作区或模型会话。

## 已通过（2026-09-21）

- 全量 Go race tests；后续运行管理协议改名后重新执行受影响的 API、runtimeadmin、workspace 测试。
- `go vet ./cmd/... ./internal/...` 与 `go build ./cmd/swarmjet`。
- React TypeScript 检查、生产构建，以及 16 个 Playwright 浏览器测试。测试服务器使用独立端口 5274。
- 源码部署与镜像部署的 Compose 配置验证，以及公共示例的 Compose 配置验证。
- 公共 HTTP 示例真实启动，按 `integration.json` 中的命令检查状态码和响应体，输出 `PASS http-health`；随后清理该测试项目的容器与网络。
- Shell 语法、Python 开发启动脚本语法、本地文档链接检查。
- 旧名称与已知公司地址检索、常见凭据格式扫描未发现待处理内容。该扫描不是完整的凭据审计。
- 来源工作区没有文件改动，原有 work-bot 服务未被本次测试启动、停止或迁移。

## 未覆盖

- 未设置 `TEST_DATABASE_URL`，需要 PostgreSQL 的集成测试按原有规则跳过。
- 未重新构建完整 Docker 镜像，未运行 GitLab CI 或向 Registry 发布镜像；CI 仍需在目标 GitLab runner 上验证。
- 未启动 SwarmJet 完整服务栈，没有创建真实 `.env` 或迁移现有数据。
- 真实模型调用、GitLab push/MR/merge 和生产部署未验证。
- 许可证和公共仓库地址尚未确定；Go module 暂用 `swarmjet`。

