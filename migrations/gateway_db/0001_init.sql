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
  `event_key` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '设备事件持久化幂等键',
  `response_json` json DEFAULT NULL COMMENT '业务处理响应快照 JSON',
  `status` enum('pending','done') NOT NULL DEFAULT 'pending' COMMENT '当前业务状态；取值 pending / done',
  `attempts` int unsigned NOT NULL DEFAULT '0' COMMENT '已尝试执行次数',
  `next_attempt_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '下次执行尝试时间',
  `last_error` varchar(255) DEFAULT NULL COMMENT '最近一次执行错误信息',
  PRIMARY KEY (`event_key`),
  KEY `idx_due` (`status`,`next_attempt_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='原生刷卡事件投递与重试';

-- charge_command：设备启动命令和确认状态
CREATE TABLE `charge_command` (
  `command_id` varchar(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT '设备指令 ID',
  `stop_command_id` varchar(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT '设备停止命令 ID',
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `payment_order_id` bigint unsigned NOT NULL COMMENT '支付订单 ID',
  `order_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务订单编号',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `port_no` tinyint unsigned NOT NULL COMMENT '设备充电端口号，从 1 开始',
  `port_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '充电端口二维码唯一编码',
  `port_id` bigint unsigned DEFAULT NULL COMMENT '充电端口 ID',
  `owns_port` tinyint(1) NOT NULL DEFAULT '0' COMMENT '是否由本条启动命令占用端口，0 否、1 是',
  `status` enum('pending','sent','acked','stopping','rejected') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '当前业务状态；取值 pending / sent / acked / stopping / rejected',
  `session_id` varchar(36) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '设备协议会话标识',
  `stop_session_id` varchar(36) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '设备停止命令的协议会话标识',
  `sent_at` datetime(3) DEFAULT NULL COMMENT '设备指令发送时间',
  `stop_sent_at` datetime(3) DEFAULT NULL COMMENT '设备停止指令发送时间',
  `error` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '设备指令执行错误说明',
  `result_reported` tinyint(1) NOT NULL DEFAULT '0' COMMENT '是否已向订单服务同步设备执行结果，0 否、1 是',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `charge_mode` tinyint unsigned NOT NULL DEFAULT '4' COMMENT '设备协议充电模式码，由协议适配器解释',
  `quantity` smallint unsigned NOT NULL DEFAULT '1' COMMENT '设备启动指令数量，单位随充电模式和协议',
  `result_code` tinyint unsigned DEFAULT NULL COMMENT '设备启动应答结果码，0 表示成功',
  `ack_at` datetime(3) DEFAULT NULL COMMENT '设备启动确认时间',
  `consumer_type` tinyint unsigned NOT NULL DEFAULT '2' COMMENT '设备协议中的充电消费方式码',
  `card_number` int unsigned NOT NULL DEFAULT '0' COMMENT '设备协议中的在线卡卡号',
  `card_balance_units` smallint unsigned NOT NULL DEFAULT '0' COMMENT '设备在线卡余额，单位为 0.1 元',
  PRIMARY KEY (`command_id`),
  UNIQUE KEY `uk_stop` (`stop_command_id`),
  UNIQUE KEY `uk_charge` (`charge_order_id`),
  UNIQUE KEY `uk_order` (`order_no`),
  KEY `idx_unreported` (`result_reported`,`created_at`),
  KEY `idx_ack_session` (`device_id`,`port_no`,`session_id`),
  KEY `idx_stop_ack_session` (`device_id`,`port_no`,`stop_session_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备启动命令和确认状态';

-- charge_end_delivery：设备结束事件投递与重试
CREATE TABLE `charge_end_delivery` (
  `device_event_id` bigint unsigned NOT NULL COMMENT '设备事件 ID',
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `payload_json` json NOT NULL COMMENT '任务执行或投递载荷 JSON',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`device_event_id`),
  KEY `idx_order` (`charge_order_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='设备结束事件投递与重试';

-- charge_stop_command：设备停止命令和确认状态
CREATE TABLE `charge_stop_command` (
  `command_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备指令 ID',
  `start_command_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备启动命令 ID',
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `order_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务订单编号',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `port_no` tinyint unsigned NOT NULL COMMENT '设备充电端口号，从 1 开始',
  `port_id` bigint unsigned NOT NULL COMMENT '充电端口 ID',
  `status` enum('pending','sent','acked','rejected') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '当前业务状态；取值 pending / sent / acked / rejected',
  `session_id` varchar(36) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '设备协议会话标识',
  `sent_at` datetime(3) DEFAULT NULL COMMENT '设备指令发送时间',
  `meter_json` json DEFAULT NULL COMMENT '设备最终计量和停止原因 JSON',
  `result_reported` tinyint(1) NOT NULL DEFAULT '0' COMMENT '是否已向订单服务同步设备执行结果，0 否、1 是',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `result_code` tinyint unsigned DEFAULT NULL COMMENT '设备返回的停止应答码',
  `rejected_at` datetime(3) DEFAULT NULL COMMENT '设备停止请求被拒绝时间',
  PRIMARY KEY (`command_id`),
  UNIQUE KEY `uk_order` (`charge_order_id`),
  KEY `idx_unreported` (`result_reported`,`charge_order_id`),
  KEY `idx_rejected` (`status`,`rejected_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备停止命令和确认状态';

-- device：设备元数据
-- last_heartbeat_at 仅记录 A4 心跳，不能由登录时间或其他报文代替。
CREATE TABLE `device` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号，由对应厂商协议解释',
  `vendor_id` bigint unsigned NOT NULL COMMENT '设备厂商 ID',
  `station_id` bigint unsigned DEFAULT NULL COMMENT '所属站点(冗余自 admin_db)',
  `port_count` tinyint unsigned NOT NULL DEFAULT '2' COMMENT '端口数',
  `model` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '设备型号',
  `firmware_version` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '设备当前固件版本',
  `mac_addr` varchar(32) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '设备 MAC 地址',
  `status` enum('enabled','disabled','retired') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'enabled' COMMENT '当前业务状态；取值 enabled / disabled / retired',
  `last_seen_at` datetime(3) DEFAULT NULL COMMENT '设备最近在线时间',
  `last_heartbeat_at` datetime(3) DEFAULT NULL COMMENT '最近心跳时间',
  `last_ip` varchar(45) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '设备最近连接的 IP 地址',
  `registered_at` datetime(3) DEFAULT NULL COMMENT '设备首次注册时间',
  `config_json` json DEFAULT NULL COMMENT '业务配置 JSON',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  `deleted_by` bigint unsigned DEFAULT NULL COMMENT '删除人 ID',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_device_id` (`device_id`,`deleted_at`),
  KEY `idx_vendor` (`vendor_id`),
  KEY `idx_station` (`station_id`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备元数据';

-- device_event：设备事件及原始上下文
CREATE TABLE `device_event` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `event_key` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '设备事件持久化幂等键',
  `protocol_name` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '产生事件的协议名称',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `event_type` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '事件类型',
  `port_no` tinyint unsigned NOT NULL DEFAULT '0' COMMENT '设备事件的端口号，0 表示无端口、255 表示设备级故障',
  `event_json` json NOT NULL COMMENT '原始设备协议事件上下文 JSON',
  `received_at` datetime(3) NOT NULL COMMENT '报告接收时间',
  `processed_at` datetime(3) DEFAULT NULL COMMENT '设备事件成功同步到业务服务的时间，NULL 表示尚未完成',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_event_key` (`event_key`),
  KEY `idx_unprocessed` (`processed_at`,`id`),
  KEY `idx_device_time` (`device_id`,`received_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备事件及原始上下文';

-- device_port：设备端口(每端口独立二维码)
CREATE TABLE `device_port` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `port_no` tinyint unsigned NOT NULL COMMENT '设备充电端口号，从 1 开始',
  `port_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '端口二维码字符串',
  `status` enum('idle','charging','full','fault','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'idle' COMMENT '当前业务状态；取值 idle / charging / full / fault / disabled',
  `current_order_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '正在充电订单 ID(冗余自 user_db.charge_order)',
  `last_telemetry_at` datetime(3) DEFAULT NULL COMMENT '最近遥测时间',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_device_port` (`device_id`,`port_no`),
  UNIQUE KEY `uk_port_code` (`port_code`,`deleted_at`),
  KEY `idx_device` (`device_id`,`port_no`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备端口(每端口独立二维码)';

-- device_provision：设备开通请求幂等记录
CREATE TABLE `device_provision` (
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `request_json` json NOT NULL COMMENT '业务请求参数快照 JSON',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`device_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备开通请求幂等记录';

-- device_session：设备长连接会话
CREATE TABLE `device_session` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `session_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '会话 ID(UUID)',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `protocol` enum('tcp','mqtt') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备协议标识；取值 tcp / mqtt',
  `remote_addr` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '设备连接的远端地址与端口',
  `started_at` datetime(3) NOT NULL COMMENT '业务启动时间',
  `last_active_at` datetime(3) NOT NULL COMMENT '连接最近活动时间',
  `ended_at` datetime(3) DEFAULT NULL COMMENT '业务结束时间',
  `close_reason` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '设备长连接结束原因',
  `bytes_in` bigint unsigned DEFAULT '0' COMMENT '会话累计接收字节数',
  `bytes_out` bigint unsigned DEFAULT '0' COMMENT '会话累计发送字节数',
  `frames_in` bigint unsigned DEFAULT '0' COMMENT '会话累计接收协议帧数',
  `frames_out` bigint unsigned DEFAULT '0' COMMENT '会话累计发送协议帧数',
  `created_month` date NOT NULL COMMENT '分区字段',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
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
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `event_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '事件唯一标识，用于追踪和幂等处理',
  `stream` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'Redis Stream 名称',
  `envelope_json` json NOT NULL COMMENT '待发布事件的统一信封 JSON',
  `status` enum('pending','published','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '当前业务状态；取值 pending / published / failed',
  `retry_count` int unsigned NOT NULL DEFAULT '0' COMMENT '已执行重试次数',
  `last_error` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '最近一次执行错误信息',
  `scheduled_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '计划执行时间',
  `published_at` datetime(3) DEFAULT NULL COMMENT '事件发布完成时间',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_event` (`event_id`),
  KEY `idx_status_sched` (`status`,`scheduled_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='与业务事务一起写入的待投递事件';

-- ota_command：OTA 指令跟踪(ACK 状态)
CREATE TABLE `ota_command` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `command_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '指令唯一 ID(UUID)',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `package_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'OTA 固件包 ID',
  `package_version` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '下发的固件包版本',
  `status` enum('pending','sent','acked','failed','timeout') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '当前业务状态；取值 pending / sent / acked / failed / timeout',
  `sent_at` datetime(3) DEFAULT NULL COMMENT '设备指令发送时间',
  `acked_at` datetime(3) DEFAULT NULL COMMENT '确认时间',
  `failed_at` datetime(3) DEFAULT NULL COMMENT '执行失败时间',
  `failure_reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '业务执行失败原因',
  `retry_count` int unsigned NOT NULL DEFAULT '0' COMMENT '已执行重试次数',
  `created_month` date NOT NULL COMMENT '月分区归属日期，取对应月份第一天',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
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
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `session_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '设备协议会话标识',
  `direction` enum('in','out') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '协议帧方向；in 设备上行、out 平台下行',
  `frame_hex` varbinary(2048) NOT NULL COMMENT '原始协议帧的十六进制编码',
  `parsed_json` json DEFAULT NULL COMMENT '原始协议帧的解析结果 JSON',
  `parse_status` enum('ok','error') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'ok' COMMENT '原始协议帧的解析状态；取值 ok / error',
  `error_msg` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '执行错误信息',
  `ts` datetime(3) NOT NULL COMMENT '数据采集时间',
  `created_month` date NOT NULL COMMENT '月分区归属日期，取对应月份第一天',
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
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `port_no` tinyint unsigned DEFAULT NULL COMMENT '遥测所属端口号，为 NULL 时表示设备级指标',
  `metric` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'voltage_v / current_a / temperature_c / battery_soc / power_w / meter_kwh',
  `value_num` decimal(18,6) DEFAULT NULL COMMENT '遥测数值，单位随指标',
  `value_json` json DEFAULT NULL COMMENT '非数值遥测内容 JSON',
  `ts` datetime(3) NOT NULL COMMENT '采集时间戳',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
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
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `port_no` tinyint unsigned DEFAULT NULL COMMENT '聚合所属端口号，为 NULL 时表示设备级指标',
  `metric` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '指标名称',
  `bucket_start` datetime(3) NOT NULL COMMENT '聚合桶起点',
  `bucket_month` date NOT NULL COMMENT '分区字段',
  `avg_value` decimal(18,6) NOT NULL COMMENT '聚合桶内有效样本的平均值，单位随指标',
  `min_value` decimal(18,6) NOT NULL COMMENT '聚合桶内有效样本的最小值，单位随指标',
  `max_value` decimal(18,6) NOT NULL COMMENT '聚合桶内有效样本的最大值，单位随指标',
  `count` int unsigned NOT NULL COMMENT '聚合桶内有效样本数量',
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
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `port_no` tinyint unsigned DEFAULT NULL COMMENT '聚合所属端口号，为 NULL 时表示设备级指标',
  `metric` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '指标名称',
  `bucket_start` datetime(3) NOT NULL COMMENT '聚合桶起始时间',
  `bucket_month` date NOT NULL COMMENT '月分区归属日期，取对应月份第一天',
  `avg_value` decimal(18,6) NOT NULL COMMENT '聚合桶内有效样本的平均值，单位随指标',
  `min_value` decimal(18,6) NOT NULL COMMENT '聚合桶内有效样本的最小值，单位随指标',
  `max_value` decimal(18,6) NOT NULL COMMENT '聚合桶内有效样本的最大值，单位随指标',
  `count` int unsigned NOT NULL COMMENT '聚合桶内有效样本数量',
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
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `vendor_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '唯一编码,如 vendor_a',
  `vendor_name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '厂商名',
  `adapter_class` varchar(256) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'Go 注册的协议适配器标识，如 dc589',
  `protocol` enum('tcp','mqtt','hybrid') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'tcp' COMMENT '设备协议标识；取值 tcp / mqtt / hybrid',
  `config_json` json DEFAULT NULL COMMENT '厂商私有配置(JSON,字段在适配器内解析)',
  `status` enum('enabled','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'enabled' COMMENT '当前业务状态；取值 enabled / disabled',
  `enabled_at` datetime(3) DEFAULT NULL COMMENT '启用时间',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  `deleted_by` bigint unsigned DEFAULT NULL COMMENT '删除人 ID',
  `live_vendor_code` varchar(64) COLLATE utf8mb4_unicode_ci GENERATED ALWAYS AS ((case when (`deleted_at` is null) then `vendor_code` else NULL end)) STORED COMMENT '未删除厂商的业务编码，用于约束有效厂商编码唯一',
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
