# ChargePilot

ChargePilot 是单客户独立部署的二轮车充电运营平台。后端使用 Go，运营后台使用 React，用户端使用 Taro，同一份用户端源码构建 H5 与微信小程序。设备通过 DC589/TCP 接入。

每个客户独立运行应用、数据库和配置。当前功能包括扫码充电、钱包与在线卡、方案发布、计费与退款、分账台账、运营后台、报修、告警、导出、审计及外部投递。正式使用前须完成商户、实机和外部平台验收，见[功能边界](#功能边界)。

- [架构与目录](#架构与目录)
- [本地运行](#本地运行)
- [模拟器](#模拟器)
- [业务规则](#业务规则)
- [接口与权限](#接口与权限)
- [数据库](#数据库)
- [异步任务](#异步任务)
- [开发与验证](#开发与验证)
- [配置](#配置)
- [生产部署](#生产部署)
- [备份与恢复](#备份与恢复)
- [监控与故障定位](#监控与故障定位)
- [功能边界](#功能边界)

仓库维护规则见 [AGENTS.md](AGENTS.md)。设备协议完整规范见 [DC589 协议](protocols/dc589.md)。字段、索引和内置权限以初始化 SQL 为准；接口字段以注册路由、DTO 和测试为准。

## 架构与目录

| 进程 | 职责 | 数据库 | 监听 |
| --- | --- | --- | --- |
| central | 用户身份、后台、站点、订单、支付、钱包、计价、退款、分账与审计 | `central_db` | HTTP `8080` |
| gateway | 设备开通、TCP 会话、端口、命令、遥测、刷卡与结束报告 | `gateway_db` | HTTP `8083`、DC589/TCP `9100` |
| worker | 持久任务、Outbox、命令与结果同步、停机、计费、退款、告警和外部投递 | 三库，执行状态存于 `worker_db` | HTTP `8085` |
| migrate | Goose 初始化及版本检查 | 三库 | 无 |

central 中的 AdminDB、UserDB、BillingDB 是同一数据库句柄的领域名称。跨服务操作通过持久任务、幂等回执和内部 API 协作。Redis Cache 保存会话等缓存；Redis Stream 保存事件，两者独立部署。

```text
cmd/                 central、gateway、worker、migrate、devseed、simulator-dc589
internal/central/    用户、订单、支付、计价、财务与后台领域
internal/gateway/    协议适配器、设备控制、存储与模拟器
internal/worker/     任务、事件投递、补偿与调度
internal/platform/   配置、数据库、鉴权与 HTTP 基础设施
migrations/          三个 schema 的初始化 SQL
admin-web/           React 18 / TypeScript / Ant Design 5 / Vite 5
miniprogram/         Taro 4.3 / React 18 / TypeScript / Vite 4
protocols/           DC589 完整协议规范
docker/              服务镜像、Caddy 和 MySQL 初始化配置
scripts/dev/         开发启动、联调数据准备与模拟器
scripts/db/          备份、恢复与开发数据重建
scripts/test/        隔离 MySQL / Redis 集成测试
scripts/git/         Git hook 安装
scripts/ops/         生产部署配置检查
monitoring/          Prometheus 配置、告警规则与规则测试
```

## 本地运行

需要 Docker Compose v2。宿主机开发工具为 Go 1.27、Node.js 24、npm 和 Make；开发 Compose 已提供后端及前端运行环境。以下命令从仓库根目录执行。

```powershell
./scripts/dev/start.ps1
node scripts/dev/prepare.mjs
```

其他系统可直接运行 `docker compose -f compose.dev.yaml up -d`。首次启动安装前端依赖、编译后端并初始化空数据库；central 就绪后再运行准备脚本。`start.ps1` 保留已有数据。`prepare.mjs` 通过后台 API 补齐厂商、站点、两台设备和测试方案，不覆盖已有配置。

| 入口 | 本地地址 |
| --- | --- |
| 运营后台 | `http://127.0.0.1:5173/admin/` |
| 用户 H5 | `http://127.0.0.1:5174/` |
| central / gateway / worker 就绪检查 | 各 HTTP 端口的 `/health/ready` |
| MySQL | `127.0.0.1:3306` |
| Redis Cache / Stream | `127.0.0.1:6379` / `6380` |
| DC589 | `127.0.0.1:9100` |

开发后台账号默认为 `admin / ChangeMe!Admin2026`，H5 测试账号默认为 `tester-1`。后台引导只在账号表为空时执行，已有密码不会被重启覆盖。可通过 `ADMIN_BOOTSTRAP_USER`、`ADMIN_BOOTSTRAP_PASSWORD` 设置引导账号；准备脚本读取当前进程环境，不自动加载 `.env`，已有账号改密后应传入有效凭证。

开发环境配套使用 `LOGIN_MODE=development`、`PAYMENT_MODE=simulation` 和 `TARO_APP_LOGIN_MODE=development`。模拟支付仍经过正常订单、设备、结算和退款逻辑。Go 服务没有自动热重载，修改后执行：

```powershell
docker compose -f compose.dev.yaml up -d --no-deps --force-recreate central gateway worker
```

联调顺序：启动开发栈、准备数据、启动两个模拟器，后台确认设备在线；用户端选择端口和套餐并完成模拟支付，再调整模拟器功率、停止充电，核对订单、计量、费用、退款及钱包流水。`cmd/devseed` 可生成后台演示数据，`-clean` 只清理其 `demo_` 数据；它不能替代真实 TCP 联调。

## 模拟器

在两个终端分别执行：

```powershell
./scripts/dev/simulator.ps1 -Number 1
./scripts/dev/simulator.ps1 -Number 2
```

| 实例 | 设备编号 | 控制端口 | 状态文件 |
| --- | --- | --- | --- |
| 1 | `5348240514082652` | `9190` | `.tmp/simulator-1-5348240514082652.json` |
| 2 | `5348240514082653` | `9191` | `.tmp/simulator-2-5348240514082653.json` |

编号为 16 位十进制数字，须与后台 DC589 设备一致。默认两端口，`-Ports` 支持 1～20，须与后台端口数一致。各实例使用独立编号、控制端口和状态文件。

TUI 支持调功率、插拔、刷卡与移卡、余额查询、设备参数、故障、温度、电压、烟雾、校时、断线重连和重启。`Tab` 切页，`m` 打开操作菜单，方向键选择，`Enter` 提交，`Esc` 返回，`q` 退出。卡持续贴住只触发一次刷卡事件，再次刷卡前需移卡。状态文件保存进行中的充电和待确认报告，支持进程重启恢复。

无界面运行及控制示例：

```powershell
go run ./cmd/simulator-dc589 -board-id 5348240514082652 -ports 2 -tui=false -control 127.0.0.1:9190 -state-file .tmp/simulator-headless.json
Invoke-RestMethod http://127.0.0.1:9190/state
Invoke-RestMethod http://127.0.0.1:9190/events -Method Post -ContentType application/json -Body '{"type":"power","port":1,"power_deciwatts":2500}'
```

控制接口只允许 loopback 监听。`power_deciwatts=2500` 表示 250 W；刷卡使用 `{"type":"card","port":1,"card":100001}`，移卡使用 `{"type":"remove-card","port":1}`。完整事件字段见 [Input](internal/gateway/simulator/dc589/behavior.go)，场景和参数见 `go run ./cmd/simulator-dc589 -h`。

充电心跳 15 秒，空闲心跳 60 秒，网关读超时为相应周期的三倍。模拟器每秒更新计量。当前使用通用五档参数，不支持特殊双口八档扩展、心跳分片合并和 OTA；完整协议规范仍保留这些内容。

## 业务规则

### 方案与计量

后台发布完整充电方案，模板应用到站点或设备时复制配置。设备独立配置整体覆盖站点配置。付款意图冻结方案、套餐和费率，订单使用该快照；后续改价不影响历史订单。能力由所选协议统一声明，当前 DC589 支持时长、电量、在线卡、满充停止、累计电量和分段功率，最长 4320 分钟；未知协议不能发布方案或新下单。

| 购买模式 | 算法 | 单位与规则 |
| --- | --- | --- |
| 金额 | `server_max_power` | 电费、服务费为分/小时；按北京时间自然日、费率时段内最高功率档计费 |
| 金额 | `server_realtime_power` | 分/小时；按各实际功率片段所在档计费 |
| 金额 | `server_energy` | 分/kWh；按实测电量计费，跨费率须有边界计量证据 |
| 时长 | 固定时长套餐 | 购买分钟数，按实际完整分钟折算未用金额 |
| 电量 | 固定电量套餐 | 整数 1～65 kWh，售价等于度数乘以电费及服务费单价之和 |

套餐 ID 为 1～99 且唯一，每个启用模式必须配置套餐。用户只能选择服务端生成的套餐。金额模式及刷卡累计保护时长默认 600 分钟，可配置 60～4320 分钟；时长套餐为 1～4320 分钟。损耗系数固定为 0，渠道系数固定为 10000 基点。

金额以整数分保存，费率和中间计算使用十进制。服务器金额模式分别累计电费、服务费后四舍五入到分；设备套餐消耗向下取整到分，分项尾差按既有算法协调。`1 kWh = 1000 Wh`，`1 Wh = 1000 mWh = 3600 W·s`。

完整分钟按整次使用计算，再分配到费率边界；不能逐片段向下取整。最大功率算法只回溯同一天、同一费率时段，不回溯其他时段。免费分钟与最低电费只作用于金额模式：免费范围内或无实际使用不收最低电费，超过免费条件后按完整使用计费。跨费率电量缺少实测边界、计量矛盾或证据不足时进入人工计量复核，不按时间均分或插值补造最终读数。

| 例子 | 结果 |
| --- | --- |
| 实时功率：低档电费/服务费 80/40 分每小时用 20 分钟，高档 120/60 分每小时用 40 分钟 | 电费 107 分、服务费 53 分，共 160 分；付款 300 分退 140 分 |
| 最大功率：同一时段低档升为上述高档，共 60 分钟 | 按高档收 180 分；付款 300 分退 120 分 |
| 300 分购买 120 分钟，实际 61 分 30 秒 | 收 152 分，退 148 分 |
| 电费/服务费 80/20 分每度，购买 3 度，实际 1375 Wh | 电费 109 分、服务费 28 分，共 137 分，退 163 分 |

计算实现和边界示例见 [pricing](internal/central/pricing) 与 [scheme_examples_test.go](internal/central/pricing/scheme_examples_test.go)。旧兼容计价枚举不表示新方案支持相应模式。

### 支付、启动与结束

扫码发现、端口和套餐查询可匿名访问。下单提交 `client_request_id` UUID、`port_id`、`offer_id`，价格由服务器确定。支付前只保存冻结意图；可信付款回调确认后创建充电订单。相同请求与内容返回原结果，同号不同内容返回冲突。

二维码支持设备码、端口码或带单个 `code` 参数的 HTTPS 链接。裸码为 1～64 位字母、数字、下划线、冒号或连字符；链接禁止用户信息和 fragment。设备码需要继续选择端口，端口码直接定位插口。

微信回调先验签、解密，核对商户、AppID、用户、商户单号、渠道交易号和金额，再同事务写入支付、订单、端口占用及 Outbox。过期付款退款，不启动设备。客户端支付成功不代替渠道回调。

worker 读取持久付款任务，gateway 先保存命令再下发；设备明确回执后推进启动结果。启动超时或回执丢失表示结果未知，保留占用并查询、重试或补偿，不能据此全退或再次启动。设备明确拒绝才按失败处理。

设备结束事件先落库并提交 central；central 保存结束回执、最终计量和计费任务后，gateway 才释放对应占用。首次预算、最长时长、购买时长或用户停止确定计费截止点，迟到的物理停机不增加截止点后的费用。最终实收不超过已付可用额度。实时费用用于展示，超过 30 秒未更新标记为 stale，不作为最终计费证据。

设备 `enabled/disabled` 与 TCP 在线状态独立。禁用阻止新扫码付款、新刷卡启动及加时，已有订单仍可停止、结算和退款。

自动停止还检测持续至少 1 分钟的端口零功率，最新观测须在 30 秒内；缺失目标端口或重新出现正功率会重置判断。离线、缺失读数不能作为拔出或零功率证据。

| 状态轴 | 取值及含义 |
| --- | --- |
| `charge_order.status` | `pending_payment/paid/charging/completed/cancelled/failed/refunding/refunded`，内部生命周期 |
| `business_status` | `pending_start/charging/completed`，由订单状态生成 |
| `payment_status` | `pending/paid/partial_refunded/refunded`，按实际到账及成功退款更新 |
| `payment_order.status` | `initiated/paid/closed/refunded/partial_refunded/failed` |
| `refund_record.status` | `pending/processing/success/failed/rejected`；`execution_policy` 独立决定人工审核或自动执行 |

充电结束、费用结算、退款申请与退款到账是不同事实。`processing` 不表示退款成功。

### 在线卡与钱包

卡号为非零 uint32，规范化为十进制字符串。运营人员核实归属后绑定，同一用户可绑定多卡；用户只能挂失或解绑自己的卡。卡使用钱包余额与方案中的时长套餐，不能作为月卡或年卡。

首次刷卡同事务扣钱包、生成支付单和订单、冻结方案、保存卡操作及业务事件记录。充电中同卡重刷沿用原快照，原子追加付款和分钟数，不下发新的 B7。异卡占用、余额不足、启动未确认、已到截止点或整次追加超保护上限时拒绝，不部分扣款。

DC589 原生刷卡报文没有事件序号。gateway 为每次新报文生成 UUID，投递重试复用该 UUID；已完成操作返回原结果，新事件超过 30 秒不再扣款。实机须保证一次放卡只报告一次，移开重刷才产生新事件。设备余额显示使用 0.1 元并向下取整，钱包始终按分记账。

钱包可用金额为 `balance_cents - frozen_cents`。钱包退款按原充值支付单拆分，预留余额及各支付单可退额度。成功才扣减和释放，明确失败释放，渠道结果未知保持预留。5 分钟内第三次钱包退款请求触发频次风控；审核、解冻权限独立，解除一项冻结不自动解除其他冻结。

### 财务与审核

未用差额按原支付来源返还。累计成功退款与待执行预留共同占用可退额度；渠道超时继续查询同一 `refund_no`，不换号重提。退款成功回执、支付累计已退金额和订单资金状态同事务更新。

人工订单退款限已结束、微信付款且有可退额度的订单。人工退款、计量分段修订和发票通过采用不同人员复核；首签冻结业务内容，第二签核对首签人的实时资格及快照。驳回保存理由，重试权限不能绕过人工审核。分段修订保留原总量及冻结来源；缺失计费截止证据不能用物理结束片段替代。人工最终金额结算由有效财务人员按现有权限执行，受实付上限、原因及审计约束。

分账模板为 2～8 方，比例合计 10000 基点。`mode_a` 分配电费和服务费，`mode_b` 只分配服务费；各方合计必须等于分账池。已绑定模板不能修改比例，有交易历史的站点不能改绑。提现额度扣除待审及已审占用；当前提供申请、审核和人工打款登记。

普通账单由权威计费回执生成；发票申请金额取该回执，同一订单仅允许一份有效申请。对账比较外部输入记录与内部台账；导出使用固定资源白名单生成 CSV、XLSX、PDF，限制查询范围、行数和有效期，下载时重新鉴权。

## 接口与权限

普通 JSON 响应为 `code/message/data/request_id/trace_id`，成功 `code=0`，HTTP 状态同时表达结果。`X-Request-ID` 允许 1～128 位字母、数字、下划线或连字符，无效或缺失则生成；响应 trace 与 request 使用同值。微信回调、OpenAPI、文件下载和指标按各自格式响应。

金额为整数分，计量字段注明单位。用户 Snowflake ID 使用十进制字符串，前端不得转成 JavaScript `Number`。数据库时间按 UTC 解析，计费日界和 DC589 本地时间按北京时间。列表常用 `page/page_size`，默认 1/20，上限 100；过程查询另有游标、时间窗及行数限制。

用户与管理员使用不同 kind 的 JWT，访问令牌期限 15 分钟，刷新令牌在 Redis 中轮换。用户操作校验 JWT、Redis 会话及有效用户；后台另查 auth_version 和实时角色权限。后台口令使用 bcrypt，可启用 TOTP；后台登录、MFA、刷新按 IP 限速，Redis 不可用时拒绝相关请求。菜单权限只影响展示。服务间接口使用 `X-Service-Token`。

用户匿名入口为 `/scan/resolve`、`/scan/port`、`/scan/offers`、`/station/nearby`、`/station/:id`、`/announcement/list`，均位于 `/api/v1/user`。订单、钱包、卡、手机号和其他用户操作校验当前会话与资源归属。

手机号保存在 `central_db.user.phone` 明文列，未绑定为 `NULL`，唯一索引限制号码归属。正式绑定使用微信授权 code，开发模式允许测试号码。绑定响应返回脱敏号码；后台 `charge_user.read` 可读取完整号码并按号码或片段检索，当前没有独立敏感字段权限。后台站点、设备等资源有数据范围约束；订单和支付查询尚无站点或厂商范围过滤。

### 接口导航

后台 OpenAPI 为 `GET /api/docs/admin.openapi.json`，源文件是 [openapi.json](internal/central/admin/openapi.json)，覆盖部分现行后台接口。完整注册路径及字段以以下入口和主程序实际装配为准。

| 领域 | 路径或实现入口 |
| --- | --- |
| 用户身份 | [identity/http.go](internal/central/identity/http.go) |
| 扫码与支付 | `/api/v1/user/scan/*`；[scan.go](internal/central/charge/scan.go)、[payment_http.go](internal/central/charge/payment_http.go) |
| 充电状态、历史、停止 | `/api/v1/user/charge/*`；[user_query.go](internal/central/charge/user_query.go)、[user_stop.go](internal/central/charge/user_stop.go) |
| 钱包、手机号、站点、发票与报修 | [user_account.go](internal/central/charge/user_account.go) |
| 在线卡、优惠券、账单与欠费 | [charge](internal/central/charge) 的 `card_http.go/coupon.go/bill.go/debt_http.go` |
| 后台身份、账号、角色与数据范围 | [admin](internal/central/admin) 的 `http.go/admin_users.go/roles.go/data_scope.go` |
| 站点、设备、订单、支付与厂商 | `resources.go/device_imports.go/vendors.go` |
| 方案模板与在线卡 | `charging_schemes.go/online_cards.go` |
| 财务 | `finance_ops.go/refunds.go/wallet_risks.go/meter_reviews.go/invoices.go/split_templates.go/exports.go` |
| 用户运营、报修与审计 | `charge_users.go/coupons.go/coupon_activity.go/casework.go/audit_log.go/dashboard.go/operations.go` |
| central 内部 | [charge](internal/central/charge)、[billing/service.go](internal/central/billing/service.go)、[regulatory/http.go](internal/regulatory/http.go) |
| gateway 内部 | [provision](internal/gateway/provision)、[control](internal/gateway/control) |
| worker 内部 | [internaljob/ops.go](internal/worker/internaljob/ops.go)、[schedule/http.go](internal/worker/schedule/http.go) |

### Webhook

订阅地址要求 HTTPS，校验 DNS 和目标 IP，拒绝 loopback、私网、元数据地址及 URL 用户信息。签名为 `HMAC-SHA256(secret, timestamp + "." + raw_body)`。

| Header | 值 |
| --- | --- |
| `X-ChargePilot-Timestamp` | 十进制 Unix 秒时间戳 |
| `X-ChargePilot-Signature` | `sha256=` 加签名十六进制值 |
| `X-ChargePilot-Event` | 事件类型 |
| `X-ChargePilot-Event-Id` | 稳定事件 ID |

接收方校验原始请求体签名和时间窗，按事件 ID 去重。投递日志、重试状态和事件发布分别记录；当前消费及游标限制见[异步任务](#异步任务)。

## 数据库

| Schema | 业务表数 | 初始化定义 |
| --- | --- | --- |
| `central_db` | 85 | [0001_init.sql](migrations/central_db/0001_init.sql) |
| `gateway_db` | 15 | [0001_init.sql](migrations/gateway_db/0001_init.sql) |
| `worker_db` | 5 | [0001_init.sql](migrations/worker_db/0001_init.sql) |

另有各库的 Goose 版本表。表和字段 COMMENT 描述职责、单位、状态、快照及幂等用途。部分表按月分区，查询、更新及唯一性检查须使用实际联合键。表存在不表示对应功能已在主程序接线。

初始化空库需配置 `DATABASE_URL_GATEWAY`、`DATABASE_URL_CENTRAL`、`DATABASE_URL_WORKER`，然后执行：

```sh
go run ./cmd/migrate -schema all
```

MySQL init 脚本仅在空数据卷创建三库和授权，表由 Goose 初始化。项目尚未发布，各 schema 仅保留 `0001_init.sql`，结构调整直接修改定义，不新增发布前递增迁移，也不自动建表。已执行版本不会因文件修改重新应用；既有数据需要兼容迁移，不能只删除 Goose 版本表。MySQL DDL 无事务回滚。

明确可丢弃的开发数据可重建：

```powershell
./scripts/db/reset-dev.ps1 -ResetDevelopmentData
node scripts/dev/prepare.mjs
```

该脚本核验本地项目、停止后端、先备份到 `.tmp/`，再重建三库并清空本项目两套 Redis，会清除业务、会话及事件。需要保留数据时不得执行。

<details>
<summary>全部业务表索引（105 张）</summary>

| 数据库 | 表 | 职责 |
| --- | --- | --- |
| central_db | `active_port_charge` | 端口当前充电占用(跨月唯一性兜底) |
| central_db | `card_charge` | 在线卡充电会话与累计购买时长 |
| central_db | `card_operation` | 在线卡刷卡操作幂等结果和扣款记录 |
| central_db | `charge_bill` | 充电账单 |
| central_db | `charge_bill_read` | 账单已读标记 |
| central_db | `charge_billing_cutoff` | 订单首次计费截止点 |
| central_db | `charge_billing_job` | 待结算订单任务及重试状态 |
| central_db | `charge_debt` | 充电欠费 |
| central_db | `charge_debt_receipt` | 欠费补缴入账回执 |
| central_db | `charge_end_receipt` | 设备结束事件的幂等回执 |
| central_db | `charge_event_log` | 订单状态事件及时间线 |
| central_db | `charge_fee_receipt` | 计费消费事件幂等回执 |
| central_db | `charge_manual_settlement` | 无法自动计量时的人工最终结算与审计 |
| central_db | `charge_meter_review` | 充电计量异常核对 |
| central_db | `charge_order` | 充电订单(内部生命周期及独立业务、支付状态) |
| central_db | `charge_order_pricing` | 订单冻结的完整方案与计算快照 |
| central_db | `charge_payment_intent` | 支付前冻结方案及端口预占 |
| central_db | `charge_port_lock` | 支付与在线卡共享的端口事务锁 |
| central_db | `charge_prepay` | 预付支付确认结果 |
| central_db | `charge_start_receipt` | 设备启动确认和幂等摘要 |
| central_db | `coupon` | 优惠券模板 |
| central_db | `coupon_activity_rule` | 优惠券活动规则及版本 |
| central_db | `coupon_grant` | 优惠券发放记录 |
| central_db | `coupon_grant_request` | 优惠券发放请求幂等回执 |
| central_db | `coupon_redemption` | 优惠券核销记录 |
| central_db | `device_fault_report` | 设备报修 |
| central_db | `device_fault_report_event` | 设备报修状态与巡检处理记录 |
| central_db | `event_outbox` | 事件 outbox(可靠发布) |
| central_db | `feedback` | 评价/投诉 |
| central_db | `invoice_admin_review` | 发票审核记录 |
| central_db | `invoice_request` | 发票申请 |
| central_db | `manual_refund_request` | 人工退款申请及审核 |
| central_db | `online_card` | 用户在线卡绑定和挂失状态 |
| central_db | `online_card_audit` | 在线卡操作审计 |
| central_db | `payment_callback_idempotent` | 微信支付回调幂等 |
| central_db | `payment_order` | 支付订单(支持 charge / wallet_recharge) |
| central_db | `refund_record` | 退款记录 |
| central_db | `refund_rejection` | 退款拒绝原因记录 |
| central_db | `refund_review` | 退款审核过程 |
| central_db | `refund_success_receipt` | 退款成功的幂等确认 |
| central_db | `risk_freeze_log` | 风控冻结记录(本期仅频次触发) |
| central_db | `snowflake_state` | Snowflake 编号分配状态，业务事务内行锁串行分配 |
| central_db | `charge_debt_payment_request` | 欠费支付请求幂等回执 |
| central_db | `user` | 终端用户 |
| central_db | `user_login_identity` | 用户登录身份关联 |
| central_db | `wallet_account` | 用户一对一钱包账户 |
| central_db | `wallet_recharge_request` | 钱包充值请求幂等键 |
| central_db | `wallet_refund_part` | 钱包退款按原支付渠道拆分明细 |
| central_db | `wallet_refund_request` | 钱包退款请求及处理结果 |
| central_db | `wallet_risk_freeze_link` | 钱包风控冻结关联 |
| central_db | `wallet_risk_release` | 钱包风控解除记录 |
| central_db | `wallet_risk_review` | 钱包风控审核记录 |
| central_db | `wallet_txn` | 钱包流水 |
| central_db | `admin_data_scope` | 后台账号数据范围 |
| central_db | `admin_field_mask` | 角色字段脱敏规则 |
| central_db | `admin_user_role` | 管理员账号 |
| central_db | `alert_event` | 告警事件 |
| central_db | `announcement` | 公告 |
| central_db | `audit_log` | 审计日志 |
| central_db | `device_import` | 设备导入请求及执行重试状态 |
| central_db | `device_import_identity` | 设备导入时的请求身份和参数校验快照 |
| central_db | `device_meta` | 设备元数据(冗余自 gateway_db) |
| central_db | `admin_event_outbox` | 事件 outbox(可靠发布) |
| central_db | `export_task` | 导出任务 |
| central_db | `finance_reconcile_log` | 财务对账日志 |
| central_db | `permission` | 权限码 |
| central_db | `pricing_publication` | 计费规则发布版本和请求幂等摘要 |
| central_db | `pricing_rule` | 计费规则 |
| central_db | `pricing_template` | 计费模板 |
| central_db | `regulatory_report` | 监管报送持久队列 |
| central_db | `role` | 角色 |
| central_db | `role_permission` | 角色-权限映射 |
| central_db | `split_party` | 分账参与方 |
| central_db | `split_template` | 分账模板 |
| central_db | `station` | 充电站点 |
| central_db | `webhook_delivery_log` | Webhook 投递日志 |
| central_db | `webhook_subscription` | Webhook 订阅 |
| central_db | `whitelabel_config` | 白标配置 |
| central_db | `fee_calculation` | 计费明细 |
| central_db | `fee_delivery` | 计费结果投递任务 |
| central_db | `fee_receipt` | 计费事件幂等回执 |
| central_db | `manual_fee_review` | 人工定价兜底单 |
| central_db | `settlement` | 分账汇总 |
| central_db | `settlement_party_amount` | 分账参与方金额 |
| central_db | `withdraw_request` | 提现申请 |
| gateway_db | `card_event_delivery` | 原生刷卡事件投递与重试 |
| gateway_db | `charge_command` | 设备启动命令和确认状态 |
| gateway_db | `charge_end_delivery` | 设备结束事件投递与重试 |
| gateway_db | `charge_process` | 已确认充电订单的 A4 心跳过程记录，无自动保留期清理 |
| gateway_db | `charge_stop_command` | 设备停止命令和确认状态 |
| gateway_db | `device` | 设备元数据 |
| gateway_db | `device_event` | 设备事件及原始上下文 |
| gateway_db | `device_port` | 设备端口(每端口独立二维码) |
| gateway_db | `device_provision` | 设备开通请求幂等记录 |
| gateway_db | `device_session` | 设备长连接会话 |
| gateway_db | `event_outbox` | 与业务事务一起写入的待投递事件 |
| gateway_db | `telemetry` | 设备遥测原始记录，按 ts 分区 |
| gateway_db | `telemetry_aggregate_15min` | 遥测 15 分钟聚合 |
| gateway_db | `telemetry_aggregate_hourly` | 遥测小时聚合 |
| gateway_db | `vendor` | 硬件厂适配器注册表 |
| worker_db | `comp_tx_log` | 跨服务补偿事务 |
| worker_db | `dlq_log` | 死信队列日志 |
| worker_db | `dlq_replay_cursor` | DLQ 重放滑动游标 |
| worker_db | `scheduled_task` | 定时任务定义 |
| worker_db | `task_execution_log` | 定时任务执行日志 |

</details>

## 异步任务

业务状态与自身 Outbox 同事务提交。Redis 写入成功而数据库发布标记未提交时会重复发布，消费者按稳定事件 ID 和业务回执去重。付款后启动、设备结束、计费和退款以数据库任务为依据，不依赖通知流推进。

| 周期 | 任务 |
| --- | --- |
| worker 每秒 | 设备告警、刷卡投递、付款后启动、启动/结束结果同步、Outbox 发布 |
| worker 每 10 秒 | 自动停机、计费、退款、退款结果消费、可选监管投递 |
| worker 调度器每秒扫描 | 当前白名单只有 `webhook_dispatch`；数据库记录 cron、租约和执行结果 |
| gateway 每 5 秒 | 用户停止和补偿停止重试 |
| central 每小时 | 到期导出清理 |

| Stream | 当前用途 |
| --- | --- |
| `charge_payment_confirmed_stream` | 付款确认的 Webhook 候选流，启动另读持久任务 |
| `charge_started_stream` | 明确启动的 Webhook 候选流 |
| `charge_start_rejected_stream` | 明确拒绝的 Webhook 候选流，退款另读数据库 |
| `charge_ended_stream` | 结束的 Webhook 候选流，计费另读 `charge_billing_job` |
| `charge_settled_stream` | 最终结算通知，当前无消费者且不在 Webhook 默认流中 |
| `refund_required_stream` | 退款需求的 Webhook 候选流，执行另扫 `refund_record` |
| `refund_succeeded_stream` | 已成功退款，兼容结果消费者及 Webhook 候选流 |
| `refund_result_stream` | 兼容输入，当前无生产者 |
| `wallet_recharge_settled_stream` | 充值通知，当前无消费者，到账已在回调事务完成 |
| `device_event_stream` | 设备 Webhook 候选流，告警另扫网关记录 |
| `charge_events_stream` | 后台指定 Webhook 事件重投 |

当前 Webhook 使用 `XRANGE` 扫描各默认流前 100 条，没有持续推进游标；缺少 `event_type/data` 格式的事件会跳过，因此不能保证全部事件持续投递。退款结果消费者只读新消息 `>`，未接入 pending 自动回收。通用 DLQ 组件有实现，worker 主程序未启用 `ConsumeBatch`、未配置 Streams 和 Replay 回调；DLQ 重放接口返回 503，积压映射为空。恢复操作前须检查实际任务、消费组及持久回执，不能据组件测试推定运行恢复完整。

## 开发与验证

```powershell
./scripts/git/install-hooks.ps1
make check
./scripts/test/integration.ps1
```

```bash
sh scripts/git/install-hooks.sh
make check
bash scripts/test/integration.sh
bash scripts/test/integration.sh -race
```

`make fmt/lint/test` 分别直接调用 `go fmt ./...`、`go vet ./...`、`go test ./...`，`make check` 顺序执行三项。格式化后重新暂存。pre-commit 执行 `make lint test`，要求本机有 Go 和 Make。Windows 没有 Make 时可在容器执行 `docker compose -f compose.dev.yaml run --rm --no-deps -T central make check`，或直接运行对应 Go 命令。

普通测试未配置数据库时会跳过集成测试。隔离脚本创建临时 MySQL/Redis、初始化三库、运行全量 Go 测试并清理容器；PowerShell 版本需要已有 `chargepilot-dev_default` 网络，Bash 版本使用随机 loopback 端口。事务、资金、唯一性和消息状态变更须运行隔离测试；并发变更按需启用 race。

| 目录 | 安装与开发 | 检查与构建 |
| --- | --- | --- |
| `admin-web` | `npm ci`、`npm run dev` | `npm test`、`npm run lint`、`npm run build` |
| `miniprogram` | `npm ci`、`npm run dev:h5` 或 `npm run dev:weapp` | `npm run typecheck`、`npm test`、`npm run build:h5`、`npm run build:weapp` |

用户端 `.npmrc` 已设置依赖兼容选项。后台 build 包含 TypeScript 检查，lint 只做 TypeScript 检查。产物为 `admin-web/dist`、`miniprogram/dist/h5` 和 `miniprogram/dist/weapp`，不提交。微信开发工具导入 `dist/weapp` 并配置真实 AppID；构建本身不需要 AppID。H5 使用 hash 路由。

CI 当前检查 Go 格式差异、vet、`go mod tidy -diff`、单元测试、四个服务命令构建及后台构建；没有运行隔离集成测试、后台测试和用户端检查。验证结论须注明实际覆盖范围。

## 配置

以 [config.go](internal/platform/config/config.go)、Compose 和前端构建配置为准。开发 Compose 提供本地值；生产从 `.env` 和显式环境传入。密钥、私钥和真实配置不提交。

| 变量 | 使用方及规则 |
| --- | --- |
| `DATABASE_URL` | gateway / worker 各自的 MySQL DSN |
| `DATABASE_URL_CENTRAL` | central / worker，central schema |
| `DATABASE_URL_GATEWAY` | worker，gateway schema |
| `DATABASE_URL_WORKER` | migrate，worker schema；migrate 同时读取另两库 URL |
| `CENTRAL_HTTP_ADDR`、`WORKER_HTTP_ADDR` | central / worker，默认 `:8080`、`:8085` |
| `GATEWAY_HTTP_ADDR`、`DC589_ADDR` | gateway，默认 `:8083`、`:9100` |
| `REDIS_CACHE_URL`、`REDIS_STREAM_URL` | central 连接 Cache，Stream URL 仍为其必填配置；worker 连接 Stream 并发布 Outbox |
| `JWT_SECRET` | central，至少 32 字节 |
| `SERVICE_TOKEN` | 三服务内部 API 共享令牌 |
| `GATEWAY_INTERNAL_URL`、`CENTRAL_INTERNAL_URL` | 服务间地址，按 Compose 服务名与内网端口配置 |
| `DEVICE_MAX_CONNECTIONS` | gateway，正整数，默认 10000 |
| `GATEWAY_DEBUG_HEARTBEAT` | gateway 心跳日志，默认 false；开发 Compose 固定 true |
| `ADMIN_BOOTSTRAP_USER`、`ADMIN_BOOTSTRAP_PASSWORD` | 空后台账号表的引导凭证 |
| `LOGIN_MODE` | central，默认 `wechat`；`development` 要求模拟支付 |
| `WECHAT_APPID`、`WECHAT_SECRET` | central 微信应用配置，必填 |
| `PAYMENT_MODE` | `disabled/simulation/wechat_direct`，默认 disabled |
| `WECHAT_MCH_ID`、`WECHAT_CERT_SERIAL`、`WECHAT_APIV3_KEY` | 微信直连商户 ID、证书序列号及 32 字节 API v3 密钥 |
| `WECHAT_PRIVATE_KEY_PATH`、`WECHAT_NOTIFY_URL` | central 容器内私钥路径及外部 HTTPS 支付回调地址 |
| `REGULATORY_MODE` | worker，`disabled/simulation/http` |
| `REGULATORY_ENDPOINT`、`REGULATORY_SIGNING_SECRET` | http 监管发送配置，签名密钥至少 16 字节 |
| `EXPORT_DIR` | central 导出目录，生产需持久卷 |
| `VITE_API_BASE` | 后台 API 地址，构建时使用 |
| `TARO_APP_API_BASE` | 用户 API 前缀，默认 `/api/v1`；微信发布需绝对 HTTPS 地址 |
| `TARO_APP_LOGIN_MODE` | 用户构建登录模式，默认 wechat |
| `DEV_API_PROXY_TARGET` | 前端开发代理，指向 central |

`.env.example` 还含未被当前实现或根 Compose 消费的 `TAG`、`ADMIN_BOOTSTRAP_PHONE`、`WECHAT_REFUND_NOTIFY_URL`、`WECHAT_CUSTOMER_SERVICE_CORP_ID`、`OTEL_EXPORTER_OTLP_ENDPOINT`、`DEFAULT_TIME_OF_USE_JSON`，修改这些值不会启用相应功能。

## 生产部署

根 [docker-compose.yml](docker-compose.yml) 定义生产拓扑。复制 `.env.example` 为 `.env` 并填写 `MYSQL_ROOT_PASSWORD/DB_PASSWORD/REDIS_PASSWORD/REDIS_STREAM_PASSWORD/JWT_SECRET/SERVICE_TOKEN/WECHAT_APPID/WECHAT_SECRET/PUBLIC_DOMAIN`。数据库和 Redis 仅内网访问，Cache 为 allkeys-lru，Stream 为 noeviction 且启用 AOF。镜像使用非 root distroless；MySQL、事件 Redis、导出和 Caddy 状态使用持久卷。

Caddy 公网暴露 `80/443`，`/api/*` 代理 central，`/admin/*` 提供后台静态文件，根路径转向后台；`/api/v1/internal/*` 返回 404。gateway 另暴露 `9100/TCP`，gateway/worker HTTP 不公开。后台静态产物必须先构建，H5 和小程序发布另行配置。

```bash
node scripts/ops/check-deploy.mjs
docker compose -f docker-compose.yml up -d --build
docker compose -f docker-compose.yml ps
docker compose -f docker-compose.yml logs --tail=100 chargepilot-central chargepilot-gateway chargepilot-worker chargepilot-caddy
```

检查脚本验证拓扑、镜像、挂载、公开端口、三库 URL 及 schema、Redis 策略、模板凭证和 Caddy 配置，不启动生产栈，也不检查数据库连通性；首次可能拉取 Caddy 镜像。`--config-only` 只跳过模板凭证值检查，仍要求 Compose 必填变量及挂载路径。应用容器当前没有 Compose healthcheck，应主动检查 `/health/ready`。

### 微信直连支付

根 Compose 没有向 central 传入支付模式和商户参数，支付默认 disabled；只修改 `.env` 不能启用。部署时增加受控 override，例如：

```yaml
services:
  chargepilot-central:
    environment:
      PAYMENT_MODE: wechat_direct
      WECHAT_MCH_ID: ${WECHAT_MCH_ID:?required}
      WECHAT_CERT_SERIAL: ${WECHAT_CERT_SERIAL:?required}
      WECHAT_APIV3_KEY: ${WECHAT_APIV3_KEY:?required}
      WECHAT_PRIVATE_KEY_PATH: /run/secrets/wechat-merchant.pem
      WECHAT_NOTIFY_URL: https://${PUBLIC_DOMAIN}/api/v1/public/payments/wechat/callback
    volumes:
      - /etc/chargepilot/wechat-merchant.pem:/run/secrets/wechat-merchant.pem:ro
```

保存部署侧文件为 `compose.payment.yaml` 后，使用 `docker compose -f docker-compose.yml -f compose.payment.yaml config --quiet` 检查合并配置，启动及后续维护也使用这两个 `-f` 文件。`check-deploy.mjs` 只检查根生产 Compose。商户私钥只读挂载，文件权限须允许容器非 root UID 65532 读取；API v3 密钥为 32 字节。worker 通过 central 的同一支付提供方执行退款和查询，不持有商户私钥。正式用户端须使用真实 AppID、已配置的 API 域名和微信发布环境。

### 上线验收

验证公网 HTTPS、内部 API 隔离、真实登录和手机号授权、真实商户支付及重复回调、原路退款、实机启动及未知结果、停机、断线恢复、刷卡重复行为、最终计量、权限、审计、故障告警、外部投递及备份恢复。模拟链路不能代替真实资金或实机验证。

监管默认 disabled；http 模式只表示具备发送适配器，目标监管平台的字段、签名、认证和响应契约仍需实际对接。仓库没有外部合规认证或可据此保证的 SLA、RPO、RTO。

## 备份与恢复

[backup.sh](scripts/db/backup.sh) 和 [restore.sh](scripts/db/restore.sh) 依赖 Bash、gzip 与 MySQL 客户端。输出目录须预先存在；容器客户端回退需设置 `MYSQL_CLIENT_CONTAINER`。连接参数为 `MYSQL_HOST/MYSQL_PORT/MYSQL_USER`；原生客户端用 `MYSQL_PWD`，容器回退从 `MYSQL_PASSWORD` 传入密码。凭证从安全环境加载，避免写入命令历史。

```bash
mkdir -p .tmp/backups
bash scripts/db/backup.sh .tmp/backups
```

备份包含三库的 SQL、例程、触发器、事件及 Goose 历史，逐库使用 single-transaction。跨库不保证同一时点，协调停止写入后备份；DDL 与备份也须避免并行。脚本检查压缩完整性和最低大小，不证明可恢复；定期在隔离环境恢复并核对资金、订单和幂等记录。

SQL 备份不包含 Redis AOF、导出文件、Caddy 状态、密钥和部署配置，须另行备份。已发布 Outbox 不会自动重发事件 Redis 中丢失的全部记录，不能直接清空 Stream 卷后宣称恢复完成。

恢复会先删除归档声明的三库，再重建并导入，失败时可能只完成部分数据。停止业务写入，核对归档及目标环境后执行：

```bash
RESTORE_CONFIRM="gateway_db central_db worker_db" bash scripts/db/restore.sh "$BACKUP_ARCHIVE"
```

`BACKUP_ARCHIVE` 须设置为已核对的归档路径。恢复后核对三库、Goose 版本和关键台账；若执行 migrate，必须配置全部三库 URL。开发服务用 `docker compose -f compose.dev.yaml restart central gateway worker`；生产使用实际部署的全部 Compose 文件重启对应服务。更新或恢复不得使用 `docker compose down -v`。

## 监控与故障定位

三服务均提供 `/health/live`、`/health/ready` 和 `/metrics`。live 只说明进程存活，ready 检查服务依赖。HTTP 指标按已注册路由统计，不能作为 TCP 连接数或设备在线率。

```bash
docker compose -f compose.dev.yaml -f compose.monitoring.yaml up -d prometheus
```

当前 Prometheus 面向开发内网，端口 9090，抓取间隔 15 秒、保留 7 天。抓取失败持续 1 分钟告警；近 5 分钟至少 20 次请求且 5xx 超 5%，持续 5 分钟告警；排除 `/metrics` 后同样流量条件下 p95 超 2 秒，持续 5 分钟告警。配置和规则测试见 [monitoring](monitoring)，当前没有 Alertmanager 通知链路。

| 现象 | 核对项 |
| --- | --- |
| 服务未就绪 | 对应三库 URL、Goose 版本、数据库/Redis 凭证、依赖连通性与日志 |
| 管理员无法登录 | 引导仅空表执行、账号状态、MFA、Redis 会话、实时权限及限速 |
| 设备离线 | TCP 9100、设备编号/端口数、开通状态、连接会话与心跳；断线不代表端口已停机 |
| 已付款未充电 | 支付意图、端口占用、启动命令及回执；先确定结果，不能重复启动或直接退款 |
| 已停机未结算 | 结束投递、central 回执、计费任务、计量复核和冻结快照 |
| 退款未到账 | 同一退款号的渠道结果、预留额度、执行策略及双人审核，processing 不等于成功 |
| Webhook 或消息积压 | Stream 载荷、消费组 pending、投递日志及实际主程序接线限制 |
| HTTPS 异常 | 域名解析、80/443、服务器时钟、Caddy 日志及持久数据卷 |

## 功能边界

- H5 支持开发登录及模拟支付；正式微信支付和手机号授权通过微信小程序。H5 扫码使用手动输入，定位失败可填写坐标。
- 微信直连 SDK 已接入，真实商户和实机仍需验收；汇付天下通道未实现。
- 微信订阅消息、电子发票签发、自动银行出款和自动渠道账单下载未接入。
- Webhook 连续消费、退款结果 pending 回收及通用 DLQ 运行恢复仍受上述接线限制。
- MQTT、OTA、月卡/年卡、邀请奖励、混合支付、在线客服和可配置告警订阅不在当前功能范围。已有烟雾、高温和设备故障告警、确认及恢复保留。
- 用户公告当前只返回全局公告，站点和城市定向未接线。
- 指定监管平台适配、合规认证和外部验收均不能由数据库表或本地测试推定完成。

开源许可见 [LICENSE](LICENSE)，商业授权模板见 [COMMERCIAL_LICENSE.md](COMMERCIAL_LICENSE.md)，实际授权以签订合同为准。
