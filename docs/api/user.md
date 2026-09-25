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

### 限流

> 限流全局规则见 `docs/技术规格.md` § 7.4(权威源),本文档仅列该服务覆写与特殊端点(各端点 `[限流]` 标注)。

- 默认:每 user 100 req/s,每 IP 1000 req/s
- 特殊端点更严:`POST /scan/start` 每 user 5 req/min
- 超过限流返回 `4291`

### 错误码

> 完整错误码字典见 `services/common-error/errors.toml` + `docs/技术规格.md` § 7.2(权威源)。
> 段位固定:`0`=成功 / `1xxx`=通用 / `2xxx`=业务(`user` 服务专属子段 2001-2099)/ `3xxx`=第三方 / `4xxx`=限流 / `5xxx`=服务器。本文仅列该服务用到的子集。

| 段位 | 含义 | 示例 |
| --- | --- | --- |
| 1xxx | 通用错误 | 1001 未授权 / 1003 禁止访问 / 1004 资源不存在 |
| 2xxx | 业务错误 | 2001 端口被占用 / 2002 设备已停用 / 2003 余额不足 |
| 3xxx | 第三方错误 | 3001 微信支付失败 / 3002 微信退款失败 |
| 4xxx | 限流 | 4291 超过限流 |
| 5xxx | 服务器错误 | 5001 内部错误 / 5003 服务暂时不可用 |

## 端点清单(共 30 个)

### 公开接口(无需鉴权)

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/public/auth/login` | 微信 code 换 JWT |
| POST | `/api/v1/public/auth/refresh` | 刷新 JWT |
| POST | `/api/v1/public/payment/wechat/callback` | 微信支付回调 |

### 扫码与充电(5s 轮询链路)

**核心语义(P0-1):扫码 ≠ 启动**。扫码 / 选端口 = 仅展示;启动必须等支付回调成功 → `charge_started_stream` → gateway 启动。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/user/scan/resolve` | 扫码路由:端口码 → 详情 / 设备码 → 端口列表(**只读,不锁端口**) |
| POST | `/api/v1/user/scan/port` | 单端口详情(**只读,不锁端口**) |
| POST | `/api/v1/user/scan/start` | 创建订单 + 微信 JSAPI 预下单 + 占逻辑锁,**不直接启动设备** |
| POST | `/api/v1/user/scan/cancel` | 60s 窗口内取消未支付订单(P0-1 新增) |
| GET | `/api/v1/user/charge/ongoing/snapshot` | 充电中 5s 轮询快照(含告警) |
| GET | `/api/v1/user/charge/ongoing/curve` | 充电中曲线(功率/电流/SOC/温度) |
| POST | `/api/v1/user/charge/stop` | 主动停止充电 |
| GET | `/api/v1/user/charge/history` | 历史订单列表 |
| GET | `/api/v1/user/charge/{order_id}` | 订单详情 |
| GET | `/api/v1/user/charge/{order_id}/curve` | 历史订单曲线回看 |
| POST | `/api/v1/user/charge/{order_id}/feedback` | 提交评价 / 投诉 |

### 用户与钱包

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/user/profile` | 个人中心 |
| POST | `/api/v1/user/phone/bind` | 绑定手机号(可选,绑送奖励) |
| GET | `/api/v1/user/wallet/balance` | 钱包余额查询 |
| POST | `/api/v1/user/wallet/recharge` | 钱包充值(微信支付下单) |
| GET | `/api/v1/user/wallet/txns` | 余额流水(分页) |
| POST | `/api/v1/user/wallet/refund` | 余额退款申请 |

### 站点与找桩

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/user/station/nearby` | 附近站点(经纬度 + 半径) |
| GET | `/api/v1/user/station/{station_id}` | 站点详情(端口列表 + 实时空闲数) |
| POST | `/api/v1/user/device/report-fault` | 用户报修充电桩故障 |

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
| POST | `/api/v1/user/customer-service/entry` | 分配客服坐席(前端用 wx.openCustomerServiceChat 唤起) |

---

## 公开接口

### `POST /api/v1/public/auth/login`

**鉴权**:[公开]
**触发场景**:小程序启动 / 任何需要 user_id 的端点之前
**业务目标**:`wx.login()` 拿 code → 调微信 `code2Session` 换 `openid` → 签发 JWT

**请求体**:
```json
{
  "code": "08123456..."        // wx.login() 返回的临时凭证(微信 code2Session 唯一入参)
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
**触发场景**:小程序"端口详情"页面 → 用户点击"开始充电"
**业务目标**:基于已确认的 `port_id` 创建充电订单 + 微信 JSAPI 预下单,**返回拉起支付控件所需的参数**。
**重要语义**:**此端点不会直接启动设备**。设备启动 = 微信支付回调成功 + 发 `charge_started_stream` 后由 gateway 完成(详见 `docs/diagrams/charge-payment-sequence.md`)。

**请求体**:
```json
{
  "port_id": "xx_001_01"        // 必须从 /scan/port 或 /scan/resolve 拿到的 port_id
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
    "payment_params": {           // 微信 JSAPI 拉起支付控件所需参数
      "appId": "wx...",
      "timeStamp": "1695638400",
      "nonceStr": "5K8264ILTKCH...",
      "package": "prepay_id=wx201...",
      "signType": "RSA",
      "paySign": "oR9d8PuhnIc+Y..."
    },
    "estimated_rate": {           // 预估费率(展示用)
      "electric_cents_per_kwh": 55,
      "service_cents_per_kwh": 50
    },
    "hold_expires_at": "2026-09-25T14:05:00Z"  // 逻辑锁过期时间,超时请重新扫码
  }
}
```

**业务逻辑**:
1. 校验 `port_id` 格式合法(§ 技术规格.md 6.2)
2. 反查 `gateway_db.device` 拿 `device_id` + `station_id`,校验 `status='enabled'` + `online=TRUE`(最近 30 min 内有心跳)
3. **逻辑锁占位**(`SET charge:hold:port_xxx <order_no> NX EX 300`,TTL=5 min):
   - 失败 → 查 holder 对应订单状态:
     - `pending_payment` 且未超时 → 返回 `2001`(端口被他人支付中)
     - `charging` → 返回 `2001`(端口占用)
4. 校验 `admin_db.pricing_rule`(注意:是 `admin_db` 不是 `charge_db`)是否对该 port 启用(无则用 station 默认规则)
5. **事务内**:
   - `INSERT charge_order(status='pending_payment', order_no, user_id, device_id, port_id, payment_order_id=NULL, ...)`
   - `INSERT payment_order(status='initiated', biz_type='charge', biz_id=charge_order.id, ...)`
   - `UPDATE charge_order.payment_order_id = payment_order.id`
6. **RPC billing**:`POST /internal/quote` 拿预估金额(展示用 + 微信下单用)
7. **调微信 JSAPI 预下单**:`POST https://api.mch.weixin.qq.com/v3/pay/transactions/jsapi` 拿 `prepay_id`
8. 封装 `payment_params` 返回小程序 → 调 `wx.requestPayment()`
9. **不要释放逻辑锁** —— 锁由 gateway 在收到 `charge_started_stream` 时验证后释放,或在 `pending_payment` 超时 / 用户取消时释放

> **错误码**:
> - `1001`: JWT 缺失 / 过期
> - `2001`: 端口被占用(逻辑锁失败)
> - `2002`: 设备已停用 / 离线
> - `2004`: 计费规则未配置(需客户运营先配置)
> - `3001`: 微信预下单失败(签名 / 证书 / 频次)
> - `4291`: 超过限流(5 req/min)
> - `5003`: billing 报价失败

---

### `POST /api/v1/user/scan/cancel`

**鉴权**:[JWT]
**限流**:每 user 5 req/min
**触发场景**:小程序"等待支付"页面 → 用户点"取消"(仅在 60s 窗口内可生效)
**业务目标**:**取消未支付订单**;若微信回调已先到(已支付)则转入退款流程

**请求体**:
```json
{
  "order_no": "CH20260925140000123"
}
```

**业务逻辑**:
1. 校验 `order_no` 归属当前 user + `status='pending_payment'`
2. 校验 `created_at > NOW() - 60s`(超过 60s 不允许"快速取消",走超时分支)
3. **事务内**:
   - `DEL charge:hold:port_xxx` 释放逻辑锁
   - `UPDATE charge_order.status='cancelled'`, `cancelled_at=NOW()`
   - `UPDATE payment_order.status='cancelled'`
4. 若 `payment_order.status='success'`(回调已先于取消到达)→ 发 `refund_required_stream`(走退款)
5. 返回 200 OK

**错误码**:
- `1001`: JWT 缺失 / 过期
- `2017`: 订单不存在 / 不属于当前用户
- `2018`: 订单不在 `pending_payment` 状态(可能已支付 / 已失败 / 已超时)
- `2019`: 超过 60s 取消窗口,走超时分支
- `4291`: 超过限流

---

### `POST /api/v1/user/scan/resolve`

**鉴权**:[JWT]
**限流**:每 user 20 req/min(扫码频次低)
**触发场景**:小程序扫码后**第一步必须调用**(路由分发)
**业务目标**:根据扫码内容智能判断是端口码还是设备码,返回对应数据
- **端口码**(二维码印在插头旁)→ 直接返回单端口详情
- **设备码**(二维码印在设备外壳)→ 返回该设备下所有端口列表

**请求体**:
```json
{
  "code": "xx_001_01"           // 扫码得到的原始字符串
}
```

**响应(200) — 端口码场景**:
```json
{
  "code": 0,
  "data": {
    "resolve_type": "port",        // 路由结果:port / device
    "port": {                      // resolve_type='port' 时填
      "port_id": "xx_001_01",
      "device_id": "xx_001",
      "station_name": "万达广场地下停车场",
      "station_address": "北京市朝阳区...",
      "port_status": "idle",       // "idle" / "charging" / "fault"
      "pricing_rule": {
        "rule_name": "万达广场 - 白天",
        "electric_cents_per_kwh": 55,
        "service_cents_per_kwh": 50
      }
    },
    "ports": null                  // resolve_type='port' 时为 null
  }
}
```

**响应(200) — 设备码场景**:
```json
{
  "code": 0,
  "data": {
    "resolve_type": "device",      // 路由结果
    "port": null,                  // resolve_type='device' 时为 null
    "ports": [                     // 该设备下所有端口列表(空闲 + 充电中 + 故障)
      {
        "port_id": "xx_001_01",
        "port_status": "idle",
        "current_order_no": null
      },
      {
        "port_id": "xx_001_02",
        "port_status": "charging",
        "current_order_no": "CH..."
      },
      {
        "port_id": "xx_001_03",
        "port_status": "fault",
        "current_order_no": null
      }
    ],
    "device_id": "xx_001",
    "station_name": "万达广场地下停车场",
    "station_address": "北京市朝阳区..."
  }
}
```

**业务逻辑**:
1. 解析 `code`:
   - 端口码格式:`<vendor>_<device>_<port>`(8-32 字符)
   - 设备码格式:`<vendor>_<device>`(短于端口码,不含端口)
2. 服务端智能判断(正则匹配 / 查数据库是否存在):
   - 匹配 `gateway_db.device.port_id` → 端口码 → `resolve_type='port'`
   - 匹配 `gateway_db.device.device_id` → 设备码 → `resolve_type='device'`
   - 既不是端口码也不是设备码 → `2016`(二维码无效)
3. **端口码分支**:查 `port_status` + `pricing_rule` → 直接返回详情(无需再调 `/scan/port`)
4. **设备码分支**:查 `port_view` 该设备下所有端口 + 实时状态 → 返回端口列表(前端展示);用户点选某个端口后再调 `/scan/port` 拿单端口详情
5. **本端点不锁端口、不写表、不创建订单**(P0-1 核心约束:扫码 ≠ 启动;启动需用户选端口后调 `/scan/start`)

**错误码**:
- `1001`: JWT 失效
- `1004`: device_id / port_id 都不存在
- `2016`: 二维码无效(格式不匹配 / 厂商未注册)
- `4291`: 超过限流(20 req/min)

---

### `POST /api/v1/user/scan/port`

**鉴权**:[JWT]
**限流**:每 user 30 req/min(浏览端口详情频次中等)
**触发场景**:从"设备码 → 端口列表"页面**用户点选某个端口后**调用,获取单端口详情
**业务目标**:展示单端口的实时状态 + 计费规则,**不创建订单、不锁端口**(P0-1 核心约束:扫码 ≠ 启动)

**请求体**:
```json
{
  "port_id": "xx_001_01"        // 必须从 /scan/resolve 或 /station/{id} 拿到的 port_id
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
    "port_status": "idle",       // "idle" / "charging" / "fault"
    "pricing_rule": {
      "rule_name": "万达广场 - 白天",
      "electric_cents_per_kwh": 55,
      "service_cents_per_kwh": 50
    }
  }
}
```

**业务逻辑**:
1. 校验 `port_id` 格式
2. 查 `gateway_db.device` + `port_view` 拿 `port_status`(从最新 telemetry 推断)
3. 查 `admin_db.pricing_rule`(取 station 默认规则)
4. 返回展示数据(不创建订单,不触发副作用)

**错误码**:
- `2001`: 端口被占用(展示场景返回 `port_status='charging'`,不算错误)
- `2002`: 设备已停用
- `1004`: port_id 不存在

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
    "alert": {                    // **告警**(无告警时为 null)
      "level": "mid",             // "low" / "mid" / "high"
      "type": "temperature_high", // 告警类型(参考需求 § 7.4)
      "message": "桩端温度偏高(72°C),请注意充电安全",
      "suggest_action": "check_environment"
    },
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
4. 查最新告警(`alert_event` 表中 `device_id` + `status='active'`):
   - **有** → 填 `alert` 字段(高告警前端弹窗 + 推送)
   - **无** → `alert=null`
5. 返回数据(数值字段用字符串防 JS 浮点精度)

**错误码**:
- `1001`: JWT 失效
- `1004`: 订单不存在
- `1003`: 订单不属于当前 user(越权)

---

### `GET /api/v1/user/charge/ongoing/curve`

**鉴权**:[JWT]
**限流**:每 user 6 req/min(曲线查询频次低)
**触发场景**:小程序"充电中"页面 → 用户点击"查看充电曲线"
**业务目标**:返回充电会话内的遥测时间序列(功率 / 电流 / 电压 / SOC / 温度 / 累计电量)

**请求 query**:
| 参数 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `order_id` | int | — | 充电订单 ID(必填) |
| `window` | string | `last_30min` | 时间窗:`last_5min` / `last_30min` / `last_2h` / `since_start` |

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "order_id": 12345,
    "window": "last_30min",
    "sample_interval_seconds": 60,        // 实际采样间隔(由后端压缩决定)
    "series": [
      {
        "ts": "2026-09-25T14:00:00Z",
        "voltage_v": "220.5",
        "current_a": "3.20",
        "power_w": "704.0",
        "temperature_c": "32.5",
        "battery_soc": 60,
        "meter_kwh": "0.020"
      },
      {
        "ts": "2026-09-25T14:01:00Z",
        "voltage_v": "221.0",
        "current_a": "3.18",
        "power_w": "703.0",
        "temperature_c": "32.8",
        "battery_soc": 62,
        "meter_kwh": "0.041"
      }
    ],
    "summary": {                       // 曲线摘要
      "max_power_w": "750.0",
      "max_temperature_c": "36.2",
      "max_current_a": "3.40",
      "total_kwh": "0.520"
    }
  }
}
```

**业务逻辑**:
1. 校验 `order_id` 属于当前 user(防越权 → `1003`)
2. 查 `charge_order.started_at` + `ended_at`(若已结束)确定时间窗
3. 按 `window` 从 `gateway_db.telemetry` 查数据:
   - `last_5min` / `last_30min` / `last_2h` → 查原始 telemetry(分 16 张表,按 `device_id` hash 路由)
   - `since_start` → 查 `started_at` 至今的全部 telemetry
4. **采样压缩**(避免返回过多点):
   - `last_5min` → 每 10 秒 1 点 → ≤ 30 点
   - `last_30min` → 每 1 分钟 1 点 → ≤ 30 点
   - `last_2h` → 每 5 分钟 1 点 → ≤ 24 点
   - `since_start` → 自适应:< 30 min 用 10 秒;30 min ~ 2 h 用 1 min;> 2 h 用 5 min(上限 200 点)
5. 算 `summary` 字段(最大功率 / 最高温度 / 峰值电流 / 累计电量)
6. 返回时间序列 + 摘要

**错误码**:
- `1001` / `1003` / `1004`
- `2017`: 订单未在充电中(`window=since_start` 仅适用于 finished 订单,charging 订单只能用 last_xxx)

---

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
2. **HTTP RPC 调 gateway**(`POST /api/v1/internal/stop-charge`)→ 网关通过 MQTT 下发断电指令 → 等设备 ACK
3. 同步返回 `status='cancelling'`,前端跳转到"结算中"页面
4. **退款判定 + 执行全部异步**:
   - 设备 ACK 后 gateway 发 `charge_ended_stream` → billing 消费 → 算费 + 判退款
   - 若需退款(全额 / 部分):**billing 发布 `refund_required_stream`** → admin 消费 → 调微信退款 API(详见 `docs/api/billing.md` § 六 + `docs/api/admin.md` § F)
   - 最终结果通过 `charge_ended_stream` + `refund_required_stream` 异步通知 user → 推送小程序消息
5. **本期 user 服务不发 `refund_required_stream`**(沿用 cross-reference § 1 真实生产者清单:billing 单生产)

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

### `GET /api/v1/user/charge/{order_id}/curve`

**鉴权**:[JWT]
**限流**:每 user 10 req/min
**触发场景**:小程序"订单详情"页面 → 用户点击"查看充电曲线"
**业务目标**:返回历史订单的充电曲线(从聚合表查,与 `ongoing/curve` 数据源不同)

**路径参数**:
| 参数 | 类型 | 说明 |
| --- | --- | --- |
| `order_id` | int | 订单 ID |

**请求 query**:
| 参数 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `granularity` | string | `15min` | 粒度:`15min` / `hourly`(超长会话用 hourly) |

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "order_id": 12345,
    "granularity": "15min",
    "series": [
      {
        "bucket_start": "2026-09-25T14:00:00Z",
        "voltage_v_avg": "220.5",
        "voltage_v_max": "221.0",
        "current_a_avg": "3.20",
        "current_a_max": "3.40",
        "temperature_c_avg": "32.5",
        "temperature_c_max": "34.0",
        "battery_soc_end": 62,
        "meter_kwh_end": "0.080"
      }
    ],
    "summary": {
      "max_power_w": "750.0",
      "max_temperature_c": "36.2",
      "total_kwh": "0.520",
      "avg_power_w": "700.0"
    }
  }
}
```

**业务逻辑**:
1. 校验 `order_id` 属于当前 user
2. 查 `charge_order.started_at` + `ended_at` 确定时间窗
3. 按 `granularity` 从聚合表查数据:
   - `15min` → `gateway_db.telemetry_aggregate_15min`(§ 4.6 修订,保留 3 年)
   - `hourly` → `gateway_db.telemetry_aggregate_hourly`(会话 > 24 h 时用)
4. 算 `summary` 字段
5. 返回时间序列 + 摘要

**错误码**:
- `1001` / `1003` / `1004`(同上)
- `2018`: 订单时间窗超过聚合表保留期(3 年)

---

### `POST /api/v1/user/charge/{order_id}/feedback`

**鉴权**:[JWT]
**限流**:每 user 1 req/order(每笔订单只能评价一次)
**触发场景**:小程序"充电结束页" → 用户点击"评价 / 投诉"
**业务目标**:用户对充电体验评分 + 文字反馈 / 投诉分类(需求 § 5.3)

**路径参数**:
| 参数 | 类型 | 说明 |
| --- | --- | --- |
| `order_id` | int | 订单 ID |

**请求体**:
```json
{
  "rating": 5,                    // 1-5 星(投诉场景可填 1)
  "comment": "充电很快,设备正常",   // 文字评论(选填)
  "category": "experience",       // "experience" 体验 / "device" 设备 / "fee" 费用
  "is_complaint": false,          // true = 投诉(客户运营重点跟进)
  "contact_back": true            // 是否希望客服回复(留微信号等)
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "feedback_id": 54321,
    "created_at": "2026-09-25T15:00:00Z",
    "estimated_response_hours": 24  // 若 contact_back=true,客户客服响应 SLA
  }
}
```

**业务逻辑**:
1. 校验 `order_id` 属于当前 user + `status='finished'`
2. 校验是否已评价过(查 `feedback` 表唯一索引 `uk_feedback_order_user`)→ 已评过返回 `2019`
3. **事务**:
   - INSERT `feedback(rating, comment, category, is_complaint, contact_back)`
   - `is_complaint=true` → 写 `alert_stream` 事件(优先级 mid)+ 推送客户运营 PC 后台 + 客服微信通知
   - `contact_back=true` + `is_complaint=false` → 写入客服待回访队列
4. 推送小程序消息"评价已收到,感谢您的反馈"
5. 同步返回 `feedback_id`

**错误码**:
- `1001` / `1003` / `1004`
- `2005`: 订单未结束
- `2019`: 已评价过(每笔订单仅一次)

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
**触发场景**:小程序"绑定手机号"按钮 → 前端用 `wx.getPhoneNumber` 拿明文手机号
**业务目标**:手机号绑定 + **触发绑送奖励**(§ 5.3.2,需求文档)

**请求体**:
```json
{
  "phone": "13800138000",         // 明文,前端从 wx.getPhoneNumber 拿到(微信已做合法性校验,后端再做格式校验)
  "code": "..."                   // wx.getPhoneNumber 返回的动态令牌(后端需调微信接口解密,本期简化不验证)
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
2. 校验手机号未绑定其他账号(查 `user.phone_hash` 唯一):
   - 已绑定 → 返回 `2006`(该手机号已被其他账号绑定)
3. **事务**:
   - `UPDATE user SET phone_enc=AES_ENCRYPT($phone, $key), phone_hash=SHA256($phone), registered_at 不变`
   - 查 `coupon` 模板中 `grant_source='phone_bind'` 的模板
   - 对每个模板 INSERT `coupon_grant(status='unused')`(发券,本期不送余额)
4. 推送小程序消息"您已绑定手机号,获得 X 优惠券"
5. 记录 `audit_log`("用户绑定手机号")

**错误码**:
- `1001` / `2006`(已绑其他账号)
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

### `POST /api/v1/user/wallet/recharge`

**鉴权**:[JWT]
**限流**:每 user 10 req/min
**触发场景**:小程序"钱包"页 → 用户点击"充值"→ 选档位 → 跳微信支付
**业务目标**:创建充值支付订单(走微信直连或汇付天下,§ 9.3)

**请求体**:
```json
{
  "amount_cents": 10000,           // 充值金额(分)
  "pay_channel": "wechat"          // "wechat" 微信直连 / "huifu" 汇付天下(由客户配置)
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "payment_order_no": "PY20260925...",
    "amount_cents": 10000,
    "wechat_pay_params": {          // 前端用此唤起 wx.requestPayment
      "appId": "wx...",
      "timeStamp": "...",
      "nonceStr": "...",
      "package": "...",
      "signType": "RSA",
      "paySign": "..."
    },
    "expires_at": "2026-09-25T14:15:00Z"   // 支付单过期时间(默认 15 min)
  }
}
```

**业务逻辑**:
1. 校验 `amount_cents > 0`(下限由客户配置,默认 100 分 = 1 元)
2. 校验 `pay_channel` 在客户配置中启用(读 `customer.whitelabel_config.pay_channels`)
3. 创建 `payment_order(biz_type='recharge', biz_id=NULL, parent_order_id=NULL, pay_method=$channel, total_fee_cents=$amount, status='pending')`
4. 调微信支付 V3 API(根据 `pay_channel` 路由):
   - 微信直连 → `POST /v3/pay/transactions/jsapi`
   - 汇付天下 → 调汇付 SDK(占位)
5. 拿到 `wechat_pay_params` 返回给前端
6. 等待微信回调(异步,走 `payment/wechat/callback` 端点):
   - 回调成功 → UPDATE `payment_order.status='success'` + 钱包入账 + 写 `wallet_txn`
   - 超时(15 min)→ 定时任务标 `status='failed'`

**错误码**:
- `1001` / `2020`(金额低于客户配置下限)
- `3001`: 微信支付下单失败
- `5001`: 数据库错误

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
2. 校验风控规则(§ 9.4):**二轮车 1-2 元/单场景下金额阈值无意义,本期仅频次规则**
   - 同用户 5 min 内 ≥ 3 笔退款 → 冻结 + 入人工审
3. 校验余额退款的笔次凑(§ 资金安全 4 决策):
   - 按时间顺序遍历 `payment_order WHERE biz_type='recharge' AND status='success'`
   - 每笔可退 = `paid_fee_cents - 该笔已退金额`
   - 凑到 `amount_cents` 为止(或所有笔次耗尽)
4. **事务内**:对每笔生成 `refund_record(payment_order_id, refund_cents, refund_reason='recharge_refund', status='pending')` + 冻结对应 `wallet_account.available_cents`(预扣,防双花)
5. **同步调用微信退款 API**(不走 Stream,user 自己发起同步调用;`refund_required_stream` 仅用于充电退款,billing 单生产)→ 写 `wallet_txn(txn_type='refund', status=success/failed)` + UPDATE `refund_record.status`
6. 同步返回"已受理"(`request_id`),实际到账通过微信回调异步确认;失败时回滚冻结 + 标 `refund_record.status='failed'` + 触发风控复核

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

### `POST /api/v1/user/device/report-fault`

**鉴权**:[JWT]
**限流**:每 user 5 req/day(防骚扰,真故障一天报一次足够)
**触发场景**:小程序"站点详情"页 → 用户点击"报修"按钮
**业务目标**:用户上报充电桩故障 → 客户巡检跟进

**请求体**:
```json
{
  "device_id": "xx_001",            // 报修的设备 ID
  "port_id": "xx_001_03",          // 可选,具体哪个端口故障
  "fault_type": "charging_failure", // 故障类型枚举
  "description": "插头插上后无反应",  // 文字描述
  "photos": [                      // 可选,照片 URL 列表(小程序上传到 OSS 后)
    "https://bucket.oss/photo1.jpg"
  ]
}
```

`fault_type` 枚举:
- `charging_failure`:充电失败(插上无反应 / 启动失败)
- `port_damage`:硬件损坏(插头 / 端口物理损伤)
- `display_abnormal`:显示异常(屏幕 / 指示灯)
- `network_failure`:网络故障(扫码后无法连接)
- `other`:其他

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "report_id": 88888,
    "report_no": "RP20260925...",
    "status": "pending",             // "pending" / "dispatched" / "resolved" / "closed"
    "estimated_response_hours": 24,  // 客户巡检响应 SLA(可配)
    "created_at": "2026-09-25T14:00:00Z"
  }
}
```

**业务逻辑**:
1. 校验 `device_id` 存在(查 `gateway_db.device`)
2. 校验 `port_id` 属于该 `device_id`(若提供)
3. 校验 `fault_type` 在枚举中
4. INSERT `device_fault_report(device_id, port_id, fault_type, description, photos, status='pending', user_id=$current)`
5. 发布 `alert_stream` 事件(`severity='low'`,路由给客户巡检 PC 后台)
6. 推送小程序消息"报修已收到,客服将于 24 小时内联系您"
7. 同步返回 `report_no` + `status`

**错误码**:
- `1001` / `1004`
- `2021`: 同一设备 24h 内已报修过(防骚扰)
- `2022`: 故障类型非法

---

### `POST /api/v1/user/customer-service/entry`

**鉴权**:[JWT]
**业务目标**:分配客服坐席并返回坐席信息(需求 § 5.5 微信原生客服)

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
    "agent_id": 101,                // 客服坐席 ID(前端用于展示)
    "agent_name": "客服小张",
    "agent_avatar_url": "https://...",  // 坐席头像(可选)
    "estimated_response_seconds": 60   // 首响 SLA(从 customer_service_config 读)
  }
}
```

**业务逻辑**:
1. 查 `customer_service_config WHERE status='online' AND current_chat_count < max_concurrent_chats`,按 `current_chat_count ASC` 选第一个(负载最低)
2. 选不到 → `2015`(客服繁忙,请稍后再试)
3. UPDATE `current_chat_count += 1`(临时占用,会话结束 webhook 回调时减回)
4. 返回坐席信息(前端**自行用 `wx.openCustomerServiceChat` 唤起客服会话**,本端点不返回 URL,因为微信客服唤起是前端 API,不走 URL scheme)
5. 前端唤起成功后,微信客服消息转发到客户的客服坐席微信(已在 admin 后台配置)

**错误码**:
- `1001` / `2015`(无在线客服)
- `5001`: 内部错误

---

## 文档维护

- 修改本文件需在 PR 标题写 `api(user): <简短描述>`,并在 PR 描述中说明影响哪些端点
- 任何新增 / 删除 / 修改端点必须同步更新 `services/user/src/openapi.rs` 与本文件
- CI 检查:OpenAPI 规范与本文件端点清单必须一致(脚本 `tools/check-api-consistency.ts`)
