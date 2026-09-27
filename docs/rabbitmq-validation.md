# RabbitMQ 接入验证（2026-09-27）

测试位置：`dev` 工作区；RabbitMQ `4.3.6-management` 独立临时容器（单节点、持久卷），PostgreSQL 18 运行中实例的独立临时 schema。测试结束后 schema 自动删除。测试没有连接真实 GitLab 项目；一万条消息均含唯一 delivery ID，但不生成 Task。

| 检查 | 结果 |
| --- | --- |
| 10,000 条唯一 GitLab webhook 首次 ACK | 10,000 / 10,000 |
| 503 重试与最终 ACK | 0 次；最终 10,000 / 10,000 |
| ACK 延迟 p50 / p95 / p99 | 15.99 / 167.59 / 225.99 ms |
| 发送阶段 / 收据排空 | 17.41 / 28.07 s |
| Broker 投递 / PostgreSQL 唯一收据 | 10,000 / 10,000 |
| 观察到的丢失 / 重复投递 | 0 / 0 |
| 采样到的 PostgreSQL 锁等待 | 0 |

运行命令：

```bash
TEST_DATABASE_URL='<isolated test PostgreSQL URL>' \
SWARMJET_RABBITMQ_TEST_URL='amqp://<test credentials>@<disposable broker>/' \
SWARMJET_BURST_TEST=1 go test -count=1 -run TestWebhookBurstIntegration -v ./internal/service

TEST_DATABASE_URL='<isolated test PostgreSQL URL>' \
go test -count=1 ./internal/service ./internal/store ./internal/pipeline

SWARMJET_RABBITMQ_TEST_URL='amqp://<test credentials>@<disposable broker>/' \
go test -count=1 -run TestBrokerIntegration -v ./internal/mq

TEST_DATABASE_URL='<isolated test PostgreSQL URL>' \
SWARMJET_RABBITMQ_TEST_URL='amqp://<test credentials>@<disposable broker>/' \
go test -count=1 -run TestWebhookAPIAndConsumerRestartIntegration -v ./internal/service

# 分两次执行，中间重启测试 Broker 并等待健康：
SWARMJET_RABBITMQ_RESTART_PHASE=publish SWARMJET_RABBITMQ_TEST_URL='<test URL>' \
go test -count=1 -run TestBrokerRestartPersistence ./internal/mq
SWARMJET_RABBITMQ_RESTART_PHASE=verify SWARMJET_RABBITMQ_TEST_URL='<test URL>' \
go test -count=1 -run TestBrokerRestartPersistence ./internal/mq
```

Broker 集成测试通过：持久发布确认、mandatory 无法路由、队列满拒绝、消费者永久失败死信与保留 ID 的重放。独立运行 `TestBrokerRestartPersistence` 的 `publish` 阶段，重启 RabbitMQ 容器并等待其健康后运行 `verify` 阶段，消息仍可读取并手动 ACK。`TestWebhookAPIAndConsumerRestartIntegration` 还验证 API 进程模拟重建、消费者停止再启动后，50 条已确认事件形成 50 条唯一收据。单节点重启期间客户端连接失败是预期现象。

数据库集成测试通过：GitLab 新 signing token 的 HMAC 与时间窗验证、delivery ID 去重与不同内容冲突、扫描失败后保留待重试消息、同仓库批量扫描一次、可接单 Issue 触及每项目 100 条待准入上限时不记录 webhook 收据、命令发布失败与重发及 Kubernetes Workflow 命令去重。完整 Go race 测试、前端构建、安装器单测、三套 Compose 配置校验也已通过。

尚未验收：真实 GitLab/GitHub 发送端对 503 的退避重试、真实可接单事件的万条端到端积压、API/消费者与 Broker 同时重启、三节点 quorum 的节点故障切换、生产环境监控和备份恢复演练。本次消息排空结果不代表一万个 Task 完成。
