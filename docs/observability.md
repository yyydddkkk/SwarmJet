# 可观测性基线（2026-09-27）

API 和 Worker 各自输出低基数 Prometheus 指标及 JSON 结构化日志。

## 启用与访问

Compose API/Worker 在私有网络监听 `:9090`。`docker compose --profile observability up -d postgres-exporter prometheus alertmanager` 启动可选监控栈；Prometheus、Alertmanager UI 仅绑定宿主回环地址，详见[部署说明](../deploy/README.md)。

原生 `swarmjet serve` / `swarmjet worker` 默认不监听指标端口。分别设置不同的 `METRICS_LISTEN_ADDRESS`，例如 `127.0.0.1:9190` 和 `127.0.0.1:9191`。指标与健康监听没有控制台会话认证，应只绑定受信任网络。`/healthz` 表示进程存活；指标监听的 `/readyz` 和 API HTTP 入口的 `/readyz` 检查 PostgreSQL 与 Temporal。依赖失败返回 503，响应不包含连接串或凭证。Compose 现有 API healthcheck 仍使用 `/healthz`，避免依赖短暂故障触发进程重启循环。

## 指标、日志和基数

| 指标 | 含义 |
| --- | --- |
| `swarmjet_http_requests_total`、`swarmjet_http_request_duration_seconds` | 固定路由类别、方法、状态类别的请求量与耗时。 |
| `swarmjet_messages_processed_total` | webhook/command Job 的完成、重试和取消结果。 |
| `swarmjet_scan_runs_total`、`swarmjet_scan_duration_seconds` | 全量及单仓库对账。 |
| `swarmjet_admission_runs_total`、`swarmjet_admitted_tasks_total` | 准入循环及即时准入的成功量；`local_or_node` 合并本地与固定 Node 路径。 |
| `swarmjet_activity_steps_total`、`swarmjet_activity_step_duration_seconds` | 固定阶段的 Activity 尝试、结果和耗时；重试分别计数。 |
| `swarmjet_admission_waiting`、`swarmjet_admission_oldest_wait_seconds` | 待准入 Task 数与最长等待，按 Task 创建时间计算。 |
| `swarmjet_command_pending`、`swarmjet_command_oldest_wait_seconds` | PostgreSQL 中未送达 Temporal 的命令数与最老年龄。 |
| `swarmjet_runner_slots_reserved`、`swarmjet_legacy_inbox_pending` | Runner 已预留槽及迁移前 inbox 残留。Job 积压在 PostgreSQL 的 `river_job`，不在这个指标里。 |
| `swarmjet_state_snapshot_success` | 最近一次数据库水位采样是否成功；失败时其他水位保留旧值。 |

水位每 15 秒采样一次，查询超时 3 秒。Temporal 原生指标提供服务及 Task Queue 指标；`postgres-exporter` 提供 `pg_up` 等数据库状态。Prometheus 配置在 [`deploy/observability/prometheus.yml`](../deploy/observability/prometheus.yml)。`task_id`、`execution_id`、delivery ID、仓库 ID、凭证和 Prompt 都不进入 Prometheus 标签。结构化日志包含可关联的 `delivery_id`、`task_id`、`command_id`、`execution_id`。高基数标识仍应按日志的访问控制和保留期管理。

## 实测与首批 SLI/SLO

| SLI | 首批目标及证据边界 |
| --- | --- |
| webhook ACK：2xx 比例及 p95 耗时 | 开发验收候选：p95 < 1 s、成功率 ≥ 99%；依据是隔离 10,000 条消息结果，尚需真实代码平台与可接单任务复测。 |
| 消息/命令交付：`command_pending`、最老年龄、`river_job` 未完成行 | 候选：稳定负载下命令最老年龄 < 60 s；尚未测压。 |
| 准入等待：waiting 数与最老年龄 | 候选：有空闲容量时最老等待 < 300 s；资源争用目标待实测。 |
| 阶段可靠性：按 `phase` 的 Activity 错误尝试率与最终 Task 结果 | 先区分可重试错误、预设故障与最终失败；当前仅验证了预设 503 被准确计数，不能制定生产成功率。 |
| 观测可用性：`up`、`pg_up`、`state_snapshot_success` | 开发验收要求采集目标均为 1，采样成功为 1；生产可用性窗口及目标待持续运行数据确定。 |

[`alerts.yml`](../deploy/observability/alerts.yml) 配置采集失败、PostgreSQL 连接失败、采样失败，以及命令积压、准入等待和阶段错误。命令 60 秒与准入 300 秒阈值标为 `threshold_status="provisional_dev"`，是开发排障触发值，**不是已验证的生产 SLO 或告警阈值**。Alertmanager 默认仅本机展示，不发送外部通知；生产通知目的地、阈值和升级策略需结合真实负载、值班职责与故障演练配置。

## 故障定位与回放

1. 查 Prometheus `/targets` 的 `up`，再看 `pg_up` 与 `swarmjet_state_snapshot_success`；采样失败时不要把旧水位解释为当前值。
2. 看 `swarmjet_messages_processed_total` 与 `river_job` 中 `available`、`retryable`、`scheduled`、`discarded`、`cancelled` 的行，判断未消费、重试或永久失败；命令再核对 PostgreSQL `command_pending` 与 Temporal Workflow ID。
3. 用日志 `delivery_id` 找到 `task scheduled` 的 `task_id`，再按 `task_id` 查准入、命令和 Activity。
4. `/healthz` 为 200 且 `/readyz` 为 503 时先排查 PostgreSQL 和 Temporal；不要通过反复重启掩盖依赖故障。

仓库尚无已保存的 `WorkTask` 历史；其旧历史回放及跨版本长任务升级仍是单独验收项。精确模型费用与资源账本也不由采样指标推算。
