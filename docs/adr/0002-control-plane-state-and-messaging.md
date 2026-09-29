# 控制面状态与消息传递的归属

状态：已接受（2026-09-27）

企业主线使用 PostgreSQL 作为 Task、接单唯一性、准入、执行权与人工命令的权威业务状态；Temporal 管理长任务的流程历史；RabbitMQ 4.3 quorum queues 承担 webhook 事件和内部命令的缓冲、投递、重试与消费者扩容。当前不引入 Redis，也不以 Redis 保存权威 Task 状态。开发及现有 Compose 使用带持久卷的单节点 Broker；该拓扑只验证持久化和进程重启恢复。生产高可用部署采用三节点 quorum 集群。

业务状态变更与待发送命令保持同一 PostgreSQL 事务边界；数据库中的 commands 行是事务性发送意图，投递器在 RabbitMQ 发布确认后记录发布时间。命令消费者调用 Temporal 成功后标记 delivered 并 ACK。固定命令 ID、Workflow ID 及 Workflow 内去重处理确认丢失和重复投递。Webhook 以平台 delivery ID 发布持久消息，仅在发布确认且成功路由后返回 202；无稳定 ID 返回 400，Broker 不可用、确认超时、队列满或无法路由返回 503。PostgreSQL `(provider, delivery ID)` 收据处理发布确认丢失后的重复发送；相同 ID 的不同内容进入死信并告警。消息队列收到消息或消费者 ACK 不代表业务操作恰好执行一次，所有业务处理仍须核对 PostgreSQL 唯一性约束与状态。Redis 以后只在可重建缓存等具体需求被验证时重新评估，不作为当前架构的预设依赖。

主队列使用 `reject-publish` 和消息数量、字节上限；延迟重试与 quorum queue at-least-once dead-lettering 负责失败重试和可靠转移。死信须在确认消息及原因后人工或运维工具重放；重放仍按稳定 ID 去重。队列积压指标独立于 `/api/operations` 的 Task 准入指标告警。切换时旧 PostgreSQL inbox 先排空，未处理记录不得删除；周期对账与消费者使用同一仓库租约。

这一选择接受新增消息队列的运维成本，换取独立扩容、积压管理和消息投递能力，同时保留 PostgreSQL 事务对业务不变量的保护。队列条数限制在并发发布时可能少量超限，不能当作精确硬上限。已完成隔离环境万条唯一 webhook 收据排空及单节点 Broker 重启恢复；真实平台退避重试、可接单万条事件与三节点故障切换仍待验收。
