# billing 服务 API 详细设计

**服务**:`billing`(`services/billing`)
**对外地址**(内部 HTTP):`:8084`(**仅内网可达**,Docker Compose 内服务间调用)
**鉴权**(内部 HTTP):服务间共享密钥(`Authorization: Bearer <service_token>`)
**主入口**:**异步事件**(消费 `charge_ended_stream`),**不直接面向终端用户或 PC 后台操作员**

> **本文件覆盖范围**:billing 服务内部 HTTP 端点(供 admin / user / gateway 内部调用)+ **Stream 消费约定** + **计费 / 分账引擎契约**。billing 服务**只对自己的 schema(billing_db)有读写权限**(§ 4.2)。

---

## 通用约定

### 后台订单费用与分账快照

`GET /api/v1/internal/orders/{order_id}/billing-summary`：order_id 为 user 充电订单的数值 ID。当前运行环境使用 `X-Service-Token` 内部鉴权。

在同一数据库事务中读取该订单最新计费记录，以及关联的分账单和参与方金额。响应遵循统一包装，data 包含 `calculation_no`、`electric_cents`、`service_cents`、`total_cents` 和 `settlements[]`。每个分账单包含 `settlement_id`、`settlement_no`、`mode`、`status`、`total_cents`、`split_pool_cents`、`parties[]`；参与方包含 ID、编码、名称、基点比例、分金额及状态。

未计费返回四个 null 字段及空 settlements。订单存在性由调用方先查询 user 确认。数据库/解码错误不得转为空数据。该接口读取持久化结果，不触发计费或分账。

### 请求 / 响应格式

- 内部 HTTP 全部 JSON
- 时间戳 ISO 8601(UTC,`Z` 后缀,毫秒精度)
- 金额字段 `*_cents` 后缀,单位分
- 业务 ID `*_id` / `*_no`

### 统一响应包装

```json
{
  "code": 0,
  "message": "ok",
  "data": { ... },
  "request_id": "..."
}
```

### 限流

> billing 服务对内调用,**不限流**(信任内部调用方);全局限流规则见 `docs/技术规格.md` § 7.4。

- **不限流**(信任内部调用方)
- 单实例计算能力约 **100 req/s**(纯计算服务,无 IO 瓶颈)

### 错误码

> 完整错误码字典见 `services/common-error/errors.toml` + `docs/技术规格.md` § 7.2(权威源)。
> 段位固定:`0`=成功 / `1xxx`=通用 / `2xxx`=业务(`billing` 服务专属子段 2201-2299)/ `5xxx`=服务器。本文仅列该服务用到的子集。billing **不**使用 `3xxx`(无第三方通道)与 `4xxx`(内网不限流)。

| 段位 | 含义 | 示例 |
| --- | --- | --- |
| 1xxx | 通用错误 | 1001 鉴权失败 / 1004 资源不存在 / 1005 参数校验失败 |
| 2xxx | 业务错误 | 2001 计费规则不存在 / 2002 订单未计费 / 2003 分账模板比例之和不等于 10000 / 2004 提现申请已审核 |
| 5xxx | 服务器错误 | 5001 内部错误 / 5003 下游服务暂时不可用 |

### 幂等保证(关键)

- billing 服务的写入操作**全部幂等**:基于 `event_id`(Redis Stream 事件 ID)/ `order_id` 去重
- 重复消费同一 `event_id` → 直接返回历史结果,不重复计算 / 写库
- 实现:`fee_calculation.charge_order_id` UNIQUE;`settlement` + `settlement_party_amount` 也基于 `charge_order_id` 唯一

---

## 一、HTTP 端点清单(共 9 个)

> 所有路径在 `:8084`;**仅内网可达**。

### A. 计费 / 报价类(3 个)

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/internal/quote` | 预扣费预估(用户扫码后调,§ 2.4 已定路径) |
| POST | `/api/v1/internal/calculate` | 计费计算(消费 `charge_ended_stream` 后调,内部触发) |
| GET | `/api/v1/internal/orders/{order_id}/fee-breakdown` | 订单计费明细(用户查账单 / admin 查) |

### B. 分账类(3 个)

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/internal/split` | 分账计算(消费 `charge_ended_stream` 后调,内部触发) |
| GET | `/api/v1/internal/orders/{order_id}/split` | 订单分账明细(admin PC 后台查) |
| GET | `/api/v1/internal/settlements/{settled_id}` | 分账单详情(admin PC 后台查) |

### C. 提现类(3 个)— `withdraw_request`

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/internal/withdraw-requests` | 提现申请列表(分页 + 状态筛选) |
| POST | `/api/v1/internal/withdraw-requests` | 创建提现申请(分账参与方发起) |
| POST | `/api/v1/internal/withdraw-requests/{withdraw_id}/approve` | 客户财务审核通过 |

---

## 二、计费 / 报价类(关键端点展开)

### `POST /api/v1/internal/quote`

**鉴权**:服务间共享密钥
**触发场景**:user 服务在用户扫码进入充电启动页时调用,**预估本次充电可能费用**(显示给用户确认)
**业务目标**:根据预计电量 + 计费规则 → 计算预估费用 + 优惠券折扣 + 余额抵扣

**请求体**:
```json
{
  "device_id": "xx_001_abc",
  "port_id": "xx_001_01",
  "user_id": 8888,
  "estimated_kwh": "0.500",                    // 按端口功率 × 预计时长粗估
  "estimated_duration_sec": 3600,
  "coupon_grant_id": null,                     // 用户选用的优惠券(可选)
  "use_wallet_balance": true                   // 是否用钱包余额抵扣
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "quote_id": "Q20260925140000123",
    "estimated_total_fee_cents": 65,           // 预估总费用(分)
    "electric_fee_cents": 29,
    "service_fee_cents": 26,
    "estimated_coupon_discount_cents": 10,     // 优惠券抵扣
    "estimated_wallet_deduct_cents": 0,        // 钱包余额抵扣(若余额够)
    "estimated_wechat_pay_cents": 55,          // 实际微信支付
    "billing_mode": "time_of_use",             // 实际计费模式
    "pricing_rule_id": 5,
    "quote_expires_at": "2026-09-25T14:05:00Z" // 报价 5 min 内有效(超时重算)
  }
}
```

**业务逻辑**:
1. 校验 `device_id` 在线 → 离线返回 `2005`
2. **查计费规则**:`admin_db.pricing_rule WHERE station_id = (查 device_meta.station_id)`(通过 HTTP 调 admin)
3. 校验 `coupon_grant_id` 有效(通过 HTTP 调 user)
4. **计费计算**(纯本地计算,无 IO):
   - `estimated_kwh × time_of_use` → 分时段累加电费
   - `estimated_kwh × service_price` → 服务费
   - 应用优惠券折扣(若 `coupon_grant_id` 有效)
   - 应用余额抵扣(若 `use_wallet_balance=true` 且余额够)
5. 返回报价 + 5 min 过期时间
6. **注意**:`quote` **不写库**,不持久化**(仅当用户实际启动充电 → `charge_ended_stream` 后才正式计费入库)

**错误码**:
- `2001`: 计费规则不存在(站点未配)
- `1005`: `estimated_kwh` 不合法

---

### `POST /api/v1/internal/calculate`

**鉴权**:服务间共享密钥
**触发场景**:billing 消费 `charge_ended_stream` 后,**正式计算本次充电费用**(写 `fee_calculation`)
**业务目标**:按实际电量 / 时长 + 当时计费规则快照 → 写入计费快照

**请求体**:
```json
{
  "event_id": "charge_ended_stream_event_xxx",  // 幂等 key
  "charge_order_id": 12345,
  "user_id": 8888,
  "device_id": "xx_001_abc",
  "station_id": 12,
  "started_at": "2026-09-25T14:00:00Z",
  "ended_at": "2026-09-25T15:00:00Z",
  "duration_seconds": 3600,
  "meter_kwh": "0.520",                         // 实走表电量(§ 6.5 A 方案)
  "estimated_kwh": null,                        // § 6.5 B 方案填
  "is_estimated": false,                        // true = 估算订单
  "timestamp_drift_ms": 0,                      // 设备时钟偏差
  "pricing_rule_id": 5
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "fee_calculation_id": 98765,
    "charge_order_id": 12345,
    "electric_fee_cents": 29,
    "service_fee_cents": 26,
    "total_fee_cents": 55,
    "calculation_detail": {                     // 计算过程(便于审计)
      "billing_mode": "time_of_use",
      "tiers": [
        { "period": "peak", "kwh": "0.200", "price_cents_per_kwh": 110, "subtotal_cents": 22 },
        { "period": "off_peak", "kwh": "0.320", "price_cents_per_kwh": 55, "subtotal_cents": 18 }
      ],
      "service_fee": { "kwh": "0.520", "price_cents_per_kwh": 50, "subtotal_cents": 26 }
    },
    "pricing_rule_snapshot": {                  // 计费规则快照(冗余)
      "rule_id": 5,
      "rule_name": "万达广场白天时段",
      "snapshot_at": "2026-09-25T14:00:00Z"
    },
    "is_duplicate": false                       // true = 幂等命中,本次无副作用
  }
}
```

**业务逻辑**:
1. **幂等检查**:`SELECT fee_calculation WHERE charge_order_id=?` → 已存在 → 直接返回(`is_duplicate=true`)
2. 校验 `event_id` 与已处理 event_id 比对(防 reorder)
3. 查 `admin_db.pricing_rule` 通过 HTTP 调 admin(取完整规则)→ **快照化**(防止规则改版影响历史)
4. **计费计算**:
   - `time_of_use` 模式 → 按 `started_at` / `ended_at` 拆时段累加
   - `by_kwh` 模式 → `meter_kwh × price`
   - `by_time` 模式 → `duration_seconds × price_per_sec`
   - `tiered` 模式 → 按功率分档累加
5. **事务**:
   - INSERT `fee_calculation(charge_order_id, ...)` + 写 `pricing_rule_snapshot`
   - 发 `comp_tx_stream` 事件(消费者 user 更新 `charge_order.fee_status`)
6. **金额守门**:校验 `total_fee_cents == electric_fee_cents + service_fee_cents`(不等则拒绝写入 + 告警)

**错误码**:
- `2001`: 计费规则不存在(快照失败)
- `1005`: 金额守门失败(数据异常)
- `5001`: 内部错误

---

### `GET /api/v1/internal/orders/{order_id}/fee-breakdown`

**鉴权**:服务间共享密钥。billing 只读本 schema 的 `fee_calculation` 及价费快照;响应 `data` 包含 `order_id`、`settlement_status`、`electric_fee_cents`、`service_fee_cents`、`total_fee_cents` 和明细数组。尚未完成计费时返回 `settlement_status='pending'` 与空明细,调用方不得将预估价显示为实结价。`order_id` 无效返回 `1005`,下游暂不可用返回 `5003`。

---

## 三、分账类(关键端点展开)

### `POST /api/v1/internal/split`

**鉴权**:服务间共享密钥
**触发场景**:billing 在 `calculate` 完成后,**按分账模板计算每个参与方的金额**
**业务目标**:按 `split_template` + 价费分离原则 → 写入 `settlement` + `settlement_party_amount`

**请求体**:
```json
{
  "event_id": "split_trigger_xxx",
  "charge_order_id": 12345,
  "fee_calculation_id": 98765,
  "station_id": 12,
  "split_template_id": 3,
  "total_fee_cents": 55,
  "electric_fee_cents": 29,
  "service_fee_cents": 26
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "settlement_id": 55555,
    "charge_order_id": 12345,
    "split_mode": "mode_a",                      // "mode_a" 全分账 / "mode_b" 仅服务费分账
    "parties": [
      {
        "party_type": "operator",
        "party_name": "本公司",
        "ratio_bp": 4000,
        "amount_cents": 22                        // 40.00% × 55
      },
      {
        "party_type": "property",
        "party_name": "万达物业",
        "ratio_bp": 3500,
        "amount_cents": 19
      },
      {
        "party_type": "franchisee",
        "party_name": "李老板加盟",
        "ratio_bp": 2500,
        "amount_cents": 14
      }
    ],
    "is_duplicate": false
  }
}
```

**业务逻辑**:
1. **幂等检查**:`SELECT settlement WHERE charge_order_id=?` → 已存在 → 直接返回
2. 查 `admin_db.split_template WHERE id=?` 通过 HTTP 调 admin(取模板 + 参与方比例)
3. **校验比例和**:`SUM(ratio_bp) == 10000` → 不等返回 `2003`
4. **分账模式**:
   - **mode_a**(全分账):`total_fee_cents` 按 `ratio_bp` 分 → 每个参与方金额
   - **mode_b**(仅服务费分账):`electric_fee_cents` 全归运营商,`service_fee_cents` 按 `ratio_bp` 分
5. **金额守门**:`SUM(amount_cents) == total_fee_cents`(精确分到分,无舍入误差;若有尾差 → 加到 `operator` 兜底)
6. **事务**:
   - INSERT `settlement(charge_order_id, split_template_id, total_fee_cents, split_mode, calculated_at=NOW())`
   - INSERT `settlement_party_amount(settlement_id, party_type, party_name, ratio_bp, amount_cents) × N`
7. 发 `comp_tx_stream` 事件(用户端 / 财务端可用)

**错误码**:
- `2003`: 分账模板比例之和不等于 10000
- `1005`: 金额守门失败(数据异常)

### `GET /api/v1/internal/settlements/{settled_id}`

**鉴权**:服务间共享密钥

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "settlement_id": 55555,
    "charge_order_id": 12345,
    "charge_order_no": "CH20260925140000123",
    "split_mode": "mode_a",
    "total_fee_cents": 55,
    "calculated_at": "2026-09-25T15:00:10Z",
    "parties": [
      { "party_type": "operator", "party_name": "本公司", "ratio_bp": 4000, "amount_cents": 22 },
      { "party_type": "property", "party_name": "万达物业", "ratio_bp": 3500, "amount_cents": 19 },
      { "party_type": "franchisee", "party_name": "李老板加盟", "ratio_bp": 2500, "amount_cents": 14 }
    ]
  }
}
```

**业务逻辑**:JOIN `settlement` + `settlement_party_amount` + 调 gateway 拿订单号(`gateway_db.charge_order` 也在 gateway 侧,跨服务查)。

---

## 四、提现类

### `POST /api/v1/internal/withdraw-requests`

**鉴权**:服务间共享密钥
**触发场景**:分账参与方(物业 / 加盟商)在 PC 后台"我的分账"页提交提现申请(实际是 admin 服务代发起,调本端点)
**业务目标**:把已结算的分账金额 → 申请提现到指定账户

**请求体**:
```json
{
  "party_type": "property",
  "party_name": "万达物业",
  "amount_cents": 19000,                       // 申请提现金额(分)
  "settlement_id_range": [55550, 55551, 55552],  // 涉及哪些分账单
  "settlement_account": "6222021234567890",     // 收款账号
  "account_holder": "万达物业有限公司"
}
```

**响应(201)**:
```json
{
  "code": 0,
  "data": {
    "withdraw_id": 777,
    "status": "pending",
    "requested_at": "2026-09-25T16:00:00Z"
  }
}
```

**业务逻辑**:
1. 校验 `amount_cents` ≤ 未提现的分账总额(查 `settlement_party_amount` 已提现 vs 未提现)
2. 校验 `settlement_id_range` 中每笔都是该 `party_type` / `party_name` 持有
3. INSERT `withdraw_request(status='pending')` + 写审计
4. 发 `comp_tx_stream` 通知 admin 服务

### `POST /api/v1/internal/withdraw-requests/{withdraw_id}/approve`

**鉴权**:服务间共享密钥 + **双签**(客户财务 2 个不同账号,沿用退款双签机制)

**请求体**:
```json
{
  "approve_comment": "同意转账",
  "second_signature": "sig_xxx"
}
```

**业务逻辑**:
1. 校验 `withdraw_id` 存在 + `status='pending'` → 否则返回 `2004`
2. 双签校验(同退款审核)
3. UPDATE `withdraw_request(status='approved', reviewed_by=..., reviewed_at=NOW())`
4. **实际打款**:本期不接入真实银行 API,**只标记状态**(客户线下打款后人工确认);二期接银行接口

---

## 六、Stream 消费约定(billing 作为消费者)

billing 服务**主动消费**以下 Stream(沿用 § 5.1):

| Stream | 来源 | 处理流程 | 产出 |
| --- | --- | --- | --- |
| `charge_ended_stream` | gateway | 1. 调 `POST /calculate` → 写 `fee_calculation`<br>2. 调 `POST /split` → 写 `settlement` + `settlement_party_amount`<br>3. 若需退款 → 发 `refund_required_stream`<br>4. 若需发票 → 发 `invoice_required_stream` | `fee_calculation` / `settlement` 落库 + 发 `refund_required_stream` / `invoice_required_stream` |
| `comp_tx_stream` | 各服务 | 对 `type='charge_refund_requested'` 的启动失败 / 取消后支付事件,按 `event_key` 幂等确认支付金额(经 user 内部接口读取),发布 `refund_required_stream`;不得直写 `user_db` | 退款事件 |

> **幂等保证**:消费方按 payload 的稳定 `event_key` 去重;只有 `refund_required_stream` 发布得到确认后才 ACK 上游 `comp_tx_stream`,失败保持 pending 并重试。退款事件使用同一 `event_key`,admin / user 消费端据此防重复退款。billing 不直写 `user_db` 或 `worker_db`。

---

## 七、计费 / 分账引擎契约

### 计费引擎(`engine/tariff.rs`)

**输入**:
```rust
pub struct TariffInput {
    pub billing_mode: BillingMode,
    pub meter_kwh: Decimal,
    pub duration_seconds: u32,
    pub started_at: DateTime<Utc>,
    pub ended_at: DateTime<Utc>,
    pub pricing_rule_snapshot: PricingRuleSnapshot,   // 冻结的规则
    pub timestamp_drift_ms: i32,
}
```

**输出**:
```rust
pub struct TariffOutput {
    pub electric_fee_cents: i64,
    pub service_fee_cents: i64,
    pub total_fee_cents: i64,
    pub calculation_detail: serde_json::Value,
}
```

**实现要点**:

- `time_of_use` 模式:按 `started_at` / `ended_at` 跨越时段边界,**每段独立计算**后累加
- `tiered` 模式:按累计 `meter_kwh` 落入对应功率档位 → 每档独立计费
- `by_kwh` 模式:统一价 × kwh
- `by_time` 模式:统一价 × 时长(秒)

### 分账引擎(`split/`)

**输入**:
```rust
pub struct SplitInput {
    pub split_mode: SplitMode,    // "mode_a" / "mode_b"
    pub total_fee_cents: i64,
    pub electric_fee_cents: i64,
    pub service_fee_cents: i64,
    pub parties: Vec<SplitParty>,  // 从 split_template 取
}

pub struct SplitParty {
    pub party_type: PartyType,
    pub party_name: String,
    pub ratio_bp: u32,             // 1-10000,基点
    pub settlement_account: Option<String>,
}
```

**输出**:
```rust
pub struct SplitOutput {
    pub parties: Vec<PartyAmount>,
    pub leftover_cents: i64,       // 尾差(应 = 0)
}

pub struct PartyAmount {
    pub party_type: PartyType,
    pub party_name: String,
    pub amount_cents: i64,
}
```

**实现要点**:

- 金额按比例分到分(`Decimal` 运算,避免浮点)
- **尾差处理**:`SUM(amount_cents)` 与 `total_fee_cents` 的差额加到 `operator` 兜底
- `mode_b` 时,`electric_fee_cents` 全归 `operator`,仅 `service_fee_cents` 分账

---

## 八、数据保留与归档(§ 4.6)

- `fee_calculation` / `settlement` / `settlement_party_amount`:保留 ≥ 3 年(合规底线,§ 13.3)
- `fee_calculation` 按月分区,滚动创建下月分区
- 超 3 年:`worker_db.data_retention` 任务(`§ 3.5`)物理归档至冷存储(OSS / 磁带)
- `pricing_tier_snapshot` / `withdraw_request`:不分表,跟随主表归档

---

## 文档维护

- 修改本文件需在 PR 标题写 `api(billing): <简短描述>`
- 新增 HTTP 端点必须同步更新 `services/billing/src/openapi.rs`
- **Stream 名必须从 § 5.1 8 个真实 Stream 中选**,新增 Stream 必须先在技术规格登记
- **计费 / 分账引擎的输入输出结构变更**(影响 `fee_calculation.calculation_detail` JSON 格式)→ 必须同步更新本文档 § 七 + `docs/db/billing.md` 表结构 + 写数据库 migration 兼容老数据
- CI 检查:OpenAPI 规范与本文件端点清单一致(脚本 `tools/check-api-consistency.ts`)
