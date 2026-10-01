# user_db 数据库表设计

**所属服务**:user(对外 :8081,小程序侧 API)
**Schema 名**:`user_db`
**字符集 / 排序规则**:`utf8mb4` / `utf8mb4_unicode_ci`
**引擎**:InnoDB(全表)
**数据库版本**:MySQL 8.4 LTS

> **软删除策略**:跨 schema 对账见 `docs/cross-reference.md` § 5.5(权威源),本文档通用约定与之一致;若冲突,以 cross-reference 为准。

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
| **`customer_id` 列** | **不带**(P1-6 单客户单部署硬约束,与 README § 核心约束一致);客户级隔离由部署边界保证(每客户独立一套 `user_db` 实例,无跨客户访问) | 无 |
| **业务状态 vs 软删除二维关系** | `status` 字段(如 `pending` / `success` / `cancelled`)是**业务生命周期状态机**;`deleted_at` 字段是**数据可见性软删除**;**二者独立,不互斥**:被软删的订单 `status` 保持原值(如 `cancelled` 订单被软删后,`status='cancelled'` + `deleted_at NOT NULL`);查询经仓储层封装自动加 `WHERE deleted_at IS NULL`,运维查询可绕过 | 审计日志 / 幂等表 无 status 字段 |

## 表清单(19 张)

| 表名 | 业务说明 | 分表策略 | 估算行数(单客户 5 年) |
| --- | --- | --- | --- |
| `user` | 终端用户(openid + 可选手机号) | 不分 | ~50 万 |
| **`charge_order`** | **充电会话生命周期**(扫码 → 启动 → 充电中 → 结束 / 取消 / 失败),**纯业务,不含支付** | 按月分区(§ 4.8) | ~2000 万 |
| **`payment_order`** | **支付订单**(支持充电付款 / 钱包充值两种业务),**纯支付,不含充电** | 按月分区(§ 4.8) | ~2200 万 |
| `wallet_account` | 用户余额账户(1:1) | 不分 | ~50 万 |
| `wallet_txn` | 余额流水 | 按月分区(§ 4.8) | ~500 万 |
| `refund_record` | 退款记录(关联 `payment_order.id`) | 按月分区(隐含) | ~200 万 |
| **`refund_reconcile_diff`** | **每日对账差异记录**(微信账单 vs 内部退款) | 不分 | ~5000/年 |
| **`risk_freeze_log`** | **风控冻结记录**(频次/金额触发) | 不分 | ~1000/年 |
| `coupon` | 优惠券模板 | 不分 | ~1000 |
| `coupon_grant` | 优惠券发放记录(用户持有,使用时关联 `payment_order.id`) | 不分 | ~500 万 |
| `membership_card` | 会员卡(本期预留,数据可能为空) | 不分 | ~10 万 |
| `invoice_request` | 发票申请记录 | 不分 | ~50 万 |
| `port_view` | 找桩缓存(冗余自 gateway_db,加速查询) | 不分 | ~5000 |
| `payment_callback_idempotent` | 微信支付回调幂等表 | 不分 | ~200 万 |
| **`feedback`** | **评价 / 投诉记录**(每笔订单唯一评价) | 不分 | ~200 万 |
| **`device_fault_report`** | **设备报修记录**(用户报修 + 巡检处理) | 不分 | ~5 万 |
| **`device_fault_report_event`** | **报修处理状态历史与巡检备注** | 不分 | 报修数量 × 派单/处理次数 |
| **`active_port_charge`** | **端口当前充电占用**,跨月唯一性兜底 | 不分 | ≤ 端口数 |
| **`event_outbox`** | **user 服务待发布 Stream 事件**,与业务状态同事务落库 | 不分 | 按处理状态滚动清理 |

> **本文件包含全部 19 张表**:原 16 张业务表 + 端口占用、事件 outbox 和报修处理历史表。
> 业务场景覆盖:用户管理 / 充电 / 支付 / 退款 / 钱包 / 优惠券 / 会员 / 发票 / 找桩 / 微信支付幂等 / **评价 / 投诉 / 设备报修**。

### 容量估算假设前提

> **本表行数为 5 年累计估算,基于下列业务假设。后续如假设变更,必须同步更新本节。**

| 假设项 | 取值 | 出处 |
| --- | --- | --- |
| 单客户部署规模 | 5000 台设备 × 5000 终端用户 | `需求分析.md` § 13.1 / `技术规格.md` § 10.3 |
| 单端口日均充电单数 | 1.0 单(小区场景中位数;运营商场景可达 3-5 单) | 行业经验值,需客户首次签约时按站点类型确认 |
| 平均充电时长 | 4 小时 | 行业经验值(物业居民充电模式) |
| 平均订单关联数据 | 1 条 charge_order + 1-2 条 payment_order + 0-1 退款 + 0-1 invoice + 0-1 投诉 | 单笔订单典型形态 |
| 单设备日均遥测帧 | 86,400(每秒 1 帧)+ 客户上报增量约 2 倍 | 协议层约定见 `技术规格.md` § 6.4 + 实际比 `telemetry` 频次高 |
| `wallet_txn` 5 年累计 | 用户充值 + 退款 + 赠送 = ~5 × 充值订单 ~ ~500 万 | 由 `wallet_account` 平均充值频次推得 |
| `feedback` 5 年累计 | 评价率约 30% × 2000 万订单 ~ ~600 万,**5 年**中后期开始填写 | 经验值,需要客户持续观察后校准 |
| `device_fault_report` 5 年累计 | 5000 设备 × 5 年 × 1 次报修/年 ~ ~5 万 | 设备老旧程度相关,**只作底数** |

> **如何校准**:客户上线满 3 个月后,导出各表真实行数与本表对照;差异 > 2x 必须更新此处假设并触发 `cross-reference.md` § 2 同步。

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
- 手机号绑定为可选;当前实现不存手机号明文且尚未配置绑送奖励
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
| `phone_hash` | `VARCHAR(64)` | UNIQUE NULL | NULL | 服务端对微信验证手机号计算的 SHA-256 哈希(用于唯一性检查) |
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
| `uk_phone_hash` | `phone_hash` | 唯一(可空) | 阻止同一手机号绑定多个有效账号(由 `user_db/0018_unique_phone_hash.sql` 增加) |
| `idx_user_last_active` | `last_active_at` | 普通 | 找活跃用户 / 数据分析 |
| **`idx_user_deleted_at`** | `deleted_at` | 普通 | **加速扫描已删除用户**(worker 物理归档) |

### 约束

- 当前微信手机号绑定流程仅写 `phone_hash`;不保存明文，也不要求 `phone_enc` 必须有值。未绑定时 `phone_hash` 为 NULL。
- `banned_at` NOT NULL 时,`status` 必须为 `banned`(应用层约束)
- `deleted_at` NOT NULL 时,`deleted_by` 可 NULL(用户主动注销)或 NOT NULL(管理员操作)

### 关系

- 一对多 → `charge_order.user_id`(一个用户可有多笔充电订单)
- 一对多 → `payment_order.user_id`(一个用户可有多笔支付订单,含充值)
- 一对一 → `wallet_account.user_id`(每个用户一个余额账户)
- 一对多 → `coupon_grant.user_id`(用户持有的多张优惠券)

### 业务规则

- **首次登录流程**:`code2Session` → 拿 `openid` → `INSERT ... ON DUPLICATE KEY UPDATE last_active_at = NOW()`(幂等)
- **手机号绑定流程(当前实现)**:小程序提交 `getPhoneNumber` 一次性凭证 → user 服务调用微信 `wxa/business/getuserphonenumber` → 只在服务端对返回号码计算 SHA-256 并写 `phone_hash`。不接收客户端手机号/哈希，不保存明文，当前不发放绑送奖励。部署 `0018_unique_phone_hash.sql` 前须核查历史重复哈希；重复值会使唯一索引迁移失败，需先让相关账号重新验证后再清理重复值。
- **注销流程**:`UPDATE user SET status='banned', banned_at=NOW(), banned_reason='user_request', phone_enc=NULL, unionid=NULL, nickname='', avatar_url=NULL, deleted_at=NOW(), deleted_by=NULL`;`openid` 保留用于 30 天审计追溯,30 天后 worker 物理归档
- **软删除查询规范**:所有查询经仓储层封装(`UserRepository::find_by_id($id)`),仓储内自动加 `WHERE deleted_at IS NULL`;直接 `SELECT *` 仅用于后台运维查询

---

## 表 2:`user_db.charge_order`

**业务说明**:**充电会话生命周期表**。一笔 `charge_order` = 用户一次完整的充电过程(扫码 → 选端口 → 微信支付回调 → 启动 → 充电中 → 结束 / 取消 / 失败)。**不含任何支付字段** —— 支付通过 `payment_order` 关联。
**核心变更(P0-1)**:`status` ENUM 新增 `pending_payment`(扫码选端口后,等待微信支付回调);`started_at` / `payment_order_id` 允许 NULL;`customer_id` 字段已移除(单客户单部署,客户级隔离由部署边界保证)。

**关键业务规则**:

- 状态机:`pending` → `pending_payment` → `charging` → `finished` / `cancelled` / `failed`(详见 `diagrams/charge-order.fsm.md`)
- 端口唯一约束:由不分区的 `active_port_charge.port_id` 主键实现;`charge_order` 按月分区,无法在分区表上建立不带分区列的跨月唯一键(见 [MySQL 8.4 分区唯一键约束](https://dev.mysql.com/doc/refman/8.4/en/partitioning-limitations-partitioning-keys-unique-keys.html))
- 估算订单:§ 6.5 B 方案的离线补传兜底,订单带 `billing_mode='estimated'` 标记
- 不存支付信息:`electric_fee_cents` / `service_fee_cents` / `paid_fee_cents` / 微信 transaction_id **全部移到 `payment_order`**

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `order_no` | `CHAR(32)` | UNIQUE, NOT NULL | — | 业务订单号,格式 `CH + YYYYMMDDHHmmss + 12 位随机`(用户侧展示"充电订单号") |
| `user_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `user.id` |
| `device_id` | `VARCHAR(32)` | NOT NULL | — | 充电桩设备 ID(§ 6.2 格式约定) |
| `port_id` | `VARCHAR(32)` | NOT NULL | — | 端口 ID(同一 device 下的物理插槽) |
| `vendor_id` | `VARCHAR(8)` | NOT NULL | — | 厂商 ID(2-4 字母前缀) |
| `station_id` | `BIGINT UNSIGNED` | NULL | NULL | 站点 ID(冗余自 admin_db,便于统计) |
| `charge_rule_id` | `BIGINT UNSIGNED` | NOT NULL | — | 使用的计费规则 ID(对应 admin_db 计费规则模板) |
| `payment_order_id` | `BIGINT UNSIGNED` | **NULL** | NULL | 关联 `payment_order.id`(扫码时未创建,NULL;支付回调后回填) |
| `started_at` | `DATETIME(3)` | **NULL** | NULL | 充电开始时间(`pending` / `pending_payment` 状态时 NULL;`charging` 时必填) |
| `ended_at` | `DATETIME(3)` | NULL | NULL | 充电结束时间(可能为 NULL 表示进行中) |
| `duration_seconds` | `INT UNSIGNED` | NULL | NULL | 充电时长(秒),结束回填 |
| `meter_kwh` | `DECIMAL(10,3)` | NULL | NULL | 实走表电量(kWh,精度 0.001) |
| `power_w` | `DECIMAL(10,2)` | NULL | NULL | 平均功率(W),结束回填 |
| `status` | `ENUM('pending','pending_payment','charging','finished','cancelled','failed')` | NOT NULL | — | 订单状态(P0-1 新增 `pending_payment`) |
| `billing_mode` | `ENUM('normal','estimated')` | NOT NULL | `'normal'` | 计费模式:normal 真实遥测 / estimated § 6.5 B 方案估算 |
| `actual_kwh` | `DECIMAL(10,3)` | NULL | NULL | **实际消耗电量**(kWh,需求 § 8.4 按已充结算字段) |
| `actual_fee_cents` | `BIGINT` | NULL | NULL | **实结费用**(分,= electric_fee + service_fee) |
| `refundable_cents` | `BIGINT` | NULL | NULL | **应退金额**(分,提前结束时 = paid_fee - actual_fee;正常结束 = 0) |
| `fail_reason` | `VARCHAR(256)` | NULL | NULL | 失败原因(`status=failed` 时填) |
| `cancel_reason` | `VARCHAR(256)` | NULL | NULL | 取消原因(`cancelled` 时填) |
| `cancel_initiator` | `ENUM('user','system','timeout')` | NULL | NULL | 取消发起方 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 订单创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间(NULL = 未删除) |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |
| `created_month` | `DATE` | GENERATED ALWAYS AS (DATE_FORMAT(`created_at`, '%Y-%m-01')) STORED | — | 分区键(MySQL 8.4 要求分区字段出现在每个 UNIQUE / PRIMARY KEY 中) |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_charge_order` | `id`, `created_month` | 主键 | MySQL 8.4 分区约束:分区字段必须出现在主键 |
| `uk_charge_order_no` | `order_no`, `created_month` | 唯一 | 用户查充电订单(分区字段必带) |
| `idx_charge_order_port_status` | `port_id`, `status` | 普通 | 查端口订单;当前占用唯一性由 `active_port_charge` 保证 |
| `idx_charge_order_payment_order` | `payment_order_id` | 普通 | 跨表查询 |
| `idx_charge_order_user_created` | `user_id`, `created_at` | 普通 | 用户充电订单列表 |
| `idx_charge_order_device_started` | `device_id`, `started_at` | 普通 | 设备维度订单查询 |
| `idx_charge_order_status_started` | `status`, `started_at` | 普通 | 状态筛选扫描 |
| `idx_charge_order_deleted_at` | `deleted_at` | 普通 | worker 物理归档扫描 |

### 分区策略

按 `created_month` 范围分区(滚动保留 36 个月):

```sql
PARTITION BY RANGE (TO_DAYS(created_month)) (
  PARTITION p2026m01 VALUES LESS THAN (TO_DAYS('2026-02-01')),
  PARTITION p2026m02 VALUES LESS THAN (TO_DAYS('2026-03-01')),
  ...
  PARTITION pmax VALUES LESS THAN MAXVALUE
);
```

由 `worker_db.data_retention` 周期任务每月 1 日滚动创建下月分区 + 删除超龄分区。

### 约束

- **状态机合法迁移**:仅允许以下迁移(应用层校验)
  - `pending` → `pending_payment` / `cancelled`
  - `pending_payment` → `charging` / `failed` / `cancelled`
  - `charging` → `finished` / `cancelled` / `failed`
- `status='charging'` 时,`started_at` / `payment_order_id` 必须 NOT NULL
- `status='finished'` 时,`ended_at` / `duration_seconds` / `meter_kwh` 必须 NOT NULL
- `billing_mode='estimated'` 时,`meter_kwh` 可为 NULL(§ 6.5 B 方案按功率估算)
- `deleted_at` NOT NULL 时,该订单不可再触发任何业务流程(状态冻结)

### 关系

- 多对一 → `user.id`
- 一对一 → 充电支付主单 `payment_order`(通过 `payment_order_id`;`/scan/start` 事务内创建支付单后回填)
- 多对一 → `admin_db.pricing_rule`(通过 `charge_rule_id`,跨服务,无外键)

### 业务规则

- **订单创建**(`status=pending_payment`):user 收到 `/scan/start` 请求 → 占逻辑锁 `charge:hold:port_xxx` → `INSERT charge_order(status='pending_payment', payment_order_id=NULL, started_at=NULL)` + `INSERT payment_order(status='initiated', biz_type='charge', biz_id=charge_order.id)` → `UPDATE charge_order.payment_order_id = payment_order.id` → 调微信 JSAPI 预下单 → **不启动设备**
- **订单启动**(`pending_payment` → `charging`):微信成功回调 → user 同事务写支付状态与 `event_outbox(charge_started_stream)` → gateway 消费并下发指令 → ACK 后调用 user 内部接口 → user 同事务插入 `active_port_charge` 并更新 `charge_order.status='charging'`。
- **订单结束**(`status=finished`):gateway 发布 `charge_ended_stream` → user 消费后写 `ended_at` / `meter_kwh` / `power_w` / `duration_seconds`,并按订单 ID 释放 `active_port_charge`。
- **触发退款**:启动失败、实结差额或取消后迟到支付时,user 在自己的事务内创建 `refund_record` 并写 `refund_required_stream` outbox;admin 经 user 内部接口领取并执行退款。`comp_tx_stream` 只记录退款完成结果。
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

> **P0-2 修正**:MySQL 8.4 要求**分区字段必须出现在每个 UNIQUE / PRIMARY KEY 中**,否则 `ERROR 1503`。
> 改造:`partition_key` DATE → `created_month` DATE(由 `created_at` 生成);主键 / 唯一键都加 `created_month`。

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `order_no` | `CHAR(32)` | NOT NULL | — | 业务支付单号,格式 `PY + YYYYMMDDHHmmss + 12 位随机` |
| `biz_type` | `ENUM('charge','recharge')` | NOT NULL | — | **业务类型**:`charge` 充电付款 / `recharge` 钱包充值 |
| `biz_id` | `BIGINT UNSIGNED` | NULL | NULL | **关联的业务订单 ID**:`biz_type='charge'` 时 = `charge_order.id`;`biz_type='recharge'` 时 = NULL |
| `user_id` | `BIGINT UNSIGNED` | NOT NULL | — | 付款用户 |
| `parent_order_id` | `BIGINT UNSIGNED` | NULL | NULL | **组合支付场景**:子单的父订单 ID(主单的 parent_order_id = NULL) |
| `pay_method` | `ENUM('wechat','wallet','mixed')` | NOT NULL | — | 支付方式:**主单** = `mixed`(组合);**子单** = `wechat` / `wallet`;**单独支付** = `wechat` / `wallet` |
| `pay_components` | `JSON` | NULL | NULL | **组合支付明细**(主单填,子单留空):JSON 数组,每个元素含 `channel`(`wechat`/`wallet`/`coupon`)、`amount_cents`、`channel_ref`(微信 transaction_id / wallet 流水 / coupon_grant_id) |
| `total_fee_cents` | `BIGINT` | NOT NULL | — | 支付总金额(分)。主单 = 组合金额合计;子单 = 本通道金额;单独支付 = 实际支付金额 |
| `discount_cents` | `BIGINT` | NOT NULL | `0` | 优惠抵扣(分,仅主单有值) |
| `paid_fee_cents` | `BIGINT` | NOT NULL | `0` | 实付金额(分,= total - discount) |
| `wechat_transaction_id` | `VARCHAR(64)` | NULL | NULL | 微信支付 transaction_id(仅 pay_method='wechat' 或组合中含微信时填;幂等键,§ 5.4) |
| `status` | `ENUM('initiated','success','failed','cancelled','refunded','partial_refunded')` | NOT NULL | `'initiated'` | 支付状态(P0-1 / P1-6 后续会统一到 payment.fsm.md 的状态集) |
| `fail_reason` | `VARCHAR(256)` | NULL | NULL | 失败原因(`status='failed'` 时填) |
| `paid_at` | `DATETIME(3)` | NULL | NULL | 支付完成时间(`status='success'` 时填) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `created_month` | `DATE` | GENERATED ALWAYS AS (DATE_FORMAT(`created_at`, '%Y-%m-01')) STORED | — | **P0-2 分区字段**(MySQL 8.4 强制要求出现在每个 UNIQUE / PRIMARY KEY) |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

> **P0-2 修正**:所有唯一 / 主键索引包含 `created_month`(MySQL 8.4 强制)。

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_payment_order` | `id`, `created_month` | 主键 | MySQL 8.4 分区约束:分区字段必须出现在主键 |
| `uk_payment_order_no` | `order_no`, `created_month` | 唯一 | 用户查支付单(分区字段必带) |
| `uk_payment_order_wechat_txn` | `wechat_transaction_id`, `created_month` | 唯一(可空) | 分区内防重复;跨月回调幂等以不分区的 `payment_callback_idempotent.wechat_transaction_id` 为准 |
| `idx_payment_order_biz` | `biz_type`, `biz_id` | 普通 | 反查"某笔充电的所有支付单" |
| `idx_payment_order_user_status` | `user_id`, `status`, `created_at` | 普通 | 我的支付订单列表 |
| `idx_payment_order_parent` | `parent_order_id` | 普通 | 查"主单的所有子单" |
| `idx_payment_order_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 分区策略

按 `created_month` 范围分区(滚动保留 36 个月,详见 `docs/技术规格.md` § 4.8):

```sql
PARTITION BY RANGE (TO_DAYS(created_month)) (
  PARTITION p2026m01 VALUES LESS THAN (TO_DAYS('2026-02-01')),
  ...
  PARTITION pmax VALUES LESS THAN MAXVALUE
);
```

### 约束

- **状态机合法迁移**(应用层校验):
  - `pending` → `success`(支付完成)/ `failed`(支付失败)/ `cancelled`(用户撤销)
  - `success` → `cancelled`(已支付后撤销,触发原路退款)
  - `failed` / `cancelled` → **终态**(不再迁移)
  - **不允许 `success` → `failed`**(避免审计混乱)
  - **子单状态独立迁移**:子单失败时,主单 status 仍可能为 `pending`(等所有子单结果后聚合)
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

## 表 5:user_db.refund_record

**业务说明**：原路退款记录，关联 user 自有的 payment_order，可按一张支付单分多笔退款。实际 schema 与 migrations/user_db/0001_init.sql 及 0013_refund_rejection.sql 一致。

### 字段定义

| 字段 | 类型 | 约束 / 默认值 | 说明 |
| --- | --- | --- | --- |
| id | BIGINT UNSIGNED | 自增 | 分区复合主键之一 |
| refund_no | VARCHAR(64) | 非空 | 退款业务编号 |
| payment_order_id | BIGINT UNSIGNED | 非空 | 关联 payment_order.id |
| user_id | BIGINT UNSIGNED | 非空 | 退款所属用户 |
| biz_type | ENUM('charge','wallet_recharge') | 非空 | 原支付业务类型 |
| biz_id | BIGINT UNSIGNED | 非空 | 原业务订单 ID |
| refund_cents | BIGINT | 非空 | 退款金额，单位分 |
| reason | VARCHAR(255) | 可空 | 申请原因 |
| status | ENUM('pending','processing','success','failed','rejected') | 非空，默认 pending | 0013 新增 rejected |
| retry_count | INT UNSIGNED | 非空，默认 0 | 已报告的退款失败次数 |
| wechat_refund_id | VARCHAR(64) | 可空 | 微信退款单号 |
| claimed_by | BIGINT UNSIGNED | 可空 | 管理员领取人 ID |
| claimed_at | DATETIME(3) | 可空 | 领取时间 |
| completed_at | DATETIME(3) | 可空 | 退款成功、失败或拒绝完成时间 |
| failure_reason | VARCHAR(255) | 可空 | 失败或拒绝原因 |
| created_month | DATE | 非空 | 显式分区键，写入时存月份首日 |
| created_at | DATETIME(3) | 非空，当前时间 | 创建时间 |
| updated_at | DATETIME(3) | 自动更新 | 修改时间 |
| deleted_at / deleted_by | DATETIME(3) / BIGINT UNSIGNED | 可空 | 软删除 |

### 索引与分区

- 主键：(id, created_month)。
- 唯一键：uk_refund_no(refund_no, created_month)。
- 普通索引：idx_payment(payment_order_id)、idx_status(status)。
- 按 created_month 范围分区，分区字段必须包含在所有主键/唯一键中。
- 不声明跨表外键；订单和用户关联由业务事务校验。

### 当前状态与流程

- 状态只包括 pending、processing、success、failed、rejected。waiting、retried、manual_review、settled 不属于当前表枚举；审核意见和拒绝凭据由 refund_review / refund_rejection 保存，自动执行阶段由 admin_db.refund_task 保存。
- user 在单个事务内创建退款记录和 refund_required_stream outbox；启动失败、实结退款、确认收款后订单失效、钱包充值原路退款和双签通过后的退款均走此路径。
- admin 消费退款事件并调用 user 内部领取接口，将记录从 pending 置为 processing；同一 refund_no 的微信提交、查询和回报进度由 admin 自有 refund_task 续跑。
- user 的微信退款回调/内部结果接口在事务内校验支付和退款快照。成功时更新 refund_record、payment_order，钱包退款还会结算预留金额；失败时更新 failure_reason 和 retry_count。
- 每次已确认的成功或失败结果同事务写 event_outbox(refund_completed)，由 worker 幂等审计至 worker_db.comp_tx_log。它不执行资金补偿，也不更新 billing。
- 充电退款可以由两名不同财务账号分别审核；审核回执存于 refund_review。拒绝写 refund_rejection 并把退款记录置为 rejected，不会产生执行事件。
- 钱包退款风险审核/资金预留由 wallet_refund_request 等专属表负责，不会把不存在的 manual_review 状态写入此表。
## 表 6:`user_db.coupon_grant`

**业务说明**:用户持有的优惠券发放实例。真实表结构以 `migrations/user_db/0001_init.sql` 和后续迁移为准；当前已实现运营发券、额度核验和幂等回执。

### 当前字段

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | 发放记录 ID |
| `coupon_id` | `BIGINT UNSIGNED` | NOT NULL | `coupon.id` 模板 |
| `user_id` | `BIGINT UNSIGNED` | NOT NULL | 持有人 |
| `grant_source` | ENUM | NOT NULL | `register/activity/invite/manual/invite_reward` |
| `status` | ENUM | NOT NULL DEFAULT `unused` | `unused/used/expired` |
| `used_payment_order_id` | `BIGINT UNSIGNED` | NULL | 使用时关联支付单 |
| `used_at` | `DATETIME(3)` | NULL | 使用时间 |
| `expired_at` | `DATETIME(3)` | NOT NULL | 发放实例到期时间 |
| `source_event_id` | `CHAR(36)` | NULL, UNIQUE | 活动 Stream event_id 幂等键(user_db/0020) |
| `created_at` | `DATETIME(3)` | NOT NULL | 发放时间 |
| `deleted_at` | `DATETIME(3)` | NULL | 软删除时间 |

现有索引为 `idx_user_status(user_id,status)`、`idx_coupon(coupon_id)`，迁移 `0020_coupon_grant_idempotency.sql` 新增唯一 `source_event_id`。

### 当前发放规则

- 管理员发券经 admin → user 内部 API；user 在单个事务中锁优惠券模板、核对有效用户、模板状态/有效时间、总发放额度与个人额度，再插入券和 `coupon_grant_request` 回执。
- `coupon_grant_request.request_id` 是 UUID 主键并保存模板、用户和券 ID。同请求同参数返回原券；相同 UUID 改绑不同模板或用户会冲突。
- 活动 Stream 消费使用 envelope `event_id` 作为 request_id 和 `source_event_id`。无效事件、数据库故障或额度耗尽不 ACK 成功，由消费框架重试并进入 DLQ。
- 当前尚未接通扫码结算核销；`used` 状态目前没有订单付款路径写入。会员/钱包/优惠券组合支付和优惠券抵扣也未实现。

---
## 表 7:`user_db.refund_reconcile_diff`

**业务说明**:**每日对账差异记录**(微信账单 vs 内部 `refund_record`)。每日 03:00 worker 拉微信退款账单,与 `refund_record` 对比,差异入表 + 告警。

**关键业务规则**:

- 每日对账,覆盖昨日全部退款
- 4 种差异类型,严重程度不同:`missing_internal` 最严重(钱可能漏记账)
- 差异入表后**必须**人工处理,不能自动修复
- 软删除:差异处理完成后软删,保留审计追溯

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `reconcile_date` | `DATE` | NOT NULL | — | 对账日期(对账的是哪天的数据) |
| `wechat_refund_id` | `VARCHAR(64)` | NULL | NULL | 微信退款单号(微信账单侧) |
| `wechat_amount_cents` | `BIGINT` | NULL | NULL | 微信账单金额(分) |
| `wechat_status` | `VARCHAR(32)` | NULL | NULL | 微信账单退款状态(SUCCESS / PROCESSING / CLOSED 等) |
| `internal_refund_id` | `BIGINT UNSIGNED` | NULL | NULL | 内部 `refund_record.id`(系统侧) |
| `internal_amount_cents` | `BIGINT` | NULL | NULL | 内部 `refund_record.refund_cents` |
| `internal_status` | `ENUM('pending','retrying','success','failed','manual_review')` | NULL | NULL | 内部退款状态 |
| `diff_type` | `ENUM('missing_internal','missing_wechat','amount_mismatch','status_mismatch')` | NOT NULL | — | **差异类型**:`missing_internal` 微信退了但系统无记录(最严重)/ `missing_wechat` 系统有但微信无(可能重试中)/ `amount_mismatch` 金额不一致 / `status_mismatch` 状态不一致 |
| `severity` | `ENUM('critical','high','medium','low')` | NOT NULL | — | 严重程度,critical = `missing_internal`,其他按情况 |
| `resolved` | `BOOLEAN` | NOT NULL | `FALSE` | 是否已处理 |
| `resolved_by` | `BIGINT UNSIGNED` | NULL | NULL | 处理人(客户财务 user_id) |
| `resolved_at` | `DATETIME(3)` | NULL | NULL | 处理时间 |
| `resolved_note` | `VARCHAR(512)` | NULL | NULL | 处理备注 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_refund_reconcile_diff` | `id` | 主键 | — |
| `idx_refund_reconcile_diff_date_resolved` | `reconcile_date`, `resolved` | 普通 | 客户财务查未处理差异列表 |
| `idx_refund_reconcile_diff_type_severity` | `diff_type`, `severity`, `resolved` | 普通 | 按类型筛选(优先处理 critical) |
| `idx_refund_reconcile_diff_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `resolved=TRUE` 时,`resolved_by` / `resolved_at` / `resolved_note` 必须 NOT NULL
- `diff_type='missing_internal'` 时,`wechat_refund_id` NOT NULL,`internal_refund_id` NULL
- `diff_type='missing_wechat'` 时,`internal_refund_id` NOT NULL,`wechat_refund_id` NULL
- `diff_type IN ('amount_mismatch','status_mismatch')` 时,两个 id 都不能 NULL

### 关系

- 可选关联 → `refund_record.id`(差异类型含 internal 时)
- 不关联 `payment_order` / `charge_order`(只看退款本身)

### 业务规则

- **触发**:worker 每日 03:00 拉昨日微信退款账单(微信 `/v3/merchant/fund/refund/out-bill-no` 接口)
- **对比**:逐笔与 `refund_record` 按 `wechat_refund_id` 关联对比 → 不一致即写入
- **告警**:`severity='critical'` 立即触发 Webhook + 短信(若客户配置)→ 客户财务;其他类型次日 PC 后台提醒
- **处理**:客户财务在 admin PC 后台"对账差异"页面查未处理项 → 选处理方式(补记 / 联系微信客服 / 标记为已知)+ 填备注 → UPDATE `resolved=TRUE`
- **不自动修复**:差异是资金问题,任何自动修复都可能放大错误,**只人工处理**

---

## 表 8:user_db.risk_freeze_log

**业务说明**：钱包退款申请达到频次门槛时记录冻结原因。字段对应 migrations/user_db/0001_init.sql；它不关联 refund_record，也不保存审核决定。

### 字段定义

| 字段 | 类型 | 约束 / 默认值 | 说明 |
| --- | --- | --- | --- |
| id | BIGINT UNSIGNED | 主键、自增 | 冻结记录 ID |
| user_id | BIGINT UNSIGNED | 非空 | 被冻结用户 |
| trigger_rule | VARCHAR(64) | 非空 | 当前规则值为 wallet_refund_frequency |
| frozen_action | VARCHAR(64) | 非空 | 当前动作为 wallet_refund |
| reason | VARCHAR(255) | 可空 | 冻结原因 |
| window_minutes | INT UNSIGNED | 可空 | 统计窗口分钟数 |
| threshold_value | INT UNSIGNED | 可空 | 触发阈值 |
| actual_value | INT UNSIGNED | 可空 | 实际触发值；当前写入未提供 |
| unfreeze_at | DATETIME(3) | 可空 | 解冻时间 |
| status | ENUM('frozen','unfrozen') | 非空，默认 frozen | 此冻结原因是否仍有效 |
| created_at | DATETIME(3) | 非空，当前时间 | 创建时间 |

索引：主键 id；idx_user_status(user_id, status)。无软删除字段、无 freeze_type/refund_record_id，也没有金额阈值规则。

### 当前钱包退款风控流程

- wallet_refund_request 保存幂等申请与响应；5 分钟窗口内第三次申请触发 wallet 状态冻结，同时创建 risk_freeze_log 与 wallet_risk_freeze_link。
- 待审核状态存储在 wallet_refund_request.response_json；审核意见和决定保存在 wallet_risk_review。审核通过会按原充值支付记录拆分退款并写 refund_required_stream；拒绝则记录终态，不新建退款执行事件。
- 解冻单独由 wallet_risk_release 记录操作者、原因和响应；user 只解除与此申请关联的冻结记录，并保留其他仍有效的冻结原因。
- 该风控流程针对钱包退款申请；它不把 refund_record.status 写成 manual_review。refund_record 当前有效状态见表 5。
## 表 9:`user_db.wallet_txn`

**业务说明**:**余额流水**。每次 `wallet_account` 余额变动都同步写一条流水,用于对账、审计、查询"我的余额明细"。

**关键业务规则**:

- **流水类型**:`recharge`(充值入账)/ `consume`(消费扣减)/ `refund`(退款入账)/ `freeze`(支付冻结)/ `unfreeze`(冻结回退)/ `admin_adjust`(管理员调整)
- **必须事务**:余额变动 + 流水写入在同一事务,严禁只改余额不写流水
- **金额守恒**:`SUM(amount_cents) WHERE txn_type IN ('recharge','refund') - WHERE txn_type IN ('consume') = wallet_account.available_cents + wallet_account.frozen_cents`
- 软删除启用:异常流水(测试数据 / 误操作)由客户财务软删

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `txn_no` | `CHAR(32)` | UNIQUE, NOT NULL | — | 业务流水号,格式 `WT + YYYYMMDDHHmmss + 12 位随机` |
| `account_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `wallet_account.id` |
| `user_id` | `BIGINT UNSIGNED` | NOT NULL | — | 冗余,便于查询 |
| `txn_type` | `ENUM('recharge','consume','refund','freeze','unfreeze','admin_adjust')` | NOT NULL | — | 流水类型 |
| `amount_cents` | `BIGINT` | NOT NULL | — | 变动金额(分,**正负值**,recharge/refund/unfreeze 为正,consume/freeze 为负) |
| `balance_after_cents` | `BIGINT` | NOT NULL | — | **变动后余额快照**(便于审计,无需再 JOIN wallet_account) |
| `related_payment_order_id` | `BIGINT UNSIGNED` | NULL | NULL | 关联 `payment_order.id`(recharge/consume/freeze 时填) |
| `related_refund_id` | `BIGINT UNSIGNED` | NULL | NULL | 关联 `refund_record.id`(refund 时填) |
| `related_charge_order_id` | `BIGINT UNSIGNED` | NULL | NULL | 关联 `charge_order.id`(consume 时填,便于追溯"哪笔充电花了多少") |
| `remark` | `VARCHAR(256)` | NULL | NULL | 备注(admin_adjust 时必填,如"客户投诉补偿 50 元") |
| `operator_id` | `BIGINT UNSIGNED` | NULL | NULL | 操作者(admin_adjust 时填,记录管理员 ID;自动类型为 NULL) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 流水时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |
| `created_month` | `DATE` | GENERATED ALWAYS AS (DATE_FORMAT(`created_at`, '%Y-%m-01')) STORED | — | **P0-2 分区字段** |

### 索引

> **P0-2 修正**:按月分区表所有唯一 / 主键索引必须包含 `created_month`。

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_wallet_txn` | `id`, `created_month` | 主键 | MySQL 8.4 分区约束 |
| `uk_wallet_txn_no` | `txn_no`, `created_month` | 唯一 | 单条流水追溯(分区字段必带) |
| `idx_wallet_txn_account_created` | `account_id`, `created_at` | 普通 | 用户"我的余额明细"列表 |
| `idx_wallet_txn_user_type` | `user_id`, `txn_type`, `created_at` | 普通 | 按类型筛选 |
| `idx_wallet_txn_payment_order` | `related_payment_order_id` | 普通(可空) | 反查"某笔支付触发的所有流水" |
| `idx_wallet_txn_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 分区策略

按 `created_month` 范围分区:

```sql
PARTITION BY RANGE (TO_DAYS(created_month)) (
  PARTITION p2026m01 VALUES LESS THAN (TO_DAYS('2026-02-01')),
  ...
  PARTITION pmax VALUES LESS THAN MAXVALUE
);
```

### 约束

- `txn_type IN ('recharge','refund','unfreeze')` 时,`amount_cents > 0`
- `txn_type IN ('consume','freeze')` 时,`amount_cents < 0`
- `txn_type='admin_adjust'` 时,`amount_cents` 可正可负,`operator_id` 必须 NOT NULL,`remark` 必须 NOT NULL
- `txn_type='recharge'` 时,`related_payment_order_id` NOT NULL(关联 `biz_type='recharge'` 的 payment_order)
- `txn_type='refund'` 时,`related_refund_id` NOT NULL

### 关系

- 多对一 → `wallet_account.id`
- 多对一 → `user.id`(冗余)
- 可选关联 → `payment_order.id` / `refund_record.id` / `charge_order.id`(按类型填)

### 业务规则

- **充值流水**:`payment_order(biz_type='recharge')` 微信回调成功 → 事务内 UPDATE `wallet_account.available_cents += X` + INSERT `wallet_txn(txn_type='recharge', amount_cents=+X, balance_after, related_payment_order_id, remark='钱包充值')`
- **消费冻结**(组合支付扣余额):事务内 UPDATE `wallet_account.available_cents -= X, frozen_cents += X` + INSERT `wallet_txn(txn_type='freeze', amount_cents=-X, balance_after, related_payment_order_id, related_charge_order_id, remark='充电消费冻结')`
- **冻结转扣减**(订单结束扣款成功):UPDATE `wallet_account.frozen_cents -= X, total_consumed += X` + INSERT `wallet_txn(txn_type='consume', amount_cents=-X, balance_after, related_charge_order_id, remark='充电消费扣款')`
- **冻结回退**(支付失败):UPDATE `wallet_account.frozen_cents -= X` + INSERT `wallet_txn(txn_type='unfreeze', amount_cents=+X, balance_after, remark='冻结回退')`
- **退款入账**:`refund_record.status='success'` → UPDATE `wallet_account.available_cents += X, total_refunded += X` + INSERT `wallet_txn(txn_type='refund', amount_cents=+X, balance_after, related_refund_id, remark='退款入账')`
- **管理员调整**:客户财务在 PC 后台填金额 + 备注 → 事务内 UPDATE wallet_account + INSERT `wallet_txn(txn_type='admin_adjust', operator_id, remark='客户投诉补偿 50 元')`,**强制审计日志**(`audit_log` 表同时记录)

---

## 表 10:`user_db.coupon`

**业务说明**:用户服务拥有的优惠券模板。admin 页面不得直接查询或修改本表，必须通过 user 内部 API。

### 当前字段

| 字段 | 类型 | 默认/约束 | 说明 |
| --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | 模板 ID |
| `code` | `VARCHAR(64)` | NOT NULL, UNIQUE WITH `deleted_at` | 业务编码 |
| `name` | `VARCHAR(128)` | NOT NULL | 用户可见名称 |
| `name_i18n` | JSON | NULL | 多语言预留 |
| `discount_type` | ENUM | NOT NULL | `amount/percentage/time_free` |
| `discount_value_cents` | BIGINT | NULL | 固定金额折扣(分) |
| `discount_percent` | DECIMAL(5,2) | NULL | 百分比折扣(0–100) |
| `min_charge_cents` | BIGINT | NOT NULL DEFAULT 0 | 最低消费门槛 |
| `valid_hours` | `INT UNSIGNED` | NOT NULL DEFAULT 24 | 领取后的最长有效小时数 |
| `total_quota` | `INT UNSIGNED` | NOT NULL DEFAULT 0 | 总发放量，0 表示不限 |
| `per_user_quota` | `INT UNSIGNED` | NOT NULL DEFAULT 1 | 每用户最多发放数 |
| `status` | ENUM | NOT NULL DEFAULT `active` | `active/disabled` |
| `start_at/end_at` | `DATETIME(3)` | NULL | 可选模板发放时间边界 |
| `created_at/updated_at` | `DATETIME(3)` | NOT NULL | 创建/更新时间 |
| `deleted_at/deleted_by` | DATETIME/BIGINT | NULL | 软删除信息 |

唯一键为 `uk_code(code,deleted_at)`。模板折扣字段按 `discount_type` 互斥校验。创建时 valid_hours 为 1–8760，额度不可为负；删除已有发放实例的模板会拒绝，运营应停用。

### 当前操作与未完成项

- admin 的模板列表、创建、详情、名称/状态/结束时间更新、软删除及统计都经 user 内部 API，不跨 schema 访问。
- `coupon.grant` 权限保护的人工发券端点会校验模板状态/时窗和总量/个人额度。统计从 user_db 实例实时汇总，过期但尚未清理的券计入 expired。
- 当前没有扫码付款抵扣、会员卡组合付款、指定站点/设备适用范围或自动活动规则引擎；`time_free` 仅存模板/可预览，未作用于结算。

---
## 表 11:`user_db.membership_card`

**业务说明**:**会员卡**(本期预留,数据可能为空)。二期扩展场景:用户购买月度 / 年度会员,享折扣 + 优先客服。

**关键业务规则**:

- 本期**预留 schema**,实际功能二期实施
- 会员卡 = 用户付费购买的时间段权限(可叠加优惠券)
- 软删除启用:会员过期不删,只标 `status='expired'`

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `card_no` | `CHAR(32)` | UNIQUE, NOT NULL | — | 会员卡号,格式 `MC + YYYYMMDD + 8 位随机` |
| `user_id` | `BIGINT UNSIGNED` | NOT NULL | — | 持卡人 |

| `card_type` | `ENUM('monthly','quarterly','yearly')` | NOT NULL | — | 会员类型:月卡 / 季卡 / 年卡 |
| `price_cents` | `BIGINT` | NOT NULL | — | 购买价格(分) |
| `discount_percent` | `DECIMAL(5,2)` | NULL | NULL | 充电折扣(如 90 = 9 折),NULL = 无折扣 |
| `valid_from` | `DATETIME(3)` | NOT NULL | — | 生效时间 |
| `valid_until` | `DATETIME(3)` | NOT NULL | — | 失效时间 |
| `auto_renew` | `BOOLEAN` | NOT NULL | `FALSE` | 是否自动续费(二期) |
| `status` | `ENUM('active','expired','cancelled')` | NOT NULL | `'active'` | 状态:active 有效 / expired 过期 / cancelled 取消 |
| `purchase_payment_order_id` | `BIGINT UNSIGNED` | NULL | NULL | 购买时的支付订单 ID(`payment_order.biz_type='membership'`,二期填) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间(购买时间) |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_membership_card` | `id` | 主键 | — |
| `uk_membership_card_no` | `card_no` | 唯一 | 卡号追溯 |
| `idx_membership_card_user_status` | `user_id`, `status`, `valid_until` | 普通 | 用户"我的会员卡"列表 |

| `idx_membership_card_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `valid_until > valid_from`(应用层校验)
- `status='cancelled'` 时,该卡立即失效,即使 `valid_until` 未到
- `status='expired'` 时,`valid_until < NOW()`(应用层校验或 worker 任务标)

### 关系

- 多对一 → `user.id`

- 多对一 → `payment_order.id`(购买订单,二期填)

### 业务规则

- **本期**:此表无业务逻辑,仅 schema 占位;客户运营可在 admin PC 后台手动 INSERT 测试数据
- **二期触发**:
  1. 客户合同要求会员功能
  2. 业务引入会员体系
  3. 用户反馈"希望长期打折"
- **二期流程**(预留):用户支付 → INSERT `membership_card(status='active')` + INSERT `payment_order(biz_type='membership', biz_id=card.id)`;充电下单时校验有效会员 → 自动应用 `discount_percent`

---

## 表 12:`user_db.invoice_request`

**业务说明**:**发票申请记录**。用户提交发票申请(抬头 + 税号 + 邮箱),客户财务在 admin PC 后台人工审核,通过后生成电子发票 + 邮件发送。

**关键业务规则**:

- **人工审核**:需求文档已定,客户财务审核,不自动开票
- **关联支付订单**:可申请一张发票包含多笔 payment_order(累计开票)
- **电子发票 PDF**:开票后上传 OSS,URL 存 `invoice_file_url`
- 软删除启用:异常申请 / 用户撤回可软删

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `request_no` | `CHAR(32)` | UNIQUE, NOT NULL | — | 申请单号,格式 `INV + YYYYMMDD + 10 位随机` |
| `user_id` | `BIGINT UNSIGNED` | NOT NULL | — | 申请人 |

| `invoice_type` | `ENUM('personal','company')` | NOT NULL | — | 发票类型:个人 / 企业 |
| `title` | `VARCHAR(128)` | NOT NULL | — | 发票抬头(个人填姓名,企业填公司名) |
| `tax_id` | `VARCHAR(32)` | NULL | NULL | 税号(企业必填,个人可不填) |
| `email` | `VARCHAR(128)` | NOT NULL | — | 接收发票的邮箱 |
| `amount_cents` | `BIGINT` | NOT NULL | — | 申请开票金额(分) |
| `related_payment_order_ids` | `JSON` | NOT NULL | — | 关联的支付订单 ID 列表(JSON 数组,支持一张发票包含多笔订单) |
| `status` | `ENUM('pending','approved','rejected','issued','failed')` | NOT NULL | `'pending'` | 状态:待审 / 已通过 / 已拒绝 / 已开票 / 开票失败 |
| `reviewed_by` | `BIGINT UNSIGNED` | NULL | NULL | 审核人(客户财务 user_id) |
| `reviewed_at` | `DATETIME(3)` | NULL | NULL | 审核时间 |
| `review_note` | `VARCHAR(512)` | NULL | NULL | 审核备注(拒绝时必填) |
| `invoice_file_url` | `VARCHAR(512)` | NULL | NULL | 电子发票 PDF 的 OSS URL(`status='issued'` 时填) |
| `invoice_no` | `VARCHAR(64)` | NULL | NULL | 发票号码(税务局系统分配) |
| `issued_at` | `DATETIME(3)` | NULL | NULL | 开票时间 |
| `email_sent_at` | `DATETIME(3)` | NULL | NULL | 邮件发送时间 |
| `email_send_fail_count` | `TINYINT UNSIGNED` | NOT NULL | `0` | 邮件发送失败次数(> 0 时人工跟进) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 申请时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_invoice_request` | `id` | 主键 | — |
| `uk_invoice_request_no` | `request_no` | 唯一 | 申请单号追溯 |
| `idx_invoice_request_user_status` | `user_id`, `status`, `created_at` | 普通 | 我的发票申请列表 |

| `idx_invoice_request_status_reviewed` | `status`, `reviewed_at` | 普通 | 查"已通过未开票"的工单 |
| `idx_invoice_request_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `invoice_type='company'` 时,`tax_id` NOT NULL(应用层校验)
- `status='approved'` 时,`reviewed_by` / `reviewed_at` NOT NULL
- `status='rejected'` 时,`reviewed_by` / `reviewed_at` / `review_note` NOT NULL
- `status='issued'` 时,`invoice_file_url` / `invoice_no` / `issued_at` / `email_sent_at` NOT NULL
- `status='failed'` 时,`email_send_fail_count > 0`
- 关联的支付订单 `SUM(paid_fee_cents) = amount_cents`(应用层校验)

### 关系

- 多对一 → `user.id`

- 多对多 → `payment_order.id`(通过 `related_payment_order_ids` JSON 数组,跨服务无外键)

### 业务规则

- **申请**:小程序"我的 → 申请发票"选要开票的支付订单(可多选累计)→ 填抬头/税号/邮箱 → 提交 → INSERT `invoice_request(status='pending')`
- **金额校验**:系统自动校验所选订单 `SUM(paid_fee_cents)` = 用户填的 `amount_cents`(防误填);不一致则提示用户
- **审核**:客户财务在 admin PC 后台"发票管理 → 待审核"队列 → 校验抬头 / 税号格式 → 通过 / 拒绝 + 备注 → UPDATE `status='approved'/'rejected'`
- **开票**:审核通过后,客户财务在 admin PC 后台"开票"操作(对接客户税务系统 / 第三方电子发票平台,如"票易通")→ 生成 PDF → 上传 OSS → 写 `invoice_file_url` + `invoice_no` → 发邮件 → UPDATE `status='issued', issued_at, email_sent_at`
- **邮件失败重试**:邮件发送失败 → `email_send_fail_count += 1` + 写 `alert_stream` + 客户财务人工跟进(查看 OSS URL 手动转发)
- **撤回**:用户申请后未审核前可撤回 → UPDATE `status='cancelled'`(实际不在 status 枚举中,改为 `deleted_at` 软删 + status='rejected' + review_note='用户撤回')

---

## 表 13:`user_db.port_view`

**业务说明**:**找桩缓存**(冗余自 `gateway_db.device` / `gateway_db.port`,加速用户"找桩 / 地图"查询)。`gateway_db` 写主表,`user_db.port_view` 是只读缓存,由 worker 周期任务同步。

**关键业务规则**:

- **缓存性质**:不存真实状态(空闲 / 充电中),只存静态信息(站点 / 经纬度 / 端口数)
- **不软删除**:缓存数据陈旧时由 worker 覆盖更新,无需软删
- **数据来源**:admin 配置站点 → 写入 admin_db;worker 从 admin_db + gateway_db 同步到 user_db.port_view

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `port_id` | `VARCHAR(32)` | UNIQUE, NOT NULL | — | 端口 ID(冗余自 gateway_db) |
| `device_id` | `VARCHAR(32)` | NOT NULL | — | 设备 ID |
| `vendor_id` | `VARCHAR(8)` | NOT NULL | — | 厂商 ID |
| `station_id` | `BIGINT UNSIGNED` | NOT NULL | — | 所属站点 |
| `station_name` | `VARCHAR(128)` | NOT NULL | — | 站点名称(冗余,避免 JOIN) |
| `longitude` | `DECIMAL(10,6)` | NOT NULL | — | 经度(精度 6 位 ≈ 0.1 m) |
| `latitude` | `DECIMAL(10,6)` | NOT NULL | — | 纬度 |
| `address` | `VARCHAR(256)` | NULL | NULL | 详细地址 |
| `total_ports` | `TINYINT UNSIGNED` | NOT NULL | — | 设备总端口数 |
| `online` | `BOOLEAN` | NOT NULL | `FALSE` | 设备是否在线(worker 周期从 gateway_db 同步) |
| `last_sync_at` | `DATETIME(3)` | NOT NULL | — | 最近同步时间 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_port_view` | `id` | 主键 | — |
| `uk_port_view_port_id` | `port_id` | 唯一 | 按端口 ID 查 |
| `idx_port_view_station` | `station_id` | 普通 | 按站点查 |
| `idx_port_view_geo` | `longitude`, `latitude` | 普通 | 地理范围查询(找桩附近) |
| `idx_port_view_online_sync` | `online`, `last_sync_at` | 普通 | 找"需要重新同步"的端口 |

### 约束

- `longitude` 范围 `-180.000000` ~ `180.000000`,`latitude` 范围 `-90.000000` ~ `90.000000`(应用层校验)
- `online=FALSE` 且 `NOW() - last_sync_at > 1 HOUR` → 标记"可能离线"

### 关系

- 多对一 → `admin_db.station.id`(跨服务,无外键)
- 多对一 → `gateway_db.device`(跨服务,无外键)

### 业务规则

- **同步触发**:worker 每 5 min 跑一次 → 查 `gateway_db.device` 状态 + `admin_db.station` 元数据 → UPSERT `user_db.port_view`
- **找桩查询**:小程序"找桩"页 → user 查 `port_view`(`online=TRUE` + 按距离排序)→ 直接展示,无需跨服务
- **实时状态**:端口"空闲 / 充电中"状态不缓存,需要时由小程序扫码后 user 调 `gateway` HTTP 接口查实时
- **过期清理**:`port_view` 数据仅作缓存,源数据删除时由 worker 自动覆盖;无需软删除

---

## 表 14:`user_db.payment_callback_idempotent`

**业务说明**:**微信支付回调幂等表**(§ 5.4 微信支付回调幂等维度)。按微信 `transaction_id` 去重,**与 Redis Stream 的 `event_id` 幂等是正交两套**。

**关键业务规则**:

- **不软删除**:幂等表是日志性质,30 天后物理清理
- **唯一键**:`wechat_transaction_id`(微信交易号,同一笔不会被记录两次)
- **保留期**:30 天(微信退款有效期 1 年,但幂等只需覆盖重复推送窗口 5 min + 余量)

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `wechat_transaction_id` | `VARCHAR(64)` | UNIQUE, NOT NULL | — | 微信 transaction_id(幂等键) |
| `payment_order_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `payment_order.id`(第一次处理时记录) |
| `wechat_amount_cents` | `BIGINT` | NOT NULL | — | 微信回调金额(分) |
| `processed_at` | `DATETIME(3)` | NOT NULL | — | 处理时间(首次写入时间) |
| `wechat_raw_payload` | `JSON` | NULL | NULL | 微信回调原始 payload(便于排查) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_payment_callback_idempotent` | `id` | 主键 | — |
| `uk_payment_callback_idempotent_txn` | `wechat_transaction_id` | 唯一 | 幂等去重(微信回调重复推送时直接返回 200 OK) |
| `idx_payment_callback_idempotent_created` | `created_at` | 普通 | worker 周期清理 30 天前记录 |

### 约束

- `wechat_amount_cents > 0`(应用层校验)
- 唯一约束保证同一 `wechat_transaction_id` 不会写入两条(数据库层面)

### 关系

- 多对一 → `payment_order.id`

### 业务规则

- **写入**:`POST /api/v1/public/payment/wechat/callback` 验签后按 `out_trade_no` 定位支付单;首次成功回调在同一事务内 INSERT 本表、更新支付单并写 `event_outbox`;重复回调不重复入账,但必须保留待发布 outbox 供发布器重试
- **清理**:worker 每日 04:00 跑 `DELETE FROM payment_callback_idempotent WHERE created_at < NOW() - 30 DAY`(物理删除,不软删)
- **不存敏感信息**:`wechat_raw_payload` 仅保留必要字段(支付单号/金额/时间),不存用户敏感数据

---

**user_db 全部 14 张表设计完成**

> 文件结构:`通用约定` → `表清单(14 张)` → `关键架构决策` → 表 1 ~ 表 14 → `本批次结束`。
> 第二批新增 6 张支撑型表已覆盖余额流水 / 优惠券模板 / 会员卡预留 / 发票申请 / 找桩缓存 / 微信支付幂等的全部业务场景。
>
> **下一文件**:`docs/db/admin.md`(admin_db,客户管理 / 角色权限 / 白标 / 公告 / Webhook / 财务审核 / 审计日志)。

---

# 第三批:2 张用户交互表(API 补漏)

## 表 15:`user_db.feedback`

**业务说明**:**评价 / 投诉记录**。用户在充电结束页对本次体验评分 + 文字反馈(需求 § 5.3)。**每笔订单仅能评价一次**(唯一约束)。

**关键业务规则**:

- `is_complaint=TRUE` → 客户运营重点跟进 + 推 `alert_stream` 事件
- `contact_back=TRUE` → 写入客服待回访队列
- **每笔订单唯一评价**:`uk_feedback_order_user` 唯一索引防重复
- 软删除启用:误操作可软删,保留审计

### 字段定义

> **P0-2 修正**:按月分区表 → 主键 / 唯一键加 `created_month` generated column(MySQL 8.4 强制要求分区字段出现在每个 UNIQUE / PRIMARY KEY)。

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `feedback_no` | `CHAR(32)` | NOT NULL | — | 业务评价单号,格式 `FB + YYYYMMDD + 10 位随机` |
| `user_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `user.id` |
| `order_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `charge_order.id` |
| `rating` | `TINYINT UNSIGNED` | NOT NULL | — | 评分 1-5(1 = 投诉最差 / 5 = 最佳) |
| `comment` | `TEXT` | NULL | NULL | 文字评论(选填) |
| `category` | `ENUM('experience','device','fee','speed','other')` | NOT NULL | — | 评价类别 |
| `is_complaint` | `BOOLEAN` | NOT NULL | `FALSE` | **是否投诉** |
| `contact_back` | `BOOLEAN` | NOT NULL | `FALSE` | **是否希望客服回复** |
| `status` | `ENUM('pending','reviewed','closed')` | NOT NULL | `'pending'` | 状态:待处理 / 已回复 / 已关闭 |
| `reviewed_by` | `BIGINT UNSIGNED` | NULL | NULL | 处理人 |
| `reviewed_at` | `DATETIME(3)` | NULL | NULL | 处理时间 |
| `reply_comment` | `TEXT` | NULL | NULL | 客服回复内容 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `created_month` | `DATE` | GENERATED ALWAYS AS (DATE_FORMAT(`created_at`, '%Y-%m-01')) STORED | — | **P0-2 分区字段** |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_feedback` | `id`, `created_month` | 主键 | MySQL 8.4 分区约束 |
| `uk_feedback_no` | `feedback_no`, `created_month` | 唯一 | 单号追溯(分区字段必带) |
| **`uk_feedback_order_user`** | `order_id`, `user_id`, `created_month` | **唯一** | **每笔订单每用户仅一次评价**(分区字段必带) |
| `idx_feedback_user_created` | `user_id`, `created_at` | 普通 | 用户历史评价查询 |
| `idx_feedback_complaint_status` | `is_complaint`, `status`, `created_at` | 普通 | 客户运营查投诉队列 |
| `idx_feedback_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 分区策略

按 `created_month` 范围分区(滚动保留 36 个月):

```sql
PARTITION BY RANGE (TO_DAYS(created_month)) (
  PARTITION p2026m01 VALUES LESS THAN (TO_DAYS('2026-02-01')),
  ...
  PARTITION pmax VALUES LESS THAN MAXVALUE
);
```

### 约束

- `rating` 范围 1-5(应用层校验)
- 同一 `(order_id, user_id)` 唯一(防重复评价)
- `status='reviewed'` 时,`reviewed_by` / `reviewed_at` NOT NULL
- `contact_back=TRUE` 且 `status='reviewed'` 时,`reply_comment` NOT NULL

### 关系

- 多对一 → `user.id`
- 多对一 → `charge_order.id`(跨服务逻辑关联)

### 业务规则

- **创建**:用户提交评价 → 校验订单属于当前 user + 已 finished + 未评价过 → INSERT `feedback(status='pending', is_complaint, contact_back)`
- **触发告警**:`is_complaint=TRUE` → 推 `alert_stream`(`severity='mid'`)+ 推送客户运营 PC 后台
- **客服回复**:`contact_back=TRUE` 评价 → 客服 PC 后台回复 → UPDATE `status='reviewed', reply_comment`
- **唯一性**:DB 唯一约束防重复评价(应用层先查,DB 层兜底)

---

## 表 16:`user_db.device_fault_report`

> 当前代码已接入巡检处理：`status` 按 `open → dispatched → fixed → closed` 更新，`assigned_to` 保存 admin 用户 ID，`resolved_at` 在标记修复时写入。`device_fault_report_event` 同事务记录提交、派单/改派、修复和关闭状态，包含操作人、指派账号及处理备注。user_db/0019_device_fault_casework_history.sql 为旧报修写入明确的迁移状态快照；旧历史缺失不会伪造。

**业务说明**:**设备报修记录**。用户在小程序"站点详情"上报修充电桩故障(限每设备 24h 一次,防骚扰)。

**关键业务规则**:

- **限频**:同一设备 24h 内只能报修一次(应用层 + DB 部分约束)
- 报修后写 `alert_stream` 事件给客户巡检
- 软删除启用

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `report_no` | `CHAR(32)` | UNIQUE, NOT NULL | — | 业务报修单号,格式 `RP + YYYYMMDD + 10 位随机` |
| `user_id` | `BIGINT UNSIGNED` | NOT NULL | — | 报修人(`user.id`) |

| `device_id` | `VARCHAR(32)` | NOT NULL | — | 报修设备 ID |
| `port_id` | `VARCHAR(32)` | NULL | NULL | 具体端口(可选) |
| `fault_type` | `ENUM('charging_failure','port_damage','display_abnormal','network_failure','other')` | NOT NULL | — | 故障类型 |
| `description` | `TEXT` | NOT NULL | — | 文字描述 |
| `photos` | `JSON` | NULL | NULL | 照片 URL 列表(用户上传到 OSS) |
| `status` | `ENUM('pending','dispatched','resolved','closed')` | NOT NULL | `'pending'` | 状态:待派单 / 已派单 / 已修复 / 已关闭 |
| `dispatched_to` | `BIGINT UNSIGNED` | NULL | NULL | 派单给巡检员(`admin_user_role.id`) |
| `dispatched_at` | `DATETIME(3)` | NULL | NULL | 派单时间 |
| `resolved_at` | `DATETIME(3)` | NULL | NULL | 修复时间 |
| `resolution_note` | `VARCHAR(512)` | NULL | NULL | 修复备注(巡检员填) |
| `closed_by` | `BIGINT UNSIGNED` | NULL | NULL | 关闭人 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_device_fault_report` | `id` | 主键 | — |
| `uk_device_fault_report_no` | `report_no` | 唯一 | 单号追溯 |
| **`idx_device_fault_report_device_recent`** | `device_id`, `created_at` | 普通 | **查同设备最近报修**(应用层 24h 防重) |
| `idx_device_fault_report_status_created` | `status`, `created_at` | 普通 | 巡检员查待处理队列 |
| `idx_device_fault_report_dispatched` | `dispatched_to`, `status` | 普通 | 巡检员查我的工单 |
| `idx_device_fault_report_device_created` | `device_id`, `created_at` | 普通 | 查某设备的报修历史 |
| `idx_device_fault_report_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- 同一 `device_id` 24h 内只能报修一次(应用层查询 `created_at > NOW() - 24 HOUR` 防重)
- `status='dispatched'` 时,`dispatched_to` / `dispatched_at` NOT NULL
- `status='resolved'` 时,`resolved_at` / `resolution_note` NOT NULL
- `status='closed'` 时,`closed_by` NOT NULL

### 关系

- 多对一 → `user.id`(报修人)
- 多对一 → `gateway_db.device`(跨服务逻辑关联)

### 业务规则

- **创建**:用户报修 → 校验 24h 内同设备无报修 → INSERT `device_fault_report(status='pending')` + 推 `alert_stream`(`severity='low'`,路由巡检)
- **派单**:巡检员 PC 后台"待派单"队列 → 选报修 → 选巡检员 → UPDATE `status='dispatched', dispatched_to, dispatched_at`
- **修复**:巡检员现场修复 → 填备注 + 拍现场照片 → UPDATE `status='resolved', resolved_at, resolution_note`
- **关闭**:客户运营确认 → UPDATE `status='closed', closed_by`
- **限频**:同设备 24h 内只能 1 条未删除报修(防骚扰;紧急情况由巡检员直接录入)

## 表 17:`user_db.device_fault_report_event`

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | 主键及同时间排序键 |
| `report_id` | `BIGINT UNSIGNED` | `device_fault_report.id`，不使用外键以兼容软删除和分批清理 |
| `actor_id` | `BIGINT UNSIGNED NULL` | 用户报修人或后台操作人；系统迁移快照为空 |
| `event_type` | ENUM | `reported/dispatched/reassigned/fixed/closed/migration_baseline` |
| `from_status` / `to_status` | `VARCHAR(24) NULL` | 变化前后状态 |
| `assigned_to` | `BIGINT UNSIGNED NULL` | 本次事件后的指派账号 |
| `note` | `VARCHAR(2000) NULL` | 处理备注；后台队列可读，公开 history 只读 user_visible=1 内容 |
| `user_visible` | `TINYINT(1)` | 是否可展示给报修人 |
| `created_at` | `DATETIME(3)` | UTC 事件时间 |

索引 `idx_fault_event_report_time(report_id,created_at,id)` 支持单报修顺序查询；`idx_fault_event_actor_time(actor_id,created_at)` 支持操作审计分析。报修行锁、状态更新、事件插入在同一事务内提交。迁移 `0019_device_fault_casework_history.sql` 对已有记录只写当前状态快照且设 `user_visible=0`。

---

## 表 18:`user_db.active_port_charge`

**业务说明**:端口当前充电占用的数据库兜底表,不分区。Redis 逻辑锁和物理锁只负责短期协调;跨月唯一性由本表的 `port_id` 主键保证。

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `port_id` | `VARCHAR(32)` | PK, NOT NULL | 全局端口 ID;一端口最多一行 |
| `charge_order_id` | `BIGINT UNSIGNED` | UNIQUE, NOT NULL | 当前充电订单 ID |
| `order_no` | `CHAR(32)` | UNIQUE, NOT NULL | 当前订单号,供 Redis 锁持有者比对 |
| `started_at` | `DATETIME(3)` | NOT NULL | 占用生效时间 |

user 服务收到 gateway 的启动成功结果后,**在同一 `user_db` 事务内**先 `INSERT active_port_charge`,再将对应 `charge_order` 更新为 `charging`;主键冲突时不得把第二笔订单置为 `charging`,应进入启动异常与退款补偿。订单结束、启动失败或人工确认设备断电后,仅执行带 `port_id` **和** `charge_order_id` 条件的删除。超时不能仅凭 Redis TTL 删除本表;必须核对设备状态与订单状态。禁止 gateway 直写本表。

---

## 表 19:`user_db.event_outbox`

**业务说明**:支付回调等事务需要可靠发布的 Stream 事件。写业务状态与写 outbox 在同一 `user_db` 事务内完成;事务外的 user 发布器按 `next_retry_at` 扫描并重试,收到 Redis `XADD` 确认后才标记 `published`。

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | outbox ID |
| `event_key` | `VARCHAR(128)` | UNIQUE, NOT NULL | 业务幂等键,如 `charge-start:{payment_order_id}` |
| `stream_name` | `VARCHAR(64)` | NOT NULL | 目标 Stream,如 `charge_started_stream` / `comp_tx_stream` |
| `payload` | `JSON` | NOT NULL | 含 `event_key`、`order_id`、事件类型和必要参数 |
| `status` | `ENUM('pending','published')` | NOT NULL | 发布状态 |
| `attempt_count` | `INT UNSIGNED` | NOT NULL | 已尝试次数 |
| `next_retry_at` | `DATETIME(3)` | NOT NULL | 下次重试时间 |
| `published_at` | `DATETIME(3)` | NULL | 成功发布时间 |
| `created_at` | `DATETIME(3)` | NOT NULL | 创建时间 |

发布器可重复 `XADD`;消费方以 `event_key` 去重,不得只依赖 Redis 自动生成的 Stream entry ID。未发布记录持续重试并告警;已发布记录至少保留至消费确认和对账完成。回调幂等表只阻止重复入账,**不能替代 outbox 的投递状态**。

---

**user_db 全部 19 张表设计完成**
