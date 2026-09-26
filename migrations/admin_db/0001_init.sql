-- ============================================
-- ChargePilot · admin_db 初始迁移
-- 文档: docs/db/admin.md + docs/技术规格.md § 4
-- 表清单(25 张):admin_user_role / role / permission / role_permission /
--   station / device_meta / pricing_rule / pricing_template / coupon /
--   split_template / split_party / whitelabel_config / announcement /
--   customer_service_config / webhook_subscription / webhook_delivery_log /
--   ota_package / ota_schedule / alert_rule / alert_subscription / risk_config /
--   settled_record / finance_reconcile_log / invoice_review / alert_event /
--   audit_log
-- ============================================

SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci;

-- ---------------- 客户(部署单位)----------------
CREATE TABLE IF NOT EXISTS `customer` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `name` VARCHAR(128) NOT NULL,
  `code` VARCHAR(64) NOT NULL,
  `status` ENUM('active','suspended') NOT NULL DEFAULT 'active',
  `contact_phone` VARCHAR(32) DEFAULT NULL,
  `contact_email` VARCHAR(128) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_code` (`code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='客户(部署单位)';

-- ---------------- 管理员账号 ----------------
CREATE TABLE IF NOT EXISTS `admin_user_role` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `username` VARCHAR(64) NOT NULL,
  `display_name` VARCHAR(128) DEFAULT NULL,
  `password_hash` VARCHAR(255) NOT NULL,
  `phone` VARCHAR(32) DEFAULT NULL,
  `email` VARCHAR(128) DEFAULT NULL,
  `role_id` BIGINT UNSIGNED DEFAULT NULL,
  `mfa_secret` VARCHAR(64) DEFAULT NULL COMMENT 'TOTP 密钥(本期预留)',
  `mfa_enabled` TINYINT(1) NOT NULL DEFAULT 0,
  `status` ENUM('active','disabled','locked') NOT NULL DEFAULT 'active',
  `last_login_at` DATETIME(3) DEFAULT NULL,
  `failed_login_count` INT UNSIGNED NOT NULL DEFAULT 0,
  `locked_until` DATETIME(3) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  `deleted_by` BIGINT UNSIGNED DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_username` (`username`, `deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='管理员账号';

-- ---------------- 角色 ----------------
CREATE TABLE IF NOT EXISTS `role` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `code` VARCHAR(64) NOT NULL,
  `name` VARCHAR(128) NOT NULL,
  `description` VARCHAR(255) DEFAULT NULL,
  `is_builtin` TINYINT(1) NOT NULL DEFAULT 0 COMMENT '内置不可删',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_code` (`code`, `deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='角色';

-- ---------------- 权限 ----------------
CREATE TABLE IF NOT EXISTS `permission` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `code` VARCHAR(64) NOT NULL COMMENT '如 orders.read / orders.refund.approve',
  `name` VARCHAR(128) NOT NULL,
  `module` VARCHAR(64) NOT NULL COMMENT 'orders / devices / billing / ...',
  `description` VARCHAR(255) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_code` (`code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='权限码';

CREATE TABLE IF NOT EXISTS `role_permission` (
  `role_id` BIGINT UNSIGNED NOT NULL,
  `permission_id` BIGINT UNSIGNED NOT NULL,
  PRIMARY KEY (`role_id`, `permission_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='角色-权限映射';

-- ---------------- 站点 ----------------
CREATE TABLE IF NOT EXISTS `station` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `code` VARCHAR(64) NOT NULL,
  `name` VARCHAR(128) NOT NULL,
  `address` VARCHAR(255) DEFAULT NULL,
  `longitude` DECIMAL(11, 8) NOT NULL,
  `latitude` DECIMAL(10, 8) NOT NULL,
  `open_hours` VARCHAR(64) DEFAULT NULL,
  `contact_phone` VARCHAR(32) DEFAULT NULL,
  `status` ENUM('active','disabled','construction') NOT NULL DEFAULT 'active',
  `pricing_template_id` BIGINT UNSIGNED DEFAULT NULL,
  `split_template_id` BIGINT UNSIGNED DEFAULT NULL,
  `config_json` JSON DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  `deleted_by` BIGINT UNSIGNED DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_code` (`code`, `deleted_at`),
  KEY `idx_geo` (`latitude`, `longitude`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='充电站点';

-- ---------------- 设备元数据 ----------------
CREATE TABLE IF NOT EXISTS `device_meta` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `device_id` VARCHAR(64) NOT NULL,
  `station_id` BIGINT UNSIGNED DEFAULT NULL,
  `vendor_id` BIGINT UNSIGNED DEFAULT NULL,
  `model` VARCHAR(128) DEFAULT NULL,
  `serial_no` VARCHAR(128) DEFAULT NULL,
  `install_at` DATETIME(3) DEFAULT NULL,
  `warranty_until` DATETIME(3) DEFAULT NULL,
  `status` ENUM('enabled','disabled','retired','fault') NOT NULL DEFAULT 'enabled',
  `tags_json` JSON DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_device_id` (`device_id`, `deleted_at`),
  KEY `idx_station` (`station_id`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备元数据(冗余自 gateway_db)';

-- ---------------- 计费规则 ----------------
CREATE TABLE IF NOT EXISTS `pricing_rule` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `name` VARCHAR(128) NOT NULL,
  `name_i18n` JSON DEFAULT NULL,
  `station_id` BIGINT UNSIGNED DEFAULT NULL,
  `mode` ENUM('kwh','minute','mixed') NOT NULL DEFAULT 'kwh',
  `time_of_use_json` JSON DEFAULT NULL COMMENT '分时电价 JSON',
  `service_fee_cents_per_kwh` BIGINT NOT NULL DEFAULT 0,
  `service_fee_cents_per_min` BIGINT NOT NULL DEFAULT 0,
  `min_charge_cents` BIGINT NOT NULL DEFAULT 0 COMMENT '起步价',
  `version` INT UNSIGNED NOT NULL DEFAULT 1,
  `status` ENUM('active','disabled') NOT NULL DEFAULT 'active',
  `effective_from` DATETIME(3) DEFAULT NULL,
  `effective_to` DATETIME(3) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_station_status` (`station_id`, `status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='计费规则';

-- ---------------- 计费模板(站点分配)----------------
CREATE TABLE IF NOT EXISTS `pricing_template` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `code` VARCHAR(64) NOT NULL,
  `name` VARCHAR(128) NOT NULL,
  `name_i18n` JSON DEFAULT NULL,
  `default_pricing_rule_id` BIGINT UNSIGNED DEFAULT NULL,
  `description` VARCHAR(255) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_code` (`code`, `deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='计费模板';

-- ---------------- 运营优惠券(后台视角)----------------
CREATE TABLE IF NOT EXISTS `admin_coupon` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_coupon_id` BIGINT UNSIGNED NOT NULL COMMENT '关联 user_db.coupon.id',
  `name` VARCHAR(128) NOT NULL,
  `status` ENUM('active','disabled') NOT NULL DEFAULT 'active',
  `grant_strategy` JSON DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='运营优惠券管理(冗余自 user_db.coupon)';

-- ---------------- 分账模板 ----------------
CREATE TABLE IF NOT EXISTS `split_template` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `code` VARCHAR(64) NOT NULL,
  `name` VARCHAR(128) NOT NULL,
  `name_i18n` JSON DEFAULT NULL,
  `mode` ENUM('mode_a','mode_b') NOT NULL DEFAULT 'mode_a' COMMENT 'A: 全分账;B: 仅服务费',
  `status` ENUM('active','disabled') NOT NULL DEFAULT 'active',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_code` (`code`, `deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='分账模板';

CREATE TABLE IF NOT EXISTS `split_party` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `split_template_id` BIGINT UNSIGNED NOT NULL,
  `party_code` VARCHAR(64) NOT NULL,
  `party_name` VARCHAR(128) NOT NULL,
  `ratio_bp` INT UNSIGNED NOT NULL COMMENT '万分比(basis point,合计 10000)',
  `bank_account` VARCHAR(64) DEFAULT NULL,
  `bank_name` VARCHAR(128) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_template` (`split_template_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='分账参与方';

-- ---------------- 白标配置 ----------------
CREATE TABLE IF NOT EXISTS `whitelabel_config` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `name` VARCHAR(128) NOT NULL,
  `logo_url` VARCHAR(512) DEFAULT NULL,
  `mini_program_name` VARCHAR(64) DEFAULT NULL,
  `mini_program_appid` VARCHAR(64) DEFAULT NULL,
  `theme_color` VARCHAR(16) DEFAULT NULL,
  `contact_phone` VARCHAR(32) DEFAULT NULL,
  `about_text` TEXT,
  `config_json` JSON DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='白标配置';

-- ---------------- 公告 ----------------
CREATE TABLE IF NOT EXISTS `announcement` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `title` VARCHAR(255) NOT NULL,
  `title_i18n` JSON DEFAULT NULL,
  `content` TEXT NOT NULL,
  `content_i18n` JSON DEFAULT NULL,
  `scope` ENUM('global','station','city') NOT NULL DEFAULT 'global',
  `target_ids` JSON DEFAULT NULL,
  `priority` TINYINT UNSIGNED NOT NULL DEFAULT 0,
  `start_at` DATETIME(3) NOT NULL,
  `end_at` DATETIME(3) DEFAULT NULL,
  `status` ENUM('draft','published','expired') NOT NULL DEFAULT 'draft',
  `created_by` BIGINT UNSIGNED NOT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_status_window` (`status`, `start_at`, `end_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='公告';

-- ---------------- 客服坐席配置 ----------------
CREATE TABLE IF NOT EXISTS `customer_service_config` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `agent_wechat` VARCHAR(64) NOT NULL COMMENT '客服坐席微信号',
  `agent_name` VARCHAR(64) DEFAULT NULL,
  `path` VARCHAR(64) DEFAULT NULL COMMENT '小程序客服路径',
  `priority` INT UNSIGNED NOT NULL DEFAULT 0,
  `enabled` TINYINT(1) NOT NULL DEFAULT 1,
  `working_hours_json` JSON DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='客服坐席';

-- ---------------- Webhook 订阅 ----------------
CREATE TABLE IF NOT EXISTS `webhook_subscription` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `name` VARCHAR(128) NOT NULL,
  `url` VARCHAR(512) NOT NULL,
  `secret` VARCHAR(128) NOT NULL COMMENT 'HMAC 密钥',
  `event_types` JSON NOT NULL COMMENT '订阅的事件类型列表',
  `headers_json` JSON DEFAULT NULL,
  `enabled` TINYINT(1) NOT NULL DEFAULT 1,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Webhook 订阅';

CREATE TABLE IF NOT EXISTS `webhook_delivery_log` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `subscription_id` BIGINT UNSIGNED NOT NULL,
  `event_id` VARCHAR(64) NOT NULL,
  `event_type` VARCHAR(64) NOT NULL,
  `request_body` JSON NOT NULL,
  `response_status` INT DEFAULT NULL,
  `response_body` TEXT,
  `error_msg` VARCHAR(255) DEFAULT NULL,
  `attempt_count` INT UNSIGNED NOT NULL DEFAULT 1,
  `duration_ms` INT UNSIGNED DEFAULT NULL,
  `delivered_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_sub_event` (`subscription_id`, `event_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Webhook 投递日志';

-- ---------------- OTA 固件包 ----------------
CREATE TABLE IF NOT EXISTS `ota_package` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `code` VARCHAR(64) NOT NULL,
  `vendor_id` BIGINT UNSIGNED DEFAULT NULL,
  `version` VARCHAR(64) NOT NULL,
  `storage_url` VARCHAR(512) NOT NULL,
  `size_bytes` BIGINT UNSIGNED NOT NULL,
  `checksum_sha256` VARCHAR(64) NOT NULL,
  `sign` VARCHAR(512) DEFAULT NULL,
  `release_notes` TEXT,
  `status` ENUM('draft','published','archived') NOT NULL DEFAULT 'draft',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_code_version` (`code`, `version`, `deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='OTA 固件包';

CREATE TABLE IF NOT EXISTS `ota_schedule` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `package_id` BIGINT UNSIGNED NOT NULL,
  `target_filter_json` JSON DEFAULT NULL,
  `rollout_strategy` ENUM('all','canary','batch','manual') NOT NULL DEFAULT 'all',
  `batch_size` INT UNSIGNED DEFAULT NULL,
  `status` ENUM('pending','running','completed','cancelled','failed') NOT NULL DEFAULT 'pending',
  `scheduled_at` DATETIME(3) DEFAULT NULL,
  `started_at` DATETIME(3) DEFAULT NULL,
  `completed_at` DATETIME(3) DEFAULT NULL,
  `created_by` BIGINT UNSIGNED NOT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='OTA 推送计划';

-- ---------------- 告警规则(自定义)----------------
CREATE TABLE IF NOT EXISTS `alert_rule` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `name` VARCHAR(128) NOT NULL,
  `device_id_pattern` VARCHAR(128) NOT NULL DEFAULT '*',
  `metric` VARCHAR(64) NOT NULL,
  `op` ENUM('>','<','!=','between','==') NOT NULL,
  `threshold` JSON NOT NULL,
  `window_seconds` INT UNSIGNED NOT NULL DEFAULT 60,
  `severity` ENUM('warning','critical','fatal') NOT NULL DEFAULT 'warning',
  `enabled` TINYINT(1) NOT NULL DEFAULT 1,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_metric_enabled` (`metric`, `enabled`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='告警规则';

CREATE TABLE IF NOT EXISTS `alert_subscription` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `rule_id` BIGINT UNSIGNED DEFAULT NULL,
  `severity` ENUM('warning','critical','fatal') DEFAULT NULL,
  `webhook_subscription_id` BIGINT UNSIGNED DEFAULT NULL,
  `admin_user_id` BIGINT UNSIGNED DEFAULT NULL,
  `enabled` TINYINT(1) NOT NULL DEFAULT 1,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='告警订阅';

-- ---------------- 风控配置 ----------------
CREATE TABLE IF NOT EXISTS `risk_config` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `key` VARCHAR(64) NOT NULL,
  `value` JSON NOT NULL,
  `description` VARCHAR(255) DEFAULT NULL,
  `updated_by` BIGINT UNSIGNED DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_key` (`key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='风控配置';

-- ---------------- 分账出账 ----------------
CREATE TABLE IF NOT EXISTS `settled_record` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `settlement_no` VARCHAR(64) NOT NULL,
  `split_template_id` BIGINT UNSIGNED NOT NULL,
  `period_start` DATE NOT NULL,
  `period_end` DATE NOT NULL,
  `total_cents` BIGINT NOT NULL,
  `status` ENUM('draft','confirmed','paid','disputed') NOT NULL DEFAULT 'draft',
  `reviewed_by` BIGINT UNSIGNED DEFAULT NULL,
  `reviewed_at` DATETIME(3) DEFAULT NULL,
  `paid_at` DATETIME(3) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_no` (`settlement_no`),
  KEY `idx_period` (`period_start`, `period_end`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='分账出账';

-- ---------------- 财务对账日志 ----------------
CREATE TABLE IF NOT EXISTS `finance_reconcile_log` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `reconcile_type` ENUM('wechat_refund','wechat_pay','split','withdraw') NOT NULL,
  `reconcile_date` DATE NOT NULL,
  `internal_count` INT UNSIGNED NOT NULL,
  `wechat_count` INT UNSIGNED NOT NULL,
  `diff_count` INT NOT NULL,
  `internal_cents` BIGINT NOT NULL,
  `wechat_cents` BIGINT NOT NULL,
  `diff_cents` BIGINT NOT NULL,
  `diffs_json` JSON DEFAULT NULL,
  `resolved` TINYINT(1) NOT NULL DEFAULT 0,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_type_date` (`reconcile_type`, `reconcile_date`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='财务对账日志';

-- ---------------- 发票审核 ----------------
CREATE TABLE IF NOT EXISTS `invoice_review` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `invoice_request_id` BIGINT UNSIGNED NOT NULL COMMENT '关联 user_db.invoice_request.id',
  `review_status` ENUM('pending','approved','rejected') NOT NULL DEFAULT 'pending',
  `reviewed_by` BIGINT UNSIGNED DEFAULT NULL,
  `reviewed_at` DATETIME(3) DEFAULT NULL,
  `reject_reason` VARCHAR(255) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_status` (`review_status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='发票审核';

-- ---------------- 告警事件 ----------------
CREATE TABLE IF NOT EXISTS `alert_event` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `device_id` VARCHAR(64) NOT NULL,
  `rule_id` BIGINT UNSIGNED DEFAULT NULL,
  `severity` ENUM('warning','critical','fatal') NOT NULL DEFAULT 'warning',
  `metric` VARCHAR(64) NOT NULL,
  `value` DECIMAL(18, 6) DEFAULT NULL,
  `threshold` VARCHAR(64) DEFAULT NULL,
  `status` ENUM('active','acknowledged','resolved','auto_resolved') NOT NULL DEFAULT 'active',
  `acked_by` BIGINT UNSIGNED DEFAULT NULL,
  `acked_at` DATETIME(3) DEFAULT NULL,
  `resolved_at` DATETIME(3) DEFAULT NULL,
  `note` TEXT,
  `event_id` VARCHAR(64) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `created_month` DATE NOT NULL,
  PRIMARY KEY (`id`, `created_month`),
  KEY `idx_device_status` (`device_id`, `status`),
  KEY `idx_event` (`event_id`, `created_month`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='告警事件'
PARTITION BY RANGE (TO_DAYS(`created_month`)) (
  PARTITION p_init VALUES LESS THAN (TO_DAYS('2026-10-01')),
  PARTITION p_2026m10 VALUES LESS THAN (TO_DAYS('2026-11-01')),
  PARTITION p_2026m11 VALUES LESS THAN (TO_DAYS('2026-12-01')),
  PARTITION p_2026m12 VALUES LESS THAN (TO_DAYS('2027-01-01')),
  PARTITION p_max VALUES LESS THAN MAXVALUE
);

-- ---------------- 审计日志 ----------------
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