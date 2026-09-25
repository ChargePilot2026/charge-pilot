# 充电与支付时序(P0 权威源)

> **原则**:扫码只读;用户选择端口后创建待支付订单;只有微信成功回调经过持久化处理,才向 gateway 发启动事件。各服务只读写自己的 schema。接口契约见 `docs/api/user.md`、`docs/api/gateway.md`、`docs/api/billing.md`。

## § 1 主链路

```text
小程序             user-svc                 微信             Redis Stream          gateway             billing
  | POST /scan/resolve |                     |                    |                  |                   |
  |------------------->| 只读端口信息          |                    |                  |                   |
  | POST /scan/start   |                     |                    |                  |                   |
  |------------------->| SET charge:hold:port NX EX 300        |                  |                   |
  |                    | INSERT charge_order(pending_payment)   |                  |                   |
  |                    | INSERT payment_order(initiated)        |                  |                   |
  |                    | POST billing /api/v1/internal/quote ---------------------------->|                   |
  |                    | JSAPI 预下单(out_trade_no=payment_order.order_no) --> 微信       |                   |
  |<-- payment_params--|                     |                    |                  |                   |
  | wx.requestPayment ---------------------->|                    |                  |                   |
  |                    |<-- 成功回调(含 out_trade_no,transaction_id,amount) --|        |                   |
  |                    | 按 out_trade_no 找 payment_order;验商户号和金额       |        |                   |
  |                    | user_db 事务:锁订单,写幂等记录,支付成功,event_outbox  |        |                   |
  |                    |-- 事务提交后返回 200 --> 微信        |                  |                   |
  |                    | outbox 发布器重试 XADD charge_started_stream ----->|            |                   |
  |                    |                     |                    |---- 消费并验逻辑锁 -->|                   |
  |                    |                     |                    |                  | MQTT START → 设备 ACK|
  |                    |<-- POST /api/v1/internal/charge-orders/{id}/start-result --------|                   |
  |                    | user_db 事务:INSERT active_port_charge + status=charging         |                   |
  |                    |-- 持久化确认 ------->|                    |<---- ACK Stream --|                   |
  | GET /charge/ongoing/snapshot            |                    |                  |                   |
  |------------------->| Redis miss → HTTP gateway snapshot;本地查 user_db  |            |                   |
  |                    |                     |                    |                  | 设备停止 → charge_ended_stream
  |                    |<--- user-cg 消费,关闭轮询缓存 ------|                  |----> billing-cg 计费分账
```

**支付回调与事件投递**:

1. `payment_order.wechat_transaction_id` 在预下单时为 NULL;回调必须用微信返回的 `out_trade_no` 对应本地 `payment_order.order_no` 定位支付单,然后保存 `transaction_id`。
2. `payment_callback_idempotent` 唯一键防重复入账。支付状态和 `event_outbox(event_key='charge-start:{payment_order_id}')` 必须在同一 `user_db` 事务中提交;不能先提交支付成功再直接 `XADD`。
3. user 发布器在事务外将 outbox 事件写入 Redis Stream;失败持续重试和告警,成功才标记 `published`。gateway 按稳定 `event_key` 去重;重复回调也不能使待发布事件丢失。
4. gateway 仅写自己的设备会话和遥测;启动 ACK 通过 user 内部接口回传。user 在同一事务内取得不分区的 `active_port_charge.port_id` 唯一占用并更新 `charge_order`。gateway 在 user 持久化确认前不得 ACK 原 Stream 消息。

## § 2 失败、取消与退款

### § 2.1 支付失败或迟到

- 用户拒付时没有成功回调;小程序依支付结果更新界面,待支付订单由超时任务关闭。收到失败通知时 user 只更新自己的支付单和充电订单,并按 `order_no` 原子比较删除逻辑锁。
- 成功回调在订单已取消、超时或锁属于他人后到达:user 仍记录真实支付成功,但同事务写 `event_outbox(type='charge_refund_requested', stream_name='comp_tx_stream')`,不写启动事件。billing 消费补偿事件、查询 user 支付金额后,按 `event_key` 幂等发布 `refund_required_stream`。

### § 2.2 gateway 启动失败

```text
gateway 消费 charge_started_stream → MQTT START → 设备 ACK failed / 超时
  → POST user /api/v1/internal/charge-orders/{order_id}/start-result(result=failed)
  → user 事务:charge_order=failed + event_outbox(type=charge_refund_requested)
  → user 发布 comp_tx_stream → billing 发布 refund_required_stream
  → admin 消费并调 user 内部接口领取 refund_record → 微信退款 → 调 user 回写结果
```

gateway 不直接更新 `user_db`,也不发布 `refund_required_stream`。若设备已经闭合继电器但 user 的端口占用唯一键冲突,gateway 必须先发 STOP 并确认断电,再报告失败;未确认断电时告警并转人工处置。

### § 2.3 充电结束或超时

gateway 检测设备停止或超过最长充电时间时发 `charge_ended_stream`;user 消费后更新 `charge_order` 并以 `port_id` + `charge_order_id` 删除 `active_port_charge`,关闭轮询缓存。billing 独立消费并写本 schema 计费/分账表;符合退款条件时由 billing 发布 `refund_required_stream`。

### § 2.4 用户 60 秒内主动取消

`POST /api/v1/user/scan/cancel {order_no}` 在 `user_db` 事务中锁定充电单与支付单,仅允许 `pending_payment` + `initiated`。支付回调若先提交,取消返回 `2018`,不能把 `success` 覆盖为 `cancelled`;取消若先提交,迟到的成功回调走 § 2.1 退款分支。提交取消后微信关单,并仅在 Redis 锁值仍为本 `order_no` 时原子删除。

## § 3 端口并发约束

| 层 | 键或表 | 生效范围 | 释放条件 |
| --- | --- | --- | --- |
| 支付前逻辑锁 | `charge:hold:port_xxx` = `order_no` | 5 分钟,防同时支付 | 取消 / 超时 / 启动处理时比较持有者后删除 |
| 启动物理锁 | `charge:lock:port_xxx` = `order_no` | 30 秒,防重复下发指令 | ACK 后比较持有者删除 |
| 数据库唯一占用 | `user_db.active_port_charge.port_id` 主键 | 跨月,整个充电期间 | user 核对设备已停后按 `port_id` + `charge_order_id` 删除 |

`charge_order` 按 `created_month` 分区。MySQL 分区表的每个唯一键都必须含分区列,因此不能用 `(port_id,active_charging)` 在该表上实现跨月唯一约束。Redis TTL 到期不等于设备已断电;不得仅凭 TTL 清除数据库占用。

## § 4 文档同步清单

| 契约 | 权威位置 |
| --- | --- |
| 微信回调、取消、gateway 结果回写及 outbox | `docs/api/user.md`、`docs/db/user.md` |
| gateway 设备指令与状态查询 | `docs/api/gateway.md` |
| 退款事件单生产者与计费 | `docs/api/billing.md`、`docs/cross-reference.md` |
| 订单状态迁移 | `docs/diagrams/charge-order.fsm.md` |
