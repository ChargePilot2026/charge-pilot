# 充电订单状态机(`user_db.charge_order.status`)

> 启动、取消、退款时序以 `docs/diagrams/charge-payment-sequence.md` 为准;字段以 `docs/db/user.md` 为准。

## 状态与迁移

```text
新订单 → pending_payment ── gateway 启动成功且 user 占用端口成功 ──→ charging ── 停止确认 ──→ finished
                 ├── 60 秒内取消且尚未支付 ──→ cancelled
                 └── 支付失败、等待超时或启动失败 ──→ failed
```

| 状态 | 含义 | 可进入的下一状态 |
| --- | --- | --- |
| `pending` | 未进入支付的预留状态,`/scan/start` 不写此值 | `pending_payment` / `cancelled` |
| `pending_payment` | 已创建支付单、等待回调及设备启动结果 | `charging` / `cancelled` / `failed` |
| `charging` | 设备已启动且 user 已占用 `active_port_charge` | `finished` / `failed` |
| `finished` | 设备已停止,进入计费与分账 | 终态 |
| `cancelled` | 未支付且主动取消 | 终态;迟到的成功支付单独走退款补偿 |
| `failed` | 支付、启动或安全检查失败 | 终态;已支付时走退款补偿 |

`status` ENUM 仅包含以上六个值。`finishing` 是设备断电到最后一帧遥测确认的处理阶段,不写入 `charge_order.status`。人工审核由 `refund_record.status='manual_review'` 表示,不写入充电订单状态。

## 关键事务与幂等

| 触发 | user 服务的持久化动作 | 外部动作 |
| --- | --- | --- |
| `/scan/start` | `charge_order(pending_payment)` + `payment_order(initiated)` | 逻辑锁;微信 JSAPI 预下单,`out_trade_no=payment_order.order_no` |
| 微信成功回调 | 锁定订单;幂等表 + `payment_order(success)` + `event_outbox(charge_started_stream)` 同事务 | outbox 发布器重试 XADD;gateway 消费 |
| gateway ACK 成功 | `active_port_charge` 插入 + `charge_order(charging)` 同事务 | gateway 通过 user 内部接口报告结果;持久化确认后 ACK Stream |
| gateway ACK 失败 | `charge_order(failed)` + `event_outbox(comp_tx_stream,charge_refund_requested)` 同事务 | billing 发布 `refund_required_stream` |
| 60 秒内取消 | 锁定充电单与支付单,要求 `pending_payment` + `initiated`,两单同事务取消 | 微信关单;按 `order_no` 比较删除逻辑锁 |
| 设备停止 | `charge_order(finished)` + 按订单 ID 删除 `active_port_charge` | billing 独立计费;user 关闭轮询 |

同一个 gateway 指令使用稳定 `event_key`,重复回传不得重复迁移。支付回调先于取消提交时,取消返回 `2018`;取消先提交时,迟到支付回调记录真实支付并通过补偿事件退款,不得启动设备。

## 端口并发防护

1. Redis `charge:hold:port_xxx` 为支付前逻辑锁,值为 `order_no`,TTL 5 分钟。
2. Redis `charge:lock:port_xxx` 为 gateway 指令物理锁,值为 `order_no`,TTL 30 秒。
3. 不分区的 `user_db.active_port_charge` 以 `port_id` 为主键,保障跨月只有一笔进行中订单。`charge_order` 按月分区,不能在其上建立不含 `created_month` 的跨月唯一键。

Redis 锁释放必须比较持有者;TTL 到期不会自动释放数据库占用。若设备已启动但端口数据库占用冲突,gateway 先 STOP 并确认断电,再进入失败与退款补偿。
