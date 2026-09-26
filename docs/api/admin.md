# admin 服务 API 详细设计

**服务**:`admin`(`services/admin`)
**对外地址**:`https://<customer-domain>/api/v1/admin/...`(经 Caddy 反代到 `admin:8082`)
**鉴权**:JWT(HS256,§ 7.3.2)+ 角色权限(`permission_codes`)
**OpenAPI 文档**:`GET /api/docs/openapi.json` + Swagger UI `/api/docs/swagger`

> **本文件覆盖范围**:admin 服务全部 HTTP 端点,共 **112 个**,按业务域分为 12 组。所有写操作均产生 `audit_log`(§ 3.3.1 admin/audit_log.rs)。**数据归属**:admin_db 是单客户专用(沿用 § 1.2 / § 13.2),跨服务数据通过 HTTP 调用获取,**禁止直连其他 schema**。

---

## 通用约定

### 请求格式

- 所有请求 / 响应均为 JSON
- Content-Type: `application/json; charset=utf-8`
- 时间戳统一 ISO 8601 字符串(UTC,带 `Z` 后缀),精度毫秒
- 金额字段:`*_cents` 后缀,**单位分**(避免浮点)
- 业务 ID:`*_id` / `*_no` 字符串 / 数字,按端点说明
- 分页参数:`page`(默认 1)、`page_size`(默认 20,最大 100)

### 统一响应包装

```json
{
  "code": 0,
  "message": "ok",
  "data": { ... },
  "request_id": "..."
}
```

### 鉴权要求(§ 7.3.2)

| 标记 | 含义 |
| --- | --- |
| `[公开]` | 无需 JWT,但仍需限流(防刷) |
| `[JWT]` | 任意已登录管理员 |
| `[角色]` | 需要特定 `permission_codes`,缺权限返回 `1003` |

**JWT Payload**(HS256 签发):

```json
{
  "user_id": 123,                  // admin user id(对应 admin_user_role.id)
  "role_id": 2,                    // 主角色 id(本期一账号一角色,二期支持多角色)
  "permission_codes": ["order.read", "device.read", ...],  // 权限点代码数组
  "customer_id": "cp_demo",        // 客户标识(预留,本期所有 admin 属同一客户,值固定)
  "exp": 1764124800                // Unix 秒
}
```

- JWT 默认有效期 **15 分钟**;Refresh Token 有效期 **7 天**(存 Redis 允许撤销)
- admin 中间件校验顺序:**JWT 合法性 → 过期检查 → 角色权限匹配 → 写入审计上下文**
- 权限点命名规范:`{resource}.{action}[.{qualifier}]`(参考 `permission.permission_code`)

### 限流

> 限流全局规则见 `docs/技术规格.md` § 7.4(权威源),本文档仅列该服务覆写与特殊端点(各端点 `[限流]` 标注)。

- **Caddy 层**:IP 维度 1000 req/s(防 DDoS)
- **应用层**:每 admin user 100 req/s(防误用);登录端点 5 req/min(防爆破)
- 超过限流返回 `4291`

### 错误码

> 完整错误码字典见 `services/common-error/errors.toml` + `docs/技术规格.md` § 7.2(权威源)。
> 段位固定:`0`=成功 / `1xxx`=通用 / `2xxx`=业务(`admin` 服务专属子段 2101-2199)/ `3xxx`=第三方 / `4xxx`=限流 / `5xxx`=服务器。本文仅列该服务用到的子集。

| 段位 | 含义 | 示例 |
| --- | --- | --- |
| 1xxx | 通用错误 | 1001 未授权 / 1003 禁止访问 / 1004 资源不存在 / 1005 参数校验失败 |
| 2xxx | 业务错误 | 2006 账号已锁定 / 2007 账号未激活 / 2008 角色不可分配 / 2009 站点名重复 / 2010 设备已下架 / 2011 计费规则已被引用 / 2012 退款已审核 / 2013 发票已审核 / 2014 公告已过期 / 2015 Webhook URL 不合法 / 2016 固件版本号已存在 / 2017 告警规则已停用 / 2020 导出任务不存在 |
| 3xxx | 第三方错误 | 3001 微信退款失败 / 3003 OSS 上传失败 |
| 4xxx | 限流 | 4291 超过限流 |
| 5xxx | 服务器错误 | 5001 内部错误 / 5003 服务暂时不可用 |

### 审计要求(关键)

- **所有写操作**(POST / PUT / PATCH / DELETE)**必须产生 `audit_log`**:包含 `actor_id`(操作人)、`action`(操作类型)、`resource_type` / `resource_id`、`before_snapshot` / `after_snapshot`、`request_ip`、`created_at`
- 审计日志**不可修改 / 不可删除**(应用层 + 数据库权限双重保护)
- 客户合规检查 / 等保测评会拉审计日志(沿用技术规格 § 9.5)

### 跨服务调用约定(关键)

- **admin 调 gateway** / **user** / **billing** 均通过内网 HTTP(§ 2.4),**禁止直连其他 schema**(§ 4.2)
- 鉴权:服务间共享密钥(每个服务一份,`Authorization: Bearer <service_token>` header)
- **具体内部接口路径由对方服务 API 文档定义**:
  - admin 调 user 的内部接口详见 `docs/api/user.md`(用户 / 订单 / 退款 / 发票 / 优惠券等)
  - admin 调 gateway 的内部接口详见 `docs/api/gateway.md`(设备实时状态 / 远程重启 / 订单查询等)
  - admin 调 billing 的内部接口详见 `docs/api/billing.md`(分账 / 账单明细等)
- admin.md 描述"调 X 服务的 XX 接口"时,**只引用语义,不写具体路径**;若 PR 涉及新增 admin ↔ X 的调用,必须先在 X 的 API 文档落地路径,再在本文件引用
- 跨服务调用失败处理:
  - **超时** 5 s(可配),失败返回 `5003`(下游服务暂时不可用)
  - **重试**:非幂等操作(POST)不重试,幂等操作(GET)重试 1 次
  - **断路器**:连续 5 次失败熔断 60 s,期间直接返回 `5003`(避免拖垮下游)

---

## 端点清单(共 112 个)

### A. 认证与账号(13 个)— `admin_user_role` + `role` + `permission`

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| POST | `/api/v1/admin/auth/login` | 公开 | 用户名 + 密码登录,失败 5 次锁定 30 min |
| POST | `/api/v1/admin/auth/logout` | JWT | 登出(撤销 Refresh Token) |
| POST | `/api/v1/admin/auth/refresh` | 公开 | Refresh Token 换新 JWT(轮换) |
| POST | `/api/v1/admin/auth/change-password` | JWT | 登录后改密(校验旧密码 + 强度) |
| GET | `/api/v1/admin/auth/me` | JWT | 当前登录用户信息(含角色与权限) |
| GET | `/api/v1/admin/users` | 角色 | 管理员账号列表(分页 + 角色筛选) |
| POST | `/api/v1/admin/users` | 角色 | 新建账号(生成初始密码 + 邮件发送) |
| GET | `/api/v1/admin/users/{user_id}` | 角色 | 账号详情 |
| PUT | `/api/v1/admin/users/{user_id}` | 角色 | 更新账号(显示名 / 邮箱 / 角色) |
| DELETE | `/api/v1/admin/users/{user_id}` | 角色 | 软删(离职),保留审计 |
| POST | `/api/v1/admin/users/{user_id}/reset-password` | 角色 | 客户管理员代重置密码(发邮件) |
| POST | `/api/v1/admin/users/{user_id}/unlock` | 角色 | 解锁临时锁定账号 |
| GET | `/api/v1/admin/users/{user_id}/audit-logs` | 角色 | 该账号产生的审计日志(分页) |

### B. 角色与权限(4 个)— `role` + `permission`

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/admin/roles` | JWT | 角色列表(7 个预置,只读) |
| GET | `/api/v1/admin/roles/{role_id}` | JWT | 角色详情(含 `permission_codes`) |
| PUT | `/api/v1/admin/roles/{role_id}` | 角色 | 更新角色权限范围(本期禁改,二期开放) |
| GET | `/api/v1/admin/permissions` | JWT | 所有权限点(按 `resource` 分组,用于"角色配置"界面渲染) |

### C. 站点与设备(11 个)— `station` + `device_meta`

设备导入增量接口（要求 `device.import`）：

- `POST /api/v1/admin/device-imports`：请求 `{import_id: UUID, devices: [...]}`，devices 字段契约见 gateway 建档接口。保存请求后同步 gateway；响应 data 为 `{import_id,status,last_error}`。只有 status=completed 代表完成，failed 必须展示 last_error。
- `GET /api/v1/admin/device-imports`：当前操作员最近 50 条导入记录。
- `POST /api/v1/admin/device-imports/{id}/retry`：仅允许原操作员重试其记录，重新校验功能权限；完成记录直接返回完成状态，不重复写审计。

相同 import_id 不得用于不同请求。站点须存在且未删除；已有不同设备配置报错。请求持久化与同步分开，gateway 成功但 admin 未提交时，可按原始请求幂等重试。已有环境需依次应用 `admin_db/0002_device_import.sql` 与 `0003_device_import_retry.sql`。

admin 启动自动恢复循环，每 5 秒扫描到期任务。临时下游故障按 10 秒起始、最长 300 秒的退避重试，总尝试数达到 8 后停止自动重试；人工可继续重试。参数、状态冲突和权限错误不自动重试。每次自动尝试重新检查原操作员权限，账号停用或权限撤销后不执行设备写入。失败写入回滚到事务保存点，保留任务行锁后记录失败，避免并发失败覆盖完成状态。数据范围权限仍待完成，当前不可宣称完整角色流程已验收。

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/admin/stations` | 角色 | 站点列表(分页 + 区域筛选) |
| POST | `/api/v1/admin/stations` | 角色 | 新建站点(填名称 / 地址 / 经纬度) |
| GET | `/api/v1/admin/stations/{station_id}` | 角色 | 站点详情(含设备列表摘要) |
| PUT | `/api/v1/admin/stations/{station_id}` | 角色 | 更新站点(改名 / 改经纬度 / 改营业时间) |
| DELETE | `/api/v1/admin/stations/{station_id}` | 角色 | 软删站点(若有在线设备禁止删) |
| GET | `/api/v1/admin/stations/{station_id}/devices` | 角色 | 站点下设备列表(含实时状态快照) |
| GET | `/api/v1/admin/devices` | 角色 | 全量设备列表(按站点 / 状态 / 厂商筛选) |
| GET | `/api/v1/admin/devices/{device_id}` | 角色 | 设备详情(含 device_meta + 最近遥测) |
| GET | `/api/v1/admin/devices/{device_id}/telemetry` | 角色 | 设备遥测历史(按时间窗查聚合表) |
| GET | `/api/v1/admin/devices/{device_id}/sessions` | 角色 | 充电会话历史(通过 HTTP 调 gateway 查) |
| GET | `/api/v1/admin/devices/{device_id}/faults` | 角色 | 设备故障 / 告警历史 |

### D. 订单查询(只读,3 个)— 通过 HTTP 调 gateway / billing

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/admin/orders` | 角色 | 订单列表(跨站点 / 时间 / 状态筛选) |
| GET | `/api/v1/admin/orders/{order_id}` | 角色 | 订单详情(含价费分离 + 分账明细) |
| GET | `/api/v1/admin/orders/{order_id}/timeline` | 角色 | 订单状态机时间线(状态变更 + 事件流水) |

### E. 告警与风控(15 个)— `alert_rule` + `alert_subscription` + `alert_event` + `risk_config`

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/admin/alerts` | 角色 | 告警事件列表(分页 + 级别 / 状态筛选) |
| GET | `/api/v1/admin/alerts/{alert_id}` | 角色 | 告警详情(含触发时的遥测快照) |
| POST | `/api/v1/admin/alerts/{alert_id}/ack` | 角色 | 客户运营确认告警(填备注) |
| POST | `/api/v1/admin/alerts/{alert_id}/resolve` | 角色 | 客户运营关闭告警(填处理结果) |
| GET | `/api/v1/admin/alert-rules` | 角色 | 告警规则列表(客户自配) |
| POST | `/api/v1/admin/alert-rules` | 角色 | 创建告警规则(阈值 + 触发条件) |
| GET | `/api/v1/admin/alert-rules/{rule_id}` | 角色 | 规则详情 |
| PUT | `/api/v1/admin/alert-rules/{rule_id}` | 角色 | 更新规则(调阈值 / 启停) |
| DELETE | `/api/v1/admin/alert-rules/{rule_id}` | 角色 | 软删规则(若已被引用禁止删) |
| GET | `/api/v1/admin/alert-subscriptions` | 角色 | 告警订阅列表(Webhook / 邮件接收方) |
| POST | `/api/v1/admin/alert-subscriptions` | 角色 | 创建告警订阅(选事件类型 + 接收通道) |
| PUT | `/api/v1/admin/alert-subscriptions/{sub_id}` | 角色 | 更新订阅 |
| DELETE | `/api/v1/admin/alert-subscriptions/{sub_id}` | 角色 | 软删订阅 |
| GET | `/api/v1/admin/risk-config` | 角色 | 风控配置查询(单例) |
| PUT | `/api/v1/admin/risk-config` | 角色 | 更新风控配置(频次 / 金额阈值) |

### F. 财务审核(10 个)— `settled_record` + `finance_reconcile_log` + `invoice_review`

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/admin/billing/settlements` | 角色 | 分账单列表(按周期 + 参与方筛选) |
| GET | `/api/v1/admin/billing/settlements/{settled_id}` | 角色 | 分账单详情(含每个参与方的金额) |
| GET | `/api/v1/admin/billing/refunds` | 角色 | 退款审核列表(从 user_db 通过 HTTP 拉,需双签到 `refund_record`) |
| POST | `/api/v1/admin/billing/refunds/{refund_id}/approve` | 角色 | 退款审核通过(双签) |
| POST | `/api/v1/admin/billing/refunds/{refund_id}/reject` | 角色 | 退款审核拒绝(填理由) |
| GET | `/api/v1/admin/billing/invoices` | 角色 | 发票审核列表(从 `invoice_review` 查) |
| POST | `/api/v1/admin/billing/invoices/{invoice_id}/approve` | 角色 | 发票审核通过(同步通知 user 服务开票) |
| POST | `/api/v1/admin/billing/invoices/{invoice_id}/reject` | 角色 | 发票审核拒绝(填理由 + 通知用户) |
| GET | `/api/v1/admin/billing/reconcile-logs` | 角色 | 对账日志列表(每日 03:00 自动跑) |
| GET | `/api/v1/admin/billing/reconcile-logs/{reconcile_id}` | 角色 | 对账详情(差异项 + 处理建议) |

### G. 营销配置(6 个)— `coupon`

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/admin/coupons` | 角色 | 优惠券模板列表(分页 + 类型筛选) |
| POST | `/api/v1/admin/coupons` | 角色 | 创建优惠券模板(类型 / 额度 / 有效期) |
| GET | `/api/v1/admin/coupons/{coupon_id}` | 角色 | 模板详情 |
| PUT | `/api/v1/admin/coupons/{coupon_id}` | 角色 | 更新模板(只能改未发放的) |
| DELETE | `/api/v1/admin/coupons/{coupon_id}` | 角色 | 软删模板(若有发放记录禁止删) |
| GET | `/api/v1/admin/coupons/{coupon_id}/stats` | 角色 | 发放 / 使用统计(总发 / 已用 / 核销率) |

### H. 公告 / 白标 / 客服配置(13 个)— `announcement` + `whitelabel_config` + `customer_service_config`

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/admin/announcements` | 角色 | 公告列表(分页 + 类型筛选) |
| POST | `/api/v1/admin/announcements` | 角色 | 发布公告(含 `title_i18n` / `content_i18n` 预留) |
| GET | `/api/v1/admin/announcements/{ann_id}` | 角色 | 公告详情 |
| PUT | `/api/v1/admin/announcements/{ann_id}` | 角色 | 更新公告(已过期不可改) |
| DELETE | `/api/v1/admin/announcements/{ann_id}` | 角色 | 软删(撤回) |
| GET | `/api/v1/admin/whitelabel` | 角色 | 白标配置查询(单例 ID=1) |
| PUT | `/api/v1/admin/whitelabel` | 角色 | 更新白标(Logo / 主题色 / 客服电话 / 备案号) |
| GET | `/api/v1/admin/customer-service` | 角色 | 客服坐席配置列表 |
| POST | `/api/v1/admin/customer-service` | 角色 | 新增客服坐席(微信客服链接 / 分流规则) |
| GET | `/api/v1/admin/customer-service/{cs_id}` | 角色 | 坐席详情 |
| PUT | `/api/v1/admin/customer-service/{cs_id}` | 角色 | 更新坐席 |
| DELETE | `/api/v1/admin/customer-service/{cs_id}` | 角色 | 软删坐席(若被引用禁止删) |
| POST | `/api/v1/admin/customer-service/{cs_id}/test-entry` | 角色 | 测试坐席链接(模拟小程序调用验证可达) |

### I. Webhook 订阅(7 个)— `webhook_subscription` + `webhook_delivery_log`

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/admin/webhooks` | 角色 | 订阅列表(分页 + 事件类型筛选) |
| POST | `/api/v1/admin/webhooks` | 角色 | 创建订阅(生成签名密钥,只返回一次) |
| GET | `/api/v1/admin/webhooks/{sub_id}` | 角色 | 订阅详情(密钥不回显,只显示前缀) |
| PUT | `/api/v1/admin/webhooks/{sub_id}` | 角色 | 更新订阅(URL / 事件类型 / 启停) |
| DELETE | `/api/v1/admin/webhooks/{sub_id}` | 角色 | 软删订阅 |
| GET | `/api/v1/admin/webhooks/{sub_id}/deliveries` | 角色 | 推送日志(分页 + 状态筛选) |
| POST | `/api/v1/admin/webhooks/{sub_id}/test` | 角色 | 测试推送(发一条 `ping` 事件验证可达) |

### J. OTA 配置(10 个)— `ota_package` + `ota_schedule`

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/admin/ota/packages` | 角色 | 固件包元数据列表(分页 + 厂商筛选) |
| POST | `/api/v1/admin/ota/packages` | 角色 | 上传固件包元数据(走 OSS 预签名直传) |
| GET | `/api/v1/admin/ota/packages/{pkg_id}` | 角色 | 固件包详情 |
| DELETE | `/api/v1/admin/ota/packages/{pkg_id}` | 角色 | 删除固件包元数据(若有调度引用禁止删) |
| GET | `/api/v1/admin/ota/schedules` | 角色 | 推送调度列表(分页 + 状态筛选) |
| POST | `/api/v1/admin/ota/schedules` | 角色 | 创建推送调度(选包 + 目标设备 + 时段) |
| GET | `/api/v1/admin/ota/schedules/{sched_id}` | 角色 | 调度详情 |
| PUT | `/api/v1/admin/ota/schedules/{sched_id}` | 角色 | 更新调度(改时段 / 暂停) |
| DELETE | `/api/v1/admin/ota/schedules/{sched_id}` | 角色 | 取消调度(已开始的不可取消) |
| POST | `/api/v1/admin/ota/schedules/{sched_id}/execute` | 角色 | 立即执行(跳过时段,用于紧急修复) |

### K. 计费与分账模板(15 个)— `pricing_rule` + `pricing_template` + `split_template` + `split_party`

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/admin/settings/charge-rules` | 角色 | 计费规则实例列表(分页 + 站点筛选) |
| POST | `/api/v1/admin/settings/charge-rules` | 角色 | 创建计费规则实例(基于 pricing_template) |
| GET | `/api/v1/admin/settings/charge-rules/{rule_id}` | 角色 | 规则详情 |
| PUT | `/api/v1/admin/settings/charge-rules/{rule_id}` | 角色 | 更新规则(电价 / 服务费 / 时段) |
| DELETE | `/api/v1/admin/settings/charge-rules/{rule_id}` | 角色 | 软删规则(若有进行中订单禁止删) |
| GET | `/api/v1/admin/settings/pricing-templates` | 角色 | 计费规则模板列表 |
| POST | `/api/v1/admin/settings/pricing-templates` | 角色 | 创建模板 |
| PUT | `/api/v1/admin/settings/pricing-templates/{tpl_id}` | 角色 | 更新模板 |
| GET | `/api/v1/admin/settings/split-templates` | 角色 | 分账模板列表 |
| POST | `/api/v1/admin/settings/split-templates` | 角色 | 创建分账模板(选模式 A / B + 参与方) |
| GET | `/api/v1/admin/settings/split-templates/{tpl_id}` | 角色 | 模板详情 |
| PUT | `/api/v1/admin/settings/split-templates/{tpl_id}` | 角色 | 更新模板 |
| GET | `/api/v1/admin/settings/split-templates/{tpl_id}/parties` | 角色 | 参与方列表(物业 / 加盟商 / 业主) |
| POST | `/api/v1/admin/settings/split-templates/{tpl_id}/parties` | 角色 | 添加参与方 |
| PUT | `/api/v1/admin/settings/parties/{party_id}` | 角色 | 更新参与方比例 |
| DELETE | `/api/v1/admin/settings/parties/{party_id}` | 角色 | 删除参与方(比例总和必须 = 100% 才允许保存) |

> **计费分账小计**:15 个端点。计费规则必须先有模板(`pricing_template`),再用 `charge-rule` 实例绑定到站点;分账模板需配齐 `split_party` 比例后才能被站点引用。

### L. 审计与导出(6 个)— `audit_log`

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/admin/audit-logs` | 角色 | 全局审计日志(分页 + 操作人 / 资源类型 / 时间筛选) |
| POST | `/api/v1/admin/export/orders` | 角色 | 创建订单导出任务(异步,生成 CSV) |
| POST | `/api/v1/admin/export/settlements` | 角色 | 创建账单导出任务 |
| GET | `/api/v1/admin/export/tasks` | 角色 | 导出任务列表(分页 + 状态筛选) |
| GET | `/api/v1/admin/export/tasks/{task_id}` | 角色 | 导出任务详情 |
| GET | `/api/v1/admin/export/tasks/{task_id}/download` | 角色 | 下载导出文件(临时签名 URL,30 min 过期) |

---

## 内部只读接口(供 user 服务调用,不计入公开端点数)

| 方法 | 路径 | 参数 | 响应 `data` | 错误码 |
| --- | --- | --- | --- | --- |
| GET | `/api/v1/internal/alerts` | `device_id` 必填,`status=active` | `alerts=[{alert_id,device_id,severity,alert_type,created_at}]`,无告警返回空数组 | `1005` 参数错误 / `5003` 暂不可用 |
| GET | `/api/v1/internal/stations/{station_id}` | 路径站点 ID | `{station_id,station_name,address,longitude,latitude,status}` | `1004` 站点不存在 / `5003` 暂不可用 |

仅 `:8082` 内网监听,要求 `Authorization: Bearer <service_token>`。user 服务只读取本接口返回的 `admin_db` 数据,不直连 admin schema;设备告警按 `device_id` 过滤,仅返回当前有效记录。

---

## A. 认证与账号

### `POST /api/v1/admin/auth/login`

**鉴权**:[公开] + **限流**:5 req/min/IP(防爆破)
**触发场景**:PC 后台登录页输入用户名 + 密码后提交
**业务目标**:校验密码 → 签发 JWT(HS256) + Refresh Token

**请求体**:
```json
{
  "username": "zhangsan",
  "password": "P@ssw0rd!2026"
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "jwt": "eyJhbGciOiJIUzI1NiIs...",
    "refresh_token": "RT_admin_abc123",
    "user_id": 7,
    "role_id": 2,
    "role_code": "customer_ops",
    "permission_codes": ["order.read", "device.read", "alert.read"],
    "must_change_password": false,
    "jwt_expires_in": 900
  }
}
```

**业务逻辑**:
1. 校验 `username` + `password` 非空
2. 查 `admin_user_role WHERE username = ? AND deleted_at IS NULL`:
   - 不存在 → 返回 `1001`(用户名或密码错误,**不区分**以防枚举)
   - 存在但 `status='disabled'` → 返回 `1001`(同上,不暴露账号状态)
3. **临时锁定检查**:`status='locked' AND locked_until > NOW()` → 返回 `2006`,含 `locked_until` 提示用户
4. **密码校验**:argon2id verify(`password_hash`, `password`) → 失败:
   - `failed_login_count += 1`
   - 若 `>= 5` → `status='locked', locked_until = NOW() + 30 min`,返回 `2006`
   - 否则返回 `1001`
5. **强制改密检查**:`must_change_password=TRUE` → 响应额外返回 `must_change_password=true`,前端拦截跳转改密页
6. 校验通过 → 签发 JWT + Refresh Token,UPDATE `last_login_at=NOW(), last_login_ip, failed_login_count=0, status='active'`
7. 写 `audit_log(action='auth.login', actor_id=$user.id)`

**错误码**:
- `1001`: 用户名或密码错误 / 账号已停用
- `2006`: 账号已锁定(含 `locked_until`)
- `2007`: 账号待激活(初始密码未改完,前端引导去改密)
- `5001`: 内部错误

---

### `POST /api/v1/admin/auth/logout`

**鉴权**:[JWT]
**触发场景**:用户点击 PC 后台右上角"退出登录"
**业务目标**:撤销 Refresh Token,清除会话

**请求头**:`Authorization: Bearer <jwt>`

**请求体**(可选,用于前端清理前端缓存):
```json
{}
```

**响应(200)**:
```json
{ "code": 0, "data": { "logged_out": true } }
```

**业务逻辑**:
1. 校验 JWT 拿 `user_id`
2. **Redis 操作**:把当前 Refresh Token 加入撤销列表(白名单 → 黑名单)→ SET `revoked:RT_admin_xxx = 1 EX <剩余有效期>`
3. 写 `audit_log(action='auth.logout', actor_id=$user.id)`

**错误码**:
- `1001`: JWT 失效(已过期 / 伪造)
- `5001`: 内部错误

---

### `POST /api/v1/admin/auth/refresh`

**鉴权**:[公开] + Refresh Token(`Authorization: Bearer RT_admin_xxx`)

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "jwt": "eyJ...",
    "refresh_token": "RT_admin_def456",
    "jwt_expires_in": 900
  }
}
```

**业务逻辑**:
1. 校验 Refresh Token 格式(`RT_admin_` 前缀)
2. 查 Redis:
   - 在撤销列表 → `1001`(已撤销)
   - 不在白名单 → `1001`(伪造 / 已过期)
   - 在白名单 → 续期 + **轮换**(老 RT 加入撤销列表,签发新 RT)
3. 签发新 JWT + 新 Refresh Token

**错误码**:`1001`(标准)

---

### `POST /api/v1/admin/auth/change-password`

**鉴权**:[JWT]
**触发场景**:登录后改密 / 强制改密引导页
**业务目标**:校验旧密码 → 更新 `password_hash` + 清 `must_change_password`

**请求体**:
```json
{
  "old_password": "P@ssw0rd!2026",
  "new_password": "NewP@ssw0rd!2027"
}
```

**业务逻辑**:
1. 校验 JWT 拿 `user_id`
2. 查 `admin_user_role WHERE id=$user_id AND deleted_at IS NULL` → 不存在返回 `1001`
3. **校验旧密码**:argon2id verify → 失败返回 `1001`(不区分"旧密码错"与"账号不存在",防枚举)
4. **校验新密码强度**:
   - 长度 ≥ 12 字符
   - 必须包含字母 + 数字
   - 不能与最近 5 次密码相同(查 `password_history`,**本期不实现**,二期加)
5. UPDATE `password_hash, password_changed_at=NOW(), must_change_password=FALSE`
6. **强制改密流程**:若 `must_change_password=TRUE`,此端点是解锁的唯一途径(不强制要求与原密码不同,只校验强度)
7. 写 `audit_log(action='auth.change_password', actor_id=$user.id)`

**错误码**:
- `1001`: JWT 失效 / 旧密码错误
- `1005`: 新密码强度不够

---

### `GET /api/v1/admin/auth/me`

**鉴权**:[JWT]

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "user_id": 7,
    "username": "zhangsan",
    "display_name": "张三 / 客户运营",
    "role": {
      "role_id": 2,
      "role_code": "customer_ops",
      "role_name": "客户运营"
    },
    "permission_codes": ["order.read", "device.read", "alert.read"],
    "last_login_at": "2026-09-25T14:00:00Z",
    "must_change_password": false
  }
}
```

**业务逻辑**:从 JWT 直接解析 `user_id` / `role_id` / `permission_codes`,查 `admin_user_role` + `role` 拿显示名与角色名,**避免查权限点表**(性能优化,§ 4.7 JWT 内置权限码)

**错误码**:`1001`(标准)

---

### `POST /api/v1/admin/users`

**鉴权**:[角色] `user.create`(客户管理员权限)
**触发场景**:客户管理员在"账号管理"页点击"新建账号"
**业务目标**:生成随机初始密码 + 邮件发送 + INSERT `admin_user_role`

**请求体**:
```json
{
  "username": "lisi",
  "display_name": "李四 / 客户巡检",
  "email": "lisi@example.com",
  "phone": "13900139000",
  "role_id": 4,
  "send_via": "email"
}
```

**请求字段**:
| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `username` | string | 是 | 登录用户名(4-64 字符,字母数字下划线) |
| `display_name` | string | 是 | 显示名(界面展示) |
| `email` | string | 否 | 邮箱(找回密码用,AES_ENCRYPT 存) |
| `phone` | string | 否 | 备用手机号(AES_ENCRYPT 存) |
| `role_id` | int | 是 | 关联 `role.id` |
| `send_via` | enum | 否 | `email` / `sms` / `none`(默认 email) |

**响应(201)**:
```json
{
  "code": 0,
  "data": {
    "user_id": 42,
    "username": "lisi",
    "initial_password": "TmpA7x!2026K9z",  // 仅此一次返回明文,前端提示客户运营告知用户
    "sent_to": "lisi@example.com",
    "must_change_password": true
  }
}
```

**业务逻辑**:
1. 校验 `username` 唯一(`uk_admin_user_role_username`)
2. 校验 `role_id` 在 `role` 表中存在 + `status='enabled'` + `built_in=TRUE`(允许分配预置角色)
3. **生成初始密码**:16 字符随机串(字母 + 数字 + 特殊字符),argon2id 哈希
4. INSERT `admin_user_role(status='pending', must_change_password=TRUE, password_hash=...)` + 加密 `email_enc` / `phone_enc`
5. **发送初始密码**:`send_via='email'` → 邮件模板;`'sms'` → 短信(预留,本期未接短信网关);`'none'` → 仅返回明文
6. 写 `audit_log(action='user.create', actor_id=$current_user.id, after_snapshot=...)`
7. **事务边界**:INSERT + audit_log 同事务;邮件发送失败不回滚(异步重试)

**错误码**:
- `1005`: 参数校验失败
- `2008`: 角色不可分配(`disabled` / 非预置)
- `4001`: `username` 已存在
- `3003`: 邮件发送失败(降级返回明文,不阻塞账号创建)

---

### `POST /api/v1/admin/users/{user_id}/reset-password`

**鉴权**:[角色] `user.reset_password`(客户管理员权限)
**触发场景**:员工忘记密码 / 客户管理员代重置
**业务目标**:生成新临时密码 + 邮件 + 清 `must_change_password`

**请求体**:
```json
{
  "send_via": "email"
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "user_id": 42,
    "initial_password": "NewTmp7x!2026K9z",  // 仅一次明文
    "sent_to": "lisi@example.com",
    "must_change_password": true
  }
}
```

**业务逻辑**:
1. 查 `admin_user_role WHERE id=$user_id AND deleted_at IS NULL` → 不存在返回 `1004`
2. 同 `POST /users` 步骤 3-7,但不强制走"创建"流程
3. UPDATE `password_hash, password_changed_at=NOW(), must_change_password=TRUE, failed_login_count=0, status='pending', locked_until=NULL`
4. 写 `audit_log(action='user.reset_password', actor_id=$current_user.id)`

**错误码**:
- `1004`: 账号不存在
- `3003`: 发送失败(降级返回明文)

---

### `POST /api/v1/admin/users/{user_id}/unlock`

**鉴权**:[角色] `user.unlock`(客户管理员权限)

**请求体**:`{}`

**业务逻辑**:
1. 查账号 → 不存在返回 `1004`
2. 校验 `status='locked'` → 否则返回 `1005`(状态不允许)
3. UPDATE `status='active', locked_until=NULL, failed_login_count=0`
4. 写 `audit_log(action='user.unlock', actor_id=$current_user.id)`

**错误码**:`1004` / `1005`(状态不允许)

---

## B. 角色与权限

### `GET /api/v1/admin/roles`

**鉴权**:[JWT]

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "items": [
      {
        "role_id": 1,
        "role_code": "customer_admin",
        "role_name": "客户管理员",
        "role_type": "admin",
        "permission_codes": ["user.create", "user.update", "user.delete", "..."],
        "status": "enabled",
        "built_in": true,
        "account_count": 3
      }
    ]
  }
}
```

**业务逻辑**:查 `role` + LEFT JOIN `admin_user_role` 统计 `account_count`(只算 `deleted_at IS NULL`)。**本期角色只读**,不开放 PUT。

### `PUT /api/v1/admin/roles/{role_id}`

**鉴权**:[角色] `role.update`(本期**禁用**,始终返回 `1003`)

**错误码**:
- `1003`: 本期角色不可修改(预置角色,二期开放)

### `GET /api/v1/admin/permissions`

**鉴权**:[JWT]

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "groups": [
      {
        "resource": "order",
        "permissions": [
          { "permission_code": "order.read", "permission_name": "查看订单" },
          { "permission_code": "order.refund.review", "permission_name": "退款审核" }
        ]
      },
      {
        "resource": "device",
        "permissions": [
          { "permission_code": "device.read", "permission_name": "查看设备" },
          { "permission_code": "device.ota.push", "permission_name": "推送 OTA" }
        ]
      }
    ]
  }
}
```

**业务逻辑**:查 `permission` 表 → 按 `resource` 字段分组,供前端"角色管理"页渲染多选框。

---

## C. 站点与设备

### `POST /api/v1/admin/stations`

**鉴权**:[角色] `station.create`

**请求体**:
```json
{
  "station_name": "万达广场地下停车场",
  "address": "北京市朝阳区建国路 93 号 B2 层",
  "latitude": 39.9087,
  "longitude": 116.4602,
  "business_hours": "00:00-24:00",
  "contact_phone": "4001234567",
  "station_type": "indoor_paid",   // "indoor_paid" 室内付费 / "outdoor_free" 室外免费 / "residential" 住宅
  "pricing_rule_id": 5,
  "split_template_id": 3
}
```

**业务逻辑**:
1. 校验参数(经纬度范围 / 营业时间格式 / `pricing_rule_id` 存在 / `split_template_id` 存在)
2. **站点名唯一性**:本期不强制(同名不同地点允许),靠地址区分
3. INSERT `station` + 写 `audit_log`
4. **缓存失效**:`DEL station:summary:$station_id`(user 服务缓存)

**错误码**:
- `1005`: 参数校验失败
- `2011`: 计费规则已被引用(此处是反过来,rule 不存在或已软删)
- `4002`: `split_template_id` 不存在

### `DELETE /api/v1/admin/stations/{station_id}`

**鉴权**:[角色] `station.delete`

**业务逻辑**:
1. 查站点 → 不存在返回 `1004`
2. 校验站点下无在线设备(`device_meta.status='online'` 在该站点下)→ 有则返回 `2010`(设备在线,先下架)
3. 校验无进行中订单(通过 HTTP 调 user / billing 查)→ 有则返回 `2010`
4. UPDATE `station.deleted_at=NOW(), deleted_by=$actor.id`(软删)
5. 写 `audit_log`

**错误码**:`1004` / `2010`

### `GET /api/v1/admin/devices/{device_id}`

**鉴权**:[角色] `device.read`

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "device_id": "xx_001",
    "device_meta": {
      "station_id": 12,
      "station_name": "万达广场地下停车场",
      "vendor": "Xunda",
      "model": "XD-220V-10A",
      "port_count": 10,
      "firmware_version": "v1.2.3",
      "ocpp_version": null,
      "install_at": "2026-08-01T10:00:00Z",
      "status": "online"
    },
    "realtime_snapshot": {             // 从 Redis 取最近一次遥测(TTL 60s)
      "online": true,
      "last_seen_at": "2026-09-25T14:00:00Z",
      "ports_free": 7,
      "ports_charging": 3,
      "ports_fault": 0,
      "meter_total_kwh": "1234.56",
      "temperature_c": 32.5
    },
    "linked_ota": {                     // 若有进行中 OTA
      "schedule_id": 8,
      "target_version": "v1.3.0",
      "progress_pct": 45
    }
  }
}
```

**业务逻辑**:
1. JOIN `device_meta` + `station`(查站点名)+ Redis `device:realtime:$device_id` 快照
2. 若有进行中 OTA → JOIN `ota_schedule` + `ota_package`

---

## D. 订单查询(只读)

> **数据源**:admin_db **不存**订单数据;订单走 `gateway_db` / `billing_db` / `user_db`。admin 通过 HTTP 调 gateway 服务的订单内部接口获取(具体路径见 `docs/api/gateway.md`)。

### `GET /api/v1/admin/orders`

**鉴权**:[角色] `order.read`
**限流**:每 user 50 req/min(数据量大)

**请求 query**:
| 参数 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `page` | int | 1 | — |
| `page_size` | int | 20 | 最大 100 |
| `station_id` | int | — | 按站点过滤 |
| `status` | enum | — | `charging` / `finished` / `cancelled` / `failed` |
| `started_from` | ISO 8601 | — | 起始时间(默认 7 天前) |
| `started_to` | ISO 8601 | — | 截止时间(默认 NOW) |

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "total": 12345,
    "items": [
      {
        "order_id": 12345,
        "order_no": "CH20260925140000123",
        "device_id": "xx_001",
        "station_name": "万达广场地下停车场",
        "user_id": 8888,                  // 脱敏为 user_id,不暴露昵称
        "started_at": "2026-09-25T14:00:00Z",
        "ended_at": "2026-09-25T15:00:00Z",
        "duration_seconds": 3600,
        "meter_kwh": "0.520",
        "total_fee_cents": 55,
        "electric_fee_cents": 29,
        "service_fee_cents": 26,
        "status": "finished",
        "refund_status": "none"
      }
    ]
  }
}
```

**业务逻辑**:
1. 校验 JWT + 角色权限
2. **HTTP 调 user 服务**的 `GET /api/v1/internal/orders`；订单生命周期归属 user_db，与 user API 的 P1-7 修正一致
3. user 内部查 `charge_order`；admin 将站点筛选转换为设备范围，并补充站点名称
4. 校验失败、下游失败必须返回错误，不得返回虚假的空列表

**错误码**:
- `5003`: 下游服务不可用
- `5001`: 内部错误

### `GET /api/v1/admin/orders/{order_id}`

**鉴权**:[角色] `order.read`

**业务逻辑**:
1. HTTP 调 user 的 `GET /api/v1/internal/orders/{order_id}`，读取生命周期、支付与退款摘要
2. HTTP 调 billing 的 `GET /api/v1/internal/orders/{order_id}/billing-summary`，读取费用与分账快照；存在计费结果时，以 billing 金额覆盖生命周期中的费用快照
3. `billing.settlements[]` 包含分账单号、模式、状态、分账池及各参与方比例、金额、支付状态。尚未计费时 calculation_no 为 null，settlements 为空；下游失败返回 5003，不能伪装为未计费
4. 目标还包括持久化事件时间线，当前实现状态见 `../implementation-status.md`

### `GET /api/v1/admin/orders/{order_id}/timeline`

**鉴权**:[角色] `order.read`

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "order_id": 12345,
    "timeline": [
      { "at": "2026-09-25T14:00:00Z", "event": "created", "actor": "user:8888", "detail": "用户扫码启动" },
      { "at": "2026-09-25T14:00:05Z", "event": "device_ack", "actor": "gateway", "detail": "设备 ACK 启动" },
      { "at": "2026-09-25T15:00:00Z", "event": "ended", "actor": "device", "detail": "电量充满自动停止" },
      { "at": "2026-09-25T15:00:10Z", "event": "settled", "actor": "billing", "detail": "分账完成,总 55 分" },
      { "at": "2026-09-25T15:00:15Z", "event": "refund_required", "actor": "billing", "detail": "无退款需求" }
    ]
  }
}
```

**业务逻辑**:HTTP 调 user 的 `GET /api/v1/internal/orders/{order_id}/timeline`，确认订单存在且未软删除，然后读取 user_db 的 `charge_event_log`。按事件发生时间、记录 ID 稳定升序返回；每条另含唯一 `event_id`。当前写入点为创建、取消、支付确认、设备启动结果，并与订单状态变更同事务提交。旧订单没有事件时返回空数组，不根据当前状态伪造历史。充电结束、分账及退款事件接入仍待完成。

---

## E. 告警与风控

### `POST /api/v1/admin/alerts/{alert_id}/ack`

**鉴权**:[角色] `alert.ack`(客户运营)

**请求体**:
```json
{
  "comment": "已通知巡检现场排查,15 min 内到达"
}
```

**业务逻辑**:
1. 查 `alert_event WHERE id=$alert_id AND status='open'` → 不存在返回 `1004`
2. UPDATE `status='acknowledged', acknowledged_by=$actor.id, acknowledged_at=NOW(), ack_comment=...`
3. 写 `audit_log(action='alert.ack', actor_id=$actor.id)`
4. **可选**:通过告警订阅通道推"已确认"通知(避免重复打扰值班人)

### `POST /api/v1/admin/alerts/{alert_id}/resolve`

**鉴权**:[角色] `alert.resolve`

**请求体**:
```json
{
  "resolution": "fixed",          // "fixed" 已修复 / "false_positive" 误报 / "ignored" 已知问题忽略
  "comment": "更换充电模块后恢复正常",
  "related_device_action": "reboot"  // 可选:同时对设备执行的动作(reboot / firmware_update / none)
}
```

**业务逻辑**:
1. 查 `alert_event` → 不存在返回 `1004`
2. UPDATE `status='resolved', resolved_by=$actor.id, resolved_at=NOW(), resolution=..., resolve_comment=...`
3. 若 `related_device_action='reboot'` → 调 gateway 服务的设备重启内部接口(路径见 `docs/api/gateway.md`)
4. 若 `related_device_action='firmware_update'` → 校验 `alert_event.device_id` 是否在某个 OTA 调度中(本期不联动,留二期)
5. 写 `audit_log`

### `POST /api/v1/admin/alert-rules`

**鉴权**:[角色] `alert_rule.create`

**请求体**:
```json
{
  "rule_name": "设备离线 > 30 min",
  "rule_type": "device_offline",       // 枚举:device_offline / temperature_high / meter_abnormal / payment_failed
  "device_filter": {                   // 设备筛选条件(可选)
    "station_ids": [12, 13],
    "vendor": "Xunda"
  },
  "trigger": {
    "metric": "device_offline_duration_min",
    "operator": ">",                    // > / >= / < / <= / == / !=
    "threshold": 30,
    "duration_min": 5                   // 持续 5 min 才触发(防抖)
  },
  "severity": "high",                  // "low" / "mid" / "high" / "critical"
  "alert_subscription_ids": [1, 2],    // 关联订阅
  "enabled": true
}
```

**业务逻辑**:
1. 校验 `rule_type` 在预置枚举内(防止胡乱定义)
2. 校验 `device_filter` 中的 `station_ids` 存在
3. 校验 `alert_subscription_ids` 全部存在且 `status='enabled'`
4. 校验 `trigger` 字段类型与 `rule_type` 匹配(用 `rule_type → expected_metric` 映射表)
5. INSERT `alert_rule` + 写 `audit_log`
6. **缓存失效**:`DEL alert_rule:active`(worker 任务每 30 s 扫一次)

### `PUT /api/v1/admin/risk-config`

**鉴权**:[角色] `risk_config.update`(客户管理员 + 客户财务双签,**本期单签**)

**请求体**:
```json
{
  "refund_high_freq_count": 3,           // 5 分钟内 ≥ 3 次退款触发风控
  "refund_high_freq_window_sec": 300,
  "refund_double_sign_required": true,   // 双签要求(本期固定 true)
  "auto_block_enabled": true,            // 命中风控自动拦截(默认开)
  "block_duration_min": 60,              // 拦截 60 min 后允许重试
  "updated_reason": "调高频次阈值应对近期羊毛党"  // 必填审计
}
```

**业务逻辑**:
1. 校验 `risk_config` 单例 → 若不存在 INSERT 默认值;存在则 UPDATE
2. **必填审计**:`updated_reason` 不能为空,写 `audit_log` 时作为 `comment` 字段
3. **变更通知**:发企业微信 / 邮件给客户管理员(本期实现日志记录,不实接)

---

## F. 财务审核

> **跨服务原则**:admin_db **不存**支付订单 / 退款记录 / 发票申请主表;这些数据在 user_db。admin 通过 HTTP 调 user 服务获取列表,审核结果通过双签流程写回 user_db 的对应表 + 在 admin_db `invoice_review` / `finance_reconcile_log` 留档(沿用 § 4.2.1 关键表归属说明)。

### `POST /api/v1/admin/billing/refunds/{refund_id}/approve`

**鉴权**:[角色] `refund.review`(客户财务)
**双签**:**必须** 2 个不同 `customer_finance` 账号先后审核(防单人舞弊)

**请求体**:
```json
{
  "approve_comment": "同意,设备故障经核实全额退",
  "second_signature": "sig_customer_finance_2"  // 第二签账号的签名标识(由前端传 second admin's JWT)
}
```

**业务逻辑**:
1. 校验 `refund_id` 通过 HTTP 调 user 服务的退款详情查询接口(具体路径见 `docs/api/user.md`)拿到详情
2. 校验 `refund.status='pending_review'`,否则返回 `2012`(已审核)
3. **双签校验**:
   - `first_signer` = 当前账号(从 JWT 拿 `user_id`)
   - `second_signer` = 从请求体签名解析(本期简化为另一账号的 JWT,前端带 `X-Second-Signer-JWT` header)
   - 两个签名必须**不同账号**(`first_signer != second_signer`)
4. HTTP 调 user 服务的退款审核通过接口(具体路径见 `docs/api/user.md`)→ user 服务走微信退款 API + 更新 `refund_record.status='approved'`
5. UPDATE `admin_db.invoice_review` 同 ID 关联记录(若有)→ 写 `audit_log` + `finance_reconcile_log`
6. 推小程序消息"退款已审核通过"

**错误码**:
- `2012`: 退款已审核
- `1003`: 双签校验失败(同账号 / 签名无效)
- `3001`: 微信退款失败(沿用 user 服务错误码透传)

### `POST /api/v1/admin/billing/invoices/{invoice_id}/approve`

**鉴权**:[角色] `invoice.review`(客户财务)

**请求体**:
```json
{
  "approve_comment": "同意,抬头正确",
  "invoice_type": "vat_special"        // "vat_special" 增值税专票 / "vat_general" 普票 / "electronic" 电子发票
}
```

**业务逻辑**:
1. 校验 `invoice_id` 通过 HTTP 调 user 服务的发票详情查询接口(具体路径见 `docs/api/user.md`)→ 拿抬头 / 金额 / 用户提交的资料
2. 校验 `invoice_review.status='pending'`,否则返回 `2013`
3. **校验金额上限**:`invoice_review.amount_cents > 100000`(1000 元)→ 必须由客户财务 + 客户管理员双签(本期实现"必须双签",不区分金额)
4. UPDATE `admin_db.invoice_review(status='approved', invoice_type=..., reviewed_by=$actor.id, reviewed_at=NOW())` + 写 `audit_log`
5. HTTP 调 user 服务的发票审核通过回调接口(具体路径见 `docs/api/user.md`)→ user 通知用户开票(本期走邮件)
6. 异步触发 worker 生成电子发票 PDF → 推用户

### `GET /api/v1/admin/billing/reconcile-logs`

**鉴权**:[角色] `finance.read`

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "items": [
      {
        "reconcile_id": 1001,
        "reconcile_date": "2026-09-25",
        "wechat_total_cents": 1234567,
        "internal_total_cents": 1234500,
        "diff_cents": 67,                // 微信账单 vs 内部订单差额
        "diff_count": 3,                 // 差异订单数
        "status": "auto_resolved",       // "auto_resolved" 自动处理 / "manual_pending" 待人工 / "resolved" 已处理
        "created_at": "2026-09-25T03:00:00Z"
      }
    ]
  }
}
```

**业务逻辑**:查 `finance_reconcile_log`(每日 03:00 worker 自动跑,§ 3.5 reconcile 任务)。

---

## G. 营销配置

### `POST /api/v1/admin/coupons`

**鉴权**:[角色] `coupon.create`(客户运营)

**请求体**:
```json
{
  "coupon_name": "新人 5 元抵扣券",
  "coupon_name_i18n": { "zh-CN": "新人 5 元抵扣券" },   // 多语言预留
  "coupon_type": "fixed_amount",      // "fixed_amount" 固定金额 / "percentage" 百分比 / "free_time" 免费时长
  "discount_cents": 500,              // 5 元(单位分)
  "min_spend_cents": 1000,            // 满 10 元可用
  "valid_from": "2026-09-25T00:00:00Z",
  "valid_until": "2026-12-31T23:59:59Z",
  "total_quota": 1000,                // 总发放量
  "per_user_limit": 1,                // 每人限领
  "applicable_scope": "all",          // "all" 全部 / "specific_station" 指定站点 / "specific_device_type" 指定设备类型
  "applicable_ids": [],               // 按 scope 填
  "name_i18n_enabled": true           // 标记是否启用 i18n(本期只填 zh-CN)
}
```

**业务逻辑**:
1. 校验 `coupon_type` + `discount_cents` 合理性(百分比类型校验 `1-99`)
2. 校验 `applicable_scope` 与 `applicable_ids` 一致性
3. INSERT `coupon` + 写 `audit_log`
4. **缓存失效**:`DEL coupon:tpl:$coupon_id`(user 服务计费时缓存)

### `GET /api/v1/admin/coupons/{coupon_id}/stats`

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "coupon_id": 42,
    "total_quota": 1000,
    "granted_count": 856,              // 已发放
    "used_count": 423,                 // 已使用(核销)
    "unused_count": 433,               // 未使用
    "expired_count": 0,
    "usage_rate": 0.494,               // 核销率
    "total_discount_cents": 211500    // 总优惠金额(分)
  }
}
```

**业务逻辑**:JOIN `coupon_grant`(user_db)通过 HTTP 调 user 服务的优惠券统计接口(具体路径见 `docs/api/user.md`)聚合统计。**禁止直连 user_db**。

---

## H. 公告 / 白标 / 客服配置

### `POST /api/v1/admin/announcements`

**鉴权**:[角色] `announcement.create`

**请求体**:
```json
{
  "title": "国庆期间充电优惠活动",
  "title_i18n": { "zh-CN": "国庆期间充电优惠活动" },
  "content": "10 月 1 日-7 日,所有站点充电服务费 8 折",
  "content_i18n": { "zh-CN": "10 月 1 日-7 日,所有站点充电服务费 8 折" },
  "announcement_type": "promotion",   // "system_notice" / "maintenance" / "promotion"
  "display_mode": "popup",            // "popup" / "list" / "banner" / "all"
  "priority": 3,                     // 1-10,数字越小优先级越高
  "valid_from": "2026-10-01T00:00:00Z",
  "valid_until": "2026-10-07T23:59:59Z",
  "target_scope": "all",              // "all" / "specific_station" / "specific_user"
  "target_ids": [],                   // 按 scope 填
  "push_miniprogram": true            // 发布后立即推小程序(可选)
}
```

**业务逻辑**:
1. 校验 `priority` 范围 + `valid_until > valid_from`(NULL 视为永久)
2. 校验 `target_scope` 与 `target_ids` 一致性
3. INSERT `announcement` + 写 `audit_log`
4. 若 `push_miniprogram=true` → 发微信小程序订阅消息(走 user 服务,本期预留接口)

### `PUT /api/v1/admin/whitelabel`

**鉴权**:[角色] `whitelabel.update`(客户管理员)

**请求体**(全量替换):
```json
{
  "miniprogram_name": "XX 充电运营",
  "miniprogram_logo_url": "https://oss.example.com/logo.png",
  "admin_logo_url": "https://oss.example.com/admin-logo.png",
  "theme_color": "#FF6B35",
  "service_phone": "400-100-1234",
  "service_wechat_id": "ChargePilot_CS",
  "icp_record_no": "京ICP备 20260001 号",
  "custom_domain": "charge.example.com",
  "agreement_url": "https://charge.example.com/agreement",
  "privacy_url": "https://charge.example.com/privacy",
  "about_us": "# 关于我们\nXX 充电..."
}
```

**业务逻辑**:
1. 校验 `id=1`(单例)
2. UPDATE 全字段 + 写 `audit_log`(白标变更属高敏操作,**强制写 before / after snapshot**)
3. **缓存失效**:`DEL whitelabel:config`(user 服务 TTL 30 min)
4. **运维联动**:`custom_domain` 变更后,**人工**同步更新 Caddyfile + 微信小程序后台"request 合法域名"

**错误码**:
- `1005`: `theme_color` 非 HEX 格式
- `1003`: 缺少 `customer_admin` 权限

---

## I. Webhook 订阅

### `POST /api/v1/admin/webhooks`

**鉴权**:[角色] `webhook.create`

**请求体**:
```json
{
  "subscription_name": "BI 系统订单同步",
  "target_url": "https://bi.example.com/webhook/charge",
  "event_types": ["order.finished", "refund.completed", "alert.triggered"],
  "sign_algorithm": "hmac_sha256",
  "retry_policy": {
    "max_attempts": 4,
    "backoff_sec": [1, 5, 30, 120]      // 指数退避(沿用 § 退款 SOP 经验值)
  },
  "enabled": true
}
```

**响应(201)**:
```json
{
  "code": 0,
  "data": {
    "subscription_id": 17,
    "subscription_name": "BI 系统订单同步",
    "target_url": "https://bi.example.com/webhook/charge",
    "sign_secret": "whsec_abc123xyz...",  // 签名密钥,**仅此一次返回明文**,前端必须提示客户复制
    "sign_secret_prefix": "whsec_abc",    // 用于后续列表展示(模糊匹配,不全量回显)
    "event_types": ["order.finished", "refund.completed", "alert.triggered"],
    "enabled": true,
    "created_at": "2026-09-25T14:00:00Z"
  }
}
```

**业务逻辑**:
1. 校验 `target_url` 合法(HTTPS + 非内网 IP,防 SSRF)
2. 校验 `event_types` 在预置枚举内(`order.*` / `refund.*` / `alert.*` / `device.*` / `invoice.*`)
3. **生成签名密钥**:`openssl rand -hex 32`(32 字节随机串)
4. **加密存储**:`AES_ENCRYPT(sign_secret, master_key)` 存 `webhook_subscription.sign_secret_enc`(仅创建者可见明文,后续 GET 只回显前缀)
5. INSERT `webhook_subscription` + 写 `audit_log`(密钥明文不入审计,只记"密钥生成"动作)
6. 缓存 `webhook:list`(TTL 5 min)

**错误码**:
- `2015`: Webhook URL 不合法(非 HTTPS / 内网 IP / 格式错)
- `1005`: `event_types` 不在预置枚举

### `POST /api/v1/admin/webhooks/{sub_id}/test`

**鉴权**:[角色] `webhook.test`

**请求体**:
```json
{
  "test_payload": { "hello": "world" }
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "test_id": 999,
    "delivered": true,
    "http_status": 200,
    "response_excerpt": "{\"received\": true}",  // 截断前 500 字符
    "latency_ms": 234
  }
}
```

**业务逻辑**:
1. 查 `webhook_subscription WHERE id=$sub_id AND deleted_at IS NULL`
2. 用 `sign_secret` 签名 payload(测试事件 `webhook.ping`)
3. **同步** HTTP POST 到 `target_url`,超时 5 s
4. INSERT `webhook_delivery_log(delivery_type='test', http_status=..., latency_ms=...)`(不分发到目标 URL,只本地回环)
5. 返回结果(成功 / 失败都返回,不抛错)

---

## J. OTA 配置

### `POST /api/v1/admin/ota/packages`

**鉴权**:[角色] `ota.package.upload`

**请求体**(multipart/form-data 或 JSON + OSS 预签名):
```json
{
  "package_name": "Xunda-XD220V-v1.3.0",
  "vendor": "Xunda",
  "model": "XD-220V-10A",
  "firmware_version": "v1.3.0",
  "previous_version": "v1.2.3",      // 升级前的版本(可选)
  "file_size_bytes": 2048576,
  "file_sha256": "abc123...",          // 客户端上传后计算
  "changelog": "1. 修复温度过高误报\n2. 优化充电曲线算法",
  "release_type": "stable",            // "stable" / "beta" / "emergency"
  "min_battery_for_install": 30       // 设备电量 ≥ 30% 才允许安装
}
```

**业务逻辑**:
1. 校验 `firmware_version` 唯一(`vendor + model + firmware_version` 复合唯一,UK)
2. **OSS 上传**:客户端用本接口返回的预签名 URL 直传 OSS(节省 admin 带宽);admin 仅存元数据
3. INSERT `ota_package(file_url=oss_url, file_sha256=..., ...)` + 写 `audit_log`
4. 校验 `file_sha256` 与客户端传的一致性(可选)

### `POST /api/v1/admin/ota/schedules`

**鉴权**:[角色] `ota.schedule.create`

**请求体**:
```json
{
  "package_id": 12,
  "target_filter": {                   // 目标设备筛选
    "vendor": "Xunda",
    "model": "XD-220V-10A",
    "station_ids": [12, 13, 14],       // 空数组 = 全部匹配 vendor+model 的设备
    "current_firmware_max": "v1.2.9"  // 仅升级 ≤ 此版本的设备
  },
  "scheduled_window": {
    "start_at": "2026-09-26T02:00:00Z",  // 凌晨低峰
    "end_at": "2026-09-26T05:00:00Z"
  },
  "rollout_strategy": "canary",        // "canary" 金丝雀(10%) / "batch" 分批(50% → 100%) / "all" 全量
  "auto_rollback_on_failure": true,   // 失败自动回滚到 previous_version
  "notify_on_complete": true           // 完成后通知客户运营
}
```

**业务逻辑**:
1. 校验 `package_id` 存在 + `release_type='stable'`
2. 校验 `target_filter` 至少有一个筛选条件(防止误操作全网)
3. 预演匹配设备数(SELECT COUNT)→ 返回 `preview_match_count` 给客户运营二次确认
4. INSERT `ota_schedule(status='pending')` + 写 `audit_log`
5. **异步触发** worker 任务在 `scheduled_window.start_at` 启动推送(写 `ota_schedule_stream`,§ 5.1 真实 Stream 名)

### `POST /api/v1/admin/ota/schedules/{sched_id}/execute`

**鉴权**:[角色] `ota.schedule.execute`(客户管理员)

**业务逻辑**:
1. 查 `ota_schedule WHERE id=$sched_id AND status IN ('pending','paused')` → 否则返回 `2017`(状态不允许)
2. **二次确认**(本期前端必须弹窗确认,后端校验 `confirm_token` header `X-OTA-Confirm=YES_I_AM_SURE`)
3. UPDATE `status='running', started_at=NOW()` + 写 `audit_log`(高敏操作)
4. 立即发 `ota_schedule_stream` 事件 → worker 消费 → 调 gateway 推 OTA

---

## K. 计费与分账模板

### `POST /api/v1/admin/settings/charge-rules`

**鉴权**:[角色] `charge_rule.create`(客户运营)

**请求体**:
```json
{
  "rule_name": "万达广场白天时段",
  "pricing_template_id": 3,           // 引用模板
  "station_id": 12,                   // 绑定站点
  "time_of_use": [
    { "period": "peak", "start": "08:00", "end": "11:00", "electric_price_cents": 110, "service_price_cents": 50 },
    { "period": "off_peak", "start": "11:00", "end": "18:00", "electric_price_cents": 55, "service_price_cents": 30 }
  ],
  "power_band": [                     // 功率分档
    { "max_power_w": 200, "price_cents_per_kwh": 30 },
    { "max_power_w": 500, "price_cents_per_kwh": 50 },
    { "max_power_w": null, "price_cents_per_kwh": 80 }
  ],
  "effective_from": "2026-09-25T00:00:00Z",
  "effective_until": null             // 永久有效
}
```

**业务逻辑**:
1. 校验 `pricing_template_id` 存在
2. 校验 `station_id` 存在 + 未被其他 `charge-rule` 占用(一站一规则)
3. INSERT `pricing_rule` + 写 `audit_log`
4. **缓存失效**:`DEL pricing_rule:$station_id`(user / billing 服务缓存)

### `POST /api/v1/admin/settings/split-templates/{tpl_id}/parties`

**鉴权**:[角色] `split_template.update`(客户管理员)

**请求体**:
```json
{
  "parties": [
    { "party_type": "operator", "party_name": "本公司", "ratio_bp": 4000 },     // 40.00%
    { "party_type": "property", "party_name": "万达物业", "ratio_bp": 3500, "settlement_account": "6222021234567890" },
    { "party_type": "franchisee", "party_name": "李老板加盟", "ratio_bp": 2500, "settlement_account": "6222029876543210" }
  ]
}
```

> **`ratio_bp` 单位**:基点(bps),10000 = 100.00%。**admin 端只校验单条比例格式**(`0 < ratio_bp < 10000`,整数);`SUM(ratio_bp) = 10000` 的聚合校验**由 billing 服务在消费 `charge_ended_stream` 后实际算分账时执行**(详见 `docs/api/billing.md` § 三 `POST /split`),避免两端缓存不一致导致校验绕过。

**业务逻辑**:
1. 校验 `tpl_id` 存在 + `status='enabled'`
2. **单条校验**(每条):`0 < ratio_bp < 10000` + 整数 + 不重复 `party_type`(同参与方不重复)→ 失败返回 `1005`
3. **事务内**:
   - DELETE 旧 `split_party WHERE tpl_id=$tpl_id`
   - INSERT 新 `split_party`(按数组顺序)
4. **不校验 SUM**(聚合校验下放到 billing,见 `docs/api/billing.md`)
5. 写 `audit_log`(before / after snapshot 必填,比例变更是高敏操作)
6. **缓存失效**:`DEL split_template:$tpl_id`(user / billing 服务缓存)

**错误码**:
- `1005`: 单条比例格式错(非整数 / ≤ 0 / ≥ 10000 / 重复参与方)
- `2011`: 模板已被引用且不允许修改

---

## L. 审计与导出

### `GET /api/v1/admin/audit-logs`

**鉴权**:[角色] `audit.read`(客户管理员 / 监管方)
**限流**:每 user 30 req/min(数据量大)

**请求 query**:
| 参数 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `page` | int | 1 | — |
| `page_size` | int | 50 | 最大 200 |
| `actor_id` | int | — | 按操作人过滤 |
| `resource_type` | string | — | `user` / `station` / `device` / `coupon` / `webhook` / `ota` / `refund` / `invoice` 等 |
| `resource_id` | string | — | 资源 ID |
| `action` | string | — | `user.create` / `user.delete` / `webhook.create` 等 |
| `started_from` | ISO 8601 | — | 起始时间 |
| `started_to` | ISO 8601 | — | 截止时间 |

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "total": 87654,
    "items": [
      {
        "log_id": 9876543,
        "actor_id": 7,
        "actor_name": "张三",
        "action": "refund.approve",
        "resource_type": "refund",
        "resource_id": "12345",
        "request_ip": "203.0.113.5",
        "comment": "同意全额退款",
        "before_snapshot": null,    // 操作前状态(部分操作无)
        "after_snapshot": { "status": "approved" },
        "created_at": "2026-09-25T14:00:00Z"
      }
    ]
  }
}
```

**业务逻辑**:查 `audit_log` 表(按月分区,§ 4.6 数据保留 ≥ 3 年)。

### `POST /api/v1/admin/export/orders`

**鉴权**:[角色] `export.create`

**请求体**:
```json
{
  "filter": {
    "started_from": "2026-09-01T00:00:00Z",
    "started_to": "2026-09-30T23:59:59Z",
    "station_id": 12,
    "status": "finished"
  },
  "format": "csv",                  // "csv" / "xlsx"
  "fields": ["order_no", "started_at", "ended_at", "total_fee_cents", "status"]  // 字段裁剪
}
```

**响应(202)**:
```json
{
  "code": 0,
  "data": {
    "task_id": 501,
    "estimated_rows": 12345,
    "estimated_size_mb": 5,
    "status": "queued",
    "estimated_complete_at": "2026-09-25T14:05:00Z"
  }
}
```

**业务逻辑**:
1. 校验时间窗 ≤ 90 天(防止超大数据量导出导致 OOM)
2. 校验参数(filter / format / fields)合法
3. **HTTP 调 worker 服务** `POST /api/v1/internal/scheduled-tasks/export_run/trigger`(路径详见 `docs/api/worker.md` § 零),请求体携带 filter / format / fields + 触发人信息
4. 拿到 worker 返回的 `task_id` + 写 `admin_db.audit_log(action='export.create', params=...)`
5. 前端轮询 `GET /api/v1/admin/export/tasks/{task_id}`(本节)→ admin 内部**透传**到 worker `GET /api/v1/internal/export/tasks/{task_id}`,从 worker 拿状态 / file_url
6. 完成后前端调 `/download`(本节)→ admin 拿 worker 的 file_url(OSS 预签名 URL,30 min 过期)**直接返回给前端**(不再二次签名)

**错误码**:
- `1005`: 时间窗 > 90 天
- `2020`: 导出任务不存在(GET 时)

### `GET /api/v1/admin/export/tasks/{task_id}/download`

**鉴权**:[角色] `export.download`

**业务逻辑**:
1. **HTTP 调 worker** `GET /api/v1/internal/export/tasks/{task_id}`(路径见 `docs/api/worker.md` § 零)
2. 校验 `status='completed'` → 否则返回 `1005`(任务未完成)
3. 把 worker 返回的 `file_url`(OSS 临时签名 URL,**已含签名**,30 min 过期)直接透传给前端

> **设计说明**:导出任务状态全部存于 `worker_db.scheduled_task`(本期方案,沿用 cross-reference § 4.2 注释);admin 端不建 `export_task` 表,避免跨 schema 直连(§ 4.2)。

---

## 文档维护

- 修改本文件需在 PR 标题写 `api(admin): <简短描述>`,并在 PR 描述中说明影响哪些端点
- 任何新增 / 删除 / 修改端点必须同步更新 `services/admin/src/openapi.rs` 与本文件
- CI 检查:OpenAPI 规范与本文件端点清单必须一致(脚本 `tools/check-api-consistency.ts`)
- **跨服务一致性**:admin 通过 HTTP 调 user / gateway / billing / worker 的内部接口命名,必须与对应服务 API 文档一致;新增 admin 端点若依赖其他服务接口,必须先在对方服务的 API 文档中落地路径
- **审计一致性**:任何 admin 写端点必须在 `services/admin/src/audit_log.rs` 的 `audit_action!()` 宏中注册,否则 CI 拒绝合并
- **导出任务**:统一由 worker 服务承接,详见 `docs/api/worker.md` § 零(本期新增 3 个内部 HTTP 端点)

### 当前站点操作权限（2026-09-26）

站点 GET 列表/详情需 station.read，POST 需 station.create，PUT 需 station.update，DELETE 需 station.delete。接口查询当前数据库授权，返回 403 不触发登录失效。列表 data.permissions 包含当前角色的站点权限码，供按钮启用使用；客户端该列表不构成授权依据。

站点列表 GET /api/v1/admin/stations 现支持 page（默认 1）、page_size（默认 20，1..100）、keyword（最长 128 字符，匹配编码/名称/地址）、status（active/disabled/construction）。响应 data 包含 items、total、page、page_size、permissions。关键词中的 % 和 _ 按字面匹配。

### 设备查询当前实现补充

`GET /api/v1/admin/devices` 要求 `device.read`。参数：`page`（默认 1）、`page_size`（默认 20，1–100）、`keyword`（最多 128 字符，按设备编号、型号、有效站点名称/编码做字面子串匹配）、`status`（enabled/disabled/retired/fault）、正整数 `station_id` / `vendor_id`。返回 `items,total,page,page_size,permissions`；每行包含 `station_name,station_code`。状态为管理状态，不表示在线遥测。

详情同样实时校验 `device.read`。设备订单入口要求 `device.read` 和 `order.read`，沿用订单分页/日期筛选，路径设备编号覆盖查询参数中的设备编号；已删除/不存在设备返回 404，上游故障按真实错误返回。
