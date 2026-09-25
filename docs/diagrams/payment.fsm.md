# 支付订单状态机(`user_db.payment_order.status`)(P1-6 与 db 一致化)

> **维护者**:后端 user 服务 / 跨服务对齐时检查
> **配套文档**:`docs/db/user.md` § 表 3 `payment_order` / `docs/diagrams/refund.fsm.md` / `docs/diagrams/charge-order.fsm.md` / `docs/技术规格.md` § 5.4 + § 5.5

---

## 状态图

```
   ┌──────────┐
   │ initiated│  ← user 收到 /scan/start → INSERT payment_order(status='initiated', biz_type='charge', biz_id=charge_order.id)
   └─────┬────┘
         │ 微信回调(同步 5 min 内)
         ├──────→ success          (一次性支付成功,paid_at 落表,业务事件发出)
         │
         ├──────→ failed           (下单 / 微信返回错误,已生成单据但失败)
         │
         └──────→ expired          (用户 5 min 内未支付;worker 周期扫描清理)
                ↓
            (软删除 + 自动退款视业务)

   success ───────────┐
                       │
                       ├──→ partial_refunded  ← 部分退款(单订单多笔退款累计)
                       │
                       └──→ refunded          ← 全额退款完成(>= 1 笔 refund_record.status=success 且累计 = paid_fee_cents)
```

---

## 状态枚举(P1-6 与 `db/user.md` 表 3 payment_order.status ENUM 一致)

| 状态 | 含义 | 触发 |
| --- | --- | --- |
| `initiated` | 已下单,等待用户支付 | `POST /scan/start` 创建订单 + 微信预下单成功 |
| `success` | 微信支付成功 | 微信回调 `TRANSACTION.SUCCESS` |
| `failed` | 支付失败(用户拒付 / 余额不足 / 微信风控) | 微信回调 `TRANSACTION.FAIL` / 本地超时 |
| `expired` | 用户超过 5 分钟未支付 | 微信侧超时通知 或 worker 周期扫描 |
| `refunded` | 全额退款完成 | 全部 `refund_record` 成功,且累计 = `paid_fee_cents` |
| `partial_refunded` | 部分退款 | 至少 1 条 `refund_record.status='success'`,但仍有未退金额 |

> **不在 ENUM 中但常见于退款状态机**:`settled`(财务线下打款,见 `refund.fsm.md`)— **不**写在 `payment_order.status`,只在 `refund_record.status='settled'` 标记。

---

## 跨服务动作

| 触发事件 | 动作 |
| --- | --- |
| `initiated` 创建 | 写 `payment_callback_idempotent(wechat_transaction_id, status='initiated')` |
| `success` 切换 | user API 收到回调 → 写幂等成功 → 发 `charge_started_stream`(仅 `biz_type='charge'`)/ 发钱包流水完成事件 |
| `failed` | user API 写 `payment_order.status='failed'` + 发 `alert_stream(notice=payment_failed)` |
| `expired` | worker 周期任务扫描 `initiated` > 5 min → 标 `expired` |
| 全额 `refunded` | admin 消费 `comp_tx_stream(回执)` → 标 `refunded` |

---

## 子单 vs 主单(组合支付)

组合支付(微信 + 余额 + 优惠券)有**主单 + 多张子单**:

- 主单:`payment_order(pay_method='mixed', total_amount = 子单合计, status='success')`
- 子单:`payment_order(parent_order_id = 主单.id, pay_method='wechat'/'wallet', amount = N)`(子单不直接走微信)
- 退款:**先退子单**(现金通道)→ 主单状态切换

---

## 边界规则

- **幂等**:`payment_callback_idempotent` 是支付回调的**唯一去重 key**(独立于 `event_id`)
- **不可逆**:`success` 不可改回 `initiated` / `failed`
- **不可跳跃**:`initiated → refunded` 不允许(必须 `initiated → success → refunded`)
- **超时清理**:`expired` 订单不退款(用户没付钱),但若有预扣款(余额 / 优惠券)需解冻
- **对账**:`refunded` 状态写入后,**次日**对账必须出现在微信账单(`billing_db.finance_reconcile_log` 跟踪差异)