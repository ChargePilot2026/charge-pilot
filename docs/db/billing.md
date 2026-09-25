# billing_db 数据库表设计

**所属服务**:billing(对内 HTTP,纯计算 + 异步编排)
**Schema 名**:`billing_db`
**字符集 / 排序规则**:`utf8mb4` / `utf8mb4_unicode_ci`
**引擎**:InnoDB(全表)
**数据库版本**:MySQL 8.4 LTS

> **单客户部署约定**:billing_db 是**单客户专用数据库**,所有表都**不带 `customer_id` 列**。

> **软删除策略**:跨 schema 对账见 `docs/cross-reference.md` § 5.5(权威源),本文档通用约定与之一致;若冲突,以 cross-reference 为准。

## 通用约定

| 项目 | 约定 | 例外 |
| --- | --- | --- |
| 主键 | `BIGINT UNSIGNED AUTO_INCREMENT`,字段名 `id` | 无 |
| 时间戳 | `created_at` / `updated_at`,类型 `DATETIME(3)` | 无 |
| **软删除** | 启用:`deleted_at DATETIME(3) NULL` + `deleted_by` | **结算快照表不软删**(合规要求保留,按月分区) |
| 金额 | `BIGINT`(单位:**分**) | 无 |
| 索引命名 | `pk_` / `uk_` / `idx_` 前缀 | 无 |
| 外键 | **不声明** | 无 |

## 表清单(5 张)

| 表名 | 业务说明 | 分表策略 | 估算行数(单客户 5 年) |
| --- | --- | --- | --- |
| `fee_calculation` | 计费计算快照(每次充电结束的结果) | 按月分区 | ~2000 万 |
| `settlement` | 分账单(每笔订单的分账结果) | 不分 | ~2000 万 |
| `settlement_party_amount` | 分账明细(每个参与方金额) | 不分 | ~8000 万 |
| `pricing_tier_snapshot` | 计费阶梯电价快照(冗余 pricing_rule JSON 解析) | 不分 | ~500 |
| `withdraw_request` | 提现申请(分账后客户提现) | 不分 | ~1 万 |

> **本文件首批设计全部 5 张表**。

---

## 表 1:`billing_db.fee_calculation`

**业务说明**:**计费计算快照**(每次充电结束 → 计费引擎算费 → 写一条)。记录"当时用了哪条计费规则 / 计算过程 / 最终金额",便于审计与争议处理。

**关键业务规则**:

- **快照性质**:不可修改,即使后续 pricing_rule 改版也不影响历史记录
- **估算订单标记**:`is_estimated=TRUE` 表示按 § 6.5 B 方案估算
- **按月分区**:合规要求保留 ≥ 3 年(§ 13.3);按月分区 + 物理归档超 3 年记录

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `charge_order_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `user_db.charge_order.id` |
| `user_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `user_db.user.id` |
| `pricing_rule_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `admin_db.pricing_rule.id` |
| `billing_mode` | `ENUM('by_time','by_kwh','tiered','time_of_use')` | NOT NULL | — | 计费模式快照 |
| `meter_kwh` | `DECIMAL(10,3)` | NULL | NULL | 实走表电量(kWh,§ 6.5 A 方案) |
| `estimated_kwh` | `DECIMAL(10,3)` | NULL | NULL | 估算电量(§ 6.5 B 方案) |
| `duration_seconds` | `INT UNSIGNED` | NOT NULL | — | 充电时长(秒) |
| `is_estimated` | `BOOLEAN` | NOT NULL | `FALSE` | 是否估算订单 |
| `timestamp_drift_ms` | `INT` | NULL | NULL | 设备时钟偏差(异常订单标注) |
| `electric_fee_cents` | `BIGINT` | NOT NULL | `0` | 电费(分) |
| `service_fee_cents` | `BIGINT` | NOT NULL | `0` | 服务费(分) |
| `total_fee_cents` | `BIGINT` | NOT NULL | `0` | 总费用(分)= 电费 + 服务费 |
| `calculation_detail` | `JSON` | NOT NULL | — | 计算过程 JSON(便于审计:每段时长 / 各阶梯电价 / 各时段费用 等) |
| `pricing_rule_snapshot` | `JSON` | NOT NULL | — | **计费规则快照**(冗余当时的 `pricing_rule` 完整字段,即使规则改版也不影响审计) |
| `calculated_at` | `DATETIME(3)` | NOT NULL | — | 计算时间 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 入库时间 |
| `partition_key` | `DATE` | NOT NULL | — | 分区键 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_fee_calculation` | `id` | 主键 | — |
| `uk_fee_calculation_charge_order` | `charge_order_id` | 唯一 | 一笔充电订单 = 一条计费快照 |
| `idx_fee_calculation_user_calculated` | `user_id`, `calculated_at` | 普通 | 用户计费历史查询 |
| `idx_fee_calculation_pricing_rule` | `pricing_rule_id`, `calculated_at` | 普通 | 统计某规则的使用情况 |

### 约束

- 一笔 `charge_order` 只对应一条计费快照(`charge_order_id` 唯一)
- `is_estimated=TRUE` 时,`estimated_kwh` NOT NULL,`meter_kwh` 可为 NULL
- `is_estimated=FALSE` 时,`meter_kwh` NOT NULL
- `total_fee_cents = electric_fee_cents + service_fee_cents`(应用层校验)

### 关系

- 多对一 → `user_db.charge_order.id`(跨服务,无外键)
- 多对一 → `admin_db.pricing_rule.id`(跨服务,无外键)

### 业务规则

- **触发**:billing 服务消费 `charge_ended_stream` 事件 → 查 `pricing_rule` + `telemetry` 实时数据 → 计费计算 → INSERT 本表(同事务)+ 触发 `refund_required_stream`(如果失败 / 超时)
- **估算订单**:§ 6.5 B 方案场景下,设备无遥测数据 → 按端口历史 P50 功率估算 → `is_estimated=TRUE`
- **审计追溯**:`pricing_rule_snapshot` 存规则完整 JSON,即使规则后来改了,本表记录也不变
- **物理归档**:worker 每日扫表 → `calculated_at < NOW() - 3 YEAR` → `DELETE`(DROP PARTITION)

---

## 表 2:`billing_db.settlement`

**业务说明**:**分账单**。每笔充电订单的分账结果 = 一条 `settlement` 记录。**关联 `charge_order`,**记录本次分账的总金额 / 模式 / 参与方数量**。

**关键业务规则**:

- 一笔充电订单 = 一条 settlement(1:1)
- 模式 A(electric_and_service) / 模式 B(service_only)
- 软删除启用

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `settlement_no` | `CHAR(32)` | UNIQUE, NOT NULL | — | 业务分账单号,格式 `ST + YYYYMMDD + 10 位随机` |
| `charge_order_id` | `BIGINT UNSIGNED` | UNIQUE, NOT NULL | — | 关联 `user_db.charge_order.id`(1:1) |
| `user_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `user_db.user.id` |
| `split_template_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `admin_db.split_template.id` |
| `split_mode` | `ENUM('electric_and_service','service_only')` | NOT NULL | — | 分账模式 |
| `electric_total_cents` | `BIGINT` | NOT NULL | `0` | 电费总额(分) |
| `service_total_cents` | `BIGINT` | NOT NULL | `0` | 服务费总额(分) |
| `total_amount_cents` | `BIGINT` | NOT NULL | — | 总金额(分)= electric + service |
| `party_count` | `TINYINT UNSIGNED` | NOT NULL | — | 参与方数量(2-8) |
| `withdraw_status` | `ENUM('pending','partial','completed')` | NOT NULL | `'pending'` | 提现状态(本期预留) |
| `withdraw_completed_at` | `DATETIME(3)` | NULL | NULL | 提现完成时间 |
| `settled_at` | `DATETIME(3)` | NOT NULL | — | 分账时间 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_settlement` | `id` | 主键 | — |
| `uk_settlement_no` | `settlement_no` | 唯一 | 单号追溯 |
| `uk_settlement_charge_order` | `charge_order_id` | 唯一 | 一笔订单 = 一条分账 |
| `idx_settlement_user_settled` | `user_id`, `settled_at` | 普通 | 用户分账历史 |
| `idx_settlement_template_settled` | `split_template_id`, `settled_at` | 普通 | 模板维度统计 |
| `idx_settlement_withdraw_status` | `withdraw_status`, `settled_at` | 普通 | 待提现查询 |
| `idx_settlement_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- 一笔 `charge_order` 只对应一条 settlement(唯一约束)
- `party_count` 范围 2-8(应用层校验)
- `total_amount_cents = electric_total_cents + service_total_cents`
- `withdraw_status='completed'` 时,`withdraw_completed_at` NOT NULL

### 关系

- 多对一 → `user_db.charge_order.id`(跨服务,无外键)
- 多对一 → `admin_db.split_template.id`(跨服务,无外键)
- 一对多 → `settlement_party_amount.settlement_id`(N 条参与方金额)

### 业务规则

- **生成**:billing 服务消费 `charge_ended_stream` → 算费完成 → 按 `split_template` 拆分 → INSERT `settlement` + INSERT N 条 `settlement_party_amount`(同事务)
- **提现**(本期预留):客户财务在 PC 后台"提现管理"发起提现申请 → 按 `split_party.bank_account` 走银行转账 → UPDATE `withdraw_status='completed'`
- **审计**:每次分账生成写 `audit_log`

---

## 表 3:`billing_db.settlement_party_amount`

**业务说明**:**分账明细**(每个参与方应得金额)。一笔 settlement 对应 N 条 `settlement_party_amount`,记录每方的金额 / 比例 / 银行账号。

**关键业务规则**:

- **比例守恒**:所有 `party_amount` 之和 = `settlement.total_amount_cents`
- 软删除启用

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `settlement_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `settlement.id` |
| `split_party_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `admin_db.split_party.id` |
| `party_name` | `VARCHAR(64)` | NOT NULL | — | 参与方名称(冗余) |
| `party_type` | `ENUM('platform','property','franchisee','owner','customer_self','other')` | NOT NULL | — | 参与方类型(冗余) |
| `split_percent` | `DECIMAL(5,2)` | NOT NULL | — | 分账比例(% 冗余) |
| `party_amount_cents` | `BIGINT` | NOT NULL | — | 该方应得金额(分) |
| `electric_amount_cents` | `BIGINT` | NOT NULL | `0` | 该方电费分成(模式 A) |
| `service_amount_cents` | `BIGINT` | NOT NULL | `0` | 该方服务费分成 |
| `withdraw_status` | `ENUM('pending','withdrawn','failed')` | NOT NULL | `'pending'` | 该方提现状态 |
| `withdraw_request_id` | `BIGINT UNSIGNED` | NULL | NULL | 关联 `withdraw_request.id`(已提现时填) |
| **`next_settlement_at`** | `DATETIME(3)` | NULL | NULL | **下次结算日**(配合 split_party.settlement_cycle,worker 按此日期生成提现申请) |
| **`last_settled_at`** | `DATETIME(3)` | NULL | NULL | **上次结算日**(已结算的分账记录;NULL = 尚未结算) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_settlement_party_amount` | `id` | 主键 | — |
| `idx_settlement_party_amount_settlement` | `settlement_id`, `deleted_at` | 普通 | 查某分账的所有参与方 |
| `idx_settlement_party_amount_party` | `split_party_id`, `created_at` | 普通 | 参与方维度统计 |
| `idx_settlement_party_amount_withdraw` | `withdraw_status`, `created_at` | 普通 | 待提现查询 |
| `idx_settlement_party_amount_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- 同一 `settlement_id` 下所有未删除的 `party_amount_cents` 之和 = `settlement.total_amount_cents`(应用层校验)
- `electric_amount_cents + service_amount_cents = party_amount_cents`

### 关系

- 多对一 → `settlement.id`
- 多对一 → `admin_db.split_party.id`(跨服务,无外键)

### 业务规则

- **生成**:与 `settlement` 同事务批量 INSERT N 条
- **提现**(本期预留):客户财务在 PC 后台勾选若干 `settlement_party_amount` → 发起提现申请 → INSERT `withdraw_request` + UPDATE `withdraw_status='withdrawn', withdraw_request_id=$id`
- **舍入误差**:按比例计算的金额可能产生 1 分误差,最后一条参与方用 `total_amount - SUM(前面已分配的)` 兜底

---

## 表 4:`billing_db.pricing_tier_snapshot`

**业务说明**:**计费阶梯电价快照**(冗余 `admin_db.pricing_rule.electric_fee_config` JSON 解析后的结构化数据)。每次计费时,billing 服务读取本表而不是解析 JSON,加速计算。

**关键业务规则**:

- **缓存性质**:admin 改 pricing_rule → worker 同步生成新 snapshot → 计费时读最新 snapshot
- **不软删除**:旧 snapshot 保留(便于审计历史订单用了哪个版本)
- 一条 pricing_rule 可能有多条 snapshot(历史版本)

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `pricing_rule_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `admin_db.pricing_rule.id` |
| `version` | `INT UNSIGNED` | NOT NULL | `1` | 版本号(每次规则变更 +1) |
| `electric_fee_mode` | `ENUM('flat','tiered','time_of_use')` | NOT NULL | — | 电费模式 |
| `electric_fee_flat_cents_per_kwh` | `BIGINT` | NULL | NULL | flat 模式:每度电价(分) |
| `electric_fee_tiers` | `JSON` | NULL | NULL | tiered 模式:阶梯数组 JSON(`[{min:0,max:200,cents:80},{min:200,max:null,cents:120}]`) |
| `electric_fee_time_of_use` | `JSON` | NULL | NULL | time_of_use 模式:时段配置 JSON(`{peak:{hours:'08:00-21:00',cents:120},off_peak:{hours:'21:00-08:00',cents:50}}`) |
| `service_fee_mode` | `ENUM('flat','percentage','by_time','by_kwh')` | NOT NULL | — | 服务费模式 |
| `service_fee_config` | `JSON` | NOT NULL | — | 服务费配置 JSON(类似 electric_fee) |
| `min_charge_cents` | `BIGINT` | NOT NULL | `0` | 最低消费 |
| `max_charge_cents` | `BIGINT` | NULL | NULL | 最高消费 |
| `is_current` | `BOOLEAN` | NOT NULL | `TRUE` | 是否当前版本(同 pricing_rule_id 只允许 1 条 TRUE) |
| `effective_from` | `DATETIME(3)` | NOT NULL | — | 生效时间 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_pricing_tier_snapshot` | `id` | 主键 | — |
| `uk_pricing_tier_snapshot_rule_version` | `pricing_rule_id`, `version` | 唯一 | 防重复生成同版本 |
| `idx_pricing_tier_snapshot_current` | `pricing_rule_id`, `is_current` | 普通 | 计费时查当前版本 |

### 约束

- 同一 `pricing_rule_id` 下 `is_current=TRUE` 只允许 1 条(应用层校验)
- 模式对应字段:
  - `electric_fee_mode='flat'` → `electric_fee_flat_cents_per_kwh` NOT NULL
  - `electric_fee_mode='tiered'` → `electric_fee_tiers` NOT NULL
  - `electric_fee_mode='time_of_use'` → `electric_fee_time_of_use` NOT NULL

### 关系

- 多对一 → `admin_db.pricing_rule.id`(跨服务,无外键)

### 业务规则

- **同步触发**:admin 服务 pricing_rule 变更 → 发布 `pricing_rule_changed_stream` → billing worker 消费 → 生成新 snapshot(版本+1)+ 旧 snapshot `is_current=FALSE`
- **计费查询**:billing 计算时查 `is_current=TRUE AND pricing_rule_id=$id` → 直接读结构化字段,无需解析 JSON
- **历史追溯**:`fee_calculation.pricing_rule_snapshot` 存当时的完整 JSON,与本表互为校验

---

## 表 5:`billing_db.withdraw_request`

**业务说明**:**提现申请**。客户财务把分账余额从平台账户提现到银行账号。**本期预留,实际功能二期实施**(需求文档未明确要求提现,但属于分账后必要闭环)。

**关键业务规则**:

- **本期预留 schema**,实际功能二期实施
- 一笔提现申请可包含多个 `settlement_party_amount`(批量提现)
- 提现状态:pending / processing / success / failed / cancelled

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `request_no` | `CHAR(32)` | UNIQUE, NOT NULL | — | 提现申请单号,格式 `WD + YYYYMMDD + 10 位随机` |
| `applicant_id` | `BIGINT UNSIGNED` | NOT NULL | — | 申请人(客户财务 user_id) |
| `party_name` | `VARCHAR(64)` | NOT NULL | — | 收款方(冗余自 split_party) |
| `bank_account_name` | `VARCHAR(64)` | NOT NULL | — | 银行账户名 |
| `bank_account_no_enc` | `VARBINARY(255)` | NOT NULL | — | 银行账号 AES_ENCRYPT |
| `bank_name` | `VARCHAR(64)` | NOT NULL | — | 开户行 |
| `amount_cents` | `BIGINT` | NOT NULL | — | 提现金额(分) |
| `related_party_amount_ids` | `JSON` | NOT NULL | — | 关联的 `settlement_party_amount.id` 列表(批量提现) |
| `status` | `ENUM('pending','processing','success','failed','cancelled')` | NOT NULL | `'pending'` | 提现状态 |
| `bank_transaction_id` | `VARCHAR(64)` | NULL | NULL | 银行流水号(success 时填) |
| `failure_reason` | `VARCHAR(256)` | NULL | NULL | 失败原因 |
| `processed_at` | `DATETIME(3)` | NULL | NULL | 处理时间 |
| `completed_at` | `DATETIME(3)` | NULL | NULL | 完成时间 |
| **`reviewed_by`** | `BIGINT UNSIGNED` | NULL | NULL | **审核人**(需求 § 9.3 客户财务审核,关联 `admin_db.admin_user_role.id`) |
| **`reviewed_at`** | `DATETIME(3)` | NULL | NULL | **审核时间** |
| **`review_note`** | `VARCHAR(512)` | NULL | NULL | **审核备注**(通过 / 拒绝原因) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 申请时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_withdraw_request` | `id` | 主键 | — |
| `uk_withdraw_request_no` | `request_no` | 唯一 | 单号追溯 |
| `idx_withdraw_request_applicant_status` | `applicant_id`, `status`, `created_at` | 普通 | 客户财务查自身提现记录 |
| `idx_withdraw_request_status_created` | `status`, `created_at` | 普通 | 待处理查询 |
| `idx_withdraw_request_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `status IN ('success','failed','cancelled')` 时,`completed_at` NOT NULL
- `status='success'` 时,`bank_transaction_id` NOT NULL
- `status='failed'` 时,`failure_reason` NOT NULL
- `amount_cents > 0`(应用层校验)

### 关系

- 多对一 → `admin_user_role.id`(申请人)
- 多对多 → `settlement_party_amount.id`(通过 `related_party_amount_ids` JSON 关联)

### 业务规则

- **本期**:仅 schema 占位,客户财务可在 PC 后台手动 INSERT 测试数据;无实际银行接口对接
- **二期触发**:
  1. 客户合同要求提现功能
  2. 业务接入银行转账接口(如企业网银 API)
  3. 平台账户余额达到一定阈值需要清账
- **二期流程**(预留):客户财务在 PC 后台"提现管理"勾选若干 `settlement_party_amount` → 填银行账号 → 提交 → INSERT `withdraw_request(status='pending')` → 后台 worker 调银行 API → 成功后 UPDATE `status='success', bank_transaction_id` + UPDATE `settlement_party_amount.withdraw_status='withdrawn'`

---

**billing_db 全部 5 张表设计完成**

> **下一文件**:`docs/db/worker.md`(worker_db,任务调度 + 补偿事务 + DLQ)。
