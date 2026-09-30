-- +goose NO TRANSACTION
-- +goose Up

-- ChargePilot worker_db 初始化结构
-- 后台任务：事件消费、定时执行、重试与死信
-- 发布前基线：直接修改 CREATE TABLE，重建空库；不叠加增量 ALTER。
-- 表间关联由所属服务维护；此文件只初始化当前库。
-- 计量、事件和审计表保留月分区及 p_max，后续月份由维护任务创建。

SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci;
SET sql_mode = 'STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION,ERROR_FOR_DIVISION_BY_ZERO,NO_ZERO_DATE,NO_ZERO_IN_DATE';

-- comp_tx_log：跨服务补偿事务
CREATE TABLE `comp_tx_log` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `tx_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `consumer_group` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `stream` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `payload_hash` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `status` enum('pending','committed','compensated','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `retry_count` int unsigned NOT NULL DEFAULT '0',
  `last_error` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_month` date NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `committed_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`,`created_month`),
  UNIQUE KEY `uk_tx` (`tx_id`,`created_month`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='跨服务补偿事务'
/*!50100 PARTITION BY RANGE (to_days(`created_month`))
(PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB,
 PARTITION p_2026m10 VALUES LESS THAN (740286) ENGINE = InnoDB,
 PARTITION p_2026m11 VALUES LESS THAN (740316) ENGINE = InnoDB,
 PARTITION p_2026m12 VALUES LESS THAN (740347) ENGINE = InnoDB,
 PARTITION p_max VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */;

-- dlq_log：死信队列日志
CREATE TABLE `dlq_log` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `stream` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `entry_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `reason` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `payload_json` json DEFAULT NULL,
  `status` enum('open','replayed','closed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'open',
  `resolved_by` bigint unsigned DEFAULT NULL,
  `resolved_at` datetime(3) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `created_month` date NOT NULL,
  PRIMARY KEY (`id`,`created_month`),
  KEY `idx_stream_status` (`stream`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='死信队列日志'
/*!50100 PARTITION BY RANGE (to_days(`created_month`))
(PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB,
 PARTITION p_2026m10 VALUES LESS THAN (740286) ENGINE = InnoDB,
 PARTITION p_2026m11 VALUES LESS THAN (740316) ENGINE = InnoDB,
 PARTITION p_2026m12 VALUES LESS THAN (740347) ENGINE = InnoDB,
 PARTITION p_max VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */;

-- dlq_replay_cursor：DLQ 重放滑动游标(D21)
CREATE TABLE `dlq_replay_cursor` (
  `stream` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务流名(不含 .dlq 后缀)',
  `last_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '已扫描到的最后一个 DLQ entry id;NULL = 尚未开始',
  `scanned_total` bigint unsigned NOT NULL DEFAULT '0' COMMENT '累计扫描条目数(运维观测用)',
  `replayed_total` bigint unsigned NOT NULL DEFAULT '0' COMMENT '累计成功重放条目数',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`stream`),
  KEY `idx_updated` (`updated_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='DLQ 重放滑动游标(D21)';

-- retry_queue：重试队列(Webhook / 支付 / OTA)
CREATE TABLE `retry_queue` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `queue_name` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'webhook_retry / pay_retry / ota_retry',
  `payload_json` json NOT NULL,
  `run_at` datetime(3) NOT NULL,
  `attempt_count` int unsigned NOT NULL DEFAULT '0',
  `max_attempts` int unsigned NOT NULL DEFAULT '5',
  `last_error` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `status` enum('pending','running','done','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_queue_run` (`queue_name`,`run_at`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='重试队列(Webhook / 支付 / OTA)';

-- scheduled_task：定时任务定义
CREATE TABLE `scheduled_task` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `task_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '如 alert_scan / billing_cycle_daily',
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `cron_expr` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `enabled` tinyint(1) NOT NULL DEFAULT '1',
  `last_run_at` datetime(3) DEFAULT NULL,
  `next_run_at` datetime(3) DEFAULT NULL,
  `config_json` json DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `consecutive_fail_count` int unsigned NOT NULL DEFAULT '0',
  `lease_token` varchar(36) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `lease_until` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_code` (`task_code`),
  KEY `idx_due` (`enabled`,`next_run_at`,`lease_until`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='定时任务定义';

-- task_execution_log：定时任务执行日志
CREATE TABLE `task_execution_log` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `task_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `started_at` datetime(3) NOT NULL,
  `finished_at` datetime(3) DEFAULT NULL,
  `status` enum('running','success','failed','partial') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'running',
  `affected_rows` bigint unsigned DEFAULT NULL,
  `error_msg` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_month` date NOT NULL,
  `triggered_by` enum('cron','admin_api') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'cron',
  `trigger_reason` varchar(256) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  PRIMARY KEY (`id`,`created_month`),
  KEY `idx_task_time` (`task_code`,`started_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='定时任务执行日志'
/*!50100 PARTITION BY RANGE (to_days(`created_month`))
(PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB,
 PARTITION p_2026m10 VALUES LESS THAN (740286) ENGINE = InnoDB,
 PARTITION p_2026m11 VALUES LESS THAN (740316) ENGINE = InnoDB,
 PARTITION p_2026m12 VALUES LESS THAN (740347) ENGINE = InnoDB,
 PARTITION p_max VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */;

-- 初始化数据：内置权限、角色及系统默认配置。业务与演示数据另行创建。

-- scheduled_task 默认记录
INSERT INTO `scheduled_task` (`id`,`task_code`,`name`,`cron_expr`,`enabled`,`last_run_at`,`next_run_at`,`config_json`,`created_at`,`updated_at`,`consecutive_fail_count`,`lease_token`,`lease_until`) VALUES (1,'alert_evaluate','设备告警阈值扫描','*/10 * * * * *',1,NULL,'2026-09-30 19:22:13.142',NULL,'2026-09-30 19:22:13.142','2026-09-30 19:22:13.142',0,NULL,NULL);
INSERT INTO `scheduled_task` (`id`,`task_code`,`name`,`cron_expr`,`enabled`,`last_run_at`,`next_run_at`,`config_json`,`created_at`,`updated_at`,`consecutive_fail_count`,`lease_token`,`lease_until`) VALUES (2,'webhook_dispatch','Webhook 待投递事件扫描','*/10 * * * * *',1,NULL,'2026-09-30 19:22:13.142',NULL,'2026-09-30 19:22:13.142','2026-09-30 19:22:13.142',0,NULL,NULL);

-- +goose Down
-- 仅供一次性开发/测试库回退；删除当前库全部业务表。
DROP TABLE IF EXISTS `task_execution_log`;
DROP TABLE IF EXISTS `scheduled_task`;
DROP TABLE IF EXISTS `retry_queue`;
DROP TABLE IF EXISTS `dlq_replay_cursor`;
DROP TABLE IF EXISTS `dlq_log`;
DROP TABLE IF EXISTS `comp_tx_log`;
