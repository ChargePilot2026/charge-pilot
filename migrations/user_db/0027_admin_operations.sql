-- +goose NO TRANSACTION
-- +goose Up
-- Keep the audit in the same transaction and schema as the affected user record.
CREATE TABLE IF NOT EXISTS `audit_log` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `actor_id` BIGINT UNSIGNED NOT NULL,
  `actor_name` VARCHAR(64) DEFAULT NULL,
  `module` VARCHAR(64) NOT NULL,
  `action` VARCHAR(64) NOT NULL,
  `target_type` VARCHAR(64) DEFAULT NULL,
  `target_id` VARCHAR(64) DEFAULT NULL,
  `request_id` VARCHAR(64) DEFAULT NULL,
  `before_json` JSON DEFAULT NULL,
  `after_json` JSON DEFAULT NULL,
  `client_ip` VARCHAR(45) DEFAULT NULL,
  `created_month` DATE NOT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`, `created_month`),
  KEY `idx_actor_time` (`actor_id`, `created_at`),
  KEY `idx_module_action` (`module`, `action`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='审计日志'
PARTITION BY RANGE (TO_DAYS(`created_month`)) (
  PARTITION p_init VALUES LESS THAN (TO_DAYS('2026-10-01')),
  PARTITION p_2026m10 VALUES LESS THAN (TO_DAYS('2026-11-01')),
  PARTITION p_2026m11 VALUES LESS THAN (TO_DAYS('2026-12-01')),
  PARTITION p_2026m12 VALUES LESS THAN (TO_DAYS('2027-01-01')),
  PARTITION p_max VALUES LESS THAN MAXVALUE
);
ALTER TABLE coupon ADD COLUMN free_minutes INT UNSIGNED NULL,
 ADD COLUMN active_code VARCHAR(64) GENERATED ALWAYS AS (IF(deleted_at IS NULL,code,NULL)) STORED,
 ADD UNIQUE KEY uk_coupon_active_code(active_code);
