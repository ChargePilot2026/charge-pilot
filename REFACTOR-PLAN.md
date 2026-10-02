# charge-pilot 后端重构方案（基于全量代码审读定稿）

> 证据基线：`internal/` 179 个生产 Go 文件 / 32359 行，150 个测试文件 / 17432 行（含 cmd 2 个），合计 329 文件 / 49791 行；
> `migrations/{central_db,gateway_db,worker_db}/0001_init.sql` 逐行读完（85 / 15 / 5 张业务表）；
> 193 条路由（去重路由字面量，internal/）、64 条权限码全量提取；行数用 `wc -l` 复核（含空行，2026-10-02 逐行重验）。

本文件是**提案**，不是已生效的契约。采纳后应把结论分别并入 README「架构与目录」与 AGENTS.md，然后删除本文件——AGENTS.md 明确"不新增重复文档树"。

---

## 第一部分：术语表（四列对照）

规则：一个概念全仓一个名字；每个目录名必须钉在下面某一列上；引用某列的名字之前，先验证那个名字本身是对的。

### 1.1 两类用户（用户自己给出的概念）

| 概念 | 表名 | 路由 | 权限码 | 既有包名 |
|---|---|---|---|---|
| C 端充电用户 | `user`、`user_login_identity` | `/api/v1/user/*`；后台 `/api/v1/admin/charge-users` | `charge_user.read` | `central/identity` |
| 后台管理用户 | **`admin_user_role`** | `/api/v1/admin/admin-users` | `admin_user.create/delete/read/reset_password/update` | `central/admin` |

判定：

- **C 端：`user` 正确，`charge_user` 是漂移名。** `user` 出现在 4 处且互相印证——表名 `user`、JWT `kind=user`（`platform/auth/jwt.go:85,91`）、Redis 会话前缀 `user:session:`、表 `user_login_identity`。`charge_user` 只出现在 2 处（后台路由段 + 1 条权限码），且它的表根本不叫 `charge_user`。**结论：发布前把 `charge_user.read` → `user.read`，`/api/v1/admin/charge-users` → `/api/v1/admin/users`。**
- **后台：`admin_user` 正确，`admin_user_role` 是错名。** 逐列看，`admin_user` 赢了 3 列（路由、权限码、审计 `target_type`）；输的那一列是表名。而这张表**不是关联表**——它有 `username` / `password_hash` / `mfa_secret` / `status` / `failed_login_count` / `locked_until` / `auth_version`，是账号主表；真正的关联表是 `role_permission(role_id, permission_id)`（表内另内联 `role_id` 列，即一个账号一个角色，改名后该语义须在新表注释里写明）。**结论：表 `admin_user_role` → `admin_user`。**（这是我此前引用 `charge_user.read` 被你纠正的那类错误，方向相反但同源。）

### 1.2 三个"库"不是库

`central_db/0001_init.sql` 里的 `-- user_db 领域表`(:8) / `-- admin_db 领域表`(:899) / `-- billing_db 领域表`(:1551) **只是同一个文件里的三段注释**。证据：

- `dbconn.DSN` (`platform/dbconn/mysql.go:26-29`) 强制一个连接池只能锁一个 schema。
- `cmd/central/main.go:55` — `adminORM, billingORM := userORM, userORM`。
- `cmd/worker/main.go:61-62` — `databases["user"], databases["admin"] = databases["central"], databases["central"]`。
- `cmd/devseed/main.go:151` — `return db, db, err`。

三处独立代码各自把这个假边界重新造了一遍。**`central_db` 是一个库；`user_db`/`admin_db`/`billing_db` 三个词在系统里没有任何代码、配置或进程边界承认。** 目录不能按它们命名。

### 1.3 全量概念对照表

| 概念 | 表名（权威） | 路由 | 权限码 | 既有包名 | 归属进程 |
|---|---|---|---|---|---|
| C 端用户 | `user`,`user_login_identity` | `/api/v1/user/*` | `charge_user.read` | `central/identity` | central |
| 后台账号 | `admin_user_role` | `/api/v1/admin/admin-users` | `admin_user.*`(5) | `central/admin` | central |
| 角色权限 | `role`,`role_permission`,`permission`,`admin_data_scope`,`admin_field_mask` | `/api/v1/admin/roles`,`/permissions` | `role.*`(3) | `central/admin` | central |
| 站点 | `station` | `/api/v1/admin/stations` | `station.*`(4) | `central/admin` | central |
| 设备资料 | `device_meta`,`device_import*` | `/api/v1/admin/devices`,`/device-imports` | `device.read/operate/import` | `central/admin` | central |
| 厂商 | `vendor` | `/api/v1/admin/vendors`,`/vendor-options` | `vendor.*`(3) | `central/admin` | central |
| 充电订单 | `charge_order`,`charge_order_pricing`,`charge_billing_cutoff`,`charge_start_receipt`,`charge_end_receipt`,`charge_event_log` | `/api/v1/user/orders`,`/api/v1/admin/orders` | `order.read` | `central/charge` | central |
| 计量复核 | `charge_meter_review` | `/api/v1/admin/billing/meter-reviews` | `finance.read` | `central/charge`+`central/admin` | central |
| 计费明细 | `charge_bill`,`charge_bill_read`,`charge_fee_receipt`,`charge_manual_settlement` | `/api/v1/admin/billing/*` | `finance.read` | `central/charge`+`central/admin` | central |
| 费率 | `pricing_rule`,`pricing_template`,`pricing_publication` | `/api/v1/admin/settings/charging-schemes` | `pricing.*`(3) | `central/pricing`+`central/admin` | central |
| 支付订单 | `payment_order`,`charge_prepay`,`charge_payment_intent`,`payment_callback_idempotent` | `/api/v1/admin/payment-orders` | `finance.read` | `central/charge` | central |
| 退款 | `refund_record`,`refund_review`,`refund_rejection`,`refund_success_receipt`,`manual_refund_request` | `/api/v1/admin/billing/*` | `order.refund.create/review`,`finance.refund.*` | `central/charge`+`central/admin`+**`worker/outbox`** | 跨进程 |
| 钱包 | `wallet_account`,`wallet_txn`,`wallet_recharge_request`,`wallet_refund_*`,`wallet_risk_*`,`risk_freeze_log` | `/api/v1/user/wallet/*` | `finance.wallet_risk.*` | `central/charge`+`central/admin` | central |
| 在线卡 | `online_card`,`online_card_audit`,`card_operation`,`card_charge` | `/api/v1/admin/online-cards` | `online_card.manage` | `central/charge`+`worker/charge` | 跨进程 |
| 优惠券 | `coupon`,`coupon_activity_rule`,`coupon_grant`,`coupon_grant_request`,`coupon_redemption` | `/api/v1/admin/coupons`,`/coupon-activities` | `coupon.*`(7) | `central/charge`+`central/admin` | central |
| 分账结算 | `split_template`,`split_party`,`settlement`,`settlement_party_amount`,`finance_reconcile_log` | `/api/v1/admin/billing/*` | `finance.split_*.create` | `central/billing`+`central/admin` | central |
| 提现 | `withdraw_request` | `/api/v1/admin/billing/*` | `finance.withdraw.*` | `central/admin` | central |
| 发票 | `invoice_request`,`invoice_admin_review` | `/api/v1/admin/billing/*` | `invoice.review` | `central/admin`+`central/charge` | central |
| 告警 | `alert_event` | `/api/v1/admin/alerts` | `alert.read/ack` | **仅 `worker/alerts`** | 跨进程 |
| 故障 | `device_fault_report`,`device_fault_report_event` | `/api/v1/admin/device-fault-reports` | `fault.read/dispatch/resolve` | `central/admin` | central |
| 反馈 | `feedback` | `/api/v1/admin/feedback` | `feedback.read/reply` | `central/admin` | central |
| 公告 | `announcement` | `/api/v1/admin/announcements` | `announcement.*`(4) | `central/admin` | central |
| 审计 | `audit_log` | `/api/v1/admin/audit-logs` | `audit.read` | `central/admin` | central |
| Webhook | `webhook_subscription`,`webhook_delivery_log` | `/api/v1/admin/webhooks` | `webhook.*`(4) | `central/admin`+**`worker/webhook`** | 跨进程 |
| 白标 | `whitelabel_config` | `/api/v1/admin/whitelabel` | `whitelabel.read/update` | `central/admin` | central |
| 导出 | `export_task` | `/api/v1/admin/exports` | `export.create` | `central/admin` | central |
| 监管 | `regulatory_report` | `/api/v1/internal/regulatory/events` | — | `internal/regulatory`（顶层，cmd 双端导入） | 跨进程 |
| 事件流 | `event_outbox`,`admin_event_outbox` | — | — | `worker/outbox` | 跨进程 |
| 补偿台账 | `comp_tx_log`,`dlq_log`,`dlq_replay_cursor` | `/api/v1/internal/worker/ops/*` | — | `worker/outbox`,`worker/internaljob` | worker |
| 调度 | `scheduled_task`,`task_execution_log` | `/api/v1/internal/scheduled-tasks/*` | — | `worker/schedule` | worker |
| 欠费（死） | `charge_debt`,`charge_debt_receipt`,`charge_debt_payment_request` | `/api/v1/user/debts` | — | `central/charge` | central |
| 端口锁（孤儿） | `charge_port_lock` | — | — | **无任何代码** | — |
| 设备（运行态） | `device`,`device_port`,`device_event`,`device_session`,`device_provision`,`telemetry*`,`charge_command`,`charge_process`,`charge_stop_command`,`charge_end_delivery`,`card_event_delivery` | gateway `/api/v1/internal/*` | — | `gateway/store`,`gateway/control` | gateway |

### 1.4 三套模块分类法互不吻合（不要试图统一，要记录）

| 分类法 | 取值数 | 问题 |
|---|---|---|
| schema 段落注释 | 3（`user_db`/`admin_db`/`billing_db`） | 无代码承认，见 1.2 |
| `permission.module` 列 | 20 | `fault.read`/`fault.dispatch` 在 `inspection` 下，`fault.resolve` 在 `fault` 下；`feedback.*` 在 `device` 下；`order.refund.*` 在 `finance` 下；`charge_user.read` 自成一个 `charge_user` 模块 |
| 代码包结构 | 6（`admin`/`billing`/`charge`/`identity`/`payment`/`pricing`） | 与前两者都无对应关系 |

**结论：不要新建"统一模块"层。** 权限码是唯一覆盖全部 64 条的既有分类法，但它本身有 4 处已知错误——在动目录之前先把这 4 处修掉，目录才有可对齐的锚点。

---

## 第二部分：概念边界结论

### 2.1 守住的边界（照抄，不要动）

1. **worker 从不写 `charge_order.status`。** 订单状态推进全部由 central 的 `start_result.go` / `end_result.go` 完成。`worker/charge` 只写 `charge_billing_cutoff`（截止点）和 `device_port`（端口释放）。
2. **gateway 的 frame→domain 边界只有一个入口。** `protocol.Event` 是唯一出口，保留 `RawPayload` 供重放；`dc589` 不 import 任何 DB/HTTP 包。
3. **每条命令独立 6 字节 session 落库**，回执查询条件互不重叠（连接会话 ≠ 命令会话）。
4. **先落库后下发 + 条件更新**；物理上报 `reported_status` 与业务预约 `status` 分列两存。
5. **幂等同事务**：编号分配、业务写入、幂等凭据、审计、Outbox 同事务提交。
6. **计量证据校验是单一权威**：`pricing.usageFromMeter` (`pricing/actual.go:41-89`)，消费封顶与最终结算共用。
7. **实时费用是只读估算**：`charge/live_meter.go` 会为缺口插值补 `PowerW`（首段 ≤30s、其余 ≤180s），但该函数**从不写任何结算字段**（`live_meter.go:39-40` 注释 + 无写操作），且 `meter.ReviewRequired` 会在 `usageFromMeter:42` 触发 `ErrMeterReview`，最终由 `live_meter.go:91-94` 转成 `FeeUnavailable` 字符串。**这条性质必须原样保住：显示估算与结算证据的分离是设计，不是 bug。**

### 2.2 破掉的边界（按危害排序）

#### P0-1　worker 绕过 HTTP 直写两个进程的资金与状态表

`cmd/worker/main.go:45` 同时打开 `gateway_db` / `central_db` / `worker_db` 三个池，16 处直写：

| 目标表 | 位置 | 危害 |
|---|---|---|
| `central_db.refund_record` / `refund_success_receipt` / `payment_order` | `outbox/result_consumer.go:245,256,265` | **真实资金入账在 worker 进程内完成** |
| `central_db.charge_billing_cutoff`（含 `electric_cents`/`service_cents`） | `charge/auto_stop.go:149`,`card_deadline.go:33` | **金额由 worker 产生、central 消费** |
| `central_db.alert_event` | `alerts/device.go:131,175` | 跨库事务，无原子性 |
| `central_db.webhook_delivery_log` | `webhook/deliver.go:325` | 配置在 admin、投递在 worker |
| `central_db.regulatory_report` | `regulatory/delivery.go` | 入队 API 在 central、投递在 worker |
| `central_db.event_outbox` / `admin_event_outbox` | `outbox/publisher.go:45-70` | 事件发布跨进程 |
| `gateway_db.device_port`（端口释放） | `charge/end_sync.go:112` | 端口占用是订单正确性的前提 |
| `gateway_db.device_event.processed_at` | `end_sync.go:125`,`alerts/device.go:74` | |
| `gateway_db.charge_command.result_reported` | `charge/sync.go:101` | |
| `gateway_db.charge_end_delivery` / `card_event_delivery` | `meter_segments.go:63`,`cards.go:72,77,114` | |

> 2026-10-02 修订：上表 16 处直写已全部消除（第 5 批①–⑤，见第七部分）。worker 对 central_db / gateway_db 的剩余访问仅为 outbox Publisher 直读三库 `event_outbox` / `admin_event_outbox`（计划认可的形态）；业务数据访问一律走 HTTP。

最严重的一条：`outbox/result_consumer.go:12` **直接 `import "internal/central/charge"`**，为了用 `charge.SettledPaymentStatus` 和 `charge.SyncOrderPaymentStatus`，然后在 `:265-269` 用它们改 `central_db.payment_order`。worker 编译期依赖 central 的存储层类型，事务边界彻底消失。

同一个文件里两种通信模式并存：`charge.Synchronizer` / `EndSynchronizer` / `CardDispatcher` / `billing.Dispatcher` / `refund.Dispatcher` 走 HTTP；`AutoStopper` / `ResultConsumer` / `alerts.DeviceSynchronizer` / `WebhookDeliverer` / `regulatory.Deliverer` 直连。

#### P0-2　13 条业务不变量在 worker 里被重写了一遍

| 不变量 | worker 副本 | central 权威 |
|---|---|---|
| 哪些订单待启动 | `charge/paid_start.go:37-40` | `charge/authorization.go:44-47`、`charge/start_result.go:152-153`（**共 3 份，跨 2 进程**） |
| 故障码语义（`0xBB`=烟雾、`0xAA`=高温、`Port==0xFF`=设备级） | `alerts/device.go:89-100` | 无——设备协议语义被写进 worker |
| 什么算恢复（`PortStates[i]<=2`） | `alerts/device.go:184-197` | 无 |
| 同一故障只保留一条告警 | `alerts/device.go:120-127` | 无 |
| 退款何时算到账 | `outbox/result_consumer.go:225-244` | `central/charge/refund.go` |
| 启动结果是否可信（`ResultCode>3` 需复核） | `charge/sync.go:79-88` | `central/charge/start_result.go` |
| 何时停机 + 停机冻结多少费用 | `charge/auto_stop.go:93-141` | `central/charge/billing.go` |
| 刷卡拒绝 vs 服务故障（409 + `insufficient_balance`） | `charge/cards.go:203-212` | central 的错误信封契约被重新解析 |
| 领域 → outbox 表映射 | `outbox/publisher.go:39-41` 裸字符串 `if Source=="admin"` | |
| 分区月键 | `outbox/result_consumer.go:158,174,201`、`dlq.go:157` 用 `time.Now().UTC()` 重算当月 | central 另有实现 |

#### P0-3　`central/charge` 与 `central/admin` 共享表所有权

`central/charge` 36 个生产文件 / 7140 行，一个 receiver 7 个领域（`charge/user_account.go` 1214 行）。`central/admin` 40 个文件 / 9897 行，**直接命名** `withdraw_request`、`invoice_admin_review`、`refund_review`、`refund_rejection`、`manual_refund_request`、`finance_reconcile_log`、`split_template`、`split_party`、`settlement`、`settlement_party_amount`、`fee_receipt`、`manual_fee_review`、`charge_bill`。

**同一个 `charge_order` 生命周期，admin 和 charge 各写一遍。** 真正需要拆分的不是"用户侧 vs 后台侧"（那条边界已经存在且干净），而是**表所有权**。

#### P1-1　服务令牌校验逐字复制 16+ 份

`sha256 + subtle.ConstantTimeCompare` 同一段逻辑：worker 3 份（`schedule/http.go:26-37`、`internaljob/ops.go:32-43`、`regulatory/http.go:26-37`）+ gateway 9 份 + central/charge 7 份（其中 5 份字节级相同）+ central/billing 1 份（`service.go:105`），合计 20 份。`internal/platform` 里**没有**共享助手。

#### P1-2　分页解析 5 套，边界不一致

admin 4 套 + charge 3 套（含 1 个共享）。`page ≤ 100000` vs `≤ 1000000` vs `charge/debt_http.go` 把 page 也限到 100。约 20 处手写 `Count + Offset/Limit`。

#### P1-3　幂等机制 4 套并行

`LAST_INSERT_ID()`（全仓 17 处，charge 包内 7 处）、GORM `Create` 回填、两套重复键判定（其中一套降级到字符串匹配）、请求号重放比对。

#### P1-4　`httpapi` 错误码 1005 一码三义

`httpapi.BadRequest`(400)、`Write(..., http.StatusConflict, 1005, ...)`(409)、`NoMethod`(405) 都用 1005。`httpapi` 里一个错误码常量都没有，全部是裸整数。`FieldErr` / `Envelope.Errors` 结构在仓库里**从未被写入过**——字段级校验错误未接线。

#### P1-5　`charge/live_meter` 与 `charge/query` 之外还有 `identity/http.go:119-150` 复写鉴权

`profile` handler 完整重写了 `SessionAuthenticator.Authenticate` 已提供的鉴权链，鉴权规则被复制两份。

### 2.3 已确认的 bug（不是设计问题）

| # | 位置 | 问题 |
|---|---|---|
| B1 | `worker/outbox/dlq.go:84-97` | handler 失败时**先 `XAck` 再判断重试次数**，注释写"重试预算耗尽前保留 pending 状态"但 pending 已被清掉 → 消息永不重试。对照 `ResultConsumer.Handle:147` 是失败不 ack，**两个消费者语义相反** |
| B2 | `worker/outbox/dlq.go:131-133` | `attempts()` 每次失败 `XPendingExt` 只拉前 100 条再线性找一条 → 积压 >100 时 `RetryCount` 恒为 0，死信永不触发 |
| B3 | `worker/outbox/dlq.go` + `cmd/worker/main.go:114,115` | `DLQ.ConsumeBatch` **生产零调用**（仅测试）；`OpsAPI.Replay` **从未赋值** → `POST /api/v1/internal/worker/ops/dlq/:stream/replay` 恒返回 503；`DLQ.Streams` 未设 → `GET .../ops/streams` 恒返回 `{}` |
| B4 | `worker/webhook/deliver.go:91,112` | 用 `XRANGE - +`（从头扫）而非 `>`，成功后只 `XACK` 不 `XDEL` → 共享流单调增长，同一事件反复扫描、反复对外 POST |
| B5 | `cmd/worker/main.go:99-106` | 所有 worker HTTP 组件的 `Client` 字段**全部未赋值** → 每次请求新建 `&http.Client{}`（`auto_stop.go:270`、`cards.go:196`、`sync.go:66`…）→ **连接池完全不生效**。且超时散落 6 处：30s(`internaljob`)/10s(`webhook`)/5s(其余) |
| B6 | `worker/outbox/result_consumer.go:158-161` | `isCommitted` 固定查"当月"分区 + 重复的 `Where("tx_id = ?")` → **跨月重投的退款结果会被当成未提交而重复入账** |
| B7 | `worker/alerts/device.go:57` vs `:111` | gateway 事务内开 central 独立事务，无原子性。central 提交成功 + gateway 回滚 → 下次重复创建告警；反之告警丢失且 `processed_at` 已推进 |
| B8 | `worker/charge/auto_stop.go:165` | `spendCapReached` **零调用方** → `pricing.StopAtMeter` 在生产不可达（现役走 `BudgetStopAtMeter`） |
| B9 | `central/admin/pricing_rules.go:12` | `registerPricing` 定义了 3 条路由，`resources.go:33` 从未调用 |
| B10 | `central/charge/debt.go:53,229,285` | `RecordDebt` / `SettleDebt` / `RemindDebt` **零调用方** → `charge_debt` 永远不会有行，但 `GET /api/v1/user/debts` 和 `POST .../pay` 是活路由（前端能打开一个永远空的页面） |
| B11 | `internal/finance/checkout.go`、`pricing.go` | `Fund` / `PriceEnergy` / `EnergyUnit` / `ChargeFee` / `ErrInvalidTariff` 零生产调用方（仅 `finance_test.go`）。3 个文件 197 行里只有 `Allocate` 活着，且唯一调用方是 `central/billing/settlement.go:11` |
| B12 | `central_db.charge_port_lock` | 生产 Go 零引用，但 dev 库**现存 7 行数据**（`0000000088100001-1` 等）—— 已被 `SELECT ... FOR UPDATE` 取代的旧锁机制残留 |
| B13 | `internal/platform/config/config.go` | `Gateway.DatabaseURL` / `Worker.DatabaseURL` 读 `DATABASE_URL`，`Central.DatabaseURL` 读 `DATABASE_URL_CENTRAL`；而 `cmd/migrate` 读 `DATABASE_URL_WORKER` → **同一张 `worker_db` 有两个环境变量名** |
| B14 | `internal/worker/webhook/deliver.go:1` | `package worker`，目录名 ≠ 包名 → `cmd/worker/main.go:25` 必须起别名 `webhookdelivery` |
| B15 | `internal/gateway/protocol` | 被 `central/charge`(2)、`central/pricing`(1)、`worker/*`(5) 跨进程 import，但物理位置在 gateway 进程目录树内 |
| B16 | `migrations/admin_db`、`migrations/billing_db`、`migrations/user_db` | 三个**空目录**，schema 合并提交 `a95a1ff` 的残留 |
| B17 | `platform/httpapi/gin.go:69` vs `:42` | `BadRequest`(400) 与 `NoMethod`(405) 共用 code 1005；`schedule/http.go:108` 的 409 也用 1005 |
| B18 | `netguard/guard.go:83-84` | 显式放行 `198.18.0.0/15`（基准测试网段），注释自承"为兼容开发环境 DNS 映射" → 出站 SSRF 防护有一个已知开口。已定（D3，2026-10-02）：生产不依赖该 DNS 映射，**删除放行条**；若开发环境受影响，再以配置项方式仅 dev profile 放行 |
| B19 | `worker/charge/auto_stop.go:149`、`card_deadline.go:33`、`meter_segments.go:63` | 三处 `OnConflict DoUpdates` 为自赋值空更新，插入冲突被静默吞掉；关键写入（停机冻结/结束回执冻结）须改为查回或报错重试 |

### 2.4 我要收回的此前判断

1. **"identity 需要拆分"——作废。** C 端在 `identity`（表 `user`/`user_login_identity`），后台在 `admin`（表 `admin_user_role`），两包无交叉 import，Redis 命名空间 `user:session:` / `admin:session:` 独立，JWT `kind` 闭枚举隔离。这条边界已经存在且干净。
2. **"模拟器应与生产 codec 解耦"——作废。** `simulator/dc589/simulator.go:16`、`behavior.go:15` 别名复用 `wire`，并用硬编码厂商样例字节做 golden 断言。这是更优取舍。
3. **"按模块发 schema 限定句柄"——不成立。** `dbconn.DSN:26-29` 强制单 schema，跨 schema 只能全限定表名或另开连接池。
4. **`fee_*` / `manual_fee_review` 在 `admin_db` 段——我此前说错了。** 它们在 `billing_db` 段（`0001_init.sql:1551` 之后）。`billing_db` 段共 7 张表：`fee_calculation`、`fee_delivery`、`fee_receipt`、`manual_fee_review`、`settlement`、`settlement_party_amount`、`withdraw_request`。
5. **"`charge_debt` 整条链路是死代码"——收窄。** 写侧（`RecordDebt`/`SettleDebt`/`RemindDebt`）零调用，但读侧 `/api/v1/user/debts` 是活路由。准确表述是"欠费功能是个永远为空的读壳"。

### 2.5 不推荐大爆炸重写

`AGENTS.md` 里的不变量（幂等同事务、二签核对实时权限、禁止零值伪装、`1 Wh = 1000 mWh`、北京时间业务日界）是这个仓库最值钱的资产，**全仓 155 个测试文件 / 17377 行都建立在这些不变量上**。gateway 的分层模式（`protocol.Event` 边界、命令 session 隔离、先落库后下发、物理/业务状态分列）质量明显高于 central，应当**照抄**而不是重设计。

---

## 第三部分：目录结构定稿

### 3.1 铁律（违反即打回）

1. **目录名 = 二进制名 = Compose 服务名 = 环境变量前缀。** 例：`cmd/worker` ↔ `worker` ↔ `DATABASE_URL`。
2. **进程名沿用 `central` / `gateway` / `worker`。** 概念模型（谁写谁、谁调谁）只进文档，不进目录名。不用 `control-plane` / `data-plane` / `async-plane` / `console` / `backoffice`。
3. **层名必须来自系统里已存在的名字**（路由段 / 权限码 / 表名 / 既有包名），且引用前先验证那个名字本身是对的。
4. **表归属决定包归属。** 事务边界跟着表走，不跟着"用户侧 / 后台侧"走。
5. **一个进程只写自己的库。** 跨进程一律 HTTP + `X-Service-Token`。

### 3.2 顶层结构

```
cmd/
  central/            二进制 central  :8080   DATABASE_URL_CENTRAL
  gateway/            二进制 gateway  :9100   DATABASE_URL
  worker/             二进制 worker   :8085   DATABASE_URL + DATABASE_URL_CENTRAL + DATABASE_URL_GATEWAY
  migrate/  devseed/  simulator-dc589/        （无库或单库工具，不属于三进程）

internal/
  platform/           跨领域通用能力，无业务语义
    config/  dbconn/  httpapi/  serviceclient/  auth/  snowflake/  netguard/  phone/
  protocol/           ★ 从 gateway/protocol 提升为共享契约库
    dc589/            ★ 从 gateway/protocol/dc589 平移

  central/            业务实现，按表归属分包
  gateway/            设备接入
    store/  control/  provision/  simulator/
  worker/             调度、消费、重试、外部投递
  delivery/           ★ 外部投递（webhook + regulatory 合并；alerts 不进来，见下）
```

**三处结构性变更，都有现有名字支撑：**

- `internal/gateway/protocol` → `internal/protocol`。名字不新：目录已叫 `protocol`，类型已叫 `protocol.Adapter` / `protocol.Event`，AGENTS.md 已在用这两个名字。理由：它被 3 个进程 import（B15），放在 gateway 目录树内是错的。
- `internal/worker/webhook`（`package worker`）+ `internal/regulatory` → `internal/delivery/`。两者共同点是**向进程外投递**：订阅端点与监管端点的签名和 HTTP 发送。名字取自表名 `webhook_delivery_log` 已有的词 `delivery`（`worker/webhook/deliver.go:317` 也在用 `delivered_at`）。**已定稿（2026-10-02 审核修订）**：`worker/alerts` 不进 `delivery`——它的价值是设备协议语义翻译（故障码、恢复判定），随第 5 批 `alert_event` 写入改调 central HTTP 后整体并入 `protocol/dc589`，不独立成包。
- `internal/finance` **不升为领域包**。3 个文件 197 行，2/3 死；`Allocate` 唯一调用方是 `central/billing/settlement.go:11`。把 `Allocate` 并进 `central/billing`，删掉 `checkout.go` / `pricing.go`。真正的计价领域已经是 `central/pricing`（15 文件 / 1949 行 / 零 DB）。

### 3.3 `internal/central/` 子包定稿

现有 6 个包的处置：

| 现有包 | 文件/行 | 处置 | 理由 |
|---|---|---|---|
| `pricing` | 15 / 1949 | **不动** | 纯计算、零 DB、被 charge/billing/worker 引用。已经是干净的领域 |
| `identity` | 6 / 642 | **不动** | C 端用户与会话，边界已干净 |
| `payment` | 3 / 220 | **不动** | 渠道适配器（`wechat.go`/`simulation.go`/`refund.go`），与 `gateway` 的协议适配器对称 |
| `billing` | 4 / 596 | 吸收 `internal/finance.Allocate` | 结算 + 编号 + 内部派发端点 |
| `charge` | 36 / 7140 | **拆分**（本次重构主体） | 神包；与 `admin` 共享 13 张表 |
| `admin` | 40 / 9897 | 瘦身为 HTTP 适配层 | 移走表写入后应显著缩小 |

`charge` 的拆分轴 = **表前缀家族**（`charge_*` 在 schema 里已经是事实命名空间）：

| 新包 | 承接表 | 来源文件 |
|---|---|---|
| `order` | `charge_order`、`charge_order_pricing`、`charge_billing_cutoff`、`charge_start_receipt`、`charge_end_receipt`、`charge_event_log`、`charge_port_lock` | `order_state.go`、`start_result.go`、`end_result.go`、`authorization.go` |
| `settlement` | `charge_bill`、`charge_bill_read`、`charge_fee_receipt`、`charge_manual_settlement`、`charge_meter_review` | `billing.go`、`meter_review.go` |
| `payment` | `payment_order`、`charge_prepay`、`charge_payment_intent`、`payment_callback_idempotent` | `payment_intent.go`、`payment_callback.go` |
| `refund` | `refund_record`、`refund_review`、`refund_rejection`、`refund_success_receipt`、`manual_refund_request` | `refund.go` |
| `wallet` | `wallet_*`、`risk_freeze_log` | `wallet*.go` |
| `card` | `online_card`、`online_card_audit`、`card_operation`、`card_charge` | `card*.go`、`online_card.go` |
| `coupon` | `coupon*` | `coupon*.go` |
| （不建 `account`） | `user`、`user_login_identity` 档案读写并回 `identity`；`snowflake_state` 随编号分配逻辑留原处或随 `billing/number.go` 归并 | `user_account.go`（1214 行，按表前缀家族拆散） |

**三处命名冲突已裁决（2026-10-02 审核修订）：**

1. **`payment` 已被 `central/payment`（渠道适配器）占用。** 支付**订单**与支付**渠道**必须分开。**定稿：渠道适配器改名 `channel`**——`payment/wechat.go` 本身就是渠道实现，`config` 里的 `PAYMENT_MODE=simulation` 也是"渠道"词，`channel` 有系统内既有词汇支撑；`payment` 让给支付订单。
2. **`refund` 与 `settlement` 的边界。定稿：业务退款（`refund_record` 等 5 张表）进 `refund`，渠道退款（`central/payment/refund.go`）随渠道适配器并入 `channel`。** 连锁约束：退款结算事务同时触碰 `refund_record`、`payment_order` 与钱包，**`refund` 与 `payment` 两个包允许双向引用（同一事务内），不强制单向依赖**。
3. **`account` vs `identity`。定稿：不建 `account`**——`charge/user_account.go` 的用户档案读写并回 `identity`（表 `user`/`user_login_identity` 本就归 identity），钱包/卡/券按表前缀家族各归新包。

### 3.4 `internal/worker/` 子包定稿

worker 的定位应该是**只做三件事**：消费事件流、调度定时任务、向进程外投递。**不做业务判定，不直写别人的库。**

| 现有包 | 文件/行 | 处置 |
|---|---|---|
| `outbox` | 3 / 676 | 保留 `Publisher`；`ResultConsumer` 的资金写入改为调 central HTTP |
| `schedule` | 2 / 327 | 不动（纯 worker_db，干净） |
| `internaljob` | 2 / 153 | 升为 worker 的唯一服务间 HTTP 出口（现在 6 处手写客户端应收敛到这里） |
| `charge` | 7 / 1108 | 拆：`Synchronizer`/`EndSynchronizer`/`CardDispatcher` 保留（走 HTTP）；`AutoStopper` 的停机判定与 `charge_billing_cutoff` 写入**移回 central**，worker 只发"该停了"的意图 |
| `billing` `refund` | 各 1 / 16 | 合并进 `internaljob`（只是路径字符串包装） |
| `webhook` `alerts` | 1+1 / 548 | `webhook` 移入 `internal/delivery`（与 `internal/regulatory` 合并）；`alerts` 不独立成包，随第 5 批并入 `protocol/dc589` |
| — | — | 新增 `dlq` 消费接线，修 B1/B2/B3 |

---

## 第四部分：执行顺序

分 6 批，每批独立可发布、可回滚。每批结束跑 `make check` + `scripts/test/integration.ps1`。

### 第 1 批：删死代码（无行为变更，零风险）

| 动作 | 位置 |
|---|---|
| 删 `spendCapReached` | `worker/charge/auto_stop.go:163-181` |
| 删 `registerPricing` | `central/admin/pricing_rules.go` 整个文件 |
| 删 `RecordDebt`/`SettleDebt`/`RemindDebt` + `/api/v1/user/debts*` 两条路由 + `charge_debt*` 三张表 | `charge/debt.go`、`charge/debt_http.go`、schema。已确认整删（D1，2026-10-02）：产品层面放弃欠费/追缴 |
| 删 `internal/finance/checkout.go`、`pricing.go`；`Allocate` 并入 `central/billing` | |
| 删空目录 `migrations/{admin_db,billing_db,user_db}` | |
| 删 `charge_port_lock`（dev 库 7 行数据已确认可弃，D2，2026-10-02） | schema |

验收：`make check` 绿；`GET /api/v1/user/debts` 返回 404（前端需同步隐藏入口）。

### 第 2 批：修 bug（不改结构）

B1、B2、B3、B5、B6、B7、B13、B17、B18。全部是局部修改，可逐个独立 PR。

另加 B19（2026-10-02 审核新增）：`worker/charge/auto_stop.go:149`、`card_deadline.go:33`、`meter_segments.go:63` 三处 `OnConflict DoUpdates` 均为 `gorm.Expr("charge_order_id")` 式自赋值空更新——插入冲突被静默吞掉，而停机冻结/结束回执冻结恰是关键写入。统一改为：先查回（幂等重试语义本就需要），冲突即返回错误进入重试，不做静默跳过。

### 第 3 批：命名归一（趁未发布，成本最低）

- 表 `admin_user_role` → `admin_user`；`charge_user.read` → `user.read`；`/api/v1/admin/charge-users` → `/api/v1/admin/users`。
- 修 `permission.module` 的 4 处错误，目标取值已定（2026-10-02）：`fault.resolve` → `fault`；`feedback.*` → `feedback`（新模块）；`order.refund.*` → `refund`（跟随 3.3 包名）；`charge_user.read` 改名后模块列即 `user`，原 `charge_user` 模块消失。
- `internal/worker/webhook` 的包名改回 `webhook`（B14）。
- 统一 `worker_db` 的环境变量名（B13），方向已定：worker 进程 `config.Worker.DatabaseURL` 从 `DATABASE_URL` 改为 `DATABASE_URL_WORKER`，与 `cmd/migrate` 的 `DATABASE_URL_<schema>` 通用规则及 `compose.dev.yaml:83` 对齐；同步更新 compose 注入与 `.env` 模板。
- 前端 + `internal/central/admin/openapi.json` 同步。

### 第 4 批：提升共享契约库

`internal/gateway/protocol` → `internal/protocol`，`dc589` 一并平移。纯移动，import 路径机械替换。**这一批做完，`central/charge` 和 `worker/*` 就不再编译期依赖 gateway 目录树。**

### 第 5 批：切断 worker 的跨库直写（主体工作量）

按 P0-1 逐表改造，每张表一个 PR。**前置 PR 0（2026-10-02 增补）**：gateway 内部证据端点扩展——停机判定移回 central 后需要设备心跳证据，而 `AutoStopper.Run` 现直读 `gateway_db.device_event`（`auto_stop.go:107-123`，每订单最多 10080 行），现有 `/charging-samples` 端点（`live_meter.go:49`）的单次调用形态不够用，须先支持批量/游标取证据：

1. `refund_record` / `refund_success_receipt` / `payment_order`（资金，最高优先）→ **已完成（2026-10-02，按证据修正）**：复核发现退款结算已在 central `RefundExecutor.apply` 行内完成、worker `ResultConsumer` 服务的是无生产者的 legacy schema，故直接删除该消费者与 comp_tx_log，而非新增 HTTP settle 端点。详见第七部分第 5 批①。
2. `charge_billing_cutoff`（含金额）→ **已完成（2026-10-02）**：停机判定整体移回 central（`central/charge/auto_stop.go` + `POST /api/v1/internal/charge-orders/auto-stop`），worker 只发扫描意图，证据经 PR 0 批量端点拉取。详见第七部分第 5 批②。
3. `alert_event` → **已完成（2026-10-02，按①②模式修正计划原文）**：计划原文为"central 新增告警写入端点、`worker/alerts` 整体并入 `protocol/dc589`"。实际连写入端点都不需要——同步整体移入 `central/admin`（alert_event 本地写、device_event 证据全走 gateway HTTP），`protocol/dc589` 只接收故障码语义（`FaultMetric`/`FaultRecovered`），`worker/alerts` 瘦身为单行触发器而非整体并入。恢复判定改为 after_key 水印语义。详见第七部分第 5 批③。
4. `webhook_delivery_log` / `regulatory_report` → **已完成（2026-10-02）**：`worker/webhook` 与 `internal/regulatory` 合并为顶层 `internal/delivery`（worker 只做签名与 HTTP 发送，零数据库句柄）；central 新增四个内部端点（订阅清单 / 投递日志批量 upsert / 监管租约领取 / 监管状态回执），监管入队与状态查询 API 也随 `internal/regulatory` 删除平移进 `central/admin`；顺手修复 B4（成功后 XDEL，不再反复外投）。详见第七部分第 5 批④。
5. `gateway_db` 的 6 处 → **已完成（2026-10-02）**：6 处全部改走 gateway HTTP，其中 4 处走既有端点，2 处（card_event 决策冻结、charge_end_delivery 冻结）能力缺失由新增 `worker_sync.go` 13 个内部端点补齐；worker 四个组件（Synchronizer / EndSynchronizer / CardDispatcher / outbox Publisher 之外）零 gateway 库句柄。详见第七部分第 5 批⑤。

配套：`alerts` 跨库双事务的补偿机制（B7）；锁序契约 `card_deadline.go:10-11` 变成代码可验证的约定。

### 第 6 批：拆 `central/charge` + 瘦 `central/admin`

按 3.3 的表前缀家族拆包。`admin` 移走表写入后只留 HTTP 适配 + 权限码校验 + 导出。

---

## 第五部分：本次未验证的边界

1. **未做实机验收。** 全部结论来自源码、schema 与 dev 容器（`central_db` / `gateway_db` / `worker_db` 当前实例）。模拟器与 `PAYMENT_MODE=simulation` 的结果不构成实机或真实资金验证。
2. **未跑集成测试。** `scripts/test/integration.ps1` 在 PowerShell 5.1 下会因 docker 写 stderr 触发 `NativeCommandError` 提前终止（非测试失败）；本次只做了等价的手工只读查询。`scripts/test/integration.ps1` 的 PS 5.1 兼容性仍未修（你尚未决定是否要修）。
3. **前端未纳入本次范围。** `charge-users` → `users` 改名会波及 `admin-web` 与 `miniprogram`，需在第 3 批一并处理。
4. **`198.18.0.0/15` 放行（B18）已决（D3）**：生产不依赖该 DNS 映射，删除放行条，转入第 2 批执行。
5. **仍未答复的三个问题**（前几轮遗留，与本次重构无关）：在线设备点开跳哪里；TabBar 是否该出现在扫码/充电中这类全屏页；`integration.ps1` 的 PS 5.1 兼容是否要修。

---

## 第六部分：决策记录（2026-10-02 全部落定）

原"待决策清单"三项均已由老杨师傅裁决，无遗留阻塞项：

| # | 事项 | 结论 |
|---|---|---|
| D1 | 欠费功能去留 | **整删**——两条路由 + 3 张表 + 前端入口全去掉，产品层面放弃欠费/追缴 |
| D2 | `charge_port_lock` 7 行 dev 数据 | **可弃**——直接删表，无需备份 |
| D3 | `198.18.0.0/15` 放行 | **生产不依赖**——删除 `netguard` 放行条（B18，入第 2 批）；若开发环境受影响，再以配置项方式仅 dev profile 放行 |

此前"需要你裁决"的命名与包结构事项（`channel`/`payment`、`refund` 边界、`identity` 归并、`delivery` 构成、B13 环境变量方向、B19 修复范围、第 5 批前置 PR 0）均已按审核推荐定稿，见 1.1、2.3、3.2、3.3、3.4 及第 2/3/5 批的修订标注。


---

## 第七部分：执行进度（2026-10-02 起）

### 第 1 批：删死代码 —— ✅ 已完成

| 动作 | 结果 |
|---|---|
| 删 `spendCapReached` | `worker/charge/auto_stop.go` 已删，`pricing.StopAtMeter` 仍保留（pricing 领域 API，有测试覆盖） |
| 删 `registerPricing` | `central/admin/pricing_rules.go` 整文件删除；openapi.json 中两条死路由（`settings/charge-rules` 及 `/{id}/disable`）同步移除 |
| 欠费功能整删（D1） | `charge/debt.go`、`charge/debt_http.go`、`cmd/central` 路由注册、`charge_debt`/`charge_debt_receipt`/`charge_debt_payment_request` 三表、`payment_order.biz_type` 枚举值；前端全仓无入口无需改动；`oneOfStatus`/`splitFields` 移至 `charge/status.go`；相关 4 个测试文件同步 |
| `internal/finance` 并入 `central/billing` | `allocation.go` 平移为 `billing/allocation.go`（类型去 `finance.` 前缀），测试平移为 `billing/allocation_test.go`；`checkout.go`/`pricing.go`/`finance_test.go` 删除；`settlement.go` 改本地类型 |
| 删空迁移目录 | `migrations/{admin_db,billing_db,user_db}` 已删 |
| 删 `charge_port_lock`（D2） | 表与 DROP 语句删除；**修正计划 B12 的判断**：该表并非零引用——`port_lock.go` 的 `lockCheckoutPort` 被扫码支付与在线卡两条活路径调用。已删除锁函数，端口抢占并发安全改由既有唯一键兜底（`charge_payment_intent.uk_active_port` 生成列 + `card_charge.active_port`），刷卡路径唯一键冲突显式映射 `ErrCardOperation`；`checkoutPortAvailable` 保留于 `charge/checkout_port.go` |

验收：`make fmt && make lint && make test` 全绿；`scripts/test/integration.sh` 隔离集成测试全绿（唯一断言按删债后实际单数修正）。README 表索引 105→101 张。

### 第 2 批：修 bug —— ✅ 已完成（B3 自动消费循环有意推迟至第 5 批，见该条备注）

| Bug | 状态 |
|---|---|
| B7 告警跨库双事务 | ✅ 已修：`alerts` 改为先 central 写告警（event_id 去重幂等）、后 gateway 推进 `processed_at`，包注释写明顺序契约；两方向均不丢事件、不重复建告警。**2026-10-02 第 5 批③注记**：新架构下不再有跨库双事务——同步整体在 central 单库事务内完成，原顺序契约由"recordFault 幂等 + mark-processed 失败留待重扫"等价保证（mark-processed 只推进 `processed_at IS NULL` 的行，失败不丢事件） |
| B13 worker_db 环境变量 | ✅ 已修：worker 进程 `DATABASE_URL` → `DATABASE_URL_WORKER`；`compose.dev.yaml`、`docker-compose.yml`、`.env.example`、`check-deploy.mjs` 同步 |
| B17 错误码 1005 一码三义 | ✅ 已修：`httpapi/codes.go` 定义具名常量区段（1xxx 请求凭证 / 2xxx 业务冲突 / 5xxx 依赖故障）；409 冲突改用 2000/2009/2010 等，405 改用 1006；`schedule`、`regulatory` 等裸整数全部替换；前端无数值匹配无需改动 |
| B18 `198.18.0.0/15` 放行 | ✅ 已修：按 D3 关闭开口——该网段现按非公网目标拦截（`isPrivateAddress` 返回真），注释注明若开发需要应以配置项仅 dev profile 放行；测试已更新 |
| B19 三处 OnConflict 空更新 | ✅ 已修：`auto_stop.go`、`card_deadline.go` 改为先查回、冲突即返回错误进入 10s 重试；`meter_segments.go` 直接插入、主键冲突按错误上抛由 `SyncBatch` 记录重试 |
| B1/B2 DLQ 语义 | ✅ 已修：失败不再先 XAck，保留 pending 由 `"0"` 回收路径重投，预算耗尽转死信；`attempts()` 改为按消息 ID 定点 XPENDING，积压超 100 也能取到真实投递数。附带发现并修复：`moveToDeadLetter` 先 XACK 再 XDEL（XDEL 不清 PEL，不 ACK 会留幽灵 pending 项）。新增集成测试 `TestDLQRetriesThenDeadLetters` 覆盖"失败保 pending → 重投 → 转死信"全链路 |
| B3 DLQ/ops 接线 | 🔶 部分完成：`DLQ.Streams` 已设（导出 `DefaultBusinessStreams`），`OpsAPI.Replay` 已赋值——退款流重放走 `ResultConsumer.Handle`（comp_tx_log 幂等），其余流显式报"无注册的重放处理器"而非假装成功；ops 积压/死信查询与重放接口从恒空/恒 503 变为可用。**自动消费循环仍无生产调用，有意推迟到第 5 批**：现网各流要么无业务消费组（webhook 仅 XRANGE 扫描，接线会双重投递，需与 B4 的 webhook 重设计一起做）、要么已被 `ResultConsumer` 消费（并行组会重复处理），单独接线没有安全落点。**2026-10-02 第 5 批①注记**：`ResultConsumer` 与 comp_tx_log 已整体删除（退款结算本就在 central 派发行内完成），退款流重放分支随之移除，本条所述"退款流重放走 ResultConsumer"不再适用 |
| B4 webhook 重复外投 | ✅ 已修（随第 5 批④完成，见该批）：投递整体改为"拉订阅清单 → XRANGE 扫描 → 外投 → 结果批量上报 central → **XDEL 删除已处理条目**"。此前消费组从未建立、XACK 空转，成功条目留在流里被每次扫描反复 POST；修复后交付结果先落库再删条目（落库失败保留条目重投，至少一次语义）。无匹配订阅的事件视为已处理同样删除；无法解析的条目仍保留给属主。新增集成测试 `TestHandledEntryIsDeletedAndNotRescanned` |
| B5 worker HTTP Client 未接线 | ✅ 已修：`cmd/worker/main.go` 建 3 个共享 `http.Client`（内部同步 5s / 长任务派发 30s / webhook 外投 10s，沿用各组件原回退超时），接入 Synchronizer / EndSynchronizer / PaidStarter / CardDispatcher / AutoStopper / billing.Dispatcher / refund.Dispatcher / WebhookDeliverer 全部 8 个组件，连接池真正生效 |
| B6 跨月重投重复入账 | ✅ 已修（已被第 5 批①整体取代，见该条）：`isCommitted` / `recordTx` / `markTx` 全部改为按 `tx_id` 全分区访问（`uk_tx` 含分区列，跨月重投会建新行，锁死当月会把上月已提交当成未提交），并删除重复的 `Where("tx_id = ?")`。新增集成测试 `TestRefundResultCommittedInPreviousMonthSkipsPosting`：预置上月 committed 台账，验证跨月重投不再重复入账。**注：第 5 批①已删除整个 `ResultConsumer` 与 comp_tx_log，该修复与测试随组件一并移除——幂等改由 central `apply()` 行内的 `refund_success_receipt` 唯一性保证** |

### 第 3 批：命名归一 —— ✅ 已完成（含两处计划外冲突裁决）

| 动作 | 结果 |
|---|---|
| 表 `admin_user_role` → `admin_user` | ✅ schema（`migrations/central_db/0001_init.sql`）与全部 Go 引用（store/operations/roles/admin_users 及测试）已改；无外键列引用旧名，安全 |
| `permission.module` 4 处修正 | ✅ `order.refund.*` → module `refund`；`feedback.*` → `feedback`；`fault.read`/`fault.dispatch` → `fault`（`fault.resolve` 本来就是 `fault`）；`charge_user.read` 改名后 module 为 `user`，`charge_user` 模块消失 |
| `charge_user.read` → `user.read`、路由 `charge-users` → `users` | ✅ 后端路由（`charge_users.go`）、`openapi.json`、`admin-web` 全部同步（API 路径、权限串、React Router 内部路径）；miniprogram 无引用。测试文件内自注册路由一并统一 |
| **计划外冲突 1：路由双归属** | 改名后发现 `operations.go` 的旧 `GET/POST /api/v1/admin/users`（通用列表 + createUser，admin 账号侧）与 C 端用户路由撞车。**裁决**：admin 账号侧整体归入 `/admin-users` 家族——通用 `GET "users"` 删除（由 `admin_users.go` 的分页 `GET /admin-users` 取代，返回列是其超集），`POST /users` → `POST /admin-users`；openapi.json 补 POST 条目并重写 `/users` GET 段为充电用户语义 |
| **计划外冲突 2：前端路由/菜单撞名** | `App.tsx` 中 `path="users"` 被充电用户页与管理员页重复注册，`MainLayout` 菜单 key `/users` 同样重复。**裁决**：管理员页路由/菜单改 `/admin-users`；`Casework.tsx` 指派下拉改调 `GET /admin-users?page_size=100`（响应 `items` 结构兼容），`Users.tsx` 创建改 `POST /admin-users` |
| B14 webhook 包名归位 | ✅ `internal/worker/webhook` 包名 `worker` → `webhook`（含集成测试外部测试包），`cmd/worker/main.go` 去掉 `webhookdelivery` 别名直接 import |
| B13 worker_db 环境变量 | ✅ 已在第 2 批完成（`DATABASE_URL_WORKER`，见上表） |

验收：`go build ./...`、`go vet ./...`、`gofmt -l` 全绿；`scripts/test/integration.sh` 隔离集成测试 26 个包全绿无 FAIL；`admin-web` `npm test` 78/78 通过、`npm run build` 成功（chunk 体积警告为既有现象）。

### 第 4 批：提升共享契约库 —— ✅ 已完成

| 动作 | 结果 |
|---|---|
| `internal/gateway/protocol` → `internal/protocol`，`dc589` 平移 | ✅ `git mv` 整树移动；49 个 Go 文件的 import 路径机械替换（sed 后 gofmt 修复对齐）；`simulator/dc589` 留在 `gateway/simulator`（按 3.2 定稿），仅改 import |

验收：`go build` / `go vet` / `gofmt -l` 全绿；隔离集成测试全绿（含新路径 `internal/protocol`、`internal/protocol/dc589` 两个包）。

**遗留边界（记录，不在本批扩scope）**：生产代码 `central/*`、`worker/*` 已零依赖 gateway 目录树；仍有 4 个集成测试文件 import gateway 侧测试夹具（`admin/resources`、`admin/vendors` 用 `gateway/provision`；`worker/alerts`、`worker/outbox` 用 `gateway/store`）。属测试期依赖，处理需建独立 testfixture 包，列入后续批次可选项。

### 第 5 批：切断 worker 跨库直写 —— ✅ 已完成

**前置 PR 0（gateway 证据批量/游标端点）✅ 已完成**：
- `GET /api/v1/internal/devices/:device_id/charging-samples` 增加 `after_id` 游标参数，响应增加 `next_after_id`；窗口上限仍为 7 天、单段 10080 行，语义向后兼容（既有调用方 `central/charge/live_meter.go` 不受影响）
- 新增 `POST /api/v1/internal/charging-evidence`：一次最多 20 条 `{device_id, port_no, started_at, after_id}`，供停机判定循环批量取证。挂在 `/internal` 根而非 `/devices` 下——`/devices` 已有 `:device_id` 通配，静态段会在 gin 基数树冲突
- 实现抽取共享的 `collectChargingSamples`（统一改为 id 升序 + limit+1 探测截断，替代原 DESC+反转）；新增单元测试（令牌/边界校验 + 路由注册）与隔离集成测试（游标翻页三页断言、批量端点端到端含窗口过滤）

验收：`go build` / `go vet` / `gofmt` 全绿；`scripts/test/integration.sh` 全量绿，PR 0 两个 DB 集成测试定向验证通过。

**主体（5 张表逐表改造）✅ 全部完成（①含对计划原文的证据修正）**：

**① 退款资金表 ✅ 已完成——按证据修正计划原文。** 计划原文为"central 新增 `POST /api/v1/internal/refunds/:refund_no/settle`，worker 改调 HTTP"。实施前复核发现该路径服务的是死代码：
- `refund_succeeded_stream` 的唯一生产者是 central 自己的 `RefundExecutor.apply`（`refund.go:241-247`），结算（refund_record + refund_success_receipt + payment_order + 钱包 + charge_order 状态）已在 central 派发行内完成
- worker `ResultConsumer` 期待的 `RefundResult{success, channel_ref, payment_order_no}` schema 在全仓无任何生产者（`refund_result_stream` 流无生产者），它消费 central 的成功通知时 `success` 恒缺省为 false，实际只做错误的 comp_tx_log 记账，从不触发资金写入
- 原 `post()` 与 `apply()` 逻辑重复且更少（无钱包/订单状态）

**处置（比计划原文更彻底地达成 P0-1 目标）**：删除 `ResultConsumer` 及其 4 个专属测试；`comp_tx_log` 表随 worker schema 删除（唯一使用方就是它）；`cmd/worker` 去掉退款结果消费循环与退款流重放分支（重放现统一报"无注册处理器"）；README 表索引 101→100、异步任务与流用途说明同步。

**② `charge_billing_cutoff` 停机判定移回 central ✅ 已完成（2026-10-02）**：
- 判定引擎整体移入 `central/charge`：`auto_stop.go`（订单扫描、断电/时长/预算判定、计费截止点冻结、停机下发）+ `card_deadline.go`（刷卡时长截止冻结，锁序契约注释随代码迁移）。心跳证据改经 PR 0 的 `POST /charging-evidence` 批量端点按订单拉取（20 单一批、next_after_id 最多翻 8 页），不再直读 `gateway_db.device_event`
- central 新增 `POST /api/v1/internal/charge-orders/auto-stop`（服务令牌鉴权，25s 扫描超时）；worker 侧 `AutoStopper` 瘦身为单行触发器，只发扫描意图，不读计费快照、不写 cutoff、不碰 gateway 库
- `cmd/central`、`cmd/worker` 重接线；`measuredSegments` 判定侧调用直接走 `pricing.MeasuredSegments`（worker 保留同名包装供 `EndSynchronizer` 使用，其 gateway 库访问归第 5 批⑤）
- 测试：`noPowerForMinute` 单元测试随代码迁移；新增集成测试 `TestAutoStopFreezesCutoffAndRequestsStop`（httptest 模拟 gateway 取证/停机，断言 cutoff 冻结为 `duration_exhausted` 且停机恰下发一次）与端点鉴权测试。全量集成测试绿

**③ `alert_event` 同步移入 central ✅ 已完成（2026-10-02，按①②模式修正计划原文）**。计划原文为"central 新增告警写入端点、`worker/alerts` 整体并入 `protocol/dc589`"。实施时沿用①②确立的"worker 薄触发 + central 干活"模式，连写入端点都不需要：

- **故障语义入协议适配器**：`internal/protocol/dc589/fault.go` 导出 `FaultMetric(event)`（0xBB→烟雾/fatal、0xAA→高温/critical、设备级→device_fault、端口→port_fault_N）与 `FaultRecovered(metric, heartbeat)`（PortStates nil 不算恢复；设备级要求 DeviceStatus==0；端口级 state≤2）。配 `fault_test.go` 单测
- **gateway 新增三个内部端点**（`gateway/control/device_faults.go`，证据读取面）：`GET /api/v1/internal/device-faults?limit=1..100`（未处理 dc589 故障原始载荷）；`POST /api/v1/internal/device-faults/mark-processed`（ids 1–100，事务内只推进 `processed_at IS NULL` 的行，返回 marked）；`GET /api/v1/internal/devices/:device_id/latest-heartbeat?after=RFC3339` 或 `?after_key=<event_key>`（after_key 优先：先按水印事件 received_at 定位，再查其后的最新心跳；水印不存在返回 found:false）。配边界单测 + DB 集成 round-trip
- **同步器移入 `central/admin`**：`alerts_sync.go` 的 `DeviceAlertSync` 拉故障→recordFault（本地事务，event_id 去重幂等语义照抄旧 worker 版）→批量 mark-processed→resolveRecovered。**恢复判定的关键语义对齐**：旧 worker 版按故障源事件的 received_at 锚定最近一次心跳，新实现用 `after_key=alert.EventID` 等价复现（曾误用 alert.CreatedAt 导致旧心跳提前恢复，集成测试抓出后修正）；`alerts_sync_http.go` 注册 `POST /api/v1/internal/device-alerts/sync`（令牌鉴权，25s 超时）
- **worker 瘦身为触发器**：`internal/worker/alerts/` 只留 `sync.go`（包注释写明领域逻辑已迁 central），每秒 POST 上述端点，不读 gateway 库、不写 alert_event。`cmd/central`、`cmd/worker` 重接线
- **测试平移**：原 worker 烟雾告警集成测试场景平移为 `TestDeviceSmokeAlertRecoversAndRaisesAgain`（建警→恢复→再触发全链路）。全量集成测试 25 包绿、0 FAIL

**④ webhook/regulatory 配置与投递分离 ✅ 已完成（2026-10-02）**。按 3.4 定稿合并为顶层 `internal/delivery`，worker 侧零数据库句柄：

- **worker 侧 `internal/delivery`**：`webhook.go` 的 `WebhookDeliverer`（Stream + serviceclient，外投 10s 共享连接池）+ `regulatory.go` 的 `RegulatoryDeliverer`（Sender 签名发送）+ `events.go`（`Event`/`ValidateEvent` 与 central 共用）+ `senders.go`。删 `internal/worker/webhook` 与 `internal/regulatory`（含其集成测试），B14 随之消解（旧包名 `worker` 别名问题不存在了）
- **central 新增 `DeliveryDispatchAPI` 四个内部端点**（`central/admin/delivery_dispatch*.go`，令牌鉴权）：`GET /webhook-subscriptions`（启用订阅清单，event_types 解码）；`POST /webhook-deliveries/record`（批量 ≤100 upsert，**attempt_count 改由数据库自增**——旧版 worker 先查最大尝试数再 +1 有两步读风险）；`POST /regulatory-reports/claim`（`SELECT ... FOR UPDATE` 事务领取 ≤20 笔，2 分钟租约，行锁取代旧实现的乐观重查）；`POST /regulatory-reports/finish`（按 lease_token 条件推进：成功置 delivered，失败回 queued 并按 2^n 秒有界指数退避重排——**退避计算移入 central**，用 MySQL 赋值左到右求值等价复现旧 `retryDelay(attempts+1)`，集成测试核对了时序语义）
- **监管入队/状态查询 API 平移进 `central/admin`**（`regulatory_events.go`，路由不变 `POST/GET /api/v1/internal/regulatory/events*`）：`internal/regulatory` 包整体删除，`cmd/central` 原已注册该 API 只是换了实现来源；`cmd/worker` 去掉 `databases["admin"]/orms["admin"]` 别名——**worker 至此零 central_db 表访问**（余下 gateway_db 6 处归第 5 批⑤）
- **B4 顺带修复**：投递完成（含无订阅匹配）的条目 XDEL 删除，结果先批量上报 central 再删（上报失败保留条目重投，至少一次语义）；无法解析的条目仍保留给属主。原 XACK 路径删除（消费组从未建立，XACK 一直空转）
- **测试**：webhook 单测（解析/匹配/签名）+ `TestHandledEntryIsDeletedAndNotRescanned`（B4：已处理条目删除、失败保留、netguard 拦截仍上报记录）+ `TestUnreadableEntryIsLeftForItsOwner`（平移）；central 侧 `TestWebhookDispatchRoundTrip`（upsert 自增）与 `TestRegulatoryDispatchClaimAndFinish`（租约/退避/失效回执 lost）+ 端点鉴权测试；`TestRegulatoryDeliveryRoundTrip` 平移原六类对象场景（mock central 走完整 claim/send/finish 回环）。全量集成测试 24 包绿、0 FAIL（包数 25→24：regulatory、worker/webhook 删除，delivery 新增）

**⑤ `gateway_db` 6 处改走 gateway HTTP ✅ 已完成（2026-10-02）**。计划原文假设"`/api/v1/internal/*` 已存在对应能力"，实施复核后修正：4 处可复用既有端点，`card_event` 决策冻结与 `charge_end_delivery` 冻结两处能力缺失，由 gateway 新增 `control/worker_sync.go`（挂在 TelemetryAPI，服务令牌鉴权）补齐：

- **gateway 新增 13 个 worker-sync 内部端点**：启动回执清单/批量上报标记、结束事件清单、按订单查启动命令（found:false 兜底）、device_event 批量 mark-processed、端口释放（非空闲非本单 409 code 2009）、meter-samples 取证（10080 哨兵语义：超限放弃分段）、charge-end-deliveries 冻结（首写 frozen:false / 同事实重放 frozen:true，**身份校验忽略 segments**，异事实 409）、card-events 清单/决策冻结 begin+finish（条件首写，输家返回胜出方回复）/advance（retry 重排）、端口解析。配 13 端点鉴权单测 + 5 个 DB 集成用例（worker_sync_integration_test.go）
- **worker 四个组件零 gateway 库句柄**：`Synchronizer`（sync.go）清单与上报标记走 HTTP；`EndSynchronizer`（end_sync.go / meter_segments.go）事件清单、命令查询、取证、冻结、端口释放、mark-processed 全走 HTTP，release 失败为硬错误（"gateway port could not be released after settlement"）；`CardDispatcher`（cards.go）决策经 decide-finish 首写冻结，端口解析走 `/ports/resolve`（decide-begin 端点存在但 worker 不调用——单进程串行 + finish 首写已足够）；新增 `sync_client.go`（gatewaySyncAPI 统一解 code/data 包）。`cmd/worker/main.go` 重接线，四个组件去掉 `GatewayDB` 字段
- **范围说明（与计划一致）**：`PaidStarter` 不在本批——它读的是 central 的 charge_order，经 HTTP 访问 central，本就不碰 gateway 库；outbox Publisher 保留三库 event_outbox 直读（计划认可的形态）；worker 的 `DATABASE_URL_GATEWAY` 连接保留给 outbox。worker 业务组件至此全部零跨库句柄
- **测试重写**：原 sync/end_sync 两个集成测试从"真库 + httptest central"重写为"httptest mock-gateway（有状态端点状态机）+ mock-central"契约测试——断言 central 失败不 mark-reported、成功才标记；结束同步断言调用顺序 freeze→central→release→mark-processed、central 503 时不 release、证据消失后重放 freeze 返回 frozen:true 且 central 收到的 payload 内容不变。`gorm_test_helper_test.go` 保留（paid_start 测试仍用）。全量集成测试 24 包绿、0 FAIL

**第 5 批总验收**：`go build` / `go vet` / `gofmt` 全绿；`scripts/test/integration.sh` 全量 24 包 ok、0 FAIL。worker 至此：资金零句柄（①）、停机零句柄（②）、告警零句柄（③）、webhook/regulatory 零句柄（④）、gateway_db 业务访问零句柄（⑤）——跨库直写全部切断，仅剩 outbox 三库 event_outbox 直读（计划认可形态）。

### 第 6 批：拆 `central/charge` + 瘦 `central/admin` —— ✅ 已完成（2026-10-02）

按 3.3 表前缀家族拆包（`charge` 69 文件 / 11187 行，`admin` 15,829 行，逐家族推进）。执行顺序：先 `channel` 改名腾出 `payment` 名，再按 order / settlement / payment+refund / wallet / card / coupon / user_account 回并 identity 逐家族拆。

**步骤 1：`central/payment`（渠道适配器）→ `central/channel` ✅ 已完成（2026-10-02）**：
- `git mv internal/central/payment internal/central/channel`，包名 `payment` → `channel`（wechat.go / simulation.go / refund.go / order_number_test.go / refund_test.go）
- 13 个引用点更新 import 与限定符（`cmd/central/main.go`、`admin/resources_integration_test.go`、`charge` 包 11 文件）。注意点：charge 包里大量 `payment.XXX` 是本地变量（PaymentOrderRecord 形参）而非包引用，只替换 8 个导出符号（Simulator / PrepayRequest / PrepayParams / RefundProvider / RefundRequest / RefundResult / ErrRefundNotFound / VerifiedTransaction / NewWechatDirect / Config），本地变量零误伤
- 环境变量 `PAYMENT_MODE` 与路由 `/api/v1/public/payments/*` 不变（定稿：只改包名）
- 验收：`go build` / `go vet` / `gofmt` 全绿；`scripts/test/integration.sh` 24 包 ok、0 FAIL

**步骤 2：`coupon` 家族拆包 ✅ 已完成（2026-10-02，首个家族，验证拆包模式）**：
- 新建 `internal/central/coupon`：迁入 `coupon.go`（券查询/报价/核销 + `CouponAPI` 用户只读视图）、`coupon_activity.go`（活动规则求值）；表归属 `coupon*` 家族，含 `coupon_grant_request` 等全部券表读写
- **零反向依赖的关键改法**：`redeemCouponInTx(tx, intent PaymentIntentRecord, order PaymentOrderRecord, …)` 改为 `RedeemInTx(tx, RedeemRef{…}, reference)`——coupon 包只接收最小事实集合，不 import charge 的 gorm 记录类型；`isEstablishedUser` 本就走 `tx.Table("charge_order")` 裸表名，天然无类型依赖。`chargeNoFor`（PENDING 单号推导）属支付侧，随迁出后由 charge 侧 `pendingChargeNoFor` 承接
- 跨包符号导出：`ActivityEvent` / `ActivityResult` / `ErrActivityNotApplicable` / `ActivityEventKeyForOrder` / `ActivityEventKeyForRecharge` / `RedeemInTx` / `RedeemRef`；`charge` 侧 5 个调用文件（payment_callback / billing / payment_intent / payment_http / user_account）与 `cmd/central/main.go` 改经 `coupon.` 调用
- 测试：`coupon_activity_integration_test.go` 随包迁移为 external test 包（`coupon_test`，import charge 不构成环——coupon 生产代码不 import charge）；charge 侧 `billing_activity_integration_test.go` 所需的活动夹具在 `activity_fixture_test.go` 重建一份（注释说明两边独立维护的原因）；`utcDate` 依赖以 `time.Now().UTC().Truncate(24h)` 就地替代
- 验收：`go build` / `go vet` / `gofmt` 全绿；`scripts/test/integration.sh` 25 包 ok（新增 coupon 包）、0 FAIL
- **确立的家族拆包模式（后续家族沿用）**：①生产代码单向依赖（charge → 新家族），新家族不 import charge；②跨家族函数签名只传最小值结构或裸表名，不传 gorm 记录类型；③被迁移文件的 internal 测试改为 external test 包随迁；④留在 charge 的测试若用迁移夹具，在 charge 侧就地重建

**步骤 3：`wallet` 家族拆包 ✅ 已完成（2026-10-02，调整执行顺序：叶节点优先）**：
- **顺序修正**：原计划先拆 order，勘察发现 order 四文件（start_result 等）反向依赖 refund/wallet/card 的函数与记录，直接先拆必然产生 import 环；改为先拆叶节点家族，order 留待 charge 瘦身后再拆
- 新建 `internal/central/wallet`：迁入 `wallet_refund.go`（锁钱包/结算/释放三函数，改收自包 `Settlement` 最小事实结构）与 `wallet_refund_reservation.go`（`ReserveRefund`）
- **零反向依赖的第二招（与 coupon 的 RedeemRef 同族）**：`ReserveRefund` 不再使用 charge 的 `PaymentOrderRecord`/`RefundRecord` 类型——支付行查询改局部行投影（`tx.Table("payment_order")` + 内联匿名 struct），退款行插入改用带列名的匿名 struct（GORM 回填自增 ID）；错误哨兵独立为 `wallet.ErrConflict`
- 调用方更新：`charge/refund.go`（executor 经 `walletSettlement(r)` 映射，五处调用）、`charge/user_account.go`（提现；局部变量 `wallet` 遮蔽包名，import 别名 `walletrefund`；409 分支同时匹配 `ErrRefundConflict` 与 `wallet.ErrConflict`）、`admin/wallet_risks.go`（风控审核代客申请，错误映射改 `wallet.ErrConflict`，并顺势移除对该文件唯一的 charge import）
- 表归属说明：`wallet_refund_part` 写入随退款事务归 refund 使用方；wallet_account/wallet_txn 的其余写入（充值入账在 payment_callback、余额视图在 user_account）随各自家族迁移，本包只承载"退款事务内的钱包侧"逻辑
- 验收：`go build` / `go vet` / `gofmt` 全绿；`scripts/test/integration.sh` 25 包 ok、0 FAIL

**步骤 4：家族记录类型分发 ✅ 已完成（2026-10-02，order/payment/refund/settlement 四包落地）**：
- 新建 `central/order`（ChargeOrderRecord / StartReceiptRecord / EndReceiptRecord / ActivePortChargeRecord / ChargeEventLogRecord / ChargePricingSnapshotRecord）、`central/payment`（PaymentOrderRecord / PaymentIntentRecord / PaymentCallbackDigestRecord / ChargePrepayRecord）、`central/refund`（RefundRecord）、`central/settlement`（ChargeFeeRecord + Fees 解码）；均为纯记录类型，零 charge 依赖
- `charge/gorm_models.go` 从 215 行瘦身为 40 行：只留跨家族的 EventOutboxRecord（event_outbox 由多家族共写，归属留待批次收尾）与共享的 isMySQLDuplicate
- 31 个引用文件全部改经家族包限定（admin 侧 `charge.XxxRecord` 同步改为 `order./payment./refund.` 限定）
- **遮蔽处理**：charge 包内 `order`/`payment`/`refund` 是高频局部变量名——冲突文件用 import 别名（`orderpkg`/`paymentpkg`/`refundpkg`/`settlementpkg`），仅限定记录类型名、不动局部变量字段引用；不冲突文件保持默认包名
- **资金族环的结构性结论（写入文档备后续执行）**：order/payment/refund 三族逻辑互相在同一事务内写对方表（start_result 建失败退款、refund executor 更新 charge_order、payment_callback 建迟到退款），Go 禁止 import 环，"refund 与 payment 允许双向引用"在字面上无法实现；后续逻辑拆包采用：①跨家族读一律用局部行投影（同 wallet 包手法）；②跨家族写由被写方家族提供 `XxxInTx(tx, 最小结构)` 函数；③实在同事务双向的（executor 改 charge_order 状态 vs start_result 建退款记录），由事务编排方持有 tx 并单向调用，环在编排层剪断（必要时接口注入，cmd/central 接线）
- 验收：`go build` / `go vet` / `gofmt` 全绿；`scripts/test/integration.sh` 25 包 ok、0 FAIL；`admin/order_package.go`、`admin/refunds.go`、`admin/charge_user_recharges_integration_test.go`、`admin/order_process_integration_test.go`、`admin/order_scheme_integration_test.go` 五个文件顺势移除 charge import（记录引用已迁出）

**步骤 5：order 家族逻辑文件迁包 ✅ 已完成（2026-10-02，顺序修正：order 先行于 card/settlement）**：
- 执行顺序再修正：步骤 3 曾判 order 为"反向依赖 refund/wallet/card、留待后拆"，步骤 4 落地记录包后重新勘察：`start_result.go` 确因依赖 card 族 `walletRefund/lockWallet/walletRow`（在 online_card.go）与 `settlementNotice`（billing.go）暂不迁；但 order 四文件（order_state / authorization / end_result / order_number）本身已是可剪环的独立子图，先行迁出可为后续家族腾出"被写方"归属，故 order 逻辑先行
- `git mv` 四文件到 `internal/central/order/`：`order_state.go`、`authorization.go`、`end_result.go`、`order_number.go`（包名 `order`，自包引用去限定）
- 导出符号清单：`SettledPaymentStatus`、`SyncOrderPaymentStatus`、`PaymentStatusSQL`（const）、`StartAuthorization`、`EndMeter`、`EndResult`/`EndResultStore`/`EndResultAPI`、`ErrEndResultConflict`、`ChargeOrderNumber`（原 `newChargeOrderNumber`）、`SameSegments`（原 `sameSegments`）；`ChargeOrderNumber` 内部对 payment 包 `ErrPaymentIntentConflict` 的引用以包限定保留（order → payment 单向，无环）
- 记录包补强：`eventoutbox` 独立小包落地（`EventOutboxRecord` 从 charge/gorm_models.go 迁出，多家族共写表只有这一个记录类型）；`card` 记录包落地（`CardCharge`/`CardOperation` 从 online_card.go 迁出）；`payment` 包补 `errors.go`（`ErrPaymentIntentConflict` 从 payment_intent.go 迁出）——三者均为纯记录/哨兵，零 charge 依赖
- 测试随迁与改造：`authorization_integration_test.go` 随迁改 `package order_test`（局部变量 `order` 遮蔽，用 `orderpkg.StartAuthorization`；尾部自建 testGORMDB helper）；纯单元测试 `order_number_test.go`、`order_state_test.go` 随迁改 `package order`（同包直调，无需别名）
- charge 侧调用方改造（遮蔽文件用 `orderpkg`/`cardpkg`/`paymentpkg` 别名，仅限定类型名）：`online_card.go`（SyncOrderPaymentStatus×2、NewChargeOrderNumber→ChargeOrderNumber）、`refund.go`、`meter_review.go`（SameSegments）、`billing.go`（EndMeter）、`payment_callback.go`（ChargeOrderNumber）、`order_state_integration_test.go`、`online_card_integration_test.go`（card 局部变量遮蔽两处 + EndResult/EndMeter/EndResultStore）、`billing_number_integration_test.go`、`start_result_integration_test.go`（补 orderpkg import）
- `cmd/central/main.go`：`charge.StartAuthorization/EndResultAPI/EndResultStore` → `order.` 前缀（新增 order import）；`admin/payment_orders.go` 的 `charge.PaymentStatusSQL` → `order.PaymentStatusSQL`、`admin/order_states_integration_test.go` 的 `charge.SettledPaymentStatus` → `orderpkg.SettledPaymentStatus`，两处均移除 charge import（admin 瘦身累计 7 个文件）
- **遗留**：`start_result.go` 仍留 charge 包，待 card 家族（walletRefund/lockWallet/walletRow 归属）与 settlement 家族（settlementNotice 归属）拆包完成后再迁入 order
- 验收：`go build` / `go vet` / `gofmt` 全绿；`scripts/test/integration.sh` 26 包 ok（新增 order 包）、0 FAIL

**步骤 6：card 家族拆包 ✅ 已完成（2026-10-02，含 6a/6b/6c）**：
- **6a 钱包账本助手归属落定 ✅（2026-10-02）**：`walletRow`/`lockWallet`/`walletMove`（原散在 online_card.go，被 billing/settlement 与 start_result/order 复用）迁入 wallet 包 `ledger.go`，导出为 `Row`/`Lock`/`Move`；错误语义独立为 `wallet.ErrInactive`/`wallet.ErrInsufficient`，charge 侧 `mapWalletMoveError` 映射回 `ErrCardOperation`/`ErrCardBalance`，对外错误语义零变化。`walletRefund`（刷卡退款编排：钱包入金 + payment_order 改写 + order 状态同步 + 退款记录）定性为**卡族编排**，暂留 charge（signature 改收 `*walletpkg.Row`）；后续 billing/settlement、start_result 迁出时由各自包 import wallet 原语 + 复制编排或上移编排层
- **6b 前置依赖归位 ✅（2026-10-02）**：
  - 新建 `internal/platform/dbutil`：`UTCDate()`（UTC 日界，created_month 统一口径）与 `IsMySQLDuplicate()`（MySQL 1062 兼容 GORM 包装）；charge 侧 `utcDate`/`isMySQLDuplicate` 保留为薄包装（存量 11 个文件调用点零改动），新迁出家族直接用 dbutil
  - `checkoutPortAvailable` → payment 包 `CheckoutPortAvailable`（charge_payment_intent 归 payment 家族；charge_order 经裸表名 LEFT JOIN 读取，payment 不 import order，保持下游无环）；`newPaymentOrderNumber` → payment 包 `NewOrderNumber`；charge 侧旧文件 `checkout_port.go`/`payment_number.go` 删除，5 个调用点改写（online_card 用 paymentpkg 别名，payment_intent/user_account 不遮蔽用默认名）
  - `freezeCardDeadline` 事务体 → card 包 `FreezeDeadline(ctx, db, id, now)`（card_charge/charge_billing_cutoff 归 card 家族）；charge 侧保留 AutoStopper 薄方法委托（方法必须与类型同包）
  - card 包新增 `port.go`：`PortRef`（刷卡所需最小端口事实）+ `PortLookup` 接口——scan 家族（charge.ScanAPI）经 cmd 接线注入，card 不反向依赖 charge
  - 验收：`go build` / `go vet` / `gofmt` 全绿；`scripts/test/integration.sh` 26 包 ok、0 FAIL
- **6c 四个文件并入 central/card ✅（2026-10-02，card 家族收官）**：
  - `git mv`：online_card.go / card_http.go / card_balance.go 与两个测试文件入 `internal/central/card/`，与记录 models.go、deadline.go、port.go 合并为完整 card 包；charge 侧仅剩 AutoStopper 薄方法（card_deadline.go）与 ScanPortLookup 适配器（card_lookup.go）
  - `Swipe` 改收 `card.PortRef`（替代 charge.ScanResult，条件判断 `port.Port==nil` → `!port.Found` 等价改写）；`CardAPI.Scan` 改 `PortLookup` 接口，charge 新增 `ScanPortLookup` 适配器（ScanResult→PortRef 映射），cmd/central 接线注入；端口编码校验正则 card 侧本地定义（与 scan 的 userScanCodePattern 同口径、各自输入契约独立维护）
  - `walletRefund` 导出为 `card.RefundInTx`（卡族退款编排：wallet.Move + payment_order 改写 + orderpkg.SyncOrderPaymentStatus + refundpkg 记录）；`utcDate`/`isMySQLDuplicate` 调用点改 `dbutil` 直用
  - **剪环处置**：card 逻辑依赖 order（编号分配/支付状态同步/订单记录），而 order 的 authorization/end_result 原本 import card 记录——按"跨家族读写用裸表名/局部投影"规则，order 两处改 `tx.Table("card_charge")`（authorization 行投影读卡号与余额快照；end_result 仅取行锁 + 裸表名清 active_port），order 摘除 card import，card→order 单向成立
  - 调用方与外部引用：`charge/billing.go` 的 `walletRefund`→`cardpkg.RefundInTx`、`ErrCardOperation`→`cardpkg.ErrCardOperation`；`charge/start_result.go` 同改（card 默认名）；`admin/online_cards.go` 改 cardpkg 别名（局部 `card` 变量遮蔽，第 8 个移除 charge import 的 admin 文件）；`charge/payment_number_integration_test.go` 的刷卡夹具改 cardpkg.PortRef（扫码意图与刷卡端口助手拆分）
  - 测试随迁：online_card_test.go（CanExtend 纯单元）改 `package card`；online_card_integration_test.go 改 `package card_test`（局部 `card`→`cardRow`，testGORMDB 助手就地重建，引用 charge.BillingOrders/charge.StartResultStore 属测试期跨包引用，不构成生产环）
  - 验收：`go build` / `go vet` / `gofmt` 全绿；`scripts/test/integration.sh` 27 包 ok（新增 card 测试包）、0 FAIL
- **start_result.go 收尾策略（写入备执行）**：迁 order 时 card 引用按以下处置——①`card.RefundInTx` 在 order 内就地复现（walletpkg.Move + paymentpkg 改写 + 本包 SyncOrderPaymentStatus + refundpkg 插入，约 20 行；order 不得 import card，因 card→order 已单向）；②card_charge/card_operation 读写改裸表名（end_result.go 已有同先例）；③settlementNotice 待 settlement 家族拆包时随 billing.go 定性

**步骤 7：settlement 家族拆包 ✅ 已完成（2026-10-02）**：
- 勘察确认 settlement 家族对 charge 其余部分**零被依赖**（settlementNotice 仅在族内使用，start_result.go 实际不依赖），属干净切口
- `git mv` 四文件入 `internal/central/settlement/`：billing.go（BillingOrders 结算编排 + settlementNotice + applyOrderCampaigns）、bill.go（BillStore/BillIssuer/BillHTTP 账单视图）、manual_settlement.go、meter_review.go（MeterReview 计量复核）；与记录 models.go（ChargeFeeRecord）合并为一包；bill.go 摘除对旧 settlement 记录包的自引用 import 并去限定
- 依赖处置：`readPaging`/`readPagingDefault` 上移 `internal/platform/httpapi` 为 `ReadPaging(c, fallback)`（分页解析是跨领域 HTTP 关注点；charge 侧留薄包装，settlement 直用）；`utcDate` 调用点改 `dbutil.UTCDate`
- 调用方改写：`admin/meter_reviews.go`（ManualSettlement/BillingOrders/MeterReview → settlement 包，第 9 个移除 charge import 的 admin 文件）；`cmd/central/main.go`（BillingOrders/BillIssuer/BillStore/BillHTTP → settlement 包）；card 集成测试的 BillingOrders 引用同步改
- 测试处置：settlement_notice_test.go（纯单元）与 billing_number_integration_test.go 随迁（testGORMDB 助手在 settlement 侧重建）；activity_fixture_test.go 随迁（唯一使用方已迁出）；**billing_integration_test.go 留 charge**——它同时驱动 charge.RefundExecutor（refund 家族未迁）与 settlement.BillingOrders，属跨家族集成测试，待 refund 家族迁包后再定性归属
- live_meter.go（LiveMeterView/LiveMeterService，admin 使用）属遥测视图而非结算，留 charge 待后续批次定性
- 验收：`go build` / `go vet` / `gofmt` 全绿；`scripts/test/integration.sh` 28 包 ok（新增 settlement 测试包）、0 FAIL

**步骤 8：payment 家族拆包 ✅ 已完成（2026-10-02）**：
- 勘察确认 payment 家族（支付订单/支付意图/回调/扫码/模拟支付/设备操作）被 charge 其余部分广泛引用，但家族自身除依赖 order/coupon/refund/eventoutbox（均已就位、方向正确）外无环，切口成立
- `git mv` 六文件入 `internal/central/payment/` 并改包名：`payment_intent.go`（PaymentIntentStore/支付意图生命周期）、`payment_http.go`（PaymentStartAPI/支付接口）、`development_payment.go`（DevelopmentPaymentAPI 模拟支付）、`payment_callback.go`（PaymentCallbackStore/回调入账/VerifiedPayment）、`scan.go`（ScanAPI 扫码）、`device_operation.go`（DeviceOperationReader/Store）；与记录 models.go、checkout.go、errors.go 合并为一包
- 自包引用去限定；`utcDate()`→`dbutil.UTCDate()`、`isMySQLDuplicate(`→`dbutil.IsMySQLDuplicate()`
- **order 哨兵独立（关键剪环）**：order 不得 import payment——`order.ErrChargeNumberConflict` 为 order 本包新哨兵（order_number.go 使用），card_http.go 的 409 判断加 `errors.Is(err, orderpkg.ErrChargeNumberConflict)`；payment 回调建订单走 payment→orderpkg（NewChargeOrderNumber/SyncOrderPaymentStatus/记录写入），方向合法
- 跨包导出：`ScanAPI.lookup` → `ScanAPI.Lookup`（charge 的 ScanPortLookup 适配器跨包注入需要），scan.go 内自调用与 payment_http.go 两处同步改写
- 调用方与接线：`charge/card_lookup.go` 改引 `payment.ScanAPI/ScanResult`；`charge/user_account.go` 的 `PrepayProvider` → `payment.PrepayProvider`；`cmd/central/main.go` 九处接线（ScanAPI/DeviceOperationStore/PrepayProvider/SimulationCallbackAPI/PaymentCallbackStore/DevelopmentPaymentAPI/WechatCallbackAPI/PaymentStartAPI/PaymentIntentStore）全部 → `payment.` 前缀
- 测试处置：四个集成测试（payment_callback / payment_intent / payment_http / recharge_callback）随迁改 `package payment`，新建 `payment/test_helper_test.go`（testGORMDB/openAccountDB/authenticatedRouter/callJSON/createTwoUsers/completeIntent 本地副本，注释说明与 charge 独立维护原因）；**development_payment_integration_test.go 留 charge**（使用 charge 的 accountRouter 接线）；留 charge 的 payment_number_integration_test.go、user_account_integration_test.go（81 行 payment.DevelopmentPaymentAPI/PaymentCallbackStore）、scheme_fixture_test.go（补 scanSession 助手）、development_payment_integration_test.go 同步加 qualification；worker 测试 `internal/worker/charge/paid_start_integration_test.go` 的 centralcharge 全部改 centralpayment
- 验收：`go build` / `go vet` / `gofmt` 全绿；`scripts/test/integration.sh` 29 包 ok（新增 payment 测试包）、0 FAIL

**步骤 9：refund 家族拆包 + start_result 收尾入 order ✅ 已完成（2026-10-02）**：
- `git mv` refund 家族两文件入 `internal/central/refund/`：`refund.go`→`executor.go`（RefundExecutor 自动退款派发：claim 租约/apply 入账）、`refund_http.go`→`http.go`（RefundAPI 内部派发端点），与记录 models.go 合并为一包；`walletSettlement` 映射随迁（族内私用）；models.go 包注释同步更新
- **剪环（实施期新发现）**：payment→refund 已单向（回调建退款记录），executor 原引用 payment 记录类型会成环——claim/apply 的 payment_order 行锁读与改写全部改 `paymentOrderRow` 行投影 + 裸表名（`refund 不得反向依赖 payment`，与 coupon/wallet 的跨家族最小事实模式同族）；charge_prepay 读改行投影
- **start_result.go 按 6c 策略收尾入 order**（`git mv` 至 `internal/central/order/`）：①`card.RefundInTx` 在 order 内复现为 `refundBalanceStartFailure`（wallet.Move + payment_order 裸表名改写 + 本包 SyncOrderPaymentStatus + refund_record 裸表名插入；order 不得 import card/payment/refund——三者的 order 依赖均已单向占用）；②card_charge/card_operation 读写改裸表名（行锁复用 end_result.go 的 `cardLocked` 先例）；③余额分支 payment_order 读改行投影
- **错误语义说明**：refundBalanceStartFailure 中 wallet.Move 错误原经 card.mapWalletMoveError 映射为 card 哨兵（同归 503），现直传原始错误，HTTP 行为不变；支付行校验冲突返回 ErrStartResultConflict（409）
- 调用方改写：`cmd/central/main.go`（StartResultAPI/StartResultStore→order、RefundAPI/RefundExecutor→refund）；`charge/user_account.go` 两处 `ErrRefundConflict`→`refund.ErrRefundConflict`（哨兵随执行器迁出）；`charge/billing_integration_test.go`/`development_payment_integration_test.go`（局部变量 refund 遮蔽，import 别名 refundpkg）、`admin/resources_integration_test.go` 的 RefundExecutor 同步 qualification
- 测试处置：`start_result_integration_test.go` 随迁 order 改 **external test 包**（`order_test`，复用 authorization_integration_test.go 的 testGORMDB，orderpkg 限定）；`start_refund_integration_test.go` 随迁 refund 改 `package refund`（internal——需要 `executor.apply` 内部断言；payment 记录引用改原生 SQL 以避开 internal 测试包 import payment 的环，testGORMDB 助手本包重建）；card 集成测试的 charge.StartResult/StartResultStore 改 orderpkg
- 收尾清理：charge `gorm_models.go` 薄包装文件删除（isMySQLDuplicate 无剩余使用方；EventOutboxRecord 早已归 eventoutbox 包）；charge 测试残留的 `utcDate()` 三处改 `dbutil.UTCDate()` 直用
- 验收：`go build` / `go vet` / `gofmt` 全绿；`scripts/test/integration.sh` 30 包 ok、0 FAIL

**步骤 10：user_account 回并 identity + admin 瘦身 ✅ 已完成（2026-10-02）**：

- **10a：手机号绑定回并 identity ✅（2026-10-02）**：新建 `internal/central/identity/phone.go`——`PhoneAPI`（bind/unbind 两路由 + `DevelopmentPhone`/`PhoneExchange` 字段从 charge.UserAccountAPI 整体迁入；user 表归 identity 家族）。`charge/user_account.go` 摘除两处理器、`errPhoneTaken` 哨兵、phone/mysql 两个 import；`cmd/central/main.go` 改接 `identity.PhoneAPI`（微信手机号授权回调随迁）；测试 accountRouterWithGateway 补注册 PhoneAPI（`DevelopmentPhone: true`）。钱包/券/发票/公告/站点/报修/反馈等跨家族视图留 charge（读视图不强制下沉）
- **10b1：admin 人工退款工作流写入下移 refund 家族 ✅（2026-10-02，P0-3 首例）**：新建 `internal/central/refund/review.go`——`ReviewStore`（`ManualCreate` 幂等建单 / `Review` 双人复核 / `Retry` 执行重投），`refund_review`/`refund_rejection`/`manual_refund_request`/`refund_record` 四表写入归属 refund 包；payment_order 经 `paymentOrderRow` 行投影（不反向依赖 payment）；**第二签实时身份核对**经注入回调 `LookupSigner` 回读 admin 授权域（角色须 customer_finance 且持 order.refund.review），保持"第二签核对前一审核人实时权限"约束；方法不自行开事务，由 admin `auditedTransaction` 供给（审计留 admin 域）。`admin/refunds.go` 瘦身为纯 HTTP 编排（输入校验/权限/审计/错误映射，`asConflict` 把 refund.ErrRefundConflict 映射为后台统一 409）；列表读视图留 admin
- **10b2：admin 发票双审写入下移 settlement 家族 ✅（2026-10-02）**：新建 `settlement/invoice_review.go`——`InvoiceReviewStore.Review`（`invoice_request`/`invoice_admin_review` 双审状态机：首签 awaiting_second、第二签核同款发票链接 + 经 `LookupReviewer` 回读实时身份后 approved 置 issued、单人驳回）；`ErrInvoiceConflict` 哨兵；`admin/invoices.go` 瘦身为编排（列表读视图留 admin），`asConflict` 扩展映射
- **10b3：admin 分账模板写入下移 settlement 家族 ✅（2026-10-02）**：新建 `settlement/split_template.go`——模板/参与方行类型、`CodePattern`、`ValidParties`、`SplitTemplateStore`（Create/Update/ReplaceParties）、`TemplateBound`/`RatiosValid`/`UsableTemplate` 守卫，`ErrSplitTemplateReferenced`/`ErrSplitParties` 哨兵；`admin/split_templates.go` 瘦身为编排（列表/详情/参与方读视图与脱敏回显留 admin）；站点改绑事务中模板可用性判定改 `settlement.UsableTemplate`（`admin/stations.go` 同步）
- **10b4：admin 提现生命周期写入下移 settlement 家族 ✅（2026-10-02，P0-3 收官）**：新建 `settlement/withdraw.go`——`WithdrawRow`、`WithdrawStore`（`AvailableCents`/`AvailableCentsExcluding` 余额视图、Create 幂等申请、Decide 审核、Pay 打款登记），`ErrWithdrawConflict` 哨兵；`admin/finance_ops.go` 三个处理器瘦身为编排；提现集成测试改用 `settlement.WithdrawStore`
- **P0-3 剩余边界说明**：`finance_reconcile_log`（对账结果）、`webhook_delivery_log`/`admin_event_outbox`（重发排队）是 admin 自有运营表，无跨所有权冲突，写入留 admin；admin 对 `settlement`/`settlement_party_amount`/`charge_bill`/`manual_fee_review` 仅剩导出与列表读视图——**13 张共享表的写入所有权全部归属家族包**
- **charge 包收官盘点**：家族拆完后 charge 剩 12 个生产文件，定位转为**跨家族编排层**——auto_stop / auto_stop_http（停机编排 + gateway HTTP）、status / user_query / user_stop（用户查询与主动停机编排）、scheme_view（计费方案视图）、live_meter（遥测视图）、feedback（故障上报）、user_account 剩余部分（钱包/券/发票/公告/站点的用户端视图与充值编排）、card_deadline / card_lookup（card 薄委托与扫码适配）；薄包装 gorm_models.go 已删。跨家族写入一律经家族包或裸表名局部投影，无反向依赖
- **第 6 批总验收**：`go build` / `go vet` / `gofmt` 全绿；`scripts/test/integration.sh` 全量 30 包 ok、0 FAIL。`central/charge` 从 69 文件 / 11187 行拆出 channel / coupon / wallet / order / payment / refund / settlement / card / identity(回并) 九个家族包，admin 15,829 行中的资金/结算写入全部下移家族包

> 验证口径说明：以上验证均为源码级与隔离集成测试；未做实机与真实资金验收。`integration.ps1` 的 PS 5.1 兼容问题未修，本次验证走 `scripts/test/integration.sh`。

> **全部批次收官（2026-10-02）**：第七部分第 1–6 批全部完成。P1 清单中未纳入批次范围、仍留作已知技术债的项：P1-1 服务令牌校验逐字复制（未收敛为共享助手）、P1-3 幂等机制多套并行（仅 LAST_INSERT_ID 等随家族包局部收敛）、P1-4 httpapi 错误码 1005 一码三义、P1-5 identity profile 复写鉴权链（P1-2 分页解析已随步骤 7 部分收敛为 `httpapi.ReadPaging`）。这些不影响本次拆包确立的包边界与表所有权，后续单独立项处理。
