# Changelog

## v0.1.0 — 发布准备中

首个公开版本：自托管的 Agent swarm，围绕 GitHub 仓库、持久会话与可修改的方法论协作。下列“v0.1.0 起提供”条目仍依赖第五轮其他任务合入；本记录不代表已发布。

### 项目与协作（第一至三轮）

- 以 Hive 管理仓库、Forager、消息和配置；网页与本地 Agent 均可派活、对话、查看任务进展。
- 通过 GitHub App 接入仓库，平台发布受控分支与 PR，把评论、评审和 CI 事件送回对应 Agent。
- 网页一键创建 GitHub App，MCP 连接支持一次性显示 Token、作用域和客户端配置。
- 一键单机 Compose 安装；提供健康检查、数据库迁移、升级、日志、备份与可选监控。
- 支持仅开放 webhook 的临时隧道、地址同步与失败投递补投；控制台默认保留在本机回环地址。
- Agent 使用持久 Docker Cell 与会话；消息持久化、去重和重试，服务重启后可继续处理。

### 方法论与凭证边界（第四轮）

- 工作流程改为 Hive 的版本化方法论：角色、提示词、skill、事件、权限、合并条件和预算均可定义，支持发布与回滚。
- 提供 pstack 方法论包：worker 编写代码，verifier 独立验证当前提交；平台按声明检查验证结论和 CI，锁定 head 合并。
- Cell 统一运行 oh-my-pi；仓库可执行扩展、hook、工具与项目 MCP 默认禁用，规则和 skill 仍可读取。
- 模型凭证由平台的 auth-broker / auth-gateway 保管，Cell 经平台代理请求模型；网页与 admin MCP 可配置凭证，提供用量和花费记录。
- 外部 GitHub 内容保留来源可信度标记；Git 凭证与模型凭证不注入 Cell。

### Queen 与首次引导（第五轮，v0.1.0 起提供）

- 每个新 Hive 默认开启 Queen：管理方法论、派活、处理未认领事件和 Forager 提问；核心职责由平台固定，风格与合并权限由方法论补充。
- 网页创建 Hive 向导与 Queen 对话成为日常入口，无需常开本地 Agent；Queen 模型由用户选择。
- 新 Hive 不预装方法论；提供 pstack 与极简模板供 Queen 引导选择，支持按需安装。
- 新方法论生效后连续 3 个 Forager 失败时自动回滚到上一版本，并通知 Queen。
- 内部防循环上限放宽；并发由用户设置，Queen 不占 Forager 名额，预算按方法论声明执行。
- 发布前还需合入第四轮补充修复：PR 关闭后结束 owner、base 前进后的发布、并发满时排队补派、微额预算通知与验证准则。

### 发布与兼容性

- 同步发布平台与 Agent 两个 GHCR 镜像，包含版本 tag 与 latest；Agent 固定 OMP_VERSION，凭证服务共用该镜像。首版发布平台为 linux/amd64。
- 从第三轮部署重新运行新版 install 可移除旧 AGENT_CLIS 中的 opencode，并迁移旧 DeepSeek 默认模型；upgrade 子命令本身不执行这两项配置迁移，见[部署指南](deploy/README.md#升级与第三轮配置迁移)。
- 当前仅支持 GitHub 与单机部署；模型凭证由部署者提供。
