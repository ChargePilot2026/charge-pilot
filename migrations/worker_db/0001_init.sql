-- ============================================
-- ChargePilot · worker_db 初始迁移
-- 文档: docs/db/worker.md + docs/技术规格.md § 4
-- 表清单(5 张):scheduled_task / task_execution_log / comp_tx_log /
--   dlq_log / retry_queue
-- ============================================

SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `scheduled_task` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `task_code` VARCHAR(64) NOT NULL COMMENT '如 alert_scan / billing_cycle_daily',
  `name` VARCHAR(128) NOT NULL,
  `cron_expr` VARCHAR(64) NOT NULL,
  `enabled` TINYINT(1) NOT NULL DEFAULT 1,
  `last_run_at` DATETIME(3) DEFAULT NULL,
  `next_run_at` DATETIME(3) DEFAULT NULL,
  `config_json` JSON DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_code` (`task_code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='定时任务定义';

CREATE TABLE IF NOT EXISTS `task_execution_log` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `task_code` VARCHAR(64) NOT NULL,
  `started_at` DATETIME(3) NOT NULL,
  `finished_at` DATETIME(3) DEFAULT NULL,
  `status` ENUM('running','success','failed','partial') NOT NULL DEFAULT 'running',
  `affected_rows` BIGINT UNSIGNED DEFAULT NULL,
  `error_msg` VARCHAR(512) DEFAULT NULL,
  `created_month` DATE NOT NULL,
  PRIMARY KEY (`id`, `created_month`),
  KEY `idx_task_time` (`task_code`, `started_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='定时任务执行日志'
PARTITION BY RANGE (TO_DAYS(`created_month`)) (
  PARTITION p_init VALUES LESS THAN (TO_DAYS('2026-10-01')),
  PARTITION p_2026m10 VALUES LESS THAN (TO_DAYS('2026-11-01')),
  PARTITION p_2026m11 VALUES LESS THAN (TO_DAYS('2026-12-01')),
  PARTITION p_2026m12 VALUES LESS THAN (TO_DAYS('2027-01-01')),
  PARTITION p_max VALUES LESS THAN MAXVALUE
);

CREATE TABLE IF NOT EXISTS `comp_tx_log` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `tx_id` VARCHAR(64) NOT NULL,
  `consumer_group` VARCHAR(128) NOT NULL,
  `stream` VARCHAR(64) NOT NULL,
  `payload_hash` VARCHAR(64) DEFAULT NULL,
  `status` ENUM('pending','committed','compensated','failed') NOT NULL DEFAULT 'pending',
  `retry_count` INT UNSIGNED NOT NULL DEFAULT 0,
  `last_error` VARCHAR(255) DEFAULT NULL,
  `created_month` DATE NOT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `committed_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`, `created_month`),
  UNIQUE KEY `uk_tx` (`tx_id`, `created_month`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='跨服务补偿事务'
PARTITION BY RANGE (TO_DAYS(`created_month`)) (
  PARTITION p_init VALUES LESS THAN (TO_DAYS('2026-10-01')),
  PARTITION p_2026m10 VALUES LESS THAN (TO_DAYS('2026-11-01')),
  PARTITION p_2026m11 VALUES LESS THAN (TO_DAYS('2026-12-01')),
  PARTITION p_2026m12 VALUES LESS THAN (TO_DAYS('2027-01-01')),
  PARTITION p_max VALUES LESS THAN MAXVALUE
);

CREATE TABLE IF NOT EXISTS `dlq_log` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `stream` VARCHAR(64) NOT NULL,
  `entry_id` VARCHAR(64) NOT NULL,
  `reason` VARCHAR(512) DEFAULT NULL,
  `payload_json` JSON DEFAULT NULL,
  `status` ENUM('open','replayed','closed') NOT NULL DEFAULT 'open',
  `resolved_by` BIGINT UNSIGNED DEFAULT NULL,
  `resolved_at` DATETIME(3) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `created_month` DATE NOT NULL,
  PRIMARY KEY (`id`, `created_month`),
  KEY `idx_stream_status` (`stream`, `status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='死信队列日志'
PARTITION BY RANGE (TO_DAYS(`created_month`)) (
  PARTITION p_init VALUES LESS THAN (TO_DAYS('2026-10-01')),
  PARTITION p_2026m10 VALUES LESS THAN (TO_DAYS('2026-11-01')),
  PARTITION p_2026m11 VALUES LESS THAN (TO_DAYS('2026-12-01')),
  PARTITION p_2026m12 VALUES LESS THAN (TO_DAYS('2027-01-01')),
  PARTITION p_max VALUES LESS THAN MAXVALUE
);

CREATE TABLE IF NOT EXISTS `retry_queue` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `queue_name` VARCHAR(64) NOT NULL COMMENT 'webhook_retry / pay_retry / ota_retry',
  `payload_json` JSON NOT NULL,
  `run_at` DATETIME(3) NOT NULL,
  `attempt_count` INT UNSIGNED NOT NULL DEFAULT 0,
  `max_attempts` INT UNSIGNED NOT NULL DEFAULT 5,
  `last_error` VARCHAR(255) DEFAULT NULL,
  `status` ENUM('pending','running','done','failed') NOT NULL DEFAULT 'pending',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_queue_run` (`queue_name`, `run_at`, `status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='重试队列(Webhook / 支付 / OTA)';