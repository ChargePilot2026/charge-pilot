# admin_db 数据库表设计

**所属服务**:admin(对外 :8082,PC 后台侧 API)
**Schema 名**:`admin_db`
**字符集 / 排序规则**:`utf8mb4` / `utf8mb4_unicode_ci`
**引擎**:InnoDB(全表)
**数据库版本**:MySQL 8.4 LTS

> **单客户部署约定**(沿用需求 § 1.2 / § 13.2):admin_db 是**单客户专用数据库**,不与其他客户共享;所有表都**不带 `customer_id` 列**(客户级隔离由部署边界保证)。

> **软删除策略**:跨 schema 对账见 `docs/cross-reference.md` § 5.5(权威源),本文档通用约定与之一致;若冲突,以 cross-reference 为准。

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

## 表清单(25 张)

| 表名 | 业务说明 | 分表策略 | 估算行数(单客户 5 年) |
| --- | --- | --- | --- |
| `admin_user_role` | PC 后台管理员账号 | 不分 | ~50 |
| `role` | 角色(7 个预置角色) | 不分 | ~10 |
| `permission` | 权限点 | 不分 | ~200 |
| `station` | 充电站点 | 不分 | ~500 |
| `device_meta` | 设备配置元数据(冗余 gateway_db) | 不分 | ~5000 |
| `pricing_rule` | 计费规则实例 | 不分 | ~100 |
| `pricing_template` | 计费规则模板 | 不分 | ~20 |
| **`coupon`** | **优惠券模板**(用户持有的优惠券规格定义) | 不分 | ~1000 |
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
| **`alert_event`** | **告警事件持久化**(规则触发记录,便于查询历史) | 按月分区 | ~50 万 |
| `audit_log` | 所有 admin 写操作审计 | 按月分区 | ~500 万 |

> **本文件包含全部 25 张表**:首批 7 张核心 + 第二批 17 张运营型(`station` / `device_meta` / `pricing_rule` / `pricing_template` / `coupon` / `split_template` / `split_party` / `webhook_subscription` / `webhook_delivery_log` / `ota_package` / `ota_schedule` / `alert_rule` / `alert_subscription` / `risk_config` / `settled_record` / `finance_reconcile_log` / `invoice_review`) + 第三批 1 张(`alert_event`,按月分区持久化告警事件)。

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
| `title` | `VARCHAR(128)` | NOT NULL | — | 公告标题(中文默认) |
| `title_i18n` | `JSON` | NULL | NULL | **多语言标题**(技术规格 § 15.8:本期只填 `{"zh-CN": "..."}`,en-US 等二期补) |
| `content` | `TEXT` | NOT NULL | — | 公告内容(支持 Markdown,中文默认) |
| `content_i18n` | `JSON` | NULL | NULL | **多语言内容**(同上) |
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

---

# 第二批:16 张运营型表

## 表 8:`admin_db.station`

**业务说明**:**充电站点元数据**。客户运营在 PC 后台"站点管理"配置,供小程序找桩 + 设备归属 + 财务对账使用。

**关键业务规则**:

- 站点 = 多个设备的容器(同一物理地点)
- 经纬度用于"找桩 / 地图"展示
- 营业时间用于显示"该站点当前是否营业"
- **软删除**:站点停用软删(关联设备不删,只是 `station_id` 不再指向有效站点)

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `station_name` | `VARCHAR(128)` | NOT NULL | — | 站点名称(用户可见,如"万达广场地下停车场") |
| `station_code` | `VARCHAR(32)` | UNIQUE, NOT NULL | — | 站点编码(客户运营自定,便于批量管理) |
| `address` | `VARCHAR(256)` | NOT NULL | — | 详细地址 |
| `longitude` | `DECIMAL(10,6)` | NOT NULL | — | 经度 |
| `latitude` | `DECIMAL(10,6)` | NOT NULL | — | 纬度 |
| `business_hours_start` | `TIME` | NULL | NULL | 营业开始时间(NULL = 24 小时) |
| `business_hours_end` | `TIME` | NULL | NULL | 营业结束时间 |
| `contact_phone` | `VARCHAR(32)` | NULL | NULL | 站点联系电话 |
| `total_ports` | `INT UNSIGNED` | NOT NULL | `0` | 总端口数(冗余自 device_meta,加速展示) |
| `operator_name` | `VARCHAR(64)` | NULL | NULL | 现场负责人(物业 / 第三方) |
| `operator_phone` | `VARCHAR(32)` | NULL | NULL | 现场负责人电话 |
| `remark` | `VARCHAR(512)` | NULL | NULL | 备注 |
| `status` | `ENUM('enabled','disabled')` | NOT NULL | `'enabled'` | 启用 / 停用 |
| `created_by` | `BIGINT UNSIGNED` | NOT NULL | — | 创建人 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_station` | `id` | 主键 | — |
| `uk_station_code` | `station_code` | 唯一 | 按编码查 |
| `idx_station_geo` | `longitude`, `latitude` | 普通 | 地理范围查询(找桩) |
| `idx_station_status_deleted` | `status`, `deleted_at` | 普通 | 客户运营查站点列表 |
| `idx_station_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `business_hours_end > business_hours_start`(同一天内;跨夜由应用层校验)
- `total_ports` 必须等于该站点下 `device_meta` 启用设备的端口数之和(应用层校验)

### 关系

- 一对多 → `device_meta.station_id`(同一站点的多台设备)
- 一对多 → `announcement.target_ids` JSON(投放范围含本站点)
- 一对多 → `split_party.station_id`(分账参与方按站点划分)

### 业务规则

- **创建**:客户运营在 PC 后台"站点管理" → 填名称 / 地址 / 经纬度 / 营业时间 → INSERT
- **找桩查询**:小程序"找桩"页 → user 服务查 `status='enabled' AND deleted_at IS NULL` 的站点 → 按距离排序展示
- **删除 / 停用**:`UPDATE status='disabled', deleted_at=NOW(), deleted_by=$操作人.id`(软删);关联设备仍存在,但 `device_meta.station_id` 保留(便于追溯历史订单)
- **总端口数同步**:worker 每日扫表 → 计算 `device_meta WHERE station_id=本.id AND deleted_at IS NULL` 的端口总数 → UPDATE `station.total_ports`

---

## 表 9:`admin_db.device_meta`

**业务说明**:**设备配置元数据**(冗余自 `gateway_db.device`,加速 admin 查询)。客户运营在 PC 后台"设备管理"查看所有设备的型号 / 固件版本 / 所属站点 / 在线状态。

**关键业务规则**:

- **缓存性质**:由 worker 周期同步自 gateway_db,admin_db 这份是只读副本
- **不软删除**:worker 同步时 UPSERT,源数据删除由 worker 覆盖更新
- 软启用 / 停用(`status` 字段,用于客户主动停用某台设备)

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `device_id` | `VARCHAR(32)` | UNIQUE, NOT NULL | — | 设备 ID(冗余自 gateway_db,§ 6.2 格式) |
| `vendor_id` | `VARCHAR(8)` | NOT NULL | — | 厂商 ID |
| `station_id` | `BIGINT UNSIGNED` | NULL | NULL | 所属站点 |
| `model` | `VARCHAR(64)` | NULL | NULL | 设备型号 |
| `total_ports` | `TINYINT UNSIGNED` | NOT NULL | `0` | 端口数 |
| `firmware_version` | `VARCHAR(32)` | NULL | NULL | 当前固件版本 |
| `online` | `BOOLEAN` | NOT NULL | `FALSE` | 在线状态(worker 从 gateway_db 同步) |
| `last_online_at` | `DATETIME(3)` | NULL | NULL | 最近在线时间 |
| `installed_at` | `DATE` | NULL | NULL | 安装日期 |
| `status` | `ENUM('enabled','disabled','maintenance','retired')` | NOT NULL | `'enabled'` | 设备状态:启用 / 停用 / 维护中 / 退役 |
| `last_sync_at` | `DATETIME(3)` | NOT NULL | — | 最近同步时间 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_device_meta` | `id` | 主键 | — |
| `uk_device_meta_device_id` | `device_id` | 唯一 | 按 device_id 查 |
| `idx_device_meta_station_status` | `station_id`, `status` | 普通 | 查某站点下的所有设备 |
| `idx_device_meta_vendor_status` | `vendor_id`, `status` | 普通 | 查某厂商的所有设备 |
| `idx_device_meta_online_sync` | `online`, `last_sync_at` | 普通 | 找需要重新同步的设备 |

### 约束

- `station_id` 必须指向有效 `station.id`(应用层校验)
- `online=FALSE` 且 `NOW() - last_online_at > 1 HOUR` → 标记"可能离线"

### 关系

- 多对一 → `station.id`(跨服务无外键,逻辑关联)
- 多对一 → `gateway_db.device`(跨服务,worker 同步)

### 业务规则

- **同步**:worker 每 5 min 跑一次 → 拉 `gateway_db.device` 全部设备 → UPSERT `device_meta`(新增 / 更新字段 / 设置 `online`)
- **客户运营查看**:PC 后台"设备管理" → 按站点 / 厂商 / 状态筛选 → 列表展示
- **停用**:客户运营主动 `UPDATE status='disabled'`(设备仍在 gateway_db,但停止接受订单)
- **退役**:设备物理拆除 → `UPDATE status='retired'`(保留历史数据,不再同步)
- **OTA 推送**:推送时按 `device_meta` 的型号 + 固件版本匹配 `ota_package`

---

## 表 10:`admin_db.pricing_rule`

**业务说明**:**计费规则实例**。客户运营在 PC 后台"计费管理"配置。每个 `station`(或设备组)绑定一个 `pricing_rule_id`,充电订单按此规则计费。

**关键业务规则**:

- **计费维度**:按时间(元/小时) / 按电量(元/度) / 阶梯电价 / 时段电价(峰平谷)
- 软删除启用:规则改版 → 旧规则软删,新规则启用(订单保留旧规则 ID 用于追溯)
- **价费分离**:电费 + 服务费 分别配置(需求文档 § 7.2)

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `rule_name` | `VARCHAR(64)` | NOT NULL | — | 规则名称(如"万达广场 - 白天") |
| `billing_mode` | `ENUM('by_time','by_kwh','tiered','time_of_use')` | NOT NULL | — | 计费模式 |
| `template_id` | `BIGINT UNSIGNED` | NULL | NULL | 关联 `pricing_template.id`(可空,客户自定义规则不基于模板) |
| `electric_fee_mode` | `ENUM('flat','tiered','time_of_use')` | NOT NULL | — | 电费模式 |
| `electric_fee_config` | `JSON` | NOT NULL | — | 电费配置 JSON:按模式不同(`{type:'flat',cents_per_kwh:80}` / `{type:'tiered',tiers:[{min:0,max:200,cents:80},{min:200,max:null,cents:120}]}` / `{type:'time_of_use',peak:{hours:'08:00-21:00',cents:120},off_peak:{hours:'21:00-08:00',cents:50}}`) |
| `service_fee_mode` | `ENUM('flat','percentage','by_time','by_kwh')` | NOT NULL | — | 服务费模式 |
| `service_fee_config` | `JSON` | NOT NULL | — | 服务费配置 JSON(类似 electric_fee_config) |
| `min_charge_cents` | `BIGINT` | NOT NULL | `0` | 最低消费(分),0 = 无最低消费 |
| `max_charge_cents` | `BIGINT` | NULL | NULL | 最高消费(分),NULL = 无上限 |
| `valid_from` | `DATETIME(3)` | NOT NULL | — | 生效时间 |
| `valid_until` | `DATETIME(3)` | NULL | NULL | 失效时间(NULL = 永久) |
| `status` | `ENUM('enabled','disabled')` | NOT NULL | `'enabled'` | 启用 / 停用 |
| `created_by` | `BIGINT UNSIGNED` | NOT NULL | — | 创建人 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_pricing_rule` | `id` | 主键 | — |
| `idx_pricing_rule_status_valid` | `status`, `valid_from`, `valid_until` | 普通 | 充电时查"当前生效"的规则 |
| `idx_pricing_rule_template` | `template_id` | 普通 | 反查某模板的所有规则实例 |
| `idx_pricing_rule_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `electric_fee_config` / `service_fee_config` JSON 结构必须符合对应模式(应用层校验)
- `max_charge_cents >= min_charge_cents`(应用层校验)

### 关系

- 多对一 → `pricing_template.id`(可空)
- 一对多 → `device_meta.pricing_rule_id`(隐式关联,通过 station 间接)
- 一对多 → `charge_order.charge_rule_id`(历史订单引用)

### 业务规则

- **创建**:客户运营在 PC 后台"计费管理"新建 → 选计费模式 + 填配置 → INSERT
- **充电时计算**:user 服务收到充电结束事件 → 查 `pricing_rule WHERE status='enabled' AND valid_from <= NOW() AND (valid_until IS NULL OR valid_until > NOW())` → 按配置计算电费 + 服务费
- **改版**:客户运营改规则 → 旧规则 `UPDATE deleted_at=NOW()`(软删,保留历史订单引用)+ 新规则 INSERT;**已开始的订单不受影响**(用旧规则计算)
- **时段电价**:worker 每日 00:00 检查时段切换 → 实际计费在订单结束时按各时段分段计算

---

## 表 11:`admin_db.pricing_template`

**业务说明**:**计费规则模板**(预置常用方案,客户运营一键复用,再按需调整)。降低客户运营配置成本。

**关键业务规则**:

- **预置模板 + 客户自配模板**:`built_in=TRUE` 为系统预置,`FALSE` 为客户自配
- 模板与规则分离:模板是"快速复用"的样板,规则是"实际生效"的实例
- **不软删除**:模板是配置,启用 / 停用即可

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `template_name` | `VARCHAR(64)` | NOT NULL | — | 模板名称(如"小区基础模板 - 1 元/度 + 0.5 元/小时服务费",中文默认) |
| `template_name_i18n` | `JSON` | NULL | NULL | **多语言名称**(技术规格 § 15.8:本期只填 `{"zh-CN": "..."}`) |
| `billing_mode` | `ENUM('by_time','by_kwh','tiered','time_of_use')` | NOT NULL | — | 计费模式 |
| `electric_fee_config` | `JSON` | NOT NULL | — | 电费配置 JSON(同 pricing_rule) |
| `service_fee_config` | `JSON` | NOT NULL | — | 服务费配置 JSON |
| `min_charge_cents` | `BIGINT` | NOT NULL | `0` | 最低消费 |
| `max_charge_cents` | `BIGINT` | NULL | NULL | 最高消费 |
| `description` | `VARCHAR(256)` | NULL | NULL | 模板描述(适用场景说明) |
| `built_in` | `BOOLEAN` | NOT NULL | `FALSE` | 是否预置(预置不可删) |
| `status` | `ENUM('enabled','disabled')` | NOT NULL | `'enabled'` | 启用 / 停用 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_pricing_template` | `id` | 主键 | — |
| `idx_pricing_template_status_builtin` | `status`, `built_in` | 普通 | PC 后台列模板(预置 vs 自配分组) |

### 约束

- `built_in=TRUE` 时不可删除(应用层)
- `electric_fee_config` / `service_fee_config` JSON 结构必须符合对应模式

### 关系

- 一对多 → `pricing_rule.template_id`(基于模板创建的规则实例)

### 业务规则

- **预置模板**:系统初始化脚本 INSERT 5-10 个常用模板(如"小区基础"、"商场高峰"、"园区低谷"、"按时间 1 元/h"、"按电量 0.8 元/度")
- **客户运营使用**:PC 后台"计费管理" → 选模板 → 改配置 → "基于此模板新建规则" → INSERT `pricing_rule` 拷贝模板字段
- **停用**:`UPDATE status='disabled'`(已基于此模板创建的规则不受影响)

---

## 表 12:`admin_db.coupon`

**业务说明**:**优惠券模板**(用户在客户端看到的优惠券规格定义)。客户运营在 PC 后台"营销管理"配置。**不发模板给用户** —— 发的是 `user_db.coupon_grant` 发放记录。

**关键业务规则**:

- **模板与发放记录分离**:模板是配置(可改),发放记录是实例(不可改)
- **3 种类型**:固定金额(`fixed_amount`)/ 百分比折扣(`percentage`)/ 满减(`full_reduction`)
- **不软删除**:模板是配置,启用 / 停用即可
- 客户级配置:每个模板属于当前客户(单客户部署)

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `name` | `VARCHAR(64)` | NOT NULL | — | 优惠券名称(用户可见,如"新人 5 元抵扣券",中文默认) |
| **`name_i18n`** | `JSON` | NULL | NULL | **多语言名称**(技术规格 § 15.8:本期只填 `{"zh-CN": "..."}`,en-US 等二期补) |
| `coupon_type` | `ENUM('fixed_amount','percentage','full_reduction')` | NOT NULL | — | 类型:固定金额 / 百分比折扣 / 满减 |
| `discount_cents` | `BIGINT` | NULL | NULL | 优惠金额(分);`fixed_amount` / `full_reduction` 时填 |
| `discount_percent` | `DECIMAL(5,2)` | NULL | NULL | 折扣百分比(0-100,精度 0.01);`percentage` 时填 |
| `max_discount_cents` | `BIGINT` | NULL | NULL | 折扣上限(分);`percentage` 时填 |
| `min_spend_cents` | `BIGINT` | NULL | NULL | 最低消费(分);`full_reduction` 时必填 |
| `valid_days` | `SMALLINT UNSIGNED` | NULL | NULL | 领取后有效天数 |
| `valid_from` | `DATETIME(3)` | NULL | NULL | 固定生效时间 |
| `valid_until` | `DATETIME(3)` | NULL | NULL | 固定失效时间 |
| `total_limit` | `INT UNSIGNED` | NULL | NULL | 总发放数量上限(NULL = 无上限) |
| `granted_count` | `INT UNSIGNED` | NOT NULL | `0` | 已发放数量 |
| `user_limit` | `INT UNSIGNED` | NOT NULL | `1` | 单用户最多持有数量 |
| `scope` | `ENUM('all','specific_station','specific_device')` | NOT NULL | `'all'` | 适用范围 |
| `scope_ids` | `JSON` | NULL | NULL | 适用范围 ID 列表 |
| `status` | `ENUM('enabled','disabled','archived')` | NOT NULL | `'enabled'` | 启用 / 停用 / 归档 |
| `created_by` | `BIGINT UNSIGNED` | NOT NULL | — | 创建人 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_coupon` | `id` | 主键 | — |
| `idx_coupon_status_valid` | `status`, `valid_until` | 普通 | 查可发放的有效模板 |

### 约束

- **类型字段对应**:
  - `coupon_type='fixed_amount'` → `discount_cents` NOT NULL,其他可空
  - `coupon_type='percentage'` → `discount_percent` NOT NULL
  - `coupon_type='full_reduction'` → `discount_cents` + `min_spend_cents` NOT NULL
- `valid_days` 与 `valid_from`+`valid_until` 二选一(应用层校验)
- `granted_count <= total_limit`

### 关系

- 一对多 → `user_db.coupon_grant.coupon_id`(每个发券实例关联模板)

### 业务规则

- **创建**:客户运营在 PC 后台"营销管理 → 优惠券模板"新建 → 填类型/面值/门槛 → INSERT
- **发放**:admin 发布 `coupon_grant_required_stream`,user 消费并写入自己 schema 的 `coupon_grant`;admin 收到幂等结果后更新本表 `granted_count`,不直写 `user_db`
- **停用 / 归档**:UPDATE `status='disabled'/'archived'`,已发放的 `coupon_grant` 不受影响

---

## 表 13:`admin_db.split_template`

**业务说明**:**分账模板**。定义多方分账方案(N ≤ 8,需求文档 § 9.2)。客户运营在 PC 后台"分账管理"配置,关联 `pricing_rule` 使用。

**关键业务规则**:

- **多方分账**:N ≤ 8(需求文档 § 9.2 已定);参与方通过 `split_party` 关联
- **分账模式**:A. 电费 + 服务费分账 / B. 仅服务费分账(系统仅记账不接入电网,需求文档 § 9.2)
- 软删除启用:分账模板改版软删,旧订单仍按旧模板分账

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `template_name` | `VARCHAR(64)` | NOT NULL | — | 模板名称(如"万达广场 - 物业分成 30%",中文默认) |
| `template_name_i18n` | `JSON` | NULL | NULL | **多语言名称**(技术规格 § 15.8:本期只填 `{"zh-CN": "..."}`) |
| `split_mode` | `ENUM('electric_and_service','service_only')` | NOT NULL | — | 分账模式(A / B) |
| `total_party_count` | `TINYINT UNSIGNED` | NOT NULL | — | 参与方总数(2-8) |
| `description` | `VARCHAR(256)` | NULL | NULL | 模板描述 |
| `status` | `ENUM('enabled','disabled')` | NOT NULL | `'enabled'` | 启用 / 停用 |
| `created_by` | `BIGINT UNSIGNED` | NOT NULL | — | 创建人 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_split_template` | `id` | 主键 | — |
| `idx_split_template_status_deleted` | `status`, `deleted_at` | 普通 | 客户运营查模板列表 |
| `idx_split_template_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `total_party_count` 范围 2-8(应用层校验)
- `total_party_count` 必须等于该模板下 `split_party` 的实际数量(应用层校验)

### 关系

- 一对多 → `split_party.template_id`(本模板的所有参与方)

### 业务规则

- **创建**:客户运营在 PC 后台"分账管理" → 选模式 + 填参与方(每个参与方配比例 / 银行账号)→ INSERT `split_template` + INSERT N 条 `split_party`(同事务)
- **修改**:参与方变更 → UPDATE `split_template.total_party_count` + 调整 `split_party`(应用层校验总比例 = 100%)
- **删除 / 停用**:`UPDATE status='disabled', deleted_at=NOW(), deleted_by=$操作人.id`;关联历史订单仍按旧模板分账

---

## 表 14:`admin_db.split_party`

**业务说明**:**分账参与方**(模板实例)。每个 `split_template` 对应 N 条 `split_party`,记录每个参与方的银行账号 / 比例 / 类型。

**关键业务规则**:

- **比例总和必须 = 100%**(应用层校验,创建 / 修改时)
- 参与方类型:平台运营 / 物业 / 加盟商 / 业主 / 客户(自己)等
- **软删除启用**:参与方被踢出分账 → 软删(历史订单仍按原比例)

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `template_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `split_template.id` |
| `party_name` | `VARCHAR(64)` | NOT NULL | — | 参与方名称(如"万达物业"、"平台运营") |
| `party_type` | `ENUM('platform','property','franchisee','owner','customer_self','other')` | NOT NULL | — | 参与方类型 |
| `split_percent` | `DECIMAL(5,2)` | NOT NULL | — | 分账比例(% 精度 0.01,如 30.00 = 30%) |
| **`settlement_cycle`** | `ENUM('daily','weekly','monthly')` | NOT NULL | `'monthly'` | **结算周期**(需求 § 9.3,日清 / 周结 / 月结) |
| `bank_account_name` | `VARCHAR(64)` | NULL | NULL | 银行账户名 |
| `bank_account_no_enc` | `VARBINARY(255)` | NULL | NULL | 银行账号 AES_ENCRYPT 密文(§ 9.3) |
| `bank_name` | `VARCHAR(64)` | NULL | NULL | 开户行 |
| `contact_phone_enc` | `VARBINARY(255)` | NULL | NULL | 联系电话 AES_ENCRYPT |
| `remark` | `VARCHAR(256)` | NULL | NULL | 备注 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_split_party` | `id` | 主键 | — |
| `idx_split_party_template` | `template_id`, `deleted_at` | 普通 | 查某模板的所有参与方 |
| `idx_split_party_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- 同一 `template_id` 下未删除的 `split_party.split_percent` 之和必须 = 100%(应用层校验)
- `split_percent` 范围 0-100,精度 0.01

### 关系

- 多对一 → `split_template.id`

### 业务规则

- **创建**:与 `split_template` 同事务创建,总比例 = 100% 校验
- **修改比例**:`UPDATE split_percent`(应用层校验总比例仍 = 100%)
- **踢出**:`UPDATE deleted_at=NOW()`(软删,历史订单仍按原比例分账)
- **分账执行**:billing 服务按 `split_party.split_percent` 计算每方应得金额 → 写 `billing_db.settlement`(待第二批 billing.md 设计)

---

## 表 15:`admin_db.webhook_subscription`

**业务说明**:**Webhook 订阅**。客户运维在 PC 后台配置接收事件的 URL,系统按事件类型推送(订单事件 / 设备事件 / 告警事件)。**HMAC-SHA256 签名**(§ 9.5)。

**关键业务规则**:

- **事件类型**:可订阅多种事件(订单创建 / 订单结束 / 退款 / 设备告警 / OTA 完成等)
- **HMAC 签名**:每次推送都带 `X-Signature` header,接收方校验
- 软删除启用:订阅停用软删,保留审计
- **密钥管理**:`secret` 生成时仅返回一次,丢失需重新生成

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `subscription_name` | `VARCHAR(64)` | NOT NULL | — | 订阅名称(便于客户运维识别) |
| `target_url` | `VARCHAR(512)` | NOT NULL | — | 接收 URL(HTTPS 强制) |
| `secret` | `VARCHAR(64)` | NOT NULL | — | HMAC 签名密钥(生成时返回一次,数据库存明文,**应用层需加密存储**) |
| `event_types` | `JSON` | NOT NULL | — | 订阅事件类型数组(如 `["order.created","order.ended","refund.success","device.alert"]`) |
| `status` | `ENUM('enabled','disabled')` | NOT NULL | `'enabled'` | 启用 / 停用 |
| `last_delivery_at` | `DATETIME(3)` | NULL | NULL | 最近推送时间 |
| `last_delivery_status` | `ENUM('success','failed','timeout')` | NULL | NULL | 最近推送状态 |
| `consecutive_fail_count` | `TINYINT UNSIGNED` | NOT NULL | `0` | 连续失败次数(≥ 10 → 自动禁用 + 告警) |
| `created_by` | `BIGINT UNSIGNED` | NOT NULL | — | 创建人 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_webhook_subscription` | `id` | 主键 | — |
| `idx_webhook_subscription_status_deleted` | `status`, `deleted_at` | 普通 | worker 查"启用且未删除"的订阅 |
| `idx_webhook_subscription_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `target_url` 必须 HTTPS(应用层校验)
- `consecutive_fail_count >= 10` → 自动 `status='disabled'`(应用层 + admin API 校验)
- `event_types` JSON 不能为空数组

### 关系

- 一对多 → `webhook_delivery_log.subscription_id`(本订阅的所有推送记录)

### 业务规则

- **创建**:客户运维在 PC 后台"Webhook 配置"新建 → 填名称 / URL / 选事件类型 → 系统生成 `secret`(32 位随机串)→ **首次返回 secret 明文,后续不可查** + INSERT
- **重新生成密钥**:客户运维手动触发 → 生成新 secret + UPDATE(老密钥立即失效)
- **推送**:worker 消费 `webhook_retry_stream` → 按订阅推送事件 → HMAC-SHA256 签名 → 接收方校验
- **失败重试**:HTTP 5xx / 超时(> 10s)→ 重试 3 次(指数退避)→ 累计 `consecutive_fail_count`;成功后归 0
- **自动禁用**:`consecutive_fail_count >= 10` → `UPDATE status='disabled'` + 告警客户运维

---

## 表 16:`admin_db.webhook_delivery_log`

**业务说明**:**Webhook 推送日志**(每次推送一条)。记录推送 URL / 事件 / 响应 / 重试次数 / 状态。**按月分区 + 物理归档**,**不软删除**。

**关键业务规则**:

- **不软删除**:日志类,只追加
- 按月分区:加速查询 + 按月归档
- 物理归档:超 90 天物理清理(短保留期,日志量大)

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `subscription_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `webhook_subscription.id` |
| `event_type` | `VARCHAR(64)` | NOT NULL | — | 事件类型 |
| `event_id` | `VARCHAR(64)` | NOT NULL | — | 事件 ID(Redis Stream event_id) |
| `request_payload` | `JSON` | NOT NULL | — | 推送 payload JSON |
| `request_url` | `VARCHAR(512)` | NOT NULL | — | 实际推送 URL(冗余 subscription_id 当时的 URL) |
| `response_status_code` | `INT` | NULL | NULL | HTTP 响应码 |
| `response_body` | `TEXT` | NULL | NULL | 响应 body(限制长度,避免过大) |
| `response_time_ms` | `INT UNSIGNED` | NULL | NULL | 响应耗时(毫秒) |
| `status` | `ENUM('success','failed','timeout','retrying')` | NOT NULL | — | 推送状态 |
| `retry_count` | `TINYINT UNSIGNED` | NOT NULL | `0` | 已重试次数 |
| `error_message` | `VARCHAR(512)` | NULL | NULL | 错误信息 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 推送时间 |
| `partition_key` | `DATE` | NOT NULL | — | 分区键 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_webhook_delivery_log` | `id` | 主键 | — |
| `idx_webhook_delivery_log_subscription_created` | `subscription_id`, `created_at` | 普通 | 客户运维查某订阅的推送历史 |
| `idx_webhook_delivery_log_event` | `event_type`, `event_id` | 普通 | 反查"某事件推送给哪些订阅" |
| `idx_webhook_delivery_log_status_created` | `status`, `created_at` | 普通 | 查失败 / 重试中的推送 |

### 约束

- `status='success'` 时,`response_status_code` 必须 2xx
- `status IN ('failed','timeout')` 时,`error_message` 必须 NOT NULL

### 关系

- 多对一 → `webhook_subscription.id`

### 业务规则

- **写入**:worker 每次推送(成功 / 失败 / 重试)都 INSERT 一条
- **查询**:客户运维在 PC 后台"Webhook 日志"查历史 → 按订阅 / 事件 / 状态筛选
- **物理归档**:worker 每日扫表 → `created_at < NOW() - 90 DAY` → `DELETE`(DROP PARTITION)

---

## 表 17:`admin_db.ota_package`

**业务说明**:**OTA 固件包元数据**。客户运维在 PC 后台"OTA 管理"上传固件,系统按设备型号推送。**固件文件存对象存储**(本地 MinIO 或云 OSS),本表只存元数据。

**关键业务规则**:

- **固件文件不入库**:存对象存储,本表存 `firmware_url` + `sha256` + `signature`
- **签名验证**:`signature` 用厂商私钥签名(SHA-256 + RSA),设备端验签(§ 6.6)
- **不软删除**:包元数据是审计需要,启用 / 停用即可

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `package_name` | `VARCHAR(128)` | NOT NULL | — | 包名称(如"v1.2.3-stability") |
| `version` | `VARCHAR(32)` | NOT NULL | — | 版本号 |
| `vendor_id` | `VARCHAR(8)` | NOT NULL | — | 适用厂商 |
| `model` | `VARCHAR(64)` | NOT NULL | — | 适用设备型号 |
| `firmware_url` | `VARCHAR(512)` | NOT NULL | — | 固件文件 OSS URL |
| `file_size_bytes` | `BIGINT UNSIGNED` | NOT NULL | — | 文件大小(字节) |
| `sha256` | `CHAR(64)` | NOT NULL | — | SHA-256 哈希 |
| `signature` | `VARCHAR(512)` | NOT NULL | — | 厂商私钥签名(Base64) |
| `changelog` | `TEXT` | NULL | NULL | 更新日志 |
| `release_notes` | `TEXT` | NULL | NULL | 发布说明(用户可见,可选) |
| `status` | `ENUM('draft','published','retired')` | NOT NULL | `'draft'` | 状态:草稿 / 已发布 / 退役 |
| `uploaded_by` | `BIGINT UNSIGNED` | NOT NULL | — | 上传人 |
| `uploaded_at` | `DATETIME(3)` | NOT NULL | — | 上传时间 |
| `published_at` | `DATETIME(3)` | NULL | NULL | 发布时间 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_ota_package` | `id` | 主键 | — |
| `uk_ota_package_vendor_model_version` | `vendor_id`, `model`, `version` | 唯一 | 防重复上传同版本 |
| `idx_ota_package_status_published` | `status`, `published_at` | 普通 | 查"已发布"的固件 |

### 约束

- `(vendor_id, model, version)` 三元组唯一(应用层校验)
- `status='published'` 时,`published_at` NOT NULL
- `file_size_bytes <= 50 MB`(应用层校验,避免超大包)

### 关系

- 一对多 → `ota_schedule.package_id`(基于本包创建的多次推送)

### 业务规则

- **上传**:客户运维在 PC 后台"OTA 管理"上传固件 → 系统算 SHA-256 + 签名 → 上传 OSS → INSERT `ota_package(status='draft')`
- **发布**:`UPDATE status='published', published_at=NOW()`(推送前发布)
- **推送**(全量):客户运维选定目标设备范围 → 创建 `ota_schedule` → worker 推送
- **退役**:`UPDATE status='retired'`(不再推送,保留审计)
- **设备验签**:设备下载固件 → 校验 `signature` → 失败则立即回滚(§ 6.6)

---

## 表 18:`admin_db.ota_schedule`

**业务说明**:**OTA 推送调度**。每次推送固件到一批设备 = 一条 `ota_schedule`。记录推送目标 / 调度时间 / 进度 / 结果。

**关键业务规则**:

- **全量推送**:需求文档已定,不支持灰度(同一型号全部设备)
- **进度跟踪**:`success_count` / `failed_count` / `pending_count` 实时更新
- 软删除启用:调度取消软删

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `package_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `ota_package.id` |
| `schedule_name` | `VARCHAR(128)` | NOT NULL | — | 调度名称 |
| `target_filter` | `JSON` | NOT NULL | — | 目标设备筛选条件(如 `{vendor_id:'xx',model:'yy',status:'enabled'}`) |
| `target_count` | `INT UNSIGNED` | NOT NULL | `0` | 目标设备总数 |
| `success_count` | `INT UNSIGNED` | NOT NULL | `0` | 升级成功数 |
| `failed_count` | `INT UNSIGNED` | NOT NULL | `0` | 升级失败数 |
| `pending_count` | `INT UNSIGNED` | NOT NULL | `0` | 待升级数 |
| `scheduled_at` | `DATETIME(3)` | NOT NULL | — | 计划推送时间 |
| `started_at` | `DATETIME(3)` | NULL | NULL | 实际开始时间 |
| `completed_at` | `DATETIME(3)` | NULL | NULL | 完成时间 |
| `status` | `ENUM('pending','running','completed','failed','cancelled')` | NOT NULL | `'pending'` | 调度状态 |
| `failure_reason` | `VARCHAR(256)` | NULL | NULL | 失败原因 |
| `created_by` | `BIGINT UNSIGNED` | NOT NULL | — | 创建人 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_ota_schedule` | `id` | 主键 | — |
| `idx_ota_schedule_package_status` | `package_id`, `status` | 普通 | 反查某包的所有调度 |
| `idx_ota_schedule_status_scheduled` | `status`, `scheduled_at` | 普通 | 查待执行的调度 |
| `idx_ota_schedule_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `target_count = success_count + failed_count + pending_count`(应用层 + 触发器保证)
- `status='completed'` 时,`pending_count = 0` 且 `completed_at` NOT NULL
- `status='cancelled'` 时,调度停止,设备不再推送

### 关系

- 多对一 → `ota_package.id`

### 业务规则

- **创建**:客户运维选固件包 → 填目标筛选 → INSERT `ota_schedule(target_count=按筛选实时计算, status='pending')`
- **执行**:到 `scheduled_at` → worker UPDATE `status='running', started_at=NOW()` → 通过 `ota_schedule_stream` 推送 → 设备回执 `ota_apply_result` → 更新各计数
- **失败检测**(§ 6.6):gateway 30 min 内未收到设备心跳 → 视为失败 → UPDATE `failed_count += 1`
- **完成**:所有设备回执后 → `status='completed', completed_at=NOW()`
- **取消**:`UPDATE status='cancelled', deleted_at=NOW()`(软删)

---

## 表 19:`admin_db.alert_rule`

**业务说明**:**告警规则**(§ 3.1.4 客户自配原则)。客户运维在 PC 后台"告警规则"自定义监测条件(过流 / 过温 / SOC 异常 / 通信中断等),触发后推送告警事件。

**关键业务规则**:

- **不预设默认值**(§ 3.1.4 决策):客户全部自配,避免误报
- 字段:device 筛选 / 监测字段 / 阈值 / 持续时长 / 严重程度 / 启用
- 软删除启用

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `rule_name` | `VARCHAR(128)` | NOT NULL | — | 规则名称(如"电流过载告警") |
| `device_filter` | `JSON` | NOT NULL | — | 适用设备筛选(如 `{vendor_id:'xx',model:'yy',station_id:1}` 或 `*` 表示全部) |
| `metric` | `ENUM('voltage_v','current_a','temperature_c','battery_soc','power_w','meter_kwh','relay_status','charge_state')` | NOT NULL | — | 监测字段 |
| `op` | `ENUM('gt','lt','neq','between')` | NOT NULL | — | 比较运算符(> / < / != / 区间) |
| `threshold` | `JSON` | NOT NULL | — | 阈值 JSON(`{value:32}` 或 `{min:30,max:35}` 或 `{eq:'fault'}`) |
| `window_seconds` | `INT UNSIGNED` | NOT NULL | `0` | 持续时长(秒,0 = 立即触发;> 0 表示持续 N 秒才触发) |
| **`charge_duration_max_seconds`** | `INT UNSIGNED` | NULL | NULL | **充电超时阈值**(秒,需求 § 8.4 GB 47371 合规 >10h = 36000;NULL = 不启用超时检测;触发后 is_auto_poweroff 自动断电) |
| `severity` | `ENUM('low','mid','high')` | NOT NULL | — | **严重程度三级**(需求 § 7.4 / § 8.4):low 提示 / mid 推送 / high 自动断电 |
| **`is_auto_poweroff`** | `BOOLEAN` | NOT NULL | `FALSE` | **是否自动断电**(需求 § 8.4:高级别告警自动断电;仅 `severity='high'` 时可设 TRUE) |
| `enabled` | `BOOLEAN` | NOT NULL | `TRUE` | 是否启用 |
| `trigger_count_24h` | `INT UNSIGNED` | NOT NULL | `0` | 24h 内触发次数(用于"频繁告警"识别) |
| `last_triggered_at` | `DATETIME(3)` | NULL | NULL | 最近触发时间 |
| `created_by` | `BIGINT UNSIGNED` | NOT NULL | — | 创建人 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_alert_rule` | `id` | 主键 | — |
| `idx_alert_rule_enabled_deleted` | `enabled`, `deleted_at` | 普通 | gateway 查"启用且未删除"的规则(实时匹配) |
| `idx_alert_rule_metric_severity` | `metric`, `severity` | 普通 | 客户运维按字段 / 程度查规则 |
| `idx_alert_rule_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `op='between'` 时,`threshold` 必须 `{min, max}` 两个值
- `op IN ('gt','lt','neq')` 时,`threshold` 必须 `{value}` 单值
- `window_seconds >= 0`(0 = 立即触发)

### 关系

- 一对多 → `alert_subscription.rule_id`(本规则可配置多个订阅通道)
- 一对多 → `alert_rule` 触发产生的告警事件(写入 Redis Stream)

### 业务规则

- **创建**:客户运维在 PC 后台"告警规则"新建 → 选设备 / 字段 / 阈值 / 严重程度 → INSERT
- **实时匹配**:gateway 每次写入 telemetry 时,加载所有 `enabled=TRUE` 规则 → 内存匹配 → 命中则发布 `alert_stream` 事件
- **window_seconds 防抖**:`window_seconds > 0` 时,gateway 需维护"过去 N 秒状态"判断持续性(可用 Redis SET NX + EXPIRE 实现)
- **频繁告警**:`trigger_count_24h > 100` → 自动 `enabled=FALSE` + 告警客户运维(避免告警风暴)
- **启用 / 停用**:`UPDATE enabled`(不软删,运维可快速重启用)

---

## 表 20:`admin_db.alert_subscription`

**业务说明**:**告警订阅**(规则与推送通道的关联)。每条规则可配置多个订阅通道(Webhook / 邮件)。

**关键业务规则**:

- **唯一通道 = Webhook**(§ 3.1.4 决策);邮件本期不内置,客户可通过 Webhook 自行集成(如邮件网关)
- 软删除启用:订阅停用软删

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `rule_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `alert_rule.id` |
| `channel` | `ENUM('webhook','email')` | NOT NULL | — | 推送通道(本期仅 webhook 真用) |
| `target` | `VARCHAR(512)` | NOT NULL | — | 推送目标(Webhook URL / 邮箱地址) |
| `severity_filter` | `ENUM('all','critical_only','fatal_only')` | NOT NULL | `'all'` | 严重程度筛选 |
| `enabled` | `BOOLEAN` | NOT NULL | `TRUE` | 是否启用 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_alert_subscription` | `id` | 主键 | — |
| `idx_alert_subscription_rule_enabled_deleted` | `rule_id`, `enabled`, `deleted_at` | 普通 | 告警触发时查订阅列表 |
| `idx_alert_subscription_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `channel='webhook'` 时,`target` 必须是 HTTPS URL
- `channel='email'` 时,`target` 必须是邮箱格式

### 关系

- 多对一 → `alert_rule.id`

### 业务规则

- **创建**:客户运维在 PC 后台"告警订阅"为某条规则新建 → 选通道 + 填目标 → INSERT
- **推送**:worker 消费 `alert_stream` → 按 `rule_id` 查所有 enabled 订阅 → 按 `severity_filter` 过滤 → 推送
- **本期简化**:`channel='email'` 字段保留但本期不实现(客户走 webhook 自集成)

---

## 表 21:`admin_db.risk_config`

**业务说明**:**风控配置**(单例表,ID=1)。定义退款风控的频次 / 金额阈值(§ Q3 决策)。客户管理员在 PC 后台"风控配置"调整。

**关键业务规则**:

- **单例**:全表只有一条记录(ID=1)
- **不软删除**:配置类
- 阈值变更记录历史(可加 audit_log,二期再做历史版本表)

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | `1` | 单例 ID(永远 = 1) |
| `frequency_enabled` | `BOOLEAN` | NOT NULL | `TRUE` | 是否启用频次风控 |
| `frequency_count` | `TINYINT UNSIGNED` | NOT NULL | `3` | 频次阈值(同用户 N 笔触发) |
| `frequency_window_seconds` | `INT UNSIGNED` | NOT NULL | `300` | 频次时间窗(秒,默认 5 min) |
| `amount_rule_enabled` | `BOOLEAN` | NOT NULL | `FALSE` | 是否启用金额风控(**本期固定 FALSE**:二轮车 1-2 元/单场景下任何金额阈值均无意义;字段保留以便二期扩展) |
| `updated_by` | `BIGINT UNSIGNED` | NULL | NULL | 最近修改人 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_risk_config` | `id` | 主键 | — |

### 约束

- 应用层强制只有一条记录(ID=1)
- `frequency_count >= 2`(至少 2 笔才合理)
- `amount_threshold_cents > 0`

### 关系

- 无外键关系(单例配置)

### 业务规则

- **初始化**:系统首次部署时,初始化脚本 INSERT 一条默认记录(ID=1,频率=3/5min,金额规则 disabled)
- **客户管理员调整**:PC 后台"风控配置"页 → 改阈值 → UPDATE(变更即时生效)
- **触发逻辑**:admin 退款前查本 schema `risk_config` → 按规则判断是否冻结;若需写风控冻结记录,调用 user 内部接口由 user 写 `risk_freeze_log`,admin 不直写 `user_db`
- **缓存**:user 服务缓存本表配置(TTL 5 min,§ 4.7)

---

## 表 22:`admin_db.settled_record`

**业务说明**:**账单结清记录**。按账单期(自然月)记录客户应收 / 已收 / 状态。需求文档 § 13.3 + § 9.2 提及。

**关键业务规则**:

- **账单期**:自然月(每月 1 日 00:00:00 ~ 月末 23:59:59)
- **结清流程**:客户财务审核 → 标记已结清 → 影响"已结清后退款"权限(§ Q2 决策)
- 软删除启用:异常账单软删

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `period_year` | `SMALLINT UNSIGNED` | NOT NULL | — | 账单年(如 2026) |
| `period_month` | `TINYINT UNSIGNED` | NOT NULL | — | 账单月(1-12) |
| `period_start` | `DATETIME(3)` | NOT NULL | — | 账单期开始(冗余,避免计算) |
| `period_end` | `DATETIME(3)` | NOT NULL | — | 账单期结束 |
| `total_order_count` | `INT UNSIGNED` | NOT NULL | `0` | 订单总数 |
| `total_revenue_cents` | `BIGINT` | NOT NULL | `0` | 总收入(分) |
| `total_refund_cents` | `BIGINT` | NOT NULL | `0` | 总退款(分) |
| `net_revenue_cents` | `BIGINT` | NOT NULL | `0` | 净收入(分)= 总收入 - 总退款 |
| `status` | `ENUM('draft','pending_review','settled','disputed')` | NOT NULL | `'draft'` | 状态:草稿 / 待审核 / 已结清 / 有争议 |
| `settled_at` | `DATETIME(3)` | NULL | NULL | 结清时间 |
| `settled_by` | `BIGINT UNSIGNED` | NULL | NULL | 结清操作人(客户财务) |
| `dispute_note` | `VARCHAR(512)` | NULL | NULL | 争议备注 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_settled_record` | `id` | 主键 | — |
| `uk_settled_record_period` | `period_year`, `period_month` | 唯一 | 一月一账单 |
| `idx_settled_record_status` | `status`, `settled_at` | 普通 | 客户财务查待审核账单 |
| `idx_settled_record_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `(period_year, period_month)` 唯一(应用层 + 数据库唯一约束)
- `status='settled'` 时,`settled_at` / `settled_by` NOT NULL
- `status='disputed'` 时,`dispute_note` NOT NULL

### 关系

- 无外键(逻辑关联 `payment_order` 通过业务时间范围)

### 业务规则

- **生成**:worker 每月 1 日 02:00 自动生成上月账单 → 统计 `payment_order` WHERE created_at 在上月区间 → INSERT `settled_record(status='draft')`
- **审核**:客户财务在 PC 后台"账单管理" → 校验数据 → 通过 → `UPDATE status='settled', settled_at=NOW(), settled_by=$操作人.id`
- **结清后退款**:`status='settled'` 后,`payment_order.settled_at` 已填 → 仅 `customer_finance` 角色可发起退款(§ Q2)
- **争议**:`status='disputed'` 表示对账差异未解决 → 客户财务跟进 → 解决后 UPDATE `status='settled'`

---

## 表 23:`admin_db.finance_reconcile_log`

**业务说明**:**财务对账日志**(对账差异的处理记录)。与 `user_db.refund_reconcile_diff` 关联(差异本身在 user_db,处理过程在 admin_db)。

**关键业务规则**:

- 软删除启用
- 处理过程完整审计:发现 / 处理 / 解决

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `reconcile_diff_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `user_db.refund_reconcile_diff.id` |
| `reconcile_date` | `DATE` | NOT NULL | — | 对账日期 |
| `diff_type` | `VARCHAR(64)` | NOT NULL | — | 差异类型(冗余,便于查询) |
| `amount_cents` | `BIGINT` | NULL | NULL | 差异金额(分) |
| `processing_status` | `ENUM('pending','investigating','resolved','escalated')` | NOT NULL | `'pending'` | 处理状态 |
| `investigated_by` | `BIGINT UNSIGNED` | NULL | NULL | 调查人 |
| `investigated_at` | `DATETIME(3)` | NULL | NULL | 调查时间 |
| `resolution` | `ENUM('wechat_supplement','internal_supplement','customer_refund','platform_loss','ignored')` | NULL | NULL | 解决方案 |
| `resolution_note` | `VARCHAR(512)` | NULL | NULL | 解决方案备注 |
| `resolved_by` | `BIGINT UNSIGNED` | NULL | NULL | 处理人 |
| `resolved_at` | `DATETIME(3)` | NULL | NULL | 处理时间 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_finance_reconcile_log` | `id` | 主键 | — |
| `idx_finance_reconcile_log_diff` | `reconcile_diff_id` | 普通 | 反查对账差异的处理过程 |
| `idx_finance_reconcile_log_status_date` | `processing_status`, `reconcile_date` | 普通 | 客户财务查待处理项 |
| `idx_finance_reconcile_log_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `processing_status='investigating'` 时,`investigated_by` / `investigated_at` NOT NULL
- `processing_status='resolved'` 时,`resolution` / `resolution_note` / `resolved_by` / `resolved_at` NOT NULL
- `processing_status='escalated'` 时,`resolution_note` NOT NULL(说明升级原因)

### 关系

- 多对一 → `user_db.refund_reconcile_diff.id`(跨服务无外键)

### 业务规则

- **创建**:对账差异入 `user_db.refund_reconcile_diff` 后,自动同步创建 `finance_reconcile_log(processing_status='pending')`(worker 同步)
- **调查**:客户财务在 PC 后台"对账处理"查待处理项 → 选差异 → UPDATE `processing_status='investigating', investigated_by=$操作人.id`
- **解决**:调查完毕 → 选解决方案(微信补单 / 内部补记 / 客户退款 / 平台承担 / 忽略)→ UPDATE `processing_status='resolved', resolution, resolved_*`
- **升级**:处理不了 → `processing_status='escalated'` + 备注原因 → 客户管理员跟进
- **审计**:所有处理过程进入 `audit_log`

---

## 表 24:`admin_db.invoice_review`

**业务说明**:**发票审核记录**(客户财务审核 user_db.invoice_request 的过程)。user_db 存申请,admin_db 存审核过程;两者配合形成完整审计链。

**关键业务规则**:

- 与 `user_db.invoice_request` 配合:申请在 user_db,审核在 admin_db
- 审核动作:通过 / 拒绝 / 开票完成
- 软删除启用:误操作可软删

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `invoice_request_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `user_db.invoice_request.id` |
| `review_action` | `ENUM('reviewed','approved','rejected','issued','failed')` | NOT NULL | — | 审核动作:已审 / 通过 / 拒绝 / 已开票 / 开票失败 |
| `reviewer_id` | `BIGINT UNSIGNED` | NOT NULL | — | 审核人(客户财务) |
| `review_note` | `VARCHAR(512)` | NULL | NULL | 审核备注 |
| `invoice_no` | `VARCHAR(64)` | NULL | NULL | 发票号码(issued 时填) |
| `invoice_file_url` | `VARCHAR(512)` | NULL | NULL | 电子发票 OSS URL(issued 时填) |
| `issued_at` | `DATETIME(3)` | NULL | NULL | 开票时间 |
| `email_sent_at` | `DATETIME(3)` | NULL | NULL | 邮件发送时间 |
| `failure_reason` | `VARCHAR(256)` | NULL | NULL | 失败原因(failed 时填) |
| `reviewed_at` | `DATETIME(3)` | NOT NULL | — | 审核时间 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_invoice_review` | `id` | 主键 | — |
| `idx_invoice_review_invoice_request` | `invoice_request_id`, `reviewed_at` | 普通 | 反查某申请的所有审核动作 |
| `idx_invoice_review_reviewer_action` | `reviewer_id`, `review_action`, `reviewed_at` | 普通 | 客户财务查自身操作历史 |
| `idx_invoice_review_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `review_action='rejected'` 时,`review_note` 必须 NOT NULL
- `review_action='issued'` 时,`invoice_no` / `invoice_file_url` / `issued_at` NOT NULL
- `review_action='failed'` 时,`failure_reason` NOT NULL

### 关系

- 多对一 → `user_db.invoice_request.id`(跨服务无外键)

### 业务规则

- **审核动作记录**:客户财务对 `user_db.invoice_request` 做任何操作(审 / 通过 / 拒绝 / 开票 / 失败)→ INSERT `invoice_review`(同事务或紧跟事务)
- **同步状态**:`user_db.invoice_request.status` 与 `admin_db.invoice_review.review_action` 保持一致(通过 Redis Stream 事件同步)
- **开票**:审核通过后,客户财务开票 → INSERT `invoice_review(review_action='issued', invoice_*, issued_at)` + UPDATE `user_db.invoice_request(status='issued', invoice_file_url, ...)`
- **撤回**:审核后用户撤回 → INSERT `invoice_review(review_action='rejected', review_note='用户撤回')` + UPDATE `user_db.invoice_request(status='rejected')`

---

**admin_db 全部 23 张表设计完成**

> 文件结构:`通用约定` → `表清单(23 张)` → `关键架构决策` → 表 1 ~ 表 23 → 文档结束。
>
> **下一文件**:`docs/db/gateway.md`(gateway_db,设备 / 遥测 / 告警 / 会话)。

---

# 增补:告警事件持久化表

## 表 25:`admin_db.alert_event`

**业务说明**:**告警事件持久化表**(规则触发后的每条告警存一条)。原本只在 Redis Stream 流转,过期后无法查询;本表用于"最近 24h 哪些设备告警过 / 告警趋势"等查询。**按月分区 + 6 个月物理归档**。

**关键业务规则**:

- **每条告警 = 一行**:gateway 实时匹配 `alert_rule` → 触发后发布 `alert_stream` 事件 + INSERT 本表(异步批量)
- **状态机**:`active` 触发中 → `acknowledged` 运维确认 → `resolved` 已恢复
- **不软删除**:日志类,按月分区

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `event_no` | `CHAR(32)` | UNIQUE, NOT NULL | — | 告警单号,格式 `AL + YYYYMMDDHHmmss + 10 位随机` |
| `rule_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `alert_rule.id` |
| `device_id` | `VARCHAR(32)` | NOT NULL | — | 设备 ID |
| `port_id` | `VARCHAR(32)` | NULL | NULL | 端口 ID |
| `metric` | `VARCHAR(32)` | NOT NULL | — | 触发的监测字段(冗余自 alert_rule) |
| `severity` | `ENUM('low','mid','high')` | NOT NULL | — | 严重程度(冗余) |
| `is_auto_poweroff` | `BOOLEAN` | NOT NULL | `FALSE` | 是否触发自动断电 |
| `trigger_value` | `VARCHAR(64)` | NULL | NULL | 触发时的实际值(如 `current_a=35.5`) |
| `threshold_value` | `VARCHAR(64)` | NULL | NULL | 触发时的阈值(冗余自 alert_rule) |
| `triggered_at` | `DATETIME(3)` | NOT NULL | — | 触发时间 |
| `acknowledged_by` | `BIGINT UNSIGNED` | NULL | NULL | 确认人(客户巡检 / 客服) |
| `acknowledged_at` | `DATETIME(3)` | NULL | NULL | 确认时间 |
| `resolved_at` | `DATETIME(3)` | NULL | NULL | 恢复时间(条件消除后自动填) |
| `status` | `ENUM('active','acknowledged','resolved','false_positive')` | NOT NULL | `'active'` | 状态 |
| `resolution_note` | `VARCHAR(512)` | NULL | NULL | 处理备注 |
| `partition_key` | `DATE` | NOT NULL | — | 分区键 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_alert_event` | `id` | 主键 | — |
| `uk_alert_event_no` | `event_no` | 唯一 | 单号追溯 |
| `idx_alert_event_device_triggered` | `device_id`, `triggered_at` | 普通 | 查某设备的告警历史 |
| `idx_alert_event_status_triggered` | `status`, `triggered_at` | 普通 | 查未处理的告警 |
| `idx_alert_event_severity_triggered` | `severity`, `triggered_at` | 普通 | 按严重程度筛选 |
| `idx_alert_event_rule_triggered` | `rule_id`, `triggered_at` | 普通 | 查某规则的所有告警 |

### 约束

- `status='acknowledged'` 时,`acknowledged_by` / `acknowledged_at` NOT NULL
- `status='resolved'` 时,`resolved_at` NOT NULL
- `status='false_positive'` 时,`resolution_note` NOT NULL(说明误报原因)

### 关系

- 多对一 → `alert_rule.id`
- 多对一 → `gateway_db.device.id`(跨服务,无外键)

### 业务规则

- **触发**:gateway 实时匹配 `alert_rule` → 命中后 INSERT `alert_event(status='active')` + 发布 `alert_stream` 事件 → 订阅方推送 Webhook / 小程序
- **确认**:客户巡检 / 客服在 PC 后台"告警中心"看到 → 点击确认 → UPDATE `status='acknowledged', acknowledged_by, acknowledged_at`
- **恢复**:gateway 检测到设备状态恢复(条件不再满足)→ UPDATE `status='resolved', resolved_at`(自动)
- **误报**:运维标 `status='false_positive'` + 填原因(便于后续规则调优)
- **物理归档**:worker 每日扫表 → `triggered_at < NOW() - 6 MONTH` → `DELETE`(DROP PARTITION)

---

**admin_db 全部 24 张表设计完成**
