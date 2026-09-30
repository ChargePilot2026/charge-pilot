-- +goose NO TRANSACTION
-- +goose Up

-- ChargePilot gateway_db 初始化结构
-- 设备网关：厂商协议、连接会话、计量数据及指令回执
-- 发布前基线：直接修改 CREATE TABLE，重建空库；不叠加增量 ALTER。
-- 表间关联由所属服务维护；此文件只初始化当前库。
-- 计量、事件和审计表保留月分区及 p_max，后续月份由维护任务创建。

SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci;
SET sql_mode = 'STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION,ERROR_FOR_DIVISION_BY_ZERO,NO_ZERO_DATE,NO_ZERO_IN_DATE';

-- card_event_delivery：原生刷卡事件投递与重试
CREATE TABLE `card_event_delivery` (
  `event_key` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `response_json` json DEFAULT NULL,
  `status` enum('pending','done') NOT NULL DEFAULT 'pending',
  `attempts` int unsigned NOT NULL DEFAULT '0',
  `next_attempt_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `last_error` varchar(255) DEFAULT NULL,
  PRIMARY KEY (`event_key`),
  KEY `idx_due` (`status`,`next_attempt_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- charge_command：设备启动命令和确认状态
CREATE TABLE `charge_command` (
  `command_id` varchar(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `stop_command_id` varchar(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `charge_order_id` bigint unsigned NOT NULL,
  `payment_order_id` bigint unsigned NOT NULL,
  `order_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `port_no` tinyint unsigned NOT NULL,
  `port_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `port_id` bigint unsigned DEFAULT NULL,
  `owns_port` tinyint(1) NOT NULL DEFAULT '0',
  `status` enum('pending','sent','acked','stopping','rejected') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `session_id` varchar(36) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `stop_session_id` varchar(36) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `sent_at` datetime(3) DEFAULT NULL,
  `stop_sent_at` datetime(3) DEFAULT NULL,
  `error` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `result_reported` tinyint(1) NOT NULL DEFAULT '0',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `charge_mode` tinyint unsigned NOT NULL DEFAULT '4',
  `quantity` smallint unsigned NOT NULL DEFAULT '1',
  `result_code` tinyint unsigned DEFAULT NULL,
  `ack_at` datetime(3) DEFAULT NULL,
  `consumer_type` tinyint unsigned NOT NULL DEFAULT '2',
  `card_number` int unsigned NOT NULL DEFAULT '0',
  `card_balance_units` smallint unsigned NOT NULL DEFAULT '0',
  PRIMARY KEY (`command_id`),
  UNIQUE KEY `uk_stop` (`stop_command_id`),
  UNIQUE KEY `uk_charge` (`charge_order_id`),
  UNIQUE KEY `uk_order` (`order_no`),
  KEY `idx_unreported` (`result_reported`,`created_at`),
  KEY `idx_ack_session` (`device_id`,`port_no`,`session_id`),
  KEY `idx_stop_ack_session` (`device_id`,`port_no`,`stop_session_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- charge_end_delivery：设备结束事件投递与重试
CREATE TABLE `charge_end_delivery` (
  `device_event_id` bigint unsigned NOT NULL,
  `charge_order_id` bigint unsigned NOT NULL,
  `payload_json` json NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`device_event_id`),
  KEY `idx_order` (`charge_order_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- charge_stop_command：设备停止命令和确认状态
CREATE TABLE `charge_stop_command` (
  `command_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `start_command_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `charge_order_id` bigint unsigned NOT NULL,
  `order_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `port_no` tinyint unsigned NOT NULL,
  `port_id` bigint unsigned NOT NULL,
  `status` enum('pending','sent','acked','rejected') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `session_id` varchar(36) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `sent_at` datetime(3) DEFAULT NULL,
  `meter_json` json DEFAULT NULL,
  `result_reported` tinyint(1) NOT NULL DEFAULT '0',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `result_code` tinyint unsigned DEFAULT NULL COMMENT '设备返回的停止应答码',
  `rejected_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`command_id`),
  UNIQUE KEY `uk_order` (`charge_order_id`),
  KEY `idx_unreported` (`result_reported`,`charge_order_id`),
  KEY `idx_rejected` (`status`,`rejected_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- device：设备元数据
-- last_heartbeat_at 仅记录 A4 心跳，不能由登录时间或其他报文代替。
CREATE TABLE `device` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一 ID,8-32 字符',
  `vendor_id` bigint unsigned NOT NULL,
  `station_id` bigint unsigned DEFAULT NULL COMMENT '所属站点(冗余自 admin_db)',
  `port_count` tinyint unsigned NOT NULL DEFAULT '2' COMMENT '端口数',
  `model` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `firmware_version` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `mac_addr` varchar(32) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `status` enum('enabled','disabled','retired') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'enabled',
  `last_seen_at` datetime(3) DEFAULT NULL,
  `last_heartbeat_at` datetime(3) DEFAULT NULL,
  `last_ip` varchar(45) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `registered_at` datetime(3) DEFAULT NULL,
  `config_json` json DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  `deleted_by` bigint unsigned DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_device_id` (`device_id`,`deleted_at`),
  KEY `idx_vendor` (`vendor_id`),
  KEY `idx_station` (`station_id`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备元数据';

-- device_event：设备事件及原始上下文
CREATE TABLE `device_event` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `event_key` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `protocol_name` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `event_type` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL,
  `port_no` tinyint unsigned NOT NULL DEFAULT '0',
  `event_json` json NOT NULL,
  `received_at` datetime(3) NOT NULL,
  `processed_at` datetime(3) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_event_key` (`event_key`),
  KEY `idx_unprocessed` (`processed_at`,`id`),
  KEY `idx_device_time` (`device_id`,`received_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- device_port：设备端口(每端口独立二维码)
CREATE TABLE `device_port` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `port_no` tinyint unsigned NOT NULL COMMENT '端口号(1..port_count)',
  `port_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '端口二维码字符串',
  `status` enum('idle','charging','full','fault','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'idle',
  `current_order_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '正在充电订单 ID(冗余自 user_db.charge_order)',
  `last_telemetry_at` datetime(3) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_device_port` (`device_id`,`port_no`),
  UNIQUE KEY `uk_port_code` (`port_code`,`deleted_at`),
  KEY `idx_device` (`device_id`,`port_no`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备端口(每端口独立二维码)';

-- device_provision：设备开通请求幂等记录
CREATE TABLE `device_provision` (
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `request_json` json NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`device_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- device_session：设备长连接会话
CREATE TABLE `device_session` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `session_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '会话 ID(UUID)',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `protocol` enum('tcp','mqtt') COLLATE utf8mb4_unicode_ci NOT NULL,
  `remote_addr` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `started_at` datetime(3) NOT NULL,
  `last_active_at` datetime(3) NOT NULL,
  `ended_at` datetime(3) DEFAULT NULL,
  `close_reason` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `bytes_in` bigint unsigned DEFAULT '0',
  `bytes_out` bigint unsigned DEFAULT '0',
  `frames_in` bigint unsigned DEFAULT '0',
  `frames_out` bigint unsigned DEFAULT '0',
  `created_month` date NOT NULL COMMENT '分区字段',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`,`created_month`),
  UNIQUE KEY `uk_session` (`session_id`,`created_month`),
  KEY `idx_device` (`device_id`,`created_month`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备长连接会话'
/*!50100 PARTITION BY RANGE (to_days(`created_month`))
(PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB,
 PARTITION p_2026m10 VALUES LESS THAN (740286) ENGINE = InnoDB,
 PARTITION p_2026m11 VALUES LESS THAN (740316) ENGINE = InnoDB,
 PARTITION p_2026m12 VALUES LESS THAN (740347) ENGINE = InnoDB,
 PARTITION p_max VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */;

-- event_outbox：与业务事务一起写入的待投递事件
CREATE TABLE `event_outbox` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `event_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `stream` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `envelope_json` json NOT NULL,
  `status` enum('pending','published','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `retry_count` int unsigned NOT NULL DEFAULT '0',
  `last_error` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `scheduled_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `published_at` datetime(3) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_event` (`event_id`),
  KEY `idx_status_sched` (`status`,`scheduled_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ota_command：OTA 指令跟踪(ACK 状态)
CREATE TABLE `ota_command` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `command_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '指令唯一 ID(UUID)',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `package_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `package_version` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `status` enum('pending','sent','acked','failed','timeout') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `sent_at` datetime(3) DEFAULT NULL,
  `acked_at` datetime(3) DEFAULT NULL,
  `failed_at` datetime(3) DEFAULT NULL,
  `failure_reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `retry_count` int unsigned NOT NULL DEFAULT '0',
  `created_month` date NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`,`created_month`),
  UNIQUE KEY `uk_command` (`command_id`,`created_month`),
  KEY `idx_device_status` (`device_id`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='OTA 指令跟踪(ACK 状态)'
/*!50100 PARTITION BY RANGE (to_days(`created_month`))
(PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB,
 PARTITION p_2026m10 VALUES LESS THAN (740286) ENGINE = InnoDB,
 PARTITION p_2026m11 VALUES LESS THAN (740316) ENGINE = InnoDB,
 PARTITION p_2026m12 VALUES LESS THAN (740347) ENGINE = InnoDB,
 PARTITION p_max VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */;

-- raw_frame_log：TCP/MQTT 原始帧日志(排障)
CREATE TABLE `raw_frame_log` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `session_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `direction` enum('in','out') COLLATE utf8mb4_unicode_ci NOT NULL,
  `frame_hex` varbinary(2048) NOT NULL,
  `parsed_json` json DEFAULT NULL,
  `parse_status` enum('ok','error') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'ok',
  `error_msg` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `ts` datetime(3) NOT NULL,
  `created_month` date NOT NULL,
  PRIMARY KEY (`id`,`created_month`),
  KEY `idx_device_ts` (`device_id`,`ts`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='TCP/MQTT 原始帧日志(排障)'
/*!50100 PARTITION BY RANGE (to_days(`created_month`))
(PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB,
 PARTITION p_2026m10 VALUES LESS THAN (740286) ENGINE = InnoDB,
 PARTITION p_2026m11 VALUES LESS THAN (740316) ENGINE = InnoDB,
 PARTITION p_2026m12 VALUES LESS THAN (740347) ENGINE = InnoDB,
 PARTITION p_max VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */;

-- telemetry：设备遥测原始记录(按 ts 分区,1 月保留)
CREATE TABLE `telemetry` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `port_no` tinyint unsigned DEFAULT NULL,
  `metric` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'voltage_v / current_a / temperature_c / battery_soc / power_w / meter_kwh',
  `value_num` decimal(18,6) DEFAULT NULL,
  `value_json` json DEFAULT NULL,
  `ts` datetime(3) NOT NULL COMMENT '采集时间戳',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`,`ts`),
  KEY `idx_device_metric_ts` (`device_id`,`metric`,`ts`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备遥测原始记录(按 ts 分区,1 月保留)'
/*!50100 PARTITION BY RANGE (to_days(`ts`))
(PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB,
 PARTITION p_2026m10 VALUES LESS THAN (740286) ENGINE = InnoDB,
 PARTITION p_2026m11 VALUES LESS THAN (740316) ENGINE = InnoDB,
 PARTITION p_2026m12 VALUES LESS THAN (740347) ENGINE = InnoDB,
 PARTITION p_max VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */;

-- telemetry_aggregate_15min：遥测 15 分钟聚合
CREATE TABLE `telemetry_aggregate_15min` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `port_no` tinyint unsigned DEFAULT NULL,
  `metric` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL,
  `bucket_start` datetime(3) NOT NULL COMMENT '聚合桶起点',
  `bucket_month` date NOT NULL COMMENT '分区字段',
  `avg_value` decimal(18,6) NOT NULL,
  `min_value` decimal(18,6) NOT NULL,
  `max_value` decimal(18,6) NOT NULL,
  `count` int unsigned NOT NULL,
  PRIMARY KEY (`id`,`bucket_month`),
  UNIQUE KEY `uk_bucket` (`device_id`,`port_no`,`metric`,`bucket_start`,`bucket_month`),
  KEY `idx_metric_time` (`metric`,`bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='遥测 15 分钟聚合'
/*!50100 PARTITION BY RANGE (to_days(`bucket_month`))
(PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB,
 PARTITION p_2026m10 VALUES LESS THAN (740286) ENGINE = InnoDB,
 PARTITION p_2026m11 VALUES LESS THAN (740316) ENGINE = InnoDB,
 PARTITION p_2026m12 VALUES LESS THAN (740347) ENGINE = InnoDB,
 PARTITION p_2027q1 VALUES LESS THAN (740437) ENGINE = InnoDB,
 PARTITION p_2027q2 VALUES LESS THAN (740528) ENGINE = InnoDB,
 PARTITION p_2027q3 VALUES LESS THAN (740620) ENGINE = InnoDB,
 PARTITION p_2027q4 VALUES LESS THAN (740712) ENGINE = InnoDB,
 PARTITION p_max VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */;

-- telemetry_aggregate_hourly：遥测小时聚合
CREATE TABLE `telemetry_aggregate_hourly` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `port_no` tinyint unsigned DEFAULT NULL,
  `metric` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL,
  `bucket_start` datetime(3) NOT NULL,
  `bucket_month` date NOT NULL,
  `avg_value` decimal(18,6) NOT NULL,
  `min_value` decimal(18,6) NOT NULL,
  `max_value` decimal(18,6) NOT NULL,
  `count` int unsigned NOT NULL,
  PRIMARY KEY (`id`,`bucket_month`),
  UNIQUE KEY `uk_bucket` (`device_id`,`port_no`,`metric`,`bucket_start`,`bucket_month`),
  KEY `idx_metric_time` (`metric`,`bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='遥测小时聚合'
/*!50100 PARTITION BY RANGE (to_days(`bucket_month`))
(PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB,
 PARTITION p_2026m10 VALUES LESS THAN (740286) ENGINE = InnoDB,
 PARTITION p_2026m11 VALUES LESS THAN (740316) ENGINE = InnoDB,
 PARTITION p_2026m12 VALUES LESS THAN (740347) ENGINE = InnoDB,
 PARTITION p_2027q1 VALUES LESS THAN (740437) ENGINE = InnoDB,
 PARTITION p_2027q2 VALUES LESS THAN (740528) ENGINE = InnoDB,
 PARTITION p_2027q3 VALUES LESS THAN (740620) ENGINE = InnoDB,
 PARTITION p_2027q4 VALUES LESS THAN (740712) ENGINE = InnoDB,
 PARTITION p_max VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */;

-- vendor：硬件厂适配器注册表
CREATE TABLE `vendor` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `vendor_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '唯一编码,如 vendor_a',
  `vendor_name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '厂商名',
  `adapter_class` varchar(256) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'Go 注册的协议适配器标识，如 dc589',
  `protocol` enum('tcp','mqtt','hybrid') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'tcp',
  `config_json` json DEFAULT NULL COMMENT '厂商私有配置(JSON,字段在适配器内解析)',
  `status` enum('enabled','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'enabled',
  `enabled_at` datetime(3) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  `deleted_by` bigint unsigned DEFAULT NULL,
  `live_vendor_code` varchar(64) COLLATE utf8mb4_unicode_ci GENERATED ALWAYS AS ((case when (`deleted_at` is null) then `vendor_code` else NULL end)) STORED,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_vendor_code` (`vendor_code`,`deleted_at`),
  UNIQUE KEY `uk_vendor_live_code` (`live_vendor_code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='硬件厂适配器注册表';

-- 初始化数据：内置权限、角色及系统默认配置。业务与演示数据另行创建。

-- +goose Down
-- 仅供一次性开发/测试库回退；删除当前库全部业务表。
DROP TABLE IF EXISTS `vendor`;
DROP TABLE IF EXISTS `telemetry_aggregate_hourly`;
DROP TABLE IF EXISTS `telemetry_aggregate_15min`;
DROP TABLE IF EXISTS `telemetry`;
DROP TABLE IF EXISTS `raw_frame_log`;
DROP TABLE IF EXISTS `ota_command`;
DROP TABLE IF EXISTS `event_outbox`;
DROP TABLE IF EXISTS `device_session`;
DROP TABLE IF EXISTS `device_provision`;
DROP TABLE IF EXISTS `device_port`;
DROP TABLE IF EXISTS `device_event`;
DROP TABLE IF EXISTS `device`;
DROP TABLE IF EXISTS `charge_stop_command`;
DROP TABLE IF EXISTS `charge_end_delivery`;
DROP TABLE IF EXISTS `charge_command`;
DROP TABLE IF EXISTS `card_event_delivery`;
