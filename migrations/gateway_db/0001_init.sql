-- ============================================
-- ChargePilot · gateway_db 初始迁移
-- 文档: docs/db/gateway.md + docs/技术规格.md § 4
-- 创建时间: 2026-09
-- 表清单(8 张):
--   vendor / device / device_session / telemetry /
--   telemetry_aggregate_15min / telemetry_aggregate_hourly /
--   raw_frame_log / ota_command
-- ============================================

SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci;
SET sql_mode = 'STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION,ERROR_FOR_DIVISION_BY_ZERO,NO_ZERO_DATE,NO_ZERO_IN_DATE';

-- ---------------- 厂商适配器注册 ----------------
CREATE TABLE IF NOT EXISTS `vendor` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `vendor_code` VARCHAR(64) NOT NULL COMMENT '唯一编码,如 vendor_a',
  `vendor_name` VARCHAR(128) NOT NULL COMMENT '厂商名',
  `adapter_class` VARCHAR(256) NOT NULL COMMENT 'Rust 路径如 vendor_a::Adapter',
  `protocol` ENUM('tcp', 'mqtt', 'hybrid') NOT NULL DEFAULT 'tcp',
  `config_json` JSON DEFAULT NULL COMMENT '厂商私有配置(JSON,字段在适配器内解析)',
  `status` ENUM('enabled', 'disabled') NOT NULL DEFAULT 'enabled',
  `enabled_at` DATETIME(3) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  `deleted_by` BIGINT UNSIGNED DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_vendor_code` (`vendor_code`, `deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='硬件厂适配器注册表';

-- ---------------- 设备主表 ----------------
CREATE TABLE IF NOT EXISTS `device` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `device_id` VARCHAR(64) NOT NULL COMMENT '设备全局唯一 ID,8-32 字符',
  `vendor_id` BIGINT UNSIGNED NOT NULL,
  `station_id` BIGINT UNSIGNED DEFAULT NULL COMMENT '所属站点(冗余自 admin_db)',
  `port_count` TINYINT UNSIGNED NOT NULL DEFAULT 2 COMMENT '端口数',
  `model` VARCHAR(128) DEFAULT NULL,
  `firmware_version` VARCHAR(64) DEFAULT NULL,
  `mac_addr` VARCHAR(32) DEFAULT NULL,
  `status` ENUM('enabled','disabled','retired') NOT NULL DEFAULT 'enabled',
  `last_seen_at` DATETIME(3) DEFAULT NULL,
  `last_ip` VARCHAR(45) DEFAULT NULL,
  `registered_at` DATETIME(3) DEFAULT NULL,
  `config_json` JSON DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  `deleted_by` BIGINT UNSIGNED DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_device_id` (`device_id`, `deleted_at`),
  KEY `idx_vendor` (`vendor_id`),
  KEY `idx_station` (`station_id`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备元数据';

-- ---------------- 端口(冗余自 admin_db,加速查询)----------------
CREATE TABLE IF NOT EXISTS `device_port` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `device_id` VARCHAR(64) NOT NULL,
  `port_no` TINYINT UNSIGNED NOT NULL COMMENT '端口号(1..port_count)',
  `port_code` VARCHAR(64) NOT NULL COMMENT '端口二维码字符串',
  `status` ENUM('idle','charging','full','fault','disabled') NOT NULL DEFAULT 'idle',
  `current_order_id` VARCHAR(64) DEFAULT NULL COMMENT '正在充电订单 ID(冗余自 user_db.charge_order)',
  `last_telemetry_at` DATETIME(3) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_port_code` (`port_code`, `deleted_at`),
  KEY `idx_device` (`device_id`, `port_no`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备端口(每端口独立二维码)';

-- ---------------- 设备会话(TCP/MQTT 长连接跟踪)----------------
CREATE TABLE IF NOT EXISTS `device_session` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `session_id` VARCHAR(64) NOT NULL COMMENT '会话 ID(UUID)',
  `device_id` VARCHAR(64) NOT NULL,
  `protocol` ENUM('tcp', 'mqtt') NOT NULL,
  `remote_addr` VARCHAR(64) DEFAULT NULL,
  `started_at` DATETIME(3) NOT NULL,
  `last_active_at` DATETIME(3) NOT NULL,
  `ended_at` DATETIME(3) DEFAULT NULL,
  `close_reason` VARCHAR(64) DEFAULT NULL,
  `bytes_in` BIGINT UNSIGNED DEFAULT 0,
  `bytes_out` BIGINT UNSIGNED DEFAULT 0,
  `frames_in` BIGINT UNSIGNED DEFAULT 0,
  `frames_out` BIGINT UNSIGNED DEFAULT 0,
  `created_month` DATE NOT NULL COMMENT '分区字段',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`, `created_month`),
  UNIQUE KEY `uk_session` (`session_id`, `created_month`),
  KEY `idx_device` (`device_id`, `created_month`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备长连接会话'
PARTITION BY RANGE (TO_DAYS(`created_month`)) (
  PARTITION p_init VALUES LESS THAN (TO_DAYS('2026-10-01')),
  PARTITION p_2026m10 VALUES LESS THAN (TO_DAYS('2026-11-01')),
  PARTITION p_2026m11 VALUES LESS THAN (TO_DAYS('2026-12-01')),
  PARTITION p_2026m12 VALUES LESS THAN (TO_DAYS('2027-01-01')),
  PARTITION p_max VALUES LESS THAN MAXVALUE
);

-- ---------------- 设备遥测原始记录(高频,1 月保留)----------------
CREATE TABLE IF NOT EXISTS `telemetry` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `device_id` VARCHAR(64) NOT NULL,
  `port_no` TINYINT UNSIGNED DEFAULT NULL,
  `metric` VARCHAR(32) NOT NULL COMMENT 'voltage_v / current_a / temperature_c / battery_soc / power_w / meter_kwh',
  `value_num` DECIMAL(18, 6) DEFAULT NULL,
  `value_json` JSON DEFAULT NULL,
  `ts` DATETIME(3) NOT NULL COMMENT '采集时间戳',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`, `ts`),
  KEY `idx_device_metric_ts` (`device_id`, `metric`, `ts`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备遥测原始记录(按 ts 分区,1 月保留)'
PARTITION BY RANGE (TO_DAYS(`ts`)) (
  PARTITION p_init VALUES LESS THAN (TO_DAYS('2026-10-01')),
  PARTITION p_2026m10 VALUES LESS THAN (TO_DAYS('2026-11-01')),
  PARTITION p_2026m11 VALUES LESS THAN (TO_DAYS('2026-12-01')),
  PARTITION p_2026m12 VALUES LESS THAN (TO_DAYS('2027-01-01')),
  PARTITION p_max VALUES LESS THAN MAXVALUE
);

-- ---------------- 15 分钟聚合(3 年保留)----------------
CREATE TABLE IF NOT EXISTS `telemetry_aggregate_15min` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `device_id` VARCHAR(64) NOT NULL,
  `port_no` TINYINT UNSIGNED DEFAULT NULL,
  `metric` VARCHAR(32) NOT NULL,
  `bucket_start` DATETIME(3) NOT NULL COMMENT '聚合桶起点',
  `bucket_month` DATE NOT NULL COMMENT '分区字段',
  `avg_value` DECIMAL(18, 6) NOT NULL,
  `min_value` DECIMAL(18, 6) NOT NULL,
  `max_value` DECIMAL(18, 6) NOT NULL,
  `count` INT UNSIGNED NOT NULL,
  PRIMARY KEY (`id`, `bucket_month`),
  UNIQUE KEY `uk_bucket` (`device_id`, `port_no`, `metric`, `bucket_start`, `bucket_month`),
  KEY `idx_metric_time` (`metric`, `bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='遥测 15 分钟聚合'
PARTITION BY RANGE (TO_DAYS(`bucket_month`)) (
  PARTITION p_init VALUES LESS THAN (TO_DAYS('2026-10-01')),
  PARTITION p_2026m10 VALUES LESS THAN (TO_DAYS('2026-11-01')),
  PARTITION p_2026m11 VALUES LESS THAN (TO_DAYS('2026-12-01')),
  PARTITION p_2026m12 VALUES LESS THAN (TO_DAYS('2027-01-01')),
  PARTITION p_2027q1 VALUES LESS THAN (TO_DAYS('2027-04-01')),
  PARTITION p_2027q2 VALUES LESS THAN (TO_DAYS('2027-07-01')),
  PARTITION p_2027q3 VALUES LESS THAN (TO_DAYS('2027-10-01')),
  PARTITION p_2027q4 VALUES LESS THAN (TO_DAYS('2028-01-01')),
  PARTITION p_max VALUES LESS THAN MAXVALUE
);

-- ---------------- 小时聚合(3 年保留)----------------
CREATE TABLE IF NOT EXISTS `telemetry_aggregate_hourly` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `device_id` VARCHAR(64) NOT NULL,
  `port_no` TINYINT UNSIGNED DEFAULT NULL,
  `metric` VARCHAR(32) NOT NULL,
  `bucket_start` DATETIME(3) NOT NULL,
  `bucket_month` DATE NOT NULL,
  `avg_value` DECIMAL(18, 6) NOT NULL,
  `min_value` DECIMAL(18, 6) NOT NULL,
  `max_value` DECIMAL(18, 6) NOT NULL,
  `count` INT UNSIGNED NOT NULL,
  PRIMARY KEY (`id`, `bucket_month`),
  UNIQUE KEY `uk_bucket` (`device_id`, `port_no`, `metric`, `bucket_start`, `bucket_month`),
  KEY `idx_metric_time` (`metric`, `bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='遥测小时聚合'
PARTITION BY RANGE (TO_DAYS(`bucket_month`)) (
  PARTITION p_init VALUES LESS THAN (TO_DAYS('2026-10-01')),
  PARTITION p_2026m10 VALUES LESS THAN (TO_DAYS('2026-11-01')),
  PARTITION p_2026m11 VALUES LESS THAN (TO_DAYS('2026-12-01')),
  PARTITION p_2026m12 VALUES LESS THAN (TO_DAYS('2027-01-01')),
  PARTITION p_2027q1 VALUES LESS THAN (TO_DAYS('2027-04-01')),
  PARTITION p_2027q2 VALUES LESS THAN (TO_DAYS('2027-07-01')),
  PARTITION p_2027q3 VALUES LESS THAN (TO_DAYS('2027-10-01')),
  PARTITION p_2027q4 VALUES LESS THAN (TO_DAYS('2028-01-01')),
  PARTITION p_max VALUES LESS THAN MAXVALUE
);

-- ---------------- 原始 TCP 帧日志(排障)----------------
CREATE TABLE IF NOT EXISTS `raw_frame_log` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `device_id` VARCHAR(64) NOT NULL,
  `session_id` VARCHAR(64) DEFAULT NULL,
  `direction` ENUM('in', 'out') NOT NULL,
  `frame_hex` VARBINARY(2048) NOT NULL,
  `parsed_json` JSON DEFAULT NULL,
  `parse_status` ENUM('ok','error') NOT NULL DEFAULT 'ok',
  `error_msg` VARCHAR(255) DEFAULT NULL,
  `ts` DATETIME(3) NOT NULL,
  `created_month` DATE NOT NULL,
  PRIMARY KEY (`id`, `created_month`),
  KEY `idx_device_ts` (`device_id`, `ts`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='TCP/MQTT 原始帧日志(排障)'
PARTITION BY RANGE (TO_DAYS(`created_month`)) (
  PARTITION p_init VALUES LESS THAN (TO_DAYS('2026-10-01')),
  PARTITION p_2026m10 VALUES LESS THAN (TO_DAYS('2026-11-01')),
  PARTITION p_2026m11 VALUES LESS THAN (TO_DAYS('2026-12-01')),
  PARTITION p_2026m12 VALUES LESS THAN (TO_DAYS('2027-01-01')),
  PARTITION p_max VALUES LESS THAN MAXVALUE
);

-- ---------------- OTA 指令跟踪 ----------------
CREATE TABLE IF NOT EXISTS `ota_command` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `command_id` VARCHAR(64) NOT NULL COMMENT '指令唯一 ID(UUID)',
  `device_id` VARCHAR(64) NOT NULL,
  `package_id` VARCHAR(64) NOT NULL,
  `package_version` VARCHAR(64) DEFAULT NULL,
  `status` ENUM('pending','sent','acked','failed','timeout') NOT NULL DEFAULT 'pending',
  `sent_at` DATETIME(3) DEFAULT NULL,
  `acked_at` DATETIME(3) DEFAULT NULL,
  `failed_at` DATETIME(3) DEFAULT NULL,
  `failure_reason` VARCHAR(255) DEFAULT NULL,
  `retry_count` INT UNSIGNED NOT NULL DEFAULT 0,
  `created_month` DATE NOT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`, `created_month`),
  UNIQUE KEY `uk_command` (`command_id`, `created_month`),
  KEY `idx_device_status` (`device_id`, `status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='OTA 指令跟踪(ACK 状态)'
PARTITION BY RANGE (TO_DAYS(`created_month`)) (
  PARTITION p_init VALUES LESS THAN (TO_DAYS('2026-10-01')),
  PARTITION p_2026m10 VALUES LESS THAN (TO_DAYS('2026-11-01')),
  PARTITION p_2026m11 VALUES LESS THAN (TO_DAYS('2026-12-01')),
  PARTITION p_2026m12 VALUES LESS THAN (TO_DAYS('2027-01-01')),
  PARTITION p_max VALUES LESS THAN MAXVALUE
);