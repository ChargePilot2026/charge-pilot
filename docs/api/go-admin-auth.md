# Go 后台认证与仪表盘

以下接口已注册到 central `:8080`，与旧 admin 服务 `:8082` 区分。
OpenAPI：`GET /api/docs/admin.openapi.json`（仅本模块，不代表全站发布门禁通过）。
响应使用统一 `code/message/data/request_id/trace_id` 封套，金额为整数分。

| 方法 | 路径 | 请求或说明 |
| --- | --- | --- |
| POST | `/api/v1/admin/auth/login` | `{username,password}`；成功 data 包含 `token`（兼容现有后台）、`access_token`、`refresh_token`、`expires_in:900`、`admin_user_id`、`username`、`role`、`permissions` |
| POST | `/api/v1/admin/auth/refresh` | `{refresh_token}`；返回同登录结构，旧 refresh token 不可再次使用；7 天绝对有效期不续期 |
| POST | `/api/v1/admin/auth/logout` | Bearer token；撤销当前会话及其刷新令牌，返回 `{revoked:true}` |
| GET | `/api/v1/admin/auth/me` | Bearer token；当前账号、角色及数据库中的实时权限，包含 `display_name` 和 `role_id` |
| POST | `/api/v1/admin/auth/change-password` | Bearer token，`{old_password,new_password}`；新密码 12–72 字节且不能与旧密码相同；成功立即使该账号所有旧会话失效 |
| GET | `/api/v1/admin/dashboard` | Bearer token 且需 `dashboard.read`；充电中订单、本日订单/结算金额、用户统计、站点/设备数、待处理告警及近 7 天趋势 |

密码使用 bcrypt；失败 5 次锁定 30 分钟，禁用/删除账号禁止登录。登录及刷新按连接 IP 每分钟最多
5 次，429 附 `Retry-After:60`。默认不信任客户端转发 IP 头；反向代理后的限流按代理连接 IP 计数，
部署独立可信代理网段配置仍待补齐。所有业务授权读取当前角色权限，不以 JWT 中的旧角色缓存授权。
账号初始化、登录成功/失败、改密写入审计；不记录密码和 token。

启用 MFA 的已有账号当前拒绝登录，不绕过第二因素；TOTP 绑定与验证仍未交付。
后台现有页面仍使用短期 token，自动刷新交互尚未接入。账号 CRUD、菜单/数据/字段权限仍未完成。
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
