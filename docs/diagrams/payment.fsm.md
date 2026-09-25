# 支付订单状态机(`user_db.payment_order.status`)

> 字段与 ENUM 以 `docs/db/user.md` 表 3 为准;微信回调与取消的竞争处理见 `docs/diagrams/charge-payment-sequence.md`。

## 状态迁移

```text
initiated ── 微信成功回调 ──→ success ── 部分退款成功 ──→ partial_refunded ── 剩余退款成功 ──→ refunded
     ├──── 微信关单或 60 秒内取消 ──→ cancelled
     └──── 本地预下单失败 / 超时确认未支付 ──→ failed
cancelled / failed ── 迟到且验签通过的真实成功回调 ──→ success ──→ 退款补偿
```

| 状态 | 含义 | 写入方 |
| --- | --- | --- |
| `initiated` | 已创建支付单,等待微信支付 | user `/scan/start` |
| `success` | 微信实际支付成功,含取消后迟到的成功回调 | user 微信回调 |
| `failed` | 已确认支付失败或超时未付 | user 超时任务 / 失败处理 |
| `cancelled` | 取消先于支付成功回调提交 | user `/scan/cancel` |
| `partial_refunded` | 实付金额部分退款成功 | user 处理退款结果 |
| `refunded` | 实付金额全部退款成功 | user 处理退款结果 |

`expired`、`settled` 不在本表 ENUM 中,不能写入 `payment_order.status`。支付单 `failed` / `cancelled` 后若仍收到经过验证的成功回调,必须记录真实支付成功并发起退款补偿,不能直接丢弃回调。

## 回调与事件

1. `/scan/start` 创建 `payment_order(status='initiated', wechat_transaction_id=NULL)`,微信预下单的 `out_trade_no` 使用本地 `order_no`。此时**不**写 `payment_callback_idempotent`。
2. 微信成功回调按 `out_trade_no` 找支付单,校验商户号和金额,再用 `transaction_id` 写不分区的 `payment_callback_idempotent`。支付状态、幂等记录和 `event_outbox` 在同一 `user_db` 事务中提交。
3. `biz_type='charge'` 且订单仍可启动时写 `charge_started_stream` outbox;取消/超时已提交时写 `comp_tx_stream` 退款请求 outbox。`biz_type='recharge'` 时在同一事务增加余额并写 `wallet_txn`,不发充电启动事件。发布器重试至 Redis 确认;消费方按稳定 `event_key` 去重。
4. 退款成功后 user 按累计成功退款额将支付单迁移至 `partial_refunded` 或 `refunded`;admin 不直写 `user_db`。

组合支付的主单与子单规则见 `docs/db/user.md` 表 3。微信子单使用自己的 `order_no` 作为 `out_trade_no`;回调只更新该微信子单,再由 user 汇总主单支付状态。
