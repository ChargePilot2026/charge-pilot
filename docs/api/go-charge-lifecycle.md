# Go 充电生命周期接口（当前实现）

> 本文只记录新 Go 后端已接线的充电接口。支付预下单、真实支付回调、计费结算和退款执行仍未完成；以下内部接口只能在测试库中用已付款测试单验收。生产切换门禁保持关闭。

## 服务内部认证

`gateway` 和 `central` 的 `/api/v1/internal/...` 接口仅在 Docker 内网调用，必须携带 `X-Service-Token`。公开小程序接口使用用户 JWT。响应均使用 `{code,message,data,request_id,trace_id}` 信封。

## 付款后启动

1. `POST gateway /api/v1/internal/charge-orders/start`，请求 `{ "order_no": "ORD-..." }`。gateway 通过 `GET central /api/v1/internal/charge-orders/{order_no}/start-authorization` 核对 `charge_order.status=paid`、同一业务的 `payment_order.status=paid`、足额支付及持久化的 `charge_mode/charge_quantity`。缺失任一条件返回冲突，不下发设备命令。
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
