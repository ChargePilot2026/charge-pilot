# API ↔ DB ↔ Stream 一致性对账文档

> **目的**:防止三个层次的文档(API / DB / Stream)漂移。任何新增 / 修改 / 删除必须**同步更新本文档对应行**,否则 CI 拒绝合并。
> **维护工具**:`tools/check-api-consistency.ts`(检查端点路径、Stream 名、表名是否在文档中一致出现)
> **最近一次同步**:2026-09-26(随 6 次 API docs commits 落地)

> **Redis 实例拆分(P0-3 固化)**:业务缓存与事件流分两个 Redis 容器,避免 allkeys-lru 误淘汰 Stream 事件:
> - `chargepilot-redis-cache`:DB 0,`allkeys-lru`,业务缓存(`snapshot:{order_id}` 等)
> - `chargepilot-redis-stream`:DB 0,**`noeviction`**(Stream 不能被 LRU 淘汰),事件流(`device_event_stream` 等 11 个)
> 两个 Redis **独立 `REDIS_PASSWORD`**,网络层走同一 `internal` docker network。
> 详见 `docs/技术规格.md` § 4.7 + `examples/docker-compose.yml`。

> **MySQL 8.4 分区约束(P0-2 固化)**:所有按月分区表,**主键 + 所有 UNIQUE 索引都必须包含分区字段**(否则 `ERROR 1503`)。
> 分区字段统一用 generated column 命名:
> - 按 `created_at` 分区:`created_month DATE`(默认)
> - 聚合表按聚合时间:`bucket_month` / `hour_month`
> 详见 `docs/技术规格.md` § 4.8 + 各 `db/*.md` 表的"索引"段。

> **8 张核心按月分区表已落地 P0-2**:`user_db.charge_order` / `user_db.payment_order` / `user_db.refund_record` / `user_db.wallet_txn` / `user_db.feedback` / `billing_db.fee_calculation` / `gateway_db.telemetry_aggregate_15min` / `gateway_db.telemetry_aggregate_hourly`。
> 其余按月分区表(admin 审计 / alert_event / webhook_delivery_log;gateway device_session / raw_frame_log / ota_command;worker task_execution_log / dlq_log 等)按相同模式迁移,本期不展开(由代码动工时按本规则生成)。

---

## § 1 Stream 名总账(§ 技术规格 5.1,**11 个**)

| Stream 名 | 生产者 | 消费者 | 用途 | 在哪些 API 文档中出现 |
| --- | --- | --- | --- | --- |
| `device_event_stream` | gateway | admin / user | 设备状态变更 + 充电中快照缓存填充 | gateway.md(§ 六), admin.md(跨服务调用), user.md(轮询) |
| `alert_stream` | gateway | admin / worker | 告警事件(Webhook 推送) | gateway.md(§ 六), admin.md(§ E 告警), worker.md(§ 一.1.1) |
| **`charge_started_stream`** | **user** | **gateway** | **充电启动(微信支付回调成功后 → 下发设备启动指令)** | **user.md § 公开接口 payment/wechat/callback,技术规格 § 5.4** |
| `charge_ended_stream` | gateway | billing / user | 充电结束(触发计费 / 分账 / 退款 + 关闭 user 轮询) | gateway.md(§ 五), billing.md(§ 六), user.md(轮询关闭) |
| `refund_required_stream` | billing | admin / user | 退款触发(自动退款 / 调微信 API) | billing.md(§ 六), admin.md(§ F), user.md(退款编排) |
| `invoice_required_stream` | billing | admin | 发票申请(待人工审核) | billing.md(§ 六), admin.md(§ F) |
| `webhook_retry_stream` | admin | worker | Webhook 失败重试 | admin.md(§ I), worker.md(§ 一.1.3) |
| `ota_schedule_stream` | admin | worker / gateway | OTA 推送调度 | admin.md(§ J), worker.md(§ 一.1.2), gateway.md(§ 五) |
| `comp_tx_stream` | 各服务 | 各服务 | 跨服务补偿事务 | billing.md(§ 六), worker.md(§ 一.1.4), gateway.md(§ 五) |
| `coupon_grant_required_stream` | admin | user | 运营活动发券请求;user 写 `coupon_grant` | admin.md(§ G), user.md(优惠券) |
| `pricing_rule_changed_stream` | admin | billing | 计费规则版本更新通知 | admin.md(§ K), billing.md(计费快照) |

> **约束**:11 个 Stream 是穷举的。新增 Stream 必须先在技术规格 § 5.1 登记,再在本表登记,再在 API 文档中使用。

---

## § 2 数据库表总账(5 schema,**61 张**)

### 2.1 user_db(18 张)

| 表名 | 服务的端点引用 | 关键端点 |
| --- | --- | --- |
| `user` | `user.md` § 用户与钱包 | `POST /auth/login` / `GET /profile` |
| `port_view` | `user.md` § 站点与找桩 | `GET /station/nearby` |
| `wallet_account` | `user.md` § 用户与钱包 | `GET /wallet/balance` / `POST /wallet/recharge` |
| `wallet_txn` | `user.md` § 用户与钱包 | `GET /wallet/txns` |
| `coupon` | `user.md` § 优惠券与发票 | `GET /coupon/my` / `POST /coupon/preview` |
| `coupon_grant` | `user.md` § 优惠券与发票 + `admin.md` § G | 同上 + `GET /admin/coupons/{id}/stats` |
| `membership_card` | `user.md` § 用户与钱包 | `GET /profile` |
| `charge_order` | `user.md` § 扫码与充电 | 充电全链路 |
| `payment_order` | `user.md` § 公开接口 | `POST /payment/wechat/callback` |
| `refund_record` | `user.md` § 用户与钱包 | `POST /wallet/refund` |
| `refund_reconcile_diff` | `user.md` 内部 | 对账 |
| `risk_freeze_log` | `user.md` 内部 | 风控 |
| `invoice_request` | `user.md` § 优惠券与发票 | `POST /invoice/apply` / `GET /invoice/my` |
| `payment_callback_idempotent` | `user.md` § 公开接口 | 微信支付回调幂等 |
| `feedback` | `user.md` § 扫码与充电 | `POST /charge/{id}/feedback` |
| `device_fault_report` | `user.md` § 站点与找桩 | `POST /device/report-fault` |
| `active_port_charge` | `user.md` § 充电启动结果 | 端口进行中订单跨月唯一性 |
| `event_outbox` | `user.md` § 支付回调 | 可靠发布 `charge_started_stream` / 补偿事件 |

### 2.2 admin_db(25 张)

| 表名 | 服务的端点引用 | 关键端点 |
| --- | --- | --- |
| `admin_user_role` | `admin.md` § A | `POST /auth/login` / `GET /users` / `POST /users` |
| `role` | `admin.md` § B | `GET /roles` |
| `permission` | `admin.md` § B | `GET /permissions` |
| `station` | `admin.md` § C | `POST /stations` / `GET /stations` |
| `device_meta` | `admin.md` § C | `GET /devices` |
| `pricing_rule` | `admin.md` § K | `POST /settings/charge-rules` |
| `pricing_template` | `admin.md` § K | `POST /settings/pricing-templates` |
| `coupon` | `admin.md` § G | `POST /coupons` |
| `split_template` | `admin.md` § K | `POST /settings/split-templates` |
| `split_party` | `admin.md` § K | `POST /settings/split-templates/{id}/parties` |
| `whitelabel_config` | `admin.md` § H | `PUT /whitelabel` |
| `announcement` | `admin.md` § H | `POST /announcements` |
| `customer_service_config` | `admin.md` § H | `POST /customer-service` |
| `webhook_subscription` | `admin.md` § I | `POST /webhooks` |
| `webhook_delivery_log` | `admin.md` § I | `GET /webhooks/{id}/deliveries` |
| `ota_package` | `admin.md` § J | `POST /ota/packages` |
| `ota_schedule` | `admin.md` § J | `POST /ota/schedules` |
| `alert_rule` | `admin.md` § E | `POST /alert-rules` |
| `alert_subscription` | `admin.md` § E | `POST /alert-subscriptions` |
| `risk_config` | `admin.md` § E | `PUT /risk-config` |
| `settled_record` | `admin.md` § F | `GET /billing/settlements` |
| `finance_reconcile_log` | `admin.md` § F | `GET /billing/reconcile-logs` |
| `invoice_review` | `admin.md` § F | `POST /billing/invoices/{id}/approve` |
| `alert_event` | `admin.md` § E | `GET /alerts` / `POST /alerts/{id}/ack` |
| `audit_log` | `admin.md` § A + L | 全部写操作 |

> **注**:admin_db 实际共 **25 张表**(表清单见 `docs/db/admin.md` § 表清单)。`export_task` 表本期未单独建,导出任务状态由 `worker_db.scheduled_task.task_code='export_run'` 承接(详见 § 4.2 admin 服务表的 ⚠️ 注释)。

### 2.3 gateway_db(8 张)

| 表名 | 服务的端点引用 | 关键端点 |
| --- | --- | --- |
| `vendor` | `gateway.md` § 一.2 | Adapter 注册 |
| `device` | `gateway.md` § 三 / 四 | `POST /device/register` / `GET /devices/{id}` |
| `device_session` | `gateway.md` § 一.1 | TCP/MQTT 会话跟踪 |
| `telemetry` | `gateway.md` § 三 | `POST /device/backfill`(批量落库) |
| `telemetry_aggregate_15min` | `gateway.md` § 三 | admin 查曲线 |
| `telemetry_aggregate_hourly` | `gateway.md` § 三 | admin 查长会话曲线 |
| `raw_frame_log` | `gateway.md` § 一 | TCP 帧原始字节落库 |
| `ota_command` | `gateway.md` § 五 | `POST /firmware-push` ACK 跟踪 |

### 2.4 billing_db(5 张)

| 表名 | 服务的端点引用 | 关键端点 |
| --- | --- | --- |
| `fee_calculation` | `billing.md` § 二 | `POST /calculate` |
| `settlement` | `billing.md` § 三 | `POST /split` / `GET /orders/{id}/split` |
| `settlement_party_amount` | `billing.md` § 三 | 同上 |
| `pricing_tier_snapshot` | `billing.md` § 二 | `POST /calculate` 写入 |
| `withdraw_request` | `billing.md` § 四 | `POST /withdraw-requests` |

### 2.5 worker_db(5 张)

| 表名 | 服务的端点引用 | 关键端点 |
| --- | --- | --- |
| `scheduled_task` | `worker.md` § 二 | 12 个 `task_code` |
| `task_execution_log` | `worker.md` § 二 | 每次执行记录 |
| `comp_tx_log` | `worker.md` § 一.1.4 | 跨服务事务幂等 |
| `dlq_log` | `worker.md` § 四 | 失败消息兜底 |
| `retry_queue` | `worker.md` § 一.1.3 | Webhook / 支付重试 |

---

## § 3 跨服务调用约定表

> **约束**:跨服务 HTTP 调用必须经过本表登记,新增 admin → X 的内部接口必须先在 X 的 API 文档落地路径,再在本表登记。

### § 3.1 充电启动时序权威源(P0-1)

> **核心约定**:**扫码 ≠ 启动**(P0-1 老杨师傅决策)。扫码 / 选端口 = 仅展示;启动 = 微信支付回调成功 + 发 `charge_started_stream` + gateway 启动。
> 完整时序(主链路 + 失败 / 超时 / 取消 / 退款分支 + 端口锁分层)见 `docs/diagrams/charge-payment-sequence.md`,**本文档以此为权威源**。

### § 3.2 跨服务 HTTP 调用表

| 调用方 | 被调方 | 调用场景 | 路径(在调用方文档中引用) | 在被调方文档落地位置 |
| --- | --- | --- | --- | --- |
| user | gateway | 充电中快照订阅(`charge_started_stream` 消费后 Redis 缓存填充) | `/api/v1/internal/devices/{id}/snapshot?order_id={id}` | `gateway.md` § 四 |
| user | gateway | 充电结束通知(`charge_ended_stream` 消费关轮询) | `charge_ended_stream.user-cg` | `gateway.md` § 五 + `技术规格 § 5.3` |
| user | gateway | 设备实时状态查询(轮询快照 cache miss 时) | `/api/v1/internal/devices/{id}` | `gateway.md` § 四 |
| user | gateway | 端口列表(扫描设备码时) | `/api/v1/internal/devices/{id}/ports` | `gateway.md` § 四 |
| user | gateway | 历史曲线查询(订单回看 + 充电中 detail) | `/api/v1/internal/devices/{id}/curve?order_id={id}&window=last_5min` + `/historical-curve?granularity=15min` | `gateway.md` § 四 |
| user | billing | 预扣费预估(scan/start 时报价) | `/api/v1/internal/quote` | `billing.md` § 二 |
| user | billing | 计费快照查询(订单详情页) | `/api/v1/internal/orders/{order_id}/fee-breakdown` | `billing.md` § 二 |
| user | admin | 当前告警查询(充电中页轮询) | `/api/v1/internal/alerts?device_id={id}&status=active` | `admin.md` § E |
| user | admin | 站点详情查询(找桩) | `/api/v1/internal/stations/{station_id}` | `admin.md` § C |
| user | 微信支付 API | JSAPI 预下单(scan/start 时) | `https://api.mch.weixin.qq.com/v3/pay/transactions/jsapi` | `user.md` § 扫码与充电 |
| user | 微信支付 API | 钱包充值退款(同步调用,不走 Stream) | `https://api.mch.weixin.qq.com/v3/refund/...` | `user.md` § 用户与钱包 |
| gateway | user | 启动 ACK 结果回写(含失败补偿) | `POST /api/v1/internal/charge-orders/{order_id}/start-result` | `user.md` § 内部接口 |
| gateway | billing | 充电结束计费(`charge_ended_stream` 消费) | `charge_ended_stream.billing-cg` | `billing.md` § 三 + `技术规格 § 5.3` |
| billing | user | 退款前确认实付金额 | `GET /api/v1/internal/payment-orders/{payment_order_id}` | `user.md` § 内部接口 |
| admin | user | 幂等领取退款记录与回写结果 | `POST /api/v1/internal/refund-records/claim` + `POST /api/v1/internal/refund-records/{refund_id}/result` | `user.md` § 内部接口 |
| admin | user | 退款详情查询 | `/api/v1/internal/refunds/{refund_id}` | `user.md` § 退款 |
| admin | user | 退款审核通过回调 | `/api/v1/internal/refunds/{refund_id}/approve-callback` | `user.md` § 退款 |
| admin | user | 发票详情 / 审核回调 | `/api/v1/internal/invoices/{invoice_id}` + `/approve-callback` | `user.md` § 发票 |
| admin | user | 优惠券统计 | `/api/v1/internal/coupons/stats?coupon_id={id}` | `user.md` § 优惠券 |
| admin | user | 订单详情查询(财务审核) | `/api/v1/internal/orders/{order_id}` | `user.md` § 订单 |
| admin | gateway | 设备远程重启 | `/api/v1/internal/devices/{id}/reboot` | `gateway.md` § 五 |
| admin | gateway | 订单查询 | `/api/v1/internal/devices/{device_id}/orders` | `gateway.md` § 四 |
| admin | billing | 分账 / 账单明细 | `/api/v1/internal/settlements/{settlement_id}` + `/invoices/{invoice_id}/settle-detail` | `billing.md` § 三 |
| admin | worker | 导出任务查询(避免 admin_db 缺 export_task 表) | `/api/v1/internal/export/tasks/{id}` | `worker.md` § 二 |
| billing | admin | 计费规则 / 分账模板查询 | `/api/v1/internal/admin/pricing-rules/{id}` + `/split-templates/{id}` | `admin.md` § K |
| billing | admin | 订单详情查询(写 fee_calculation 时回查) | `/api/v1/internal/orders/{order_id}` | `admin.md` § C / `user.md` |
| billing | 微信支付 API | 充电退款执行(billing 发 refund_required_stream → admin 消费 → admin 调微信) | `https://api.mch.weixin.qq.com/v3/refund/...` | `admin.md` § F |
| worker | gateway | OTA 固件推送 | `/api/v1/internal/devices/{id}/firmware-push` | `gateway.md` § 五 |
| worker | admin | 告警落库 + 订阅推送 | `POST /api/v1/admin/alerts`(admin.md § E) | `admin.md` § E |

---

## § 4 端点 ↔ 表 1-to-1 检查

> **目的**:任何写端点必须有对应表承接。CI 脚本扫 `api/*.md` 中所有 POST / PUT / DELETE,对照 `db/*.md` 中所有表,确保不漏建表。

### 4.1 user 服务

| 端点 | 写入表 | 状态 |
| --- | --- | --- |
| `POST /auth/login` | `user`(UPSERT) | ✅ |
| `POST /phone/bind` | `user`(UPDATE phone_enc) | ✅ |
| `POST /scan/resolve` | 不写表(只读,**不锁端口**) | ✅ |
| `POST /scan/port` | 不写表(只读,**不锁端口**) | ✅ |
| `POST /scan/start` | `charge_order` + `payment_order`(**pending_payment**,**不启动设备**) | ✅ |
| `POST /scan/cancel` | `charge_order` + `payment_order`(60s 窗口内取消) | ✅(P0-1 新增) |
| `POST /charge/stop` | `charge_order` | ✅ |
| `POST /charge/{id}/feedback` | `feedback` | ✅(已加) |
| `POST /wallet/recharge` | `payment_order` + `wallet_account` + `wallet_txn` | ✅ |
| `POST /wallet/refund` | `refund_record` + `wallet_txn` | ✅ |
| `POST /invoice/apply` | `invoice_request` | ✅ |
| `POST /device/report-fault` | `device_fault_report` | ✅(已加) |
| `POST /customer-service/entry` | 不写表(仅路由) | ✅ |

### 4.2 admin 服务(高敏,逐项已校)

> 全部 50+ 写端点对应表已落地(§ 2.2 端点引用表)。重点核对:
> - `POST /auth/login` → `admin_user_role` ✅
> - `POST /users` / `PUT /users/{id}` / `DELETE /users/{id}` → `admin_user_role` ✅
> - `POST /stations` / `PUT /stations/{id}` → `station` ✅
> - `POST /alert-rules` / `PUT /alert-rules/{id}` → `alert_rule` ✅
> - `POST /alert-subscriptions` → `alert_subscription` ✅
> - `PUT /risk-config` → `risk_config` ✅
> - `POST /coupons` / `PUT /coupons/{id}` → `coupon`(admin 视角) ✅
> - `POST /announcements` → `announcement` ✅
> - `PUT /whitelabel` → `whitelabel_config` ✅
> - `POST /webhooks` → `webhook_subscription` ✅
> - `POST /ota/packages` → `ota_package` ✅
> - `POST /ota/schedules` → `ota_schedule` ✅
> - `POST /settings/charge-rules` → `pricing_rule` ✅
> - `POST /settings/split-templates` + `/parties` → `split_template` + `split_party` ✅
> - `POST /export/orders` → `export_task`(本期 admin_db 无此表,worker_db `scheduled_task.task_code='export_run'` 承接)⚠️ **注**:`export_task` 表本期不存在,任务调度走 `worker_db.scheduled_task`(`task_code='export_run'`),worker 消费完成任务后写 OSS 文件;admin 通过 `GET /export/tasks/{id}` 查 worker 状态。本期方案合规,不新建表。

### 4.3 gateway 服务

| 端点 | 写入表 | 状态 |
| --- | --- | --- |
| `POST /device/register` | `device`(UPDATE last_seen) + `device_session` | ✅ |
| `POST /device/backfill` | `telemetry`(分 16 表) | ✅ |
| `POST /device/heartbeat` | `device`(UPDATE last_seen) | ✅ |
| `POST /device/log` | `raw_frame_log` | ✅ |
| `POST /start-charge` / `/stop-charge` | `device_event_log`(本期 gateway_db 无此表,**待二期**补)+ 状态变更发 `comp_tx_stream` | ⚠️ **注**:本期通过 `charge_ended_stream` 通知下游,gateway 不落 device_event_log 表(状态变更仅发 Stream)。 |
| `POST /firmware-push` | `ota_command` | ✅ |

### 4.4 billing 服务

| 端点 | 写入表 | 状态 |
| --- | --- | --- |
| `POST /quote` | 不写表(纯计算) | ✅ |
| `POST /calculate` | `fee_calculation` + `pricing_tier_snapshot` | ✅ |
| `POST /split` | `settlement` + `settlement_party_amount` | ✅ |
| `POST /withdraw-requests` | `withdraw_request` | ✅ |
| `POST /withdraw-requests/{id}/approve` | `withdraw_request` | ✅ |

### 4.5 worker 服务

无 HTTP 端点,所有写操作通过 Stream 消费 + 定时任务触发:

| 触发源 | 写入表 |
| --- | --- |
| `alert_stream` 消费 | `admin_db.alert_event`(经 admin API) |
| `ota_schedule_stream` 消费 | 经 gateway `/firmware-push` 落 `ota_command` |
| `webhook_retry_stream` 消费 | `retry_queue` + `webhook_delivery_log`(经 admin API) |
| `comp_tx_stream` 消费 | `comp_tx_log` |
| 定时任务执行 | `task_execution_log` |
| DLQ 兜底 | `dlq_log` |

---

## § 5 错误码总账(§ 技术规格 § 7.2)

| 段位 | 含义 | 跨服务使用 |
| --- | --- | --- |
| 1xxx | 通用错误 | 5 个服务全部沿用 |
| 2xxx | 业务错误 | 各服务定义专属子段 |
| 3xxx | 第三方错误 | user.md(微信)、billing.md(无)、admin.md(微信/OSS)、gateway.md(协议) |
| 4xxx | 限流 | 全部沿用 |
| 5xxx | 服务器错误 | 全部沿用 |

> **不允许** 各服务私自定义超出 § 7.2 段位的错误码。新增错误码必须先在技术规格登记。

---

## § 5.5 软删除策略差异说明(跨 schema)

| Schema | 业务表 | 配置类 | 日志类 | 备注 |
| --- | --- | --- | --- | --- |
| **user_db** | 软删除 + 抹除 PII(§ user.md) | — | 审计/幂等表 不软删 | 抹除 PII(`phone_enc` / `unionid` / `nickname` / `avatar_url` 置 NULL),`openid` 保留 30 天 |
| **admin_db** | 软删除(§ admin.md) | **不软删**,启用 / 停用 | 按月分区 + 物理归档(超 3 年) | 配置类:角色 / 权限 / 白标 / 告警订阅 |
| **billing_db** | **不软删**(§ billing.md) | — | 按月分区 + 物理归档 | 计费 / 分账快照为合规证据,必须保留 |
| **gateway_db** | 设备表软删,其它不分 | — | 遥测 / 帧日志 / 会话表 按月分区 | 遥测原始 1 月 + 聚合 3 年(技术规格 § 4.6) |
| **worker_db** | `scheduled_task` 软删 | — | 执行日志 / DLQ 不软删,按月分区 | 同 admin_db 模式 |

**统一约束**:所有 schema 的"软删除"都通过 `deleted_at + deleted_by` 实现,所有查询经仓储层自动 `WHERE deleted_at IS NULL`;运维查询可绕过。

---

## § 6 文档维护规则

1. **任何新增 / 删除 / 修改端点** → 必须同步更新 § 2 + § 4 对应行
2. **任何新增 / 删除 / 修改表** → 必须同步更新 § 2 + § 4 + 引用此表的所有 API 文档
3. **任何新增 / 删除 / 修改 Stream** → 必须先在技术规格 § 5.1 登记,再更新 § 1
4. **跨服务新增调用** → 必须先在被调方 API 文档落地路径,再更新 § 3
5. **跨文档章节引用规范**:
   - 禁止裸 `§ X.Y` 引用 —— 必须使用 `文档名 § X.Y` 形式(如 `技术规格 § 5.1`、`需求分析 § 3.1`)
   - 原因:同节号在多份文档含义不同(例:`§ 5.5` 在需求分析 / 技术规格 / 本文档含义各异),裸引用必歧义
   - 内部指向本文件 § X.Y 可保留裸形式,需紧邻段落注明"见上 § X"
6. **CI 检查**:`tools/check-api-consistency.ts` 自动扫以下不变量:
   - `api/*.md` 中出现的所有 `_stream` 名都在 § 1 列表内
   - `db/*.md` 中出现的所有表名都在 § 2 列表内
   - 写端点(POST/PUT/DELETE)都有对应表承接(§ 4)
   - 跨服务调用路径在调用方 + 被调方文档中**一致**(引用而非重新声明)
   - 跨文档裸 `§ X.Y` 引用 → 告警(可豁免:明确指向本文件的内部引用)
   - 表总数 = § 2 标题数字(自动清点 § 2 各 schema 小节行数)
7. **不一致的处理**:CI 失败 → 拒绝合并 → 必须先更新本文档再重试
