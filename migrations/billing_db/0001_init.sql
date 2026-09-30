-- +goose NO TRANSACTION
-- +goose Up

-- ChargePilot billing_db 初始化结构
-- 计费结算：账单、费用确认及资金分账
-- 发布前基线：直接修改 CREATE TABLE，重建空库；不叠加增量 ALTER。
-- 表间关联由所属服务维护；此文件只初始化当前库。
-- 计量、事件和审计表保留月分区及 p_max，后续月份由维护任务创建。

SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci;
SET sql_mode = 'STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION,ERROR_FOR_DIVISION_BY_ZERO,NO_ZERO_DATE,NO_ZERO_IN_DATE';

-- fee_calculation：计费明细
CREATE TABLE `fee_calculation` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `calculation_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `order_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `charge_order_id` bigint unsigned NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `station_id` bigint unsigned DEFAULT NULL,
  `pricing_rule_id` bigint unsigned DEFAULT NULL,
  `pricing_rule_version` int unsigned DEFAULT NULL,
  `charged_kwh` decimal(12,4) NOT NULL,
  `charged_seconds` int unsigned NOT NULL,
  `peak_kwh` decimal(12,4) DEFAULT NULL,
  `off_kwh` decimal(12,4) DEFAULT NULL,
  `electric_cents` bigint NOT NULL,
  `service_cents` bigint NOT NULL,
  `total_cents` bigint NOT NULL,
  `breakdown_json` json DEFAULT NULL,
  `created_month` date NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`,`created_month`),
  UNIQUE KEY `uk_no` (`calculation_no`,`created_month`),
  KEY `idx_order` (`order_no`,`created_month`),
  KEY `idx_user` (`user_id`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='计费明细'
/*!50100 PARTITION BY RANGE (to_days(`created_month`))
(PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB,
 PARTITION p_2026m10 VALUES LESS THAN (740286) ENGINE = InnoDB,
 PARTITION p_2026m11 VALUES LESS THAN (740316) ENGINE = InnoDB,
 PARTITION p_2026m12 VALUES LESS THAN (740347) ENGINE = InnoDB,
 PARTITION p_max VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */;

-- fee_delivery：计费结果投递任务
CREATE TABLE `fee_delivery` (
  `charge_order_id` bigint unsigned NOT NULL,
  `payload_json` json NOT NULL,
  `delivered` tinyint(1) NOT NULL DEFAULT '0',
  `attempts` int unsigned NOT NULL DEFAULT '0',
  `scheduled_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `delivered_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`charge_order_id`),
  KEY `idx_due` (`delivered`,`scheduled_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- fee_receipt：计费事件幂等回执
CREATE TABLE `fee_receipt` (
  `charge_order_id` bigint unsigned NOT NULL,
  `source_json` json NOT NULL,
  `calculation_id` bigint unsigned DEFAULT NULL,
  `calculation_no` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`charge_order_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- manual_fee_review：人工定价兜底单(D16)
CREATE TABLE `manual_fee_review` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `charge_order_id` bigint unsigned NOT NULL,
  `order_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `reason` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '无法自动计费的原因(供人工定价参考)',
  `energy_wh` bigint unsigned NOT NULL DEFAULT '0' COMMENT '设备上报的总电量',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `created_month` varchar(7) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'YYYY-MM,供月度归档扫描',
  `source_json` json DEFAULT NULL,
  `status` enum('pending','resolved') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `resolved_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_order` (`charge_order_id`),
  KEY `idx_month` (`created_month`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='人工定价兜底单(D16)';

-- pricing_tier_snapshot：计费规则快照(版本化)
CREATE TABLE `pricing_tier_snapshot` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `fee_calculation_id` bigint unsigned NOT NULL,
  `pricing_rule_id` bigint unsigned NOT NULL,
  `version` int unsigned NOT NULL,
  `snapshot_json` json NOT NULL COMMENT '计费规则完整快照(便于审计回放)',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_fee_calc` (`fee_calculation_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='计费规则快照(版本化)';

-- settlement：分账汇总
CREATE TABLE `settlement` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `settlement_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `split_template_id` bigint unsigned NOT NULL,
  `split_template_code` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `mode` enum('mode_a','mode_b') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'mode_a',
  `fee_calculation_id` bigint unsigned NOT NULL,
  `generation` int unsigned NOT NULL DEFAULT '1',
  `order_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `total_cents` bigint NOT NULL,
  `split_pool_cents` bigint NOT NULL COMMENT '进入分账池的金额',
  `split_pool_excluded_electric_cents` bigint NOT NULL DEFAULT '0' COMMENT 'mode_b 下不进入分账池的电费金额',
  `status` enum('pending','confirmed','paid','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `created_month` date NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `confirmed_at` datetime(3) DEFAULT NULL,
  `paid_at` datetime(3) DEFAULT NULL,
  `electric_cents` bigint NOT NULL DEFAULT '0',
  `service_cents` bigint NOT NULL DEFAULT '0',
  PRIMARY KEY (`id`,`created_month`),
  UNIQUE KEY `uk_no` (`settlement_no`,`created_month`),
  UNIQUE KEY `uk_fee_generation` (`fee_calculation_id`,`generation`),
  KEY `idx_fee_calc` (`fee_calculation_id`,`created_month`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='分账汇总';

-- settlement_party_amount：分账参与方金额
CREATE TABLE `settlement_party_amount` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `settlement_id` bigint unsigned NOT NULL,
  `party_id` bigint unsigned NOT NULL,
  `party_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `party_name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `ratio_bp` int unsigned NOT NULL,
  `amount_cents` bigint NOT NULL,
  `status` enum('pending','paid','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `electric_cents` bigint NOT NULL DEFAULT '0',
  `service_cents` bigint NOT NULL DEFAULT '0',
  `generation` int unsigned NOT NULL DEFAULT '1',
  PRIMARY KEY (`id`),
  KEY `idx_settlement` (`settlement_id`),
  KEY `idx_party_code` (`party_code`),
  KEY `idx_settlement_party` (`settlement_id`,`party_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='分账参与方金额';

-- withdraw_request：提现申请
CREATE TABLE `withdraw_request` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `withdraw_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `party_id` bigint unsigned NOT NULL,
  `party_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `amount_cents` bigint NOT NULL,
  `bank_account` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `bank_name` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `status` enum('pending','approved','rejected','paid','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `reviewed_by` bigint unsigned DEFAULT NULL,
  `reviewed_at` datetime(3) DEFAULT NULL,
  `reject_reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `paid_at` datetime(3) DEFAULT NULL,
  `note` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_no` (`withdraw_no`),
  KEY `idx_party_status` (`party_id`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='提现申请';

-- 初始化数据：内置权限、角色及系统默认配置。业务与演示数据另行创建。

-- +goose Down
-- 仅供一次性开发/测试库回退；删除当前库全部业务表。
DROP TABLE IF EXISTS `withdraw_request`;
DROP TABLE IF EXISTS `settlement_party_amount`;
DROP TABLE IF EXISTS `settlement`;
DROP TABLE IF EXISTS `pricing_tier_snapshot`;
DROP TABLE IF EXISTS `manual_fee_review`;
DROP TABLE IF EXISTS `fee_receipt`;
DROP TABLE IF EXISTS `fee_delivery`;
DROP TABLE IF EXISTS `fee_calculation`;
