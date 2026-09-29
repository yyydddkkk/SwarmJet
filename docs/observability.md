# 可观测性基线（2026-09-27）

API 和 Worker 各自输出低基数 Prometheus 指标及 JSON 结构化日志。设置 `OTEL_EXPORTER_OTLP_ENDPOINT` 或 `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` 后，两进程输出 OTLP/HTTP trace。HTTP、RabbitMQ、Temporal Workflow/Activity 和 GitLab/GitHub REST 调用使用 W3C trace context；新 Task 的 `traceParent` 随 PostgreSQL 任务快照保存，使独立的命令 outbox 循环可以续接原 trace。旧任务没有该字段时仍可凭 `task_id` 关联，但无法补回接入段 trace。Webhook 批量处理的消费 span 以首条消息为父 span、其余消息为 link；批内其他任务应结合 link 和 `delivery_id` 定位。

## 启用与访问

Compose API/Worker 在私有网络监听 `:9090`。`docker compose --profile observability up -d postgres-exporter prometheus alertmanager jaeger` 启动可选监控栈；Prometheus、Alertmanager、Jaeger UI 仅绑定宿主回环地址，详见[部署说明](../deploy/README.md)。要让 Compose API/Worker 导出 trace，在 `deploy/.env` 设置 `OTEL_EXPORTER_OTLP_ENDPOINT=http://jaeger:4318` 后更新两个应用容器。Jaeger 使用开发用内存存储，不适合作为生产 trace 留存。

原生 `swarmjet serve` / `swarmjet worker` 默认不监听指标端口。分别设置不同的 `METRICS_LISTEN_ADDRESS`，例如 `127.0.0.1:9190` 和 `127.0.0.1:9191`。指标与健康监听没有控制台会话认证，应只绑定受信任网络。`/healthz` 表示进程存活；指标监听的 `/readyz` 检查 PostgreSQL、Temporal，API 还检查 RabbitMQ。API HTTP 入口也提供 `/readyz`。依赖失败返回 503，响应不包含连接串或凭证。Compose 现有 API healthcheck 仍使用 `/healthz`，避免依赖短暂故障触发进程重启循环。

## 指标、日志和基数

| 指标 | 含义 |
| --- | --- |
| `swarmjet_http_requests_total`、`swarmjet_http_request_duration_seconds` | 固定路由类别、方法、状态类别的请求量与耗时。 |
| `swarmjet_messages_processed_total` | webhook/command 消费尝试的 ACK、重试、死信结果。 |
| `swarmjet_scan_runs_total`、`swarmjet_scan_duration_seconds` | 全量及单仓库对账。 |
| `swarmjet_admission_runs_total`、`swarmjet_admitted_tasks_total` | 准入循环及即时准入的成功量；`local_or_node` 合并本地与固定 Node 路径。 |
| `swarmjet_activity_steps_total`、`swarmjet_activity_step_duration_seconds` | 固定阶段的 Activity 尝试、结果和耗时，包含 Kubernetes Runner Activity；重试分别计数。 |
| `swarmjet_admission_waiting`、`swarmjet_admission_oldest_wait_seconds` | 待准入 Task 数与最长等待，按 Task 创建时间计算。 |
| `swarmjet_command_pending`、`swarmjet_command_oldest_wait_seconds` | PostgreSQL 中未送达 Temporal 的命令数与最老年龄。 |
| `swarmjet_runner_slots_reserved`、`swarmjet_legacy_inbox_pending` | Runner 已预留槽及迁移前 inbox 残留，后者不是 RabbitMQ 队列深度。 |
| `swarmjet_state_snapshot_success` | 最近一次数据库水位采样是否成功；失败时其他水位保留旧值。 |

水位每 15 秒采样一次，查询超时 3 秒。RabbitMQ 原生指标提供 ready/unacked、消费者和 Broker 状态；Temporal 原生指标提供服务及 Task Queue 指标；`postgres-exporter` 提供 `pg_up` 等数据库状态。Prometheus 配置在 [`deploy/observability/prometheus.yml`](../deploy/observability/prometheus.yml)。`task_id`、`execution_id`、delivery ID、仓库 ID、凭证和 Prompt 都不进入 Prometheus 标签。结构化日志包含可关联的 `delivery_id`、`task_id`、`command_id`、`execution_id`、`trace_id`（有 trace 时）；trace 不记录请求 URL、query、body 或认证头。高基数标识仍应按日志与 trace 的访问控制和保留期管理。

## 实测与首批 SLI/SLO

隔离夹具 `TestObservabilityTaskFixture` 使用独立 PostgreSQL schema、临时 RabbitMQ、独立 Temporal namespace 和假 GitLab HTTP 服务。一次运行记录了 webhook 收据、待准入 Task、已送达命令和预设的 503 平台依赖故障；对应的指标、日志与 trace 均可关联。该夹具证明关联链路和故障定位，不提供吞吐或生产延迟分布。此前的隔离 webhook 压测不生成 Task，仍需以真实代码平台和可接单事件复测。

复现命令（Broker 和 Temporal namespace 只能供此夹具使用，不能有其他消费者；数据库测试 helper 自动创建并删除独立 schema）：

```sh
TEST_DATABASE_URL='<test PostgreSQL URL>' \
SWARMJET_RABBITMQ_TEST_URL='amqp://<test user>:<test password>@<disposable broker>/' \
SWARMJET_TEMPORAL_ADDRESS='<test Temporal host:port>' \
SWARMJET_TEMPORAL_NAMESPACE='<isolated namespace>' \
SWARMJET_OBSERVABILITY_FIXTURE=1 \
go test -race -count=1 -run '^TestObservabilityTaskFixture$' -v ./internal/service
```

| SLI | 首批目标及证据边界 |
| --- | --- |
| webhook ACK：2xx 比例及 p95 耗时 | 开发验收候选：p95 < 1 s、成功率 ≥ 99%；依据是隔离 10,000 条消息结果，尚需真实代码平台与可接单任务复测。 |
| 消息/命令交付：ready/unacked、`command_pending`、最老年龄 | 候选：稳定负载下命令最老年龄 < 60 s；当前仅单任务夹具通过，尚未测压。 |
| 准入等待：waiting 数与最老年龄 | 候选：有空闲容量时最老等待 < 300 s；当前仅夹具通过，资源争用目标待实测。 |
| 阶段可靠性：按 `phase` 的 Activity 错误尝试率与最终 Task 结果 | 先区分可重试错误、预设故障与最终失败；当前仅验证了预设 503 被准确计数，不能制定生产成功率。 |
| 观测可用性：`up`、`pg_up`、`state_snapshot_success` | 开发验收要求采集目标均为 1，采样成功为 1；生产可用性窗口及目标待持续运行数据确定。 |

[`alerts.yml`](../deploy/observability/alerts.yml) 配置采集失败、PostgreSQL 连接失败、采样失败、死信积压，以及消息/命令积压、准入等待和阶段错误。积压保持时间、命令 60 秒与准入 300 秒阈值标为 `threshold_status="provisional_dev"`，是开发排障触发值，**不是已验证的生产 SLO 或告警阈值**。Alertmanager 默认仅本机展示，不发送外部通知；生产通知目的地、阈值和升级策略需结合真实负载、值班职责与故障演练配置。

## 故障定位与回放

1. 查 Prometheus `/targets` 的 `up`，再看 `pg_up` 与 `swarmjet_state_snapshot_success`；采样失败时不要把旧水位解释为当前值。
2. 看 RabbitMQ ready/unacked、死信，配合 `swarmjet_messages_processed_total` 判断未发布、未消费、重试或永久失败；命令再核对 PostgreSQL `command_pending` 与 Temporal Workflow ID。
3. 用日志 `delivery_id` 找到 `task scheduled` 的 `task_id`，再按 `task_id` 查准入、命令和 Activity；有 `trace_id` 时在 Jaeger 沿同一 trace 检查平台调用。批处理同时检查其他消息的 span link。
4. `/healthz` 为 200 且 `/readyz` 为 503 时先排查 PostgreSQL、Temporal、API 的 RabbitMQ 连通性；不要通过反复重启掩盖依赖故障。

Temporal 追踪以 SDK interceptor 接入，未修改 Workflow 决策代码或已有命令载荷。接入前的 `kubernetes_v1_history.json` 基线回放通过，接入后用相同历史和生产等价 interceptor 的回放也通过。仓库尚无已保存的 `WorkTask` 历史；其旧历史回放及跨版本长任务升级仍是单独验收项。精确模型费用与资源账本也不由采样指标推算。
