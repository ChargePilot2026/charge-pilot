# user_db 数据库表设计

**所属服务**:user(对外 :8081,小程序侧 API)
**Schema 名**:`user_db`
**字符集 / 排序规则**:`utf8mb4` / `utf8mb4_unicode_ci`
**引擎**:InnoDB(全表)
**数据库版本**:MySQL 8.4 LTS

## 通用约定

| 项目 | 约定 | 例外 |
| --- | --- | --- |
| 主键 | `BIGINT UNSIGNED AUTO_INCREMENT`,字段名 `id` | 无 |
| 业务唯一键 | UUID v4 或业务字符串(如 `order_no`),单独字段 | 无 |
| 时间戳 | `created_at` / `updated_at`,类型 `DATETIME(3)`(毫秒精度) | 无 |
| 软删除 | `deleted_at DATETIME(3) NULL`,**本期不启用**(物理删除 + 审计日志兜底) | 无 |
| 金额 | `BIGINT`(单位:**分**,避免浮点误差) | 无 |
| 加密字段 | `VARBINARY` + MySQL `AES_ENCRYPT`(§ 9.3);读取用 `AES_DECRYPT` | 仅敏感字段 |
| 状态字段 | `ENUM(...)` + 配套 comment 列出全部枚举值 | 仅业务状态字段 |
| JSON 字段 | `JSON` 类型,严格字段定义在文档而非 DB | 仅配置类元数据 |
| 主键索引 | 默认随主键创建 | 无 |
| 索引命名 | `pk_` / `uk_` / `idx_` / `fk_` 前缀 | 无 |
| 外键 | **不声明**(跨服务事务用最终一致性,§ 4.3 + § 5.4) | 无 |

## 表清单(11 张)

| 表名 | 业务说明 | 分表策略 | 估算行数(单客户 5 年) |
| --- | --- | --- | --- |
| `user` | 终端用户(openid + 可选手机号) | 不分 | ~50 万 |
| `payment_order` | 充电支付订单(核心) | 按月分区(§ 4.8) | ~2000 万 |
| `wallet_account` | 用户余额账户(1:1) | 不分 | ~50 万 |
| `wallet_txn` | 余额流水 | 按月分区(§ 4.8) | ~500 万 |
| `refund_record` | 退款记录 | 按月分区(隐含) | ~200 万 |
| `coupon` | 优惠券模板 | 不分 | ~1000 |
| `coupon_grant` | 优惠券发放记录(用户持有) | 不分 | ~500 万 |
| `membership_card` | 会员卡(本期预留,数据可能为空) | 不分 | ~10 万 |
| `invoice_request` | 发票申请记录 | 不分 | ~50 万 |
| `port_view` | 找桩缓存(冗余自 gateway_db,加速查询) | 不分 | ~5000 |
| `payment_callback_idempotent` | 微信支付回调幂等表 | 不分 | ~200 万 |

> **本文件首批设计 5 张核心表**:`user` / `payment_order` / `wallet_account` / `refund_record` / `coupon_grant`。
> 剩余 6 张(`wallet_txn` / `coupon` / `membership_card` / `invoice_request` / `port_view` / `payment_callback_idempotent`)在第二批设计。

---

## 表 1:`user_db.user`

**业务说明**:终端用户基础信息表。每条记录对应一个**微信用户**(以 `openid` 为唯一标识),手机号绑定为可选。

**关键业务规则**:

- 一个微信用户 = 一条记录(按 `openid` 唯一)
- 手机号绑定为可选,绑送余额或优惠券(需求文档 § 5.3.2)
- 注销流程:用户主动注销 → 标记 `status=banned` + 抹除 `phone_enc` / `unionid`(保留 `openid` 用于审计追溯,30 天后物理删除)
- 跨服务访问:仅 user 服务读写;admin 通过 HTTP 调 user 读取(§ 4.2)

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键,内部使用 |
| `openid` | `VARCHAR(64)` | UNIQUE, NOT NULL | — | 微信 openid(来自 `code2Session`) |
| `unionid` | `VARCHAR(64)` | UNIQUE NULL | NULL | 微信开放平台 unionid(多小程序 / 公众号打通时填) |
| `nickname` | `VARCHAR(64)` | NOT NULL | `''` | 微信昵称(脱敏后存储:emoji 转 `*` / 特殊字符过滤) |
| `avatar_url` | `VARCHAR(512)` | NULL | NULL | 微信头像 URL(下载到 OSS 后存 OSS 路径,避免微信 URL 失效) |
| `phone_enc` | `VARBINARY(255)` | NULL | NULL | 手机号 AES_ENCRYPT 密文(§ 9.3);未绑定时为 NULL |
| `phone_hash` | `CHAR(64)` | UNIQUE NULL | NULL | 手机号 SHA-256 哈希(用于"该手机号是否已注册"查询,避免解密) |
| `status` | `ENUM('active','banned')` | NOT NULL | `'active'` | 状态:active 正常 / banned 封禁(含主动注销) |
| `banned_reason` | `VARCHAR(128)` | NULL | NULL | 封禁原因(主动注销 / 投诉 / 风控) |
| `banned_at` | `DATETIME(3)` | NULL | NULL | 封禁时间 |
| `registered_at` | `DATETIME(3)` | NOT NULL | — | 首次登录时间 |
| `last_active_at` | `DATETIME(3)` | NOT NULL | — | 最近一次活跃时间(任一 API 调用都更新) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间(自动更新) |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_user` | `id` | 主键 | — |
| `uk_user_openid` | `openid` | 唯一 | 登录 / `code2Session` 后查表 |
| `uk_user_unionid` | `unionid` | 唯一(可空) | 跨小程序 unionid 打通 |
| `uk_user_phone_hash` | `phone_hash` | 唯一(可空) | "该手机号是否已注册"查询 |
| `idx_user_last_active` | `last_active_at` | 普通 | 找活跃用户 / 数据分析 |

### 约束

- `phone_enc` 与 `phone_hash` **至少一个为 NULL**(未绑定手机号时都为 NULL);绑定后必须两个都填
- `banned_at` NOT NULL 时,`status` 必须为 `banned`(应用层约束)

### 关系

- 一对多 → `payment_order.user_id`(一个用户可有多笔订单)
- 一对一 → `wallet_account.user_id`(每个用户一个余额账户)
- 一对多 → `coupon_grant.user_id`(用户持有的多张优惠券)

### 业务规则

- **首次登录流程**:`code2Session` → 拿 `openid` → `INSERT ... ON DUPLICATE KEY UPDATE last_active_at = NOW()`(幂等)
- **手机号绑定流程**:小程序 `getPhoneNumber` 拿明文 → 应用层 `AES_ENCRYPT` 写 `phone_enc` + `SHA256` 写 `phone_hash` → **触发绑送奖励**(发优惠券 / 余额,具体看 `coupon_grant` 与 `wallet_txn` 表)
- **注销流程**:标记 `status=banned` + `banned_at` + `banned_reason='user_request'` → 清空 `phone_enc` / `unionid` / `nickname` / `avatar_url`(保留 `openid` 用于 30 天审计追溯)

---

## 表 2:`user_db.payment_order`

**业务说明**:**最核心的业务表**,记录每一笔充电订单的生命周期。从扫码启动到结束结算的全链路状态都在这张表。

**关键业务规则**:

- 一笔订单 = 一次完整充电会话(用户扫码 → 启动 → 充电中 → 结束 / 取消 / 失败)
- **金额拆分**:电费 + 服务费 分离存储(需求文档 § 7.2 价费分离)
- **状态机**:`pending` → `charging` → `finished` / `cancelled` / `failed` →(必要时)`refunded`
- **估算订单**:§ 6.5 B 方案的离线补传兜底,订单带 `billing_mode='estimated'` 标记
- **唯一约束**:`(port_id, status='charging')` 部分唯一索引,防同一端口双订单(§ 5.5)
- **幂等**:`wechat_transaction_id` 唯一,防微信回调重复入账(§ 5.4 微信支付回调幂等)

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `order_no` | `CHAR(32)` | UNIQUE, NOT NULL | — | 业务订单号,格式 `YYMMDDHHmmss + 12 位随机`(用户侧展示) |
| `user_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `user.id` |
| `customer_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `admin_db.customer.id`(冗余,便于跨服务查询) |
| `device_id` | `VARCHAR(32)` | NOT NULL | — | 充电桩设备 ID(§ 6.2 格式约定) |
| `port_id` | `VARCHAR(32)` | NOT NULL | — | 端口 ID(同一 device 下的物理插槽) |
| `vendor_id` | `VARCHAR(8)` | NOT NULL | — | 厂商 ID(2-4 字母前缀) |
| `station_id` | `BIGINT UNSIGNED` | NULL | NULL | 站点 ID(冗余自 admin_db,便于统计) |
| `charge_rule_id` | `BIGINT UNSIGNED` | NOT NULL | — | 使用的计费规则 ID(对应 admin_db 计费规则模板) |
| `started_at` | `DATETIME(3)` | NOT NULL | — | 充电开始时间(用户视角) |
| `ended_at` | `DATETIME(3)` | NULL | NULL | 充电结束时间(可能为 NULL 表示进行中) |
| `duration_seconds` | `INT UNSIGNED` | NULL | NULL | 充电时长(秒),结束回填 |
| `meter_kwh` | `DECIMAL(10,3)` | NULL | NULL | 实走表电量(kWh,精度 0.001) |
| `power_w` | `DECIMAL(10,2)` | NULL | NULL | 平均功率(W),结束回填 |
| `status` | `ENUM('pending','charging','finished','cancelled','failed')` | NOT NULL | — | 订单状态 |
| `billing_mode` | `ENUM('normal','estimated')` | NOT NULL | `'normal'` | 计费模式:normal 真实遥测 / estimated § 6.5 B 方案估算 |
| `electric_fee_cents` | `BIGINT` | NULL | NULL | 电费(分) |
| `service_fee_cents` | `BIGINT` | NULL | NULL | 服务费(分) |
| `total_fee_cents` | `BIGINT` | NULL | NULL | 总金额(分)= 电费 + 服务费 |
| `paid_fee_cents` | `BIGINT` | NOT NULL | `0` | 实付金额(分) |
| `pay_method` | `ENUM('wechat','wallet','coupon','mixed')` | NOT NULL | — | 支付方式 |
| `wechat_transaction_id` | `VARCHAR(64)` | UNIQUE NULL | NULL | 微信支付 transaction_id(幂等键,§ 5.4) |
| `discount_cents` | `BIGINT` | NOT NULL | `0` | 优惠抵扣(分)(优惠券 + 会员折扣) |
| `refund_status` | `ENUM('none','pending','partial','full','failed')` | NOT NULL | `'none'` | 退款状态 |
| `refunded_cents` | `BIGINT` | NOT NULL | `0` | 已退金额(分) |
| `fail_reason` | `VARCHAR(256)` | NULL | NULL | 失败原因(订单 status=failed 时填) |
| `cancel_reason` | `VARCHAR(256)` | NULL | NULL | 取消原因(cancelled 时填) |
| `cancel_initiator` | `ENUM('user','system','timeout')` | NULL | NULL | 取消发起方 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 订单创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `partition_key` | `DATE` | NOT NULL | — | 分区键(冗余 `started_at` 的日期部分,加速分区路由) |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_payment_order` | `id` | 主键 | — |
| `uk_payment_order_no` | `order_no` | 唯一 | 用户查订单 |
| `uk_payment_order_port_charging` | `port_id`, `status` | 部分唯一(`status='charging'`) | 防同一端口双订单(§ 5.5) |
| `uk_payment_order_wechat_txn` | `wechat_transaction_id` | 唯一(可空) | 微信回调幂等 |
| `idx_payment_order_user_status` | `user_id`, `status`, `started_at` | 普通 | 用户订单列表(我的订单) |
| `idx_payment_order_device_started` | `device_id`, `started_at` | 普通 | 设备维度订单查询 |
| `idx_payment_order_customer_started` | `customer_id`, `started_at` | 普通 | 客户维度订单查询 |

### 约束

- **金额守恒**:`total_fee_cents = electric_fee_cents + service_fee_cents`(应用层约束,触发器或 service 层校验)
- **状态机合法迁移**:仅允许以下迁移(应用层校验)
  - `pending` → `charging` / `cancelled`
  - `charging` → `finished` / `cancelled` / `failed`
  - `cancelled` / `failed` → 可触发退款,生成 `refund_record`
- `status='finished'` 时,`ended_at` / `duration_seconds` / `meter_kwh` / `total_fee_cents` 必须 NOT NULL
- `billing_mode='estimated'` 时,`meter_kwh` 可为 NULL(§ 6.5 B 方案按功率估算)

### 关系

- 多对一 → `user.id`
- 多对一 → `admin_db.customer.id`(跨服务,无外键)
- 一对多 → `refund_record.order_id`(一笔订单可多次部分退款)
- 一对多 → `coupon_grant.used_order_id`(使用的优惠券)

### 业务规则

- **订单创建**(`status=pending`):user 收到扫码请求 → 校验端口空闲 → `INSERT payment_order(status='pending')` → 通知 gateway 启动 → 等待设备确认 → 切到 `charging`
- **订单结束**(`status=finished`):gateway 收到 `charge_ended_stream` 事件 → user 写 `ended_at` / `meter_kwh` → billing 算 `electric_fee_cents` / `service_fee_cents` → 调微信支付(或扣余额 / 用券) → `status='finished'`
- **触发退款**(失败 / 超时 / 60s 内取消 / 拔出插头):user 发布 `refund_required_stream` → billing 处理
- **估算订单提示**:billing_mode='estimated' 时,小程序充电结束页底部显示"本次计费基于设备离线数据,如有问题请联系客服"

---

## 表 3:`user_db.wallet_account`

**业务说明**:用户余额账户表。**1:1 关系** —— 每个用户最多一个账户。金额以**分**为单位存储,避免浮点误差。

**关键业务规则**:

- 余额 = `available_cents`(可用) - `frozen_cents`(冻结,如支付中)
- **余额操作必须走事务**(行级锁 `SELECT ... FOR UPDATE`),严禁先读后写
- **流水必须留痕**:每次余额变动同步写 `wallet_txn` 流水(待第二批设计)
- 不允许余额为负:`available_cents >= 0`(应用层校验)

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `user_id` | `BIGINT UNSIGNED` | UNIQUE, NOT NULL | — | 关联 `user.id`(1:1) |
| `available_cents` | `BIGINT` | NOT NULL | `0` | 可用余额(分) |
| `frozen_cents` | `BIGINT` | NOT NULL | `0` | 冻结余额(分,如支付中) |
| `total_recharged_cents` | `BIGINT` | NOT NULL | `0` | 累计充值(分,只增不减) |
| `total_consumed_cents` | `BIGINT` | NOT NULL | `0` | 累计消费(分,只增不减) |
| `total_refunded_cents` | `BIGINT` | NOT NULL | `0` | 累计退款(分,只增不减) |
| `version` | `INT UNSIGNED` | NOT NULL | `0` | 乐观锁版本号(并发更新防护) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间(账户开通时间) |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_wallet_account` | `id` | 主键 | — |
| `uk_wallet_account_user` | `user_id` | 唯一 | 1:1 关系约束 |

### 约束

- `available_cents >= 0`(应用层 CHECK + 事务)
- `frozen_cents >= 0`(应用层)
- `total_recharged_cents = total_consumed_cents + available_cents + frozen_cents + total_refunded_cents`(应用层对账,实际不强制,允许少量精度误差)

### 关系

- 一对一 → `user.id`
- 一对多 → `wallet_txn.account_id`(待第二批设计)

### 业务规则

- **充值**:小程序发起微信支付 → 微信回调成功 → `UPDATE wallet_account SET available_cents = available_cents + X, total_recharged_cents = total_recharged_cents + X, version = version + 1` + INSERT `wallet_txn`
- **扣减**(支付充电):事务开始 → `SELECT ... FOR UPDATE` → 校验 `available_cents >= X` → `UPDATE ... SET available_cents = available_cents - X, frozen_cents = frozen_cents + X` → INSERT `wallet_txn(type='freeze')` → 事务提交
- **冻结转扣减**(订单结束):`UPDATE ... SET frozen_cents = frozen_cents - X, total_consumed_cents = total_consumed_cents + X` → INSERT `wallet_txn(type='consume')`
- **冻结回退**(支付失败):`UPDATE ... SET frozen_cents = frozen_cents - X`(available 不变)
- **退款入账**:调微信退款成功 → `UPDATE ... SET available_cents = available_cents + X, total_refunded_cents = total_refunded_cents + X` + INSERT `wallet_txn(type='refund')`

---

## 表 4:`user_db.refund_record`

**业务说明**:退款记录。每笔 `payment_order` 触发的退款事件 = 一条或多条 `refund_record`(支持多次部分退款)。

**关键业务规则**:

- **触发条件**(需求文档 § 7.5):充电失败 / 充电超时 / 60s 内主动取消 / 拔出插头 / 计量异常(走人工)
- **退款方式**:原路微信自动退款,实时到账
- **幂等**:`wechat_refund_id` 唯一,防微信退款 API 重复调用
- **状态机**:`pending` → `success` / `failed` →(必要时)`manual_review`

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `order_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `payment_order.id` |
| `order_no` | `CHAR(32)` | NOT NULL | — | 冗余订单号(便于查询) |
| `user_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `user.id` |
| `refund_no` | `CHAR(32)` | UNIQUE, NOT NULL | — | 业务退款单号,格式 `RF + YYYYMMDD + 12 位随机` |
| `refund_cents` | `BIGINT` | NOT NULL | — | 退款金额(分) |
| `refund_reason` | `ENUM('charge_failed','timeout','user_cancel_60s','plug_pulled','meter_abnormal','manual')` | NOT NULL | — | 退款原因 |
| `refund_method` | `ENUM('wechat','wallet')` | NOT NULL | — | 退款方式 |
| `wechat_refund_id` | `VARCHAR(64)` | UNIQUE NULL | NULL | 微信退款单号(幂等键) |
| `status` | `ENUM('pending','success','failed','manual_review')` | NOT NULL | `'pending'` | 退款状态 |
| `fail_reason` | `VARCHAR(256)` | NULL | NULL | 失败原因(微信退款 API 报错等) |
| `retry_count` | `TINYINT UNSIGNED` | NOT NULL | `0` | 已重试次数(最多 3 次) |
| `requested_at` | `DATETIME(3)` | NOT NULL | — | 退款申请时间 |
| `completed_at` | `DATETIME(3)` | NULL | NULL | 退款完成时间(success/failed 时填) |
| `manual_review_note` | `VARCHAR(512)` | NULL | NULL | 人工审核备注(manual_review 时填) |
| `trigger_event_id` | `VARCHAR(64)` | NOT NULL | — | 触发本次退款的 Redis Stream event_id(§ 5.4 幂等) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_refund_record` | `id` | 主键 | — |
| `uk_refund_record_no` | `refund_no` | 唯一 | 用户查退款 |
| `uk_refund_record_wechat` | `wechat_refund_id` | 唯一(可空) | 微信回调幂等 |
| `uk_refund_record_event` | `trigger_event_id` | 唯一 | § 5.4 Stream 消费幂等 |
| `idx_refund_record_order` | `order_id`, `requested_at` | 普通 | 查订单的所有退款 |
| `idx_refund_record_user_status` | `user_id`, `status`, `requested_at` | 普通 | 我的退款列表 |
| `idx_refund_record_status_retry` | `status`, `retry_count`, `requested_at` | 普通 | worker 扫表重试 |

### 约束

- `status='success'` 时,`completed_at` 必须 NOT NULL
- `status='failed'` 时,`fail_reason` 必须 NOT NULL
- `status='manual_review'` 时,`manual_review_note` 必须 NOT NULL
- 单笔订单累计退款 `SUM(refund_cents)` ≤ `payment_order.paid_fee_cents`(应用层校验)

### 关系

- 多对一 → `payment_order.id`
- 多对一 → `user.id`

### 业务规则

- **触发**:`payment_order` 状态变更为 `failed` / `cancelled`(60s 内)→ user 写 `refund_record(status='pending')` + 发布 `refund_required_stream` 事件
- **执行**:worker 消费事件 → 调微信退款 API → 成功:更新 `status='success'` + `wechat_refund_id` + 发布 `comp_tx_stream`(billing 标记订单最终状态);失败:重试 3 次 → `status='failed'` → 客户财务 PC 后台人工处理
- **计量异常**:billing 检测到电量异常 → 不自动退款 → `refund_record(status='manual_review')` + 客户 PC 后台通知运维
- **多次部分退款**:支持,但需应用层校验累计不超 `paid_fee_cents`

---

## 表 5:`user_db.coupon_grant`

**业务说明**:**用户持有的优惠券发放记录**(coupon_grant = 模板 coupon 的具体实例)。一个用户可持有同一模板的多张券(在 `coupon.user_limit` 限制内)。

**关键业务规则**:

- **不发优惠券模板给用户**,而是发**发放记录**;模板 (`coupon` 表)定义面值 / 门槛 / 有效期,**发放记录**绑定到用户
- **状态机**:`unused` → `used` / `expired` / `frozen`(风控冻结)
- **使用与核销**:用户支付时,user 服务扣减 `coupon_grant` + 在 `payment_order` 记录 `discount_cents`
- **过期清理**:worker 周期任务扫表,过期券 `status='expired'`

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `coupon_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `coupon.id`(模板) |
| `user_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `user.id`(持有人) |
| `grant_no` | `CHAR(32)` | UNIQUE, NOT NULL | — | 发放单号,格式 `GR + YYYYMMDD + 12 位随机` |
| `grant_source` | `ENUM('system_event','admin_grant','phone_bind','referral','compensation')` | NOT NULL | — | 发放来源 |
| `grant_source_id` | `VARCHAR(64)` | NULL | NULL | 来源 ID(如活动 ID / 管理员操作 ID) |
| `discount_cents` | `BIGINT` | NULL | NULL | 实际优惠金额(分)快照(发券时按模板计算后存,避免模板改动影响已发券) |
| `min_spend_cents` | `BIGINT` | NULL | NULL | 最低消费(分)快照 |
| `valid_from` | `DATETIME(3)` | NOT NULL | — | 生效时间 |
| `valid_until` | `DATETIME(3)` | NOT NULL | — | 失效时间 |
| `status` | `ENUM('unused','used','expired','frozen')` | NOT NULL | `'unused'` | 状态 |
| `used_order_id` | `BIGINT UNSIGNED` | NULL | NULL | 被使用的订单 ID(`status='used'` 时填) |
| `used_at` | `DATETIME(3)` | NULL | NULL | 使用时间 |
| `frozen_reason` | `VARCHAR(128)` | NULL | NULL | 冻结原因(风控触发等) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 发放时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_coupon_grant` | `id` | 主键 | — |
| `uk_coupon_grant_no` | `grant_no` | 唯一 | 单条发放追溯 |
| `idx_coupon_grant_user_status` | `user_id`, `status`, `valid_until` | 普通 | 用户"我的优惠券"列表 |
| `idx_coupon_grant_coupon_status` | `coupon_id`, `status` | 普通 | 模板维度统计发放 / 使用 |
| `idx_coupon_grant_expire_scan` | `status`, `valid_until` | 普通 | worker 周期扫表清理过期 |

### 约束

- `status='used'` 时,`used_order_id` / `used_at` 必须 NOT NULL
- `status='expired'` 时,`valid_until < NOW()`(应用层校验)
- `status='frozen'` 时,`frozen_reason` 必须 NOT NULL
- 一个 `user_id` 对同一 `coupon_id` 的未使用券数量 ≤ `coupon.user_limit`(应用层校验)

### 关系

- 多对一 → `coupon.id`(模板)
- 多对一 → `user.id`(持有人)
- 多对一 → `payment_order.id`(使用订单,可空)

### 业务规则

- **发放**(系统活动):worker 消费 `coupon_grant_required_stream` → 查 `coupon` 模板 → 校验未超 `total_limit` / 用户未超 `user_limit` → INSERT `coupon_grant(status='unused')` + UPDATE `coupon.granted_count`
- **发放**(手机号绑定):user 完成手机号绑定 → 查"绑定赠送"类模板 → INSERT `coupon_grant` + 推送"您获得 X 优惠券"小程序消息
- **使用**(下单核销):user 收到下单请求 → 校验 `coupon_grant.status='unused'` + 在有效期 + 满足 `min_spend_cents` → 事务内 UPDATE `coupon_grant(status='used', used_order_id, used_at)` + INSERT `payment_order` 时 `discount_cents = coupon.discount_cents`
- **过期清理**:worker 每日扫表 → `status='unused'` 且 `valid_until < NOW()` → UPDATE `status='expired'`

---

**本批次结束**

> 剩余 6 张表(`wallet_txn` / `coupon` 模板 / `membership_card` / `invoice_request` / `port_view` / `payment_callback_idempotent`)将在第二批设计,届时会沿用本文件的"通用约定"和表设计格式。
