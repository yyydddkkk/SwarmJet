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

GitLab CI 使用项目自带的 Container Registry。镜像发布 job 需要支持 privileged Docker-in-Docker 的 runner，并共享 `/certs/client`；普通检查无需公司特定 runner 标签。main 和版本标签触发镜像发布，不自动部署服务器。

尚未选择开源许可证，公共仓库地址也尚未确定。目前 Go module 为 `swarmjet`；确定仓库地址后可统一更改模块路径。公开发布前应明确代码归属、保留所需署名并加入适用许可证。
