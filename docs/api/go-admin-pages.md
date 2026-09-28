# Go 后台业务页面接口（2026-09-29）

本清单描述当前 React 后台使用且已注册的 Go 接口，不等同于旧 admin.md 的 112 个目标端点全部交付。统一由 central :8080 提供，前缀 `/api/v1/admin`。认证见 [认证契约](go-admin-auth.md)，路由索引见 `/api/docs/admin.openapi.json`。

## 通用规则

- Bearer 管理员会话；每次请求从数据库检查有效账号、角色及操作权限。菜单过滤仅改善界面，不能替代服务端授权。
- JSON 响应采用 `code/message/data/request_id/trace_id`。非法参数 400、失效会话 401、无权 403、缺失记录 404、状态/重复冲突 409、依赖故障 503。不会把未知接口伪装为成功空列表。
- 分页资源使用 `page`、`page_size`（1–100），返回 `items,total,page,page_size`；站点、设备包含按钮权限。其他简单配置列表当前仅返回 `items`，尚未全面分页。
- 金额均为整数分；写接口限制请求大小、拒绝未知字段，操作人取自会话。数据库事务内写审计：admin 配置写 admin.audit_log，用户/退款/发票操作写 user.audit_log。
- central 按逻辑模块连接 admin/user/billing 各自 schema，不拼跨 schema SQL。设备预建档通过 gateway 内部 HTTP，不直连 gateway 库。

## 页面与实际路由

下表所有路径省略 `/api/v1/admin`；`{id}` 为资源 ID。

| 页面 | 路由 | 能力与限制 |
| --- | --- | --- |
| 站点 | GET/POST `/stations`；GET/PUT `/stations/{id}` | 新建、编辑、分页、keyword/status 筛选；代码保留唯一性、经纬度校验；当前页面无删除按钮，Go 未实现删除 |
| 设备 | GET `/devices`、`/devices/{id}` | keyword/status/station_id/vendor_id 筛选；显示管理状态，不代表在线遥测 |
| 导入 | GET/POST `/device-imports`；POST `/device-imports/{import_id}/retry` | CSV 预览后提交 JSON，每批 1–100；gateway 幂等建档，再落 admin 元数据；失败保留批次供显式重试 |
| 订单 | GET `/orders`、`/orders/{id}`、`/orders/{id}/timeline` | 分页及订单号、设备、站点、状态、时间筛选；从已有订单、计费和事件记录读取，不生成虚构计费 |
| 管理员 | GET/POST `/users`；GET `/roles` | 新建账号，密码 12–72 字节 bcrypt；只可分配不超出操作者权限的有效角色；未实现账号编辑/删除/MFA |
| 告警 | GET `/alerts`；POST `/alerts/{id}/ack` | 列表与确认，尚无新规则、自动恢复和通知执行器 |
| 优惠券 | GET/POST `/coupons`；PUT `/coupons/{id}`；GET `/coupons/{id}/stats`；POST `/coupons/{id}/grants` | 模板编辑、计数、按用户发放；限总量/个人额度与有效期，UUID 幂等；时长券使用 free_minutes；支付核销未接入 |
| 财务结算 | GET `/billing/settlements` | 读取既有 settled_record，不代表完成结算执行 |
| 发票 | GET `/billing/invoices`；POST `/billing/invoices/{id}/approve`、`reject` | 两名不同有效财务账号，以同一 HTTPS invoice_url 复核；拒绝需 reason；不会生成发票 PDF 或连接税控 |
| 退款申请 | POST `/orders/{id}/refunds` | request_id UUID、amount_cents、reason；终态订单，锁支付单检查剩余额度；申请本身不算第一签 |
| 退款队列 | GET `/billing/refunds`；POST `/billing/refunds/{refund_no}/approve`、`reject`、`retry` | 双财务签名后转自动执行；拒绝释放待退款额度；重试只唤醒原自动任务，不重置终态；按实时权限返回可用动作 |
| 钱包风控 | GET `/billing/wallet-risks`；POST `/billing/wallet-risks/{request_id}/review`、`release` | review 使用 approved/comment，release 使用 comment；审核与独立解冻分开，解冻不解除其他冻结 |
| Webhook | GET/POST `/webhooks` | name/url/event_types；仅建订阅配置，创建时显示一次 secret，列表仅前缀；投递、重试与 SSRF 执行边界待实现 |
| OTA | GET `/ota/packages`、`/ota/schedules` | 当前页面只读包和计划；没有固件上传或设备推送执行 |
| 公告 | GET/POST `/announcements` | 创建即 published，校验标题/内容/起止时间与 global/station/city 范围；非 global 必须 target_ids |
| 客服坐席 | GET/POST `/customer-service`；PUT/DELETE `/customer-service/{id}` | 配置 agent_wechat/agent_name/HTTPS path/priority/工作时间；DELETE 实为禁用，保留数据；微信真实接待未联调 |
| 反馈 | GET `/feedback`；POST `/feedback/{id}/reply` | action=reply 携带 reply_content；action=close 关闭；有状态检查与审计，尚无用户推送 |
| 报修 | GET `/device-fault-reports`、`/{id}/history`；POST `/{id}/dispatch`、`/{id}/resolve` | 派单/改派需有效 fault.resolve 账号；仅当前指派人可修复/关闭，修复备注必填，历史持久化 |
| 设置 | GET/PUT `/whitelabel`；GET `/settings/charge-rules` | 白标配置保存；规则页支持版本发布与停用；正式结束订单计费及模拟差额退款已接线，见 go-charge-lifecycle.md |

## 金额与重试边界

退款 worker 使用同一 refund_no 查询渠道，必要时创建，再查询恢复；成功回执和已退累计同事务写入，重复成功不重复入账。人工退款必须完成两名财务签名，第一签被撤销权限时不得由第二签放行。钱包风险批准时按原充值单分配额度并预留余额，渠道成功才扣减余额和记录流水；渠道明确 CLOSED/ABNORMAL 时释放该笔预留，网络或未知结果继续保留。

开发环境使用模拟支付。真实商户/硬件联调、结算冲减、发票生成/发送、Webhook 投递、OTA 执行、数据范围与字段脱敏、完整导出仍不在本次页面修复完成范围。旧 admin.invoice_review 未迁移到 user.invoice_admin_review，正式数据切换前须处理旧审核记录。

## 验证

`TestAdminPagesIntegration` 覆盖已注册页面列表、鉴权、分页/输入、站点、导入、优惠券、配置、反馈、报修、退款双签/拒绝/重试、发票审核和钱包风控。使用 `scripts/test-integration.sh -race` 在隔离 MySQL/Redis 上运行，避免默认跳过数据库测试。实际点击情况见 [页面验收记录](../testing/admin-pages-2026-09-29.md)。

## 计费规则版本发布

GET `/settings/charge-rules` 返回规则完整时段、站点、状态、版本及权限。POST 同路径要求 `pricing.rule.create`，body 包含 UUID `request_id`、`name`、`station_id`、`expected_version`、`mode`、`time_of_use`、`service_fee_cents_per_kwh`、`service_fee_cents_per_min`、`min_charge_cents`。每个时段含 `period/start/end/electric_price_cents`，可选 `service_price_cents`。时间使用北京时间 HH:mm，全部时段必须完整覆盖一天且不得重叠，结束可为 24:00，金额为 0–1000000 整数分。

站点行锁串行分配新版本；expected_version 必须与该站点最大版本一致。发布会在同一事务停用同站点旧规则、写新记录、幂等回执及审计，旧规则内容不改。相同账号及完全相同请求重放返回同一 ID/version，修改请求返回 409。POST `/{id}/disable` 需 `pricing.rule.update`，只停用指定记录，不恢复旧版本，不影响已创建支付快照。迁移 admin 0027。

## 实际计量核实

- GET `/billing/meter-reviews`：`finance.read`，支持 page/page_size/keyword/status（pending/resolved）。返回原始计费来源及所有追加核实记录。
- POST `/billing/meter-reviews/{charge_order_id}/propose`：`billing.meter.review` 且有效客户财务账号。body 为 UUID `request_id`、`reason`、`segments`；各段包含 RFC3339 `started_at/ended_at` 和整数 `energy_wh`。段必须连续覆盖原始时间、总电量一致，且每段只能处于同一有效费率。不能改订单、总电量、秒数或冻结价格。
- POST `/billing/meter-reviews/{charge_order_id}/decide`：同权限，body 为 `review_id/approve/reason`。另一名财务复核；通过时核对原始来源未变且首审账号/权限仍有效。拒绝必须填写依据，之后可新建另一份核实记录。

核实及决定保存在 user 的追加审计记录中。第二人通过和恢复计费任务同事务；billing 投递回写成功后将异常队列标为 resolved。设备原始回执从不覆盖。当前仅补充分段计量，损坏的价格快照不能通过本接口人工改价。
