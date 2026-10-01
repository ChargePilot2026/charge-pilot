# Go 后台认证与仪表盘

以下接口已注册到 central `:8080`，与旧 admin 服务 `:8082` 区分。
OpenAPI：`GET /api/docs/admin.openapi.json`（仅本模块，不代表全站发布门禁通过）。
响应使用统一 `code/message/data/request_id/trace_id` 封套，金额为整数分。

| 方法 | 路径 | 请求或说明 |
| --- | --- | --- |
| POST | `/api/v1/admin/auth/login` | `{username,password}`；成功 data 包含 `token`（兼容现有后台）、`access_token`、`refresh_token`、`expires_in:900`、`admin_user_id`、`username`、`display_name`、`role`、`role_name`、`role_id`、`mfa_enabled`、`permissions`；启用 MFA 时先返回挑战 |
| POST | `/api/v1/admin/auth/refresh` | `{refresh_token}`；返回同登录结构，旧 refresh token 不可再次使用；7 天绝对有效期不续期 |
| POST | `/api/v1/admin/auth/logout` | Bearer token；撤销当前会话及其刷新令牌，返回 `{revoked:true}` |
| GET | `/api/v1/admin/auth/me` | Bearer token；当前账号、角色及数据库中的实时权限，包含 `display_name/role_name/role_id/mfa_enabled` |
| POST | `/api/v1/admin/auth/mfa` | `{mfa_challenge,code}`；完成已验过密码的登录挑战后签发会话 |
| POST | `/api/v1/admin/auth/mfa-settings` | Bearer token，仅本人有效会话；绑定、确认或关闭 TOTP，见下方流程 |
| POST | `/api/v1/admin/auth/change-password` | Bearer token，`{old_password,new_password}`；新密码 12–72 字节且不能与旧密码相同；成功立即使该账号所有旧会话失效 |
| GET | `/api/v1/admin/dashboard` | Bearer token 且需 `dashboard.read`；充电中订单、本日订单/结算金额、用户统计、站点/设备数、待处理告警及近 7 天趋势 |

密码使用 bcrypt；失败 5 次锁定 30 分钟，禁用/删除账号禁止登录。登录及刷新按连接 IP 每分钟最多
5 次，429 附 `Retry-After:60`。默认不信任客户端转发 IP 头；反向代理后的限流按代理连接 IP 计数，
部署独立可信代理网段配置仍待补齐。所有业务授权读取当前角色权限，不以 JWT 中的旧角色缓存授权。
账号初始化、登录成功/失败、改密写入审计；不记录密码和 token。

启用 MFA 时口令验证成功后只返回 `mfa_required:true` 与 5 分钟有效的 `mfa_challenge`，不签发会话；需用验证器验证码完成登录。后台已接入会话自动续期、账号管理及按实时权限过滤菜单。
仪表盘按北京时间划分日期，金额只统计已有 `total_cents` 的结束订单，不包含待计费订单，未扣除后续退款。

## 仪表盘统计字段（2026-10-01）

| 字段 | 统计口径 |
| --- | --- |
| `charging_orders` | 当前充电中的订单数 |
| `today_orders` | 本日创建的订单数 |
| `today_settled_cents` | 本日已结束且已计费订单的实结金额，单位分；不扣减后续退款 |
| `total_users` | 充电用户总数 |
| `new_users` | 本日新建的充电用户数 |
| `today_charging_users` | 按本日 `started_at` 去重后的充电用户数 |
| `station_count` / `device_count` | 站点数 / 设备数 |
| `active_alerts` | `active` 或 `acknowledged` 的待处理告警数 |
| `daily_trend` | 最近 7 个北京时间自然日的 `day/completed_orders/settled_cents`，按日期升序；无记录日期补 0 |
| `updated_at` | 本次快照的生成时间 |

首页按上述 9 项指标显示三行卡片，并用柱状图展示近 7 天订单与结算金额。刷新按钮后只保留更新时间；“本日充电用户数”按实际开始充电时间统计，与创建过订单的用户数分开。

## PC 会话自动续期

前端保存登录返回的刷新令牌，在访问令牌 401 后自动调用 refresh 并仅重试原请求一次。Web Locks 串行化同源标签页的刷新、登录与退出，旧请求不能覆盖后来的账号。刷新网络故障保留会话供重试，刷新令牌失效才清除并跳转登录。资金表单使用独立的登录会话标识区分账号切换与同会话令牌轮换。刷新接口与会话轮换仍由后端实时校验；前端不改变有效期或绕过审核权限。

## 本人双因素认证（2026-10-01）

顶部账号菜单的“账号安全”和管理员列表本人行的 MFA 操作进入 `/security`；专用接口不要求管理员账号管理权限，不能指定其他账号。

| action | 请求字段 | 响应与行为 |
| --- | --- | --- |
| `enrol` | `password` 当前密码 | 返回 `enrollment_id/secret/otpauth_uri/expires_in:300/mfa_enabled:false/sessions_revoked:false`；5 分钟待绑定密钥存入 Redis 并绑定本人账号、会话与凭证版本，重新绑定使旧登记失效；已启用时拒绝，不覆盖原密钥，不返回当前验证码 |
| `confirm` | `enrollment_id/code` 验证器六位码 | 校验未过期登记与验证码，启用并递增凭证版本；返回 `mfa_enabled:true/sessions_revoked:true`，旧会话全部失效，需重新登录 |
| `disable` | `password/code` 当前密码与当前验证器六位码 | 同时校验后关闭并递增凭证版本；返回 `mfa_enabled:false/sessions_revoked:true`，需重新登录 |

密码或验证码错误返回 400（1002），过期或被替换登记返回 409（2009），每分钟超过 5 次返回 429（4291），依赖故障返回 503（5003）；错误输入不会被解释为当前登录会话过期。启用、关闭与安全审计在数据库事务内完成，不记录密码、密钥、URI 或验证码。
