# worker 服务任务定义与 Stream 消费约定

**服务**:`worker`(`services/worker`)
**对外地址**:**无 HTTP**(技术规格 § 3.5)
**主入口**:
- **Redis Stream 消费者**(`webhook_retry_stream` / `alert_stream` / `ota_schedule_stream` / `comp_tx_stream`)
- **定时任务调度**(`tokio-cron-scheduler`,写 `worker_db.scheduled_task` 表)
- **HTTP 回调**(调内部 HTTP API,不暴露端口)

> **本文件覆盖范围**:worker 服务**没有对外 HTTP API**,但承担系统所有**异步事件消费 + 定时任务调度**职责。本文件约定:
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
| `comp_tx_log` | 补偿事务日志(跨服务最终一致性,`event_id` 幂等) |
| `dlq_log` | DLQ 处理日志(Redis Stream 失败消息,按月分区) |
| `retry_queue` | 重试队列(支付 / Webhook 重试) |

### 幂等保证(关键)

- 所有 Stream 消费 + 定时任务都**必须幂等**:`event_id`(Stream) / `task_code + run_id`(定时任务) 作为幂等 key
- 写入 `worker_db.comp_tx_log` 记录已处理的 event_id(UNIQUE 约束)
- 重启 / 重消费 → 先查 `comp_tx_log` → 已处理则直接跳过

### 错误处理与 DLQ

- **Stream 消费失败**(consumer error):写入 `worker_db.dlq_log` + 发 `alert_stream` 通知(severity=critical)
- **定时任务失败**:`consecutive_fail_count` 累计 → ≥ 5 → 自动 `status='paused'` + 发 `alert_stream`
- **DLQ 重放**:人工介入(`worker_db.dlq_log.status='replayed'` 后重新消费)

### 与其他服务的关系

- worker **不直接面向用户或 PC 后台**
- worker **通过 HTTP 回调 admin / user / gateway / billing 服务**实现业务动作(详见各任务流程)
- worker **只对自己的 schema(worker_db)有读写权限**(§ 4.2)
- worker **对外提供 3 个内部 HTTP 端点**(详见 § 零),供 admin 等服务查询任务状态(因为任务存于 `worker_db.scheduled_task`,admin 服务不直连)

---

## 零、内部 HTTP 端点(共 3 个)

> 所有路径在 worker 服务监听 `:8085`(**仅内网可达**,Docker Compose 内服务间调用);鉴权为服务间共享密钥(§ 通用约定)。
> worker 服务以 **Stream 消费 + 定时任务** 为主,本节 HTTP 端点仅供 **任务状态查询**(因为任务数据在 `worker_db`,其他服务通过 HTTP 拉,避免跨 schema 直连)。

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

worker 服务**主动消费**以下 4 个 Stream(沿用 § 5.1):

| Stream | 来源 | 处理逻辑 | 失败时 DLQ 目标 |
| --- | --- | --- | --- |
| `alert_stream` | gateway | 落库到 `admin_db.alert_event`(调 admin 内部 API)+ 按 `alert_subscription` 推 Webhook | `dlq_log.alert_dlq` |
| `ota_schedule_stream` | admin | 调 gateway `POST /firmware-push`(§ 5.1 gateway.md 已落地路径) | `dlq_log.ota_dlq` |
| `webhook_retry_stream` | admin | 重试失败的 Webhook 投递(指数退避) | `dlq_log.webhook_dlq` |
| `comp_tx_stream` | 各服务 | 跨服务最终一致性确认 / 失败补偿 | `dlq_log.comp_tx_dlq` |

> **不消费** `device_event_stream` / `charge_ended_stream` / `refund_required_stream` / `invoice_required_stream`(分别由 admin / billing / user 处理)。

### 1.1 消费 `alert_stream`

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

### 1.2 消费 `ota_schedule_stream`

**触发**:admin 创建 OTA 调度 → 发 `ota_schedule_stream`(具体时刻由 `scheduled_window.start_at` 决定)
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

### 1.3 消费 `webhook_retry_stream`

**触发**:admin 推 Webhook 失败 → 入 `webhook_retry_queue` → 发 `webhook_retry_stream`
**处理流程**:

```
worker 消费 webhook_retry_stream 事件
  → 查 retry_queue WHERE webhook_subscription_id = ? AND status = 'pending'
  → 按 backoff 策略重试(默认 1s/5s/30s/2min 四次,沿用 § 退款 SOP 经验值)
  → 成功 → UPDATE retry_queue.status='success', UPDATE webhook_delivery_log
  → 仍失败 → UPDATE retry_queue.status='failed' + 发 alert_stream(severity=critical)
```

### 1.4 消费 `comp_tx_stream`

**触发**:跨服务事务完成 / 失败,各服务统一发 `comp_tx_stream`
**处理流程**:

```
worker 消费 comp_tx_stream 事件
  → 写入 worker_db.comp_tx_log(event_id, status, result, processed_at=NOW())
    → 已存在 → 跳过(幂等)
  → 若 status='failed' → 发 alert_stream(severity=mid) + 通知相关服务
```

---

## 二、定时任务清单(`scheduled_task.task_code`)

> 所有任务**预置**于 `worker_db.scheduled_task`,客户可禁用 / 启用,但不能修改 `handler`(`internal` 类型)。`customer_config` 类型允许新增(简单任务)。

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

```
1. UPDATE admin_db.announcement SET deleted_at=NOW(), deleted_by=NULL
   WHERE valid_until < NOW() AND deleted_at IS NULL
   → 软删,保留审计
2. 写 task_execution_log(rows_affected, duration_ms, status)
```

### 3.5 数据归档(`data_retention`)

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

## 四、DLQ 处理约定

### 4.1 触发 DLQ 的场景

- Stream 消费失败(连续 3 次)
- 定时任务连续 5 次失败(自动暂停)
- HTTP 回调下游失败 + 重试耗尽

### 4.2 DLQ 表结构(`worker_db.dlq_log`)

| 字段 | 说明 |
| --- | --- |
| `id` | 主键 |
| `dlq_type` | `alert_dlq` / `ota_dlq` / `webhook_dlq` / `comp_tx_dlq` / `task_dlq` |
| `event_id` | 原始 event_id(便于追溯) |
| `payload` | JSON(原始 Stream payload 或任务参数) |
| `error_message` | 失败原因 |
| `attempt_count` | 尝试次数 |
| `first_failed_at` | 首次失败时间 |
| `last_retry_at` | 最近重试时间 |
| `status` | `pending` / `replayed` / `abandoned` |
| `resolved_by` | 处理人(admin user id) |
| `resolved_at` | 处理时间 |

### 4.3 DLQ 重放流程

```
运维在 PC 后台"DLQ 管理"页(本期简化为日志查询,二期做交互):
  → 查看 dlq_log WHERE status='pending'
  → 人工判断:
    - 重放:UPDATE status='replayed', re-emit 原 event 到 Stream → 重走消费
    - 放弃:UPDATE status='abandoned' + 备注原因
```

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

# DLQ 积压
dlq_pending_count{dlq_type="webhook_dlq"} 0

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