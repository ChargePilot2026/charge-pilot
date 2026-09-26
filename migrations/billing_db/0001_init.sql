-- ============================================
-- ChargePilot · billing_db 初始迁移
-- 文档: docs/db/billing.md + docs/技术规格.md § 4
-- 表清单(5 张):fee_calculation / settlement / settlement_party_amount /
--   pricing_tier_snapshot / withdraw_request
-- ============================================

SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `fee_calculation` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `calculation_no` VARCHAR(64) NOT NULL,
  `order_no` VARCHAR(64) NOT NULL,
  `charge_order_id` BIGINT UNSIGNED NOT NULL,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `station_id` BIGINT UNSIGNED DEFAULT NULL,
  `pricing_rule_id` BIGINT UNSIGNED DEFAULT NULL,
  `pricing_rule_version` INT UNSIGNED DEFAULT NULL,
  `charged_kwh` DECIMAL(12, 4) NOT NULL,
  `charged_seconds` INT UNSIGNED NOT NULL,
  `peak_kwh` DECIMAL(12, 4) DEFAULT NULL,
  `off_kwh` DECIMAL(12, 4) DEFAULT NULL,
  `electric_cents` BIGINT NOT NULL,
  `service_cents` BIGINT NOT NULL,
  `total_cents` BIGINT NOT NULL,
  `breakdown_json` JSON DEFAULT NULL,
  `created_month` DATE NOT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`, `created_month`),
  UNIQUE KEY `uk_no` (`calculation_no`, `created_month`),
  KEY `idx_order` (`order_no`, `created_month`),
  KEY `idx_user` (`user_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='计费明细'
PARTITION BY RANGE (TO_DAYS(`created_month`)) (
  PARTITION p_init VALUES LESS THAN (TO_DAYS('2026-10-01')),
  PARTITION p_2026m10 VALUES LESS THAN (TO_DAYS('2026-11-01')),
  PARTITION p_2026m11 VALUES LESS THAN (TO_DAYS('2026-12-01')),
  PARTITION p_2026m12 VALUES LESS THAN (TO_DAYS('2027-01-01')),
  PARTITION p_max VALUES LESS THAN MAXVALUE
);

CREATE TABLE IF NOT EXISTS `settlement` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `settlement_no` VARCHAR(64) NOT NULL,
  `split_template_id` BIGINT UNSIGNED NOT NULL,
  `mode` ENUM('mode_a','mode_b') NOT NULL DEFAULT 'mode_a',
  `fee_calculation_id` BIGINT UNSIGNED NOT NULL,
  `order_no` VARCHAR(64) NOT NULL,
  `total_cents` BIGINT NOT NULL,
  `split_pool_cents` BIGINT NOT NULL COMMENT '进入分账池的金额',
  `status` ENUM('pending','confirmed','paid','failed') NOT NULL DEFAULT 'pending',
  `created_month` DATE NOT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `confirmed_at` DATETIME(3) DEFAULT NULL,
  `paid_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`, `created_month`),
  UNIQUE KEY `uk_no` (`settlement_no`, `created_month`),
  KEY `idx_fee_calc` (`fee_calculation_id`, `created_month`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='分账汇总';

CREATE TABLE IF NOT EXISTS `settlement_party_amount` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `settlement_id` BIGINT UNSIGNED NOT NULL,
  `party_id` BIGINT UNSIGNED NOT NULL,
  `party_code` VARCHAR(64) NOT NULL,
  `party_name` VARCHAR(128) NOT NULL,
  `ratio_bp` INT UNSIGNED NOT NULL,
  `amount_cents` BIGINT NOT NULL,
  `status` ENUM('pending','paid','failed') NOT NULL DEFAULT 'pending',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_settlement` (`settlement_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='分账参与方金额';

CREATE TABLE IF NOT EXISTS `pricing_tier_snapshot` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `fee_calculation_id` BIGINT UNSIGNED NOT NULL,
  `pricing_rule_id` BIGINT UNSIGNED NOT NULL,
  `version` INT UNSIGNED NOT NULL,
  `snapshot_json` JSON NOT NULL COMMENT '计费规则完整快照(便于审计回放)',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_fee_calc` (`fee_calculation_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='计费规则快照(版本化)';

CREATE TABLE IF NOT EXISTS `withdraw_request` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `withdraw_no` VARCHAR(64) NOT NULL,
  `party_id` BIGINT UNSIGNED NOT NULL,
  `party_code` VARCHAR(64) NOT NULL,
  `amount_cents` BIGINT NOT NULL,
  `bank_account` VARCHAR(64) DEFAULT NULL,
  `bank_name` VARCHAR(128) DEFAULT NULL,
  `status` ENUM('pending','approved','rejected','paid','failed') NOT NULL DEFAULT 'pending',
  `reviewed_by` BIGINT UNSIGNED DEFAULT NULL,
  `reviewed_at` DATETIME(3) DEFAULT NULL,
  `reject_reason` VARCHAR(255) DEFAULT NULL,
  `paid_at` DATETIME(3) DEFAULT NULL,
  `note` VARCHAR(255) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_no` (`withdraw_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='提现申请';