-- ============================================
-- ChargePilot · user_db 初始迁移
-- 文档: docs/db/user.md + docs/技术规格.md § 4
-- 表清单(18 张):user / charge_order / payment_order /
--   wallet_account / wallet_txn / refund_record / refund_reconcile_diff /
--   risk_freeze_log / coupon / coupon_grant / membership_card / invoice_request /
--   port_view / payment_callback_idempotent / feedback / device_fault_report /
--   active_port_charge / event_outbox
-- ============================================

SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci;

-- ---------------- 终端用户 ----------------
CREATE TABLE IF NOT EXISTS `user` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `openid` VARCHAR(64) NOT NULL COMMENT '微信 openid',
  `unionid` VARCHAR(64) DEFAULT NULL,
  `nickname` VARCHAR(64) DEFAULT NULL,
  `avatar_url` VARCHAR(512) DEFAULT NULL,
  `phone_enc` VARBINARY(255) DEFAULT NULL COMMENT 'AES_ENCRYPT 加密',
  `phone_hash` VARCHAR(64) DEFAULT NULL COMMENT '查询用 SHA-256 hash(不可逆)',
  `gender` ENUM('unknown','male','female') NOT NULL DEFAULT 'unknown',
  `status` ENUM('active','frozen') NOT NULL DEFAULT 'active',
  `first_seen_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `last_login_at` DATETIME(3) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  `deleted_by` BIGINT UNSIGNED DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_openid` (`openid`, `deleted_at`),
  KEY `idx_phone_hash` (`phone_hash`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='终端用户';

-- ---------------- 充电订单(纯生命周期,按月分区)----------------
CREATE TABLE IF NOT EXISTS `charge_order` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `order_no` VARCHAR(64) NOT NULL COMMENT '业务唯一号 ORD-xxx',
  `user_id` BIGINT UNSIGNED NOT NULL,
  `device_id` VARCHAR(64) NOT NULL,
  `port_no` TINYINT UNSIGNED NOT NULL,
  `port_code` VARCHAR(64) DEFAULT NULL,
  `payment_order_id` BIGINT UNSIGNED DEFAULT NULL,
  `status` ENUM('pending_payment','paid','charging','completed','cancelled','failed','refunding','refunded') NOT NULL DEFAULT 'pending_payment',
  `started_at` DATETIME(3) DEFAULT NULL,
  `ended_at` DATETIME(3) DEFAULT NULL,
  `charged_kwh` DECIMAL(12, 4) DEFAULT NULL,
  `charged_seconds` INT UNSIGNED DEFAULT NULL,
  `peak_kwh` DECIMAL(12, 4) DEFAULT NULL,
  `off_kwh` DECIMAL(12, 4) DEFAULT NULL,
  `electric_cents` BIGINT DEFAULT NULL,
  `service_cents` BIGINT DEFAULT NULL,
  `total_cents` BIGINT DEFAULT NULL,
  `failure_reason` VARCHAR(255) DEFAULT NULL,
  `created_month` DATE NOT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  `deleted_by` BIGINT UNSIGNED DEFAULT NULL,
  PRIMARY KEY (`id`, `created_month`),
  UNIQUE KEY `uk_order_no` (`order_no`, `created_month`),
  KEY `idx_user_created` (`user_id`, `created_at`),
  KEY `idx_status` (`status`),
  KEY `idx_payment` (`payment_order_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='充电订单(纯生命周期)'
PARTITION BY RANGE (TO_DAYS(`created_month`)) (
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

-- ---------------- 支付订单(纯支付,按月分区)----------------
CREATE TABLE IF NOT EXISTS `payment_order` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `order_no` VARCHAR(64) NOT NULL,
  `biz_type` ENUM('charge','wallet_recharge') NOT NULL,
  `biz_id` BIGINT UNSIGNED NOT NULL COMMENT '关联 charge_order.id 或 wallet_txn.id',
  `user_id` BIGINT UNSIGNED NOT NULL,
  `pay_method` ENUM('wechat','balance','mixed','coupon') NOT NULL,
  `total_cents` BIGINT NOT NULL COMMENT '订单总金额(分)',
  `paid_cents` BIGINT NOT NULL DEFAULT 0,
  `refunded_cents` BIGINT NOT NULL DEFAULT 0,
  `wechat_transaction_id` VARCHAR(64) DEFAULT NULL COMMENT '微信侧 transaction_id',
  `status` ENUM('initiated','paid','closed','refunded','partial_refunded','failed') NOT NULL DEFAULT 'initiated',
  `paid_at` DATETIME(3) DEFAULT NULL,
  `closed_at` DATETIME(3) DEFAULT NULL,
  `expired_at` DATETIME(3) DEFAULT NULL,
  `failure_reason` VARCHAR(255) DEFAULT NULL,
  `client_ip` VARCHAR(45) DEFAULT NULL,
  `created_month` DATE NOT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  `deleted_by` BIGINT UNSIGNED DEFAULT NULL,
  PRIMARY KEY (`id`, `created_month`),
  UNIQUE KEY `uk_order_no` (`order_no`, `created_month`),
  UNIQUE KEY `uk_wechat_txn` (`wechat_transaction_id`, `created_month`),
  KEY `idx_biz` (`biz_type`, `biz_id`),
  KEY `idx_user_created` (`user_id`, `created_at`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='支付订单(支持 charge / wallet_recharge)'
PARTITION BY RANGE (TO_DAYS(`created_month`)) (
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

-- ---------------- 钱包账户(1:1 with user)----------------
CREATE TABLE IF NOT EXISTS `wallet_account` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `balance_cents` BIGINT NOT NULL DEFAULT 0 COMMENT '余额(分)',
  `frozen_cents` BIGINT NOT NULL DEFAULT 0 COMMENT '冻结金额(提现中)',
  `status` ENUM('active','frozen') NOT NULL DEFAULT 'active',
  `version` BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '乐观锁',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  `deleted_by` BIGINT UNSIGNED DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user` (`user_id`, `deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='钱包账户(1:1 with user)';

-- ---------------- 钱包流水(按月分区)----------------
CREATE TABLE IF NOT EXISTS `wallet_txn` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `txn_no` VARCHAR(64) NOT NULL,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `wallet_account_id` BIGINT UNSIGNED NOT NULL,
  `direction` ENUM('in','out') NOT NULL,
  `amount_cents` BIGINT NOT NULL,
  `balance_after_cents` BIGINT NOT NULL,
  `biz_type` ENUM('recharge','pay','refund','gift','freeze','unfreeze','adjust') NOT NULL,
  `biz_ref` VARCHAR(64) DEFAULT NULL COMMENT '关联业务单号',
  `note` VARCHAR(255) DEFAULT NULL,
  `created_month` DATE NOT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`, `created_month`),
  UNIQUE KEY `uk_txn_no` (`txn_no`, `created_month`),
  KEY `idx_user_time` (`user_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='钱包流水'
PARTITION BY RANGE (TO_DAYS(`created_month`)) (
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

-- ---------------- 退款记录(按月分区)----------------
CREATE TABLE IF NOT EXISTS `refund_record` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `refund_no` VARCHAR(64) NOT NULL,
  `payment_order_id` BIGINT UNSIGNED NOT NULL,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `biz_type` ENUM('charge','wallet_recharge') NOT NULL,
  `biz_id` BIGINT UNSIGNED NOT NULL,
  `refund_cents` BIGINT NOT NULL,
  `reason` VARCHAR(255) DEFAULT NULL,
  `status` ENUM('pending','processing','success','failed') NOT NULL DEFAULT 'pending',
  `retry_count` INT UNSIGNED NOT NULL DEFAULT 0,
  `wechat_refund_id` VARCHAR(64) DEFAULT NULL,
  `claimed_by` BIGINT UNSIGNED DEFAULT NULL COMMENT 'admin user id',
  `claimed_at` DATETIME(3) DEFAULT NULL,
  `completed_at` DATETIME(3) DEFAULT NULL,
  `failure_reason` VARCHAR(255) DEFAULT NULL,
  `created_month` DATE NOT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  `deleted_by` BIGINT UNSIGNED DEFAULT NULL,
  PRIMARY KEY (`id`, `created_month`),
  UNIQUE KEY `uk_refund_no` (`refund_no`, `created_month`),
  KEY `idx_payment` (`payment_order_id`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='退款记录'
PARTITION BY RANGE (TO_DAYS(`created_month`)) (
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

-- ---------------- 每日对账差异(不分)----------------
CREATE TABLE IF NOT EXISTS `refund_reconcile_diff` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `reconcile_date` DATE NOT NULL,
  `internal_count` INT UNSIGNED NOT NULL,
  `wechat_count` INT UNSIGNED NOT NULL,
  `diff_count` INT NOT NULL,
  `internal_cents` BIGINT NOT NULL,
  `wechat_cents` BIGINT NOT NULL,
  `diff_cents` BIGINT NOT NULL,
  `diffs_json` JSON DEFAULT NULL,
  `resolved` TINYINT(1) NOT NULL DEFAULT 0,
  `resolved_at` DATETIME(3) DEFAULT NULL,
  `resolved_by` BIGINT UNSIGNED DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_date` (`reconcile_date`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='每日对账差异(微信账单 vs 内部退款)';

-- ---------------- 风控冻结日志(不分)----------------
CREATE TABLE IF NOT EXISTS `risk_freeze_log` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `trigger_rule` VARCHAR(64) NOT NULL,
  `frozen_action` VARCHAR(64) NOT NULL,
  `reason` VARCHAR(255) DEFAULT NULL,
  `window_minutes` INT UNSIGNED DEFAULT NULL,
  `threshold_value` INT UNSIGNED DEFAULT NULL,
  `actual_value` INT UNSIGNED DEFAULT NULL,
  `unfreeze_at` DATETIME(3) DEFAULT NULL,
  `status` ENUM('frozen','unfrozen') NOT NULL DEFAULT 'frozen',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_user_status` (`user_id`, `status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='风控冻结记录(本期仅频次触发)';

-- ---------------- 优惠券模板 ----------------
CREATE TABLE IF NOT EXISTS `coupon` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `code` VARCHAR(64) NOT NULL COMMENT '业务唯一码',
  `name` VARCHAR(128) NOT NULL,
  `name_i18n` JSON DEFAULT NULL,
  `discount_type` ENUM('amount','percentage','time_free') NOT NULL,
  `discount_value_cents` BIGINT DEFAULT NULL COMMENT 'amount 模式:减 N 分',
  `discount_percent` DECIMAL(5, 2) DEFAULT NULL COMMENT 'percentage 模式:N% 折扣',
  `min_charge_cents` BIGINT NOT NULL DEFAULT 0 COMMENT '最低消费门槛',
  `valid_hours` INT UNSIGNED NOT NULL DEFAULT 24 COMMENT '领取后有效小时',
  `total_quota` INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '0=无限',
  `per_user_quota` INT UNSIGNED NOT NULL DEFAULT 1,
  `status` ENUM('active','disabled') NOT NULL DEFAULT 'active',
  `start_at` DATETIME(3) DEFAULT NULL,
  `end_at` DATETIME(3) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  `deleted_by` BIGINT UNSIGNED DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_code` (`code`, `deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='优惠券模板';

-- ---------------- 优惠券发放(用户持有)----------------
CREATE TABLE IF NOT EXISTS `coupon_grant` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `coupon_id` BIGINT UNSIGNED NOT NULL,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `grant_source` ENUM('register','activity','invite','manual','invite_reward') NOT NULL,
  `status` ENUM('unused','used','expired') NOT NULL DEFAULT 'unused',
  `used_payment_order_id` BIGINT UNSIGNED DEFAULT NULL,
  `used_at` DATETIME(3) DEFAULT NULL,
  `expired_at` DATETIME(3) NOT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_user_status` (`user_id`, `status`),
  KEY `idx_coupon` (`coupon_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='优惠券发放记录';

-- ---------------- 会员卡(月卡/年卡)----------------
CREATE TABLE IF NOT EXISTS `membership_card` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `card_type` ENUM('month','year','quarter') NOT NULL,
  `status` ENUM('active','expired','refunded') NOT NULL DEFAULT 'active',
  `start_at` DATETIME(3) NOT NULL,
  `end_at` DATETIME(3) NOT NULL,
  `price_cents` BIGINT NOT NULL,
  `payment_order_id` BIGINT UNSIGNED DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_user_status` (`user_id`, `status`),
  KEY `idx_end` (`end_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='会员卡(预留)';

-- ---------------- 发票申请 ----------------
CREATE TABLE IF NOT EXISTS `invoice_request` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `invoice_no` VARCHAR(64) NOT NULL,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `biz_type` ENUM('charge','wallet_recharge') NOT NULL,
  `biz_id` BIGINT UNSIGNED NOT NULL,
  `total_cents` BIGINT NOT NULL,
  `invoice_type` ENUM('normal','vat_special') NOT NULL DEFAULT 'normal',
  `title` VARCHAR(255) NOT NULL,
  `tax_no` VARCHAR(64) DEFAULT NULL,
  `email` VARCHAR(128) DEFAULT NULL,
  `review_status` ENUM('pending','approved','rejected','issued') NOT NULL DEFAULT 'pending',
  `reviewed_by` BIGINT UNSIGNED DEFAULT NULL,
  `reviewed_at` DATETIME(3) DEFAULT NULL,
  `reject_reason` VARCHAR(255) DEFAULT NULL,
  `invoice_url` VARCHAR(512) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_invoice_no` (`invoice_no`),
  KEY `idx_user_created` (`user_id`, `created_at`),
  KEY `idx_review` (`review_status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='发票申请';

-- ---------------- 找桩缓存(冗余自 gateway_db)----------------
CREATE TABLE IF NOT EXISTS `port_view` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `station_id` BIGINT UNSIGNED NOT NULL,
  `station_name` VARCHAR(128) NOT NULL,
  `address` VARCHAR(255) DEFAULT NULL,
  `longitude` DECIMAL(11, 8) NOT NULL,
  `latitude` DECIMAL(10, 8) NOT NULL,
  `available_ports` INT UNSIGNED NOT NULL DEFAULT 0,
  `total_ports` INT UNSIGNED NOT NULL DEFAULT 0,
  `last_synced_at` DATETIME(3) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_geo` (`latitude`, `longitude`),
  KEY `idx_station` (`station_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='找桩缓存(冗余自 admin_db)';

-- ---------------- 微信回调幂等表(不分)----------------
CREATE TABLE IF NOT EXISTS `payment_callback_idempotent` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `wechat_transaction_id` VARCHAR(64) NOT NULL,
  `processed_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `request_digest` VARCHAR(128) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_txn` (`wechat_transaction_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='微信支付回调幂等(30 天保留)';

-- ---------------- 评价 / 投诉(不分)----------------
CREATE TABLE IF NOT EXISTS `feedback` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `order_id` BIGINT UNSIGNED DEFAULT NULL,
  `device_id` VARCHAR(64) DEFAULT NULL,
  `rating` TINYINT UNSIGNED DEFAULT NULL COMMENT '1-5 星',
  `category` ENUM('rating','complaint','suggestion') NOT NULL,
  `content` TEXT,
  `images_json` JSON DEFAULT NULL,
  `status` ENUM('pending','processed','closed') NOT NULL DEFAULT 'pending',
  `replied_by` BIGINT UNSIGNED DEFAULT NULL,
  `replied_at` DATETIME(3) DEFAULT NULL,
  `reply_content` TEXT,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_user_created` (`user_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='评价/投诉';

-- ---------------- 设备报修(不分)----------------
CREATE TABLE IF NOT EXISTS `device_fault_report` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `device_id` VARCHAR(64) NOT NULL,
  `user_id` BIGINT UNSIGNED DEFAULT NULL,
  `report_source` ENUM('user','inspect','monitor') NOT NULL DEFAULT 'user',
  `fault_type` ENUM('mechanical','electrical','communication','display','other') NOT NULL,
  `description` TEXT,
  `images_json` JSON DEFAULT NULL,
  `status` ENUM('open','dispatched','fixed','closed') NOT NULL DEFAULT 'open',
  `assigned_to` BIGINT UNSIGNED DEFAULT NULL,
  `resolved_at` DATETIME(3) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_device_status` (`device_id`, `status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备报修';

-- ---------------- 端口占用跨月兜底(不分)----------------
CREATE TABLE IF NOT EXISTS `active_port_charge` (
  `port_id` BIGINT UNSIGNED NOT NULL COMMENT '复合 ID: device_id+port_no',
  `device_id` VARCHAR(64) NOT NULL,
  `port_no` TINYINT UNSIGNED NOT NULL,
  `charge_order_id` BIGINT UNSIGNED NOT NULL,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `started_at` DATETIME(3) NOT NULL,
  `ended_at` DATETIME(3) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`port_id`),
  UNIQUE KEY `uk_device_port` (`device_id`, `port_no`),
  KEY `idx_order` (`charge_order_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='端口当前充电占用(跨月唯一性兜底)';

-- ---------------- 事件 outbox(同事务写,异步发布)----------------
CREATE TABLE IF NOT EXISTS `event_outbox` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `event_id` VARCHAR(64) NOT NULL,
  `stream` VARCHAR(64) NOT NULL COMMENT '目标 stream 名',
  `envelope_json` JSON NOT NULL,
  `status` ENUM('pending','published','failed') NOT NULL DEFAULT 'pending',
  `retry_count` INT UNSIGNED NOT NULL DEFAULT 0,
  `last_error` VARCHAR(255) DEFAULT NULL,
  `scheduled_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `published_at` DATETIME(3) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_event` (`event_id`),
  KEY `idx_status_sched` (`status`, `scheduled_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='事件 outbox(可靠发布)';