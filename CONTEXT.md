# SwarmJet 任务执行

SwarmJet 将任务分配给执行节点，并在每次需要 Agent 工作时启动独立执行。以下术语区分运行位置、工具和一次执行。

## Language

**Node（执行节点）**：承载任务工作区并管理该位置上任务执行的运行位置。一个 Node 可同时承接多个任务。

**Agent**：实际处理任务的 CLI 工具，例如 OpenCode、DST 或 Codex。同一种 Agent 可以用于多个任务。

**Runner**：属于单个任务的一次 Agent 执行的容器，运行选定的 Agent。一个任务在不同阶段可以产生多个 Runner；一个 Node 可以同时运行多个 Runner。
_Avoid_: Agent 容器、执行器、任务执行容器

**临时测试容器**：用 Runner 镜像探测 Node 网络连通性或 CLI 安装状态的短暂运行实例，不处理任务，也不验证 Agent 登录或模型调用。
