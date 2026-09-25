# 跨服务数据血缘(Data Lineage)

> **目的**:从一次"扫码 → 充电 → 结束 → 计费 → 退款"的全链路,**追踪每条数据从诞生到落表的全路径**。
> 散落在 `api/*.md` / `db/*.md` / `cross-reference.md` 的表之间,这张图是关键参考。
> **配套文档**:见`cross-reference.md` § 1-5。

---

## 1. 主链路:充电一笔订单的数据流

```
用户扫码
  │
  ▼
[user] POST /api/v1/user/scan/resolve
  │   读:gateway_db.device / port_view (从 gateway_db 同步)
  │
  ▼
[user] POST /api/v1/user/scan/start
  │   写:user_db.charge_order(status=pending_payment)
  │   写:user_db.payment_order(status=initiated, biz_type='charge', biz_id=charge_order.id)
  │   写:Redis: charge:hold:port_xxx(holder=order_no, ttl=300s)
  │   RPC: billing POST /api/v1/internal/quote (报价)
  │   RPC: 微信 POST /v3/pay/transactions/jsapi(out_trade_no=payment_order.order_no)
  │
  ▼
[user] 调起 wx.requestPayment() → 微信
  │
  ▼  (异步)
[user] POST /api/v1/public/payment/wechat/callback
  │   验签 + 解密
  │   按 out_trade_no 定位支付单;验金额和商户号
  │   同一事务写:user_db.payment_callback_idempotent + payment_order(status=success)
  │   同一事务写:user_db.event_outbox(charge_started_stream,event_key)
  │   发布器重试发 Redis Stream: charge_started_stream
  │
  ▼ (stream 消费)
[gateway] 消费 charge_started_stream
  │   → MQTT 下发 charge/{vendor}/{device}/cmd 启动指令到设备
  │   设备 ACK 后 → 写:gateway_db.device_session(started_at)
  │   → POST user /api/v1/internal/charge-orders/{order_id}/start-result
  │   → user 同事务写:active_port_charge + charge_order(status=charging)
  │
  ▼ (设备上报)
[gateway] MQTT 上行 charge/{vendor}/{device}/telemetry
  │   写:gateway_db.telemetry(hash 16 表) ← 每秒
  │   写:gateway_db.telemetry_aggregate_15min / _hourly
  │   状态变更(charging) → 发:device_event_stream
  │   越界 → 发:alert_stream
  │
  ▼ (stream 消费)
[user] 消费 device_event_stream
  │   → 写 Redis: snapshot:{order_id}(TTL=10s) 充电中快照缓存
  │
  ▼ (用户小程序 5s 轮询)
[user] GET /api/v1/user/charge/ongoing/snapshot?order_id=xxx
  │   读 Redis: snapshot:{order_id}(cache hit > 90%)
  │   cache miss → HTTP 调 gateway 查询 telemetry + 本地查 user_db.charge_order → 回填缓存
  │   返回 polling 响应(含 poll_continue)
  │
  ▼ (设备停止 / 拔插头 / 充电结束)
[gateway] 检测 charge_state 变化 → 发:charge_ended_stream(payload={order_id, ended_at, reason})
  │
  ▼
[user] 消费 charge_ended_stream
  │   → 关闭快照缓存(下次轮询 → poll_continue: false → 跳结束页)
  │
[billing] 消费 charge_ended_stream
  │   读:user_db.charge_order(从 Redis payload 或 HTTP 调 user)
  │   读:admin_db.pricing_rule / pricing_template(经 cache,TTL=10min)
  │   写:billing_db.fee_calculation(每订单一行快照,价费分离)
  │   写:billing_db.pricing_tier_snapshot(冻结当时电价)
  │   写:billing_db.settlement(分账单头)
  │   写:billing_db.settlement_party_amount(每个参与方一行)
  │   判断是否退款:
  │     是 → 发:refund_required_stream
  │     否 → 直接推账单通知给 user
  │
  ▼ (退款流程)
[admin] 消费 refund_required_stream
  │   经 user 内部接口领取:user_db.refund_record(status=processing)
  │   RPC: 微信 POST /v3/refund/...
  │   微信回调 → 经 user 内部接口写:user_db.refund_record(status=success)
  │   发:comp_tx_stream(refund_id)
  │
[billing] 消费 comp_tx_stream
  │   → 写:billing_db.settlement(status=refunded)
  │
[user] 通过小城程序消息订阅推送退款通知给用户
  │
  ▼
订单生命周期结束,数据长期保留
```

---

## 2. 表 ↔ 表 血缘表(关键字段)

| 上游 | 下游 | 关联字段 | 流转时机 |
| --- | --- | --- | --- |
| `user_db.charge_order.id` | `user_db.payment_order.biz_id` | `biz_type='charge'` | 创建支付时 |
| `user_db.charge_order.id` | `billing_db.fee_calculation.charge_order_id` | UNIQUE | billing 消费 `charge_ended_stream` |
| `user_db.payment_order.id` | `user_db.refund_record.payment_order_id` | 多笔退款可关联同订单 | 触发退款时 |
| `user_db.payment_order.id` | `user_db.payment_callback_idempotent.payment_order_id` | 1:1 | 微信回调时 |
| `user_db.payment_order.id` | `user_db.wallet_txn.payment_order_id`(若走余额) | 1:N | 余额扣减/返还 |
| `user_db.refund_record.id` | `billing_db.settlement.refund_id`(若 metadata) | 0..1 | 退款完成时 |
| `billing_db.fee_calculation.id` | `billing_db.settlement.fee_calculation_id` | UNIQUE | 出账时 |
| `billing_db.settlement.id` | `billing_db.settlement_party_amount.settlement_id` | 1:N (N ≤ 8) | 出账时 |
| `billing_db.settlement.id` | `billing_db.withdraw_request.settlement_id` | 0:N | 客户提现时 |
| `gateway_db.device.id` | `user_db.charge_order.device_id` | 多笔订单 | 启动充电时 |
| `gateway_db.device.id` | `gateway_db.telemetry.device_id` | 1:N (hash 16 表) | 实时上报 |
| `gateway_db.device.id` | `gateway_db.device_session.device_id` | 1:N (按月分区) | 长连接生命周期 |
| `admin_db.alert_rule.id` | `admin_db.alert_event.rule_id` | 1:N | 规则触发时 |
| `admin_db.webhook_subscription.id` | `admin_db.webhook_delivery_log.subscription_id` | 1:N (按月分区) | Webhook 推送时 |
| `admin_db.ota_package.id` | `admin_db.ota_schedule.package_id` | 1:N | OTA 推送调度时 |
| `admin_db.split_template.id` | `admin_db.split_party.template_id` | 1:N (N ≤ 8) | 配置时 |
| `worker_db.scheduled_task.task_code` | `worker_db.task_execution_log.task_code` | 1:N | 每次执行 |

---

## 3. Stream 触发表写入矩阵(P0-3:每消费方独立消费者组)

> **消费者组命名**:`{stream}.{consumer}-cg`(详见 `docs/技术规格.md` § 5.3)
> 例如 `charge_ended_stream` 同时被 billing / user / admin 消费,各自独立组 `billing-cg` / `user-cg` / `admin-cg`。

| Stream | 消费方(每方独立 -cg) | 写入表 / 副作用 |
| --- | --- | --- |
| `device_event_stream` | `worker-cg`(快照填充)、`admin-cg`(可选:状态推送) | Redis `snapshot:{order_id}` + admin_db.alert_event |
| `alert_stream` | `admin-cg`(落库 + Webhook)、`worker-cg`(可选:周期复核) | admin_db.alert_event + (Webhook 推送) |
| `charge_started_stream` | `gateway-cg` | gateway 启动设备 + 经 user 内部接口更新 `charge_order`;user 事务写 `active_port_charge` |
| `charge_ended_stream` | `billing-cg`(计费)、`user-cg`(关轮询)、`admin-cg`(可选:订单快照) | billing_db.fee_calculation + settlement / user 关闭 Redis snapshot |
| `refund_required_stream` | `admin-cg` | 经 user 内部接口领取 `refund_record` → admin 调微信退款 → 经 user 回写结果 |
| `invoice_required_stream` | `admin-cg` | admin_db.invoice_review(status=pending) |
| `webhook_retry_stream` | `worker-cg` | worker_db.retry_queue + admin_db.webhook_delivery_log |
| `ota_schedule_stream` | `worker-cg`(调度)、`gateway-cg`(下发指令) | gateway_db.ota_command(经 gateway API) |
| `comp_tx_stream` | 各服务各自的消费者组 | worker 将自身补偿记录写 `worker_db.comp_tx_log`;billing 等其他服务按 `event_key` 在本服务处理,不直写 `worker_db` |
| `coupon_grant_required_stream` | `user-cg` | user 校验活动规则后写 `user_db.coupon_grant`,再回传 admin 更新发放计数 |
| `pricing_rule_changed_stream` | `billing-cg` | billing 更新本 schema 的计费规则快照 |

---

## 4. 数据保留与归档路径

| 表 | 在线保留 | 归档至 | 触发 |
| --- | --- | --- | --- |
| 业务表(订单 / 计费 / 分账 / 告警 / 操作日志) | ≥ 3 年 | 冷存储(同 OSS 桶冷归档层 / 客户自定) | worker_db.data_retention 周期任务 |
| gateway_db.telemetry(原始) | 1 个月 | 冷存储 | worker_db.data_retention |
| gateway_db.telemetry_aggregate_15min / _hourly | ≥ 3 年 | 不归档 | 一直保留 |
| gateway_db.raw_frame_log | 1 个月 | 冷存储 | worker_db.data_retention |
| 软删除记录(`deleted_at` 非空) | 在线可见 | 物理归档 (`deleted_at < NOW() - 3 YEAR`) | data_retention |
| audit_log / webhook_delivery_log / task_execution_log / dlq_log | 按月分区 + ≥ 180 天 / ≥ 3 年 | 物理归档 | data_retention |
