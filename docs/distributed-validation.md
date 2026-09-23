# 分布式镜像与节点验收记录

日期：2026-09-22。本次使用确定性测试执行器，不运行真实 Codex/OpenCode，也不调用模型或修改真实 GitLab 项目。

## 自动化检查

- Go：带独立 PostgreSQL 的 `go test -race ./cmd/... ./internal/...` 通过；后续受影响模块再次通过。
- 前端：TypeScript / Vite 构建通过；镜像、执行器、项目配置回归 22 项通过，新增节点/网络测试 4 项通过；之后节点/网络/镜像/执行器组合回归 15 项通过。
- 已检查节点和网络页面的 1440px / 390px 截图，修正复选框、列表对齐和响应式布局。
- Compose：基础、源码构建、独立 builder 组合及 Distribution 配置解析通过；安装脚本 3 项测试通过。
- 新增语义测试包括：配置版本冲突、显式直连、节点停用与归档限制、任务快照保持、网络版本未同步拒绝调度、一个节点支持不同角色镜像、准备失败保留旧环境、跨写入日志脱敏、代理 HTTP / HTTPS CONNECT / NO_PROXY、远程初始化路由与取消。

## 真实双主机测试

- 控制面和 BuildKit 测试主机：`192.0.2.121`。
- 第二个 Node 与 Distribution：`192.0.2.199`，工作目录 `/home/ubuntu/swarmjet-test/`。
- 第二台主机预检：Linux 4.18、amd64、Docker 28.0.1、Compose 2.32.4；使用独立目录和服务，没有停止原有业务容器或重启 Docker。
- Distribution 测试入口：`https://192.0.2.199:15443`。测试证书包含此 IP 的 SAN，使用短期测试证书；未关闭 TLS 验证。
- 匿名访问 `/v2/` 返回 401；reader 读取返回 200、上传返回 401；builder 创建上传返回 202，证明读写权限分开。
- 独立 BuildKit `v0.23.2` 通过生产 `RemoteRunner` 构建并发布 busybox + 静态测试执行器镜像。这里的 codex 文件是确定性测试替身，绝非真实 Agent CLI。
- 两个 Node 独立通过只读账号拉取并校验相同的仓库 manifest 摘要：

```text
192.0.2.199:15443/swarmjet/fixture@sha256:645dd2fa5105dc4801fa8eb108db49120f279196b2d43e196fa67e408d788e30
```

- 通过数据库正式调度入口分别固定到 test-local 和 test-remote，使用任务快照创建执行容器，均返回 succeeded，并将测试任务保存为 completed。
- 执行网络分别为 `sj-node-test-local` 和 `sj-node-test-remote`；调度快照包含 2048 MiB、2 CPU 的限制。
- 通过控制面按 `?node=` 路由读取两台节点的运行环境状态，均确认镜像可用。CLI 登录和模型有效性不在该结果的证明范围内。

## 本地开发实际检查

重建并重启后，`/api/runtime/images`、`/api/networks`、`/api/nodes`、`/api/registry`、`/api/project-defaults` 均返回 200。镜像服务报告 `scope=registry`；本地 Node 自动注册，初始 `enabled=false`，没有自动启用 Agent 或应用镜像。

## 保留的测试资源与复现

Distribution 和仓库数据保留在测试服务器，Compose 项目名为 `swarmjet-test-registry`。部署文件在 `/home/ubuntu/swarmjet-test/deploy/`，私有环境文件在 `/home/ubuntu/swarmjet-test/registry.env`，仓库账号原文在 `/home/ubuntu/swarmjet-test/registry/credentials.json`（0600）；不把密码复制进文档或 Git。

示例状态查询：

```sh
cd /home/ubuntu/swarmjet-test
docker compose --env-file registry.env -f deploy/compose.registry.yaml ps
```

隔离测试 API、测试 Node 进程、临时 PostgreSQL、测试执行容器和测试 BuildKit 在验收结束后清理；开发环境自己的 Node、builder 和 BuildKit 保留。两台 Docker 主机保留仅针对 `192.0.2.199:15443` 的测试 CA 信任，便于后续使用仓库；未修改其他仓库或 daemon 的代理设置。

重新接入远程 Node 时，请从正式平台节点页面创建新的节点凭据并修改其控制面地址；测试 API 地址不是永久控制面地址。正式使用仓库前应替换测试证书并安排存储备份。此验证未覆盖用户真实代理出口、真实模型调用或真实 GitLab 完整开发流程。
