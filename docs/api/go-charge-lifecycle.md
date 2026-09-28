# Go 充电生命周期接口（当前实现）

> 本文记录新 Go 后端的充电接口。支付预下单、微信回调接线和本地模拟已实现；真实商户联调、计费结算和退款执行仍未完成。生产切换门禁保持关闭。旧小程序仍调用废止的 `/scan/quote` 和 `quote_id`，尚未适配本契约。

## 服务内部认证

`gateway` 和 `central` 的 `/api/v1/internal/...` 接口仅在 Docker 内网调用，必须携带 `X-Service-Token`。公开小程序接口使用用户 JWT。响应均使用 `{code,message,data,request_id,trace_id}` 信封。

## 扫码只读

`POST central /api/v1/user/scan/resolve` 请求 `{ "code": "设备码或端口码" }`，`POST central /api/v1/user/scan/port` 请求 `{ "port_id": "印刷端口码" }`。两个接口都校验用户 JWT、实时会话和账号状态，经 `GET gateway /api/v1/internal/scan/resolve?code=...` 读取已启用设备与端口。返回 `kind=port|device`、站点 ID、端口状态、最近两分钟心跳推断的在线状态及 `available`。端口码是字符串，内部数字端口 ID 不暴露。扫码不创建订单、不预占端口。价格不放在扫码查询响应里。

扫码之后没有独立报价接口或 `quote_id`。用户决定支付时，调用 `POST central /api/v1/user/scan/start`，请求 `client_request_id`（UUID）、`port_id`、`estimated_kwh`、`estimated_minutes`。服务端读取站点有效计费规则、计算预付金额并保存规则快照，只创建支付意图和支付单；响应包含 `intent_id`、`merchant_order_no`、费用分项、`payment_params`、`expires_at`，没有充电订单号。服务端重新计算金额，不接受客户端提交金额。

`PAYMENT_MODE=wechat_direct` 时，官方 SDK 创建 JSAPI 预支付并验签、解密 `POST /api/v1/public/payments/wechat/callback`。回调核对商户、应用、用户 openid、交易号和金额后，才在同一事务中创建已付款充电订单；重复回调不会重复创建。过期后到账只建立待退款记录。`PAYMENT_MODE=simulation` 仅用于本地测试，其内部模拟回调要求 `X-Service-Token`，公网代理拒绝 `/api/v1/internal/*`。`PAYMENT_MODE=disabled` 拒绝发起支付。

## 付款后启动

1. worker 扫描由已验证支付回调创建、仍处于 `paid` 状态的充电订单，重试 `POST gateway /api/v1/internal/charge-orders/start`，请求 `{ "order_no": "CH..." }`。gateway 通过 `GET central /api/v1/internal/charge-orders/{order_no}/start-authorization` 核对 `charge_order.status=paid`、同一业务的 `payment_order.status=paid`、足额支付及持久化的 `charge_mode/charge_quantity`。缺失任一条件返回冲突，不下发设备命令。
2. gateway 在 `gateway_db` 中预留端口、保存唯一 START 命令及补偿 STOP 会话，提交后才发送 `dc589` B7。`202` 只表示已受理，`data.status` 为 `pending/sent/acked/stopping/rejected`，不能当作已开始充电。
3. B8 回执必须匹配设备、端口和六字节会话 ID。网关把原始帧、命令状态及 Outbox 同事务保存。worker 重试 `POST central /api/v1/internal/charge-orders/{order_no}/start-result`，请求包含 `command_id,charge_order_id,order_no,device_id,port_no,port_id,success,result_code,occurred_at`。central 在事务中写入开始回执、活动端口和事件。重复回执幂等，内容改变返回 `409`。
4. 若 central 拒绝已经收到成功 B8 的启动结果，worker 请求 `POST gateway /api/v1/internal/charge-orders/{order_no}/compensate`，请求 `{ "command_id": "UUID" }`。gateway 进入 `stopping`，重试同一 B9 STOP；只在匹配 BA 的停止成功或已空闲结果后释放端口。

## 主动停止与结束

`POST central /api/v1/user/charge/stop` 使用用户 JWT，请求 `{ "order_no": "ORD-..." }`。central 通过内部请求让 gateway 核对当前用户和充电状态，持久化唯一 B9 STOP 并派发。成功响应的 `stopped` 始终是 `false`；BA 回执仅表示设备已停止输出，计费需要 BB 结算帧。

`dc589` BB 包按协议小端解码订单号、实际 Wh、时长、停止原因及本地结束时间，原始事件先持久化再发 BC。worker 重试 `POST central /api/v1/internal/charge-orders/{order_no}/end-result`，请求包含开始/停止命令 ID、设备/端口身份和 `meter:{charged_wh,charged_seconds,ended_at,stop_reason}`。central 以事务写结束回执、订单实际读数和 `charge_ended_stream` Outbox；worker 收到确认后才释放 gateway 端口。重复一致的读数幂等，不一致返回 `409`。订单的计费金额仍未知，必须由后续计费流程填入。

## 已知边界

- 厂商协议没有带平台凭证的登录字段，当前建档设备核验依赖设备编号和厂商映射；实体设备认证增强待设备方确认。
- BB 本地时间可能与服务器时间不同；跨设备时钟偏差、B8/BA 会话回显及 OTA 回滚需实机验证。
- 设备离线时 STOP 命令持续持久重试；设备实际断电由 BA/BB 和后续实机验收确认。
- 付款后设备端口发生冲突时，worker 当前持续记录启动失败；自动关闭与退款补偿尚未接入，不可据此开放真实支付。
