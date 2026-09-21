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

当前已实现「控制面集中 + 多执行节点、任务固定归属」：`swarmjet node` 在独立 `DATA_DIR` 与 Docker 上提供服务，控制面的 `execution.Pool` 通过带时间戳与 HMAC 的 HTTP 协议调用节点。任务在 `CreateTaskScheduled` 中按在线状态与容量分配一次并写入 `tasks.node_id`；工作区、Agent、联调、报告读取和任务级退役都按该归属路由。节点失联时任务 blocked 并保留现场，不自动漂移。本机开发仍使用空 `NodeID` 走进程内管理器。

接入自动接管前仍需处理：

- 节点失联后的工作区/会话恢复与执行权校验，防止旧节点恢复后重复推送或合并；目前只支持等待原节点恢复或转人工。
- 报告集中存储；API 目前按节点代理读取，历史报告依赖节点在线。
- 每节点 runtime-admin 模型登录终端的控制台路由与凭证分发。
Kubernetes 后端应通过 Kubernetes API 管理 Pod/Job 和存储，由 Kubernetes 调度资源；Temporal 继续管理业务流程。本地开发保留本机进程加 Docker 的运行方式。