# 实现约定

Workflow 决定阶段，Activity 实施 I/O。Activity 重试使用稳定执行 ID；Docker 容器、Agent 会话和实际 MR 必须先核实再继续。进程退出不等于完成，Agent 必须写符合当前角色的结构化结果。

项目配置按版本保存，Task 固定 Project 快照。ReviewDigest 绑定整组仓库、基线、当前提交及目标分支；ApprovalDigest 仅批准同一摘要。合并请求携带源 SHA，已合并 MR 也要验证源、目标及提交身份。

管理员命令通过持久化 outbox 投递，重复命令不得重复返工或批准。需求歧义是提示，实际障碍才阻断。

长时间依赖等待按批次 Continue-As-New，携带执行序号、命令去重和等待状态，避免无限增长历史。以后修改已有 Workflow 的决策顺序时，需要使用 Temporal 版本化/patch 机制，并对已保存历史做 replay 验证。

`projects/<project>/meta` 是长期缓存。每个任务建立无共享对象硬链接的私有源仓库 `task-sources/<task>/meta`，再用 meta 创建 worktree，避免不同任务共享可写的 Git 配置、hooks 或引用。

Worker 主机侧 Git 使用最小环境并禁用可执行扩展，校验元数据与 remote。任务容器内的正常 Git hooks 保留。凭证按阶段注入，审查阶段不持有合并凭证。

任务生命周期记录位于 Worker 专有目录；Agent 只能写自己的代码、会话及当前结果。联调环境与任务一一对应，验证结果由实际执行生成。

## 执行后端与多机演进

Activity 通过 `internal/execution.AgentBackend` 调用 Agent 的启动、观察、取消和任务级执行记录查询。`internal/agent.Runtime` 提供本机 Docker 实现，在程序入口注入。稳定执行 ID 的重试接管、精确 SessionID 恢复、结果校验和停止失败反馈属于后端契约；流程不解析容器名称来判断执行状态。

执行后端负责在执行主机上创建启动所需的文件。GitLab CLI 配置、镜像内凭证助手路径由本机后端准备，Activity 只选择当前阶段允许使用的凭证和 GitLab 地址。请求环境含凭证且不参与普通 JSON 序列化；未来远程协议必须单独设计认证及凭证传递，不能直接序列化 `AgentRequest` 后发送，也不能将凭证写入 Temporal 历史。

当前仍只支持单执行宿主机，尚未实现远程服务、Kubernetes 后端或跨节点故障接管。新增接口不改变 Temporal task queue、任务记录格式和本地开发命令。不能仅通过设置远程 `DOCKER_HOST` 或增加不同主机的 Worker 来启用分布式。

后续应以整套任务执行环境为分配单位，包括私有 Git 工作区、Agent 会话和联调容器。接入第二种后端前，需要一起处理：

- 持久化任务的执行节点归属；准备、开发、验证、审查、取消和清理必须路由到同一任务现场。现有 Activity 共用队列与全局并发槽不能直接充当节点分配机制。
- 将 Workspace 管理和 Compose 联调放到执行节点；`Workspace.Root`、`Execution.LogPath` 目前仍是本机路径，不能作为跨机器可访问地址。
- 为报告和日志提供按节点访问或集中存储；API 当前读取本机 `DATA_DIR`。配置终端及模型登录 profile 也需要明确节点归属和凭证分发方式。
- 迁移保留期清理。`maintenance.Manager` 当前直接检查 Docker 容器与本地记录，只能与本机后端一起使用，不能用于远程执行记录。
- 节点失联时保留任务归属并等待恢复；若增加自动接管，必须实现工作区/会话恢复和执行权校验，防止旧节点恢复后重复推送或合并。

Kubernetes 后端应通过 Kubernetes API 管理 Pod/Job 和存储，由 Kubernetes 调度资源；Temporal 继续管理业务流程。本地开发保留本机进程加 Docker 的运行方式。
