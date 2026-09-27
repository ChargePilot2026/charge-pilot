# 退款状态机(`user_db.refund_record.status`)(P1-6 与 db 一致化)

> **配套文档**:`docs/diagrams/payment.fsm.md` / `docs/db/user.md` § 表 5 `refund_record` / `docs/需求分析.md` § 5.3 / § 8.4 / `docs/技术规格.md` § 5.4
>
> 当前实现以 `user_db.refund_record` 为退款状态权威源。`refund_required_stream` 由 user 的事务性 Outbox 发布，admin 消费并执行微信退款；微信回调由 user 更新退款结果，再把 `refund_completed` 发给 worker 作审计。worker 不执行补偿，billing 不消费 `comp_tx_stream`。

---

## 状态图

```
pending ── admin 领取 ──→ processing ── 微信成功确认 ──→ success
   │                          │
   │ 第二位财务拒绝          └── 确认退款失败 ──→ failed ── 管理员重试 ──→ processing
   ↓
rejected
```

双签意见独立保存在 refund_review。`pending` 阶段可等待第二签；只有双签通过后 admin 才能领取并执行。

---

## 当前状态枚举（与迁移一致）

| 状态 | 触发进入 | 触发退出 |
| --- | --- | --- |
| `pending` | user 事务内创建退款记录；等待双签或 admin 领取 | admin 领取后为 `processing`；第二签拒绝后为 `rejected` |
| `processing` | admin 已领取；微信执行阶段由 admin_db.refund_task 驱动 | user 确认结果后为 `success` 或 `failed` |
| `success` | user 在验证通过的退款回调/结果事务中确认退款成功 | 终态；同时更新 payment_order 累计退款状态 |
| `failed` | user 已确认微信退款失败 | admin 的持久退款任务可人工恢复并再次领取；非 DLQ 状态 |
| `rejected` | 财务第二签拒绝，凭据写 refund_rejection | 终态；不产生退款执行事件 |

当前没有 `waiting`、`retried`、`manual_review` 或 `settled` 这些 refund_record 状态。微信处理中/重试阶段保存在 admin_db.refund_task；钱包风控审批保存在独立请求/审核表中。

> **与 `payment.fsm.md` 的关系**:`refund_record.status` 字段从 `pending` 流转到终态(`success` / `rejected`)后,退款成功会影响父 `payment_order.status`:
> - 任一 `refund_record` 成功 → `payment_order.status` 切到 `partial_refunded`(累计 < paid_fee_cents)或 `refunded`(累计 = paid_fee_cents)

---

## 跨服务动作矩阵

| 触发场景 | 触发方 | Stream | 消费方 | 落表 |
| --- | --- | --- | --- | --- |
| gateway 启动失败 / 订单无法启动的迟到支付 | user | `refund_required_stream` | admin | `charge_order` + `refund_record(pending)` |
| 实结退款 / 钱包充值原路退款 | user | `refund_required_stream` | admin | `refund_record(pending)` |
| 双签拒绝 | admin → user 内部 API | 无 | user | `refund_rejection` + `refund_record(rejected)` |
| 退款回调成功/失败 | user | `comp_tx_stream` (`refund_completed`) | worker | worker `comp_tx_log` 仅审计；user 在回调事务内更新 `refund_record` / `payment_order` |
| 退款事件投递失败 | user Outbox | `refund_required_stream` | admin | consumer 重试并进入 Redis DLQ；没有自动 DLQ 人工重放 |

---

## 边界规则

- **幂等 key**:`refund_record` 写入以 `(payment_order_id, reason)` UNIQUE 防重复退款
- **微信回调幂等**:微信侧通过 `refund_record.wechat_refund_id` 防微信重复回调
- **不可跳状态**:`pending → success` 不允许(必须经过微信回调链路)
- **不可逆**:`success` / `rejected` 不可复用;需要新的、单独审批的退款申请
- **超时 / 重试**:微信状态未知时由 admin 的 refund_task 查询原退款号并继续处理；不会把网络超时误判为退款失败或新增第二笔退款
