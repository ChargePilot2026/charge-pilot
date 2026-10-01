# Go 后台业务页面接口（2026-10-01）

本清单描述当前 React 后台使用且已注册的 Go 接口，不等同于 admin.md 的目标端点全部交付。统一由 central :8080 提供，前缀 `/api/v1/admin`。认证见 [认证契约](go-admin-auth.md)，路由索引见 `/api/docs/admin.openapi.json`。

2026-10-01 已移除后台客服坐席配置及用户端在线客服页面、入口与接口。“反馈报修”位于“设备运维”；客服角色、反馈处理和钱包风控审核保留，按各自权限授权。

2026-10-01 已移除告警配置（规则与订阅）和 OTA 整个功能；设备主动上报的烟雾、温度与故障告警及告警记录、确认、恢复保留。反馈报修归入设备运维。现有部署的历史表与数据不在本轮清理范围。

顶部导航位于 `ChargePilot · 当前菜单标题` 之后，子菜单按业务分组展开；账号操作保留在右侧。“设备运维 → 反馈报修”提供反馈和设备报修两个页签。

## 通用规则

- Bearer 管理员会话；每次请求从数据库检查有效账号、角色及操作权限。菜单过滤仅改善界面，不能替代服务端授权。
- JSON 响应采用 `code/message/data/request_id/trace_id`。非法参数 400、失效会话 401、无权 403、缺失记录 404、状态/重复冲突 409、依赖故障 503。不会把未知接口伪装为成功空列表。
- 分页资源使用 `page`、`page_size`（1–100），返回 `items,total,page,page_size`；站点、设备、厂商包含按钮权限。其他简单配置列表当前仅返回 `items`，尚未全面分页。
- 金额均为整数分；写接口限制请求大小、拒绝未知字段，操作人取自会话。数据库事务内写审计：admin 配置写 admin.audit_log，用户/退款/发票操作写 user.audit_log。
- central 按逻辑模块连接 admin/user/billing 各自 schema，不拼跨 schema SQL。设备预建档通过 gateway 内部 HTTP，不直连 gateway 库。

## 页面与实际路由

下表所有路径省略 `/api/v1/admin`；`{id}` 为资源 ID。

| 页面 | 路由 | 能力与限制 |
| --- | --- | --- |
| 站点 | GET/POST `/stations`；GET/PUT `/stations/{id}`；GET `/stations/{id}/configuration` | 新建、编辑、分页、keyword/status 筛选；从名称或行内工作区直接管理本站计费、套餐、设备及策略；经纬度放在基本信息中；不提供删除 |
| 设备 | GET `/devices`、`/devices/{id}`；PUT `/devices/{id}`、`/devices/{id}/status` | keyword/status/station_id 筛选；新建复用设备开通流程；设备列表与站点工作区的设备列表支持编辑管理资料及启停；从设备行进入所属站点的计费与套餐配置；显示管理状态，不代表在线遥测 |
| 厂商 | GET/POST `/vendors`；GET/PUT `/vendors/{id}`；GET `/vendor-options` | 设备运维中的厂商管理：分页、编码/名称搜索、状态筛选、新建、编辑和启停；设备新建从可选厂商接口读取已启用厂商；不提供删除 |
| 导入 | GET/POST `/device-imports`；POST `/device-imports/{import_id}/retry` | CSV 预览后提交 JSON，每批 1–100；gateway 幂等建档，再落 admin 元数据；失败保留批次供显式重试 |
| 充电订单 | GET `/orders`、`/orders/{id}`、`/orders/{id}/timeline` | 仅充电业务；分页及订单号、设备、站点、业务状态、支付状态、启动来源、时间筛选；显示实际充电时长与累计成功退款金额；详情保留原流程及异常信息 |
| 支付订单 | GET `/payment-orders` | 只读支付流水，包含充电付款与钱包充值；支持订单号精确查询及业务类型、支付状态、支付方式筛选；不把充值列为充电订单 |
| 充电用户 | GET `/charge-users`、`/charge-users/{id}` | 后台第一个以"人"而非以"单"为入口的视图：列表给昵称、完整手机号、状态、订单数、累计消费、钱包余额与最后登录，档案再给最近 20 笔订单（业务状态与支付状态分别展示，保留原内部 status）及券/报障计数。只读，不含建号与解冻。手机号按完整号码精确搜索，输入后四位查不出来（库中只有密文与不可逆哈希） |
| 管理员 | GET/POST `/users`；GET `/roles`；PUT/DELETE `/admin-users/{id}` | 新建账号，密码 12–72 字节 bcrypt；只可分配不超出操作者权限的有效角色；支持资料、角色和状态管理，角色未改变的资料保存不撤销会话 |
| 告警 | GET `/alerts`；POST `/alerts/{id}/ack` | 设备主动上报告警列表与确认；烟雾、温度和设备故障由 worker 同步，正常心跳可自动恢复 |
| 优惠券 | GET/POST `/coupons`；PUT `/coupons/{id}`；GET `/coupons/{id}/stats`；POST `/coupons/{id}/grants` | 模板编辑、计数、按用户发放；限总量/个人额度与有效期，UUID 幂等；时长券使用 free_minutes；支付核销未接入 |
| 财务结算 | GET `/billing/settlements` | 读取既有 settled_record，不代表完成结算执行 |
| 发票 | GET `/billing/invoices`；POST `/billing/invoices/{id}/approve`、`reject` | 两名不同有效财务账号，以同一 HTTPS invoice_url 复核；拒绝需 reason；不会生成发票 PDF 或连接税控 |
| 退款申请 | POST `/orders/{id}/refunds` | request_id UUID、amount_cents、reason；终态订单，锁支付单检查剩余额度；申请本身不算第一签 |
| 退款队列 | GET `/billing/refunds`；POST `/billing/refunds/{refund_no}/approve`、`reject`、`retry` | 双财务签名后转自动执行；拒绝释放待退款额度；重试只唤醒原自动任务，不重置终态；按实时权限返回可用动作 |
| 钱包风控 | GET `/billing/wallet-risks`；POST `/billing/wallet-risks/{request_id}/review`、`release` | review 使用 approved/comment，release 使用 comment；审核与独立解冻分开，解冻不解除其他冻结 |
| Webhook | GET/POST `/webhooks` | name/url/event_types；仅建订阅配置，创建时显示一次 secret，列表仅前缀；投递、重试与 SSRF 执行边界待实现 |
| 公告 | GET/POST `/announcements` | 创建即 published，校验标题/内容/起止时间与 global/station/city 范围；非 global 必须 target_ids |
| 反馈 | GET `/feedback`；POST `/feedback/{id}/reply` | action=reply 携带 reply_content；action=close 关闭；有状态检查与审计，尚无用户推送 |
| 报修 | GET `/device-fault-reports`、`/{id}/history`；POST `/{id}/dispatch`、`/{id}/resolve` | 派单/改派需有效 fault.resolve 账号；仅当前指派人可修复/关闭，修复备注必填，历史持久化 |
| 设置 | GET/PUT `/whitelabel`；GET `/settings/charge-rules` | 白标配置保存；规则页支持版本发布与停用；正式结束订单计费及模拟差额退款已接线，见 go-charge-lifecycle.md |

计费模板与套餐模板统一位于“充电运营 → 模板”：`/templates` 页面包含“计费模板”和“套餐模板”两个页签，`tab=pricing` 或 `tab=packages` 记录当前页签，刷新和浏览器前进/后退可恢复。旧 `/pricing-templates`、`/package-templates` 地址跳转到对应页签；菜单需要 `pricing.read`，两类模板的接口和写入权限保持各自的校验。

计费模板的费率编辑先选择电费与服务费口径，再填写时段费率。功率档位表默认展开，按档位服务费与电费在同一行填写；按电量、分钟或场次的服务费统一在口径区域填写。切换服务费口径保留本次编辑中的单价，保存仅提交当前口径适用的费用。各时段只有一个“新增档位”入口，点击直接追加到尾部，已有上限和费率保持原值。拆分和删除时段均使用按钮旁的 Popconfirm。

计费编辑分为“基本信息 → 费率 → 配置 → 用户界面展示”；全局策略、设备时长策略及刷卡下单统一在“配置”步骤维护。任意步骤可预览完整未保存草稿。POST `/settings/pricing-templates/preview`（需 `pricing.read`）复用正式 `pricing.Cost` 引擎计算各时段/档位、跨时段、功率变化、免费时长及渠道对比，并支持自定义开始时间、时长、功率。预览展示电费、服务费、倍率、最低电费和用户展示设置；费用停止阈值与刷卡时长限制单独标注，不将停止阈值当作账单截断金额。设备计费只预览执行策略，金额须从购买选项确定。预览不写数据库、不下发设备。

收费方式直接决定填写单位，编辑器不再提供“档位电费口径”下拉：实时功率分档电价固定元/度，按对应电量结算；最大功率固定元/小时，按整场最高功率选档，使用开始时段的单价乘整场时长；电量计费填写各时段的统一元/度电价。实时功率的档位服务费为元/度，最大功率的档位服务费为元/小时。通过预览查看当前方案的金额示例。

新建、修改、复制和再次应用的实时功率模板必须为 `tier_price_basis=per_kwh`。旧版 `per_hour_at_ceiling` 或省略该字段的模板标为“旧版 · 待转换”，候选接口返回不能再次应用的原因。编辑时展示原填写值和按原实际计算结果得到的等效元/度电价，用户点击转换后核对并保存；沿用旧算法的整数分截断，不改变功率上限、服务费和其它策略。无法识别的旧口径或等效电价超过金额上限时阻止转换，要求新建模板重新定价。已应用的站点规则和订单冻结快照仍兼容原算法，转换模板不会更新它们；不会批量迁移或自动重算历史账单。

计费配置统一从“站点 → 工作区”进入：默认计费、套餐及设备独立配置位于“计费与套餐”，启动/退款策略及本站计费下发记录位于“站点策略与下发记录”。独立“站点计费”菜单与 `/station-pricing` 前端路由已移除，不再提供跨站点配置页面或全站点下发记录入口。进入站点需 `station.read`，工作区中的计费及策略页签另需 `pricing.read`，写操作仍按实时操作权限及数据范围校验。

## 充电订单与支付订单（2026-10-01）

“充电运营”下的菜单分为“充电订单”和“支付订单”，两页及现有充电订单详情、时间线统一要求 `order.read`。充电订单保留 `/orders` 前端地址及已有接口，支付订单新增 `/payment-orders`。当前后台订单读取按 `order.read` 查询全量记录，尚未实现站点或厂商数据范围过滤；钱包充值不虚构站点归属。

`GET /orders` 只读取未删除的充电订单，兼容原来的 `order_no`、`device_id`、`station_id`、`started_from`、`started_to` 与内部 `status` 筛选，并新增 `business_status`、`payment_status`、`start_source`。订单号与设备号为完整编号精确匹配；时间范围沿用 `COALESCE(started_at,created_at)`，尚未启动的订单按创建时间筛选。分页默认 `page=1,page_size=20`，前端默认 10 条，可选 10/20/50/100。

| 字段 / 筛选 | 取值与口径 |
| --- | --- |
| `business_status` | `pending_start` 待启动、`charging` 充电中、`completed` 已完成；由 `charge_order.business_status` 读取，不受退款进度影响 |
| `payment_status` | `pending` 待支付、`paid` 已支付、`refunded` 全额退款、`partial_refunded` 部分退款；由充电订单的独立持久字段读取，仅实际支付确认或退款成功后同步 |
| `start_source` | `payment` 扫码支付、`balance` 余额支付、`card` 在线卡；先按在线卡会话判定 `card`，再按支付方式判定 `balance` 或 `payment`，无有效来源凭据时为 `null` |
| `payment_order_status` | 关联支付订单的原始流转状态，可为 `null`；与规范的四态 `payment_status` 分开 |
| `refunded_cents` | 已成功退款的累计金额，单位分；有效关联支付单未退时为 0，没有可见的未删除支付记录时为 `null` |
| `status` / `failure_reason` | 原内部生命周期与异常原因；取消、失败和退款流转在业务状态上归入已完成，界面通过原流程提示及详情辨明原因 |

充电订单仅包含扫码支付、余额支付与在线卡三种来源的充电记录，不包含钱包充值。列表与详情的两种状态各自展示；退款申请或渠道处理中尚未成功时仍显示已支付，不提前显示全额退款或部分退款。充电中的时长来自 `live.seconds`，过期采样以 `live.stale` 标记；已结束时使用 `duration_seconds` 实际设备读数。实时读数不可用且未记录实际时长时不编造数值。

充电用户档案 `GET /charge-users/{id}` 的 `recent_orders` 同样返回 `business_status` 与 `payment_status`，直接读取上述持久字段；原 `status` 保留详细技术流转。该摘要按订单 ID 倒序，最多 20 笔，不改变 `charge_user.read` 权限要求。

`GET /payment-orders` 从 `user_db.payment_order` 读取未删除的支付流水，参数为 `page`、`page_size`（接口默认 20，1–100）、`order_no`（完整支付订单号，最多 64 字符）、`biz_type`（`charge` / `wallet_recharge`）、`payment_status`（上述四态）和 `pay_method`（`wechat` / `balance`）。返回 `items,total,page,page_size`，条目含 `payment_order_id,order_no,user_id,biz_type,pay_method,status,payment_status,total_cents,paid_cents,refunded_cents,paid_at,created_at,charge_order_id,charge_order_no`。

支付列表的 `status` 保留原支付流转状态；规范 `payment_status` 按已到账与累计成功退款金额优先计算，支付终态用于已确认付款的兜底判断。`charge` 包含上述三种充电来源产生的扣款，`wallet_recharge` 仅为充值。`paid_at` 可为 `null`。仅 `biz_type=charge` 且关联同一用户的有效、未删除充电订单时返回 `charge_order_id` 与 `charge_order_no`；无有效关联时两者为 `null`，`wallet_recharge` 始终不关联充电订单。此页只提供查询，不新增支付、充值或退款操作。

## 设备资料编辑（2026-10-01）

设备列表和站点工作区的“设备”页签均提供编辑入口，使用同一个弹窗。编辑前读取 `GET /devices/{id}` 的最新资料；`{id}` 为 `device_id` 业务编号，而非后台数据库主键。GET 列表与详情均返回 `model,serial_no,install_at,warranty_until,tags,updated_at`，无标签时 `tags` 为 `[]`。`serial_no` 为管理资料中的硬件序列号。

`PUT /devices/{id}` 复用 `device.operate` 权限；该权限同时授权设备信息编辑与启停。服务端实时检查账号权限和站点、厂商数据范围，范围外设备或已删除设备返回 404。请求必须显式提交下面六个字段；未知字段、缺失字段或非法值返回 400（code `1005`）。

| 字段 | 约束 |
| --- | --- |
| `model` / `serial_no` | 字符串或 `null`；去除首尾空白，空白字符串归为 `null`，最多 128 个字符 |
| `install_at` / `warranty_until` | 含时区的 RFC3339 日期时间或 `null`；转换为 UTC 并保留毫秒，年份为 1000–9999；两者都有值时保修截止不得早于安装时间 |
| `tags` | 数组，最多 20 项；每项去除首尾空白后为 1–32 个字符，重复项拒绝；不接受 `null`，用 `[]` 清空 |
| `expected_updated_at` | 必填且非 `null`，采用 GET 返回的 `updated_at`；含时区的 RFC3339 日期时间，按 UTC 毫秒检查并发 |

保存仅更新以上五项管理资料，不改变设备编号、所属站点、厂商、协议、计费能力或运营状态。成功返回完整设备 DTO（包含新的 `updated_at`）；资料更新与 `device.update` 审计快照在同一事务中提交。版本不一致返回 409（code `2009`），提示“设备资料已被修改，请刷新后重试”，不得覆盖另一人的修改。启停仍单独使用 `PUT /devices/{id}/status`。

## 厂商管理（2026-09-30）

厂商页面位于“设备运维 → 厂商”，前端路径为 `/vendors`；列表可复制厂商 ID，供 CSV 导入填写 `vendor_id`。厂商数据由 gateway 的 `vendor` 表保存；central 通过内部服务接口读取和修改，后台不复制厂商表，也不直连 gateway 数据库。

- `GET /vendors`、`GET /vendors/{id}`：需要 `vendor.read`。列表支持 `page`（1–1000000）、`page_size`（1–100，默认 20）、`keyword`（编码或名称，最多 128 字符）和 `status`（`enabled`/`disabled`，留空不筛选）。列表返回 `items,total,page,page_size,permissions`；详情返回单个厂商。
- `POST /vendors`：需要 `vendor.create`，且账号没有配置厂商范围限制。创建须提供下面的全部五个字段。
- `PUT /vendors/{id}`：需要 `vendor.update`，同样提交完整五字段。编码不可修改；名称及状态可修改。适配器或连接协议变更只允许无关联设备的厂商，且新组合必须为当前支持的 `dc589`/`tcp`。保留原组合时允许编辑既有旧协议厂商的名称或状态。
- `GET /vendor-options`：需要 `device.import`，不要求 `vendor.read`。支持相同的分页和关键词查询，服务端始终限定 `status=enabled`，返回相同分页结构；用于设备新建的厂商搜索选择。当前设备新建仅允许选择 DC589/TCP 厂商，旧协议厂商会标明“暂不支持该协议”。

| 写入字段 | 约束 |
| --- | --- |
| `vendor_code` | 必填，1–64 位字母、数字、下划线或短横线；有效厂商之间编码不重复，创建后不可修改 |
| `vendor_name` | 必填，去除首尾空白后为 1–128 个字符，不能含控制字符 |
| `adapter_class` | 必填；新建只接受 `dc589`，界面显示为 DC589 协议 |
| `protocol` | 必填；新建只接受 `tcp`，界面固定显示 TCP 连接 |
| `status` | 必填，`enabled` 或 `disabled`；界面新建时默认已启用 |

厂商响应 DTO 包含 `id,vendor_code,vendor_name,adapter_class,protocol,status,enabled_at,created_at,updated_at`；`enabled_at` 可为 `null`。不返回适配器私有 `config_json`。列表、详情、修改及可选厂商查询都执行当前账号的厂商数据范围：配置了厂商 ID 范围时，仅可访问范围内厂商，范围外详情或修改返回 404；客户端提供的 `ids` 不作为授权依据。此类账号的列表权限中会移除 `vendor.create`，直接创建也会返回 403。没有厂商范围项时，厂商维度不受限。

启用后，厂商可用于设备新建和设备注册；停用后，该厂商的新设备开通及注册会被拒绝。停用保留厂商与历史设备记录，不执行删除，也不会通过此接口断开已有连接或停止正在进行的充电。启停使用同一 PUT 接口修改 `status`；成功创建或修改后，中台记录厂商审计快照。

缺失或范围外厂商返回 404，编码重复或修改不可变配置返回 409，非法字段返回 400，依赖故障返回 503。网关内部认证故障也映射为 503，不会使后台会话被误判为失效。

部署时先执行 gateway 的 `0010_vendor_live_code` 与 admin 的 `0047_vendor_permissions` 迁移，再重启 gateway 和 central 并更新前端。`0010` 为未删除厂商建立编码唯一索引；若旧数据含重复的有效编码，迁移会停止，须核对厂商及设备关联后重新执行，不会自动删除或改写记录。

## 金额与重试边界

退款 worker 使用同一 refund_no 查询渠道，必要时创建，再查询恢复；成功回执和已退累计同事务写入，重复成功不重复入账。人工退款必须完成两名财务签名，第一签被撤销权限时不得由第二签放行。钱包风险批准时按原充值单分配额度并预留余额，渠道成功才扣减余额和记录流水；渠道明确 CLOSED/ABNORMAL 时释放该笔预留，网络或未知结果继续保留。

开发环境使用模拟支付。真实商户/硬件联调、结算冲减、发票生成/发送、Webhook 投递、数据范围与字段脱敏、完整导出仍不在本次页面修复完成范围。旧 admin.invoice_review 未迁移到 user.invoice_admin_review，正式数据切换前须处理旧审核记录。

## 验证

`TestAdminPagesIntegration` 覆盖已注册页面列表、鉴权、分页/输入、站点、导入、优惠券、配置、反馈、报修、退款双签/拒绝/重试、发票审核和钱包风控。使用 `scripts/test-integration.sh -race` 在隔离 MySQL/Redis 上运行，避免默认跳过数据库测试。实际点击情况见 [页面验收记录](../testing/admin-pages-2026-09-29.md)。

## 套餐售价与结算（2026-09-30）

金额方案设置消费上限，按当前计费规则消费。固定时长套餐同时配置售价与 1–600 分钟的充电时长，例如 120 分钟售价 5 元：用户选择后预付 5 元，到时停止；充满收 5 元，提前结束按实际使用秒数占购买时长的比例收费，剩余退款，延迟停机不超过售价。该售价包含本次充电费用，不再按站点电费或服务费重复计价，不另设最低消费；套餐合价与支付报价一致记入服务费，不凭空拆分电费。

上架复制模板售价与时长；修改模板不影响在售记录和订单购买快照，下架后重新上架使用模板最新售价。旧的零售价时长模板必须由运营补价后才能上架；无售价的旧在售记录不再提供给用户选择，也不能直接提交 ID 启动，不会自动填入一个未经运营确认的价格。

## 站点工作区与计费发布（2026-09-30）

进入站点工作区时直接显示站点默认计费与通用套餐，不提供配置范围下拉框。设备独立配置从“设备例外配置”或“设备”页签中对应设备行的“计费与套餐”进入；设备配置保留“返回站点默认”入口。

站点列表中的名称和「工作区」打开侧边工作区，关闭后保留搜索、分页与列表。工作区集中展示基本信息、计费与套餐、本站设备、站点策略与下发记录。设备页的所属站点和「计费与套餐」也能直接进入对应工作区；设备新建会锁定当前站点。

- `GET /stations/{id}/configuration` 同时需要 `station.read` 与 `pricing.read`，返回 `station_id,default_rule,station_latest_version,offers,permissions,can_manage_default`。`default_rule` 是当前有效时段内 active 的站点默认配置，可能为 `null`；停用站点仍可查阅配置，其运营状态决定是否可开充。`station_latest_version` 读取整站规则链最新版本，包括停用或未到生效时间的记录；零设备站点也可读取和配置默认计费。`offers` 只含本站点未删除的套餐副本，包含已下架记录。
- `GET /settings/device-pricing?station_id={id}` 返回设备当前规则、独立/继承来源和各自规则链最新版本。套餐数量按用户侧口径计算：设备同模板的在售套餐覆盖对应通用套餐，其余通用套餐仍可选，不重复计数。
- `GET /settings/pricing-template-candidates?station_id={id}&preserve_device_overrides=true` 返回模板候选及不可用原因。固定设备范围可传 `device_id`；能力不满足、模板停用或口径失效的选项不可选择。
- `POST /settings/pricing-templates/{id}/apply` 需要 `pricing.rule.create`，请求包含 `station_id,device_id,request_id,expected_version`；设备为空串表示站点默认。`request_id` 为 UUID，同一次意图重试复用；`expected_version` 必须是相应规则链最新版本，不能用当前有效版本代替。`preserve_device_overrides:true` 只在整站发布时跳过当前有效独立规则设备：计量校验、设备模式更新与下发任务采用同一批继承设备。省略该字段保持旧应用语义。
- `POST /settings/device-pricing/reset` 需要 `pricing.rule.update`，请求包含 `station_id,device_id,keep_device_offers:true`。恢复计费继承保留该设备套餐；默认规则不存在、口径失效或设备能力不兼容时返回 409，事务内不产生部分改动。省略 `keep_device_offers` 保持旧行为，设备独立规则与套餐会一起停用。
- `POST /settings/package-templates/{id}/apply` 需要 `pricing.rule.create`，以 `station_id` 和可选 `device_id` 固定上架范围，接受可选 UUID `request_id`。精确目标已在售时返回 `replayed:true`；已下架时更新原在售记录为模板当前条款并返回 `relisted:true`。工作区在确认中展示本次上架的模板售价与时长，历史订单快照不变。
- `POST /settings/charge-offers/{id}/disable` 需要 `pricing.rule.update`，仅下架所选副本。工作区明确通用/设备独立范围；用户已有支付订单继续使用其原条款。

站点与设备查询、配置读取及上述操作均检查实时权限和数据范围。厂商受限账号只能访问对应厂商的设备及设备专属套餐，不能修改全站默认配置；`can_manage_default:false` 用于禁用全站操作。设备新建/导入及重试也检查每台设备的站点与厂商范围，越界请求在调用 gateway 前拒绝。`station_id` 过滤要求单个正整数，空值、重复参数或非数字返回 400，不能退化为无过滤查询。

发布成功说明规则已保存。设备侧计费如返回 `switch_pending:true`，表示仍需下发；重放结果也需在下发记录确认硬件状态。工作区不会把创建任务显示为下发成功。此轮没有新增表结构迁移，后端源码更新后需重启 central。GET `/settings/charge-rules` 只读；规则创建通过计费模板应用完成，不再提供同路径 POST。

## 实际计量核实

- GET `/billing/meter-reviews`：`finance.read`，支持 page/page_size/keyword/status（pending/resolved）。返回原始计费来源及所有追加核实记录。
- POST `/billing/meter-reviews/{charge_order_id}/propose`：`billing.meter.review` 且有效客户财务账号。body 为 UUID `request_id`、`reason`、`segments`；各段包含 RFC3339 `started_at/ended_at` 和整数 `energy_wh`。段必须连续覆盖原始时间、总电量一致，且每段只能处于同一有效费率。不能改订单、总电量、秒数或冻结价格。
- POST `/billing/meter-reviews/{charge_order_id}/decide`：同权限，body 为 `review_id/approve/reason`。另一名财务复核；通过时核对原始来源未变且首审账号/权限仍有效。拒绝必须填写依据，之后可新建另一份核实记录。

核实及决定保存在 user 的追加审计记录中。第二人通过和恢复计费任务同事务；billing 投递回写成功后将异常队列标为 resolved。设备原始回执从不覆盖。当前仅补充分段计量，损坏的价格快照不能通过本接口人工改价。

## 本轮新增接口（2026-09-29）

以下接口均由 central 在 `:8080` 提供，前缀 `/api/v1/admin`，认证与错误约定见上文。

### 分账、提现与对账

- `GET /billing/settlements`：`finance.read`。返回实际写入的分账记录及各方金额，`mode_b` 下 `split_pool_excluded_electric_cents` 为不参与分账的电费。
- `GET/POST /billing/withdraws`：`finance.read` / `finance.withdraw.create`。POST 需 UUID `request_id`、参与方与正数金额（单位分），同一 `request_id` 重放返回原单；只能提现已结算（分账状态为 paid）的余额。
- `POST /billing/withdraws/{withdraw_no}/decide`：`finance.withdraw.review`。`approve: true` 通过；带 `reason` 为拒绝且 reason 必填。通过与打款前都会在事务内重算可用余额。
- `POST /billing/withdraws/{withdraw_no}/pay`：`finance.withdraw.review`，登记打款；重复调用返回已记录状态而不重复付款。
- `GET/POST /billing/reconciles`、`POST /billing/reconciles/{id}/resolve`：`finance.read`。POST 需 `reconcile_type`（`wechat_refund`/`wechat_pay`/`split`/`withdraw`）、`date`（YYYY-MM-DD）与 `channel_amounts` 数组；差异逐笔记录 `ref`、内部金额、渠道金额与原因，(类型,日期) 唯一，重跑覆盖当日结果。

### Webhook 投递

- `GET /webhooks/{id}/deliveries`：`webhook.read`，分页投递日志。
- `POST /webhooks/{id}/deliveries/{event_id}/retry`：`webhook.create`，把原事件重新入队并走同一签名与校验路径。
- 创建订阅时即执行 SSRF 校验：仅接受公网 HTTPS 目标，回环、私网、链路本地、云元数据与集群内域名一律拒绝；投递前再次校验以覆盖 DNS 重绑定。

### 管理员与双因素

- `GET /admin-users`、`PUT /admin-users/{id}`、`DELETE /admin-users/{id}`、`POST /admin-users/{id}/unlock`、`POST /admin-users/{id}/reset-password`：`admin_user.read/update/delete/reset_password`。普通资料更新或提交相同 `role_id` 不撤销会话；实际改角色、改密、重置与删除会递增 `auth_version`，既有会话立即失效。账号更新响应的 `sessions_revoked` 表明是否已撤销旧会话，实际换角色时为 `true`。系统始终保留至少一个有效客户管理员；禁止停用、删除或实际修改当前登录账号的角色，相同角色的保存仍允许。
- `POST /admin-users/{id}/mfa`：`admin_user.update`。`action=enrol` 生成密钥并返回 `otpauth_uri`（label 为目标账号）与当前验证码，此时密钥尚未生效；`action=confirm` 校验验证码后才启用；`action=disable` 关闭。禁止对本人账号操作。
- `POST /auth/mfa`：口令校验通过但账号启用双因素时，`/auth/login` 只返回 `mfa_required` 与 5 分钟有效的 `mfa_challenge`，不下发令牌；`/auth/mfa` 凭正确验证码才签发会话，错误验证码计入失败锁定。

### 首页统计与趋势（2026-10-01）

`GET /dashboard` 需要 `dashboard.read`。首页在一个卡片内显示 9 项统计，按三行排列：充电中订单、本日订单、充电金额；总用户数、新用户数、本日充电用户数；站点数、设备数、待处理告警数。近 7 天的每日已结束订单数及结算金额使用柱状图展示；刷新按钮之后仅显示更新时间。

字段和日期口径见 [仪表盘契约](go-admin-auth.md#仪表盘统计字段2026-10-01)。所有统计只读，查询失败返回错误，不将缺失指标显示为 0。显示为“充电金额”的 `today_settled_cents` 是本日已结束订单的实结金额，尚未扣减后续退款。

### 导出

- `GET /exports`、`GET /exports/{id}`、`GET /exports/resources`、`POST /exports`、`GET /exports/{id}/download`：`export.create` 写、`finance.read` 读。详情只返回任务状态与统计，不暴露服务器文件路径；创建需 UUID `request_id`、已授权的 `resource`（`orders`/`stations`/`devices`/`settlements`/`bills`/`reconciles`）与可选 `format`（`csv`/`xlsx`/`pdf`，默认 CSV）。PDF 只支持 `bills`/`reconciles` 汇总；这两个资源还需 `filter: {"from":"YYYY-MM-DD","to":"YYYY-MM-DD"}`，日期范围最多 31 天。无法按站点限定的财务资源只允许全局数据范围账号导出。下载时重新校验归属、资源权限和财务数据范围；文件 24 小时过期，过期请求返回 410，central 启动及每小时清理一次过期文件。相同 `request_id` 只会返回同一创建者、资源、格式和筛选条件对应的已有任务。Compose 把导出文件存入 `export-files` 卷，避免 central 容器替换时丢失未过期文件。

### 分账模板

- `GET /settings/split-templates`、`GET /settings/split-templates/{id}`、`GET /settings/split-templates/{id}/parties` 需要 `finance.read`。列表支持 `page`、`page_size`、`status`、`keyword`。
- `POST /settings/split-templates` 需要 `finance.split_template.create`，请求含 `code`、`name`、`mode`（`mode_a` 或 `mode_b`）与完整 `parties` 数组；`PUT /settings/split-templates/{id}` 可改名称、模式与状态。参与方须为 2–8 个、代码不重复、各比例大于零且合计恰好 10000 基点。创建、修改和审计在同一事务完成。
- `POST /settings/split-templates/{id}/parties` 需要 `finance.split_party.create`，以完整 `parties` 数组原子替换参与方；模板一旦绑定任一站点，模式、状态和参与方不可修改，应新建模板再在站点停用后切换。读取参与方时银行账户只返回末四位。
- `PUT /stations/{id}/split-template` 同时需要 `station.update` 与 `finance.split_template.create`，请求 `{template_id,expected_template_id}`，未绑定时预期 ID 为 0。站点须已停用；有设备时还需停用满五分钟，且这些设备从未产生支付意图或充电订单。已产生历史交易的站点暂不能改绑，避免延迟结算使用新模板重分账。新建站点也可在创建请求中提供有效的 `split_template_id`。绑定仅接受启用且比例有效的模板，并写审计。站点重新启用仍走原站点更新接口。

### 风控配置

- `PUT /risk-config`：`alert.risk_config.update`，按 key 写入 JSON 值。

## 用户侧新增接口

- `GET /user/coupons`：当前账号可用优惠券。
- `POST /user/scan/start` 的 `coupon_grant_id` 为可选字段；优惠额在支付意图中冻结，响应新增 `discount_cents` 与 `payable_cents`。重放意图时必须提交相同优惠券，否则 409。
- `GET /user/debts`、`POST /user/debts/{id}/pay`：欠费查询与补缴，金额取自服务端欠费记录，不接受客户端指定。

## 用户侧充电查询接口（2026-09-29）

小程序此前已在调用以下接口而后端未提供，现已补齐。认证使用用户 JWT，其余约定与后台一致。路径省略 `/api/v1`。

- `GET /user/charge/ongoing`：当前进行中订单；无进行中订单时返回空 `order_no` 而非 404。
- `GET /user/charge/ongoing/snapshot?order_id=`：订单快照，含电量、秒数与费用分项；gateway 端口状态为附加信息，取不到不影响快照本身。
- `GET /user/charge/ongoing/curve?order_id=&window=`：实时曲线。`window` 取 `last_30min`（默认）、`last_2h`、`last_24h`，非法值回落默认值而非报错。`series` 按时间升序，每点含 `power_w`、`current_a`、`voltage_v`、`temperature_c`、`battery_soc`、`meter_kwh`；**设备未上报的指标为 `null`**，调用方应断开折线而不是画到零。gateway 侧强制 24 小时与 2000 点上限并按秒聚合。
- `GET /user/charge/history?page=&page_size=&status=`：本人订单分页，费用取自计费回执的分项。
- `GET /user/charge/{order_no}`：单笔详情，含分项费用、欠费、支付摘要与退款列表（无退款时为空数组而非 null）。

以上接口一律先校验所有权：他���订单与不存在订单返回同样的 404。

## 用户账户接口（2026-09-29）

小程序此前已在调用而后端未提供的账户类接口，现已补齐。认证使用用户 JWT，路径省略 `/api/v1`。所有接口先校验会话，未登录返回 401。

### 钱包

- `GET /user/wallet/balance`：`balance_cents` / `frozen_cents` / `available_cents`；无钱包时返回零值而非 404。
- `GET /user/wallet/txns?direction=`：`in` / `out` 分别筛选收入与支出，含变动后余额。
- `GET /user/wallet/recharges`、`POST /user/wallet/recharge`：充值记录与发起充值。金额限 1–50000 元；`request_id` 为 UUID 主键，重复提交返回同一 `payment_order_id` 并标记 `replayed`，同一请求号改金额则 409。渠道商户单号统一使用 `PAYW` 前缀，与微信及本地模拟器一致。
- `GET /user/wallet/refunds`、`POST /user/wallet/refund`：钱包退款申请。提交即**冻结**等额余额（同一笔钱不能同时用于消费），实际出款须经风控审核；返回 `review_status`。审核意见来自 `wallet_risk_review`，无审核时为 `null`。

### 优惠券与公告

- `GET /user/coupon/my?only_usable=`：本人优惠券，含 `usable`（未使用且未过期）。`usable` 为 false 的券仍会返回，便于页面展示"已过期"态。
- `GET /user/announcement/list`：已发布且在有效期内的公告，仅返回全局公告。站点/城市范围公告需要定位信息，留待小程序适配时补充。

### 站点

- `GET /user/station/nearest?longitude=&latitude=`：附近站点，距离用球面公式在 SQL 内计算后再分页，50 公里外过滤。只返回 `active` 且未删除的站点。
- `GET /user/station/{id}`：站点详情；停用站点按不存在处理。

### 手机号

- `POST /user/phone/bind`：绑定手机号。号码以 AES-256-GCM 加密存储（`phone_enc`），另存 SHA-256 `phone_hash` 做唯一性约束；返回掩码号码。**未配置 `PHONE_ENCRYPTION_KEY` 时拒绝绑定**，不会退化为明文存储。
- `POST /user/phone/unbind`：解绑。

平台暂未接入短信验证码通道，因此绑定接口不校验验证码。在没有真实短信能力前伪造验证会让任何人都能绑定他人号码，故此处留待后续补充。

### 报修

- `POST /user/device/report-fault`：提交报修，同时写入首条处理记录。
- `GET /user/device/fault-reports`：本人报修列表。
- `GET /user/device/fault-reports/{id}/history`：处理进度，仅返回对用户可见的节点（`user_visible=1`），内部派单信息不外泄。他人报修返回 404。

### 发票

- `POST /user/invoice/apply`：为本人已完成订单申请发票。金额取自计费回执（`charge_fee_receipt`），不取订单行上计费任务从未写入的列。同一订单只能申请一次（换 UUID 也拒），同一 UUID 幂等。`invoice_type` 取 `normal` / `vat_special`。
- `GET /user/invoice/my`：本人发票申请列表，含审核状态与拒绝原因。
