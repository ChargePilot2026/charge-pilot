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

1. `refund_record` / `refund_success_receipt` / `payment_order`（资金，最高优先）→ central 新增 `POST /api/v1/internal/refunds/:refund_no/settle`；同时删掉 `result_consumer.go:12` 对 `central/charge` 的 import。
2. `charge_billing_cutoff`（含金额）→ 停机判定整体移回 central；worker 只发意图。
3. `alert_event` → central 新增告警写入端点；故障码语义从 `alerts/device.go:89-100` 移进 `protocol/dc589`（这是协议语义，本来就该在适配器）。**`worker/alerts` 包随之整体并入 `protocol/dc589`，不独立成包**。
4. `webhook_delivery_log` / `regulatory_report` → 配置与投递分离：配置写与状态推进都归 central，worker 只做签名与 HTTP 发送。
5. `gateway_db` 的 6 处 → 全部改走 gateway 已有 HTTP（`/api/v1/internal/*` 已存在对应能力）。

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

### 第 2 批：修 bug —— 🔶 部分完成

| Bug | 状态 |
|---|---|
| B7 告警跨库双事务 | ✅ 已修：`alerts` 改为先 central 写告警（event_id 去重幂等）、后 gateway 推进 `processed_at`，包注释写明顺序契约；两方向均不丢事件、不重复建告警 |
| B13 worker_db 环境变量 | ✅ 已修：worker 进程 `DATABASE_URL` → `DATABASE_URL_WORKER`；`compose.dev.yaml`、`docker-compose.yml`、`.env.example`、`check-deploy.mjs` 同步 |
| B17 错误码 1005 一码三义 | ✅ 已修：`httpapi/codes.go` 定义具名常量区段（1xxx 请求凭证 / 2xxx 业务冲突 / 5xxx 依赖故障）；409 冲突改用 2000/2009/2010 等，405 改用 1006；`schedule`、`regulatory` 等裸整数全部替换；前端无数值匹配无需改动 |
| B18 `198.18.0.0/15` 放行 | ✅ 已修：按 D3 关闭开口——该网段现按非公网目标拦截（`isPrivateAddress` 返回真），注释注明若开发需要应以配置项仅 dev profile 放行；测试已更新 |
| B19 三处 OnConflict 空更新 | ✅ 已修：`auto_stop.go`、`card_deadline.go` 改为先查回、冲突即返回错误进入 10s 重试；`meter_segments.go` 直接插入、主键冲突按错误上抛由 `SyncBatch` 记录重试 |
| B1/B2 DLQ 语义 | ⬜ 未动：失败先 XAck 导致消息永不重试、XPENDING 只拉前 100 条，待修 |
| B3 DLQ/ops 接线 | ⬜ 未动：`DLQ.Streams` 未设、`OpsAPI.Replay` 未赋值、ConsumeBatch 零生产调用，待修 |
| B5 worker HTTP Client 未接线 | ⬜ 未动：各组件 `Client` 字段未赋值导致连接池失效，待修 |
| B6 跨月重投重复入账 | ⬜ 未动：`isCommitted` 固定查当月分区 + 重复 `Where("tx_id = ?")`，待修 |

### 第 3–6 批

未开始。第 3 批注意 B14 包名与 openapi/前端同步；第 5 批前置 PR 0（gateway 证据批量端点）仍未做。

> 验证口径说明：以上验证均为源码级与隔离集成测试；未做实机与真实资金验收。`integration.ps1` 的 PS 5.1 兼容问题未修，本次验证走 `scripts/test/integration.sh`。
