# 退款状态机(`user_db.refund_record.status` + `billing_db` 协作)

> **配套文档**:`docs/需求分析.md` § 5.3 / § 8.4 / `docs/技术规格.md` § 5.4 / `docs/api/billing.md`

---

## 状态图

```
   ┌────────────┐    退款请求入队      ┌────────────┐   调微信退款 API   ┌────────────┐
   │  pending   │ ──────────────────→ │ processing │ ─────────────────→│  waiting   │
   └────────────┘                     └────────────┘                    │ (微信处理中)│
        ↑                                  │                            └─────┬──────┘
        │ 触发失败重试                       │ 系统异常(本地失败)                │ 微信回调
        │                                  ▼                                 │
   ┌────────────┐                    ┌────────────┐                          ▼
   │  retried   │ ←──── 3 次失败 ────│  failed   │                   ┌────────────┐
   └────────────┘     后转人工       │  (本地)   │                   │  success   │
                                    └─────┬──────┘                   │  (已退款)  │
                                          │ 入 DLQ + 告警              └────────────┘
                                          ▼
                                    ┌─────────────┐
                                    │ manual_review│ ← 计量异常 / 异常订单 / 风控冻结
                                    └─────────────┘
                                          │ 财务在 PC 后台审核
                                          ▼
                                    ┌────────────┐
                                    │ settled    │  →  财务线下打款 + 记录到账
                                    └────────────┘
```

---

## 状态枚举

| 状态 | 触发进入 | 触发退出 |
| --- | --- | --- |
| `pending` | billing 检测到需退款(失败 / 超时 / 60s 内取消 / 提前拔出) | 进入 `processing`(admin 消费 `refund_required_stream`) |
| `processing` | admin 调微信退款 API 已发出 | 进 `waiting`(微信返回 prepay_id)/ 进 `failed`(本地异常) |
| `waiting` | 微信侧已受理,等待异步通知 | 进 `success`(微信回调成功)/ 进 `retried`(微信要求重试) |
| `success` | 微信回调:退款到账 | 长期保留(原始记录 ≥ 3 年合规) |
| `retried` | 微信侧可重试场景:`SYSTEMERROR` / `BANK_SYSTEM_ERROR` | 进入 `processing` 重新发起;3 次后入 `failed` |
| `failed` | 本地异常 / 微信不可重试错误码(`INVALID_REQUEST` 等) | 自动入 DLQ → 转 `manual_review` |
| `manual_review` | 计量异常 / 风控冻结 / 异常订单 | 财务审核 → `success` / `settled`(线下打款) |
| `settled` | 财务决定线下打款(不走微信原路) | 长期保留(合规底线) |

---

## 跨服务动作矩阵

| 触发场景 | 触发方 | Stream | 消费方 | 落表 |
| --- | --- | --- | --- | --- |
| 充电失败(任何 `failed` 路径) | billing | `refund_required_stream` | admin | `refund_record(status=processing)` |
| 充电超时(>10h) | billing | `refund_required_stream` | admin | 同上 |
| 用户 60s 内取消 | user | `refund_required_stream` | admin | 同上 |
| 计量异常 | billing | 不发 Stream,留 `manual_review` | 无 | `refund_record(status=manual_review)` |
| 退款成功 | admin | `comp_tx_stream` | billing | `refund_record(status=success)` + `charge_order(status=refunded)` |
| 退款失败入 DLQ | admin | `alert_stream` | worker | `dlq_log` + `alert_event(severity=critical)` |

---

## 边界规则

- **幂等 key**:`refund_record` 写入以 `(payment_order_id, reason)` UNIQUE 防重复退款
- **微信回调幂等**:微信侧通过 `refund_record.wechat_refund_id` 防微信重复回调
- **不可跳状态**:`pending → success` 不允许(必须经过微信回调链路)
- **不可逆**:`success` / `settled` 不可再发起二次退款;必须创建反向手动调整记录
- **超时**:admin 调微信退款超时 → 默认 30s → 进入 `retried`(重试状态由微信响应决定)

---

## 与支付订单状态机的关系

详见 `payment.fsm.md`;退款成功后:
- `payment_order.status`:`success` → `refunded`(若全额) / `partial_refunded`(若部分)
- `charge_order.status`:通常同步转为 `refunded`(一次性清理)
- `wallet_txn`:写一条负向流水(`type='refund'`, `amount=-原值`)
