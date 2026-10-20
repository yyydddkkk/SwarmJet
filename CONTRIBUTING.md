# 参与开发

SwarmJet 当前支持 GitLab、OpenCode、Temporal、PostgreSQL 和 Docker。业务仓库、目标分支、模型与验收规则应通过项目配置提供，不把特定组织或项目写入平台逻辑。

## 本地验证

安装 Go 1.25+、Node.js 及 npm，然后运行：

```sh
VALIDATE_DEPLOY=1 RUN_BROWSER_TESTS=1 scripts/test.sh
go vet ./cmd/... ./internal/...
```

Compose 验证需要 Docker Compose。浏览器测试使用独立端口 5274，不复用已有开发页面。PostgreSQL 集成测试需要显式设置专用的 `TEST_DATABASE_URL`；其他真实 Docker、Temporal、模型测试见测试代码中的 `SWARMJET_*` 开关。不要将测试指向生产资源。

开发启动与配置见 README。不要提交 `.env`、模型登录记录、私有源码、工作区数据或日志。

## 发布

GitHub Actions 在推送到 `main`、`dev` 以及指向这两个分支的拉取请求时，运行 Go 测试、控制台构建与浏览器测试，以及 Compose 配置检查。只有在 `main` 上推送严格的 `vX.Y.Z` 标签，且这些检查通过后，才把程序镜像发布到 `ghcr.io/yyydddkkk/swarmjet:<tag>`，把执行器镜像发布到 `ghcr.io/yyydddkkk/swarmjet/agent:<tag>`。流水线不部署服务器。

代码以 [MIT 许可证](LICENSE) 发布。Go module 路径是 `github.com/yyydddkkk/SwarmJet`。
