# 使用指南

本文补充项目成员、执行器选择和任务运行规则。首次部署见[部署指南](../deploy/README.md)，GitLab 项目的逐步接入见[项目快速接入](getting-started.md)。

## 成员与权限

一个部署默认服务一个组织。平台管理员负责初装、应急管理、团队与连接管理，并创建项目。初始负责人可选；留空时由当前平台管理员管理，之后可在项目成员中指定负责人。配置 STARTTLS SMTP 后，成员可用邮箱申请账号，输入邮件中的 6 位验证码设置姓名和密码；管理员审批完成后才能登录。停用账号会撤销已有会话。

项目默认只对成员可见。直接授权与团队授权取较高角色：观察者只读；操作员可创建和处置任务；负责人可管理项目配置、直接成员、人工批准与合并。当前不提供 SSO、多因素认证或自定义角色。

GitLab 成员需要在“个人 GitLab 身份”中验证并绑定自己的 Token，工作台操作使用该成员的 Token；自动任务使用组织连接。GitLab 标签和 `/swarmjet run` 评论只有在项目启用相应触发策略，且作者是已绑定的负责人或操作员时才自动接单。无法核验的请求保持待处理。GitHub 自动接单默认关闭，由平台管理员逐项目启用。

## 创建项目

平台管理员选择 GitLab 连接或已配置的 GitHub App 安装，选择仓库并确认每个仓库的目标分支。受管项目会创建内部 Meta-Repo，运行初始化分析和隔离验证；模型或验收未就绪时项目保持草稿，不接单。GitLab 连接使用 `SWARMJET_MASTER_KEY` 加密保存；GitHub 连接保存安装 ID，短期令牌按需签发。

“导入已有 Meta-Repo”是高级入口。导入时提供 Meta-Repo URL/ref，并使项目仓库列表与 `.meta.yaml`、`.meta.yml` 或 `.meta` 中的清单一致。例如：

```yaml
projects:
  backend:
    repo: https://gitlab.example.com/team/backend.git
  client:
    repo: https://gitlab.example.com/team/client.git
    depends_on: [backend]
```

各仓库独立配置 `targetBranch`；目标分支不存在时须提供 `createFrom`，从明确起点初始化。配置修改默认仅影响新任务。Meta-Repo 自身需要改码时，将它登记为 `path: "."` 的仓库，目标分支与 `metaRef` 对齐。

## 选择 Agent

管理员在“执行器环境”中启用可用的 Agent，项目再选择一个默认 Agent。Agent CLI 必须已安装在配置终端和任务使用的镜像中；在页面勾选启用不会安装软件。Agent 镜像的来源与节点配置见[部署指南](../deploy/README.md#agent-镜像与跨机分发)。

Issue 可以添加一个 `agent:opencode`、`agent:codex`、`agent:dst`、`agent:agy` 或 `agent:claude` 标签，覆盖项目默认 Agent。多个标签、未知 Agent 或未启用 Agent 会被拒绝接单，修正后轮询重试。Agent 标签只接受 Agent ID，不接受模型名称。任务接单后执行器固定，不因后续标签或启用列表变化而切换。

任务使用所选 CLI 保存的默认模型配置。首次执行时生成任务级配置快照，开发与审查使用私有副本和独立会话。终端配置的后续修改只影响之后首次启动的任务；API key 应配置到部署环境。受管项目的静态初始化仍使用 OpenCode，相关镜像需保留它。

## 任务轮次与合并

首次任务由已登记仓库的 opened Issue 上的 `autonomous` 标签触发。后续轮次由以 `/swarmjet run` 开头的 Issue 评论触发；普通复核回复不会启动开发。当前轮运行期间收到的新命令排入下一轮，不改变当前轮已审查的提交集合。

任务固定项目配置、仓库基线和执行节点，在私有工作区修改代码、运行检查并创建 MR/PR。启用独立审查或额外验收时，未通过的检查需要返工；提交或目标分支变化后需要重新验证。需求有歧义时记录假设、标记 `needs-clarification` 并继续执行；具体障碍无法解决时才阻塞。

新项目默认开启自动合并。关闭后，系统仍会开发、验证、创建 MR/PR 和审查，待所有变更请求由团队在托管平台合并后完成该轮。来源 Issue 保持打开；提交和 MR/PR 只引用 Issue，不使用自动关单指令。任务完成后保留按提交版本生成的人工复核指引。

## 排障与执行节点

控制台可查看任务阶段、会话、假设、检查和 MR/PR；Temporal UI 可查看执行历史。暂停会停止当前执行并保留现场，继续时恢复确切会话；取消不会回滚已经合并的代码或删除远端 MR/PR。报告位于 `DATA_DIR/runs/<task>/`。

单机部署由 Worker 通过宿主 Docker socket 创建任务容器，`DATA_DIR` 在宿主机与容器内必须是相同的绝对路径。分布式模式下，任务接单后固定到一个执行节点；节点失联时保留现场并阻塞，不自动迁移。节点容量、镜像和网络配置见[部署指南](../deploy/README.md)和[执行节点说明](execution-nodes.md)。
