# 监管报送接口（Go 后端）

当前实现提供与监管厂商无关的六类标准化对象入口、MySQL 持久队列、模拟投递和可替换的 HTTPS 发送器。真实监管平台字段映射、签名/国密与回执语义须根据客户提供的规范另行联调。生产默认 `REGULATORY_MODE=disabled`，模拟成功在状态中明确标为 `delivered_mode=simulation`。

## 内部接口

仅服务间调用，须传 `X-Service-Token`。

`POST /api/v1/internal/regulatory/events` 将一个事件写入 `admin_db.regulatory_report`。`event_id` 须为 UUID；同 ID、类型、对象键和数据的重复请求返回 `queued=false`，内容冲突返回 409。`data` 最大 64 KiB。

```json
{
  "event_id": "3c37a99e-d5f3-4391-95ef-08b3a61149db",
  "object_type": "device",
  "object_key": "DEVICE-001",
  "data": {
    "device_id": "DEVICE-001",
    "model": "AC-2",
    "protocol": "dc589",
    "firmware_version": "1.0"
  }
}
```

`GET /api/v1/internal/regulatory/events/{event_id}` 返回 `status`（`queued`/`processing`/`delivered`）、`attempts`、`next_attempt_at`、`delivered_at`、`delivered_mode` 和 `last_error`。只有真实发送成功才会标 `delivered_mode=http`；它也仅表示通用 HTTP 接收端返回 2xx，不代表监管平台业务验收。

| object_type | data 必填字段 |
| --- | --- |
| `operator` | `vendor_id`, `name`, `contact` |
| `station` | `station_id`, `name`, `address`, `longitude`, `latitude`, `service_type` |
| `device` | `device_id`, `model`, `protocol`, `firmware_version` |
| `order` | `order_id`, `started_at`, `ended_at`, `energy_kwh`, `amount_cents` |
| `alert` | `alert_id`, `type`, `severity`, `time` |
| `battery` | `battery_code`, `soc`；`health` 可选 |

`object_key` 必须与对应对象 ID/编码一致。订单时间须符合 RFC 3339 且结束晚于开始；电量与金额不得为负；SOC 与健康度为 0–100。电量以十进制字符串表示，金额以分为单位。字段采用需求文档 § 十二的最小通用集合，不声明符合任何具体地方监管平台的私有协议。

## 投递模式

- `disabled`：不消费队列，生产默认值。
- `simulation`：本地模拟接收并标记 `delivered_mode=simulation`；开发 Compose 使用此模式。
- `http`：向 `REGULATORY_ENDPOINT` 发送 HTTPS POST，必须配置至少 16 字节的 `REGULATORY_SIGNING_SECRET`。请求体为上述 Event JSON；`X-ChargePilot-Timestamp` 为 Unix 秒，`X-ChargePilot-Signature` 为 `HMAC-SHA256(secret, timestamp + "." + 原始请求体)` 的小写十六进制；同时发送 `X-ChargePilot-Event-Id`。仅用作可替换的通用适配器。

worker 每 10 秒扫描最多 20 条到期记录；跨实例领取使用数据库租约。发送失败后记录错误并以 2 秒起、至多 1 小时的指数退避重试，队列记录保留。没有实际平台规范前，生产切换门禁保持关闭。

当前还没有将运营商、站点、设备、订单、告警和电池源表的变更自动转成报送事件。尤其现有源表没有完整的运营商联系人和电池唯一编码；接入真实平台前须补齐来源、字段映射和自动发布触发，并做平台回执联调。
