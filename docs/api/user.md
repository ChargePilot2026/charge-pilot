# user 服务 API 详细设计

**服务**:`user`(`services/user`)
**对外地址**:`https://<customer-domain>/api/v1/user/...`(经 Caddy 反代到 `user:8081`)
**鉴权**:JWT(HS256,openid)放 `Authorization: Bearer <jwt>` header
**OpenAPI 文档**:`GET /api/docs/openapi.json` + Swagger UI `/api/docs/swagger`

## 通用约定

### 请求格式

- 所有请求 / 响应均为 JSON
- Content-Type: `application/json; charset=utf-8`
- 时间戳统一 ISO 8601 字符串(UTC,带 `Z` 后缀),精度毫秒
- 金额字段:`*_cents` 后缀,**单位分**(避免浮点)
- 业务 ID:`*_id` / `*_no` 字符串

### 统一响应包装

```json
{
  "code": 0,           // 0 = 成功;非 0 = 错误码(参考 § 错误码)
  "message": "ok",     // 错误描述(成功时为 "ok")
  "data": { ... },     // 业务数据(成功时;失败时为 null)
  "request_id": "..."   // 链路追踪 ID(任意错误必带)
}
```

### 鉴权要求

- 标记 `[JWT]` 的端点必须带 JWT,缺失 / 过期返回 `1001`
- 标记 `[公开]` 的端点无需鉴权,但仍需限流(防刷)
- JWT 包含 `openid` / `user_id` / `exp` 三个字段

### 限流(§ 7.4)

- 默认:每 user 100 req/s,每 IP 1000 req/s
- 特殊端点更严:`POST /scan/start` 每 user 5 req/min
- 超过限流返回 `4291`

### 错误码

| 段位 | 含义 | 示例 |
| --- | --- | --- |
| 1xxx | 通用错误 | 1001 未授权 / 1003 禁止访问 / 1004 资源不存在 |
| 2xxx | 业务错误 | 2001 端口被占用 / 2002 设备已停用 / 2003 余额不足 |
| 3xxx | 第三方错误 | 3001 微信支付失败 / 3002 微信退款失败 |
| 4xxx | 限流 | 4291 超过限流 |
| 5xxx | 服务器错误 | 5001 内部错误 / 5003 服务暂时不可用 |

## 端点清单(共 22 个)

### 公开接口(无需鉴权)

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/public/auth/login` | 微信 code 换 JWT |
| POST | `/api/v1/public/auth/refresh` | 刷新 JWT |
| POST | `/api/v1/public/payment/wechat/callback` | 微信支付回调 |

### 扫码与充电(5s 轮询链路)

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/user/scan/start` | 扫码启动充电(设备码 / 端口码) |
| POST | `/api/v1/user/scan/port` | 端口详情(扫码后展示) |
| GET | `/api/v1/user/charge/ongoing/snapshot` | 充电中 5s 轮询快照 |
| POST | `/api/v1/user/charge/stop` | 主动停止充电 |
| GET | `/api/v1/user/charge/history` | 历史订单列表 |
| GET | `/api/v1/user/charge/{order_id}` | 订单详情 |

### 用户与钱包

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/user/profile` | 个人中心 |
| POST | `/api/v1/user/phone/bind` | 绑定手机号(可选,绑送奖励) |
| GET | `/api/v1/user/wallet/balance` | 钱包余额查询 |
| GET | `/api/v1/user/wallet/txns` | 余额流水(分页) |
| POST | `/api/v1/user/wallet/refund` | 余额退款申请 |

### 站点与找桩

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/user/station/nearby` | 附近站点(经纬度 + 半径) |
| GET | `/api/v1/user/station/{station_id}` | 站点详情(端口列表 + 实时空闲数) |

### 优惠券与发票

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/user/coupon/my` | 我的优惠券(分页) |
| POST | `/api/v1/user/coupon/preview` | 优惠券预览(校验 + 算折扣) |
| POST | `/api/v1/user/invoice/apply` | 申请发票 |
| GET | `/api/v1/user/invoice/my` | 我的发票申请 |

### 公告与客服

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/user/announcement/list` | 当前生效公告 |
| POST | `/api/v1/user/customer-service/entry` | 进入客服会话(拿微信客服 URL) |

---

## 公开接口

### `POST /api/v1/public/auth/login`

**鉴权**:[公开]
**触发场景**:小程序启动 / 任何需要 user_id 的端点之前
**业务目标**:`wx.login()` 拿 code → 调微信 `code2Session` 换 `openid` → 签发 JWT

**请求体**:
```json
{
  "code": "08123456...",        // wx.login() 返回的临时凭证
  "anonymous_code": "..."       // 可选,用于未注册用户首次登录创建记录
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "jwt": "eyJhbGciOiJIUzI1NiIs...",
    "refresh_token": "RT_abc123...",
    "user_id": 12345,
    "is_new_user": false,        // 本次登录是否新创建 user 记录
    "jwt_expires_in": 900        // JWT 有效期(秒,默认 15 min)
  }
}
```

**业务逻辑**:
1. 校验 `code` 非空 + 长度合法(微信 code 通常 32 字符)
2. 调微信 `code2Session`(`https://api.weixin.qq.com/sns/jscode2session`)→ 拿 `openid` + `unionid`(如有)+ `session_key`
3. 用 `openid` 查 `user_db.user`:
   - 不存在 → INSERT `user(status='active', openid, registered_at=NOW(), last_active_at=NOW())` → `is_new_user=true`
   - 存在 → UPDATE `last_active_at=NOW()`
4. 签发 JWT(`openid` / `user_id` / `exp = NOW() + 15min`)+ Refresh Token(7 天,存 Redis 允许撤销)
5. 记录 `audit_log`(虽然是公开接口,但首次登录也算重要操作)

**错误码**:
- `3001`: 微信 `code2Session` 失败(code 无效 / 过期)
- `5001`: 内部错误(数据库 / Redis 不可用)

---

### `POST /api/v1/public/auth/refresh`

**鉴权**:[公开](Refresh Token 通过 `Authorization: Bearer <refresh_token>` 传递)

**请求头**:
```
Authorization: Bearer RT_abc123...
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "jwt": "eyJ...",         // 新的 JWT
    "refresh_token": "RT_def456...",  // 新的 Refresh Token(轮换)
    "jwt_expires_in": 900
  }
}
```

**业务逻辑**:
1. 校验 Refresh Token 格式(`RT_` 前缀)
2. 查 Redis(白名单 / 撤销列表):
   - 不在白名单 → `1001`(已撤销 / 伪造)
   - 在白名单 → 续期 + 轮换(老 Refresh Token 加入撤销列表,TTL = 剩余有效期)
3. 签发新的 JWT + 新的 Refresh Token

**错误码**:
- `1001`: Refresh Token 无效 / 已过期 / 已撤销
- `5001`: 内部错误

---

### `POST /api/v1/public/payment/wechat/callback`

**鉴权**:[公开] + **微信签名**(微信支付 V3 API RSA 签名)
**触发场景**:微信支付成功后异步回调
**业务目标**:确认微信支付入账 + 创建 charge_started 事件

**请求头**:
```
Wechatpay-Signature: ...
Wechatpay-Timestamp: ...
Wechatpay-Nonce: ...
```

**请求体**(微信回调):
```json
{
  "id": "event-uuid",
  "create_time": "2026-09-25T...",
  "resource_type": "encrypt-resource",
  "event_type": "TRANSACTION.SUCCESS",
  "resource": {
    "algorithm": "AEAD_AES_256_GCM",
    "ciphertext": "...",
    "associated_data": "...",
    "nonce": "..."
  }
}
```

**响应(200)**:
```json
{
  "code": 0,
  "message": "ok",
  "data": null
}
```

**业务逻辑**:
1. 校验微信签名(用微信平台证书验签,证书通过 API 获取并缓存)
2. 解密 `resource.ciphertext` 拿到 `transaction_id` / `amount` / `mch_id` 等
3. 查 `payment_callback_idempotent` 表(`wechat_transaction_id` 唯一):
   - **已存在** → 直接返回 200 OK(幂等,不重复处理)
   - **不存在** → INSERT 幂等记录(同一事务内)
4. 查 `payment_order(wechat_transaction_id)`:
   - 找到 → UPDATE `status='success'`, `paid_at=NOW()` + 发布 `charge_started_stream` 事件(§ 5.1)
   - 找不到 → 记录 `alert.dlq`(微信支付了但系统无订单) → 告警运维
5. 立即返回 200 OK(微信要求 5s 内响应,业务逻辑可异步)

**错误码**:
- `3001`: 微信签名验证失败(签名错 / 证书过期 / timestamp 偏差 > 5min)
- `5001`: 数据库 INSERT 失败(但已写幂等表 → 后续人工补)

---

## 扫码与充电

### `POST /api/v1/user/scan/start`

**鉴权**:[JWT]
**限流**:每 user 5 req/min
**触发场景**:小程序扫码 → 用户点击"开始充电"
**业务目标**:启动充电会话(扫设备码 / 端口码两种入口,§ 5.5 端口级并发控制)

**请求体**:
```json
{
  "code": "xx_001_01",          // 扫码得到的字符串(端口码或设备码)
  "code_type": "port",          // "port" 端口码 / "device" 设备码
  "port_id": "xx_001_01"        // code_type=device 时必填,用于选定具体端口
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "order_no": "CH20260925140000123",
    "port_id": "xx_001_01",
    "device_id": "xx_001",
    "station_id": 100,
    "started_at": "2026-09-25T14:00:00.123Z",
    "estimated_rate": {           // 预估费率(展示用)
      "electric_cents_per_kwh": 55,
      "service_cents_per_kwh": 50
    }
  }
}
```

**业务逻辑**:
1. 解析 code:
   - `code_type='port'` → `code` 即 port_id;从 `port_id` 反查 `device_id` + `station_id`
   - `code_type='device'` → 校验 `device_id` + `port_id` 关联合法
2. 校验设备状态:`gateway_db.device.status='enabled'`,且 `online=TRUE`(最近 30 min 内有心跳)
3. **端口级短锁**(`SETNX charge:lock:port_xxx`,holder=order_id,TTL=30s):
   - 失败 → 查 holder 对应订单:
     - status='charging' → 返回 `2001`(端口被占用)
     - status='created' 但 30s 内未启动 → 强制释放 + 重试
4. 校验 `charge_db.charge_rule` 是否对该 port 启用(无则用 station 默认规则)
5. `INSERT charge_order(status='pending', order_no, user_id, device_id, port_id, ...)`
6. 发布 `charge_start_request_stream` 事件 → gateway 消费 → 通过 MQTT 下发启动指令
7. 等待设备 ACK(同步阻塞,timeout 30s):
   - ACK `started` → UPDATE `charge_order.status='charging'`,`started_at=NOW()` + 主动释放锁
   - ACK `failed` → UPDATE `status='failed'` + 触发 `refund_required_stream` + 返回 `2002`
   - Timeout → 释放锁 + 返回 `5003`(网关无响应,提示用户重试)

**错误码**:
- `1001`: JWT 缺失 / 过期
- `2001`: 端口被占用(锁失败)
- `2002`: 设备已停用 / 离线
- `2003`: 该端口已存在 charging 订单(DB 唯一约束兜底)
- `2004`: 计费规则未配置(需客户运营先配置)
- `4291`: 超过限流(5 req/min)
- `5003`: 网关无响应

---

### `POST /api/v1/user/scan/port`

**鉴权**:[JWT]
**触发场景**:扫码后展示端口详情(空闲 / 充电中 / 故障 + 计费规则预览)
**业务目标**:在用户确认充电前展示信息,**不创建订单**

**请求体**:
```json
{
  "code": "xx_001_01",
  "code_type": "port"
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "port_id": "xx_001_01",
    "device_id": "xx_001",
    "station_name": "万达广场地下停车场",
    "station_address": "北京市朝阳区...",
    "port_status": "idle",      // "idle" / "charging" / "fault"
    "pricing_rule": {
      "rule_name": "万达广场 - 白天",
      "electric_cents_per_kwh": 55,
      "service_cents_per_kwh": 50
    }
  }
}
```

**业务逻辑**:
1. 解析 code(同 `/scan/start`)
2. 校验 `device_id` 存在 + enabled
3. 查 `gateway_db.device` 实时状态(`port_status` 从 telemetry 最新一条推断)
4. 查 `charge_db.charge_rule`(取 station 默认规则)
5. 返回展示数据(不创建订单,不触发副作用)

**错误码**:
- `2001`: 端口被占用(展示场景返回 port_status=charging,不算错误)
- `2002`: 设备已停用
- `1004`: 设备不存在

---

### `GET /api/v1/user/charge/ongoing/snapshot`

**鉴权**:[JWT]
**限流**:每 user 20 req/s(5s 轮询约 0.2 req/s,留 100 倍余量)
**触发场景**:小程序充电中页面每 5 秒轮询
**业务目标**:返回最新实时数据,**控制是否继续轮询**(`poll_continue` 字段)

**请求 query**:
| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `order_id` | int | 是 | 充电订单 ID |

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "order_id": 12345,
    "status": "charging",        // "charging" / "finished" / "failed" / "cancelled"
    "voltage_v": "220.50",
    "current_a": "3.20",
    "power_w": "704.00",
    "temperature_c": "32.50",
    "battery_soc": 65,
    "meter_kwh": "0.123",
    "duration_seconds": 480,
    "estimated_remaining_minutes": 120,   // 按实时功率估算充满时间
    "current_fee_cents": 12,        // 当前预估费用(电费 + 服务费)
    "poll_continue": true,         // **关键字段**:false 时前端停止轮询,跳到充电结束页
    "server_ts": "2026-09-25T14:08:00.123Z"
  }
}
```

**业务逻辑**:
1. 校验 `order_id` 属于当前 user(防越权)
2. 查 Redis `snapshot:{order_id}`(TTL 10s,worker 主动填充):
   - **Hit** → 返回缓存数据
   - **Miss** → 查 `gateway_db.telemetry`(最新 1 条)+ `charge_db.charge_order` → 组合 → 回填缓存(TTL 10s)
3. 推断 `poll_continue`:
   - `status='charging'` → true
   - `status` ∈ {`finished` / `failed` / `cancelled`} → false(前端跳转充电结束页)
4. 返回数据(数值字段用字符串防 JS 浮点精度)

**错误码**:
- `1001`: JWT 失效
- `1004`: 订单不存在
- `1003`: 订单不属于当前 user(越权)

---

### `POST /api/v1/user/charge/stop`

**鉴权**:[JWT]
**触发场景**:用户在充电中页面点击"结束充电"
**业务目标**:用户主动停止充电,触发计费 + 退款流程

**请求体**:
```json
{
  "order_id": 12345
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "order_id": 12345,
    "status": "cancelling",        // 异步中
    "estimated_fee_cents": 18,
    "message": "充电已结束,正在结算..."
  }
}
```

**业务逻辑**:
1. 校验订单属于当前 user + status='charging'
2. **启动后 > 60 秒**:按已充结算(§ 8.4):
   - 发布 `charge_stop_request_stream` 事件 → gateway 消费 → 下发断电指令
   - 设备 ACK 后 → 计费 + 支付
   - **退款逻辑**:用户已支付金额 - 实际消费 = 应退金额 → 原路退
3. **启动后 ≤ 60 秒**:按"充电失败"处理(全额原路退):
   - 同样下发断电 → 触发 `refund_required_stream`(全额退)
4. 同步返回 `status='cancelling'`,前端跳转到"结算中"页面
5. 最终结果通过 `charge_ended_stream` 异步通知 + 推送小程序消息

**错误码**:
- `1001`: JWT 失效
- `1003`: 订单不属于当前 user
- `2005`: 订单不在 charging 状态
- `5003`: gateway 无响应

---

### `GET /api/v1/user/charge/history`

**鉴权**:[JWT]
**触发场景**:小程序"我的订单"页加载
**业务目标**:分页返回当前用户的充电历史(默认按时间倒序)

**请求 query**:
| 参数 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `page` | int | 1 | 页码 |
| `page_size` | int | 20 | 每页条数(最大 100) |
| `status` | string | — | 可选过滤(charging / finished / cancelled / failed) |

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "total": 42,
    "page": 1,
    "page_size": 20,
    "items": [
      {
        "order_id": 12345,
        "order_no": "CH20260925140000123",
        "device_id": "xx_001",
        "port_id": "xx_001_01",
        "started_at": "2026-09-25T14:00:00Z",
        "ended_at": "2026-09-25T15:00:00Z",
        "duration_seconds": 3600,
        "total_fee_cents": 105,
        "status": "finished"
      }
    ]
  }
}
```

**业务逻辑**:
1. 校验 JWT 拿 user_id
2. 查 `charge_db.charge_order WHERE user_id = ? AND deleted_at IS NULL`(仓储层自动过滤)
3. 可选 `status` 过滤
4. 按 `started_at DESC` 排序 + LIMIT/OFFSET 分页
5. 关联 `station_name`(JOIN 或缓存冗余)

**错误码**:
- `1001`: JWT 失效
- 通用错误码(无业务错误)

---

### `GET /api/v1/user/charge/{order_id}`

**鉴权**:[JWT]
**触发场景**:订单详情页
**业务目标**:返回单笔订单完整信息(含计费明细)

**路径参数**:
| 参数 | 类型 | 说明 |
| --- | --- | --- |
| `order_id` | int | 订单 ID |

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "order_id": 12345,
    "order_no": "CH20260925140000123",
    "device_id": "xx_001",
    "port_id": "xx_001_01",
    "station_name": "万达广场地下停车场",
    "started_at": "2026-09-25T14:00:00Z",
    "ended_at": "2026-09-25T15:00:00Z",
    "duration_seconds": 3600,
    "meter_kwh": "0.520",
    "power_w_avg": "700.00",
    "electric_fee_cents": 29,    // 0.520 度 × 55 分/度
    "service_fee_cents": 26,    // 0.520 度 × 50 分/度
    "total_fee_cents": 55,
    "paid_fee_cents": 55,
    "billing_mode": "normal",   // "normal" / "estimated"
    "is_estimated": false,
    "status": "finished",
    "refund_status": "none",     // "none" / "pending" / "partial" / "full" / "failed"
    "refunded_cents": 0,
    "payment_order_no": "PY..."   // 关联的支付订单
  }
}
```

**业务逻辑**:
1. 校验订单属于当前 user
2. 查 `charge_db.charge_order` + 关联 `station.station_name` + `payment_order.paid_fee_cents`
3. 计算 `electric_fee_cents` / `service_fee_cents` / `total_fee_cents`(若尚未结算,从 fee_calculation 拿)
4. 返回完整明细

**错误码**:
- `1001` / `1003` / `1004`(同上)

---

## 用户与钱包

### `GET /api/v1/user/profile`

**鉴权**:[JWT]
**触发场景**:小程序"我的"页加载
**业务目标**:返回用户基本信息 + 余额 + 优惠券数量 + 会员卡状态

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "user_id": 12345,
    "nickname": "张三",
    "avatar_url": "https://...",
    "phone_bound": true,          // 手机号是否绑定(不返回明文)
    "registered_at": "2026-08-01T10:00:00Z",
    "wallet": {
      "available_cents": 5000,
      "frozen_cents": 0
    },
    "coupon_unused_count": 3,
    "membership_card": null       // 会员卡预留字段(本期无会员体系)
  }
}
```

**业务逻辑**:
1. 校验 JWT 拿 user_id
2. 查 `user_db.user`(主键)+ `wallet_account`(1:1)+ `coupon_grant` COUNT(`status='unused'`)+ `membership_card`
3. 手机号绑定状态:`phone_enc IS NOT NULL → true`,不返回明文(§ 9.4 MVP 脱敏)

**错误码**:
- `1001`(标准)

---

### `POST /api/v1/user/phone/bind`

**鉴权**:[JWT]
**触发场景**:小程序"绑定手机号"按钮
**业务目标**:手机号绑定 + **触发绑送奖励**(§ 5.3.2,需求文档)

**请求体**:
```json
{
  "phone": "13800138000",         // 明文(小程序前端从 wx.getPhoneNumber 拿)
  "verification_code": "1234"     // 短信验证码(防刷)
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "bound": true,
    "rewards": [                    // 绑送奖励(本期固定:1 张优惠券 + 0 元余额,客户可配)
      {
        "type": "coupon",
        "coupon_grant_id": 98765,
        "coupon_name": "新人 5 元抵扣券"
      }
    ]
  }
}
```

**业务逻辑**:
1. 校验手机号格式(11 位 + 1[3-9]开头)
2. 校验短信验证码(Redis 存的 code,5 min 有效)
3. 校验手机号未绑定其他账号(查 `user.phone_hash` 唯一):
   - 已绑定 → 返回 `2006`(该手机号已被其他账号绑定)
4. **事务**:
   - `UPDATE user SET phone_enc=AES_ENCRYPT($phone, $key), phone_hash=SHA256($phone), registered_at 不变`
   - 查 `coupon` 模板中 `grant_source='phone_bind'` 的模板
   - 对每个模板 INSERT `coupon_grant(status='unused')`(发券,本期不送余额)
5. 推送小程序消息"您已绑定手机号,获得 X 优惠券"
6. 记录 `audit_log`("用户绑定手机号")

**错误码**:
- `1001` / `2006`(已绑其他账号)
- `2007`: 短信验证码错误 / 过期
- `2008`: 手机号格式不合法

---

### `GET /api/v1/user/wallet/balance`

**鉴权**:[JWT]
**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "available_cents": 5000,
    "frozen_cents": 0,
    "total_recharged_cents": 10000,
    "total_consumed_cents": 5000,
    "total_refunded_cents": 0
  }
}
```

**业务逻辑**:
1. 查 `wallet_account WHERE user_id = ?`(1:1,主键索引)
2. 直接返回余额快照

**错误码**:
- `1001` / `1004`(账户未开通,理论上不会发生)

---

### `GET /api/v1/user/wallet/txns`

**鉴权**:[JWT]
**业务目标**:余额流水查询(分页)

**请求 query**:
| 参数 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `page` | int | 1 | 页码 |
| `page_size` | int | 20 | 每页条数(最大 100) |
| `type` | string | — | 可选过滤(recharge / consume / refund / freeze / unfreeze / admin_adjust) |

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "total": 15,
    "items": [
      {
        "txn_no": "WT20260925...",
        "txn_type": "consume",
        "amount_cents": -105,
        "balance_after_cents": 4895,
        "remark": "充电消费扣款",
        "created_at": "2026-09-25T15:00:00Z"
      }
    ]
  }
}
```

**业务逻辑**:
1. 校验 user
2. 查 `wallet_txn WHERE user_id = ? AND deleted_at IS NULL`(按月分区)
3. 可选 type 过滤 + 按时间倒序 + 分页

**错误码**:
- `1001` / 标准

---

### `POST /api/v1/user/wallet/refund`

**鉴权**:[JWT]
**业务目标**:用户申请退余额(按笔次凑原路退,§ 9.3)

**请求体**:
```json
{
  "amount_cents": 3000              // 申请退的分
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "request_id": "RF...",
    "refund_cents": 3000,
    "estimated_arrival": "实时到账",
    "refund_orders": [               // 实际退的笔次(便于用户核对)
      {
        "payment_order_no": "PY20260901...",
        "refund_cents": 1000,
        "wechat_refund_id": null      // 微信处理中,异步更新
      }
    ]
  }
}
```

**业务逻辑**:
1. 校验 `amount_cents > 0` 且 ≤ `wallet.available_cents`(否则 `2009`)
2. 校验风控规则(§ 9.4):
   - 同用户 5 min 内 ≥ 3 笔退款 → 冻结 + 入人工审
   - 单笔 ≥ 500 元 → 冻结 + 入人工审
3. 校验余额退款的笔次凑(§ 资金安全 4 决策):
   - 按时间顺序遍历 `payment_order WHERE biz_type='recharge' AND status='success'`
   - 每笔可退 = `paid_fee_cents - 该笔已退金额`
   - 凑到 `amount_cents` 为止(或所有笔次耗尽)
4. 对每笔生成 `refund_record(payment_order_id, refund_cents, refund_reason='recharge_refund')` + 发布 `refund_required_stream`
5. worker 异步调微信退款原路返回 + 写入 `wallet_txn(txn_type='refund')`
6. 同步返回"已受理"(`request_id`),实际到账异步通过小程序消息通知

**错误码**:
- `1001` / `2009`(余额不足)
- `2010`: 触发风控冻结,需等待人工审核(返回 `status='manual_review'`)
- `5003`: 微信退款 API 暂时不可用

---

## 站点与找桩

### `GET /api/v1/user/station/nearby`

**鉴权**:[JWT]
**触发场景**:小程序"找桩 / 地图"页
**业务目标**:返回附近的站点(经纬度 + 半径)

**请求 query**:
| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `longitude` | float | 是 | 经度(-180 ~ 180) |
| `latitude` | float | 是 | 纬度(-90 ~ 90) |
| `radius_meters` | int | 否(默认 1000) | 半径(米,最大 10000) |
| `limit` | int | 否(默认 20) | 返回条数(最大 50) |

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "items": [
      {
        "station_id": 100,
        "station_name": "万达广场地下停车场",
        "address": "北京市朝阳区...",
        "longitude": "116.480000",
        "latitude": "39.910000",
        "distance_meters": 250,
        "total_ports": 10,
        "available_ports": 7,        // 实时空闲数(从 telemetry 推断,可能略有延迟)
        "business_hours": "00:00-24:00"
      }
    ]
  }
}
```

**业务逻辑**:
1. 校验经纬度 + 半径
2. 查 `user_db.port_view`(冗余自 gateway_db,worker 同步):
   - 过滤 `status='enabled' AND deleted_at IS NULL`
   - 用 haversine 公式算距离
   - 按距离升序,LIMIT
3. 推断 `available_ports`:从最新 telemetry 找 status='idle' 的端口数(可能有 1-2 分钟延迟,worker 同步周期决定)
4. 返回列表(不含实时充电中端口的精确数,精确数见 `/station/{id}` 详情)

**错误码**:
- `1001` / 校验错误

---

### `GET /api/v1/user/station/{station_id}`

**鉴权**:[JWT]
**触发场景**:小程序"站点详情"页
**业务目标**:返回站点详情 + 端口列表(含实时状态)

**路径参数**:
| 参数 | 类型 | 说明 |
| --- | --- | --- |
| `station_id` | int | 站点 ID |

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "station_id": 100,
    "station_name": "万达广场地下停车场",
    "address": "北京市朝阳区...",
    "longitude": "116.480000",
    "latitude": "39.910000",
    "business_hours": "00:00-24:00",
    "contact_phone": "010-12345678",
    "ports": [
      {
        "port_id": "xx_001_01",
        "port_status": "idle",      // "idle" / "charging" / "fault"
        "device_id": "xx_001",
        "firmware_version": "1.2.3"
      },
      {
        "port_id": "xx_001_02",
        "port_status": "charging",
        "device_id": "xx_001",
        "firmware_version": "1.2.3",
        "current_order_no": "CH..."   // 仅 charging 状态有
      }
    ]
  }
}
```

**业务逻辑**:
1. 校验 station_id 存在 + enabled
2. 查 `admin_db.station` 元数据 + `user_db.port_view` 该站点下所有端口
3. 端口实时状态:查最新 telemetry,推断 `charge_state` ∈ {idle / charging / full / fault}
4. 充电中的端口加 `current_order_no`(脱敏,只展示订单号 + 起止时间,不展示用户信息)

**错误码**:
- `1001` / `1004`

---

## 优惠券与发票

### `GET /api/v1/user/coupon/my`

**鉴权**:[JWT]
**业务目标**:我的优惠券(分页,按可用优先)

**请求 query**:
| 参数 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `page` | int | 1 | 页码 |
| `page_size` | int | 20 | 每页条数(最大 100) |
| `only_available` | bool | true | 只显示 status='unused' 且在有效期内 |

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "total": 3,
    "items": [
      {
        "coupon_grant_id": 98765,
        "coupon_name": "新人 5 元抵扣券",
        "coupon_type": "fixed_amount",
        "discount_cents": 500,
        "min_spend_cents": 0,
        "valid_from": "2026-09-25T00:00:00Z",
        "valid_until": "2026-10-25T00:00:00Z",
        "status": "unused"
      }
    ]
  }
}
```

**业务逻辑**:
1. 查 `coupon_grant WHERE user_id = ? AND (status='unused' AND valid_until > NOW())`,按 `valid_until ASC` 排序(快过期的在前)
2. 关联 `coupon` 表拿名称 / 类型 / 面值
3. 分页返回

**错误码**:
- `1001`

---

### `POST /api/v1/user/coupon/preview`

**鉴权**:[JWT]
**业务目标**:使用前预览(校验 + 算实际折扣金额),**不真使用**

**请求体**:
```json
{
  "coupon_grant_id": 98765,
  "order_total_cents": 3000          // 预估订单总金额(充电中页面调用)
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "valid": true,
    "coupon_grant_id": 98765,
    "coupon_name": "新人 5 元抵扣券",
    "discount_cents": 500,           // 实际可减 5 元
    "final_cents": 2500,             // 实付 25 元
    "reason": null                    // valid=false 时填原因
  }
}
```

**业务逻辑**:
1. 校验 `coupon_grant` 属于当前 user
2. 校验 `status='unused'` 且 `valid_until > NOW()`
3. 校验 `order_total_cents >= min_spend_cents`
4. 算折扣:
   - `fixed_amount` → `discount_cents`(模板面值)
   - `percentage` → `order_total * percent`,但 ≤ `max_discount_cents`
   - `full_reduction` → `discount_cents`(但 `order_total >= min_spend`)
5. 返回 `valid=true` + 折扣明细

**错误码**:
- `1001` / `1004` / `1003`(券不属于当前 user)
- `2011`: 券已使用 / 过期 / 冻结
- `2012`: 订单金额不满足 `min_spend_cents`

---

### `POST /api/v1/user/invoice/apply`

**鉴权**:[JWT]
**业务目标**:用户申请发票(需求 § 6.6)

**请求体**:
```json
{
  "invoice_type": "company",         // "personal" / "company"
  "title": "某科技有限公司",
  "tax_id": "91110000123456789X",     // company 时必填
  "email": "finance@example.com",
  "payment_order_ids": [12345, 67890]  // 申请开票的支付订单(可多选)
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "request_no": "INV20260925...",
    "amount_cents": 10500,
    "status": "pending",               // 待审核
    "estimated_processing_hours": 48  // 客户财务审核 SOP(可配)
  }
}
```

**业务逻辑**:
1. 校验 `invoice_type` / `title` / `email` 格式;`company` 时 `tax_id` 必填(否则 `2013`)
2. 校验 `payment_order_ids` 全部属于当前 user + `biz_type='charge' + status='success'`(充值款不开票)
3. 校验金额守恒:`SUM(paid_fee_cents) = 用户填的金额` → 自动重算,无需前端传 amount
4. INSERT `invoice_request(status='pending')` + 记录 `audit_log`
5. 客户财务 PC 后台"发票审核"队列收到通知(推送 Webhook)
6. 同步返回 `request_no` + `status='pending'`

**错误码**:
- `1001` / `1003` / `1004`
- `2013`: 必填字段缺失(company 时缺 tax_id)
- `2014`: 支付订单中有未支付 / 已退款的订单

---

### `GET /api/v1/user/invoice/my`

**鉴权**:[JWT]
**业务目标**:我的发票申请列表

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "items": [
      {
        "request_no": "INV20260925...",
        "invoice_type": "company",
        "title": "某科技有限公司",
        "amount_cents": 10500,
        "status": "pending",            // "pending" / "approved" / "rejected" / "issued" / "failed"
        "created_at": "2026-09-25T14:00:00Z",
        "invoice_file_url": null       // 已开票才有
      }
    ]
  }
}
```

**业务逻辑**:
1. 查 `invoice_request WHERE user_id = ? AND deleted_at IS NULL`,按时间倒序
2. 关联 `invoice_review` 拿最新状态(`admin_db.invoice_review` 决定 `status`)

**错误码**:
- `1001`

---

## 公告与客服

### `GET /api/v1/user/announcement/list`

**鉴权**:[JWT]
**业务目标**:当前生效的公告列表(展示 + 弹窗)

**请求 query**:
| 参数 | 类型 | 说明 |
| --- | --- | --- |
| `display_mode` | string | 可选过滤(popup / list / banner) |
| `limit` | int(默认 5) | 条数 |

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "items": [
      {
        "announcement_id": 123,
        "title": "国庆期间维护通知",
        "content": "10月1日 02:00-04:00 维护...",
        "announcement_type": "maintenance",
        "display_mode": "popup",
        "priority": 8,
        "valid_from": "2026-09-30T00:00:00Z",
        "valid_until": "2026-10-02T00:00:00Z"
      }
    ]
  }
}
```

**业务逻辑**:
1. 查 `admin_db.announcement WHERE status='enabled' AND valid_from <= NOW() AND (valid_until IS NULL OR valid_until > NOW()) AND deleted_at IS NULL`
2. 可选 `display_mode` 过滤
3. 按 `priority ASC, valid_from DESC` 排序(高级别优先)
4. LIMIT

**错误码**:
- `1001`

---

### `POST /api/v1/user/customer-service/entry`

**鉴权**:[JWT]
**业务目标**:获取微信原生客服会话入口(需求 § 5.5 微信原生客服)

**请求体**:
```json
{
  "scene": "general"               // "general" / "refund" / "complaint" — 路由到不同客服坐席
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "chat_url": "wxacommsg://...",   // 微信原生客服会话 URL
    "agent_name": "客服小张",
    "estimated_response_seconds": 60  // 首响 SLA(从 customer_service_config 读)
  }
}
```

**业务逻辑**:
1. 查 `customer_service_config WHERE status='online' AND current_chat_count < max_concurrent_chats`,按 `current_chat_count ASC` 选第一个(负载最低)
2. 选不到 → `2015`(客服繁忙,请稍后再试)
3. UPDATE `current_chat_count += 1`(临时占用,会话结束 webhook 回调时减回)
4. 调微信 `customerServiceMessage` API 拿 `chat_url`(签名 + ticket)
5. 返回 URL + 客服名 + SLA

**错误码**:
- `1001` / `2015`(无在线客服)
- `3003`: 微信客服 API 失败

---

## 文档维护

- 修改本文件需在 PR 标题写 `api(user): <简短描述>`,并在 PR 描述中说明影响哪些端点
- 任何新增 / 删除 / 修改端点必须同步更新 `services/user/src/openapi.rs` 与本文件
- CI 检查:OpenAPI 规范与本文件端点清单必须一致(脚本 `tools/check-api-consistency.ts`)
