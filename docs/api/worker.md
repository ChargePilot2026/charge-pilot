# worker 服务任务定义与 Stream 消费约定

**服务**:`worker`(`services/worker`)
**对外地址**:不提供业务 HTTP API；仅监听 `/health` 运维检查端点（8085，可由 `http_bind` 配置）。
**当前实现**:
- HTTP 只提供 `/health`。
- 仅 3 个 interval 循环执行真实工作：遥测快照预热、公告过期、设备会话清理；公告和会话数据分别由 admin/gateway 服务内部 API 修改。
- worker consumer group 注册 `webhook_retry_stream`、`ota_schedule_stream`、`comp_tx_stream`。Webhook 与 OTA 业务投递未实现，处理器返回失败；公共消费框架以 2s / 4s / 8s 间隔重试三次（加首次共四次尝试），再原子写入 Redis `{stream}.dlq` 并 ACK 原消息。`comp_tx_stream` 只接收 user 发布的 `refund_completed` 并幂等写入补偿结果审计记录，不执行退款或回滚。
- `alert_stream` 由 admin 消费；worker 的快照预热通过定时 HTTP 查询实现，不消费 `device_event_stream`。billing 当前不消费 `comp_tx_stream`。
- `scheduled_task` 尚未驱动 cron 或手动触发。账单结算、对账、OTA、Webhook、导出、归档、DLQ 重放等任务仍未完成。

> **本文件覆盖范围**:worker 服务不提供业务 HTTP API，但承担系统异步事件消费与定时任务职责。本文件约定:
> 1. **Stream 消费契约**(消费哪些 Stream + 如何处理 + 发什么事件)
> 2. **定时任务清单**(`scheduled_task` 表的所有 `task_code` + 触发时机 + 处理函数)
> 3. **关键任务流程详述**(对账 / OTA 推送 / Webhook 重试 / 数据归档)
> 4. **DLQ 处理约定**(失败消息兜底)

---

## 通用约定

### worker_db 表说明

| 表名 | 业务说明 |
| --- | --- |
| `scheduled_task` | 定时任务状态(`task_code` UNIQUE + cron 表达式 + 最近执行状态) |
| `task_execution_log` | 任务执行日志(每次执行一条,按月分区) |
| `comp_tx_log` | `refund_completed` 结果审计(`event_id` + 月份幂等) |
| `dlq_log` | 预留的 DLQ 数据库表；当前公共消费框架只写 Redis `{stream}.dlq` |
| `retry_queue` | 计划中的重试队列；当前 Webhook 消费不会写此表 |

### 幂等保证(关键)

- `comp_tx_stream` 当前处理器以 `event_id + created_month` 幂等；表唯一键为 `(tx_id, created_month)`。重复事件内容哈希或状态不同会报冲突并进入重试/DLQ。
- 其他两个 Stream 处理器目前始终失败，直到外部投递能力配置完成；它们不会写 `comp_tx_log`。

### 错误处理与 DLQ

- **Stream 消费失败**:首次尝试失败后按 2s / 4s / 8s 重试三次；随后原子写入 Redis `{stream}.dlq` 并确认原消息。当前不会写 `worker_db.dlq_log`、发告警或提供人工重放 API。若 DLQ 写入失败，原消息保留在 PEL。
- **定时任务失败**:`consecutive_fail_count` 累计 → ≥ 5 → 自动 `status='paused'` + 发 `alert_stream`
- **DLQ 重放**:尚未接入；`worker_db.dlq_log` 是预留表，当前没有消费者、管理接口或重放流程。

### 与其他服务的关系

- worker **不直接面向用户或 PC 后台**
- worker **通过 HTTP 调用 admin / gateway 服务内部端点**完成已落地的公告过期与会话清理；其余预期业务调用仍未完成
- worker **只对自己的 schema(worker_db)有读写权限**(§ 4.2)
- `GET /health` 检查 worker 数据库、Redis Cache 和 Redis Stream；依赖不可用时返回服务错误。
- worker 当前**没有**任务触发、任务状态、导出查询或 DLQ 管理 HTTP 端点。

---

## 零、计划中的内部 HTTP 端点（当前未实现）

> 以下为目标接口定义；目前 worker router 只注册 `/health`，这些路径尚未由 worker 提供。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/internal/export/tasks/{task_id}` | 查询导出任务状态(admin 端 `GET /api/v1/admin/export/tasks/{task_id}` 调此) |
| GET | `/api/v1/internal/scheduled-tasks/{task_code}/last-run` | 查询某个定时任务最近一次执行状态(供 admin 监控面板) |
| POST | `/api/v1/internal/scheduled-tasks/{task_code}/trigger` | 手动触发某个定时任务(供运维 / admin 调试用) |

### `GET /api/v1/internal/export/tasks/{task_id}`

**鉴权**:服务间共享密钥
**触发场景**:admin 端导出任务面板轮询(本期每 2s 一次)

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "task_id": 501,
    "task_code": "export_run",
    "status": "completed",                  // "queued" / "running" / "completed" / "failed"
    "estimated_rows": 12345,
    "estimated_size_mb": 5,
    "file_url": "https://oss.example.com/exports/orders-2026-09-25.csv?sign=xxx",
    "file_url_expires_at": "2026-09-25T17:30:00Z",  // OSS 临时签名 URL 30 min 过期
    "error_message": null,
    "created_at": "2026-09-25T17:00:00Z",
    "completed_at": "2026-09-25T17:00:30Z"
  }
}
```

**业务逻辑**:
1. 查 `worker_db.scheduled_task WHERE task_code='export_run' AND id=$task_id` → 不存在返回 `1004`
2. 从 `task_execution_log` 拿最近一次执行状态(STATUS / 耗时 / OSS 文件 URL)
3. `status='completed'` 时返回 `file_url`(OSS 预签名 URL,30 min 过期)
4. `status='failed'` 时返回 `error_message`(供 admin 端展示)

**错误码**:
- `1004`: 任务不存在
- `5003`: worker_db 暂时不可用

### `GET /api/v1/internal/scheduled-tasks/{task_code}/last-run`

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "task_code": "daily_refund_reconcile",
    "last_run_at": "2026-09-25T03:00:00Z",
    "last_run_status": "success",           // success / failed / timeout
    "last_run_duration_ms": 1234,
    "consecutive_fail_count": 0,
    "next_run_at": "2026-09-26T03:00:00Z"
  }
}
```

### `POST /api/v1/internal/scheduled-tasks/{task_code}/trigger`

**鉴权**:服务间共享密钥(限制仅 admin 服务可调)
**业务目标**:运维手动触发某个定时任务(例如某次对账失败后人工补跑)

**请求体**:
```json
{
  "trigger_reason": "对账失败人工补跑",  // 必填,写 task_execution_log.comment
  "force": false                          // true = 强制执行,即使 status='paused'
}
```

**业务逻辑**:
1. 查 `scheduled_task WHERE task_code=$code AND status IN ('enabled','paused')`
2. `force=false` 时,仅 `enabled` 状态可触发;`force=true` 时允许 paused 状态
3. 异步触发 `handler`(立即入队,不等下次 cron)
4. 写 `task_execution_log(triggered_by='admin_api', trigger_reason=...)`

**错误码**:
- `1004`: 任务码不存在
- `1005`: 状态不允许(`enabled` 之外 + `force=false`)

---

## 一、Stream 消费约定(worker 作为消费者)

worker 实际注册下列三个消费组。只有 `comp_tx_stream` 对当前支持的退款结果事件执行落库；Webhook / OTA 会在依赖能力未配置时失败并进入 Redis DLQ。

| Stream | 来源 | 处理逻辑 | 失败时 DLQ 目标 |
| --- | --- | --- | --- |
| `alert_stream` | gateway | 由 admin 消费并落库；worker 不重复消费 | admin 当前仅尝试发起 Webhook 事件 |
| `ota_schedule_stream` | 当前无可用生产者（目标为 admin） | worker 消费组已注册；处理器失败重试后进入 DLQ，设备分发未实现 | Redis `ota_schedule_stream.dlq` |
| `webhook_retry_stream` | admin（当前仅告警消费者发出缺少目标 URL 的 `alert_recorded`） | worker 无法投递，重试后进入 DLQ | Redis `webhook_retry_stream.dlq` |
| `comp_tx_stream` | user (`refund_completed`) | worker 校验并幂等写入 `worker_db.comp_tx_log`；仅做结果审计 | Redis `comp_tx_stream.dlq` |

> **不消费** `device_event_stream` / `charge_ended_stream` / `refund_required_stream` / `invoice_required_stream`；结束计费由 billing 处理，退款与发票事件由 admin 处理。

### 1.1 计划消费 `alert_stream`（当前仅由 admin 消费）

以下是目标流程，不是当前 worker 行为。worker 没有 `alert_stream` 消费组，也没有订阅匹配、Webhook 投递或 delivery log 写入；当前仅 admin 落告警并发出未包含目标 URL 的重试事件，worker 对该事件重试后写 DLQ。

**触发**:gateway 检测到设备越界 / 通信中断 / 温度异常 → 发 `alert_stream`
**处理流程**:

```
worker 消费 alert_stream 事件
  → 调 admin `POST /api/v1/admin/alerts`(内部,§ admin.md 未展开,此处新增:接收 alert 落库)
  → 查 admin_db.alert_subscription WHERE event_type = payload.event_type AND enabled = TRUE
  → 对每个订阅:调其 webhook URL(POST + HMAC 签名)
    → 成功 → 写 webhook_delivery_log(status='success')
    → 失败 → 入 retry_queue + 发 webhook_retry_stream(指数退避)
```

**幂等 key**:`alert_event.event_id`(由 gateway 生成)

### 1.2 计划消费 `ota_schedule_stream`（worker 处理器当前只将失败消息重试并写入 DLQ）

**目标触发**:admin 创建 OTA 调度 → 发 `ota_schedule_stream`(具体时刻由 `scheduled_window.start_at` 决定)。当前没有可用生产者，调度创建返回 503。
**处理流程**:

```
worker 消费 ota_schedule_stream 事件
  → 校验 scheduled_window.start_at ≤ NOW() ≤ scheduled_window.end_at
    → 否则等待(重入延迟队列)
  → 调 gateway `POST /api/v1/internal/devices/{device_id}/firmware-push`(路径见 gateway.md)
    → 成功 → 等设备 ACK(轮询 firmware-status 或 device 主动 ack-received)
    → 失败 → 触发自动回滚(若 auto_rollback_on_failure=true)
```

**幂等 key**:`ota_schedule.schedule_id` + `device_id`

### 1.3 计划消费 `webhook_retry_stream`（worker 处理器当前只将失败消息重试并写入 DLQ）

**目标触发**:Webhook 首次投递失败 → 入 `retry_queue` → 发 `webhook_retry_stream`。当前没有 Webhook 首次投递器或可用的重试事件目标。
**处理流程**:

```
worker 消费 webhook_retry_stream 事件
  → 查 retry_queue WHERE webhook_subscription_id = ? AND status = 'pending'
  → 按 backoff 策略重试(默认 1s/5s/30s/2min 四次,沿用 § 退款 SOP 经验值)
  → 成功 → UPDATE retry_queue.status='success', UPDATE webhook_delivery_log
  → 仍失败 → UPDATE retry_queue.status='failed' + 发 alert_stream(severity=critical)
```

### 1.4 已接入的 `comp_tx_stream` 结果审计

当前唯一已确认生产事件来自 user 微信退款回调：user 在同一事务内更新本服务的退款/支付记录，并向持久化 outbox 写入 `refund_completed`。Outbox 发布到本 Stream 后，worker 校验 UUID `event_id`、RFC3339 时间、退款单号及 `success`，计算完整 envelope 的 SHA-256，并按 `tx_id + created_month` 幂等写入 `worker_db.comp_tx_log`。成功结果记为 `committed`，失败结果记为 `failed`；重放时若 stream、哈希或状态不一致则报冲突。

该 consumer 是审计副作用，不会执行退款、回滚或通知 billing，也不能作为 Redis Stream 灾难恢复机制。billing 没有注册该 Stream 的消费者；其余服务也未接入补偿动作。

---

## 二、定时任务清单(`scheduled_task.task_code`)

> 下表为需求清单，不代表已由 `worker_db.scheduled_task` 驱动。当前代码使用硬编码 interval；除本文件开头列出的三个实际 worker 循环外，其余未接入。`scheduled_task` 管理、执行日志、失败暂停与手动触发均未实现。

| task_code | 类型 | cron | handler | 说明 |
| --- | --- | --- | --- | --- |
| `daily_refund_reconcile` | internal | `0 3 * * *`(每日 03:00) | `worker::reconcile::daily_refund` | 退款对账(微信账单 vs 内部 `refund_record`) |
| `daily_order_reconcile` | internal | `0 3 * * *`(每日 03:00) | `worker::reconcile::daily_order` | 订单对账(微信支付 vs 内部 `payment_order`) |
| `monthly_billing_settlement` | internal | `0 4 1 * *`(每月 1 日 04:00) | `worker::billing_cycle::monthly_settle` | 月结账单生成(分账参与方对账单) |
| `weekly_export_run` | internal | `0 5 * * 1`(每周一 05:00) | `worker::billing_cycle::weekly_export` | 周报导出 |
| `alert_threshold_scan` | internal | `*/5 * * * *`(每 5 min) | `worker::alert_scan::scan` | 阈值复核(扫描 gateway_db 写入的告警流,补漏) |
| `ota_schedule_poll` | internal | `*/1 * * * *`(每 1 min) | `worker::ota_schedule::poll` | OTA 推送窗口扫描(查 `scheduled_window.start_at` 到点) |
| `webhook_retry_poll` | internal | `*/1 * * * *`(每 1 min) | `worker::webhook_retry::poll` | Webhook 重试队列扫描(避免依赖 Stream) |
| `announcement_expire` | internal | `0 2 * * *`(每日 02:00) | `worker::announcement_expire::clean` | 公告过期清理(`valid_until < NOW()` 软删) |
| `data_retention` | internal | `0 6 1 * *`(每月 1 日 06:00) | `worker::data_retention::clean` | 数据归档(> 3 年物理归档至 OSS 冷存储) |
| `alert_active_resolve` | internal | `0 */6 * * *`(每 6 小时) | `worker::alert_scan::auto_resolve` | 自动关闭超时未处理的告警(> 7 天) |
| `dlq_replay_notice` | internal | `0 9 * * *`(每日 09:00) | `worker::dlq::daily_summary` | DLQ 日报(统计未处理数 + 发邮件给运维) |
| `risk_config_warm_cache` | internal | `*/30 * * * *`(每 30 min) | `worker::cache::warm_risk_config` | 风控配置缓存预热 |
| **`export_run`** | internal | **事件触发**(由 admin `POST /api/v1/admin/export/orders` 触发) | **`worker::export::run`** | **admin 导出任务执行器(本期承接 admin 端 `export_task` 表,跨服务方案) |

> 客户可新增 `customer_config` 类型任务(如"每日导出某站点订单"),通过 admin 后台"定时任务"页创建;`handler` 受限(`worker::customer::*` 命名空间),cron 表达式合法性校验。

---

## 三、关键任务流程详述

### 3.1 每日对账(`daily_refund_reconcile` / `daily_order_reconcile`)

**触发场景**:每日 03:00 自动跑(沿用 § 4 决策:每日对账)
**业务目标**:对比**昨日**微信账单 vs 内部订单,发现差异

**处理流程**:

```
1. 拉取昨日微信退款账单(**TODO**:精确的微信 V3 账单下载 API path
   本期未核实;候选:`/v3/bill/fundflowbill` 或 `/v3/pay/downloadfundflow`,
   待 AI 协作者开工前查微信支付 V3 文档确认)
   → 写入临时表 `wechat_refund_bill_temp`(Redis 缓存足够)
2. 拉取内部 user_db.refund_record WHERE created_at BETWEEN 昨日 00:00 - 今日 00:00
   → 通过 HTTP 调 user 服务(避免直连 user_db)
3. 按 wechat_refund_id 关联两表:
   - 完全匹配 → 不记录
   - 微信有 / 内部无 → 记差异 "wechat_only"(疑似内部漏单)
   - 内部有 / 微信无 → 记差异 "internal_only"(疑似微信漏单)
   - 金额不一致 → 记差异 "amount_mismatch"(差额)
4. 写入 admin_db.finance_reconcile_log(reconcile_date, diff_count, diff_amount_cents, status='auto_resolved' / 'manual_pending')
5. 若 diff_count > 0:
   - diff_count ≤ 5 且 total_diff ≤ 100 元 → status='auto_resolved'(自动合账,备注说明)
   - 否则 status='manual_pending'(待人工处理)+ 发 alert_stream(severity=high)
```

**幂等 key**:`reconcile_date`(每日一条,UNIQUE)

**输出**:客户运营在 PC 后台"对账日志"页查看(`GET /api/v1/admin/billing/reconcile-logs`,沿用 admin.md § F)

### 3.2 OTA 推送(`ota_schedule_poll` + `ota_schedule_stream` 消费)

**触发场景**:
- `ota_schedule_poll` 每 1 min 扫描 `admin_db.ota_schedule WHERE status='pending' AND scheduled_window.start_at <= NOW()`
- 命中后**直接发 `ota_schedule_stream` 事件**(admin 创建调度时已绑定的 `scheduled_window.start_at` 到点;立即执行场景走 admin.md § J `POST /api/v1/admin/ota/schedules/{sched_id}/execute`,由 admin 端直接发 Stream)
- worker 消费 `ota_schedule_stream` → 实际推送

**处理流程**:

```
ota_schedule_poll 扫描命中:
  → UPDATE ota_schedule.status='dispatching'
  → 发 ota_schedule_stream 事件(payload: schedule_id, package_id, target_filter)

worker 消费 ota_schedule_stream:
  → 按 target_filter 查 gateway_db.device(经 gateway HTTP /devices 内部端点)
  → 预演匹配设备数:SELECT COUNT(*) → 与 schedule 创建时的 preview_match_count 一致才执行
  → 对每个设备:发 firmware-push 指令(调 gateway /firmware-push)
  → 全部 ACK 后:
    - success → UPDATE ota_schedule.status='completed'
    - 部分失败 → UPDATE ota_schedule.status='partial_failure' + 自动回滚失败的设备
  → 发 alert_stream 通知客户运营
```

**幂等 key**:`schedule_id` + `device_id`

### 3.3 Webhook 重试(`webhook_retry_poll` + `webhook_retry_stream`)

**触发场景**:
- `webhook_retry_poll` 每 1 min 扫 `retry_queue WHERE status='pending' AND next_retry_at <= NOW()`
- 同时消费 `webhook_retry_stream`(admin 主动发起)

**处理流程**:

```
worker 扫 retry_queue 命中:
  → 查 webhook_subscription(target_url, sign_secret, retry_policy)
  → 按 backoff 策略(默认 [1s, 5s, 30s, 120s])重试
  → 成功 → UPDATE retry_queue.status='success', 写 webhook_delivery_log
  → 第 4 次仍失败 → UPDATE retry_queue.status='failed' + 发 alert_stream(critical)
```

**幂等 key**:`retry_queue.event_id`(每次重试独立,避免重复)

### 3.4 公告过期清理(`announcement_expire`)

**触发场景**:每日 02:00 自动跑

**处理流程**:

1. worker 调用 admin 内部 `POST /api/v1/internal/announcements/expire`。
2. admin 更新 `status='expired'` 的公告；`deleted_at` 不变，公告仍可在后台查询。
3. worker 记录受影响行数日志；持久 `task_execution_log` 尚未接入。

### 3.5 设备会话清理(`device_session_clean`)

每 5 分钟 worker 调 gateway 内部 `POST /api/v1/internal/device-sessions/cleanup-idle`。gateway 仅更新本服务 `gateway_db.device_session` 中 10 分钟无活动且未结束的会话，并返回 `closed_count`。worker 不直接访问 gateway schema。

### 3.6 数据归档(`data_retention`)

**触发场景**:每月 1 日 06:00 自动跑(沿用 § 4.6 数据保留 ≥ 3 年)

**处理流程**:

```
1. 滚动创建下月分区:
   - gateway_db.telemetry / telemetry_aggregate_*  / raw_frame_log / device_session
   - billing_db.fee_calculation
   - admin_db.audit_log / webhook_delivery_log / alert_event
   - worker_db.task_execution_log / dlq_log
2. 删除超 3 年分区:
   - gateway 遥测原始数据:> 1 个月 → 入冷存储(OSS 归档)+ 删 MySQL 分区
   - gateway / billing 聚合数据:> 3 年 → 入冷存储 + 删分区
   - audit_log / webhook_delivery_log / alert_event:> 3 年 → 入冷存储 + 删分区
3. 写 task_execution_log(archived_partitions, archived_size_bytes)
4. 失败 → 发 alert_stream(critical, "数据归档失败")
```

**注意**:归档**不删**订单主表 / 用户表 / 计费快照(订单主表不分表,永久保留 ≥ 3 年)

---

## 四、DLQ 处理约定（当前 Redis DLQ 尚无运维闭环）

### 4.1 当前行为

- consumer handler 首次调用失败后重试三次，间隔为 2s / 4s / 8s；四次总尝试仍失败时写入 Redis `{stream}.dlq`。
- 写入 DLQ 与确认原 Stream 消息通过 Redis Lua 脚本原子执行。DLQ 写入失败时不 ACK，消息保留在 PEL，稳定 consumer 重启后恢复。
- 当前 worker 不读 `{stream}.dlq`，不写 `worker_db.dlq_log`，也未提供 DLQ 查询、重放或关闭接口。
- `worker_db.dlq_log` 的实际列与索引见 [`db/worker.md`](../db/worker.md#dlq_log)；它目前是预留表。

### 4.2 尚未接入的运维流程

DLQ 运维需要实现分页查询、权限与审计、payload/错误展示、幂等重放、关闭/放弃及积压指标。目前不能通过更新 `worker_db.dlq_log.status` 重放 Redis 中的事件。

---

## 五、可观测性

### 5.1 关键指标(Prometheus,§ 6.3)

```promql
# Stream 消费 lag
redis_stream_lag_seconds{stream="alert_stream"} 30

# 定时任务成功率
rate(task_execution_total{status="success"}[5m])

# 任务执行时长
task_execution_seconds{task_name="daily_refund_reconcile", quantile=0.95}

# DLQ 积压（目标指标；当前尚未暴露）
redis_stream_dlq_length{stream="webhook_retry_stream"} 0

# 重试队列积压
retry_queue_pending_count 5
```

### 5.2 关键告警

| 触发条件 | 告警级别 | 通知通道 |
| --- | --- | --- |
| Stream lag > 5 min | critical | Webhook + 邮件 |
| 任务连续失败 ≥ 3 次 | high | Webhook + 邮件 |
| DLQ pending > 100 条 | mid | Webhook |
| 对账 diff_count > 5% | high | Webhook + 邮件 |
| 数据归档失败 | critical | Webhook + 邮件 |

---

## 六、安全与权限

- worker **不接受任何外部 HTTP 请求**(无 listen socket)
- worker **不调用任何用户可控 URL**(无 SSRF 风险)
- worker **对 worker_db 有完全权限**,对其他 schema **零权限**(§ 4.2)
- 跨服务 HTTP 调用使用**服务间共享密钥**,**强制 HTTPS** 内网

---

## 文档维护

- 修改本文件需在 PR 标题写 `api(worker): <简短描述>`
- **新增 / 修改定时任务**:
  1. 必须在 `worker_db.scheduled_task` 表 INSERT 对应记录
  2. 必须在本文件 § 二"定时任务清单"登记 `task_code` / `handler` / `cron`
  3. handler 路径变更必须同步更新 `services/worker/src/tasks/` 模块
- **新增 Stream 消费**:
  1. Stream 名必须从 § 5.1 8 个真实 Stream 中选
  2. 必须在本文件 § 一"Stream 消费约定"登记
  3. 必须配套写 DLQ 处理逻辑
- CI 检查:`scheduled_task` 表预置记录与本文件 § 二清单一致;`dlq_log.dlq_type` 枚举与本文件 § 四.4.2 一致
- **任务 cron 表达式变更**(客户在 PC 后台改):必须同步更新本文件 § 二(否则文档与代码漂移)
