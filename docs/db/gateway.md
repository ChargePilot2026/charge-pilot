# gateway_db 数据库表设计

**所属服务**:gateway(对外 :9100 TCP / :1883 MQTT,设备长连接接入)
**Schema 名**:`gateway_db`
**字符集 / 排序规则**:`utf8mb4` / `utf8mb4_unicode_ci`
**引擎**:InnoDB(全表)
**数据库版本**:MySQL 8.4 LTS

> **单客户部署约定**(沿用需求 § 1.2 / § 13.2):gateway_db 是**单客户专用数据库**,所有表都**不带 `customer_id` 列**。

> **软删除策略**:跨 schema 对账见 `docs/cross-reference.md` § 5.5(权威源),本文档通用约定与之一致;若冲突,以 cross-reference 为准。

## 通用约定

| 项目 | 约定 | 例外 |
| --- | --- | --- |
| 主键 | `BIGINT UNSIGNED AUTO_INCREMENT`,字段名 `id` | 无 |
| 时间戳 | `created_at` / `updated_at`,类型 `DATETIME(3)` | 无 |
| **软删除** | 启用:`deleted_at DATETIME(3) NULL` + `deleted_by BIGINT UNSIGNED NULL`;`idx_*_deleted_at` 索引 | **遥测数据 / 帧日志 / 会话数据不软删**(数据量极大,按时间分区 + 物理归档) |
| 索引命名 | `pk_` / `uk_` / `idx_` 前缀 | 无 |
| 外键 | **不声明** | 无 |

## 表清单(6 张)

| 表名 | 业务说明 | 分表策略 | 估算行数(单客户 5 年) |
| --- | --- | --- | --- |
| `vendor` | 厂商字典 | 不分 | ~10 |
| `device` | 设备主表 | 不分 | ~5000 |
| `device_session` | 连接会话(TCP/MQTT) | 按月分区 | ~50 万 |
| `telemetry` | 遥测原始数据 | **按 device_id hash 分 16 张表**(§ 4.8)+ 按月分区 | **1 个月 ≈ 2.2 亿** |
| `telemetry_aggregate_15min` | **15 分钟聚合**(保留 3 年) | 按月分区 | 3 年 ≈ 5000 万 |
| `telemetry_aggregate_hourly` | **小时聚合**(保留 3 年) | 按月分区 | 3 年 ≈ 1.3 亿 |
| `raw_frame_log` | 原始协议帧日志 | 按月分区 | ~3000 万 |
| `ota_command` | OTA 下行指令日志 | 按月分区 | ~1 万 |

> **本文件首批设计全部 8 张表**(gateway_db 表较少,一次性写完)。

### 容量估算假设前提

> **`telemetry` 是单实例 4 核 4 GB 配置下的"地板估算"**,实际增长高度依赖客户场景。

| 假设项 | 取值 | 出处 / 推导 |
| --- | --- | --- |
| 单设备每秒 1 帧(voltage/current)+ 5s 1 帧(temperature)+ 30s 1 帧(meter_kwh/battery_soc) | 综合 ≈ 1.5 帧/秒 | `技术规格.md` § 6.4 |
| 单客户 5000 设备 24h 连续上报 | 86,400 s × 1.5 × 5000 ≈ **6.5 亿/天**;经 hash 16 表分摊 ≈ 每表 4 千万/天 | 但实际约 1/3 设备离线 / 不充电 → **~2 亿/天**为活跃口径 |
| **`telemetry` 1 个月在线 ≈ 2.2 亿**(摘要取数)| 客户首版常保守;实际 1 个月 ≈ 1-2 亿 | 实测后调整 |
| **telemetry_aggregate_15min** 3 年 ≈ 5000 万 | 5000 设备 × 4 次/小时 × 24 h × 365 d × 3 年 ≈ **5.27 亿**;但聚合会聚合相邻空档 → 约 5000 万有效点 | 推算 |
| **telemetry_aggregate_hourly** 3 年 ≈ 1.3 亿 | 5000 设备 × 1 次/小时 × 24 × 365 × 3 ≈ **1.31 亿** | 直推 |
| **raw_frame_log** 5 年 ≈ 3000 万 | 仅在 debug 模式开启 + 数据量 = 遥测 5%;正常生产不开 | 见 `db/gateway.md` 表注释 |
| **device_session** 5 年 ≈ 50 万 | 设备数 × 平均日连接次数 ≈ 5000 × 30 = 15 万/月 | 长连接生命周期 |

> **校准方法**:上线满 3 个月导出真实行数;若 hash 分表后单表行数 > 1 亿(分表触发阈值),执行 `ALTER TABLE` 提前分表。

**设备 ID 格式**(§ 6.2 已定):
- 长度 8-32 字符,字符集 `[a-zA-Z0-9_-]`
- 厂商前缀 2-4 字母(如 `xx_xxxx_xxxx`,`xx` 为厂商代码)
- 大小写敏感

**遥测分表**(§ 4.8 已定):
- `telemetry` 按 `device_id` hash 分 16 张表
- 物理表名 `telemetry_{0..15}`
- sqlx 路由层(`crates/common-db/router.rs`)按 hash 路由

---

## 表 1:`gateway_db.vendor`

**业务说明**:**厂商字典**。每家硬件厂商对应一条记录,定义厂商协议前缀 / 协议版本 / 联系信息。

**关键业务规则**:

- **预置 + 客户自配**:系统初始化时预置几家厂商,客户可新增
- **不软删除**:字典类,启用 / 停用即可

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `vendor_code` | `VARCHAR(8)` | UNIQUE, NOT NULL | — | 厂商代码(2-4 字母,如 `xx`) |
| `vendor_name` | `VARCHAR(64)` | NOT NULL | — | 厂商名称(如"某科技公司") |
| `protocol_version` | `VARCHAR(32)` | NOT NULL | — | 协议版本(如 `v1.0`) |
| `public_key` | `TEXT` | NULL | NULL | 厂商公钥(OTA 固件签名验证用,§ 6.6) |
| `contact_phone` | `VARCHAR(32)` | NULL | NULL | 联系电话 |
| `status` | `ENUM('enabled','disabled')` | NOT NULL | `'enabled'` | 启用 / 停用 |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_vendor` | `id` | 主键 | — |
| `uk_vendor_code` | `vendor_code` | 唯一 | 按 code 查(设备鉴权时用) |
| `idx_vendor_status` | `status` | 普通 | PC 后台查可用的厂商 |

### 约束

- `vendor_code` 必须 2-4 字母(应用层校验)
- `status='disabled'` 时,不再接受该厂商设备的注册(应用层校验)

### 关系

- 一对多 → `device.vendor_id`

### 业务规则

- **预置**:系统初始化脚本 INSERT 5-10 家预置厂商
- **新增**:客户运营在 PC 后台"厂商管理"新增 → 填代码 / 名称 / 协议版本 → INSERT
- **公钥**:OTA 固件验签时用(§ 6.6),客户上传厂商公钥后填

---

## 表 2:`gateway_db.device`

**业务说明**:**设备主表**。记录每台充电桩的基本信息(device_id / 厂商 / 型号 / 所属站点 / 状态 / 固件版本)。

**关键业务规则**:

- **设备唯一标识**:`device_id` (§ 6.2 格式约定)
- **状态字段**:enabled / disabled / maintenance / fault / retired
- 软删除启用:设备退役软删,保留审计(历史订单仍引用 device_id)

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `device_id` | `VARCHAR(32)` | UNIQUE, NOT NULL | — | 设备 ID(§ 6.2 格式) |
| `device_serial_no` | `VARCHAR(64)` | UNIQUE, NOT NULL | — | **设备 SN**(客户首次扫码/录入的设备序列号,需求 § 7.1) |
| `vendor_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `vendor.id` |
| `model` | `VARCHAR(64)` | NOT NULL | — | 设备型号 |
| `station_id` | `BIGINT UNSIGNED` | NULL | NULL | 所属站点(冗余自 admin_db) |
| `total_ports` | `TINYINT UNSIGNED` | NOT NULL | `0` | 端口数 |
| `firmware_version` | `VARCHAR(32)` | NULL | NULL | 当前固件版本 |
| `public_ip` | `VARCHAR(45)` | NULL | NULL | 设备公网 IP(连接时记录) |
| `mac_address` | `VARCHAR(17)` | NULL | NULL | MAC 地址(诊断用) |
| `status` | `ENUM('enabled','disabled','maintenance','fault','retired')` | NOT NULL | `'disabled'` | 设备状态 |
| `last_online_at` | `DATETIME(3)` | NULL | NULL | 最近在线时间 |
| `last_offline_at` | `DATETIME(3)` | NULL | NULL | 最近离线时间 |
| `installed_at` | `DATE` | NULL | NULL | 安装日期 |
| `registered_at` | `DATETIME(3)` | NOT NULL | — | 注册时间(首次连入) |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 创建时间 |
| `updated_at` | `DATETIME(3)` | NOT NULL | — | 更新时间 |
| `deleted_at` | `DATETIME(3)` | NULL | NULL | 软删除时间 |
| `deleted_by` | `BIGINT UNSIGNED` | NULL | NULL | 删除操作者 ID |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_device` | `id` | 主键 | — |
| `uk_device_device_id` | `device_id` | 唯一 | 鉴权 / 查询 |
| `idx_device_vendor_status` | `vendor_id`, `status` | 普通 | 查某厂商的所有设备 |
| `idx_device_station_status` | `station_id`, `status` | 普通 | 查某站点的所有设备 |
| `idx_device_status_online` | `status`, `last_online_at` | 普通 | 找在线 / 离线设备 |
| `idx_device_deleted_at` | `deleted_at` | 普通 | 物理归档扫描 |

### 约束

- `device_id` 必须符合 § 6.2 格式约定(应用层校验)
- `total_ports >= 1`
- `status='enabled'` 时,设备可接受订单;其他状态均拒绝

### 关系

- 多对一 → `vendor.id`
- 多对一 → `admin_db.station.id`(跨服务,无外键)
- 一对多 → `device_session.device_id`
- 一对多 → `telemetry.device_id`(按 hash 路由到 16 张物理表)

### 业务规则

- **注册**:设备首次连入 → 鉴权流(§ 6.2) → 校验 `device_id` 存在 + `status='enabled'` → 接受连接 + INSERT `device_session`
- **状态变更**:`status='enabled' → 'disabled'`(客户运维主动停用)/ `'maintenance'`(维护中)/ `'fault'`(gateway 检测到设备故障)/ `'retired'`(退役,设备物理拆除)
- **在线心跳**:每次连接 / 收到心跳 → UPDATE `last_online_at = NOW()`
- **离线检测**:30 min 内无心跳 → 标记 `status='fault'`(可能离线)+ 告警 + UPDATE `last_offline_at`
- **同步至 admin_db**:worker 周期同步 `device` → `admin_db.device_meta`(§ 3.1.3)

---

## 表 3:`gateway_db.device_session`

**业务说明**:**设备连接会话记录**。每次 TCP/MQTT 连接建立一条记录,断开时 UPDATE 断开时间。**按月分区**,超 6 个月物理归档(只用于排障)。

**关键业务规则**:

- 每次新连接 INSERT,断开 UPDATE
- 不存连接期间的全部数据(数据走 telemetry)
- 按月分区 + 物理归档,不软删

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `device_id` | `VARCHAR(32)` | NOT NULL | — | 设备 ID |
| `session_id` | `VARCHAR(64)` | UNIQUE, NOT NULL | — | 会话 ID(UUID v4,连接时生成) |
| `protocol` | `ENUM('tcp','mqtt')` | NOT NULL | — | 连接协议 |
| `local_ip` | `VARCHAR(45)` | NULL | NULL | 网关本地 IP |
| `local_port` | `INT UNSIGNED` | NULL | NULL | 网关本地端口 |
| `remote_ip` | `VARCHAR(45)` | NULL | NULL | 设备远程 IP |
| `remote_port` | `INT UNSIGNED` | NULL | NULL | 设备远程端口 |
| `connected_at` | `DATETIME(3)` | NOT NULL | — | 连接时间 |
| `disconnected_at` | `DATETIME(3)` | NULL | NULL | 断开时间(NULL = 仍在线) |
| `disconnect_reason` | `ENUM('normal','timeout','auth_failed','server_restart','network_error','device_restart')` | NULL | NULL | 断开原因 |
| `bytes_received` | `BIGINT UNSIGNED` | NOT NULL | `0` | 接收字节数 |
| `bytes_sent` | `BIGINT UNSIGNED` | NOT NULL | `0` | 发送字节数 |
| `partition_key` | `DATE` | NOT NULL | — | 分区键 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_device_session` | `id` | 主键 | — |
| `uk_device_session_id` | `session_id` | 唯一 | 按会话 ID 查 |
| `idx_device_session_device_connected` | `device_id`, `connected_at` | 普通 | 查某设备的连接历史 |
| `idx_device_session_active` | `device_id`, `disconnected_at` | 普通 | 查某设备的"活跃会话"(disconnected_at IS NULL) |

### 约束

- `disconnected_at IS NULL` 表示当前在线(应用层同一 device_id 只允许 1 条活跃记录)
- `bytes_received` / `bytes_sent` 在断开时统计

### 关系

- 多对一 → `device.id`(跨表逻辑关联,无外键)

### 业务规则

- **新建**:TCP/MQTT 连接建立 + 鉴权通过 → INSERT `device_session(disconnected_at=NULL)`
- **更新**:连接断开 → UPDATE `disconnected_at=NOW(), disconnect_reason`
- **重复连接**:同一 `device_id` 已存在活跃会话(`disconnected_at IS NULL`)→ 旧会话 UPDATE 断开(`disconnect_reason='device_restart'`)+ 新会话 INSERT
- **物理归档**:worker 每日扫表 → `disconnected_at < NOW() - 6 MONTH` → `DELETE`(DROP PARTITION)

---

## 表 4:`gateway_db.telemetry`

**业务说明**:**遥测数据**(核心,数据量极大)。每次设备上报遥测(电流 / 电压 / 温度 / SOC / 功率 / 电量 / 继电器状态 / 充电状态 / 故障码)→ INSERT 一条。**按 device_id hash 分 16 张物理表**(`telemetry_0` ~ `telemetry_15`,§ 4.8 已定)。

**关键业务规则**:

- **分表路由**:sqlx 路由层按 `CRC32(device_id) % 16` 路由到对应物理表
- **数据量极大**:5000 设备 × 1 Hz 上报(仅充电中 4h/天) × 1 个月 ≈ 2.2 亿行(分 16 张 = 单表 1.4 亿行,MySQL 可支撑)
- **保留期**(方案 A 决策):
  - **原始数据保留 1 个月**:排障窗口够用,超 1 个月聚合后物理删除
  - **15 分钟聚合保留 3 年**:长期趋势分析(`telemetry_aggregate_15min`)
  - **小时聚合保留 3 年**:中期分析(`telemetry_aggregate_hourly`)
- **聚合 + 清理**:worker 每日扫表 → 原始超 1 月的按小时聚合 + 按 15 分钟聚合双写 → 物理删除原始
- **时间戳**:设备时间戳 + 服务端时间戳并存(`timestamp_drift_ms` 字段记录偏差,§ 6.2)
- **不软删除**

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `device_id` | `VARCHAR(32)` | NOT NULL | — | 设备 ID |
| `port_id` | `VARCHAR(32)` | NOT NULL | — | 端口 ID |
| `vendor_id` | `VARCHAR(8)` | NOT NULL | — | 厂商 ID |
| `device_ts` | `DATETIME(3)` | NOT NULL | — | 设备上报时间戳(RTC,可能漂移) |
| `server_ts` | `DATETIME(3)` | NOT NULL | — | 服务端接收时间戳(权威) |
| `timestamp_drift_ms` | `INT` | NULL | NULL | 设备 vs 服务端时间偏差(毫秒) |
| `voltage_v` | `DECIMAL(5,2)` | NULL | NULL | 电压(V) |
| `current_a` | `DECIMAL(5,2)` | NULL | NULL | 电流(A) |
| `temperature_c` | `DECIMAL(5,2)` | NULL | NULL | 温度(°C) |
| `battery_soc` | `TINYINT UNSIGNED` | NULL | NULL | SOC(0-100) |
| `power_w` | `DECIMAL(10,2)` | NULL | NULL | 功率(W) |
| `meter_kwh` | `DECIMAL(10,3)` | NULL | NULL | 累计电量(kWh) |
| `relay_status` | `ENUM('closed','open')` | NULL | NULL | 继电器状态 |
| `fault_code` | `INT UNSIGNED` | NULL | NULL | 故障码(0 = 正常) |
| `charge_state` | `ENUM('idle','charging','full','fault')` | NULL | NULL | 充电状态 |
| `slot_id` | `SMALLINT UNSIGNED` | NULL | NULL | 端口槽位 ID |
| `created_at` | `DATETIME(3)` | NOT NULL | — | 入库时间(服务端) |
| `partition_key` | `DATE` | NOT NULL | — | 按月分区键(冗余 `created_at`) |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_telemetry` | `id` | 主键 | — |
| `idx_telemetry_device_server_ts` | `device_id`, `server_ts` | 普通 | 查某设备最近 N 分钟遥测 |
| `idx_telemetry_port_server_ts` | `port_id`, `server_ts` | 普通 | 查某端口最近遥测(充电中) |
| `idx_telemetry_device_created` | `device_id`, `created_at` | 普通 | 数据归档扫描 |

### 约束

- `voltage_v` 范围 0-500V(应用层)
- `current_a` 范围 0-100A(应用层)
- `battery_soc` 范围 0-100(应用层)
- `timestamp_drift_ms` 绝对值 > 30 min 时,标记异常(§ 6.2,计费以服务端时间为准)

### 关系

- 多对一 → `device.id`(跨表逻辑关联)

### 业务规则

- **入库**:gateway 收到设备遥测帧 → 解析 → 七重防护校验(§ 6.4)→ INSERT 物理表
- **充电中快照**:`user-api` 收到小程序轮询时查最新 N 条 → 缓存到 Redis(`snapshot:{order_id}`,TTL 10s)
- **告警触发**:`alert_rule` 匹配 → 发布 `alert_stream` 事件
- **聚合清理**(方案 A,§ 4.6 修订):
  - worker 每小时跑一次聚合任务 → 把上一小时的原始 telemetry 按 15 分钟 / 小时双粒度聚合 → 写 `telemetry_aggregate_15min` / `telemetry_aggregate_hourly`
  - worker 每日扫表 → 原始超 1 月的物理删除
  - 聚合表保留 3 年,超期按月分区 DROP PARTITION
- **路由层**:`crates/common-db/router.rs` 中 `telemetry::route(device_id)` 函数返回物理表名,业务代码不直接拼表名

---

## 表 5:`gateway_db.raw_frame_log`

**业务说明**:**原始协议帧日志**(排障用)。每次设备上行帧 + 服务端下行帧都记录。**按月分区**,超 3 个月物理归档(短保留期,数据量大)。

**关键业务规则**:

- **不软删除**:纯日志,按月分区 + 物理归档
- **二进制字段**:原始帧用 `VARBINARY` 存储(避免编码问题)

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `device_id` | `VARCHAR(32)` | NOT NULL | — | 设备 ID |
| `direction` | `ENUM('rx','tx')` | NOT NULL | — | 方向:rx 接收 / tx 发送 |
| `protocol` | `ENUM('tcp','mqtt')` | NOT NULL | — | 协议 |
| `frame_type` | `VARCHAR(32)` | NOT NULL | — | 帧类型(心跳 / 遥测 / 事件 / 命令 / OTA 等) |
| `raw_frame` | `VARBINARY(4096)` | NOT NULL | — | 原始帧字节(限长 4KB) |
| `parse_status` | `ENUM('success','parse_failed')` | NOT NULL | `'success'` | 解析状态(排障关键) |
| `parse_error` | `VARCHAR(256)` | NULL | NULL | 解析错误信息(`parse_failed` 时填) |
| `received_at` | `DATETIME(3)` | NOT NULL | — | 接收时间(rx) / 发送时间(tx) |
| `partition_key` | `DATE` | NOT NULL | — | 分区键 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_raw_frame_log` | `id` | 主键 | — |
| `idx_raw_frame_log_device_received` | `device_id`, `received_at` | 普通 | 查某设备最近帧 |
| `idx_raw_frame_log_parse_failed` | `parse_status`, `received_at` | 普通 | 排障(查解析失败) |

### 约束

- `raw_frame` 长度 ≤ 4KB(应用层截断)
- `parse_status='parse_failed'` 时,`parse_error` NOT NULL

### 关系

- 多对一 → `device.id`(跨表逻辑关联)

### 业务规则

- **记录**:每次收发帧都 INSERT(高频,异步批量写)
- **排障**:客户运维排查设备问题时 → 按 `device_id` + 时间范围查原始帧 → 解析重放
- **物理归档**:worker 每日扫表 → `received_at < NOW() - 3 MONTH` → `DELETE`(DROP PARTITION)

---

## 表 6:`gateway_db.gateway_db.ota_command`

**业务说明**:**OTA 下行指令日志**。记录每次下发给设备的 OTA 指令(推送 / 取消 / 回滚)。**按月分区**。

**关键业务规则**:

- 仅记录下行 OTA 指令(上行结果通过 `device_event_stream` 消费)
- **不软删除**

### 字段定义

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `device_id` | `VARCHAR(32)` | NOT NULL | — | 设备 ID |
| `schedule_id` | `BIGINT UNSIGNED` | NOT NULL | — | 关联 `admin_db.ota_schedule.id` |
| `command_type` | `ENUM('download','apply','rollback','cancel')` | NOT NULL | — | 指令类型 |
| `firmware_url` | `VARCHAR(512)` | NULL | NULL | 固件 URL(`download` 时填) |
| `firmware_sha256` | `CHAR(64)` | NULL | NULL | 固件 SHA-256 |
| `firmware_signature` | `VARCHAR(512)` | NULL | NULL | 厂商签名 |
| `command_status` | `ENUM('sent','acked','timeout','failed')` | NOT NULL | `'sent'` | 指令状态 |
| `sent_at` | `DATETIME(3)` | NOT NULL | — | 发送时间 |
| `acked_at` | `DATETIME(3)` | NULL | NULL | 设备确认时间 |
| `failure_reason` | `VARCHAR(256)` | NULL | NULL | 失败原因 |
| `partition_key` | `DATE` | NOT NULL | — | 分区键 |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_ota_command` | `id` | 主键 | — |
| `idx_ota_command_device_sent` | `device_id`, `sent_at` | 普通 | 查某设备的 OTA 历史 |
| `idx_ota_command_schedule` | `schedule_id`, `command_type` | 普通 | 反查某调度的所有指令 |
| `idx_ota_command_status_sent` | `command_status`, `sent_at` | 普通 | 查超时 / 失败的指令 |

### 约束

- `command_status IN ('acked','timeout','failed')` 时,`acked_at` 或 `failure_reason` 至少一个 NOT NULL

### 关系

- 多对一 → `admin_db.ota_schedule.id`(跨服务,无外键)
- 多对一 → `device.id`(跨表逻辑关联)

### 业务规则

- **下发指令**:worker 消费 `ota_schedule_stream` → 通过 MQTT 下行 → INSERT 本表
- **超时检测**:30 min 内未收到设备 ACK(`command_status='sent'` 持续 30 min)→ 视为超时(§ 6.6 双触发)→ `command_status='timeout'` + 触发回滚
- **物理归档**:worker 每日扫表 → `sent_at < NOW() - 12 MONTH` → `DELETE`(DROP PARTITION)

---

**gateway_db 全部 6 张表设计完成**

> **下一文件**:`docs/db/billing.md`(billing_db,计费计算 + 分账执行 + 提现)。

---

# 增补:遥测聚合表(方案 A 修订)

## 表 7:`gateway_db.telemetry_aggregate_15min`

**业务说明**:**15 分钟粒度聚合表**。worker 每小时把上一小时的原始 `telemetry` 按 15 分钟分桶聚合 → 写入本表。用于长期趋势分析 / 报表 / 排障。**保留 3 年**。

**关键业务规则**:

- **聚合粒度**:每设备每 15 分钟一条(每天 96 条)
- **聚合方式**:AVG(电流/电压/温度)+ MAX(电流/电压/温度)+ MIN(温度)+ END_SOC(SOC 末尾值)+ END_METER(累计电量末尾值)+ SAMPLE_COUNT(原始条数)
- **保留 3 年**:按月分区,36 个分区,超期 DROP PARTITION
- **数据量估算**:5000 设备 × 96 条/天 × 365 天 × 3 年 = **5260 万行**(单表可支撑)
- **不软删除**

### 字段定义

> **P0-2 修正**:按月分区表 → 主键 / 唯一键加 `bucket_month` generated column(MySQL 8.4 强制要求分区字段出现在每个 UNIQUE / PRIMARY KEY)。

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `device_id` | `VARCHAR(32)` | NOT NULL | — | 设备 ID |
| `bucket_start` | `DATETIME(3)` | NOT NULL | — | 15 分钟桶起点(如 `14:00:00` / `14:15:00`) |
| `voltage_v_avg` | `DECIMAL(5,2)` | NULL | NULL | 电压 AVG |
| `voltage_v_max` | `DECIMAL(5,2)` | NULL | NULL | 电压 MAX |
| `current_a_avg` | `DECIMAL(5,2)` | NULL | NULL | 电流 AVG |
| `current_a_max` | `DECIMAL(5,2)` | NULL | NULL | 电流 MAX |
| `temperature_c_avg` | `DECIMAL(5,2)` | NULL | NULL | 温度 AVG |
| `temperature_c_max` | `DECIMAL(5,2)` | NULL | NULL | 温度 MAX |
| `temperature_c_min` | `DECIMAL(5,2)` | NULL | NULL | 温度 MIN |
| `battery_soc_end` | `TINYINT UNSIGNED` | NULL | NULL | SOC 桶末尾值 |
| `meter_kwh_end` | `DECIMAL(10,3)` | NULL | NULL | 累计电量桶末尾值 |
| `sample_count` | `INT UNSIGNED` | NOT NULL | `0` | 聚合的原始条数(异常检测用) |
| `aggregation_run_at` | `DATETIME(3)` | NOT NULL | — | 聚合执行时间 |
| `bucket_month` | `DATE` | GENERATED ALWAYS AS (DATE_FORMAT(`bucket_start`, '%Y-%m-01')) STORED | — | **P0-2 分区字段** |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_telemetry_aggregate_15min` | `id`, `bucket_month` | 主键 | MySQL 8.4 分区约束 |
| **`uk_telemetry_aggregate_15min_device_bucket`** | `device_id`, `bucket_start`, `bucket_month` | 唯一 | 一设备一时间窗一条聚合(分区字段必带) |
| `idx_telemetry_aggregate_15min_bucket` | `bucket_start` | 普通 | 时间范围查询 |

### 分区策略

按 `bucket_month` 范围分区(滚动保留 36 个月):

```sql
PARTITION BY RANGE (TO_DAYS(bucket_month)) (
  PARTITION p2026m01 VALUES LESS THAN (TO_DAYS('2026-02-01')),
  ...
  PARTITION pmax VALUES LESS THAN MAXVALUE
);
```

### 约束

- 同一 `(device_id, bucket_start)` 唯一(应用层 + 唯一约束)
- `sample_count > 0`(没数据不进聚合表)

### 关系

- 多对一 → `device.id`(跨表逻辑关联)

### 业务规则

- **聚合触发**:worker 每小时跑一次(每小时 05 分开始)→ 查上一小时原始 telemetry → 按 15 分钟分桶(每个桶 = 900 秒窗口)聚合 → 批量 UPSERT 本表
- **幂等**:UPSERT 重复执行安全(基于唯一约束)
- **数据补录**:原始数据物理删除后,聚合表仍是真实历史(不可重新聚合)
- **查询**:PC 后台"设备历史"页 → 选时间窗(1 天 / 1 周 / 1 月 / 1 年)→ 自动选 15 分钟或小时聚合表查询
- **物理归档**:worker 每日扫表 → `bucket_start < NOW() - 3 YEAR` → `DROP PARTITION`(按月分区)

---

## 表 8:`gateway_db.telemetry_aggregate_hourly`

**业务说明**:**小时粒度聚合表**。与 `telemetry_aggregate_15min` 双粒度:15 分钟用于精细时间窗分析,小时用于月度 / 季度 / 年度报表。**保留 3 年**。

**关键业务规则**:

- **聚合粒度**:每设备每小时一条(每天 24 条)
- **数据量估算**:5000 设备 × 24 条/天 × 365 × 3 年 = **1.31 亿行**(单表可支撑)
- **不软删除**

### 字段定义

> **P0-2 修正**:按月分区表 → 主键 / 唯一键加 `hour_month` generated column(MySQL 8.4 强制要求分区字段出现在每个 UNIQUE / PRIMARY KEY)。

| 字段 | 类型 | 约束 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | `BIGINT UNSIGNED` | PK, AUTO_INCREMENT | — | 主键 |
| `device_id` | `VARCHAR(32)` | NOT NULL | — | 设备 ID |
| `hour_start` | `DATETIME(3)` | NOT NULL | — | 小时桶起点(如 `14:00:00`) |
| `voltage_v_avg` | `DECIMAL(5,2)` | NULL | NULL | 电压 AVG |
| `voltage_v_max` | `DECIMAL(5,2)` | NULL | NULL | 电压 MAX |
| `current_a_avg` | `DECIMAL(5,2)` | NULL | NULL | 电流 AVG |
| `current_a_max` | `DECIMAL(5,2)` | NULL | NULL | 电流 MAX |
| `power_w_avg` | `DECIMAL(10,2)` | NULL | NULL | 功率 AVG(仅小时聚合新增,15 分钟聚合无) |
| `power_w_max` | `DECIMAL(10,2)` | NULL | NULL | 功率 MAX |
| `temperature_c_avg` | `DECIMAL(5,2)` | NULL | NULL | 温度 AVG |
| `temperature_c_max` | `DECIMAL(5,2)` | NULL | NULL | 温度 MAX |
| `temperature_c_min` | `DECIMAL(5,2)` | NULL | NULL | 温度 MIN |
| `battery_soc_end` | `TINYINT UNSIGNED` | NULL | NULL | SOC 末尾 |
| `meter_kwh_end` | `DECIMAL(10,3)` | NULL | NULL | 累计电量末尾 |
| `sample_count` | `INT UNSIGNED` | NOT NULL | `0` | 聚合的原始条数 |
| `aggregation_run_at` | `DATETIME(3)` | NOT NULL | — | 聚合执行时间 |
| `hour_month` | `DATE` | GENERATED ALWAYS AS (DATE_FORMAT(`hour_start`, '%Y-%m-01')) STORED | — | **P0-2 分区字段** |

### 索引

| 索引名 | 字段 | 类型 | 用途 |
| --- | --- | --- | --- |
| `pk_telemetry_aggregate_hourly` | `id`, `hour_month` | 主键 | MySQL 8.4 分区约束 |
| **`uk_telemetry_aggregate_hourly_device_hour`** | `device_id`, `hour_start`, `hour_month` | 唯一 | 一设备一小时一条(分区字段必带) |
| `idx_telemetry_aggregate_hourly_hour` | `hour_start` | 普通 | 时间范围查询 |

### 分区策略

按 `hour_month` 范围分区(滚动保留 36 个月):

### 约束

- 同一 `(device_id, hour_start)` 唯一

### 关系

- 多对一 → `device.id`

### 业务规则

- **聚合触发**:worker 每小时跑一次 → 把上一小时原始 telemetry 按小时聚合 → 批量 UPSERT
- **幂等**:UPSERT 重复执行安全
- **查询**:PC 后台"月度报表" / "设备趋势" → 自动选小时聚合表
- **物理归档**:worker 每日扫表 → `hour_start < NOW() - 3 YEAR` → `DROP PARTITION`

---

**gateway_db 全部 8 张表设计完成**
