# 充电订单状态机(`user_db.charge_order.status`)

> **维护者**:后端 user 服务 / 跨服务对齐时检查
> **配套文档**:`docs/diagrams/charge-payment-sequence.md`(**时序权威源**,扫码 ≠ 启动)
> / `docs/需求分析.md` § 5.3 / `docs/api/user.md` § 扫码与充电 / `docs/技术规格.md` § 5.4 + § 5.5

---

## 状态图

```
            ┌──────────────────────────────────────────────────────────┐
            │                                                          │
            ▼                                                          │
       ┌────────────┐    微信回调成功     ┌────────────┐                │
       │ pending    │ ─────success───────→│ charging   │                │
       │ payment    │  (charge_started)   └──────┬─────┘                │
       └─────┬──────┘                           │                       │
             │                                   │ 拔插 / 主动停 / 充满自停
             │ 微信回调失败 / 60s 内取消         ▼
             ├──────────→┌──────────┐      ┌────────────┐
             │           │ failed   │      │ finishing  │
             │           │ (待退款) │      │ (收尾中)   │
             │           └────┬─────┘      └─────┬──────┘
             │                │                  │  telemetry 稳定
             ├──────────→┌──────────┐       ┌────────────┐
             │           │ cancelled│       │ finished   │──→ 软删除 + worker 归档
             │           └──────────┘       └────────────┘     (≥ 3 年保留)
             │
             │ gateway 启动失败(ACK failed / 30s 未进入 charging)
             ▼
        (触发 refund_required_stream → 自动退款 → 长期不活跃 → 软删除)
```

> **关键变更(P0-1)**:`pending` 拆分为 `pending` (仅记录) + `pending_payment` (扫码后等支付)。
> 新增状态 `pending_payment` 表示"扫码 + 选端口 + 已调微信预下单,**等待微信支付回调**"。
> 扫码 ≠ 启动,启动必须等支付回调成功发 `charge_started_stream` 后由 gateway 完成(详见 `charge-payment-sequence.md`)。

---

## 状态枚举(`status` ENUM 值)

| 状态 | 中文 | 进入该状态的事件 | 退出该状态的事件 |
| --- | --- | --- | --- |
| `pending` | 待启动 | 用户扫码选端口,创建订单 + 调微信预下单 | 微信预下单完成 → `pending_payment`(中间过渡) |
| `pending_payment` | 待支付回调 | 微信预下单成功,等待 `payment/wechat/callback` | 回调 success → `charging` / 回调 failed → `failed` / 60s 内取消 → `cancelled` |
| `charging` | 充电中 | gateway 已确认设备启动 + `started_at` 已记录 | 设备断开 / 主动停 / 充满 → `finishing` |
| `finishing` | 收尾中 | 充电结束帧上报,等待最后一次遥测确认 | 收尾完成 → `finished` |
| `finished` | 已结束 | billing 写计费快照 + 分账 | 长期不活跃(`> 24h`)→ 软删除 + worker 周期归档 |
| `failed` | 失败 | ACK failed / 30s 内未进入 charging / 安全告警导致启动中断 / 微信回调 business_code ≠ SUCCESS | billing 计算后退款 → 长期不活跃 → 软删除 |
| `cancelled` | 已取消 | 用户 60s 内取消(`POST /scan/cancel`)| 直接 → 软删除;若已支付 → `refund_required_stream` |

---

## 端口锁分层(新增)

| 锁类型 | Redis key | TTL | 时机 | 作用 |
| --- | --- | --- | --- | --- |
| **逻辑锁** | `charge:hold:port_xxx` | **5 min** | `POST /scan/start` 时占位 | 同一端口同时只能被一人支付中 |
| **物理锁** | `charge:lock:port_xxx` | **30 s** | gateway 收到 `charge_started_stream` 准备下发 START 时 | 启动链路的并发互斥;ACK 后立即 DEL |
| **DB 兜底** | `charge_order.active_charging` generated column + `UNIQUE (port_id, active_charging)` | — | INSERT/UPDATE 时自动 | 锁失效后的最后一道防线 |

详细时序与悬空保护见 `charge-payment-sequence.md` § 3。

---

## 触发事件与跨服务动作

| 从 → 到 | 触发源 | 跨服务动作 |
| --- | --- | --- |
| (新建)→ `pending_payment` | user API: `POST /scan/start` | user INSERT `charge_order(status=pending_payment, payment_order_id=NULL)` + 占逻辑锁 `charge:hold:port_xxx` + INSERT `payment_order(status=initiated)` + 调微信 JSAPI 预下单 |
| `pending_payment` → `charging` | 微信支付回调:`POST /payment/wechat/callback` | user 写 `payment_callback_idempotent` + UPDATE `payment_order.status='success'` + 关联 `charge_order.payment_order_id` + 发 `charge_started_stream` → gateway 消费 → 验证逻辑锁 → SETNX 物理锁 → MQTT 下发启动指令 → ACK → UPDATE `charge_order.status='charging'` |
| `charging` → `finishing` | gateway:`charge_state` 变化(断开/充满/手动停) | gateway 发 `charge_ended_stream` → user 关闭小程序轮询(`poll_continue: false`) + billing 消费开始计费 |
| `finishing` → `finished` | billing 计费完成 | billing 写 `fee_calculation` + `settlement` → 条件触发发 `refund_required_stream` |
| `pending_payment` → `failed` | gateway ACK failed / 安全告警 / 30s 内未进入 charging | 发 `refund_required_stream` → admin 调微信退款 → 标记 `failed` |
| `pending_payment` → `cancelled` | user: `POST /scan/cancel` (60s 窗口内) | 软删除 + 释放逻辑锁;若已支付 → `refund_required_stream` |

---

## 边界规则

- **不可跳跃**:不允许 `pending → charging` 等多级跳跃;必须 `pending → pending_payment → charging`(中间有 `pending_payment` 是支付回调异步特征)
- **不可逆**:`finished` / `failed` / `cancelled` → 不可回退到任何前置状态;重启用新订单号
- **重启用新订单**:同一端口 + 同一用户,新一次充电 = 新 `order_no`(`CH{YYYYMMDDhhmmssXXXXXX}` 格式)
- **超时硬切**:订单在 `pending_payment` 超过 **5 分钟**未收到微信回调 → 标记 `failed` + 退款(防止"付了钱但永远没充上")。逻辑锁 TTL 5 min 配合此超时硬切
- **支付回调已到但逻辑锁过期**:gateway 收到 `charge_started_stream` 时先验证锁仍属本订单,失败则退款(见 `charge-payment-sequence.md` § 3 悬空保护)

---

## 与其他状态机的关系

- **支付订单**(`payment_order.status`):`initiated` → `success` / `failed` / `refunded`(详见 `payment.fsm.md`)
- **退款记录**(`refund_record.status`):`pending` → `success` / `failed` / `manual_review`(详见 `refund.fsm.md`)
- **分账**(`settlement.status`):`pending` → `split_done` → `settled` / `withdrawn`
