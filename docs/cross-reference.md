# API ↔ DB ↔ Stream 一致性对账文档

> **目的**:防止三个层次的文档(API / DB / Stream)漂移。任何新增 / 修改 / 删除必须**同步更新本文档对应行**,否则 CI 拒绝合并。
> **维护工具**:`tools/check-api-consistency.ts`(检查端点路径、Stream 名、表名是否在文档中一致出现)
> **最近一次同步**:2026-09-26(随 6 次 API docs commits 落地)

---

## § 1 Stream 名总账(§ 技术规格 5.1,**9 个**)

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

> **约束**:9 个 Stream 是穷举的。新增 Stream 必须先在技术规格 § 5.1 登记,再在本表登记,再在 API 文档中使用。

---

## § 2 数据库表总账(5 schema,54 张)

### 2.1 user_db(16 张)

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
| `callback_idempotent` | `user.md` § 公开接口 | 微信支付回调幂等 |
| `feedback` | `user.md` § 扫码与充电 | `POST /charge/{id}/feedback` |
| `device_fault_report` | `user.md` § 站点与找桩 | `POST /device/report-fault` |

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

| 调用方 | 被调方 | 调用场景 | 路径(在调用方文档中引用) | 在被调方文档落地位置 |
| --- | --- | --- | --- | --- |
| user | gateway | 启动充电 | `/api/v1/internal/start-charge` | `gateway.md` § 五 |
| user | gateway | 停止充电 | `/api/v1/internal/stop-charge` | `gateway.md` § 五 |
| user | gateway | 设备实时状态 | `/api/v1/internal/devices/{id}` | `gateway.md` § 四 |
| user | gateway | 端口列表 | `/api/v1/internal/devices/{id}/ports` | `gateway.md` § 四 |
| user | billing | 预扣费预估 | `/api/v1/internal/quote` | `billing.md` § 二 |
| user | admin | 发票详情查询 | `/api/v1/admin/invoice/...`(admin.md § F) | `admin.md` § F |
| admin | user | 退款详情查询 | 抽象引用 `user.md`(跨服务调用约定) | `user.md` |
| admin | user | 退款审核通过回调 | 抽象引用 `user.md`(跨服务调用约定) | `user.md` |
| admin | user | 发票详情 / 审核回调 | 抽象引用 `user.md` | `user.md` |
| admin | user | 优惠券统计 | 抽象引用 `user.md` | `user.md` |
| admin | gateway | 设备远程重启 | `/api/v1/internal/devices/{id}/reboot` | `gateway.md` § 五 |
| admin | gateway | 订单查询 | 抽象引用 `gateway.md`(跨服务调用约定) | `gateway.md` § 四 |
| admin | billing | 分账 / 账单明细 | 抽象引用 `billing.md` | `billing.md` |
| billing | admin | 计费规则 / 分账模板查询 | 抽象引用 `admin.md` | `admin.md` § K |
| worker | gateway | OTA 固件推送 | `/api/v1/internal/devices/{id}/firmware-push` | `gateway.md` § 五 |
| worker | admin | 告警落库 + 订阅推送 | `/api/v1/admin/alerts`(admin.md § E) | `admin.md` § E |
| worker | user | 退款执行(未来扩展) | 待 user.md 落地 | `user.md` § 用户与钱包 |

---

## § 4 端点 ↔ 表 1-to-1 检查

> **目的**:任何写端点必须有对应表承接。CI 脚本扫 `api/*.md` 中所有 POST / PUT / DELETE,对照 `db/*.md` 中所有表,确保不漏建表。

### 4.1 user 服务

| 端点 | 写入表 | 状态 |
| --- | --- | --- |
| `POST /auth/login` | `user`(UPSERT) | ✅ |
| `POST /phone/bind` | `user` | ✅ |
| `POST /scan/start` | `charge_order` | ✅ |
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

## § 6 文档维护规则

1. **任何新增 / 删除 / 修改端点** → 必须同步更新 § 2 + § 4 对应行
2. **任何新增 / 删除 / 修改表** → 必须同步更新 § 2 + § 4 + 引用此表的所有 API 文档
3. **任何新增 / 删除 / 修改 Stream** → 必须先在技术规格 § 5.1 登记,再更新 § 1
4. **跨服务新增调用** → 必须先在被调方 API 文档落地路径,再更新 § 3
5. **CI 检查**:`tools/check-api-consistency.ts` 自动扫以下不变量:
   - `api/*.md` 中出现的所有 `_stream` 名都在 § 1 列表内
   - `db/*.md` 中出现的所有表名都在 § 2 列表内
   - 写端点(POST/PUT/DELETE)都有对应表承接(§ 4)
   - 跨服务调用路径在调用方 + 被调方文档中**一致**(引用而非重新声明)
6. **不一致的处理**:CI 失败 → 拒绝合并 → 必须先更新本文档再重试