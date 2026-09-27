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
| POST | `/api/v1/user/phone/bind` | 通过微信一次性凭证验证并绑定手机号 |
| POST | `/api/v1/user/phone/unbind` | 解除当前账号的手机号绑定 |
| GET | `/api/v1/user/wallet/balance` | 钱包余额查询 |
| POST | `/api/v1/user/wallet/recharge` | 钱包充值(微信支付下单) |
| GET | `/api/v1/user/wallet/txns` | 余额流水(分页) |
| POST | `/api/v1/user/wallet/refund` | 余额退款申请 |
| GET | `/api/v1/user/wallet/refunds` | 当前用户的退款申请、拆单进度与已退金额 |

### 站点与找桩

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/user/station/nearby` | 附近站点(经纬度 + 半径) |
| GET | `/api/v1/user/station/{station_id}` | 站点详情(端口列表 + 实时空闲数) |
| POST | `/api/v1/user/device/report-fault` | 用户报修充电桩故障 |
| GET | `/api/v1/user/device/fault-reports` | 查询本人设备报修进度 |
| GET | `/api/v1/user/device/fault-reports/{id}/history` | 查看本人报修公开处理记录和备注 |

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
3. 从解密结果读取 `out_trade_no`、`transaction_id`、`amount.total`、`mchid`;以 `out_trade_no = payment_order.order_no` 查支付单并校验商户号、金额与支付方式。`wechat_transaction_id` 在下单时为 NULL,只能在此处回填,不能用于首次定位订单。
4. 在 `user_db` 事务内锁定对应 `payment_order` 行;`biz_type='charge'` 时再锁定对应 `charge_order`。以 `payment_callback_idempotent.wechat_transaction_id` 唯一键防重复入账:
   - 首次成功回调:INSERT 幂等记录,将支付单置为 `success` 并回填 `wechat_transaction_id` / `paid_at`。
   - 若充电订单仍为 `pending_payment` 且逻辑锁仍属于本 `order_no`,同事务 INSERT `event_outbox(event_key='charge-start:{payment_order_id}', stream_name='charge_started_stream')`。
   - 若订单已取消 / 超时或锁已属于他人,同事务写入 `refund_record` 和 `refund_required_stream` outbox;不得启动设备。`comp_tx_stream` 不承担待退款请求。
   - 若 `biz_type='recharge'`,同事务按支付单号幂等增加 `wallet_account` 余额并写 `wallet_txn`;不发 `charge_started_stream`。
   - 若幂等记录已存在,不重复入账;仍须检查对应 outbox 是否待发布,由发布器继续重试。
5. 事务提交后由 user outbox 发布器 `XADD` 并重试至确认,消费者用 `event_key` 去重。只有数据库事务成功才返回 200;数据库失败返回 5xx 让微信重试。Redis 暂时不可用不丢事件,发布器告警并继续重试。

**错误码**:
- `3001`: 微信签名验证失败(签名错 / 证书过期 / timestamp 偏差 > 5min)
- `5001`: 数据库事务失败(整体回滚,返回 5xx 供微信重试)

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
4. 经 admin 内部接口校验 `admin_db.pricing_rule` 是否对该 port 启用(无则用 station 默认规则),user 不直连 admin schema
5. **事务内**:
   - `INSERT charge_order(status='pending_payment', order_no, user_id, device_id, port_id, payment_order_id=NULL, ...)`
   - `INSERT payment_order(status='initiated', biz_type='charge', biz_id=charge_order.id, ...)`
   - `UPDATE charge_order.payment_order_id = payment_order.id`
6. **RPC billing**:`POST /api/v1/internal/quote` 拿预估金额(展示用 + 微信下单用)
7. **调微信 JSAPI 预下单**:`POST https://api.mch.weixin.qq.com/v3/pay/transactions/jsapi`,令请求的 `out_trade_no = payment_order.order_no`,拿 `prepay_id`
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
1. 以 `order_no` 找到当前 user 的充电订单;在 `user_db` 事务内 `SELECT ... FOR UPDATE` 锁定充电订单及关联支付单,校验 `charge_order.status='pending_payment'`、`payment_order.status='initiated'` 且 `created_at > NOW() - INTERVAL 60 SECOND`。
2. 若支付回调已先提交、支付单为 `success`,返回 `2018` 并提示用户进入充电中页或停止充电;不得把已支付状态覆盖为 `cancelled`。若取消先提交,迟到的成功回调按上述回调逻辑发起退款,不得启动设备。
3. 事务内将两张订单分别置为 `cancelled`,记录取消时间;提交后调用微信关单。关单与回调竞争时以已验证回调为准,补偿事件由回调事务写入 outbox。
4. 仅当 Redis `charge:hold:port_xxx` 的值仍等于本 `order_no` 时,用原子比较删除释放逻辑锁;不得直接 `DEL` 以免误删后来订单的锁。返回 200 OK。

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

**业务逻辑**(P1-7 修正:**禁止跨库直读**,所有跨服务数据走 HTTP 内部接口):
1. 校验 `order_id` 属于当前 user(防越权)
2. 先查 `user_db.charge_order` 并校验用户归属；充电中时 **HTTP 调 gateway**:`GET /api/v1/internal/devices/{device_id}/snapshot?order_id={order_id}&port_no={port_no}`(详见 `gateway.md` § 四)，组合真实遥测并回填 Redis 缓存(TTL 10s)。缓存不能覆盖数据库状态或订单身份。
3. 推断 `poll_continue`:
   - `status='charging'` → true
   - `status` ∈ {`finished` / `failed` / `cancelled`} → false(前端跳转充电结束页)
4. 查最新告警(**HTTP 调 admin**):`GET /api/v1/internal/alerts?device_id={device_id}&status=active`(详见 `admin.md` § E):
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

**业务逻辑**(P1-7 修正:跨库走 HTTP):
1. 校验 `order_id` 属于当前 user(防越权 → `1003`)
2. 查 `user_db.charge_order.started_at` + `ended_at`(若已结束)确定时间窗
3. **HTTP 调 gateway**:`GET /api/v1/internal/devices/{device_id}/curve?order_id={order_id}&port_no={port_no}&started_at={started_at}&window=last_5min`(详见 `gateway.md` § 四):
   - gateway 在 `gateway_db` 内部查 telemetry(分 16 张表,按 `device_id` hash 路由)
   - gateway 返回采样后的时间序列
4. **采样压缩**(gateway 侧完成):
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
2. **HTTP RPC 调 gateway**(`POST /api/v1/internal/charge-orders/stop`)→ 网关通过已验证的 TCP JSON 会话下发 STOP → 等设备 ACK
3. 同步返回 `status='cancelling'`,前端跳转到"结算中"页面
4. **退款判定 + 执行全部异步**:
   - 设备 ACK 后 gateway 发 `charge_ended_stream` → billing 消费 → 算费 + 判退款
   - 若需退款(全额 / 部分):user 将退款记录与 `refund_required_stream` outbox 同事务保存 → admin 消费并调微信退款 API(详见 `docs/api/admin.md` § F)
   - 微信退款回调由 user 更新退款/支付状态，并写 `refund_completed` 到 `comp_tx_stream`，worker 仅审计该结果
5. **退款事件由 user 服务发出**；billing 不消费 `comp_tx_stream`，也不写用户退款表。

**错误码**:
- `1001`: JWT 失效
- `1003`: 订单不属于当前 user
- `2005`: 订单不在 charging 状态
- `5003`: gateway 无响应

---

### `GET /api/v1/user/charge/history`

> 实现进度：当前已返回 total/page/page_size/items，并支持 status（finished 映射 completed）；page 必须大于 0，page_size 为 1–100。JWT 用户归属及软删除在 SQL 中过滤。当前返回状态使用数据库 completed；站点名称关联仍待实现，客户端暂展示设备编号。

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

**业务逻辑**(P1-7 修正:**charge_order 在 user_db,不是 charge_db**):
1. 校验 JWT 拿 user_id
2. 查 `user_db.charge_order WHERE user_id = ? AND deleted_at IS NULL`(仓储层自动过滤)
3. 可选 `status` 过滤
4. 按 `started_at DESC` 排序 + LIMIT/OFFSET 分页
5. 关联 `station_name`(**HTTP 调 admin**:`GET /api/v1/internal/stations/{station_id}` 或查 user_db 缓存)

**错误码**:
- `1001`: JWT 失效
- 通用错误码(无业务错误)

---

### `GET /api/v1/user/charge/{order_id}`

> 实现进度：已按 JWT 用户归属过滤并支持数值 ID/历史订单号；不存在、其他用户和软删除订单均返回 404。详情包含价费分离、支付、退款摘要，费用优先取 billing 结果。终端用户不会收到分账参与方明细。下方完整目标中站点关联、平均功率及估算标记尚未全部实现。

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

**业务逻辑**(P1-7 修正):
1. 校验订单属于当前 user
2. 查 `user_db.charge_order`(本 schema) + 关联 `user_db.payment_order`(本 schema,`paid_fee_cents`)
3. **HTTP 调 admin**:`GET /api/v1/internal/stations/{station_id}` 拿 `station_name`
4. **HTTP 调 billing**:`GET /api/v1/internal/orders/{order_id}/fee-breakdown` 拿计费明细(若未结算,返回待结算标记)
5. 计算 `electric_fee_cents` / `service_fee_cents` / `total_fee_cents` 组合返回

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

**业务逻辑**(P1-7 修正:跨库走 HTTP):
1. 校验 `order_id` 属于当前 user
2. 查 `user_db.charge_order.started_at` + `ended_at` 确定时间窗
3. **HTTP 调 gateway**:`GET /api/v1/internal/devices/{device_id}/historical-curve?order_id={order_id}&port_no={port_no}&started_at={started_at}&ended_at={ended_at}&granularity={15min|hourly}`(详见 `gateway.md` § 四):
   - gateway 在 `gateway_db` 内部查对应聚合表(`telemetry_aggregate_15min` / `telemetry_aggregate_hourly`)
   - gateway 返回采样后的时间序列
4. 算 `summary` 字段(gateway 侧完成)
5. 返回时间序列 + 摘要

**错误码**:
- `1001` / `1003` / `1004`(同上)
- `2018`: 订单时间窗超过聚合表保留期(3 年)

---

### `POST /api/v1/user/charge/{order_id}/feedback`

**鉴权**:[JWT]
**限流**:每 user 1 req/order(每笔订单只能评价一次)
**触发场景**:小程序订单详情 → 用户提交评价、投诉或建议
**业务目标**:对已完成充电订单提交一次反馈

**路径参数**:
| 参数 | 类型 | 说明 |
| --- | --- | --- |
| `order_id` | int | 订单 ID |

**请求体**:
```json
{
  "rating": 5,
  "category": "rating",             // "rating" / "complaint" / "suggestion"
  "content": "充电很快，设备正常",   // 选填,最多 2000 字
  "images": []                      // 可选,最多 5 个 HTTPS 链接
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "submitted": true,
    "feedback_id": "54321"
  }
}
```

**业务逻辑**:
1. 校验订单归属及 `status='completed'`，锁订单行以串行化重复提交。
2. 每笔订单只接受一次反馈；校验评分、类型、内容和 HTTPS 图片链接。
3. 在 `feedback` 表持久化并返回反馈 ID。后台通过服务间队列读取记录、回复或关闭；微信模板消息/订阅消息推送仍未配置。

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
3. 手机号绑定状态:`phone_hash IS NOT NULL → true`,不返回明文

**错误码**:
- `1001`(标准)

---

### `POST /api/v1/user/phone/bind`

**鉴权**:[JWT]
**触发场景**:小程序"绑定手机号"按钮 → `wx.getPhoneNumber` 返回一次性凭证
**业务目标**:由微信服务端验证手机号并绑定；当前未配置绑送奖励

**请求体**:
```json
{
  "code": "..."                   // wx.getPhoneNumber 授权事件返回的一次性凭证
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": { "bound": true }
}
```

**业务逻辑**:
1. 服务端使用微信小程序 access token 调用 `wxa/business/getuserphonenumber` 消费一次性凭证；access token 缓存于业务 Redis。
2. 校验微信返回的手机号格式并在服务端计算 SHA-256，不接收客户端自报手机号或哈希，不持久化明文。
3. 事务内检查手机号未绑定其他有效账号并更新当前用户；`user.phone_hash` 由唯一索引保证并发绑定冲突返回 409。
4. 微信凭证过期或微信服务暂不可用时不修改账号，用户重新授权后可重试。手机号绑定赠券尚未配置模板来源，当前不发放奖励。

**错误码**:
- `1001` / `1002`(手机号已绑定其他账号)
- `1000`: 微信手机号格式或授权凭证无效

---

### `POST /api/v1/user/phone/unbind`

**鉴权**:[JWT]
**请求体**:无
**响应**:`{ "code": 0, "data": { "unbound": true } }`

解除当前用户的手机号哈希绑定并清除兼容字段 `phone_enc`。解除后该手机号可由其他账号重新绑定；再次绑定仍需完成微信手机号授权。

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

**鉴权**：[JWT]。当前实现微信直连，汇付渠道及客户自定义额度仍待实现。

请求 `{ "request_id": "UUID", "amount_cents": 10000 }`，可选 `client_ip`。金额为整数分，范围 100–100000000。请求编号须是规范小写 UUID，同一编号绑定账号和金额；重试不能换编号或修改金额。

成功响应 data：`request_id`、字符串 `pay_order_id`、`pay_order_no`、`amount_cents`、`status`、`expires_at`、`can_pay`；仅可支付时含 `payment_params`（RSA 微信支付参数）。支付窗口固定为首次创建后 5 分钟，重试不延长。

先在同一事务持久化请求编号、`payment_order(biz_type=wallet_recharge,status=initiated)` 和完整微信请求，再调用渠道。渠道网络失败后仍保留原支付单，重试使用相同商户单号和参数；已缓存 prepay_id 时直接重新签名。已支付或过期返回 `can_pay=false`，不会重新下单。请求归属或金额冲突返回 409。

仅服务端验证过的支付通知/回执可把订单更新为 paid 并入账钱包、生成充值流水；小程序支付弹窗成功不代表到账。取消支付与网络异常保留本地请求，并可从服务端充值记录继续支付。

### `GET /api/v1/user/wallet/recharges`

**鉴权**：[JWT]。当前账号通过新充值入口创建的记录，按创建时间及请求编号倒序。请求 `page` 默认为 1，范围 1–100000，每页 20 条。

响应 data：`user_id`（字符串）、`page`、`items`。条目包含 `request_id`、`pay_order_no`、`amount_cents`、`status`、`expires_at`、`can_pay`。不返回支付签名。未确认支付且窗口已结束的订单显示等待核实，不把客户端到期推断为支付失败。旧版未绑定请求编号的充值仍通过钱包流水查询，不出现在此恢复列表。

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
5. 退款审核通过后 user 写入 `refund_required_stream` outbox，由 admin 调用微信退款 API；退款结果回调再更新钱包/退款记录。钱包退款也使用此 Stream，不由 user 同步调用微信退款。
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
| `status` | string | `unused` | `unused`（有效可用）、`used` 或 `expired` |
| `page` | int | 1 | 页码 |
| `page_size` | int | 20 | 每页条数(1–100) |

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "total": 3,
    "page": 1,
    "page_size": 20,
    "items": [{
        "grant_id": 98765,
        "name": "新人 5 元抵扣券",
        "discount_type": "amount",
        "discount_value_cents": 500,
        "discount_percent": null,
        "min_charge_cents": 0,
        "expired_at": "2026-10-25T00:00:00Z",
        "status": "unused"
    }]
  }
}
```

**业务逻辑**:
1. 只查当前用户且未软删除的发放记录，并按 `expired_at` 排序。
2. `unused` 筛选排除已过期记录；`expired` 同时包含已标记过期以及尚未清理但已过期的未使用记录。
3. 返回模板名称、折扣配置、门槛和发放实例状态，分页最大 100 条。

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
  "estimated_total_cents": 3000      // 预估金额(分)
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "coupon_id": 123,
    "discount_type": "amount",
    "discount_cents": 500,
    "final_cents": 2500
  }
}
```

**业务逻辑**:
1. 校验 `coupon_grant` 属于当前 user、状态未使用且未超过 `expired_at`。
2. 校验估算金额为正且不超过 1,000,000 元。
3. 校验 `estimated_total_cents >= min_charge_cents`。
4. 算折扣:
   - `amount` → 抵扣金额不超过预计订单金额
   - `percentage` → 按模板折扣比例计算
   - `time_free` → 按全额抵扣预览
4. 预览不核销优惠券，也不创建订单或支付单。当前扫码支付尚未接入优惠券抵扣。

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
  "biz_type": "charge",
  "biz_id": 12345,                     // 本人已完成的 charge_order.id
  "total_cents": 10500,                // 必须与实际支付金额相同
  "invoice_type": "normal",            // "normal" / "vat_special"
  "title": "某科技有限公司",
  "tax_no": null,
  "email": "finance@example.com"
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": { "invoice_no": "INV20260925..." }
}
```

**业务逻辑**:
1. 当前只允许对本人已完成、支付成功且无退款的单笔充电订单开票，钱包充值不支持。
2. 服务端锁定支付单并校验 `total_cents` 与实际实付金额相等；重复申请返回冲突。
3. 校验标题、发票类型、专票税号及邮箱，然后创建待审记录。

**错误码**:
- `1001` / `1003` / `1004`
- `1000`: 字段、发票类型、税号或金额无效
- `1002`: 订单不符合开票条件或已存在申请

---

### `GET /api/v1/user/invoice/my`

**鉴权**:[JWT]
**业务目标**:我的发票申请列表

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "total": 1,
    "page": 1,
    "page_size": 20,
    "items": [
      {
        "invoice_no": "INV20260925...",
        "biz_type": "charge",
        "title": "某科技有限公司",
        "total_cents": 10500,
        "review_status": "pending",
        "created_at": "2026-09-25T14:00:00Z",
        "reject_reason": null,
        "invoice_url": null
      }
    ]
  }
}
```

**业务逻辑**:
1. 查 `invoice_request WHERE user_id = ? AND deleted_at IS NULL`,按时间倒序
2. 返回当前审核状态、拒绝原因及已开具的发票链接，同时返回 total/page/page_size；page/page_size 默认 1/20，最大 100。

**错误码**:
- `1001`

---

## 公告与客服

### `GET /api/v1/user/announcement/list`

**鉴权**:[JWT]
**业务目标**:当前生效的公告列表(展示 + 弹窗)

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "items": [
      {
        "id": 123,
        "title": "国庆期间维护通知",
        "content": "10月1日 02:00-04:00 维护...",
        "priority": 8
      }
    ]
  }
}
```

**业务逻辑**:
1. user 服务经 Service Token 调用 admin 内部接口，查询当前有效且未撤回公告。
2. 上游缺失或失败按真实服务错误返回，不转换成空公告列表。

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
  "fault_type": "electrical",      // 故障类型枚举
  "description": "插头插上后无反应",  // 文字描述
  "images": [                      // 可选,HTTPS 图片 URL 列表(小程序上传到 OSS 后)
    "https://bucket.oss/photo1.jpg"
  ]
}
```

`fault_type` 枚举:
- `mechanical`:机械损坏
- `electrical`:电气异常
- `communication`:通讯异常
- `display`:显示异常
- `other`:其他

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "submitted": true,
    "report_id": "54321"
  }
}
```

**业务逻辑**:
1. 校验设备编号与故障类型，并通过 gateway 内部 API 确认设备存在。
2. 每用户滚动 24 小时最多提交 5 次；图片最多 5 个 HTTPS URL。
3. 持久化报修信息并返回报修 ID。后台巡检队列支持派单、修复和关闭；小程序可查询本人报修编号和处理状态。图片上传本身及消息通知仍未实现。

**错误码**:
- `1001` / `1004`
- `4291`: 超过每日报修次数
- `1000`: 设备编号、说明、图片或故障类型无效

### `GET /api/v1/user/device/fault-reports`

仅返回当前 JWT 用户创建且未删除的报修记录，按提交时间倒序分页。支持 `page`（从 1 开始）和 `page_size`（1–100）；响应为 `items/total/page/page_size`，条目包含报修编号、设备、类型、说明、当前状态、提交及修复时间。`GET /api/v1/user/device/fault-reports/{id}/history` 仅允许报修人读取，按时间顺序返回 `event_id`、提交/派单/改派/修复/关闭状态及面向用户的备注，不返回后台账号 ID 或内部迁移快照。其他用户或已删除报修统一返回 404。

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
    "agent_name": "客服小张",
    "agent_wechat": "cs_xiaozhang",
    "entry_url": "https://work.weixin.qq.com/...",
    "corp_id": "ww...",
    "available": true,
    "scene": "general"
  }
}
```

**业务逻辑**:
1. admin 从启用坐席中按优先级选择入口；未配置坐席时返回 404。
2. `available=true` 需要同时配置 HTTPS 客服入口 URL 和 `WECHAT_CUSTOMER_SERVICE_CORP_ID`。小程序由用户点击后调用 `wx.openCustomerServiceChat`。
3. 未配置企业客服入口时仍返回客服微信号供用户复制，不伪造会话已打开。

**错误码**:
- `1001` / `2015`(无在线客服)
- `5001`: 内部错误

---

## 内部接口(仅服务间调用,不计入公开端点数)

### 评价与设备报修处理（仅 admin 服务）

以下 user 内部接口要求 `X-Service-Token`，禁止小程序 JWT 调用。`GET /api/v1/internal/feedback` 和 `GET /api/v1/internal/device-fault-reports` 支持 `status/page/page_size`；admin 会先按 `feedback.read` 或 `fault.read` 检查操作人权限再代理请求。`POST /api/v1/internal/feedback/{id}/reply` 接收 `{ "actor_id": 12, "action": "reply", "reply_content": "..." }` 或 action=`close`。报修通过 `POST /api/v1/internal/device-fault-reports/{id}/dispatch` 指定有效 admin 用户 ID，再由被指派人使用 `/resolve` 更新为 `fixed` 或 `closed`。状态变更带行锁且重放幂等；报修状态事件、操作人、备注与工单更新同事务写入 user_db 历史表，内部 history API 可读全部信息，user history API 仅返回本人可见字段。

`GET /api/v1/internal/devices/{device_id}/orders` 同样要求 `X-Service-Token`，返回该设备最近 100 笔未删除订单，供 gateway 运维查询代理使用。订单字段严格解码自 `user_db.charge_order`；数据库查询失败不会被替换为空列表。

`GET /api/v1/internal/charge-orders/charging` 同样要求 `X-Service-Token`，只返回最多 100 条当前充电订单的快照预热字段；worker 使用它取 user 所有的订单身份和生命周期数据，再经 gateway 查询设备遥测，不直接跨库读取 user 或 gateway 表。

### 仪表盘指标

`GET /api/v1/internal/dashboard/metrics` 要求 `X-Service-Token`，由 user 服务从 `charge_order` 计算当前充电订单数、今日下单用户数、今日已完成订单实结金额及近七天每日完成订单和金额。金额取订单最终 `total_cents`，按充电结束日期归属；退款尚未在该汇总中冲减。admin 同时从本地 admin_db 读取当前告警数。

所有路径仅在 `:8081` 内网监听,必须携带 `X-Service-Token: <service_token>`。调用方不能直接连接 `user_db`。

| 方法 | 路径 | 调用方 | 用途 |
| --- | --- | --- | --- |
| POST | `/api/v1/internal/charge-orders/{order_id}/start-result` | gateway | 设备启动 ACK 结果回写 |
| GET | `/api/v1/internal/payment-orders/{payment_order_id}` | billing | 读取支付状态与实付金额,用于退款决策 |
| POST | `/api/v1/internal/refund-records/claim` | admin | 按 `event_key` 幂等创建 / 领取退款记录 |
| POST | `/api/v1/internal/refund-records/{refund_id}/result` | admin | 写入微信退款结果 |
| GET | `/api/v1/internal/invoices/{invoice_id}` | admin | 按 user 发票 ID 或发票号读取待审详情 |
| POST | `/api/v1/internal/invoices/{invoice_id}` | admin | 按审核人、决定、开票 HTTPS 链接或拒绝原因幂等更新申请状态 |

### `POST /api/v1/internal/charge-orders/{order_id}/start-result`

当前请求体：`{order_no, command_id, device_id, port_no, port_id?, success, error?}`。`order_id` 路径值为充电订单号；`command_id` 是 gateway 为设备启动指令生成的 UUID，重试保持不变；`port_id` 是 gateway 的数字设备端口 ID，成功确认时必填，不能传印刷二维码字符串。接口继续使用 `X-Service-Token`。

- 先锁支付单、再锁充电订单；核对付款关联、设备和端口，首次确认要求两者均为 `paid`。
- `success=true`：按真实数字端口 ID 写入 `active_port_charge` 并将订单设为 `charging`，全部同事务。其他未结束订单已占用该端口时返回 HTTP 409，绝不覆盖；gateway 必须处理冲突并确认设备停止，不能把 HTTP 冲突当作启动成功。
- `success=false`：同事务记录失败、创建尚未被其他退款记录覆盖的待退金额及 refund_required outbox。创建退款记录不代表已经完成退款。
- `charge_start_receipt` 持久化指令、结果和端口 ID。完全相同的确认幂等返回，即使订单之后已结束；相反结果或不同指令返回 HTTP 409。首次成功时间不被重放刷新。
- 提交成功后按印刷端口码 CAS 释放本订单的逻辑锁，不能删除其他订单的新锁。成功响应为 `{code:0,data:{ok:true}}`；无效参数 HTTP 400、不存在 HTTP 404、冲突 HTTP 409、基础设施失败 HTTP 5xx。gateway 得到持久化确认前不得 ACK 原启动 Stream。

### `GET /api/v1/internal/payment-orders/{payment_order_id}`

返回 `{payment_order_id,order_id,status,paid_fee_cents,wechat_transaction_id}`;无记录返回 `1004`。仅供 billing 核对退款金额和支付状态。

### 发票审核内部接口

`GET /api/v1/internal/invoices/{invoice_id}` 返回 user 所有的发票申请资料与当前状态；invoice_id 可为数据库 ID 或业务发票号。

`POST /api/v1/internal/invoices/{invoice_id}` 请求 `{decision,actor_id,invoice_url?,reason?}`，仅接受 admin Service Token。`approve` 只在 admin 已收集两名不同财务审核人后调用，要求 HTTPS invoice_url 并将申请标记为 `issued`；`reject` 需要最多 255 字的原因并将申请标记为 `rejected`。user 锁定申请并只允许 pending 状态迁移；相同操作人、决定及结果幂等，已审核申请不能改签。admin 负责校验两名审核人及实时财务角色权限并保存审核审计。

### `POST /api/v1/internal/refund-records/claim`

请求体:`{event_key,payment_order_id,refund_amount_cents,reason}`;事务内锁定对应 `payment_order` 行,跨全部月分区查询 `refund_record.trigger_event_id=event_key`,已有则返回原记录,否则创建退款记录并由 `event_key` 派生稳定的微信商户退款单号。响应 `{refund_id,status,refund_no}`。同一支付单的并发领取由行锁串行化;admin 重试必须沿用原 `refund_no`,不得发起一笔新退款。

### `POST /api/v1/internal/refund-records/{refund_id}/result`

请求体:`{wechat_refund_id,status,completed_at?,failure_reason?}`;user 按合法状态迁移更新本 schema 的 `refund_record`,重复结果幂等返回。admin 不直写 `user_db`。

---

## 文档维护

- 修改本文件需在 PR 标题写 `api(user): <简短描述>`,并在 PR 描述中说明影响哪些端点
- 任何新增 / 删除 / 修改端点必须同步更新 `services/user/src/openapi.rs` 与本文件
- CI 检查:OpenAPI 规范与本文件端点清单必须一致(脚本 `tools/check-api-consistency.ts`)

### 当前扫码读取实现（2026-09-26）

`/user/scan/resolve` 当前返回 `data.kind=port|device`；port 分支在 data 中直接包含 `port_id/device_id/port_no/port_code/status`，device 分支包含 `device_id/status/ports`。每个端口的 `port_id` 为印刷端口码字符串（不是数据库数字 ID），可原样传入 `/user/scan/port`，其 data 为上述端口对象。请求均只读，未创建订单或预占端口。无效码返回 400，不存在或已停用设备返回 404。设计中的站点信息、价格、在线状态和锁状态尚未合入这两个响应。

### 当前用户会话契约（2026-09-26）

登录 data 在 token/user_id/openid/is_new_user 基础上新增 refresh_token 与 jwt_expires_in。刷新请求使用 POST /api/v1/public/auth/refresh，Authorization: Bearer <refresh_token>，无请求体；返回 data.token、data.refresh_token、data.jwt_expires_in，旧刷新令牌立刻失效。POST /api/v1/public/auth/logout 使用同样的刷新令牌头撤销当前刷新令牌，返回 data.logged_out=true。访问 JWT 不可用于刷新。当前退出撤销该登录会话的全部访问 JWT；即使使用已轮换的旧刷新令牌退出，也会撤销该会话。用户访问 JWT 固定 900 秒并带 sid，所有用户路由实时检查会话和账户状态。无 sid 的旧访问令牌需要重新登录。

### 当前钱包读取契约（2026-09-26）

GET /user/wallet/balance 的 data 包含 available_cents、frozen_cents、status，并保留 balance_cents=available_cents。GET /user/wallet/txns 支持 page/page_size/type，返回 page/page_size/total/items；每条包含 txn_no、txn_type、direction、带正负号的 amount_cents、balance_after_cents、remark、created_at。page 从 1 起，page_size 为 1..100。类型可选 recharge/consume/refund/freeze/unfreeze/admin_adjust/gift；不接受客户端指定 user_id。当前数据库 wallet_txn 无软删除字段，流水查询保留全部本人资金历史。

### 充电状态读取当前实现补充

`GET /api/v1/user/charge/ongoing/snapshot` 的 `order_id` 当前接受数字 ID 或订单号。无论是否命中缓存，先校验订单属于当前用户且未软删除；不可访问订单统一返回 404。响应包含数字 `order_id`、`order_no`、数据库原始 `status/charge_state`、`poll_continue`、`next_poll_after_ms`。pending_payment/paid/charging 继续轮询，终态停止；paid 不映射为 charging。缺失遥测返回 null，`telemetry_available` 表示是否有缓存测量，不代表已接通完整遥测和告警链路。缓存白名单之外的字段不返回。停止请求仅允许当前用户未删除且 charging 的订单；其他状态 409，未授权订单 404。

### 扫码报价当前实现

新增 `POST /api/v1/user/scan/quote`（用户会话鉴权）：请求 `{port_id,estimated_kwh,estimated_minutes}`，不接受客户端 user_id。明确预计电量/时长，返回 billing 的站点规则、分项金额与报价有效期，不创建订单或预占端口。`POST /scan/start` 当前也必须携带 `estimated_kwh`（字符串）和 `estimated_minutes`（整数），不再默认推算 240 分钟。用户确认金额、防漂移及规则快照持久化仍待完成，前端目前只提供报价预览。

### 报价确认与下单保护

`POST /user/scan/quote` 额外返回 UUID `quote_id`，服务端在 Redis 保存最多五分钟的报价上下文。`POST /user/scan/start` 当前需 `{quote_id,port_id,estimated_kwh,estimated_minutes}`（可选 coupon_grant_id 尚不支持抵扣）。报价必须属于当前用户和端口，预计参数一致，且规则、版本及所有费用分项经重新计算后未变。缺 quote_id 为 400；报价过期、参数/价格变化或已用于订单为 409；他人或不同端口的报价为 404。确认快照与订单、支付单在同一事务中持久化，同一 quote_id 只能创建一次订单；前端支付确认与已创建订单的预支付恢复仍待接通。


### 微信支付回调实现约定

`POST /api/v1/public/payment/wechat/callback` 使用微信原始通知报文和 Wechatpay-* 签名头，配置的平台公钥验签通过后才解密 resource。仅接受本商户/appid 的 JSAPI 成功付款，并核对支付单金额、支付方式和付款用户。成功返回 **HTTP 204、空 body**（不使用业务 ApiEnvelope）；签名无效为 401，内容或订单冲突为 400/409，基础设施失败为 5xx，均不确认通知。

幂等重放不重复入账或启动。取消/过期等无法启动的充电订单收到付款时创建待退款记录；普通钱包充值增加余额并记录流水。支付、订单或钱包、幂等记录及事件在同一事务内提交；事件由 user 自有 outbox 发布器重试投递。退款记录创建不表示退款已经完成，充电付款确认不表示设备已经启动。


### 主动停止与结束确认实现（2026-09-26）

`POST /api/v1/user/charge/stop` 校验当前用户的订单，再请求 gateway。当前 data 为 `{accepted,stopped,command_id}`，首次仅 accepted=true；小程序继续轮询等待设备确认，不能把 HTTP 成功当作已断电。已完成的同一主动停止可重放取得原结果。

内部 `POST /api/v1/internal/charge-orders/{order_no}/end-result`（X-Service-Token）请求 `{order_no,start_command_id,stop_command_id,device_id,port_no,port_id,meter:{charged_wh,charged_seconds,ended_at}}`。校验已持久化的成功启动指令、真实端口、充电状态和时间范围；最终读数、completed 状态、活动端口结束时间、回执及时间线同事务保存。相同结果幂等，读数变化或身份冲突 HTTP 409。晚到重放不能释放已经被下一订单占用的端口。

结束时收费字段保持原值/未知，不以预计金额或零代替正式结算。poll_continue=false 表示设备结束确认已完成，不代表计费/退款完成。gateway 在收到 `{ok:true}` 后发布 charge_ended；user 消费后只失效遥测缓存，不从通知伪造最终状态。
# 实测计费内部接口（2026-09-26 实现）

- `POST /api/v1/user/wallet/refund` 当前请求要求 `request_id`（客户端每次新申请生成 UUID，网络重试沿用）、`amount_cents`、可选 `reason`。返回 request_id/status/refund_cents/refund_orders，已受理时另含 txn_no；status 为 accepted 或 manual_review。同请求标识变更金额/原因会拒绝。原支付单拆分、余额预留及事件写入同事务提交，异步确认实际到账，不返回“实时到账”。五分钟内第三个有效申请进入风控审核。
- `GET /api/v1/user/wallet/refunds` 使用用户 JWT，支持 page（默认 1，上限 100000）及 page_size（默认 20，上限 50）；仅返回本人记录。响应包含 user_id、items、total、page、page_size。每项包含 request_id、amount_cents、refunded_cents、status、reason、created_at 和 refund_orders；拆单包含 refund_no、refund_cents、status、failure_reason、completed_at。申请状态为 pending、processing、success、manual_review 或 needs_review；refunded_cents 只累计成功拆单，不把已受理视为已到账。
- `GET /api/v1/internal/refund-records` 使用服务间认证，为 admin 财务查询提供退款记录。支持 page/page_size（最多 100）、status、refund_no 精确筛选，过滤软删除，按创建时间与 ID 倒序分页；返回 items/total/page/page_size。记录包含退款单号、用户和支付 ID、业务类型、金额、状态、原因、失败原因及创建/完成时间。授权管理员范围由 admin 数据库实时权限校验执行。
- `POST /api/v1/internal/refund-records/{refund_id}/approve` 使用服务间认证；refund_id 当前为完整退款单号，请求 actor_id/comment，由 admin 校验财务身份后发送。user 将两次不同账号的意见绑定到同一退款快照；第一签返回 awaiting_second，第二签返回 approved 并与退款执行出站事件同事务保存。重复签署不重复产生事件，金额/原支付单/原因等变更会拒绝后续审核及执行。仅支持尚未被执行的终态充电订单 pending 退款；钱包风控申请另行处理。
- `POST /api/v1/internal/refund-records/{refund_id}/reject` 使用服务间认证，请求 actor_id/comment。只允许已有第一签且尚未执行的待第二签充电退款，拒绝回执、退款 rejected 状态与订单时间线同事务保存。退款查询支持 rejected 筛选，审核拒绝与微信执行失败 failed 分开表达；资金预留统计不包含 rejected，仍包含结果尚未核实的 failed。
- `POST /api/v1/internal/orders/{order_id}/refunds` 使用服务间认证；请求 actor_id 与 request（request_id、amount_cents、reason），admin 负责实时校验财务身份。manual_refund_request 的 UUID 全局唯一，将申请身份与内容绑定；支付单锁内创建退款并复用双签模块记录第一签，验证失败回滚全部写入。重复请求返回同一退款单，不产生重复审核或支付事件。

- `POST /api/v1/user/charge/:order_id/prepay`：用户 JWT 鉴权，当前 `order_id` 为充电订单号。只恢复本人未付款且原端口预留仍有效的订单。返回 order_no/payment_order_no/amount_cents/hold_expires_at/payment_params；不创建新支付单、不延长付款期限。已保存 prepay_id 时只重新生成调起支付签名，尚未取得 prepay_id 时使用同一原始请求和支付单号重试微信预下单。

- `POST /api/v1/public/refund/wechat/callback`：微信退款公开通知入口，原文 RSA 验签后使用 APIv3 密钥解密。绑定商户、支付单、微信交易号、退款单、微信退款 ID 与金额，成功提交后返回 HTTP 204。验签失败 401，非法通知/状态冲突非 2xx；回调和查询共享幂等入账，迟到异常通知不撤销已确认成功。`WECHAT_REFUND_NOTIFY_URL` 可作为退款申请参数传入；未设置时使用微信商户平台配置的退款通知地址。Caddy 已代理 `/api/v1/public/*`。

- `POST /api/v1/internal/refund-records/execution`：需服务密钥，请求 `refund_no`。核对自动充电退款条件后幂等领取，返回 refund_no/status/transaction_id/refund_cents/total_cents/wechat_refund_id；不执行外部退款。admin 将请求快照持久化后调用微信，最终仍由退款结果接口入账。钱包及人工处理中的退款不会被此接口接管。

- `GET /api/v1/internal/charge-orders/:order_id/metered`：`order_id` 为数字订单 ID，要求服务密钥；仅返回已完成且具备计量和下单规则快照的订单。返回 `charge_order_id/order_no/user_id/started_at/meter/quote`，供 billing 正式核算。
- `POST /api/v1/internal/charge-orders/:order_id/fee-result`：要求服务密钥。请求为 `calculation_no/source/electric_cents/service_cents/total_cents`，source 为上述完整快照；成功 `data.ok=true`。校验所属用户、支付关联、计量和规则快照及分项合计，原子写入费用、幂等凭据、差额退款和退款 outbox。完全一致的重放成功，冲突重放拒绝。只登记待退款，不伪造退款到账。

### 钱包退款风控内部接口

`GET /api/v1/internal/wallet-risks` 和 `POST /api/v1/internal/wallet-risks/{request_id}/review` 仅接受服务令牌。查询参数 page/page_size；审核请求 actor_id、approved、comment，由 admin 完成实时角色及权限校验。审核回执保存在 wallet_risk_review，成功审核复用原退款请求，不创建新的用户申请。用户 `GET /api/v1/user/wallet/refunds` 条目增加 review（approved、comment、actor_id）；拒绝申请 status 为 rejected。

### 优惠券模板与运营发券内部接口

以下路径只接受 `X-Service-Token`，由 admin 通过内部 HTTP 调用；优惠券模板和发放记录均由 user_db 所有：

- `GET /api/v1/internal/coupons`：返回模板 `items`，包含 ID、编码、名称、折扣类型/配置、门槛、有效小时、总量/个人额度、状态和有效时间。
- `POST /api/v1/internal/coupons`：创建模板。字段为 `code/name/discount_type/discount_value_cents/discount_percent/min_charge_cents/valid_hours/total_quota/per_user_quota/start_at/end_at`。`amount`、`percentage`、`time_free` 的金额字段互斥校验由 user 服务执行。
- `GET|PUT|DELETE /api/v1/internal/coupons/{id}`：读取、更新名称/状态/结束时间、软删除。存在发放记录的模板不可删除，应改为停用。
- `POST /api/v1/internal/coupons/{id}/grants`：请求 `{ "request_id":"<UUID>", "user_id":123 }`。事务内锁模板、核对用户仍有效、模板状态与生效时间、总发放量及个人额度后新增一张 `coupon_grant`。相同 UUID 和相同参数重放返回原券；UUID 被改用于其他用户或模板时返回冲突。响应含请求 ID、模板 ID、发放记录 ID、用户 ID、状态和到期时间。
- `GET /api/v1/internal/coupons/stats?coupon_id=123`：返回总额度、已发、可用、已用、已过期数量和核销率；未清理但已过期的未使用记录计入已过期。

活动事件 `coupon_grant_required_stream` 使用 `event_id` 作为同一事务的幂等键；无效 payload、模板读取失败或额度耗尽都会返回错误供消费框架重试/DLQ，不再吞掉错误或按默认 30 天发券。扫码支付优惠券抵扣和核销仍未接通，`POST /api/v1/user/coupon/preview` 只预览、不消耗优惠券。
