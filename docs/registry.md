# 自建 Distribution 与构建服务

Distribution 保存镜像内容，通过 Registry API 提供上传和拉取。平台构建服务负责发布；执行节点仅拉取镜像。

Agent 镜像位于配置的 `swarmjet/agent` 仓库路径。启用“系统设置 → 开发者选项”后，“镜像管理 → 平台镜像”可用独立 BuildKit 从平台源码构建并发布程序镜像到 `swarmjet/platform`；两类镜像各有版本清单。平台镜像以摘要供新 Node 部署，发布新版本不会自动升级运行中的程序容器。

本机开发和单机安装都使用同一份 `deploy/compose.registry.yaml`：Distribution 保存镜像，Nginx 网关提供 HTTPS 和读写分离认证。首次启动自动检测本机局域网 IPv4（也可指定固定地址），生成只属于这次安装的 CA、服务器证书及 `builder`/`reader` 独立随机密码，保存在 `DATA_DIR/registry-config`，并在本机 Docker 与 BuildKit 中安装信任配置。网关绑定该局域网地址的 5443 端口；其他局域网节点使用相同地址，需把生成的 `ca.crt` 安装到各节点 Docker 的 `/etc/docker/certs.d/<地址>:5443/ca.crt`。请给部署机器固定 DHCP 地址，避免 IP 变化使证书和已发布镜像引用失效。仓库数据保存在 Docker 卷中；镜像管理页默认使用它，也可选择自定义 HTTPS 仓库。密码由管理员点击“查看已保存密码”后才返回页面。

## 仓库部署

需要将仓库单独部署在另一台机器时仍使用 `deploy/compose.registry.yaml`。准备服务器证书 `server.crt`、私钥 `server.key`，证书 SAN 必须包含节点实际使用的域名或 IP；私有 CA 需安装到所有拉取镜像的 Docker 主机以及 BuildKit 信任配置中。不要设置 insecure registry 来跳过 TLS。

自动安装会生成两份 htpasswd 文件；手动部署也可以用 Apache `htpasswd -B` 准备：

- `REGISTRY_AUTH_FILE`：包含 builder 和 reader 账号。
- TLS 目录内的 `push.htpasswd`：只包含 builder，密码与上面的 builder 一致。

网关允许两个账号读取，写操作只接受 builder 文件内的账号；Registry 不发布宿主机端口，避免绕过网关认证。该策略提供整个仓库的读/写分离，不提供按项目权限。

部署环境变量：

| 变量 | 内容 |
| --- | --- |
| REGISTRY_BIND_HOST | 网关绑定的局域网地址 |
| REGISTRY_PORT | 默认 5443 |
| REGISTRY_TLS_DIR | TLS 文件所在绝对路径 |
| REGISTRY_AUTH_FILE | 读取认证文件绝对路径 |
| REGISTRY_HTTP_SECRET | 稳定随机值，存入私有环境文件 |

单独部署时运行 `docker compose -p swarmjet-registry --env-file /absolute/registry.env -f deploy/compose.registry.yaml up -d`。与平台组合部署时追加 `-f deploy/compose.registry.yaml`。备份 registry-data 卷、CA 私钥和私有配置。不要对业务 Docker 执行全局 prune。

平台默认使用自动生成的局域网仓库。在“系统设置 → 镜像仓库”选择自定义时，填写 host:port、镜像路径、builder 推送账号和 reader 拉取账号。自定义密码加密存储，不需要给 Node 推送权限。系统设置概览显示生效地址，并可从控制面检查仓库 TLS 与拉取认证；Node 心跳不等于 Docker 拉取验证。仓库镜像清单读取此路径的实时 tag 与摘要，包含外部推送的镜像；新构建也只发布到此路径。

页面删除以 manifest 摘要为单位，同摘要的所有 tag 一起失效。节点、项目、未结束任务或初始化仍引用该摘要时会拒绝删除；页面无法得知仓库外部客户端的引用。当前 Compose 示例已启用 manifest 删除，升级已有仓库容器后才会生效。删除不会立即回收磁盘，构建记录和已完成任务历史仍保留原摘要。

需要回收空间时，安排仓库维护窗口：先停止推送或将仓库设为只读，备份仓库数据卷，用 `registry garbage-collect --dry-run /etc/distribution/config.yml` 检查待清理对象，再运行不带 `--dry-run` 的命令，最后恢复写入。具体配置路径以运行中的 Registry 容器为准；不要在可写运行状态下执行垃圾回收。

## 构建服务

将 `deploy/compose.builder.yaml` 与 `deploy/compose.yaml` 组合启动。BuildKit 使用独立持久卷、固定引擎版本和资源限制；builder 通过共享 Unix socket 提交构建，不需要挂载宿主机 Docker socket。

BuildKit daemon 通过构建服务的 8094 代理入口联网，该入口随当前串行构建使用其冻结的网络配置。构建步骤和构建客户端使用同一配置，因此拉取基础镜像、下载依赖、发布产物使用一致出口。8094 只绑定宿主回环地址，不向局域网发布；仓库 CA 挂载到 BuildKit 并写入生成的 `buildkitd.toml`。

发布成功返回仓库摘要。选择节点分发后，节点异步拉取并检查架构和可用 CLI；检查只检测可执行文件，不调用模型。节点就绪前不会使用新镜像接任务。
也可以在“执行节点”页面的“默认镜像（已发布版本）”下拉框直接选仓库版本并保存节点配置；外部仓库镜像仍可手动填写完整摘要。

## 节点接入

在执行节点页面创建注册凭据，使用独立 Node ID、DATA_DIR、端口和 Compose 项目名部署 `deploy/compose.node.yaml`。填写控制面能访问的 NODE_ADVERTISE_URL；节点也必须能访问 CONTROL_PLANE_URL。新节点注册后默认暂停接单，先配置网络和发布镜像，再启用。

同一个 Docker daemon 的 Node 共享镜像拉取代理；Runner 的网络配置可以不同。Node 拉取镜像失败时，应检查 daemon 的代理、仓库认证、CA 和镜像平台，不要仅检查 Node 容器的环境变量。

### 文件权限与参考

Nginx worker 必须能读取两份 htpasswd 文件，例如文件权限 0644、TLS 目录 0755；私钥仍为 0600，仅启动时由 root 读取。账号密码原文不要放进 Compose 文件或提交到仓库。

- [Distribution 部署说明](https://distribution.github.io/distribution/about/deploying/)
- [Buildx remote driver](https://docs.docker.com/build/builders/drivers/remote/)

原生开发通过 `scripts/dev start` 启动独立 builder 进程和持久化 BuildKit 容器。构建服务日志为 `.cache/dev/builder.log`；停止开发进程不会删除 BuildKit 缓存。平台镜像构建前需要先保存仓库配置才能发布。

单机部署及原生开发会注册一个默认暂停的 `local` 节点，使用独立的 local-node 数据目录。构建成功后可把镜像分发给它，完成节点专属 CLI 登录配置后再启用。原有 Worker 配置终端用于历史本机环境，登录状态不会自动复制到新节点。
