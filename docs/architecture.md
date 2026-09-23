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

## 项目接入、连接与初始化

普通用户不需要理解 Meta-Repo。项目有两种来源：`managed` 由系统创建并托管的内部 Git 仓库；`imported`（旧项目的空值）指向已有远程 Meta-Repo。受管模式不接受调用方提供的 MetaURL，也不暴露任意服务器路径：Workspace 只用校验过的项目 ID 推导内部仓库位置，因此没有绕过既有 `file://` 校验的入口。执行节点上的受管仓库由控制面在任务准备阶段以 git bundle 传输，节点用确定性路径 `<node DATA_DIR>/managed-meta/<projectID>/work` 物化；因此受管项目不依赖创建它的那台机器的本地目录，可被调度到任意在线节点。

GitLab 连接以 `Connection` 保存：地址、验证过的用户名、Token 指纹和加密后的密文。`internal/secretbox` 用 AES-256-GCM 加密，密钥只来自部署配置 `SWARMJET_MASTER_KEY`，与数据库分开管理。密钥必须稳定并单独备份：更换或丢失后旧密文无法解密，需要重新录入 Token。项目与任务快照只保存 `connectionId`（或环境变量名），Token 不进入 URL、日志、错误正文、Temporal 历史或浏览器持久存储。服务端校验 Token 适用的 GitLab 地址，客户端不跟随重定向；更换连接地址必须重新提供并验证 Token。默认一个连接 Token 同时用于开发与合并；可选的独立合并 Token 在高级设置中单独验证。单 Token 模式不具备独立 Token 的权限隔离。保留环境变量凭证方式，兼容已有项目。

初始化是持久化、可恢复的任务，由 worker 侧的 `internal/initflow` 协调，状态写入 `initializations` 表，并用按项目的数据库租约保证单一执行者。阶段为 connect → analyze → verify，状态区分“仓库已连接”“分析完成”“可以接收开发任务”。稳定执行 ID、幂等的受管 Meta 提交和按需重新克隆，保证重试与服务重启不会重复创建 Meta-Repo，也不会覆盖人工修订。分析副本固定在各仓库提交上且只读，生成目录是唯一可写目录，由容器挂载强制执行，而不是只靠提示词；初始化角色 `init` 不注入 GitLab Token。验证命令只在 `INIT_VERIFY_IMAGE` 指定的隔离容器中执行（无网络、只读根、丢弃 capability），容器以确定性名称创建并在取消、超时或重启后清理；未配置镜像时验证被阻断。受管项目只有在持久化的 `readyForTasks` 为真时才能启用、接单；启用、建单和 Issue 扫描三条路径都做服务端校验。

自动合并是项目级开关。`autoMerge` 为 null 表示旧项目的既有行为（开启），新项目显式默认开启。关闭时 workflow 在审查通过后进入 `waiting_manual_merge`，只创建/审查 MR，并在观察到全部 MR 被合并后完成该轮任务。新任务用 `Round > 0` 标识常开 Issue 语义：合并后不关单，敏感路径不设人工批准门禁，必需验收与 GitLab 合并检查仍必须通过；旧任务保留原关单与批准行为。该决策使用 Temporal 版本化记录，避免已保存历史重放改变。

同一 Issue 可有多轮 Task；`/swarmjet run` Issue 评论的 ID 持久化去重，当前轮非终态时新评论排队并合并到下一轮。来源 Issue 描述、触发评论和上一轮 Task 引用固定在新任务快照中。人工复核指引按 Task 和审查提交摘要发布在 Issue 独立评论中，MR 描述只保留链接。平台管理的 Issue 依赖绑定首轮等待时选定的上游 Task，以上游 Task 完成而非 Issue 关闭为满足条件；外部 Issue 仍依 GitLab 关闭状态判断。
