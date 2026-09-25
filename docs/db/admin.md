# admin_db 数据库表设计

**所属服务**:admin(对外 :8082,PC 后台侧 API)
**Schema 名**:`admin_db`
**字符集 / 排序规则**:`utf8mb4` / `utf8mb4_unicode_ci`
**引擎**:InnoDB(全表)
**数据库版本**:MySQL 8.4 LTS

> **单客户部署约定**(沿用需求 § 1.2 / § 13.2):admin_db 是**单客户专用数据库**,不与其他客户共享;所有表都**不带 `customer_id` 列**(客户级隔离由部署边界保证)。

## 通用约定

| 项目 | 约定 | 例外 |
| --- | --- | --- |
| 主键 | `BIGINT UNSIGNED AUTO_INCREMENT`,字段名 `id` | 无 |
| 业务唯一键 | UUID v4 或业务字符串(如 `role_code`),单独字段 | 无 |
| 时间戳 | `created_at` / `updated_at`,类型 `DATETIME(3)` | 无 |
| **软删除** | **本期启用**:业务表加 `deleted_at DATETIME(3) NULL` + `deleted_by BIGINT UNSIGNED NULL`;删除 = `UPDATE ... SET deleted_at = NOW()`;**所有查询默认 `WHERE deleted_at IS NULL`**;`idx_*_deleted_at` 索引 | **配置 / 日志类表不软删**:角色 / 权限 / 白标 / 公告 / 审计日志 / 推送日志 / OTA 包元数据 |
| 金额 | `BIGINT`(单位:**分**) | 无 |
| 加密字段 | `VARBINARY` + MySQL `AES_ENCRYPT`(§ 9.3) | 仅敏感字段(管理员邮箱 / 备用手机号) |
| 状态字段 | `ENUM(...)` + 配套 comment | 仅业务状态字段 |
| 索引命名 | `pk_` / `uk_` / `idx_` / `fk_` 前缀 | 无 |
| 外键 | **不声明**(跨服务事务用最终一致性,§ 4.3 + § 5.4) | 无 |

## 表清单(23 张)

| 表名 | 业务说明 | 分表策略 | 估算行数(单客户 5 年) |
| --- | --- | --- | --- |
| `admin_user_role` | PC 后台管理员账号 | 不分 | ~50 |
| `role` | 角色(7 个预置角色) | 不分 | ~10 |
| `permission` | 权限点 | 不分 | ~200 |
| `station` | 充电站点 | 不分 | ~500 |
| `device_meta` | 设备配置元数据(冗余 gateway_db) | 不分 | ~5000 |
| `pricing_rule` | 计费规则实例 | 不分 | ~100 |
| `pricing_template` | 计费规则模板 | 不分 | ~20 |
| `split_template` | 分账模板 | 不分 | ~20 |
| `split_party` | 分账参与方(模板实例) | 不分 | ~200 |
| `whitelabel_config` | 白标配置 | 不分 | 1(单例) |
| `announcement` | 公告 | 不分 | ~500 |
| `customer_service_config` | 客服坐席配置 | 不分 | ~20 |
| `webhook_subscription` | Webhook 订阅 | 不分 | ~20 |
| `webhook_delivery_log` | Webhook 推送日志 | 按月分区 | ~500 万 |
| `ota_package` | OTA 固件包元数据 | 不分 | ~100 |
| `ota_schedule` | OTA 推送调度 | 不分 | ~1000 |
| `alert_rule` | 告警规则(§ 3.1.4 客户自配) | 不分 | ~100 |
| `alert_subscription` | 告警订阅(Webhook / 邮件) | 不分 | ~20 |
| `risk_config` | 风控配置(频次 / 金额阈值) | 不分 | 1(单例) |
| `settled_record` | 账单结清记录 | 不分 | ~5000 |
| `finance_reconcile_log` | 财务对账日志(对账差异处理) | 不分 | ~1000/年 |
| `invoice_review` | 发票审核记录(冗余 user_db.invoice_request) | 不分 | ~50 万 |
| `audit_log` | 所有 admin 写操作审计 | 按月分区 | ~500 万 |

> **本文件首批设计 7 张核心表**:`admin_user_role` / `role` / `permission` / `whitelabel_config` / `announcement` / `customer_service_config` / `audit_log`。
> 剩余 16 张(`station` / `device_meta` / `pricing_rule` / `pricing_template` / `split_template` / `split_party` / `webhook_subscription` / `webhook_delivery_log` / `ota_package` / `ota_schedule` / `alert_rule` / `alert_subscription` / `risk_config` / `settled_record` / `finance_reconcile_log` / `invoice_review`)在第二批设计。

### 关键架构决策(本批次)

**单客户部署约定**(老杨师傅决策):

- admin_db 是**单客户专用数据库**,由客户自购的云服务器独立部署
- 所有表都**不带 `customer_id` 列**(由部署边界保证隔离)
- 跨客户数据迁移不提供工具(需求 § 13.2 已明确不做集中升级工具)
- 不引入任何"客户级过滤"逻辑(代码层与 SQL 层都不需要)

**软删除范围**:

- **业务表**(站点 / 设备 / 计费规则 / 公告 / OTA 调度 / 告警规则 / 风险配置 / 账单 / 对账日志 / 发票审核)启用软删除
- **配置类**(角色 / 权限 / 白标 / 告警订阅 / 风控配置阈值)不软删,采用"启用 / 停用"机制
- **日志类**(审计 / Webhook 推送)按月分区 + 物理归档(超 3 年),不软删

---

## 表 1:`admin_db.admin_user_role`

**业务说明**:**PC 后台管理员账号**。一个账号对应一个客户运营人员,登录后根据 `role_id` 获得对应权限。

**关键业务规则**:

- **7 个预置角色**(技术规格 § 7.3.2 + 需求文档):终端用户 / 客户运营 / 客户财务 / 客户巡检 / 客户客服坐席 / 客户管理员 / 监管方(其中"终端用户"是 user_db 不归本表)
- 管理员账号归属 `admin_db`(本表),**不与终端用户 `user_db.user` 混在一起**
- **密码安全**:argon2id 哈希(§ 9.3),不能明文存
- **登录失败锁定**:连续 5 次失败 → 临时锁定 30 min(应用层)
- 软删除启用:员工离职 → 软删账号(保留审计);不物理删除

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `username` | `VARCHAR(64)` | UNIQUE, NOT NULL | — | 登录用户名(客户运营自定,如 zhangsan) |
| `password_hash` | `VARCHAR(255)` | NOT NULL | — | argon2id 哈希(§ 9.3,默认参数 `m=19456, t=2, p=1`) |
| `display_name` | `VARCHAR(64)` | NOT NULL | — | 显示名(界面展示,如"张三 / 客户运营") |
| `email_enc` | `VARBINARY(255)` | NULL | NULL | 邮箱 AES_ENCRYPT 密文(可选,用于找回密码 / 告警通知) |
| `phone_enc` | `VARBINARY(255)` | NULL | NULL | 备用手机号 AES_ENCRYPT 密文(可选) |
| `role_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `role.id`(主角色,**一期一账号一角色**;二期可多角色) |
| `status` | `ENUM('active','locked','disabled','pending')` | NOT NULL | `'pending'` | 状态:active 正常 / locked 临时锁定 / disabled 停用(离职)/ pending 待激活(初始密码未改) |
| `last_login_at` | `DATETIME(3)` | NULL | NULL | 最近登录时间 |
| `last_login_ip` | `VARCHAR(45)` | NULL | NULL | 最近登录 IP(支持 IPv6) |
| `failed_login_count` | `TINYINT UNSIGNED` | NOT NULL | `0` | 连续登录失败次数(成功登录后归 0) |
| `locked_until` | `DATETIME(3)` | NULL | NULL | 锁定到期时间(临时锁定时填) |
| `must_change_password` | `BOOLEAN` | NOT NULL | `TRUE` | 是否必须修改密码(初始密码登录后强制改) |
| `password_changed_at` | `DATETIME(3)` | NULL | NULL | 最近改密时间(用于"密码 90 天过期"提醒) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID(通常是客户管理员) |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_admin_user_role` | `id` | 主键 | — |
| `uk_admin_user_role_username` | `username` | 唯一 | 登录查询 |
| `idx_admin_user_role_role_status` | `role_id`, `status` | 普通 | 查某角色下的所有管理员 |
| `idx_admin_user_role_status` | `status`, `deleted_at` | 普通 | 客户管理员查账号列表 |
| `idx_admin_user_role_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `status='locked'` 时,`locked_until` NOT NULL(应用层校验过期)
- `status='active'` 时,`password_hash` 必须 NOT NULL(显然)
- `failed_login_count >= 5` 时自动设 `status='locked'`(应用层 + admin API 校验)
- `deleted_at` NOT NULL 时,`username` 可被新账号复用(不强制唯一软删)

### 关系

- 多对一 → `role.id`
- 一对多 → `audit_log.actor_id`(本账号产生的所有写操作记录)
- 一对多 → `announcement.created_by` / `webhook_subscription.created_by` 等(任何 admin 写操作都会引用本账号)

### 业务规则

- **创建**:客户管理员在 PC 后台"账号管理"新建 → 填用户名 + 显示名 + 角色 → 系统生成随机初始密码 → INSERT `admin_user_role(status='pending', must_change_password=TRUE)` + 邮件 / 短信发送初始密码
- **首次登录**:校验初始密码 → 强制改密(校验强度:≥ 12 字符 + 字母 + 数字)→ UPDATE `must_change_password=FALSE, password_changed_at=NOW(), status='active'`
- **登录**:校验 `status='active'` + 校验 `password_hash`(argon2id verify)+ 校验 `locked_until` 是否过期 → 失败累计 `failed_login_count`,≥ 5 → 自动锁定 30 min
- **离职 / 停用**:客户管理员 UPDATE `status='disabled', deleted_at=NOW(), deleted_by=$当前操作人.id`(软删,保留审计)
- **角色变更**:客户管理员在 PC 后台改角色 → UPDATE `role_id`(影响下次登录权限)
- **改密**:登录后"修改密码" → 校验旧密码 + 校验新密码强度 → UPDATE `password_hash` + `password_changed_at=NOW()`(密码 90 天未改 → 登录后提示)

---

## 表 2:`admin_db.role`

**业务说明**:**角色**(RBAC 第一层)。**7 个预置角色**(需求文档 § 15.1),客户管理员不可新增自定义角色(本期),但可调整角色权限范围(本期不做,二期支持)。

**关键业务规则**:

- **7 个预置角色**:
  1. `customer_admin` 客户管理员
  2. `customer_ops` 客户运营
  3. `customer_finance` 客户财务
  4. `customer_inspector` 客户巡检
  5. `customer_service` 客户客服坐席
  6. `regulator` 监管方(只读访问特定数据)
  7. `system` 系统(内部服务账号,无人登录)
- 角色与权限多对多(`role_permission` 关联,本批次不设计,二期);一期简化为**角色 ↔ 权限列表 JSON**
- **不软删除**:角色是预置配置,启用 / 停用即可

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `role_code` | `VARCHAR(64)` | UNIQUE, NOT NULL | — | 角色代码(枚举:`customer_admin` / `customer_ops` / `customer_finance` / `customer_inspector` / `customer_service` / `regulator` / `system`) |
| `role_name` | `VARCHAR(64)` | NOT NULL | — | 角色中文名(如"客户财务") |
| `role_type` | `ENUM('admin','service','regulator','system')` | NOT NULL | — | 角色类型(admin 客户管理员类 / service 客服类 / regulator 监管方 / system 系统) |
| `permission_codes` | `JSON` | NOT NULL | — | 权限点代码数组(如 `["order.read","order.refund.review","alert.read"]`) |
| `description` | `VARCHAR(256)` | NULL | NULL | 角色描述 |
| `status` | `ENUM('enabled','disabled')` | NOT NULL | `'enabled'` | 启用 / 停用(`disabled` 的角色不可分配新账号) |
| `built_in` | `BOOLEAN` | NOT NULL | `TRUE` | 是否预置角色(预置角色不可删除,只能停用) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间(初始化时) |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_role` | `id` | 主键 | — |
| `uk_role_code` | `role_code` | 唯一 | JWT 解析后查角色 |
| `idx_role_type_status` | `role_type`, `status` | 普通 | 客户管理员查可分配角色列表 |

### 约束

- `built_in=TRUE` 时,不可物理删除(应用层校验)
- `role_code` 必须在 7 个预置值之一(应用层校验)
- `status='disabled'` 时,不可分配新 `admin_user_role.role_id`(应用层校验)

### 关系

- 一对多 → `admin_user_role.role_id`
- 多对多 → `permission.code`(通过 `permission_codes` JSON 字段;二期可拆 `role_permission` 关联表)

### 业务规则

- **初始化**:系统首次部署时,初始化脚本 INSERT 7 个预置角色(permission_codes 按规范填)
- **JWT 鉴权**:登录后 JWT 包含 `user_id` + `role_id` + `permission_codes`;admin 中间件校验时直接读 JWT,无需查库(性能优化)
- **权限变更**(二期):客户管理员在 PC 后台"角色管理"调权限 → UPDATE `permission_codes`;已签发 JWT 强制重登录才能生效
- **本期不做**:自定义角色(客户管理员不能新建角色,只能选预置)

---

## 表 3:`admin_db.permission`

**业务说明**:**权限点**(RBAC 第二层)。系统中所有需要权限控制的操作都对应一个权限点(如 `order.refund.review` / `device.ota.push`)。**预置**,不开放自定义。

**关键业务规则**:

- **权限点代码命名**:`{资源}.{动作}.{限定}` 三段式(如 `order.refund.review`、`device.ota.push`、`finance.invoice.review`)
- 权限点与角色多对多(通过 `role.permission_codes` JSON 关联)
- **不软删除**:权限点是预置配置,启用 / 停用即可

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `permission_code` | `VARCHAR(128)` | UNIQUE, NOT NULL | — | 权限点代码(枚举,如 `order.read` / `order.refund.review` / `device.ota.push` / `finance.invoice.review`) |
| `permission_name` | `VARCHAR(64)` | NOT NULL | — | 权限点中文名(如"查看订单"、"审核退款") |
| `resource` | `VARCHAR(32)` | NOT NULL | — | 资源类型(`order` / `device` / `finance` / `alert` / `customer` / `role` 等) |
| `action` | `VARCHAR(32)` | NOT NULL | — | 操作类型(`read` / `create` / `update` / `delete` / `review` / `push` 等) |
| `description` | `VARCHAR(256)` | NULL | NULL | 权限点描述 |
| `status` | `ENUM('enabled','disabled')` | NOT NULL | `'enabled'` | 启用 / 停用 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_permission` | `id` | 主键 | — |
| `uk_permission_code` | `permission_code` | 唯一 | JWT 鉴权时查权限点 |

### 约束

- `permission_code` 必须符合 `{resource}.{action}[.{qualifier}]` 命名规范(应用层校验)
- `status='disabled'` 时,即使角色 `permission_codes` 包含也不生效

### 关系

- 多对多 → `role.id`(通过 `role.permission_codes` JSON 关联)

### 业务规则

- **初始化**:系统部署时初始化脚本 INSERT 所有预置权限点(~200 个)
- **JWT 鉴权**:admin 中间件从 JWT 读 `permission_codes` → 校验当前请求的权限点是否在列表中 → 通过则继续,失败返回 403
- **权限点查询**:客户端(PC 后台)按 `resource` 分组查所有权限点,用于"角色管理"界面展示

---

## 表 4:`admin_db.whitelabel_config`

**业务说明**:**白标配置**(单例表,单客户只有一份配置)。客户在 PC 后台"白标配置"上传 Logo / 主题色 / 域名,用于小程序与 PC 后台的品牌定制。

**关键业务规则**:

- **单例**:全表只有一条记录(ID=1),客户运营更新时 UPDATE
- **配置项**:小程序名称 / Logo / 主题色 / 客服电话 / 备案号 / 自定义域名
- **不软删除**:白标是配置,不用软删

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | `1` | 单例 ID(永远 = 1) |
| `miniprogram_name` | `VARCHAR(32)` | NOT NULL | `'充电运营'` | 小程序名称(用户可见,顶部标题) |
| `miniprogram_logo_url` | `VARCHAR(512)` | NULL | NULL | 小程序 Logo OSS URL(81×81 PNG) |
| `admin_logo_url` | `VARCHAR(512)` | NULL | NULL | PC 后台 Logo OSS URL |
| `theme_color` | `CHAR(7)` | NOT NULL | `'#1890ff'` | 主题色(HEX 格式,如 `#1890ff`) |
| `service_phone` | `VARCHAR(32)` | NULL | NULL | 客服电话(用户可见) |
| `service_wechat_id` | `VARCHAR(64)` | NULL | NULL | 客服微信号(用户可见,可选) |
| `icp_record_no` | `VARCHAR(64)` | NULL | NULL | ICP 备案号(中国大陆小程序必填) |
| `custom_domain` | `VARCHAR(128)` | NULL | NULL | 自定义域名(小程序"request 合法域名"配置用) |
| `agreement_url` | `VARCHAR(512)` | NULL | NULL | 用户协议 URL(用户注册时跳转) |
| `privacy_url` | `VARCHAR(512)` | NULL | NULL | 隐私政策 URL |
| `about_us` | `TEXT` | NULL | NULL | 关于我们(富文本 / Markdown) |
| `updated_by` | `BIGINT UNSIGNED` | NULL | NULL | 最近修改人(admin user id) |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间(首次初始化) |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_whitelabel_config` | `id` | 主键 | — |

### 约束

- 应用层强制只有一条记录(ID=1);新增时校验 `id=1`
- `theme_color` 必须是 7 字符 HEX 格式(`#RRGGBB`,应用层校验)
- `custom_domain` 变更后,需客户运维同步更新 Caddyfile + 微信小程序后台"request 合法域名"配置

### 关系

- 无外键关系(单例配置)

### 业务规则

- **初始化**:系统首次部署时,初始化脚本 INSERT 一条默认记录(ID=1)
- **客户运营更新**:PC 后台"白标配置"页 → 上传 Logo / 改主题色 / 填客服电话 → UPDATE
- **缓存**:user 服务缓存白标配置(TTL 30 min,§ 4.7),前端启动时拉一次
- **小程序审核**:微信小程序发布前必须填 ICP 备案号(`icp_record_no`),否则审核不通过

---

## 表 5:`admin_db.announcement`

**业务说明**:**公告**。客户运营发布系统公告,在小程序首页弹窗 / "公告"页展示。**软删除**(过期公告软删,保留审计)。

**关键业务规则**:

- 公告类型:系统通知 / 维护公告 / 优惠活动
- 展示方式:弹窗(强制) / 列表(可选) / Banner(可选)
- **软删除**:过期 / 撤回公告软删,保留审计
- 定时过期:`valid_until < NOW()` 后由 worker 任务标记 + 小程序不再展示

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `title` | `VARCHAR(128)` | NOT NULL | — | 公告标题 |
| `content` | `TEXT` | NOT NULL | — | 公告内容(支持 Markdown) |
| `announcement_type` | `ENUM('system_notice','maintenance','promotion')` | NOT NULL | — | 公告类型 |
| `display_mode` | `ENUM('popup','list','banner','all')` | NOT NULL | `'list'` | 展示方式 |
| `priority` | `TINYINT UNSIGNED` | NOT NULL | `5` | 优先级(1-10,数字越小优先级越高;同时间多弹窗时按优先级排序) |
| `valid_from` | `DATETIME(3)` | NOT NULL | — | 生效时间 |
| `valid_until` | `DATETIME(3)` | NULL | NULL | 失效时间(NULL = 永久有效) |
| `target_scope` | `ENUM('all','specific_station','specific_user')` | NOT NULL | `'all'` | 投放范围:全部 / 指定站点 / 指定用户 |
| `target_ids` | `JSON` | NULL | NULL | 投放范围 ID 列表(根据 `target_scope` 填) |
| `read_count` | `INT UNSIGNED` | NOT NULL | `0` | 阅读次数(用户打开公告详情时 +1) |
| `created_by` | `BIGINT UNSIGNED` | NOT NULL | — | 发布人(admin user id) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_announcement` | `id` | 主键 | — |
| `idx_announcement_valid_priority` | `valid_from`, `valid_until`, `priority` | 普通 | 小程序查"当前生效的公告" |
| `idx_announcement_type_created` | `announcement_type`, `created_at` | 普通 | 客户运营查公告列表 |
| `idx_announcement_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `valid_until > valid_from`(应用层校验;`valid_until` NULL 时永久)
- `target_scope IN ('specific_station','specific_user')` 时,`target_ids` NOT NULL
- `priority` 范围 1-10(应用层校验)

### 关系

- 多对一 → `admin_user_role.id`(创建人)

### 业务规则

- **发布**:客户运营在 PC 后台"公告管理"新建 → 填标题 / 内容 / 类型 / 展示方式 / 生效时间 → INSERT `announcement` + 推送小程序(可选,运营勾选后才推)
- **小程序展示**:user 服务查 `valid_from <= NOW() AND (valid_until IS NULL OR valid_until > NOW()) AND deleted_at IS NULL` 的公告 → 按 `priority` 排序展示
- **撤回**:客户运营 UPDATE `deleted_at=NOW(), deleted_by=$操作人.id`(软删,小程序不再展示)
- **过期清理**:worker 每日扫表 → `valid_until < NOW() AND deleted_at IS NULL` → 软删
- **阅读统计**:`read_count` 仅用于粗粒度统计,精确阅读追踪用 `audit_log` / 单独事件流

---

## 表 6:`admin_db.customer_service_config`

**业务说明**:**客服坐席配置**(微信原生客服会话)。客户运营配置客服团队的微信账号,小程序"在线客服"入口会路由到配置的微信客服。

**关键业务规则**:

- 微信原生客服:**不做自有 IM 系统**,直接接入微信原生客服会话(需求文档已定)
- 坐席配置:每个客服绑定一个微信 openid / 微信号
- 软删除启用:员工离职 → 软删
- **首次响应 SLA**:客户自配(可空)

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `display_name` | `VARCHAR(64)` | NOT NULL | — | 客服姓名(用户可见,如"客服小张") |
| `wechat_openid` | `VARCHAR(64)` | NULL | NULL | 客服微信 openid(可选,用于绑定微信原生客服账号) |
| `wechat_id` | `VARCHAR(64)` | NULL | NULL | 客服微信号(用户可见,如 cs_xiaozhang) |
| `phone` | `VARCHAR(32)` | NULL | NULL | 客服电话(可选,兜底联系方式) |
| `email` | `VARCHAR(128)` | NULL | NULL | 客服邮箱(可选,接收告警) |
| `role_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `role.id`(必须是 `customer_service` 角色) |
| `status` | `ENUM('online','offline','busy','disabled')` | NOT NULL | `'offline'` | 状态:在线 / 离线 / 忙碌 / 停用 |
| `max_concurrent_chats` | `TINYINT UNSIGNED` | NOT NULL | `5` | 最大并发会话数 |
| `current_chat_count` | `TINYINT UNSIGNED` | NOT NULL | `0` | 当前会话数(超过 `max_concurrent_chats` → 路由到下一坐席) |
| `first_response_sla_seconds` | `INT UNSIGNED` | NULL | NULL | 首次响应 SLA(秒),客户自配(可空,如 60 = 1 分钟内必须响应) |
| `rating_avg` | `DECIMAL(3,2)` | NULL | NULL | 平均评分(1.00-5.00,来自用户评价) |
| `rating_count` | `INT UNSIGNED` | NOT NULL | `0` | 评分总数 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_customer_service_config` | `id` | 主键 | — |
| `idx_customer_service_config_status_deleted` | `status`, `deleted_at` | 普通 | 小程序客服入口查"在线且未删除"的坐席(轮询分配) |
| `idx_customer_service_config_role_status` | `role_id`, `status` | 普通 | 客户管理员查坐席列表 |

### 约束

- `role_id` 必须是 `customer_service` 角色(应用层校验)
- `current_chat_count <= max_concurrent_chats`(应用层校验)
- `status IN ('online','offline','busy')` 时,`deleted_at` 必须为 NULL(软删后状态置 `disabled`)

### 关系

- 多对一 → `role.id`

### 业务规则

- **创建**:客户管理员在 PC 后台"客服管理"新建坐席 → 填姓名 / 微信号 / 关联 `admin_user_role` → INSERT
- **路由**:小程序"在线客服"入口 → user 服务查 `status='online'` + `current_chat_count < max_concurrent_chats` 的坐席 → 按 `current_chat_count` 升序分配 → 通过微信原生客服消息 API 转发
- **会话结束**:微信原生客服会话结束 → UPDATE `current_chat_count -= 1`
- **评价**:用户评价客服 → UPDATE `rating_avg` / `rating_count`(滚动平均)
- **离职 / 停用**:UPDATE `status='disabled', deleted_at=NOW(), deleted_by=$操作人.id`
- **SLA 监控**:`first_response_sla_seconds` 配置后,worker 任务监控首次响应时长,超时告警(本期监控可观察,告警推到告警订阅)

---

## 表 7:`admin_db.audit_log`

**业务说明**:**所有 admin 写操作审计**(§ 9.5 已要求)。任何 admin 用户的 `POST/PUT/PATCH/DELETE` 请求都必须记录到本表。**不软删除**(审计日志不可修改,只追加 + 物理归档超 3 年记录)。

**关键业务规则**:

- **不可修改**(只追加):DB 用户权限隔离(只 INSERT,不能 UPDATE/DELETE)
- **保留期**:≥ 180 天(§ 9.5),实际归档周期 3 年(§ 13.3 等保三级)
- 物理归档:worker 周期任务 `DELETE FROM audit_log WHERE created_at < NOW() - 3 YEAR`(超 3 年物理清理)
- 按月分区:加速查询 + 便于按月归档

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `actor_id` | `BIGINT UNSIGNED` | NOT NULL | — | 操作者(admin_user_role.id;若是 system 自动操作,填 system 账号 id) |
| `actor_username` | `VARCHAR(64)` | NOT NULL | — | 操作者用户名(冗余,便于审计查询不 JOIN) |
| `actor_ip` | `VARCHAR(45)` | NULL | NULL | 操作者 IP(IPv4 / IPv6) |
| `actor_user_agent` | `VARCHAR(512)` | NULL | NULL | 操作者浏览器 UA |
| `action` | `VARCHAR(64)` | NOT NULL | — | 操作动作(`order.refund.approve` / `device.ota.push` / `role.update` 等) |
| `resource_type` | `VARCHAR(32)` | NOT NULL | — | 资源类型(`order` / `device` / `role` / `customer` 等) |
| `resource_id` | `VARCHAR(64)` | NULL | NULL | 资源 ID(如 order.id / device.id) |
| `before_state` | `JSON` | NULL | NULL | 操作前资源状态快照(便于审计"改了啥") |
| `after_state` | `JSON` | NULL | NULL | 操作后资源状态快照 |
| `request_id` | `VARCHAR(64)` | NULL | NULL | 关联请求 ID(链路追踪 ID,便于跨服务追踪) |
| `trace_id` | `VARCHAR(64)` | NULL | NULL | OpenTelemetry trace_id |
| `remark` | `VARCHAR(256)` | NULL | NULL | 备注(可选) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 操作时间 |
| `partition_key` | `DATE` | NOT NULL | — | 分区键(冗余 `created_at` 的日期部分) |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_audit_log` | `id` | 主键 | — |
| `idx_audit_log_actor_created` | `actor_id`, `created_at` | 普通 | "某员工的操作历史" |
| `idx_audit_log_resource` | `resource_type`, `resource_id`, `created_at` | 普通 | "某资源的所有变更" |
| `idx_audit_log_action_created` | `action`, `created_at` | 普通 | "某类操作的历史"(如查所有"退款审批通过"记录) |
| `idx_audit_log_trace_id` | `trace_id` | 普通 | 链路追踪时反查审计 |

### 约束

- `before_state` 与 `after_state` 不能同时为 NULL(至少要有"操作前后状态"的对比)
- DB 用户权限:`audit_log` 表的 `UPDATE` / `DELETE` 权限**必须从 MySQL 用户层面收回**(应用账号只有 `INSERT` / `SELECT`)

### 关系

- 多对一 → `admin_user_role.id`(操作者)
- 多对一 → 任意资源表(`resource_id` 不带外键,逻辑关联)

### 业务规则

- **写入**:admin 服务中间件拦截所有 `POST/PUT/PATCH/DELETE` 请求 → 处理完成后 → 异步 INSERT `audit_log`(失败不阻塞业务,但写 `alert_stream` 告警"审计写入失败")
- **异步写入**:用本地 channel + 后台 worker 批量 INSERT(避免每个请求都同步写 DB)
- **查询**:客户管理员在 PC 后台"审计日志"页 → 按 actor / resource / action / 时间范围筛选 → 分页返回
- **导出**:合规审查时,客户管理员可导出 CSV(时间范围 + actor 范围)
- **物理归档**:worker 每日扫表 → `created_at < NOW() - 3 YEAR` → `DELETE`(分区表 DROP PARTITION 更高效)
- **不可修改保证**:DB 层 REVOKE `UPDATE` / `DELETE` 权限;应用层不暴露任何修改 API

---

**本批次结束(7 张核心表)**

> 剩余 16 张表(`station` / `device_meta` / `pricing_rule` / `pricing_template` / `split_template` / `split_party` / `webhook_subscription` / `webhook_delivery_log` / `ota_package` / `ota_schedule` / `alert_rule` / `alert_subscription` / `risk_config` / `settled_record` / `finance_reconcile_log` / `invoice_review`)将在第二批设计,沿用本文件的"通用约定"和表设计格式。
