# 充电 + 支付时序图(P0 权威源)

> **核心原则**:**扫码 ≠ 启动**。扫码仅展示端口 / 设备,**不锁端口、不发任何 Stream**。
> **启动时机** = 用户**选择端口** + **微信支付成功回调**之后。
> **配套文档**:`docs/diagrams/charge-order.fsm.md`(状态机)/ `docs/api/user.md` § 扫码与充电 / `docs/db/user.md` `charge_order` + `payment_order` / `docs/技术规格.md` § 5.4 / § 5.5
> **本文档覆盖**:**主链路** + **失败分支** + **超时分支** + **取消分支** + **退款链路**

---

## § 1 主链路(理想路径)

```
用户            小程序              user-svc           billing-svc       gateway         设备           Redis           微信
 │                │                    │                  │                │              │              │              │
 │ 1.打开小程序    │                    │                  │                │              │              │              │
 │ 2.wx.login()   │                    │                  │                │              │              │              │
 │  ────────────→ │ POST /public/auth/login                  │                │              │              │              │
 │                │ ─────────────────→│                   │                │              │              │              │
 │                │                   │ code2Session     →│                │              │              │              │
 │                │                   │ ──────────────────────────────────────────────────────────────→ 微信          │
 │                │                   │ ← openid + session_key ───────────────────────────────────────────│              │
 │                │ ← JWT + refresh ← │                   │                │              │              │              │
 │                │                   │                  │                │              │              │              │
 │ 3.扫端口码      │                    │                  │                │              │              │              │
 │                │ POST /user/scan/resolve │              │                │              │              │              │
 │                │ ─────────────────→│ (只读,不锁端口,不写表)               │              │              │              │
 │                │ ← 端口详情 ←       │                  │                │              │              │              │
 │ (若设备码:列表)  │                    │                  │                │              │              │              │
 │                │ POST /user/scan/port │                 │                │              │              │              │
 │                │ ─────────────────→│ (单端口详情)     │                │              │              │              │
 │                │ ← 端口详情 ←       │                  │                │              │              │              │              │
 │                │                   │                  │                │              │              │              │
 │ 4.点"开始充电"   │                    │                  │                │              │              │              │
 │                │ POST /user/scan/start                  │                │              │              │              │
 │                │   body: {port_id}                       │                │              │              │              │
 │                │ ─────────────────→│                  │                │              │              │              │
 │                │                   │ ① SETNX charge:hold:port_xxx (逻辑锁,TTL 5min)               │              │
 │                │                   │ ② INSERT charge_order(status=pending_payment, payment_order_id=NULL)│              │
 │                │                   │ ③ INSERT payment_order(status=initiated, biz_type='charge', biz_id=charge_order.id)│              │
 │                │                   │ ④ RPC billing POST /internal/quote (报价)                   │              │
 │                │                   │ ⑤ POST https://api.mch.weixin.qq.com/v3/pay/transactions/jsapi                │
 │                │                   │ ───────────────────────────────────────────────────────────────────────────────────→ 微信 │
 │                │                   │ ← prepay_id + payment_params ──────────────────────────────────────────────│            │
 │                │ ← payment_params  │                  │                │              │              │              │
 │ ← 拉起支付控件   │                    │                  │                │              │              │              │
 │ 5.wx.requestPayment()               │                  │                │              │              │              │
 │  ────────────→ │ ───────────────────────────────────────────────────────────────────────────────────────────────→ 微信       │
 │ 6.输入密码并付   │                    │                  │                │              │              │              │
 │ 7.微信异步回调   │ POST /public/payment/wechat/callback  │                │              │              │              │
 │                │ ─────────────────→│                  │                │              │              │              │
 │                │                   │ ① 验签(微信平台证书 RSA)             │              │              │              │
 │                │                   │ ② 解密 resource.ciphertext → transaction_id / amount               │              │
 │                │                   │ ③ 查 payment_callback_idempotent(transaction_id) ─────→ MySQL ───┐        │
 │                │                   │   - 已存在 → 直接 200 OK (幂等跳过)        │              │              │
 │                │                   │   - 不存在 ↓                              │              │              │
 │                │                   │ ④ INSERT payment_callback_idempotent ───┤ → 200 OK    │
 │                │                   │ ⑤ UPDATE payment_order.status='success', paid_at=NOW() ──┤              │
 │                │                   │ ⑥ UPDATE charge_order.payment_order_id = payment_order.id ─┤              │
 │                │                   │ ⑦ XADD charge_started_stream ─────→ Redis ────────────┤              │
 │                │                   │ ⑧ 返回 200 OK (微信要求 5s 内响应)    │              │              │
 │                │ ← 200 OK         │                  │                │              │              │              │
 │                │                   │                  │                │              │              │              │
 │                │                   │                  │                │ XREADGROUP charge_started_stream.user-cg →│
 │                │                   │ 关闭"立即等待支付"轮询界面           │              │              │              │
 │                │                   │                  │                │              │              │              │
 │                │                   │                  │                │ XREADGROUP charge_started_stream.gateway-cg ─→
 │                │                   │                  │                │ charge_ended_stream.device_event_stream ───┤
 │                │                   │                  │                │              │              │              │
 │                │                   │                  │                │ ① 验证逻辑锁 charge:hold:port_xxx 仍属本订单
 │                │                   │                  │                │    - 否则 → DEL stream event + refund_required_stream
 │                │                   │                  │                │ ② 释放逻辑锁
 │                │                   │                  │                │ ③ SETNX charge:lock:port_xxx (物理锁,TTL 30s)
 │                │                   │                  │                │ ④ MQTT 下发 charge/{vendor}/{device}/cmd = START ──→
 │                │                   │                  │                │ ← device ACK (relay_closed) ←──────────────┤              │
 │                │                   │                  │                │ ⑤ UPDATE charge_order.status='charging', started_at=NOW() ──┤
 │                │                   │                  │                │ ⑥ DEL 物理锁
 │                │                   │                  │                │ ⑦ 设备开始供电 → 上行 telemetry / event ──→  │
 │                │                   │                  │                │              │              │              │
 │ 8.进入"充电中"页 │ GET /user/charge/ongoing/snapshot?order_id=xxx  │              │              │              │              │
 │                │ ─────────────────→│ (每 5s 1 次)    │                │              │              │              │
 │                │                   │ ① 读 Redis snapshot:{order_id} (cache hit > 90%)                │
 │                │                   │ ② cache miss → SELECT gateway_db.telemetry + user_db.charge_order    │
 │                │                   │ ③ 回填 Redis TTL=10s                              │
 │                │ ← snapshot JSON ← │                  │                │              │              │              │
 │                │                   │                  │                │              │              │              │
 │ ... 5s 轮询 N 次 ...               │                  │                │              │              │              │
 │                │                   │                  │                │              │              │              │
 │ 9.用户拔插头 / 主动停止 / 充满自停  │                  │                │              │              │              │
 │                │                   │                  │                │ ← relay_open / charge_state change ←────│              │
 │                │                   │                  │                │ charge_state 变化 → XADD charge_ended_stream ─→ Redis
 │                │                   │                  │                │ device_event_stream.payload = {order_id, ended_at, reason}    │
 │                │                   │                  │                │              │              │              │
 │                │                   │ XREADGROUP charge_ended_stream.user-cg → Redis                │
 │                │                   │ ① DEL snapshot:{order_id} 缓存         │              │              │              │
 │                │                   │ ② UPDATE charge_order.status='finished', ended_at=NOW()         │
 │                │                   │ ③ 后续轮询 → poll_continue:false → 客户端跳转充电结束页        │
 │                │                   │                  │                │              │              │              │
 │                │                   │                  │ XREADGROUP charge_ended_stream.billing-cg → Redis    │
 │                │                   │                  │ ① SELECT admin_db.pricing_rule(经 Redis cache TTL=10min)                │
 │                │                   │                  │ ② INSERT billing_db.fee_calculation + pricing_tier_snapshot                 │
 │                │                   │                  │ ③ INSERT billing_db.settlement + settlement_party_amount                   │
 │                │                   │                  │ ④ 判断触发退款条件? 详见 § 2 失败分支
 │                │                   │                  │                  │              │              │              │
 │                │                   │                  │ ⑤ 小程序消息推送账单 → user-svc publish notify → 用户收到               │
 │                │ ← 充电结束页      │                  │                │              │              │              │
 │                │                   │                  │                │              │              │              │
 │ (后续可选)       │ POST /charge/{id}/feedback 评价/投诉  │                │              │              │              │
 │                │ POST /invoice/apply 发票申请          │                │              │              │              │
 │                │ ─────────────────→│ 写 user_db.feedback / invoice_request             │
```

---

## § 2 失败 / 退款分支

### § 2.1 微信支付回调失败(用户拒付 / 余额不足 / 风控)

```
支付回调 → 验签 / 解密成功 → business_code != "SUCCESS"
  ├─ user-svc: UPDATE payment_order.status='failed', failed_at=NOW()
  ├─ DEL charge:hold:port_xxx 逻辑锁(端口释放)
  ├─ 写 user_db.payment_callback_idempotent (防重放)
  └─ 用户小程序 UI 显示"支付失败,可重试"
```

### § 2.2 gateway 启动失败(设备 ACK failed / 30s 内未进入 charging)

```
gateway 收到 charge_started_stream → MQTT START → 设备 ACK `failed` / 超时 30s
  ├─ UPDATE charge_order.status='failed', failed_reason='gateway_no_ack'
  ├─ DEL charge:hold:port_xxx 逻辑锁
  ├─ XADD refund_required_stream.payload = {order_id, payment_order_id, reason='start_failed'}
  │   → admin-svc 消费
  │     ├─ INSERT user_db.refund_record(status='processing')
  │     ├─ POST 微信 refund API
  │     ├─ 微信回调 → UPDATE refund_record.status='success'
  │     └─ XADD comp_tx_stream.payload = {refund_id} → billing-svc 消费
  │       └─ UPDATE billing_db.settlement.status='refunded'
  └─ 小程序消息推送退款通知给用户
```

### § 2.3 充电超时(>10h,GB 47371)

```
gateway 检测充电时长 > 10h
  ├─ MQTT STOP → 设备断电
  ├─ XADD charge_ended_stream.payload = {reason='timeout'}
  └─ billing-svc 消费 → fee_calculation → settlement → refund_required_stream (自动退款)
```

### § 2.4 用户主动取消(60s 内)

```
用户在小程序"支付等待"页 → 点"取消"
  ├─ POST /user/scan/cancel {order_id}
  ├─ user-svc:
  │   ├─ 校验 order 状态 ∈ {pending_payment},且 created_at > NOW() - 60s (超过 60s 不可取消,走超时分支)
  │   ├─ DEL charge:hold:port_xxx 逻辑锁
  │   ├─ UPDATE charge_order.status='cancelled', cancelled_at=NOW()
  │   ├─ UPDATE payment_order.status='cancelled'
  │   └─ 若已支付 (微信回调先到 / 取消时已成功):XADD refund_required_stream
  └─ 用户 UI 回到扫码页
```

### § 2.5 计量异常 / 余额不足 → 不自动退款(走人工)

```
billing-svc 计算后检测异常 → 不发 refund_required_stream
  ├─ UPDATE charge_order.status='manual_review'
  ├─ INSERT user_db.refund_record(status='manual_review')
  ├─ XADD alert_stream.payload = {type='manual_review_required', order_id}
  ├─ admin PC 后台告警列表 → 客户财务 / 客服人工介入
  └─ 微信消息通知用户"订单待人工审核"
```

---

## § 3 端口锁分层(取代单一 SETNX 30s 锁)

| 锁类型 | Redis key | TTL | 时机 | 作用 |
| --- | --- | --- | --- | --- |
| **逻辑锁**(端口预约) | `charge:hold:port_xxx` | **5 min** | `POST /scan/start` 时占位(支付前) | 同一端口同时只能被一人支付中;TTL 长,容许用户支付迟疑 |
| **物理锁**(启动链路) | `charge:lock:port_xxx` | **30 s** | gateway 准备下发 START 指令时 | 启动链路的并发互斥;ACK 后立即 DEL |

**双重校验**:
1. **逻辑锁**:防止两个用户同时点"开始充电 → 支付"
2. **物理锁**:防止支付回调与重发 / 重试导致的重复启动
3. **DB 兜底**:`charge_order` 用 generated column `active_charging BOOLEAN GENERATED AS (CASE WHEN status='charging' THEN 1 ELSE NULL END) STORED` + `UNIQUE (port_id, active_charging)`,DB 层兜底唯一性(NULL 不参与)

**逻辑锁的悬空保护**:
- 用户付完款但回调延迟 5 min → 锁过期 → 别的用户扫码
- 解决:微信回调成功后,网关收到 `charge_started_stream` 时**先验证 `charge:hold:port_xxx` 仍属本订单** → 否则退款
- 若锁已过期且无他人占位 → 正常启动(用户已付钱)
- 若锁已过期且他人正在支付 → DEL stream event → refund_required_stream(给前用户退款)

---

## § 4 与现有文档的同步点

| 文档 | 需要修改的地方 |
| --- | --- |
| `docs/diagrams/charge-order.fsm.md` | 加 `pending_payment` 状态;连接 § 3 锁分层 |
| `docs/api/user.md` | `POST /scan/start`:明确不直接启动 + 创建 pending_payment + 预下单;新增 `POST /scan/cancel` |
| `docs/db/user.md` | `charge_order.status` ENUM 加 `pending_payment`;新增 `active_charging` generated column + UNIQUE 索引 |
| `docs/cross-reference.md` | § 4.1 user 端点表新增 `POST /scan/cancel`;§ 4.2 状态流转图 |
| `docs/技术规格.md` | § 2.1.3 充电支付时序改为本文档引用;§ 5.5 锁分层对齐本文档 § 3 |
| `docs/diagrams/data-lineage.md` | 主链路图加 `pending_payment` 节点 |