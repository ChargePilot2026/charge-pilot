# Go 充电生命周期接口（当前实现）

> 本文记录当前 Go 后端和小程序的充电主流程。扫码、后台方案、支付意图、模拟支付回调、实际计费和自动退款已接线；真实微信商户和设备联调仍需验收。

## 编号与用户 ID

新充电订单号为 `C + 北京时间 yyyyMMddHHmmss + 设备编号 + 两位端口(01–99)`。时间取启动请求创建时刻，不因设备确认或计费完成而改变；同设备同端口同秒再次起单会返回冲突，重复客户端请求则复用原记录。新计费号只将充电号首字母 `C` 替换成 `B`，历史数字及 `CH` 订单仍使用原 `FEE` 规则，已持久化回执不重新编号。

新支付单号为 `P + 独立 Snowflake`，扫码、余额/在线卡扣费、充值及欠费补缴共用分配器。用户 ID 是无前缀的数字 Snowflake，数据库保留 `BIGINT UNSIGNED`；公开 API 和 Redis 会话使用十进制字符串，前端不得转换为 JavaScript Number。历史用户 ID 和支付/充电/计费编号原样保留，原请求号重试仍复用原支付单。

分配器采用 41 位毫秒时间（纪元 2020-01-01 UTC）、10 位节点、12 位序列；所有 central 实例通过同一 user_db 的 `snowflake_state` 行锁共享节点 0，编号和业务记录同事务提交。分配状态持久化，重启、回拨及同毫秒并发不会复用已提交编号；序列耗尽推进逻辑毫秒。

支付订单后台支持 `created_from/created_to` ISO 8601 时间范围，按创建时间查询、包含两端；留空不限制时间。

## 服务内部认证

`gateway` 和 `central` 的 `/api/v1/internal/...` 接口仅在 Docker 内网调用，必须携带 `X-Service-Token`。扫码、端口、方案、公告及站点读取可匿名访问；发起支付及用户订单、钱包、退款操作使用用户 JWT。响应均使用 `{code,message,data,request_id,trace_id}` 信封。

## 扫码只读

`POST central /api/v1/user/scan/resolve` 请求 `{ "code": "二维码原始内容" }`，`POST central /api/v1/user/scan/port` 请求 `{ "port_id": "印刷端口码" }`。小程序把扫描内容原样传给 central；central 支持纯设备/端口码，或包含唯一 `code` 查询参数的 HTTPS 二维码链接，提取后经 `GET gateway /api/v1/internal/scan/resolve?code=...` 查询。返回 `kind=port|device`、站点 ID、端口状态、在线状态及 `available`。设备结果含端口列表，选择端口后再请求端口详情。扫码不创建订单、不预占端口。

端口页调用 `POST /api/v1/user/scan/offers`，请求 `{ "port_id": "..." }`，读取后台按站点发布的金额档位和时长套餐。用户只能选择方案，不能输入金额、电量、时长或价格。金额档位按设备实测电量与站点计费规则扣费，达到购买金额时停止，未用部分退款；时长套餐以后台设置的固定价格购买固定时长，到时停止，提前结束或故障时按实际充电秒数比例结算并退还剩余款项。

用户点击支付前，小程序才检查登录。`POST /api/v1/user/scan/start` 请求 `{ "client_request_id": "UUID", "port_id": "...", "offer_id": 123 }`。后端重读当前有效方案和计费规则，冻结在短期支付意图中，并创建微信支付单；响应含 `intent_id`、`merchant_order_no`、`payment_params` 和 `expires_at`，此时尚无充电订单。相同请求 ID 重试使用原冻结价格，即使运营人员随后改价或下架。真实服务拒绝客户端估价字段。

`PAYMENT_MODE=wechat_direct` 时，官方 SDK 创建 JSAPI 预支付并验签、解密 `POST /api/v1/public/payments/wechat/callback`。回调核对商户、应用、用户 openid、交易号和金额后，才在同一事务中创建已付款充电订单；重复回调不会重复创建。过期后到账只建立待退款记录。`PAYMENT_MODE=simulation` 仅用于本地测试，其内部模拟回调要求 `X-Service-Token`，公网代理拒绝 `/api/v1/internal/*`。`PAYMENT_MODE=disabled` 拒绝发起支付。

## 付款后启动

1. worker 扫描由已验证支付回调创建、仍处于 `paid` 状态的充电订单，重试 `POST gateway /api/v1/internal/charge-orders/start`，请求 `{ "order_no": "CH..." }`。gateway 通过 `GET central /api/v1/internal/charge-orders/{order_no}/start-authorization` 核对 `charge_order.status=paid`、同一业务的 `payment_order.status=paid`、足额支付及持久化的 `charge_mode/charge_quantity`。缺失任一条件返回冲突，不下发设备命令。
2. gateway 在 `gateway_db` 中预留端口、保存唯一 START 命令及补偿 STOP 会话，提交后才发送 `dc589` B7。`202` 只表示已受理，`data.status` 为 `pending/sent/acked/stopping/rejected`，不能当作已开始充电。
3. B8 回执必须匹配设备、端口和六字节会话 ID。网关把原始帧、命令状态及 Outbox 同事务保存。worker 重试 `POST central /api/v1/internal/charge-orders/{order_no}/start-result`，请求包含 `command_id,charge_order_id,order_no,device_id,port_no,port_id,success,result_code,occurred_at`。central 在事务中写入开始回执、活动端口和事件。重复回执幂等，内容改变返回 `409`。
4. 若 central 拒绝已经收到成功 B8 的启动结果，worker 请求 `POST gateway /api/v1/internal/charge-orders/{order_no}/compensate`，请求 `{ "command_id": "UUID" }`。gateway 进入 `stopping`，重试同一 B9 STOP；只在匹配 BA 的停止成功或已空闲结果后释放端口。

## 主动停止与结束

`POST central /api/v1/user/charge/stop` 使用用户 JWT，请求 `{ "order_no": "ORD-..." }`。central 通过内部请求让 gateway 核对当前用户和充电状态，持久化唯一 B9 STOP 并派发。成功响应的 `stopped` 始终是 `false`；BA 回执仅表示设备已停止输出，计费需要 BB 结算帧。

worker 每 10 秒检查充电中订单的冻结方案：套餐时长到期、金额档位根据最新可核算设备读数达到上限，或设备持续在线且该端口连续至少 1 分钟上报零功率时，向 gateway 发起自动 STOP。端口缺失读数或设备离线不视作零功率。金额档位若缺少可靠分时计量证据，当前无法据此确认到价停机，属于实机联调前必须继续处理的边界。

`dc589` BB 包按协议小端解码订单号、实际 Wh、时长、停止原因及本地结束时间，原始事件先持久化再发 BC。worker 重试 `POST central /api/v1/internal/charge-orders/{order_no}/end-result`，请求包含开始/停止命令 ID、设备/端口身份和 `meter:{charged_wh,charged_seconds,ended_at,stop_reason}`。central 以事务写结束回执、订单实际读数和 `charge_ended_stream` Outbox；worker 收到确认后才释放 gateway 端口。重复一致的读数幂等，不一致返回 `409`。订单的计费金额仍未知，必须由后续计费流程填入。

## 已知边界

- 厂商协议没有带平台凭证的登录字段，当前建档设备核验依赖设备编号和厂商映射；实体设备认证增强待设备方确认。
- BB 本地时间可能与服务器时间不同；跨设备时钟偏差及 B8/BA 会话回显需实机验证。
- 设备离线时 STOP 命令持续持久重试；设备实际断电由 BA/BB 和后续实机验收确认。
- 设备明确拒绝 B7 启动时，central 同事务将订单置为 `refunding`、插入以支付单和订单关联的全额待退款记录，并写入持久 Outbox。退款执行见下节；网关暂时不可达时仍由 worker 重试启动，不应将暂时性网络失败当作设备拒绝。真实支付仍须保持关闭。

## 自动退款执行（2026-09-29）

新增内部 `POST /api/v1/internal/refunds/dispatch`，使用 `X-Service-Token`。
worker 每 10 秒独立调度，避免阻塞设备启动和结束结果同步。
`PAYMENT_MODE=disabled` 返回 `{processed:0,enabled:false}`；开启时每批最多扫描 20 笔到期请求，
返回 `{processed,enabled:true}`，部分失败返回 503 并保留重试记录。

启动拒绝与过期支付回调创建 `execution_policy=automatic` 的退款；旧记录及人工退款默认
`manual_review`，不自动执行。执行前锁支付行并检查支付归属、金额、累计已退和其它预留退款额度，
并核对预下单快照中的渠道。每次调用先查原退款单号；渠道明确不存在才用原单号提交。
未知结果保持 `processing`，持久化下一次查询时间，退避从 30 秒起、上限 64 分钟。
渠道 `CLOSED/ABNORMAL` 转 `failed` 供后续人工处置，不自动创建另一笔退款。

确认 `SUCCESS` 后，同事务写唯一渠道回执、退款终态、支付累计已退金额和
`refund_succeeded_stream` Outbox；全额退回的 `refunding` 充电单变为 `refunded`。
成功结果校验原交易号、原商户单号、退款单号、总金额、退款金额和成功时间。
微信 SDK 通过主动查询确认结果，尚无独立退款通知入口；该 Stream 的用户通知消费仍未交付。
`simulation` 为本地模拟，不代表真实微信资金到账。人工退款双签已接入后台，汇付仍未接入。

## 实际计费与预付差额（2026-09-29）

结束回执同事务建立 `charge_billing_job`。worker 每 10 秒通过服务令牌保护的
`POST /api/v1/internal/billing/dispatch` 调度，使用支付时保存的规则快照和实际 Wh/秒数核算。
`meter.segments` 可提供连续时间段及各段实际 Wh；跨不同费率但缺少分段时，不按时间均摊电量，
持久化 `manual_fee_review` 并暂停该订单计费。后台已提供分段读数补充、拒绝重提和双人复核恢复流程，保留设备原始回执。

billing 库同事务保存唯一计费回执、价费分项、规则快照和投递记录。
user 库独立事务重新核对原始快照与金额，幂等写费用回执、订单费用和时间线。
跨库提交后回执丢失由投递记录恢复，不重复计费或退款。
预付余额扣除实际费用、已退及在途预留后，差额生成唯一自动退款单；
渠道成功后已结束订单在无其它在途退款时回到 `completed`，保留部分退款状态。
费用超过预付的差额记录在 `charge_fee_receipt.shortfall_cents`；补缴流程尚未实现。

当前自动流程只支持已有的微信支付订单（包括本地 simulation）；满足条件的累计遥测可自动生成实际分段（见下节）；
钱包支付计费、欠费补缴和结算分账仍需补齐。本地隔离数据库 race 测试覆盖并发重放、
金额篡改、跨库回执丢失、分段缺失转人工、欠费记录与模拟差额退款。

## 设备累计读数自动分段

BB 现在保留设备开始时间。worker 只使用该端口的完整心跳累计电量和已充秒数，
在 BB 设备时钟上定位读数；不使用包到达时间均摊电量，不使用不含电量的 C2 包。
要求 BB 起止时长等于已充秒数、开始时间与平台 ACK 相差不超过 5 秒、
每次读数与接收时间相差不超过 5 秒、读数按时间和电量单调、整数 Wh 且不超过 BB 总量。
缺失起始时间、暂停导致时间不一致、回退、冲突或超过 10080 个心跳时不生成自动分段。

相邻累计读数的差值形成实际分段；跨不同费率但缺少边界实测读数时，计费模块仍转人工，
不插值。结束事件 ID 限定当时已存证据，`gateway.charge_end_delivery` 冻结完整发送请求，
保证 HTTP 重试不受迟到数据或遥测归档影响。迁移 gateway 0008 已接线。
单位与集成测试覆盖跨费率、接收延迟、时钟矛盾、回退、重复、错误端口、发送失败、
源遥测删除及迟到读数；本地已验证结束同步→自动计费→模拟退款→释放端口。
以上为模拟证据，真实设备时钟和断线补传行为仍需实机联调。
