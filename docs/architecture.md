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

## 项目接入、连接与初始化

普通用户不需要理解 Meta-Repo。项目有两种来源：`managed` 由系统在 `DATA_DIR/managed-meta/<projectID>/work` 创建并托管的内部 Git 仓库；`imported`（旧项目的空值）指向已有远程 Meta-Repo。受管模式不接受调用方提供的 MetaURL，也不暴露任意服务器路径：Workspace 只用校验过的项目 ID 推导内部仓库位置，因此没有绕过既有 `file://` 校验的入口。

GitLab 连接以 `Connection` 保存：地址、验证过的用户名、Token 指纹和加密后的密文。`internal/secretbox` 用 AES-256-GCM 加密，密钥只来自部署配置 `SWARMJET_MASTER_KEY`，与数据库分开管理。密钥必须稳定并单独备份：更换或丢失后旧密文无法解密，需要重新录入 Token。项目与任务快照只保存 `connectionId`（或环境变量名），Token 不进入 URL、日志、错误正文、Temporal 历史或浏览器持久存储。服务端校验 Token 适用的 GitLab 地址，客户端不跟随重定向。默认一个连接 Token 同时用于开发与合并；可选的独立合并 Token 在高级设置中单独验证。单 Token 模式不具备独立 Token 的权限隔离。保留环境变量凭证方式，兼容已有项目。

初始化是持久化、可恢复的任务，由 worker 侧的 `internal/initflow` 协调，状态写入 `initializations` 表，并用按项目的数据库租约保证单一执行者。阶段为 connect → analyze → verify，状态区分“仓库已连接”“分析完成”“可以接收开发任务”。稳定执行 ID（`init-<projectID>-analyze-<attempt>`）、幂等的受管 Meta 提交和按需重新克隆，保证重试与服务重启不会重复创建 Meta-Repo，也不会覆盖人工修订。分析副本固定在各仓库提交上且只读，生成目录是唯一可写目录，由容器挂载强制执行，而不是只靠提示词；初始化角色 `init` 不注入 GitLab Token。验证命令只在 `INIT_VERIFY_IMAGE` 指定的隔离容器中执行（无网络、只读根、丢弃 capability）；未配置时验证被阻断，绝不以 Worker 进程身份运行模型生成的命令。受管项目只有在持久化的 `readyForTasks` 为真时才能启用、接单；启用、建单和 Issue 扫描三条路径都做服务端校验。缺少信息时保存具体问题并等待回答；无法识别或未通过验证时项目标记为“尚未完成验证”，不会自动接单。

自动合并是项目级开关。`autoMerge` 为 null 表示旧项目的既有行为（开启），新项目显式默认关闭。关闭时 workflow 在审查通过后进入 `waiting_manual_merge`，只创建/审查 MR，并在观察到全部 MR 被人工合并后才收尾关单。“等待敏感变更批准”是独立门禁，批准不能越过关闭的开关。该决策通过 `workflow.GetVersion("auto-merge-switch", DefaultVersion, 1)` 版本化，已保存的旧历史按原决策顺序重放。
