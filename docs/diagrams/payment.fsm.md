# 支付订单状态机(`user_db.payment_order.status`)

> **配套文档**:`docs/需求分析.md` § 9.4 / `docs/api/user.md` § 公开接口 / `docs/技术规格.md` § 5.4

---

## 状态图

```
   ┌────────────┐
   │ initiated  │  ← user 调微信下单后 INSERT
   └─────┬──────┘
         │ 微信回调
         ├──────→ success          (一次性支付成功)
         │
         ├──────→ failed           (下单失败 / 微信返回错误)
         │
         └──────→ expired          (用户 5 min 内未支付)
                ↓
            (软删除 + 自动退款视业务)
                                  ┌────────────┐
                          ┌──────→│  partial   │
                          │       │ refunded   │  ← 部分退款(单订单多笔退款累计)
   success ───────────────┤       └────────────┘
                          │
                          └──────→ refunded      ← 全额退款完成
```

---

## 状态枚举

| 状态 | 含义 | 触发 |
| --- | --- | --- |
| `initiated` | 已下单,等待用户支付 | `POST /payment/wechat/create` 成功后 |
| `success` | 微信支付成功 | 微信回调 `TRANSACTION.SUCCESS` |
| `failed` | 支付失败(用户拒付 / 余额不足 / 微信风控) | 微信回调 `TRANSACTION.FAIL` / 本地超时 |
| `expired` | 用户超过 5 分钟未支付 | 微信侧超时通知 或 worker 周期任务扫描 |
| `refunded` | 全额退款完成 | `refund_record.status` 全集为 `success` / `settled` |
| `partial_refunded` | 部分退款 | 至少 1 条 `refund_record` 为 `success`,但仍有未退金额 |

---

## 跨服务动作

| 触发事件 | 动作 |
| --- | --- |
| `initiated` 创建 | 写 `payment_callback_idempotent(wechat_transaction_id, status='initiated')` |
| `success` 切换 | user API 收到回调 → 写幂等成功 → 发 `charge_started_stream`(仅 `biz_type='charge'` 时)/ 发钱包流水完成事件 |
| `failed` | user API 写 `payment_order.status='failed'` + 发 `alert_stream(notice=payment_failed)` |
| `expired` | worker 周期任务扫描 `initiated` > 5 min → 标 `expired` |
| 全额 `refunded` | admin 消费 `comp_tx_stream(回执)` → 标 `refunded` |

---

## 子单 vs 主单(组合支付)

组合支付(微信 + 余额 + 优惠券)有**主单 + 多张子单**:

- 主单:`payment_order(pay_method='mixed', total_amount = 子单合计, status='success')`
- 子单:`payment_order(parent_order_id = 主单.id, pay_method='wechat'/'wallet'/'coupon', amount = N)`(子单不直接走微信)
- 退款:**先退子单**(现金通道)→ 主单状态切换

---

## 边界规则

- **幂等**:`payment_callback_idempotent` 是支付回调的**唯一去重 key**(独立于 `event_id`)
- **不可逆**:`success` 不可改回 `initiated` / `failed`
- **不可跳跃**:`initiated → refunded` 不允许(必须 `initiated → success → refunded`)
- **超时清理**:`expired` 订单不退款(用户没付钱),但若有预扣款(余额 / 优惠券)需解冻
- **对账**:`refunded` 状态写入后,**次日**对账必须出现在微信账单(`billing_db.finance_reconcile_log` 跟踪差异)
