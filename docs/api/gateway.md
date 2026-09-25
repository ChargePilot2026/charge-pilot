# gateway 服务 API 详细设计

**服务**:`gateway`(`services/gateway`)
**对外地址**(设备侧):
- TCP 监听 `:9100`(私有协议帧)
- MQTT Broker 监听 `:1883`(`rumqttd` 嵌入)
**对外地址**(内部 HTTP):
- 内部 HTTP API 监听 `:8083`(经 Docker Compose 内网调用,**不暴露公网**)
**鉴权**(设备侧):`device_id` 唯一标识(§ 6.2)
**鉴权**(内部 HTTP):服务间共享密钥(`Authorization: Bearer <service_token>`)

> **本文件覆盖范围**:gateway 服务的**全部对外接口**,包括设备长连接接入层(TCP / MQTT)+ 内部 HTTP 状态查询 / 控制指令 API。本服务**不直接面向终端用户或 PC 后台操作员**,只接受 `device` / `user` / `admin` / `worker` 内部调用。

---

## 通用约定

### 设备 ID 格式(§ 6.2)

- 长度 8-32 字符
- 字符集 `[a-zA-Z0-9_-]`
- 厂商前缀 2-4 字母(对应 `vendor.vendor_code`),如 `xx_xxxx_xxxx`
- **大小写敏感**(同一字符不同大小写视为不同 ID)

### 协议适配(§ 3.1.2)

- 每家厂商一个 adapter,实现 `Adapter` trait:
  ```rust
  pub trait Adapter: Send + Sync {
      fn vendor_code(&self) -> &str;
      fn parse_frame(&self, bytes: &[u8]) -> Result<Frame, ParseError>;
      fn build_command(&self, cmd: &Command) -> Result<Vec<u8>, EncodeError>;
  }
  ```
- 厂商字典存于 `gateway_db.vendor`;新增厂商在 admin PC 后台"厂商管理"配

### 统一响应包装(内部 HTTP)

```json
{
  "code": 0,
  "message": "ok",
  "data": { ... },
  "request_id": "..."
}
```

### 设备帧格式(私有协议,通用约定)

各厂商具体格式不同,但**通用字段**(`Frame` 结构):

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `device_id` | string | 是 | 设备唯一 ID(§ 6.2 格式) |
| `frame_type` | enum | 是 | `register` / `heartbeat` / `telemetry` / `state_change` / `fault` / `command_ack` |
| `timestamp_ms` | int64 | 是 | 设备本地时间戳(Unix 毫秒) |
| `payload` | bytes | 是 | 厂商私有负载(由对应 adapter 解析) |

### 限流 / 连接上限

> gateway 服务特殊:限制的是**设备 TCP/MQTT 连接数**而非 HTTP QPS,见 `docs/技术规格.md` § 3.1.2 + § 6.1 + § 6.3(权威源)。HTTP 内部接口限流见 § 7.4。

- **TCP 连接数**:配置项 `tcp.max_connections`(默认 10000)
- **MQTT QoS**:上行 / 下行 QoS 1(保证至少一次)
- **心跳超时**:`tcp.idle_timeout_secs`(默认 90 s)
- **HTTP 内部调用**:不限流(信任调用方)

### 错误码

> 完整错误码字典见 `services/common-error/errors.toml` + `docs/技术规格.md` § 7.2(权威源)。
> 段位固定:`0`=成功 / `1xxx`=通用 / `2xxx`=业务(`gateway` 服务专属子段 2301-2399)/ `3xxx`=第三方协议解析 / `5xxx`=服务器。gateway **不**使用 `4xxx`(内网不限流)。本文仅列该服务用到的子集。

| 段位 | 含义 | 示例 |
| --- | --- | --- |
| 1xxx | 通用错误 | 1001 鉴权失败 / 1004 资源不存在 / 1005 参数校验失败 |
| 2xxx | 业务错误 | 2001 设备未注册 / 2002 设备已停用 / 2003 端口被占用 / 2004 充电指令下发失败 |
| 3xxx | 协议错误 | 3001 帧解析失败 / 3002 心跳超时 / 3003 设备固件版本不兼容 |
| 4xxx | 限流 | 4291 TCP 连接数超限 / 4292 MQTT QoS 超限 |
| 5xxx | 服务器错误 | 5001 内部错误 / 5003 下游服务暂时不可用 |

---

## 一、设备长连接接入层(TCP / MQTT)

### 1.1 TCP 服务(端口 9100)

**协议栈**:各厂商私有 TCP 协议

**连接生命周期**:

```
设备 TCP 连接建立
  → 发送 register 帧(device_id + 厂商代码 + 协议版本)
    → 校验 device_id 在 gateway_db.device 存在 + status='active'
    → INSERT device_session(status='open', ...)
    → 回 register_ack(携带会话 ID)
  → 心跳(默认 60 s 一次,3 次未收到 → 标记 offline)
  → 遥测帧 / 状态变更帧持续上行
  → TCP 断开 → UPDATE device_session(status='closed', closed_at, closed_reason)
```

**关键规则**:

- **首次注册**:device 在 `gateway_db.device` 必须已存在(由 admin 后台"设备管理"录入)+ `status='active'`
- **重连复用**:同一 `device_id` 重连 → 关闭旧 session + 开新 session(不断网监测用)
- **断网补传**:设备本地缓存最近 60 min 遥测(§ 4.6),重连后主动调 HTTP `POST /api/v1/internal/device/backfill` 提交
- **数据落库**:`raw_frame_log` 存原始字节;`telemetry` 存解析后的字段

### 1.2 MQTT Broker(端口 1883)

**Topic 命名约定**(§ 3.1.2):

| 方向 | Topic 格式 | 示例 | QoS |
| --- | --- | --- | --- |
| 上行(设备→gateway) | `charge/{vendor_id}/{device_id}/telemetry` | `charge/xx/xx_001_abc/telemetry` | 1 |
| 上行(状态变更) | `charge/{vendor_id}/{device_id}/state` | `charge/xx/xx_001_abc/state` | 1 |
| 上行(故障) | `charge/{vendor_id}/{device_id}/fault` | `charge/xx/xx_001_abc/fault` | 1 |
| 下行(控制指令) | `charge/{vendor_id}/{device_id}/cmd` | `charge/xx/xx_001_abc/cmd` | 1 |
| 下行(固件推送) | `charge/{vendor_id}/{device_id}/firmware` | `charge/xx/xx_001_abc/firmware` | 1 |

**客户端认证**:仅校验 `device_id`(MQTT `client_id` 字段);**不做 ACL**(单客户部署,所有设备同客户,§ 3.1.2)

**下行指令 payload**(JSON):

```json
{
  "cmd_id": "uuid",
  "cmd_type": "start_charge",       // start_charge / stop_charge / reboot / firmware_update
  "issued_at": "2026-09-25T14:00:00Z",
  "expires_at": "2026-09-25T14:01:00Z",   // 过期指令设备应丢弃
  "params": {                        // 按 cmd_type 填
    "port_id": "xx_001_01",
    "max_power_w": 500,
    "max_duration_sec": 7200
  }
}
```

**设备 ACK**:

```json
{
  "cmd_id": "uuid",
  "ack_status": "success",          // success / failed / rejected
  "ack_message": "OK",
  "ack_at": "2026-09-25T14:00:05Z"
}
```

> gateway 收到 ACK 后 INSERT `ota_command(ack_status, ack_at)`(仅 OTA)或更新对应控制指令状态表,**所有指令通过 Redis Stream 通知发起方**(§ 5.1 `comp_tx_stream`)。

---

## 二、内部 HTTP 端点清单(共 17 个)

> 所有路径在 `:8083`;**仅内网可达**(Docker Compose 内服务间调用);鉴权为服务间共享密钥。

### A. 设备接入类(设备主动调用,4 个)

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/internal/device/register` | 设备注册(TCP 首次连接或重注册) |
| POST | `/api/v1/internal/device/heartbeat` | 设备心跳(MQTT 路径下用 LWT,本端点备用) |
| POST | `/api/v1/internal/device/backfill` | 设备断网补传遥测(批量) |
| POST | `/api/v1/internal/device/log` | 设备日志上报(诊断 / 错误日志) |

### B. 状态查询类(admin / user 调用,6 个)

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/internal/devices` | 设备列表(按站点 / 状态筛选) |
| GET | `/api/v1/internal/devices/{device_id}` | 设备实时状态(从 Redis 缓存取) |
| GET | `/api/v1/internal/devices/{device_id}/ports` | 端口列表(每个端口的空闲 / 充电中 / 故障状态) |
| GET | `/api/v1/internal/devices/{device_id}/telemetry` | 设备最新遥测快照 |
| GET | `/api/v1/internal/orders` | 订单列表(按站点 / 状态 / 时间筛选) |
| GET | `/api/v1/internal/orders/{order_id}` | 订单详情(含原始帧 + 解析后字段) |

### C. 控制指令类(user / admin / worker 调用,7 个)

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/internal/start-charge` | 启动充电(user 服务调,§ 2.4 已定路径) |
| POST | `/api/v1/internal/stop-charge` | 停止充电(user 服务调,§ 8.4 已定路径) |
| POST | `/api/v1/internal/devices/{device_id}/reboot` | 远程重启(admin 服务调,§ 3.3.1 已定) |
| POST | `/api/v1/internal/devices/{device_id}/firmware-push` | OTA 固件推送(worker 消费 `ota_schedule_stream` 后调) |
| POST | `/api/v1/internal/devices/{device_id}/firmware-rollback` | OTA 回滚(失败自动或人工) |
| GET | `/api/v1/internal/devices/{device_id}/firmware-status` | 查询设备当前固件版本与上次升级状态 |
| POST | `/api/v1/internal/devices/{device_id}/ack-received` | 设备指令 ACK 上行(TCP 模式下 HTTP 兜底) |

---

## 三、设备接入类(关键端点展开)

### `POST /api/v1/internal/device/register`

**鉴权**:服务间共享密钥
**触发场景**:设备 TCP 首次建立连接 / 设备因故障重启后重新注册
**业务目标**:校验设备合法性 + 建会话 + 回 register_ack

**请求体**:
```json
{
  "device_id": "xx_001_abc",
  "vendor_code": "xx",
  "protocol_version": "v1.0",
  "firmware_version": "v1.2.3",
  "hardware_model": "XD-220V-10A",
  "serial_number": "SN-2026-001-abc",
  "connect_type": "tcp",                  // "tcp" / "mqtt"
  "client_ip": "10.0.5.21"
}
```

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "session_id": 12345,
    "session_token": "sess_abc123...",     // 后续 TCP 心跳 / 帧必带
    "heartbeat_interval_sec": 60,
    "server_time_ms": 1764124800000,      // 设备校时用
    "config": {
      "telemetry_interval_sec": 5,        // 遥测上报间隔
      "heartbeat_interval_sec": 60,
      "alert_thresholds_enabled": false   // 设备本地阈值检测开关,默认关(由 gateway 集中检测)
    }
  }
}
```

**业务逻辑**:
1. 校验 `device_id` 在 `gateway_db.device` 存在 + `status='active'` → 否则返回 `2001`
2. 校验 `vendor_code` 在 `gateway_db.vendor` 存在 + `status='enabled'` → 否则返回 `1005`
3. 校验 `protocol_version` 与 `vendor.protocol_version` 兼容 → 否则返回 `3003`
4. **关闭旧 session**:`UPDATE device_session SET status='closed', closed_at=NOW(), closed_reason='re_register' WHERE device_id=? AND status='open'`
5. INSERT `device_session(status='open', connect_type, client_ip, opened_at=NOW())` → 拿 `session_id`
6. UPDATE `device.last_seen_at=NOW(), status='online', firmware_version=...`
7. 缓存 Redis `device:session:$device_id = session_id` TTL 600 s(用于查询)
8. **事务边界**:device_session + device 更新同事务;Redis 缓存失败不回滚(下次心跳会重建)

**错误码**:
- `2001`: 设备未注册(需客户运营先在 admin 录入)
- `2002`: 设备已停用
- `3003`: 协议版本不兼容
- `1005`: `vendor_code` 不存在 / 已停用

---

### `POST /api/v1/internal/device/backfill`

**鉴权**:服务间共享密钥
**触发场景**:设备断网后重连,把本地缓存的遥测批量补传
**业务目标**:批量 INSERT `telemetry` 表(按 device_id hash 路由到对应分表)

**请求体**:
```json
{
  "device_id": "xx_001_abc",
  "session_id": 12345,
  "frames": [
    {
      "timestamp_ms": 1764124790000,
      "voltage_v": "220.5",
      "current_a": "3.20",
      "power_w": "704.0",
      "temperature_c": "32.5",
      "meter_kwh": "0.045",
      "battery_soc": 25,
      "charge_state": "charging"
    },
    { "timestamp_ms": 1764124795000, "...": "..." }
  ]
}
```

**请求字段**:
| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `device_id` | string | 是 | 设备 ID |
| `session_id` | int | 是 | 当前 session ID(register 时拿) |
| `frames` | array | 是 | 补传帧数组(每批 ≤ 500 条,§ 4.6) |

**限流**:
- **每 `device_id` 1 req/s**(防设备端 bug 触发频繁重传拖垮 gateway)
- 单批 500 条上限已限制单请求最大内存
- 触发限流返回 `4291`(失败时设备端按指数退避重试,默认 1s/5s/30s)

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "accepted_count": 487,
    "rejected_count": 13,
    "first_ts_ms": 1764124790000,
    "last_ts_ms": 1764124850000,
    "duplicate_count": 5            // 与已存在帧时间戳重复(幂等)
  }
}
```

**业务逻辑**:
1. 校验 `session_id` 有效 + `status='open'` → 否则返回 `1001`
2. 校验 `frames` 长度 ≤ 500,否则返回 `1005`
3. **逐帧处理**(单批 500 条内):
   - **七重防护字段校验**(§ 需求文档 6.4):电压 / 电流 / 功率 / 温度 / SOC / 电量 / 状态合理性
   - 校验失败 → 加入 `rejected_count`,记录到 `gateway_db.alert_event`(本期 gateway_db 无此表,先入 `alert_stream`,由 worker / admin 落库)
   - 校验通过 → 计算分表路由(`hash(device_id) % 16`),INSERT 对应 `telemetry_{0..15}` 表
   - **幂等**:`(device_id, timestamp_ms)` 已存在 → 跳过(返回 `duplicate_count`)
4. UPDATE `device.last_seen_at=NOW()`
5. 异步触发 15 min / 小时聚合刷新(§ 4.6 双粒度聚合)

**错误码**:
- `1001`: session 已关闭
- `1005`: `frames` 长度超限
- `3001`: 帧字段解析失败

---

## 四、状态查询类(关键端点展开)

### `GET /api/v1/internal/devices/{device_id}`

**鉴权**:服务间共享密钥
**触发场景**:admin / user 端需要查看设备实时状态(从 Redis 缓存取)

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "device_id": "xx_001_abc",
    "online": true,
    "last_seen_at": "2026-09-25T14:00:00Z",
    "firmware_version": "v1.2.3",
    "connect_type": "tcp",
    "session_id": 12345,
    "ports": {
      "total": 10,
      "free": 7,
      "charging": 3,
      "fault": 0
    },
    "realtime": {
      "voltage_v": "220.5",
      "current_a": "3.20",
      "power_w": "704.0",
      "temperature_c": "32.5",
      "meter_total_kwh": "1234.567"
    },
    "linked_ota": null,                 // 或 "running" 调度详情
    "fault_active": false
  }
}
```

**业务逻辑**:
1. Redis 取 `device:realtime:$device_id`(由 telemetry 上行时 fill,TTL 60 s)
2. Redis miss → 降级查 `gateway_db.device` + 最近一条 `telemetry` 帧
3. 缓存命中直接返回

**错误码**:
- `2001`: 设备未注册
- `2002`: 设备已停用

### `GET /api/v1/internal/devices/{device_id}/ports`

**鉴权**:服务间共享密钥

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "device_id": "xx_001_abc",
    "ports": [
      {
        "port_id": "xx_001_01",
        "port_index": 1,
        "status": "free",                 // free / charging / full / fault / disabled
        "current_order_id": null,
        "voltage_v": "220.5",
        "current_a": "0.00",
        "last_charge_at": "2026-09-24T18:30:00Z"
      },
      {
        "port_id": "xx_001_02",
        "port_index": 2,
        "status": "charging",
        "current_order_id": 12345,
        "voltage_v": "220.8",
        "current_a": "3.50",
        "last_charge_at": "2026-09-25T14:00:00Z"
      }
    ]
  }
}
```

**业务逻辑**:
1. 查 `gateway_db.device.ports`(JSON 字段,§ db/gateway.md 表设计)→ 端口元数据(总数 / 编号)
2. 端口状态从 `device:realtime:$device_id` 取(每个端口的实时状态)
3. `current_order_id` 从 Redis `port:current_order:$port_id` 取(由 user 服务在启动充电时 fill,TTL 与充电时长一致)

### `GET /api/v1/internal/orders/{order_id}`

**鉴权**:服务间共享密钥
**触发场景**:admin 服务查订单详情时调用

**响应(200)**:
```json
{
  "code": 0,
  "data": {
    "order_id": 12345,
    "order_no": "CH20260925140000123",
    "device_id": "xx_001_abc",
    "port_id": "xx_001_01",
    "user_id": 8888,
    "started_at": "2026-09-25T14:00:00Z",
    "ended_at": "2026-09-25T15:00:00Z",
    "duration_seconds": 3600,
    "meter_start_kwh": "0.000",
    "meter_end_kwh": "0.520",
    "meter_delta_kwh": "0.520",
    "avg_power_w": "700.00",
    "max_temperature_c": "36.2",
    "charge_state_timeline": [
      { "at": "2026-09-25T14:00:00Z", "state": "charging" },
      { "at": "2026-09-25T15:00:00Z", "state": "full" }
    ],
    "fault_count": 0,
    "frames_count": 720               // 帧数(每 5 s 一帧,共 3600 s)
  }
}
```

**业务逻辑**:
1. 查 `gateway_db.charge_order`(订单在本表,§ 需求 § 8 订单模型)
2. JOIN `device_session`(拿到 session_id)→ LEFT JOIN `telemetry`(取 meter_start / meter_end + 曲线)
3. `charge_state_timeline` 从 `device_event_log`(状态变更帧聚合)
4. 详情数据可能从 `billing_db` / `user_db` 拼接(价费分离 / 支付明细),但 gateway **只返回本服务数据**,价费 / 支付部分由 admin 调 user / billing 补齐

---

## 五、控制指令类(关键端点展开)

### `POST /api/v1/internal/start-charge`

**鉴权**:服务间共享密钥
**触发场景**:user 服务在用户扫码启动充电时调用
**业务目标**:下发 MQTT / TCP 启动指令 → 等待设备 ACK → 返回结果

**请求体**:
```json
{
  "device_id": "xx_001_abc",
  "port_id": "xx_001_01",
  "order_id": 12345,
  "user_id": 8888,
  "max_power_w": 500,
  "max_duration_sec": 7200,
  "issued_by": "user-service"
}
```

**响应(200,同步 ACK)**:
```json
{
  "code": 0,
  "data": {
    "cmd_id": "uuid-abc123",
    "device_ack": "success",           // success / failed / timeout
    "device_ack_at": "2026-09-25T14:00:05Z",
    "started_at": "2026-09-25T14:00:05Z",
    "session_id": 12345
  }
}
```

**响应(200,超时未 ACK)**:
```json
{
  "code": 0,
  "data": {
    "cmd_id": "uuid-abc123",
    "device_ack": "timeout",
    "timeout_ms": 5000,
    "next_action": "user_should_retry_or_refund"
  }
}
```

**业务逻辑**:
1. 校验 `device_id` 在线(`device:realtime:$device_id` 在线 + `status='active'`)→ 否则返回 `2002`
2. 校验 `port_id` 空闲(`port:current_order:$port_id` 不存在)→ 否则返回 `2003`
3. 生成 `cmd_id`(UUID v4)
4. **下发指令**:
   - TCP 设备 → 通过 TCP 帧下发 → 等 ACK(超时 5 s)
   - MQTT 设备 → 发 `charge/{vendor_id}/{device_id}/cmd` topic → 订阅 `charge/.../cmd/ack` 等 ACK
5. **ACK 处理**:
   - `success` → INSERT `device_event_log(event='charge_started', order_id, started_at)` + Redis `port:current_order:$port_id = order_id` TTL 7200 s
   - `failed` → UPDATE `device_event_log` + 返回 `2004`
   - `timeout` → 返回 `next_action: user_should_retry_or_refund`(user 端根据超时决定重试 / 退款)
6. **事务边界**:事件写入 + Redis 缓存同事务;指令下发失败回滚
7. 发 `comp_tx_stream` 事件 → 通知 billing 计费开始

**错误码**:
- `2002`: 设备离线 / 已停用
- `2003`: 端口被占用
- `2004`: 充电指令下发失败(设备拒绝)
- `5003`: 设备无响应(超时)

---

### `POST /api/v1/internal/stop-charge`

**鉴权**:服务间共享密钥
**触发场景**:user 服务在用户主动停止 / 充电完成 / 退款触发停止时调用

**请求体**:
```json
{
  "device_id": "xx_001_abc",
  "port_id": "xx_001_01",
  "order_id": 12345,
  "stop_reason": "user_request"      // user_request / full / fault / refund
}
```

**业务逻辑**:
1. 同 `start-charge` 校验(在线 / 端口占用)
2. 下发 `stop_charge` 指令 → 等 ACK(超时 5 s)
3. ACK 成功 → UPDATE `device_event_log(event='charge_stopped', ended_at, stop_reason)` + DEL Redis `port:current_order:$port_id`
4. 发 `charge_ended_stream` 事件(§ 5.1)→ billing / user 消费
5. 同步返回 ACK 结果

### `POST /api/v1/internal/devices/{device_id}/firmware-push`

**鉴权**:服务间共享密钥
**触发场景**:worker 消费 `ota_schedule_stream` 后调用
**业务目标**:推 OTA 固件到指定设备(走 MQTT)

**请求体**:
```json
{
  "schedule_id": 8,
  "package_id": 12,
  "target_version": "v1.3.0",
  "download_url": "https://oss.example.com/firmware/xd-v1.3.0.bin",
  "sha256": "abc123...",
  "signature": "厂商 RSA 签名(用 vendor.public_key 验签)",
  "force_install": false,
  "rollback_to": "v1.2.3"
}
```

**业务逻辑**:
1. 校验 `device_id` 在线 → 否则加入 OTA 队列等设备下次上线
2. **签名验证**(应用层):用 `vendor.public_key` 校验 `signature`(`SHA256(firmware) → 厂商私钥签`)→ 失败返回 `3003`(固件版本不兼容 / 验签失败)
3. MQTT 发 `charge/{vendor_id}/{device_id}/firmware` topic
4. INSERT `ota_command(cmd_type='firmware_push', schedule_id, device_id, sent_at, status='sent')`
5. 异步等设备 ACK(通过 `firmware-status` 端点轮询,或 device 主动调 `ack-received`)
6. ACK `success` → UPDATE `ota_command(status='success', ack_at)`,发 `alert_stream` 通知
7. ACK `failed` → 触发自动回滚(若 `auto_rollback_on_failure=true`)

**错误码**:
- `2002`: 设备离线(入队等下次上线)
- `3003`: 固件签名验证失败
- `5003`: MQTT 下行失败

---

## 六、Stream 发布约定(gateway 作为生产者)

gateway 服务**主动发布**到以下 Stream(沿用 § 5.1):

| Stream | 触发场景 | Payload 关键字段 |
| --- | --- | --- |
| `device_event_stream` | 设备状态变更(上线 / 下线 / 充电开始 / 充电结束 / 故障) | `device_id`、`event_type`、`port_id`、`order_id`(若关联)、`timestamp` |
| `alert_stream` | 七重防护字段越界 / 通信中断 / 温度过高等 | `device_id`、`alert_type`、`severity`、`metric`、`value`、`threshold`、`timestamp` |
| `comp_tx_stream` | 跨服务事务补偿(下游 ACK / 失败) | `tx_id`、`status`、`result`、`timestamp` |

> **不发布** `charge_ended_stream` / `refund_required_stream` / `invoice_required_stream`(由 billing 服务生产,gateway 不参与)。

---

## 七、设备本地阈值检测(§ 3.1.4)

- **不预设默认值**(避免误报 / 漏报)
- 阈值规则由客户运营在 admin PC 后台"告警规则"自配(`alert_rule` 表)
- gateway 检测到越界时**直接发 `alert_stream`**(不本地阈值判断)
- 设备本地阈值检测**默认关闭**(`alert_thresholds_enabled=false` 由 register 时下发)

---

## 文档维护

- 修改本文件需在 PR 标题写 `api(gateway): <简短描述>`
- **协议 adapter 新增 / 修改**必须同步更新 `docs/db/gateway.md`(`vendor` 表)
- 新增 HTTP 端点必须同步更新 `services/gateway/src/openapi.rs`
- **Stream 名必须从 § 5.1 8 个真实 Stream 中选**,新增 Stream 必须先在技术规格登记
- CI 检查:OpenAPI 规范与本文件端点清单一致(脚本 `tools/check-api-consistency.ts`)