# worker_db 数据库表设计（当前迁移）

**所属服务**：worker（核心轮询、数据库计划调度与 Redis Stream 消费者）
**Schema**：`worker_db`
**字符集 / 引擎**：`utf8mb4_unicode_ci` / InnoDB

> 本文以 `migrations/worker_db/0001_init.sql` 表结构为基础，并标出 `0002`、`0003` 的增量。文档中曾出现的 `export_task` 和额外 SAGA 字段并未建表。

## 表清单

| 表 | 当前用途 | 分区 |
| --- | --- | --- |
| `scheduled_task` | Webhook 投递的 cron 计划和跨实例租约 | 否 |
| `task_execution_log` | 上述任务每次执行的结果与人工触发原因 | `created_month` 月分区 |
| `comp_tx_log` | `refund_completed` Stream 结果审计 | `created_month` 月分区 |
| `dlq_log` | 预留的数据库 DLQ 管理表；当前消费者写 Redis `{stream}.dlq` | `created_month` 月分区 |
| `retry_queue` | 预留的任务重试队列 | 否 |

## `scheduled_task`

当前数据库字段：

| 字段 | 类型 | 约束 / 默认值 |
| --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | 主键、自增 |
| `task_code` | `VARCHAR(64)` | 唯一、非空 |
| `name` | `VARCHAR(128)` | 非空 |
| `cron_expr` | `VARCHAR(64)` | 非空 |
| `enabled` | `TINYINT(1)` | 非空，默认 `1` |
| `last_run_at` | `DATETIME(3)` | 可空 |
| `next_run_at` | `DATETIME(3)` | 可空 |
| `config_json` | `JSON` | 可空 |
| `created_at` / `updated_at` | `DATETIME(3)` | 自动维护 |

唯一索引：`uk_code(task_code)`。迁移 `0003_scheduled_execution.sql` 新增 `consecutive_fail_count`、`lease_token`、`lease_until` 和 `idx_due`，并预置 `webhook_dispatch`。handler 由代码中的 allowlist 绑定，不能从数据库任意指定。`enabled=0` 表示暂停；执行连续 5 次失败时自动暂停。

## `task_execution_log`

| 字段 | 类型 | 约束 / 默认值 |
| --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | 复合主键之一、自增 |
| `task_code` | `VARCHAR(64)` | 非空 |
| `started_at` | `DATETIME(3)` | 非空 |
| `finished_at` | `DATETIME(3)` | 可空 |
| `status` | `ENUM('running','success','failed','partial')` | 非空，默认 `running` |
| `affected_rows` | `BIGINT UNSIGNED` | 可空 |
| `error_msg` | `VARCHAR(512)` | 可空 |
| `created_month` | `DATE` | 非空；分区键 |

主键为 `(id, created_month)`，索引 `idx_task_time(task_code, started_at)`。按 `created_month` range 分区，迁移中包含 `p_init`、2026-10 至 2026-12 和 `p_max`。迁移 `0003` 新增 `triggered_by`（`cron`/`admin_api`）与 `trigger_reason`；耗时由起止时间计算。

## `comp_tx_log`

worker 当前仅用此表审计 user 的 `refund_completed` 事件，不运行 SAGA 或执行退款补偿。

| 字段 | 类型 | 约束 / 默认值 |
| --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | 复合主键之一、自增 |
| `tx_id` | `VARCHAR(64)` | 非空；存 envelope `event_id` |
| `consumer_group` | `VARCHAR(128)` | 非空，当前值 `worker-cg` |
| `stream` | `VARCHAR(64)` | 非空 |
| `payload_hash` | `VARCHAR(64)` | 可空；当前存完整 envelope 的 SHA-256 |
| `status` | `ENUM('pending','committed','compensated','failed')` | 非空，默认 `pending` |
| `retry_count` | `INT UNSIGNED` | 非空，默认 `0` |
| `last_error` | `VARCHAR(255)` | 可空 |
| `created_month` | `DATE` | 非空；分区键，取 envelope 时间所属月份首日 |
| `created_at` | `DATETIME(3)` | 非空，当前时间 |
| `committed_at` | `DATETIME(3)` | 可空；成功事件填 envelope 时间 |

主键 `(id, created_month)`；唯一键 `uk_tx(tx_id, created_month)`；索引 `idx_status(status)`。按月 range 分区，迁移包含 `p_init`、2026-10 至 2026-12 和 `p_max`。

worker 将成功结果记为 `committed`，失败结果记为 `failed`。重放时如果相同 `event_id` 的 stream、哈希或状态不同，消费报冲突并进入重试/DLQ。该记录不是业务退款状态的权威数据。

## `dlq_log`

数据库表已创建，但 Redis Stream 消费框架目前未写入或读取此表，也没有人工重放接口。

| 字段 | 类型 | 约束 / 默认值 |
| --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | 复合主键之一、自增 |
| `stream` | `VARCHAR(64)` | 非空 |
| `entry_id` | `VARCHAR(64)` | 非空 |
| `reason` | `VARCHAR(512)` | 可空 |
| `payload_json` | `JSON` | 可空 |
| `status` | `ENUM('open','replayed','closed')` | 非空，默认 `open` |
| `resolved_by` | `BIGINT UNSIGNED` | 可空 |
| `resolved_at` | `DATETIME(3)` | 可空 |
| `created_at` | `DATETIME(3)` | 非空，当前时间 |
| `created_month` | `DATE` | 非空；分区键 |

主键 `(id, created_month)`；索引 `idx_stream_status(stream, status)`。按 `created_month` range 分区，分区定义与 `task_execution_log` 相同。没有唯一事件键或手动重放/归档逻辑。

## `retry_queue`

当前仅有数据结构，worker 尚未消费或调度此表。

| 字段 | 类型 | 约束 / 默认值 |
| --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | 主键、自增 |
| `queue_name` | `VARCHAR(64)` | 非空，示例 `webhook_retry` / `pay_retry` |
| `payload_json` | `JSON` | 非空 |
| `run_at` | `DATETIME(3)` | 非空 |
| `attempt_count` | `INT UNSIGNED` | 非空，默认 `0` |
| `max_attempts` | `INT UNSIGNED` | 非空，默认 `5` |
| `last_error` | `VARCHAR(255)` | 可空 |
| `status` | `ENUM('pending','running','done','failed')` | 非空，默认 `pending` |
| `created_at` / `updated_at` | `DATETIME(3)` | 自动维护 |

索引 `idx_queue_run(queue_name, run_at, status)`。没有独立业务单号、软删除字段或已接入的退避规则。

## 迁移与运行状态

- 当前只有 worker DB 初始迁移 `0001_init.sql`。
- 三个 interval 循环和三个 Stream consumer 的实际行为见 [`api/worker.md`](../api/worker.md)。
- `scheduled_task` 驱动、`task_execution_log` 写入、数据库 DLQ 管理/重放、`retry_queue` 执行和导出任务表均未实现。
