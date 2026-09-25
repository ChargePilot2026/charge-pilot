# 充电订单状态机(`user_db.charge_order.status`)

> **维护者**:后端 user 服务 / 跨服务对齐时检查
> **配套文档**:`docs/需求分析.md` § 5.3 / `docs/api/user.md` § 扫码与充电 / `docs/技术规格.md` § 5.4(最终一致性)

---

## 状态图

```
            ┌──────────────────────────────────────────────────────────┐
            │                                                          │
            ▼                                                          │
       ┌──────────┐      设备 ACK       ┌────────────┐                 │
       │ pending  │ ─────started───────→│ charging   │                 │
       └────┬─────┘                     └──────┬─────┘                 │
            │  ACK failed                      │                        │
            ├──────────→┌──────────┐           │ 拔插 / 主动停           │
            │           │ failed   │           ▼                        │
            │           │(待退款)  │     ┌────────────┐                 │
            │           └────┬─────┘     │ finishing  │                 │
            │                │           │(收尾中)   │                 │
            │                │           └─────┬──────┘                 │
            │                │                 │  telemetry 稳定        │
            │ 60 s 内取消    │                 ▼                        │
            ├──────────→┌──────────┐     ┌────────────┐                 │
            │           │ cancelled│     │ finished   │─────────────────┘
            │           └──────────┘     └────────────┘      自动归档(deleted_at)后
            │                                                    物理保留 ≥ 3 年
            ▼
        (订单清除:仅查询时不存在;超过保留期)
```

---

## 状态枚举(`status` ENUM 值)

| 状态 | 中文 | 进入该状态的事件 | 退出该状态的事件 |
| --- | --- | --- | --- |
| `pending` | 待支付 | `POST /scan/start` 创建订单 + 调用微信下单 | 微信支付回调 → `charging` 或用户取消 → `cancelled` |
| `charging` | 充电中 | 微信支付成功回调 + gateway 已确认设备启动 | 设备断开 / 用户主动停止 / 充满自停 → `finishing` |
| `finishing` | 收尾中 | 充电结束帧上报,等待最后一次遥测确认 | 收尾完成 → `finished` |
| `finished` | 已结束 | billing 写计费快照 + 分账 | 长期不活跃(`> 24h`)→ 软删除 + worker 周期归档 |
| `failed` | 失败 | ACK failed / 30s 内未进入 charging / 安全告警导致启动中断 | billing 计算后退款 → 长期不活跃 → 软删除 |
| `cancelled` | 已取消 | 用户 60s 内取消(`POST /charge/stop`)| 直接 → 软删除 |

---

## 触发事件与跨服务动作

| 从 → 到 | 触发源 | 跨服务动作 |
| --- | --- | --- |
| (新建)→ `pending` | user API: `POST /scan/start` | user INSERT `charge_order` + 调 gateway `internal/start-charge` + 调微信支付下单 |
| `pending` → `charging` | 微信支付回调:`POST /payment/wechat/callback` | user 写 `payment_callback_idempotent` + 发布 `charge_started_stream` → gateway 消费 → MQTT 下发启动指令 |
| `charging` → `finishing` | gateway:`charge_state` 变化(断开/充满/手动停) | gateway 发布 `charge_ended_stream` → user 关闭小程序轮询(`poll_continue: false`) + billing 消费开始计费 |
| `finishing` → `finished` | billing 计费完成 | billing 写 `fee_calculation` + 写 `settlement` → 发布 `refund_required_stream`(条件触发) |
| `charging`/`pending` → `failed` | gateway ACK failed / 安全告警 / 30s 内未进入 charging | billing 写 `refund_record(status=pending)` + 发布 `refund_required_stream` |
| `pending` → `cancelled` | user: `POST /charge/stop` (60s 窗口内) | 直接软删除;若已支付 → `refund_required_stream` |

---

## 边界规则

- **不可跳跃**:不允许 `pending → finished` 等多级跳跃(任何跨越必须在日志中标注)
- **不可逆**:`finished` / `failed` / `cancelled` → 不可回退到 `charging`;重启用新订单号
- **重启用新订单**:同一端口 + 同一用户,新一次充电 = 新 `order_no`(`CH{YYYYMMDDhhmmssXXXXXX}` 格式)
- **超时硬切**:订单在 `pending` 超过 5 分钟未收到回调 → 标记 `failed` + 退款(防止"付了钱但永远没充上")

---

## 与其他状态机的关系

- **支付订单**(`payment_order.status`):`initiated` → `success` / `failed` / `refunded`(详见 `payment.fsm.md`)
- **退款记录**(`refund_record.status`):`pending` → `success` / `failed` / `manual_review`(详见 `refund.fsm.md`)
- **分账**(`settlement.status`):`pending` → `split_done` → `settled` / `withdrawn`
