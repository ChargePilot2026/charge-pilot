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
| **软删除** | **本期启用**:每张业务表加 `deleted_at DATETIME(3) NULL` + `deleted_by BIGINT UNSIGNED NULL`(操作者 ID);删除 = `UPDATE ... SET deleted_at = NOW()`;**所有查询默认 `WHERE deleted_at IS NULL`**;`idx_*_deleted_at` 索引加速扫描;数据兜底靠 worker 周期任务物理归档(超 3 年) | 审计日志 / 幂等表 不软删 |
| 金额 | `BIGINT`(单位:**分**,避免浮点误差) | 无 |
| 加密字段 | `VARBINARY` + MySQL `AES_ENCRYPT`(§ 9.3);读取用 `AES_DECRYPT` | 仅敏感字段 |
| 状态字段 | `ENUM(...)` + 配套 comment 列出全部枚举值;不允许文本字段存状态 | 仅业务状态字段 |
| JSON 字段 | `JSON` 类型,严格字段定义在文档而非 DB | 仅配置类元数据 / 组合支付明细 |
| 主键索引 | 默认随主键创建 | 无 |
| 索引命名 | `pk_` / `uk_` / `idx_` / `fk_` 前缀 | 无 |
| 外键 | **不声明**(跨服务事务用最终一致性,§ 4.3 + § 5.4) | 无 |

## 表清单(12 张)

| 表名 | 业务说明 | 分表策略 | 估算行数(单客户 5 年) |
| --- | --- | --- | --- |
| `user` | 终端用户(openid + 可选手机号) | 不分 | ~50 万 |
| **`charge_order`** | **充电会话生命周期**(扫码 → 启动 → 充电中 → 结束 / 取消 / 失败),**纯业务,不含支付** | 按月分区(§ 4.8) | ~2000 万 |
| **`payment_order`** | **支付订单**(支持充电付款 / 钱包充值两种业务),**纯支付,不含充电** | 按月分区(§ 4.8) | ~2200 万 |
| `wallet_account` | 用户余额账户(1:1) | 不分 | ~50 万 |
| `wallet_txn` | 余额流水 | 按月分区(§ 4.8) | ~500 万 |
| `refund_record` | 退款记录(关联 `payment_order.id`) | 按月分区(隐含) | ~200 万 |
| `coupon` | 优惠券模板 | 不分 | ~1000 |
| `coupon_grant` | 优惠券发放记录(用户持有,使用时关联 `payment_order.id`) | 不分 | ~500 万 |
| `membership_card` | 会员卡(本期预留,数据可能为空) | 不分 | ~10 万 |
| `invoice_request` | 发票申请记录 | 不分 | ~50 万 |
| `port_view` | 找桩缓存(冗余自 gateway_db,加速查询) | 不分 | ~5000 |
| `payment_callback_idempotent` | 微信支付回调幂等表 | 不分 | ~200 万 |

> **本文件首批设计 6 张核心表**:`user` / `charge_order` / `payment_order` / `wallet_account` / `refund_record` / `coupon_grant`。
> 剩余 6 张(`wallet_txn` / `coupon` / `membership_card` / `invoice_request` / `port_view` / `payment_callback_idempotent`)在第二批设计。

### 关键架构决策(本批次)

**充电订单与支付订单拆分**(老杨师傅决策):

- **charge_order** 只记录充电生命周期:设备、端口、电量、时长、状态机;**没有任何支付字段**
- **payment_order** 只记录支付:`biz_type` + `biz_id` 通用关联,支持充电付款与钱包充值两种业务
- **组合支付**(微信 + 余额 + 优惠券):`payment_order` 主单(pay_method='mixed',total_amount) + 多张子单(每通道一张,parent_order_id 关联)
- **退款链路**:查 `payment_order` → 看 `biz_type` → 若是 `charge` 反查 `charge_order` 看是哪笔充电

**软删除**(老杨师傅决策):

- 所有业务表加 `deleted_at` + `deleted_by`
- 删除 = UPDATE 而非 DELETE
- 所有查询默认 `WHERE deleted_at IS NULL`(在仓储层封装,不写在 SQL 里)
- 数据兜底:worker 周期任务物理归档 `deleted_at < NOW() - 3 YEAR` 的记录

---

## 表 1:`user_db.user`

**业务说明**:终端用户基础信息表。每条记录对应一个**微信用户**(以 `openid` 为唯一标识),手机号绑定为可选。

**关键业务规则**:

- 一个微信用户 = 一条记录(按 `openid` 唯一)
- 手机号绑定为可选,绑送余额或优惠券(需求文档 § 5.3.2)
- 注销流程:用户主动注销 → 软删除(`deleted_at`)+ 抹除 `phone_enc` / `unionid`(`openid` 保留 30 天后物理归档)
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
| **`deleted_at`** | `DATETIME(3)` | NULL | NULL | **软删除时间**(NULL = 未删除) |
| **`deleted_by`** | `BIGINT UNSIGNED` | NULL | NULL | **删除操作者 ID**(用户主动注销 = NULL;管理员操作 = admin user id) |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_user` | `id` | 主键 | — |
| `uk_user_openid` | `openid` | 唯一 | 登录 / `code2Session` 后查表 |
| `uk_user_unionid` | `unionid` | 唯一(可空) | 跨小程序 unionid 打通 |
| `uk_user_phone_hash` | `phone_hash` | 唯一(可空) | "该手机号是否已注册"查询 |
| `idx_user_last_active` | `last_active_at` | 普通 | 找活跃用户 / 数据分析 |
| **`idx_user_deleted_at`** | `deleted_at` | 普通 | **加速扫描已删除用户**(worker 物理归档) |

### 约束

- `phone_enc` 与 `phone_hash` **至少一个为 NULL**(未绑定手机号时都为 NULL);绑定后必须两个都填
- `banned_at` NOT NULL 时,`status` 必须为 `banned`(应用层约束)
- `deleted_at` NOT NULL 时,`deleted_by` 可 NULL(用户主动注销)或 NOT NULL(管理员操作)

### 关系

- 一对多 → `charge_order.user_id`(一个用户可有多笔充电订单)
- 一对多 → `payment_order.user_id`(一个用户可有多笔支付订单,含充值)
- 一对一 → `wallet_account.user_id`(每个用户一个余额账户)
- 一对多 → `coupon_grant.user_id`(用户持有的多张优惠券)

### 业务规则

- **首次登录流程**:`code2Session` → 拿 `openid` → `INSERT ... ON DUPLICATE KEY UPDATE last_active_at = NOW()`(幂等)
- **手机号绑定流程**:小程序 `getPhoneNumber` 拿明文 → 应用层 `AES_ENCRYPT` 写 `phone_enc` + `SHA256` 写 `phone_hash` → **触发绑送奖励**(发优惠券 / 余额)
- **注销流程**:`UPDATE user SET status='banned', banned_at=NOW(), banned_reason='user_request', phone_enc=NULL, unionid=NULL, nickname='', avatar_url=NULL, deleted_at=NOW(), deleted_by=NULL`;`openid` 保留用于 30 天审计追溯,30 天后 worker 物理归档
- **软删除查询规范**:所有查询经仓储层封装(`UserRepository::find_by_id($id)`),仓储内自动加 `WHERE deleted_at IS NULL`;直接 `SELECT *` 仅用于后台运维查询

---

## 表 2:`user_db.charge_order`

**业务说明**:**充电会话生命周期表**。一笔 `charge_order` = 用户一次完整的充电过程(扫码 → 启动 → 充电中 → 结束 / 取消 / 失败)。**不含任何支付字段** —— 支付通过 `payment_order` 关联。

**关键业务规则**:

- 状态机:`pending` → `charging` → `finished` / `cancelled` / `failed`
- 唯一约束:`(port_id, status='charging')` 部分唯一索引,防同一端口双订单(§ 5.5)
- 估算订单:§ 6.5 B 方案的离线补传兜底,订单带 `billing_mode='estimated'` 标记
- 不存支付信息:`electric_fee_cents` / `service_fee_cents` / `paid_fee_cents` / 微信 transaction_id **全部移到 `payment_order`**

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `order_no` | `CHAR(32)` | UNIQUE, NOT NULL | — | 业务订单号,格式 `CH + YYYYMMDDHHmmss + 12 位随机`(用户侧展示"充电订单号") |
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
| `fail_reason` | `VARCHAR(256)` | NULL | NULL | 失败原因(`status=failed` 时填) |
| `cancel_reason` | `VARCHAR(256)` | NULL | NULL | 取消原因(`cancelled` 时填) |
| `cancel_initiator` | `ENUM('user','system','timeout')` | NULL | NULL | 取消发起方 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 订单创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间(NULL = 未删除) |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |
| `partition_key` | `DATE` | NOT NULL | — | 分区键(冗余 `started_at` 的日期部分) |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_charge_order` | `id` | 主键 | — |
| `uk_charge_order_no` | `order_no` | 唯一 | 用户查充电订单 |
| `uk_charge_order_port_charging` | `port_id`, `status` | 部分唯一(`status='charging'`) | 防同一端口双订单(§ 5.5) |
| `idx_charge_order_user_started` | `user_id`, `started_at` | 普通 | 用户充电订单列表 |
| `idx_charge_order_device_started` | `device_id`, `started_at` | 普通 | 设备维度订单查询 |
| `idx_charge_order_customer_started` | `customer_id`, `started_at` | 普通 | 客户维度订单查询 |
| `idx_charge_order_deleted_at` | `deleted_at` | 普通 | worker 物理归档扫描 |

### 约束

- **状态机合法迁移**:仅允许以下迁移(应用层校验)
  - `pending` → `charging` / `cancelled`
  - `charging` → `finished` / `cancelled` / `failed`
- `status='finished'` 时,`ended_at` / `duration_seconds` / `meter_kwh` 必须 NOT NULL
- `billing_mode='estimated'` 时,`meter_kwh` 可为 NULL(§ 6.5 B 方案按功率估算)
- `deleted_at` NOT NULL 时,该订单不可再触发任何业务流程(状态冻结)

### 关系

- 多对一 → `user.id`
- 多对一 → `admin_db.customer.id`(跨服务,无外键)
- 一对多 → `payment_order`(通过 `payment_order.biz_id` 字段 + `biz_type='charge'` 关联;一笔充电订单 = 一笔或零笔支付订单,组合支付场景下支付订单是主单+多张子单结构)

### 业务规则

- **订单创建**(`status=pending`):user 收到扫码请求 → 校验端口空闲 → `INSERT charge_order(status='pending')` → 通知 gateway 启动 → 等待设备确认 → 切到 `charging`
- **订单结束**(`status=finished`):gateway 收到 `charge_ended_stream` 事件 → user 写 `ended_at` / `meter_kwh` / `power_w` / `duration_seconds` → 触发 `payment_order` 创建(支付环节独立于本表)
- **触发退款**(失败 / 超时 / 60s 内取消 / 拔出插头):**不直接创建 refund_record**,而是发布 `refund_required_stream` 事件 → user 找到对应的 `payment_order`(通过 `biz_id=charge_order.id` + `biz_type='charge'`)→ 创建 `refund_record`
- **估算订单提示**:`billing_mode='estimated'` 时,小程序充电结束页底部显示"本次计费基于设备离线数据,如有问题请联系客服"

---

## 表 3:`user_db.payment_order`

**业务说明**:**支付订单表**(纯支付,**不含充电生命周期字段**)。通过 `biz_type` + `biz_id` 关联多种业务:

- `biz_type='charge'`:充电付款,`biz_id` = `charge_order.id`
- `biz_type='recharge'`:钱包充值,`biz_id` = NULL

**关键业务规则**:

- **一笔充电订单 = 一笔或一组支付订单**:
  - 简单场景(纯微信付款):1 张 payment_order
  - 组合支付场景(微信 + 余额 + 优惠券):1 张主单(`pay_method='mixed'`)+ N 张子单(`parent_order_id` 指向主单)
- **组合支付子单**:`pay_method` 是单一通道(wechat / wallet / coupon),`parent_order_id` 指向主单
- **退款**:按 `payment_order.id` 找退款记录,与 `biz_type` / `biz_id` 无关
- **幂等**:`wechat_transaction_id` 唯一,防微信回调重复入账(§ 5.4)
- **软删除**启用:异常订单(用户主动撤销 / 客户管理员删除)软删,保留审计

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `order_no` | `CHAR(32)` | UNIQUE, NOT NULL | — | 业务支付单号,格式 `PY + YYYYMMDDHHmmss + 12 位随机` |
| `biz_type` | `ENUM('charge','recharge')` | NOT NULL | — | **业务类型**:`charge` 充电付款 / `recharge` 钱包充值 |
| `biz_id` | `BIGINT UNSIGNED` | NULL | NULL | **关联的业务订单 ID**:`biz_type='charge'` 时 = `charge_order.id`;`biz_type='recharge'` 时 = NULL |
| `user_id` | `BIGINT UNSIGNED` | NOT NULL | — | 付款用户 |
| `customer_id` | `BIGINT UNSIGNED` | NOT NULL | — | 客户 ID(冗余) |
| `parent_order_id` | `BIGINT UNSIGNED` | NULL | NULL | **组合支付场景**:子单的父订单 ID(主单的 parent_order_id = NULL) |
| `pay_method` | `ENUM('wechat','wallet','mixed')` | NOT NULL | — | 支付方式:**主单** = `mixed`(组合);**子单** = `wechat` / `wallet`;**单独支付** = `wechat` / `wallet` |
| `pay_components` | `JSON` | NULL | NULL | **组合支付明细**(主单填,子单留空):JSON 数组,每个元素含 `channel`(`wechat`/`wallet`/`coupon`)、`amount_cents`、`channel_ref`(微信 transaction_id / wallet 流水 / coupon_grant_id) |
| `total_fee_cents` | `BIGINT` | NOT NULL | — | 支付总金额(分)。主单 = 组合金额合计;子单 = 本通道金额;单独支付 = 实际支付金额 |
| `discount_cents` | `BIGINT` | NOT NULL | `0` | 优惠抵扣(分,仅主单有值) |
| `paid_fee_cents` | `BIGINT` | NOT NULL | `0` | 实付金额(分,= total - discount) |
| `wechat_transaction_id` | `VARCHAR(64)` | UNIQUE NULL | NULL | 微信支付 transaction_id(仅 pay_method='wechat' 或组合中含微信时填;幂等键,§ 5.4) |
| `status` | `ENUM('pending','success','failed','cancelled')` | NOT NULL | `'pending'` | 支付状态 |
| `fail_reason` | `VARCHAR(256)` | NULL | NULL | 失败原因(`status='failed'` 时填) |
| `paid_at` | `DATETIME(3)` | NULL | NULL | 支付完成时间(`status='success'` 时填) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |
| `partition_key` | `DATE` | NOT NULL | — | 分区键 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_payment_order` | `id` | 主键 | — |
| `uk_payment_order_no` | `order_no` | 唯一 | 用户查支付单 |
| `uk_payment_order_wechat_txn` | `wechat_transaction_id` | 唯一(可空) | 微信回调幂等(§ 5.4) |
| `idx_payment_order_biz` | `biz_type`, `biz_id` | 普通 | 反查"某笔充电的所有支付单" / "某用户的充值记录" |
| `idx_payment_order_user_status` | `user_id`, `status`, `created_at` | 普通 | 我的支付订单列表 |
| `idx_payment_order_parent` | `parent_order_id` | 普通 | 查"主单的所有子单"(组合支付) |
| `idx_payment_order_customer_created` | `customer_id`, `created_at` | 普通 | 客户维度对账 |
| `idx_payment_order_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- **主单 / 子单关系**(应用层校验):
  - 主单:`parent_order_id IS NULL` + `pay_method='mixed'` + `pay_components` NOT NULL
  - 子单:`parent_order_id NOT NULL` + `pay_method IN ('wechat','wallet')` + `pay_components IS NULL`
  - 单独支付:`parent_order_id IS NULL` + `pay_method IN ('wechat','wallet')` + `pay_components IS NULL`
- **`biz_type` 与 `biz_id` 对应关系**:
  - `biz_type='charge'` → `biz_id NOT NULL` + `biz_id = charge_order.id`(存在性应用层校验)
  - `biz_type='recharge'` → `biz_id IS NULL`
- **金额守恒**(应用层校验):
  - 主单:`paid_fee_cents + discount_cents = total_fee_cents`
  - 子单:`paid_fee_cents = total_fee_cents`(无折扣概念)
  - 组合支付:所有子单 `SUM(paid_fee_cents) = 主单.paid_fee_cents`
- `status='success'` 时,`paid_at` NOT NULL
- `status='failed'` 时,`fail_reason` NOT NULL
- `deleted_at` NOT NULL 时,该订单冻结,不可触发退款

### 关系

- 多对一 → `user.id`
- 多对一 → `charge_order.id`(通过 `biz_id`,仅 `biz_type='charge'`,跨服务无外键)
- 自关联:`parent_order_id` → `payment_order.id`(组合支付主单-子单)
- 一对多 → `refund_record.payment_order_id`

### 业务规则

- **充电付款 - 单独支付**(纯微信 / 纯余额):
  - user 收到充电结束事件 → 算费用 → `INSERT payment_order(biz_type='charge', biz_id=$charge_order_id, parent_order_id=NULL, pay_method='wechat', total_fee_cents=X)` → 调微信支付 → 回调成功 → UPDATE `status='success'`

- **充电付款 - 组合支付**(微信 + 余额 + 优惠券):
  - user 算费用 30 元 = 微信 20 + 余额 5 + 优惠券 5
  - **第 1 步**(主单):`INSERT payment_order(order_no='PY_main', biz_type='charge', biz_id=$id, parent_order_id=NULL, pay_method='mixed', total_fee_cents=3000, discount_cents=500, paid_fee_cents=2500, pay_components=[{channel:'wechat',amount_cents:2000},{channel:'wallet',amount_cents:500},{channel:'coupon',amount_cents:500,coupon_grant_id:123}])`
  - **第 2 步**(子单):`INSERT payment_order(order_no='PY_wechat', ..., parent_order_id=$主单.id, pay_method='wechat', total_fee_cents=2000)` → 调微信支付 → 成功
  - **第 3 步**(子单):`INSERT payment_order(order_no='PY_wallet', ..., parent_order_id=$主单.id, pay_method='wallet', total_fee_cents=500)` → 扣余额 → 成功
  - **第 4 步**(优惠券核销):直接 UPDATE `coupon_grant.status='used', used_payment_order_id=$主单.id`(不走 payment_order,优惠券是抵扣不是支付通道;若要留痕也可建 coupon 子单)
  - **第 5 步**(主单):所有子单成功后 → UPDATE 主单 `status='success'`
  - **任一子单失败**:触发 Saga 补偿(已成功的子单调退款 / 退余额)+ UPDATE 主单 `status='failed'`

- **钱包充值**:`INSERT payment_order(biz_type='recharge', biz_id=NULL, parent_order_id=NULL, pay_method='wechat', total_fee_cents=X)` → 调微信支付 → 回调成功 → 钱包入账 + 写 `wallet_txn`

- **退款**:`refund_record.payment_order_id = $payment_order.id`(直接关联,不看 biz_type)

- **微信回调幂等**(§ 5.4):按 `wechat_transaction_id` 唯一索引去重,重复推送直接返回 200 OK

---

## 表 4:`user_db.wallet_account`

**业务说明**:用户余额账户表。**1:1 关系** —— 每个用户最多一个账户。金额以**分**为单位存储,避免浮点误差。

**关键业务规则**:

- 余额 = `available_cents`(可用)- `frozen_cents`(冻结,如支付中)
- **余额操作必须走事务**(行级锁 `SELECT ... FOR UPDATE`),严禁先读后写
- **流水必须留痕**:每次余额变动同步写 `wallet_txn` 流水(待第二批设计)
- 不允许余额为负:`available_cents >= 0`(应用层校验)
- 软删除:用户注销时软删账户(保留审计)

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
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_wallet_account` | `id` | 主键 | — |
| `uk_wallet_account_user` | `user_id` | 唯一 | 1:1 关系约束 |
| `idx_wallet_account_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `available_cents >= 0`(应用层 CHECK + 事务)
- `frozen_cents >= 0`(应用层)
- `total_recharged_cents = total_consumed_cents + available_cents + frozen_cents + total_refunded_cents`(应用层对账,实际不强制,允许少量精度误差)

### 关系

- 一对一 → `user.id`
- 一对多 → `wallet_txn.account_id`(待第二批设计)

### 业务规则

- **充值**:小程序发起微信支付 → 微信回调成功 → `UPDATE wallet_account SET available_cents = available_cents + X, total_recharged_cents = total_recharged_cents + X, version = version + 1` + INSERT `wallet_txn`
- **扣减**(组合支付中的余额子单):事务开始 → `SELECT ... FOR UPDATE` → 校验 `available_cents >= X` → `UPDATE ... SET available_cents = available_cents - X, frozen_cents = frozen_cents + X` → INSERT `wallet_txn(type='freeze')` → 事务提交
- **冻结转扣减**(订单结束):`UPDATE ... SET frozen_cents = frozen_cents - X, total_consumed_cents = total_consumed_cents + X` → INSERT `wallet_txn(type='consume')`
- **冻结回退**(支付失败):`UPDATE ... SET frozen_cents = frozen_cents - X`(available 不变)
- **退款入账**:调微信退款成功 → `UPDATE ... SET available_cents = available_cents + X, total_refunded_cents = total_refunded_cents + X` + INSERT `wallet_txn(type='refund')`
- **注销软删**:`UPDATE wallet_account SET deleted_at=NOW(), deleted_by=$user_id`;`available_cents > 0` 时提示用户先提现

---

## 表 5:`user_db.refund_record`

**业务说明**:退款记录。每笔 `payment_order` 触发的退款事件 = 一条或多条 `refund_record`(支持多次部分退款)。**通过 `payment_order_id` 关联支付订单,不看 `charge_order`**。

**关键业务规则**:

- **触发条件**(需求文档 § 7.5):充电失败 / 充电超时 / 60s 内主动取消 / 拔出插头 / 计量异常(走人工)
- **退款方式**:原路微信自动退款 / 钱包余额退回,实时到账
- **幂等**:`wechat_refund_id` 唯一,防微信退款 API 重复调用
- **状态机**:`pending` → `success` / `failed` →(必要时)`manual_review`

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `payment_order_id` | `BIGINT UNSIGNED` | NOT NULL | — | **关联 `payment_order.id`**(不再关联 charge_order) |
| `payment_order_no` | `CHAR(32)` | NOT NULL | — | 冗余支付单号 |
| `user_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `user.id` |
| `refund_no` | `CHAR(32)` | UNIQUE, NOT NULL | — | 业务退款单号,格式 `RF + YYYYMMDD + 12 位随机` |
| `refund_cents` | `BIGINT` | NOT NULL | — | 退款金额(分) |
| `refund_reason` | `ENUM('charge_failed','timeout','user_cancel_60s','plug_pulled','meter_abnormal','manual','recharge_refund')` | NOT NULL | — | 退款原因;`recharge_refund` 用于钱包充值退款场景 |
| `refund_method` | `ENUM('wechat','wallet')` | NOT NULL | — | 退款方式(原路返回) |
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
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_refund_record` | `id` | 主键 | — |
| `uk_refund_record_no` | `refund_no` | 唯一 | 用户查退款 |
| `uk_refund_record_wechat` | `wechat_refund_id` | 唯一(可空) | 微信回调幂等 |
| `uk_refund_record_event` | `trigger_event_id` | 唯一 | § 5.4 Stream 消费幂等 |
| `idx_refund_record_payment_order` | `payment_order_id`, `requested_at` | 普通 | 查支付单的所有退款 |
| `idx_refund_record_user_status` | `user_id`, `status`, `requested_at` | 普通 | 我的退款列表 |
| `idx_refund_record_status_retry` | `status`, `retry_count`, `requested_at` | 普通 | worker 扫表重试 |
| `idx_refund_record_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `status='success'` 时,`completed_at` 必须 NOT NULL
- `status='failed'` 时,`fail_reason` 必须 NOT NULL
- `status='manual_review'` 时,`manual_review_note` 必须 NOT NULL
- 单笔支付订单累计退款 `SUM(refund_cents)` ≤ `payment_order.paid_fee_cents`(应用层校验)

### 关系

- 多对一 → `payment_order.id`(纯支付维度,不看 `charge_order`)
- 多对一 → `user.id`
- **不直接关联 `charge_order`**:退款链路只看支付订单;若需查充电订单,通过 `payment_order.biz_id` + `biz_type='charge'` 反查

### 业务规则

- **触发**:`charge_order` 状态变更为 `failed` / `cancelled`(60s 内)→ user 反查对应的 `payment_order`(通过 `biz_id` + `biz_type='charge'`)→ 写 `refund_record(payment_order_id=...)` + 发布 `refund_required_stream` 事件
- **执行**:worker 消费事件 → 调微信退款 API → 成功:更新 `status='success'` + `wechat_refund_id` + 发布 `comp_tx_stream`;失败:重试 3 次 → `status='failed'` → 客户财务 PC 后台人工处理
- **计量异常**:billing 检测到电量异常 → 不自动退款 → `refund_record(status='manual_review')` + 客户 PC 后台通知运维
- **充值退款**:`recharge_refund` 场景下,用户申请退回充值款项 → 写 `refund_record(payment_order_id=$充值订单.id)` + 走微信退款原路返回
- **多次部分退款**:支持,但需应用层校验累计不超 `payment_order.paid_fee_cents`

---

## 表 6:`user_db.coupon_grant`

**业务说明**:**用户持有的优惠券发放记录**。一个用户可持有同一模板的多张券(在 `coupon.user_limit` 限制内)。使用时通过 `used_payment_order_id` 关联到支付订单(主单)。

**关键业务规则**:

- **不发优惠券模板给用户**,而是发**发放记录**;模板 (`coupon` 表)定义面值 / 门槛 / 有效期
- **状态机**:`unused` → `used` / `expired` / `frozen`(风控冻结)
- **使用与核销**:组合支付场景下,优惠券作为主单 `pay_components` 中的一个元素;核销时 UPDATE `coupon_grant.status='used'` + `used_payment_order_id=$主单.id`
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
| `discount_cents` | `BIGINT` | NULL | NULL | 实际优惠金额(分)快照 |
| `min_spend_cents` | `BIGINT` | NULL | NULL | 最低消费(分)快照 |
| `valid_from` | `DATETIME(3)` | NOT NULL | — | 生效时间 |
| `valid_until` | `DATETIME(3)` | NOT NULL | — | 失效时间 |
| `status` | `ENUM('unused','used','expired','frozen')` | NOT NULL | `'unused'` | 状态 |
| `used_payment_order_id` | `BIGINT UNSIGNED` | NULL | NULL | **被使用的支付订单 ID**(`status='used'` 时填,指向 `payment_order.id`,即组合支付主单) |
| `used_at` | `DATETIME(3)` | NULL | NULL | 使用时间 |
| `frozen_reason` | `VARCHAR(128)` | NULL | NULL | 冻结原因(风控触发等) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 发放时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_coupon_grant` | `id` | 主键 | — |
| `uk_coupon_grant_no` | `grant_no` | 唯一 | 单条发放追溯 |
| `idx_coupon_grant_user_status` | `user_id`, `status`, `valid_until` | 普通 | 用户"我的优惠券"列表 |
| `idx_coupon_grant_coupon_status` | `coupon_id`, `status` | 普通 | 模板维度统计发放 / 使用 |
| `idx_coupon_grant_expire_scan` | `status`, `valid_until` | 普通 | worker 周期扫表清理过期 |
| `idx_coupon_grant_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `status='used'` 时,`used_payment_order_id` / `used_at` 必须 NOT NULL
- `status='expired'` 时,`valid_until < NOW()`(应用层校验)
- `status='frozen'` 时,`frozen_reason` 必须 NOT NULL
- 一个 `user_id` 对同一 `coupon_id` 的未使用券数量 ≤ `coupon.user_limit`(应用层校验)

### 关系

- 多对一 → `coupon.id`(模板)
- 多对一 → `user.id`(持有人)
- 多对一 → `payment_order.id`(使用订单,**指向组合支付主单**)

### 业务规则

- **发放**(系统活动):worker 消费 `coupon_grant_required_stream` → 查 `coupon` 模板 → 校验未超 `total_limit` / 用户未超 `user_limit` → INSERT `coupon_grant(status='unused')` + UPDATE `coupon.granted_count`
- **发放**(手机号绑定):user 完成手机号绑定 → 查"绑定赠送"类模板 → INSERT `coupon_grant` + 推送"您获得 X 优惠券"小程序消息
- **使用**(组合支付下单核销):user 收到组合支付请求 → 校验 `coupon_grant.status='unused'` + 在有效期 + 满足 `min_spend_cents` → 创建主单 `payment_order(pay_components 含 coupon 项)` → 事务内 UPDATE `coupon_grant(status='used', used_payment_order_id=$主单.id, used_at)`(优惠券核销与主单创建在同事务,避免券被重复使用)
- **过期清理**:worker 每日扫表 → `status='unused'` 且 `valid_until < NOW()` → UPDATE `status='expired'`

---

**本批次结束**

> 剩余 6 张表(`wallet_txn` / `coupon` 模板 / `membership_card` / `invoice_request` / `port_view` / `payment_callback_idempotent`)将在第二批设计,沿用本文件的"通用约定"和表设计格式。
