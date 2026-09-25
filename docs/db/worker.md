# worker_db 数据库表设计

**所属服务**:worker(无对外接口,后台定时任务 + Redis Stream 消费者)
**Schema 名**:`worker_db`
**字符集 / 排序规则**:`utf8mb4` / `utf8mb4_unicode_ci`
**引擎**:InnoDB(全表)
**数据库版本**:MySQL 8.4 LTS

> **单客户部署约定**:worker_db 是**单客户专用数据库**,所有表都**不带 `customer_id` 列**。

> **软删除策略**:跨 schema 对账见 `docs/cross-reference.md` § 5.5(权威源),本文档通用约定与之一致;若冲突,以 cross-reference 为准。

## 通用约定

| 项目 | 约定 | 例外 |
| --- | --- | --- |
| 主键 | `BIGINT UNSIGNED AUTO_INCREMENT`,字段名 `id` | 无 |
| 时间戳 | `created_at` / `updated_at`,类型 `DATETIME(3)` | 无 |
| **软删除** | 启用:`deleted_at DATETIME(3) NULL` + `deleted_by` | **执行日志 / DLQ 日志不软删**(按月分区 + 物理归档) |
| 索引命名 | `pk_` / `uk_` / `idx_` 前缀 | 无 |
| 外键 | **不声明** | 无 |

## 表清单(5 张)

| 表名 | 业务说明 | 分表策略 | 估算行数(单客户 5 年) |
| --- | --- | --- | --- |
| `scheduled_task` | 定时任务状态(cron 任务跟踪) | 不分 | ~100 |
| `task_execution_log` | 任务执行日志(每次执行一条) | 按月分区 | ~300 万 |
| `comp_tx_log` | 补偿事务日志(跨服务最终一致性) | 不分 | ~50 万 |
| `dlq_log` | DLQ 处理日志(Redis Stream 失败消息) | 按月分区 | ~10 万 |
| `retry_queue` | 重试队列(支付 / Webhook 重试) | 不分 | ~100 万 |
| **`export_task`** | **导出任务**(账单 / 订单 / 对账 等)(P1-8 新增) | **不分** | **~5000/年** |

> **本文件首批设计 5 张表,P1-8 新增 `export_task` 表(导出任务定义 / 每次执行独立记录)**。

---

## 表 1:`worker_db.scheduled_task`

**业务说明**:**定时任务状态**。所有 cron 任务的配置 + 最近执行状态。`tokio-cron-scheduler` 调度,本表跟踪任务状态。

**关键业务规则**:

- **预置 + 客户自配**:系统初始化预置常用任务(如对账 / 备份 / 清理);客户可新增简单任务(如"每周报表导出")
- 任务类型:internal(系统预置)/ customer_config(客户自配)
- 软删除启用

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `task_code` | `VARCHAR(64)` | UNIQUE, NOT NULL | — | 任务代码(如 `daily_refund_reconcile` / `monthly_billing_settlement`) |
| `task_name` | `VARCHAR(128)` | NOT NULL | — | 任务名称 |
| `task_type` | `ENUM('internal','customer_config')` | NOT NULL | — | 任务类型 |
| `cron_expression` | `VARCHAR(64)` | NOT NULL | — | cron 表达式(如 `0 3 * * *` = 每日 03:00) |
| `handler` | `VARCHAR(128)` | NOT NULL | — | 处理函数路径(如 `worker::refund::daily_reconcile`) |
| `config` | `JSON` | NULL | NULL | 任务配置(如"对账 lookback_hours:24") |
| `status` | `ENUM('enabled','disabled','paused')` | NOT NULL | `'enabled'` | 启用 / 停用 / 暂停 |
| `last_run_at` | `DATETIME(3)` | NULL | NULL | 最近执行时间 |
| `last_run_status` | `ENUM('success','failed','timeout')` | NULL | NULL | 最近执行状态 |
| `last_run_duration_ms` | `INT UNSIGNED` | NULL | NULL | 最近执行耗时(毫秒) |
| `next_run_at` | `DATETIME(3)` | NULL | NULL | 下次计划执行时间 |
| `consecutive_fail_count` | `TINYINT UNSIGNED` | NOT NULL | `0` | 连续失败次数(≥ 5 → 自动暂停 + 告警) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_scheduled_task` | `id` | 主键 | — |
| `uk_scheduled_task_code` | `task_code` | 唯一 | 按 code 查 |
| `idx_scheduled_task_status_next_run` | `status`, `next_run_at` | 普通 | 调度器查"待执行"任务 |
| `idx_scheduled_task_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `consecutive_fail_count >= 5` → 自动 `status='paused'`(应用层 + worker 启动时校验)
- `status='enabled'` 时,`cron_expression` 必须合法(应用层校验)

### 关系

- 一对多 → `task_execution_log.task_id`

### 业务规则

- **预置任务**:系统初始化脚本 INSERT 10+ 常用任务:
  - `daily_refund_reconcile`(每日 03:00 退款对账)
  - `monthly_billing_settlement`(每月 1 日 02:00 账单生成)
  - `daily_telemetry_archive`(每日 04:00 遥测归档)
  - `hourly_data_retention`(每小时 检查数据保留期)
  - `daily_announcement_expire`(每日 05:00 公告过期清理)
  - `daily_webhook_log_archive`(每日 04:30 Webhook 日志归档)
  - ...
- **执行**:调度器按 `cron_expression` 触发 → INSERT `task_execution_log(status='running')` → 执行 handler → UPDATE `status='success'/'failed'` + `last_run_*` 字段
- **失败告警**:`consecutive_fail_count >= 3` → 推送告警;≥ 5 → 自动暂停 + 推送紧急告警
- **客户自配**(二期):客户在 PC 后台"任务管理"新增 → 选预置 handler + 配 cron → INSERT

---

## 表 2:`worker_db.task_execution_log`

**业务说明**:**任务执行日志**(每次执行一条)。记录每次任务的开始 / 结束 / 状态 / 错误。**按月分区**,超 6 个月物理归档。

**关键业务规则**:

- **不软删除**:日志类,按月分区
- 用于排障 + 性能分析

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `task_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `scheduled_task.id` |
| `task_code` | `VARCHAR(64)` | NOT NULL | — | 任务代码(冗余,便于查询) |
| `started_at` | `DATETIME(3)` | NOT NULL | — | 开始时间 |
| `finished_at` | `DATETIME(3)` | NULL | NULL | 结束时间(NULL = 仍在执行) |
| `duration_ms` | `INT UNSIGNED` | NULL | NULL | 耗时(毫秒) |
| `status` | `ENUM('running','success','failed','timeout','cancelled')` | NOT NULL | — | 执行状态 |
| `error_message` | `VARCHAR(1024)` | NULL | NULL | 错误信息 |
| `error_stack` | `TEXT` | NULL | NULL | 错误堆栈(限长,避免过大) |
| `affected_rows` | `INT UNSIGNED` | NULL | NULL | 影响行数(如"清理过期公告 123 条") |
| `trigger_source` | `ENUM('cron','manual','retry','event')` | NOT NULL | — | 触发来源 |
| `partition_key` | `DATE` | NOT NULL | — | 分区键 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_task_execution_log` | `id` | 主键 | — |
| `idx_task_execution_log_task_started` | `task_id`, `started_at` | 普通 | 查某任务的执行历史 |
| `idx_task_execution_log_status_started` | `status`, `started_at` | 普通 | 查失败 / 超时的执行 |
| `idx_task_execution_log_running` | `status`, `started_at` | 普通 | 查仍在执行的任务 |

### 约束

- `status IN ('success','failed','timeout','cancelled')` 时,`finished_at` / `duration_ms` NOT NULL
- `status IN ('failed','timeout')` 时,`error_message` NOT NULL

### 关系

- 多对一 → `scheduled_task.id`

### 业务规则

- **记录**:任务开始 INSERT `status='running'`,结束 UPDATE `status + finished_at + duration_ms + error_*`
- **超时检测**:任务执行 > 30 min → 标记 `status='timeout'`(应用层 timer)
- **排障**:PC 后台"任务日志"页 → 按 task / status / 时间范围筛选
- **物理归档**:worker 每日扫表 → `started_at < NOW() - 6 MONTH` → `DELETE`(DROP PARTITION)

---

## 表 3:`worker_db.comp_tx_log`

**业务说明**:**补偿事务日志**(跨服务最终一致性,§ 5.4)。当跨服务事件链出现失败时,记录补偿操作(回滚 / 重试 / 人工介入)。

**关键业务规则**:

- **SAGA 模式记录**:每个跨服务事务的状态机推进
- 软删除启用:异常补偿软删

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `tx_id` | `VARCHAR(64)` | UNIQUE, NOT NULL | — | 补偿事务 ID(UUID) |
| `saga_name` | `VARCHAR(64)` | NOT NULL | — | SAGA 名称(如 `refund_saga` / `charging_saga`) |
| `current_step` | `VARCHAR(64)` | NOT NULL | — | 当前步骤 |
| `total_steps` | `TINYINT UNSIGNED` | NOT NULL | — | 总步骤数 |
| `status` | `ENUM('running','compensating','compensated','failed','manual_review')` | NOT NULL | `'running'` | 状态 |
| `forward_payload` | `JSON` | NOT NULL | — | 正向操作 payload |
| `compensate_payload` | `JSON` | NULL | NULL | 补偿操作 payload |
| `retry_count` | `TINYINT UNSIGNED` | NOT NULL | `0` | 已重试次数 |
| `max_retry` | `TINYINT UNSIGNED` | NOT NULL | `3` | 最大重试次数 |
| `last_error` | `VARCHAR(1024)` | NULL | NULL | 最近错误 |
| `manual_review_note` | `VARCHAR(512)` | NULL | NULL | 人工审核备注 |
| `next_retry_at` | `DATETIME(3)` | NULL | NULL | 下次重试时间(指数退避) |
| `completed_at` | `DATETIME(3)` | NULL | NULL | 完成时间 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_comp_tx_log` | `id` | 主键 | — |
| `uk_comp_tx_log_tx_id` | `tx_id` | 唯一 | 按事务 ID 查 |
| `idx_comp_tx_log_saga_status` | `saga_name`, `status`, `created_at` | 普通 | 查某 SAGA 的所有事务 |
| `idx_comp_tx_log_status_retry` | `status`, `next_retry_at` | 普通 | 待重试事务 |
| `idx_comp_tx_log_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `status IN ('compensated','failed','manual_review')` 时,`completed_at` NOT NULL
- `status='manual_review'` 时,`manual_review_note` NOT NULL
- `retry_count <= max_retry`

### 关系

- 无外键(逻辑关联)

### 业务规则

- **触发**:跨服务事件消费失败 → 启动补偿 → INSERT `comp_tx_log(status='running')`
- **重试**:指数退避(2s / 10s / 1min / 10min),`retry_count += 1`,`next_retry_at` 更新
- **人工介入**:`retry_count > max_retry` → `status='manual_review'` + 推送告警
- **手动恢复**:运维在 PC 后台"补偿事务管理"页面手动触发重试 / 标记完成

---

## 表 4:`worker_db.dlq_log`

**业务说明**:**DLQ 处理日志**(Redis Stream 失败消息的兜底)。当某个 Stream 消费 3 次失败后,消息进入 `{stream}.dlq`,worker 周期任务扫描处理。

**关键业务规则**:

- **按月分区 + 物理归档**(超 6 个月)
- **不软删除**

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `stream_name` | `VARCHAR(64)` | NOT NULL | — | 失败的 Stream 名(如 `refund_required_stream`) |
| `original_event_id` | `VARCHAR(64)` | NOT NULL | — | 原始 event_id |
| `event_payload` | `JSON` | NOT NULL | — | 事件 payload(完整) |
| `failure_reason` | `VARCHAR(512)` | NOT NULL | — | 失败原因 |
| `retry_count` | `TINYINT UNSIGNED` | NOT NULL | `0` | DLQ 重试次数 |
| `status` | `ENUM('pending','retrying','resolved','abandoned')` | NOT NULL | `'pending'` | DLQ 处理状态 |
| `last_retry_at` | `DATETIME(3)` | NULL | NULL | 最近重试时间 |
| `resolved_at` | `DATETIME(3)` | NULL | NULL | 解决时间 |
| `resolution` | `ENUM('re_consumed','manual_fixed','abandoned')` | NULL | NULL | 解决方案 |
| `resolution_note` | `VARCHAR(512)` | NULL | NULL | 解决备注 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 进入 DLQ 时间 |
| `partition_key` | `DATE` | NOT NULL | — | 分区键 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_dlq_log` | `id` | 主键 | — |
| `uk_dlq_log_stream_event` | `stream_name`, `original_event_id` | 唯一 | 防重复入 DLQ |
| `idx_dlq_log_status_created` | `status`, `created_at` | 普通 | 运维查待处理 DLQ |
| `idx_dlq_log_stream_status` | `stream_name`, `status`, `created_at` | 普通 | 按流名查 DLQ |

### 约束

- `status IN ('resolved','abandoned')` 时,`resolved_at` / `resolution` NOT NULL

### 关系

- 无外键

### 业务规则

- **入队**:Redis Stream 消费失败 3 次后,worker 自动写 `{stream}.dlq` + INSERT `dlq_log`
- **重试**:worker 周期任务(每小时)扫表 → `status='pending'` → 重新消费原始 event → 成功 → UPDATE `status='resolved'`
- **手动修复**:复杂问题需运维查 payload + 手动处理 → 标记 `status='resolved', resolution='manual_fixed'`
- **放弃**:`status='abandoned'` 表示确认丢弃(罕见,如事件数据本身无效)
- **物理归档**:worker 每日扫表 → `created_at < NOW() - 6 MONTH` → `DELETE`(DROP PARTITION)

---

## 表 5:`worker_db.retry_queue`

**业务说明**:**重试队列**(支付 / Webhook / 退款等需要重试的操作)。记录每次重试的状态 + 失败原因 + 下次重试时间。

**关键业务规则**:

- **退避策略**:指数退避(2s / 10s / 1min / 10min / 1h / 6h)
- **最大重试**:默认 3-5 次(按队列类型配置)
- 软删除启用

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `queue_type` | `ENUM('wechat_pay','wechat_refund','webhook_delivery','email_send','bank_transfer')` | NOT NULL | — | 队列类型 |
| `biz_id` | `VARCHAR(64)` | NOT NULL | — | 业务 ID(如 `payment_order.id` / `refund_record.id` / `webhook_delivery_log.id`) |
| `payload` | `JSON` | NOT NULL | — | 重试 payload(完整) |
| `attempt_count` | `TINYINT UNSIGNED` | NOT NULL | `0` | 已尝试次数 |
| `max_attempts` | `TINYINT UNSIGNED` | NOT NULL | `3` | 最大尝试次数 |
| `next_retry_at` | `DATETIME(3)` | NOT NULL | — | 下次重试时间 |
| `last_attempt_at` | `DATETIME(3)` | NULL | NULL | 最近尝试时间 |
| `last_error` | `VARCHAR(1024)` | NULL | NULL | 最近错误 |
| `status` | `ENUM('pending','in_progress','success','failed','abandoned')` | NOT NULL | `'pending'` | 队列项状态 |
| `is_permanent_failure` | `BOOLEAN` | NOT NULL | `FALSE` | 是否永久失败(不再重试,需人工) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 入队时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_retry_queue` | `id` | 主键 | — |
| `idx_retry_queue_status_next_retry` | `status`, `next_retry_at` | 普通 | worker 周期扫表(取待重试项) |
| `idx_retry_queue_type_status` | `queue_type`, `status`, `created_at` | 普通 | 按队列类型统计 |
| `idx_retry_queue_biz` | `queue_type`, `biz_id` | 普通 | 反查"某业务的重试历史" |
| `idx_retry_queue_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `attempt_count <= max_attempts`
- `status='success'` 时,`attempt_count >= 1`
- `status='failed'` 时,`attempt_count = max_attempts` + `is_permanent_failure=TRUE`

### 关系

- 多对一(逻辑关联):`payment_order.id` / `refund_record.id` / `webhook_delivery_log.id`(跨服务无外键)

### 业务规则

- **入队**:业务执行失败 + 错误码属于"可重试"类 → INSERT `retry_queue(next_retry_at=NOW() + 2s)`
- **执行**:worker 周期任务(每分钟)扫表 → `status='pending' AND next_retry_at <= NOW()` → 执行 → 成功:UPDATE `status='success'`;失败:`attempt_count += 1`,重算 `next_retry_at`(指数退避)
- **永久失败**:`attempt_count = max_attempts` → `status='failed', is_permanent_failure=TRUE` + 推送告警 → 进入 DLQ 处理
- **退避间隔**:`2s → 10s → 1min → 10min → 1h → 6h`(超过 max_attempts 后停止)

---

## 表 6:`worker_db.export_task`(P1-8 新增)

**业务说明**:**导出任务**。每个导出请求 = 一行(账单 / 订单 / 对账 等)。**独立于 `scheduled_task`** —— `scheduled_task` 是定时任务定义,`export_task` 是具体导出任务实例。
> **为什么需要独立表**:`scheduled_task.task_code` 唯一约束 → 一个 `export_run` 不能多次;但客户财务实际有多个并发导出任务(本期账单 + 上期账单 + 半年对账 等),需要独立记录每份任务的状态 / 文件 / 过期时间。

**关键业务规则**:

- 触发方式:`POST /api/v1/admin/exports` 创建 + 由 `scheduled_task.task_code='export_run'` 周期触发
- **状态机**:`pending` → `running` → `completed` / `failed` → (30 天后)物理清理
- **文件存储**:上传到 OSS(本地 MinIO 或客户自购),URL 预签名(30 min 过期)
- **可重跑**:`status='failed'` 可由客户财务手动触发重跑(创建新行,旧行不删)

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `export_no` | `CHAR(32)` | UNIQUE, NOT NULL | — | 业务导出单号,格式 `EX + YYYYMMDDHHmmss + 10 位随机` |
| `export_type` | `ENUM('billing_statement','order_history','refund_log','reconcile_diff','customer_list')` | NOT NULL | — | 导出类型 |
| `format` | `ENUM('csv','excel','pdf')` | NOT NULL | — | 输出格式 |
| `params` | `JSON` | NULL | NULL | 导出参数(如 `{"date_range": {"from": "2026-01-01", "to": "2026-01-31"}}`) |
| `trigger_source` | `ENUM('manual','scheduled')` | NOT NULL | — | 触发来源:PC 后台手动 / `export_run` 周期任务 |
| `triggered_by` | `BIGINT UNSIGNED` | NULL | NULL | 触发人 ID(`trigger_source='manual'` 时填 admin user_id) |
| `scheduled_task_id` | `BIGINT UNSIGNED` | NULL | NULL | 关联 `scheduled_task.id`(`trigger_source='scheduled'` 时填) |
| `status` | `ENUM('pending','running','completed','failed','expired')` | NOT NULL | `'pending'` | 任务状态 |
| `started_at` | `DATETIME(3)` | NULL | NULL | 执行开始时间 |
| `completed_at` | `DATETIME(3)` | NULL | NULL | 执行完成时间(success / failed) |
| `duration_seconds` | `INT UNSIGNED` | NULL | NULL | 耗时(秒,completed 时填) |
| `file_url` | `VARCHAR(512)` | NULL | NULL | OSS 预签名 URL |
| `file_size_bytes` | `BIGINT UNSIGNED` | NULL | NULL | 文件大小 |
| `file_expires_at` | `DATETIME(3)` | NULL | NULL | OSS 预签名 URL 过期时间(创建后 30 min) |
| `row_count` | `INT UNSIGNED` | NULL | NULL | 导出记录数(用于审计) |
| `error_message` | `VARCHAR(1024)` | NULL | NULL | 失败原因(`status='failed'` 时填) |
| `attempt_count` | `TINYINT UNSIGNED` | NOT NULL | `0` | 已尝试次数(手动重跑会 +1) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_export_task` | `id` | 主键 | — |
| `uk_export_task_no` | `export_no` | 唯一 | 单号追溯 |
| `idx_export_task_status_created` | `status`, `created_at` | 普通 | PC 后台"我的导出任务"列表 |
| `idx_export_task_type_created` | `export_type`, `created_at` | 普通 | 统计各类型导出频次 |
| `idx_export_task_triggered_by` | `triggered_by`, `created_at` | 普通 | 查某 admin 用户的所有导出 |
| `idx_export_task_scheduled` | `scheduled_task_id`, `created_at` | 普通 | 周期任务的导出历史 |

### 约束

- **状态机**:`pending` → `running` → `completed` / `failed` → `expired`(30 天后)
- `status='completed'` 时,`file_url` / `file_size_bytes` / `row_count` / `duration_seconds` NOT NULL
- `status='failed'` 时,`error_message` NOT NULL
- **不可软删除**:导出任务是审计证据,30 天后物理清理(合规底线)

### 业务规则

- **创建**(PC 后台):客户财务 / 运营在 admin PC 后台"导出中心"选类型 + 格式 + 时间范围 → 提交 → INSERT `export_task(status='pending', trigger_source='manual', triggered_by=$user_id)`
- **执行**:worker 周期任务扫表 `status='pending'` → 调 billing / user 内部接口生成数据 → 上传 OSS → INSERT `file_url` + UPDATE `status='completed', completed_at, row_count`
- **周期触发**:`scheduled_task.task_code='export_run'` cron 触发 → 创建一批 `export_task(trigger_source='scheduled', scheduled_task_id=$id)`,不直接执行
- **重跑**:PC 后台"重跑"按钮 → INSERT 新行(`attempt_count=1`),旧行保留(审计)
- **OSS 预签名 URL 过期**:`file_expires_at` 后 URL 失效,客户财务需要重新下载 → 调 admin 内部接口 `/api/v1/internal/export/tasks/{id}/refresh-url` 重新签名
- **物理归档**:worker 周期任务每日 04:00 扫表 → `status='expired' AND completed_at < NOW() - 30 DAY` → `DELETE`

### 与 scheduled_task 的关系

```
scheduled_task (task_code='export_run')
  ↓ 周期触发(每日 03:00)
  ↓ 创建 N 个 export_task 实例
  ↓ 每个实例由 worker 异步执行
export_task (status=pending/running/completed/failed)
```

**注意**:`scheduled_task` 是"任务定义"(1 行),`export_task` 是"任务实例"(N 行)。两者职责清晰分离。

---

**worker_db 全部 6 张表设计完成**(P1-8 新增 `export_task`)

---

# 所有 5 个 schema 表设计完成 ✅

| Schema | 表数 | 文件 |
|---|---|---|
| `user_db` | 14 | `docs/db/user.md` |
| `admin_db` | 23 | `docs/db/admin.md` |
| `gateway_db` | 6 | `docs/db/gateway.md` |
| `billing_db` | 5 | `docs/db/billing.md` |
| `worker_db` | **6**(P1-8 新增 `export_task`) | `docs/db/worker.md` |
| **合计** | **53 张**(P0-2 前)/ **54 张**(P1-8 后新增 1 张 `export_task`) | 5 个文件 |

**所有 schema 的设计原则一致**:
- 单客户部署,所有表不带 `customer_id`
- 业务表软删除(7 个字段:`deleted_at` + `deleted_by` + 索引)
- 配置 / 日志类表不软删(按月分区 + 物理归档)
- 金额统一单位**分**(`BIGINT`)
- 加密字段统一 `AES_ENCRYPT`
- 外键全部不声明(跨服务最终一致性)

**数据库迁移**:
- 所有 schema 由 `sqlx-cli` 管理,服务启动时自动 `sqlx::migrate!()` 应用
- 迁移单向不回滚,破坏性变更走"加列 → 写双写 → 切读 → 删旧列"4 阶段(§ 13.2)

**下一阶段建议**:
1. AI 辅助开发协作者可基于本文档 + `docs/技术规格.md` + `docs/需求分析.md` 直接生成 `services/*/migrations/*.sql`
2. 或继续补充"接口契约 / OpenAPI" / "前端组件清单"等设计文档
3. 或开始搭脚手架(初始化 Monorepo + 各 Cargo crate + 各 Vue/React 工程)
