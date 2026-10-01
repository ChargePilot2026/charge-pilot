-- +goose NO TRANSACTION
-- +goose Up

-- ChargePilot user_db 初始化结构
-- 充电用户：账户钱包、支付、充电订单、退款与在线卡
-- 发布前基线：直接修改 CREATE TABLE，重建空库；不叠加增量 ALTER。
-- 表间关联由所属服务维护；此文件只初始化当前库。
-- 计量、事件和审计表保留月分区及 p_max，后续月份由维护任务创建。

SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci;
SET sql_mode = 'STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION,ERROR_FOR_DIVISION_BY_ZERO,NO_ZERO_DATE,NO_ZERO_IN_DATE';

-- active_port_charge：端口当前充电占用(跨月唯一性兜底)
CREATE TABLE `active_port_charge` (
  `port_id` bigint unsigned NOT NULL COMMENT '复合 ID: device_id+port_no',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `port_no` tinyint unsigned NOT NULL,
  `charge_order_id` bigint unsigned NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `started_at` datetime(3) NOT NULL,
  `ended_at` datetime(3) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`port_id`),
  UNIQUE KEY `uk_device_port` (`device_id`,`port_no`),
  KEY `idx_order` (`charge_order_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='端口当前充电占用(跨月唯一性兜底)';

-- audit_log：审计日志
CREATE TABLE `audit_log` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `actor_id` bigint unsigned NOT NULL,
  `actor_name` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `module` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `action` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `target_type` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `target_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `request_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `before_json` json DEFAULT NULL,
  `after_json` json DEFAULT NULL,
  `client_ip` varchar(45) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_month` date NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`,`created_month`),
  KEY `idx_actor_time` (`actor_id`,`created_at`),
  KEY `idx_module_action` (`module`,`action`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='审计日志'
/*!50100 PARTITION BY RANGE (to_days(`created_month`))
(PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB,
 PARTITION p_2026m10 VALUES LESS THAN (740286) ENGINE = InnoDB,
 PARTITION p_2026m11 VALUES LESS THAN (740316) ENGINE = InnoDB,
 PARTITION p_2026m12 VALUES LESS THAN (740347) ENGINE = InnoDB,
 PARTITION p_max VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */;

-- card_charge：在线卡充电会话与累计购买时长
CREATE TABLE `card_charge` (
  `charge_order_id` bigint unsigned NOT NULL,
  `card_id` bigint unsigned NOT NULL,
  `port_code` varchar(64) NOT NULL,
  `active_port` varchar(64) DEFAULT NULL,
  `paid_cents` bigint NOT NULL,
  `purchased_minutes` smallint unsigned NOT NULL,
  `max_minutes` smallint unsigned NOT NULL,
  `card_no` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `wallet_after_cents` bigint NOT NULL,
  `package_json` json NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`charge_order_id`),
  UNIQUE KEY `active_port` (`active_port`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- card_operation：在线卡刷卡操作幂等结果和扣款记录
CREATE TABLE `card_operation` (
  `operation_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `device_id` varchar(64) NOT NULL,
  `event_id` varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `charge_order_id` bigint unsigned NOT NULL,
  `card_id` bigint unsigned NOT NULL,
  `kind` enum('start','extend') NOT NULL,
  `price_cents` bigint NOT NULL,
  `minutes` smallint unsigned NOT NULL,
  `status` enum('confirming','confirmed','failed') NOT NULL DEFAULT 'confirming',
  `failure_reason` varchar(255) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `confirmed_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`operation_id`),
  UNIQUE KEY `uk_device_event` (`device_id`,`event_id`),
  KEY `idx_order` (`charge_order_id`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- charge_bill：充电账单
CREATE TABLE `charge_bill` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `bill_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `charge_order_id` bigint unsigned NOT NULL,
  `payment_order_id` bigint unsigned DEFAULT NULL,
  `user_id` bigint unsigned NOT NULL,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `port_no` tinyint unsigned NOT NULL DEFAULT '0',
  `electric_cents` bigint NOT NULL DEFAULT '0',
  `service_cents` bigint NOT NULL DEFAULT '0',
  `total_cents` bigint NOT NULL DEFAULT '0',
  `prepaid_cents` bigint NOT NULL DEFAULT '0',
  `refund_cents` bigint NOT NULL DEFAULT '0',
  `shortfall_cents` bigint NOT NULL DEFAULT '0',
  `charged_kwh` decimal(12,4) DEFAULT NULL,
  `charged_seconds` int unsigned DEFAULT NULL,
  `status` enum('issued','settled','voided') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'issued',
  `issued_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `settled_at` datetime(3) DEFAULT NULL,
  `created_month` date NOT NULL,
  PRIMARY KEY (`id`,`created_month`),
  UNIQUE KEY `uk_order` (`charge_order_id`,`created_month`),
  UNIQUE KEY `uk_bill_no` (`bill_no`,`created_month`),
  KEY `idx_user` (`user_id`,`issued_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='充电账单';

-- charge_bill_read：账单已读标记
CREATE TABLE `charge_bill_read` (
  `bill_id` bigint unsigned NOT NULL,
  `created_month` date NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `read_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`bill_id`,`created_month`),
  KEY `idx_user` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='账单已读标记';

-- charge_billing_cutoff：订单首次计费截止点
CREATE TABLE `charge_billing_cutoff` (
  `charge_order_id` bigint unsigned NOT NULL,
  `cutoff_at` datetime(3) NOT NULL,
  `reason` varchar(64) NOT NULL,
  `electric_cents` bigint DEFAULT NULL COMMENT '预算耗尽时冻结的实收电费',
  `service_cents` bigint DEFAULT NULL COMMENT '预算耗尽时冻结的实收服务费',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`charge_order_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- charge_billing_job：待结算订单任务及重试状态
CREATE TABLE `charge_billing_job` (
  `charge_order_id` bigint unsigned NOT NULL,
  `status` enum('pending','manual_review','done') NOT NULL DEFAULT 'pending',
  `attempts` int unsigned NOT NULL DEFAULT '0',
  `next_attempt_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `last_error` varchar(255) DEFAULT NULL,
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`charge_order_id`),
  KEY `idx_due` (`status`,`next_attempt_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- charge_debt：充电欠费
CREATE TABLE `charge_debt` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `debt_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `charge_order_id` bigint unsigned NOT NULL,
  `payment_order_id` bigint unsigned NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `debt_cents` bigint NOT NULL,
  `paid_cents` bigint NOT NULL DEFAULT '0',
  `status` enum('unpaid','partial','settled','waived') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'unpaid',
  `last_reminder_at` datetime(3) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_debt_order` (`charge_order_id`),
  UNIQUE KEY `uk_debt_no` (`debt_no`),
  KEY `idx_user_status` (`user_id`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='充电欠费';

-- charge_debt_receipt：欠费补缴入账回执
CREATE TABLE `charge_debt_receipt` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `debt_id` bigint unsigned NOT NULL,
  `payment_order_id` bigint unsigned NOT NULL,
  `paid_cents` bigint NOT NULL,
  `channel_ref` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `received_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_payment` (`payment_order_id`),
  UNIQUE KEY `uk_channel` (`channel_ref`),
  KEY `idx_debt` (`debt_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='欠费补缴入账回执';

-- charge_end_receipt：设备结束事件的幂等回执
CREATE TABLE `charge_end_receipt` (
  `charge_order_id` bigint unsigned NOT NULL,
  `stop_command_id` varchar(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `meter_json` json NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`charge_order_id`),
  UNIQUE KEY `uk_stop_command` (`stop_command_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- charge_event_log：订单状态事件及时间线
CREATE TABLE `charge_event_log` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `charge_order_id` bigint unsigned NOT NULL,
  `event_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `event` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `actor` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `detail` varchar(512) COLLATE utf8mb4_unicode_ci NOT NULL,
  `occurred_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_event` (`event_id`),
  KEY `idx_order_time` (`charge_order_id`,`occurred_at`,`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- charge_fee_receipt：计费消费事件幂等回执
CREATE TABLE `charge_fee_receipt` (
  `charge_order_id` bigint unsigned NOT NULL,
  `calculation_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `result_json` json NOT NULL,
  `shortfall_cents` bigint NOT NULL DEFAULT '0',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`charge_order_id`),
  UNIQUE KEY `calculation_no` (`calculation_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- charge_manual_settlement：无法自动计量时的人工最终结算与审计
CREATE TABLE `charge_manual_settlement` (
  `charge_order_id` bigint unsigned NOT NULL,
  `request_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `actor_id` bigint unsigned NOT NULL,
  `electric_cents` bigint NOT NULL,
  `service_cents` bigint NOT NULL,
  `reason` varchar(1000) NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`charge_order_id`),
  UNIQUE KEY `request_id` (`request_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- charge_meter_review：充电计量异常核对
CREATE TABLE `charge_meter_review` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `request_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `charge_order_id` bigint unsigned NOT NULL,
  `original_json` json NOT NULL,
  `corrected_json` json NOT NULL,
  `reason` varchar(500) NOT NULL,
  `status` enum('awaiting_second','approved','rejected') NOT NULL DEFAULT 'awaiting_second',
  `first_reviewer_id` bigint unsigned NOT NULL,
  `second_reviewer_id` bigint unsigned DEFAULT NULL,
  `reject_reason` varchar(500) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `reviewed_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_request` (`request_id`),
  KEY `idx_order` (`charge_order_id`,`id`),
  CONSTRAINT `charge_meter_review_chk_1` CHECK (((`second_reviewer_id` is null) or (`second_reviewer_id` <> `first_reviewer_id`)))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- charge_order：充电订单(纯生命周期)
CREATE TABLE `charge_order` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `order_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '北京时间启动请求年月日时分秒+设备编号+至少两位端口号',
  `user_id` bigint unsigned NOT NULL,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `port_no` tinyint unsigned NOT NULL,
  `port_code` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `payment_order_id` bigint unsigned DEFAULT NULL,
  `status` enum('pending_payment','paid','charging','completed','cancelled','failed','refunding','refunded') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending_payment',
  `started_at` datetime(3) DEFAULT NULL,
  `ended_at` datetime(3) DEFAULT NULL,
  `charged_kwh` decimal(12,4) DEFAULT NULL,
  `charged_seconds` int unsigned DEFAULT NULL,
  `peak_kwh` decimal(12,4) DEFAULT NULL,
  `off_kwh` decimal(12,4) DEFAULT NULL,
  `electric_cents` bigint DEFAULT NULL,
  `service_cents` bigint DEFAULT NULL,
  `total_cents` bigint DEFAULT NULL,
  `failure_reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_month` date NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  `deleted_by` bigint unsigned DEFAULT NULL,
  `charge_mode` tinyint unsigned DEFAULT NULL,
  `charge_quantity` smallint unsigned DEFAULT NULL,
  `discount_cents` bigint NOT NULL DEFAULT '0',
  PRIMARY KEY (`id`,`created_month`),
  UNIQUE KEY `uk_order_no` (`order_no`,`created_month`),
  KEY `idx_user_created` (`user_id`,`created_at`),
  KEY `idx_status` (`status`),
  KEY `idx_payment` (`payment_order_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='充电订单(纯生命周期)'
/*!50100 PARTITION BY RANGE (to_days(`created_month`))
(PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB,
 PARTITION p_2026m10 VALUES LESS THAN (740286) ENGINE = InnoDB,
 PARTITION p_2026m11 VALUES LESS THAN (740316) ENGINE = InnoDB,
 PARTITION p_2026m12 VALUES LESS THAN (740347) ENGINE = InnoDB,
 PARTITION p_2027q1 VALUES LESS THAN (740437) ENGINE = InnoDB,
 PARTITION p_2027q2 VALUES LESS THAN (740528) ENGINE = InnoDB,
 PARTITION p_2027q3 VALUES LESS THAN (740620) ENGINE = InnoDB,
 PARTITION p_2027q4 VALUES LESS THAN (740712) ENGINE = InnoDB,
 PARTITION p_max VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */;

-- charge_order_pricing：订单冻结的完整方案与计算快照
CREATE TABLE `charge_order_pricing` (
  `charge_order_id` bigint unsigned NOT NULL,
  `payment_intent_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `port_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `pricing_snapshot` json NOT NULL,
  `confirmed_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`charge_order_id`),
  UNIQUE KEY `uk_payment_intent` (`payment_intent_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- charge_payment_intent：支付前冻结方案及端口预占
CREATE TABLE `charge_payment_intent` (
  `intent_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `client_request_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `merchant_order_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `payment_order_id` bigint unsigned NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `openid` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `port_no` tinyint unsigned NOT NULL,
  `port_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `station_id` bigint unsigned NOT NULL,
  `pricing_rule_id` bigint unsigned NOT NULL,
  `pricing_rule_version` int unsigned NOT NULL,
  `pricing_snapshot` json NOT NULL,
  `estimated_kwh` decimal(9,3) NOT NULL,
  `estimated_minutes` smallint unsigned NOT NULL,
  `electric_cents` bigint NOT NULL,
  `service_cents` bigint NOT NULL,
  `total_cents` bigint NOT NULL,
  `discount_cents` bigint NOT NULL DEFAULT '0',
  `charge_mode` tinyint unsigned NOT NULL,
  `charge_quantity` smallint unsigned NOT NULL,
  `status` enum('initiated','paid','expired','closed','refund_required') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'initiated',
  `active_port_code` varchar(64) COLLATE utf8mb4_unicode_ci GENERATED ALWAYS AS ((case when (`status` = _utf8mb4'initiated') then `port_code` else NULL end)) STORED,
  `expires_at` datetime(3) NOT NULL,
  `paid_at` datetime(3) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `charge_order_id` bigint unsigned DEFAULT NULL,
  `coupon_grant_id` bigint unsigned DEFAULT NULL,
  `offer_id` bigint unsigned DEFAULT NULL,
  PRIMARY KEY (`intent_id`),
  UNIQUE KEY `uk_user_request` (`user_id`,`client_request_id`),
  UNIQUE KEY `uk_merchant_order` (`merchant_order_no`),
  UNIQUE KEY `uk_payment_order` (`payment_order_id`),
  UNIQUE KEY `uk_active_port` (`active_port_code`),
  UNIQUE KEY `uk_charge_order` (`charge_order_id`),
  KEY `idx_expiry` (`status`,`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- charge_port_lock：支付与在线卡共享的端口事务锁
CREATE TABLE `charge_port_lock` (
  `port_code` varchar(64) NOT NULL,
  PRIMARY KEY (`port_code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- charge_prepay：预付支付确认结果
CREATE TABLE `charge_prepay` (
  `payment_order_id` bigint unsigned NOT NULL,
  `params_json` json NOT NULL,
  `prepay_id` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`payment_order_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- charge_start_receipt：设备启动确认和幂等摘要
CREATE TABLE `charge_start_receipt` (
  `charge_order_id` bigint unsigned NOT NULL,
  `command_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `success` tinyint(1) NOT NULL,
  `port_id` bigint unsigned DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `order_no` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `port_no` tinyint unsigned DEFAULT NULL,
  `result_code` tinyint unsigned DEFAULT NULL,
  `occurred_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`charge_order_id`),
  UNIQUE KEY `uk_command` (`command_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- coupon：优惠券模板
CREATE TABLE `coupon` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `name_i18n` json DEFAULT NULL,
  `discount_type` enum('amount','percentage','time_free') COLLATE utf8mb4_unicode_ci NOT NULL,
  `discount_value_cents` bigint DEFAULT NULL COMMENT 'amount 模式:减 N 分',
  `discount_percent` decimal(5,2) DEFAULT NULL COMMENT 'percentage 模式:N% 折扣',
  `min_charge_cents` bigint NOT NULL DEFAULT '0' COMMENT '最低消费门槛',
  `valid_hours` int unsigned NOT NULL DEFAULT '24' COMMENT '领取后有效小时',
  `total_quota` int unsigned NOT NULL DEFAULT '0' COMMENT '0=无限',
  `per_user_quota` int unsigned NOT NULL DEFAULT '1',
  `status` enum('active','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active',
  `start_at` datetime(3) DEFAULT NULL,
  `end_at` datetime(3) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  `deleted_by` bigint unsigned DEFAULT NULL,
  `free_minutes` int unsigned DEFAULT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='优惠券模板';

-- coupon_activity_rule：优惠券活动规则及版本
CREATE TABLE `coupon_activity_rule` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `trigger_type` enum('first_recharge','invite_reward','threshold_redeem','holiday') COLLATE utf8mb4_unicode_ci NOT NULL,
  `coupon_id` bigint unsigned NOT NULL,
  `inviter_coupon_id` bigint unsigned DEFAULT NULL,
  `threshold_cents` bigint NOT NULL DEFAULT '0',
  `max_grants` int NOT NULL DEFAULT '0',
  `per_user_limit` int NOT NULL DEFAULT '1',
  `status` enum('active','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active',
  `start_at` datetime(3) NOT NULL,
  `end_at` datetime(3) NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  `deleted_by` bigint unsigned DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_activity_rule_active` (`trigger_type`,`status`,`start_at`,`end_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- coupon_grant：优惠券发放记录
CREATE TABLE `coupon_grant` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `coupon_id` bigint unsigned NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `grant_source` enum('register','activity','invite','manual','invite_reward') COLLATE utf8mb4_unicode_ci NOT NULL,
  `status` enum('unused','used','expired') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'unused',
  `used_payment_order_id` bigint unsigned DEFAULT NULL,
  `used_at` datetime(3) DEFAULT NULL,
  `expired_at` datetime(3) NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  `source_event_id` char(36) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_coupon_grant_source_event` (`source_event_id`),
  KEY `idx_user_status` (`user_id`,`status`),
  KEY `idx_coupon` (`coupon_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='优惠券发放记录';

-- coupon_grant_request：优惠券发放请求幂等回执
CREATE TABLE `coupon_grant_request` (
  `request_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `coupon_id` bigint unsigned NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `coupon_grant_id` bigint unsigned NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`request_id`),
  UNIQUE KEY `uk_coupon_grant_request_grant` (`coupon_grant_id`),
  KEY `idx_coupon_grant_request_coupon_user` (`coupon_id`,`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='优惠券发放请求幂等回执';

-- coupon_redemption：优惠券核销记录
CREATE TABLE `coupon_redemption` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `coupon_id` bigint unsigned DEFAULT NULL,
  `user_id` bigint unsigned NOT NULL,
  `biz_type` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL,
  `biz_id` bigint unsigned NOT NULL,
  `discount_cents` bigint NOT NULL,
  `order_no` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `redeemed_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_biz` (`biz_type`,`biz_id`),
  KEY `idx_redemption_coupon` (`coupon_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='优惠券核销记录';

-- device_fault_report：设备报修
CREATE TABLE `device_fault_report` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `user_id` bigint unsigned DEFAULT NULL,
  `report_source` enum('user','inspect','monitor') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'user',
  `fault_type` enum('mechanical','electrical','communication','display','other') COLLATE utf8mb4_unicode_ci NOT NULL,
  `description` text COLLATE utf8mb4_unicode_ci,
  `images_json` json DEFAULT NULL,
  `status` enum('open','dispatched','fixed','closed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'open',
  `assigned_to` bigint unsigned DEFAULT NULL,
  `resolved_at` datetime(3) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_device_status` (`device_id`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备报修';

-- device_fault_report_event：设备报修状态与巡检处理记录
CREATE TABLE `device_fault_report_event` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `report_id` bigint unsigned NOT NULL,
  `actor_id` bigint unsigned DEFAULT NULL,
  `event_type` enum('reported','dispatched','reassigned','fixed','closed','migration_baseline') COLLATE utf8mb4_unicode_ci NOT NULL,
  `from_status` varchar(24) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `to_status` varchar(24) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `assigned_to` bigint unsigned DEFAULT NULL,
  `note` varchar(2000) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `user_visible` tinyint(1) NOT NULL DEFAULT '1',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_fault_event_report_time` (`report_id`,`created_at`,`id`),
  KEY `idx_fault_event_actor_time` (`actor_id`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备报修状态与巡检处理记录';

-- event_outbox：事件 outbox(可靠发布)
CREATE TABLE `event_outbox` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `event_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `stream` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '目标 stream 名',
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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='事件 outbox(可靠发布)';

-- feedback：评价/投诉
CREATE TABLE `feedback` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `user_id` bigint unsigned NOT NULL,
  `order_id` bigint unsigned DEFAULT NULL,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `rating` tinyint unsigned DEFAULT NULL COMMENT '1-5 星',
  `category` enum('rating','complaint','suggestion') COLLATE utf8mb4_unicode_ci NOT NULL,
  `content` text COLLATE utf8mb4_unicode_ci,
  `images_json` json DEFAULT NULL,
  `status` enum('pending','processed','closed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `replied_by` bigint unsigned DEFAULT NULL,
  `replied_at` datetime(3) DEFAULT NULL,
  `reply_content` text COLLATE utf8mb4_unicode_ci,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_user_created` (`user_id`,`created_at`),
  KEY `idx_feedback_order` (`order_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='评价/投诉';

-- invoice_admin_review：发票审核记录
CREATE TABLE `invoice_admin_review` (
  `invoice_request_id` bigint unsigned NOT NULL,
  `review_status` enum('awaiting_second','approved','rejected') COLLATE utf8mb4_unicode_ci NOT NULL,
  `first_reviewer_id` bigint unsigned DEFAULT NULL,
  `second_reviewer_id` bigint unsigned DEFAULT NULL,
  `invoice_url` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `reject_reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`invoice_request_id`),
  CONSTRAINT `invoice_admin_review_chk_1` CHECK (((`second_reviewer_id` is null) or (`second_reviewer_id` <> `first_reviewer_id`)))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- invoice_request：发票申请
CREATE TABLE `invoice_request` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `invoice_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `biz_type` enum('charge','wallet_recharge') COLLATE utf8mb4_unicode_ci NOT NULL,
  `biz_id` bigint unsigned NOT NULL,
  `total_cents` bigint NOT NULL,
  `invoice_type` enum('normal','vat_special') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'normal',
  `title` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `tax_no` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `email` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `review_status` enum('pending','approved','rejected','issued') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `reviewed_by` bigint unsigned DEFAULT NULL,
  `reviewed_at` datetime(3) DEFAULT NULL,
  `reject_reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `invoice_url` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_invoice_no` (`invoice_no`),
  KEY `idx_user_created` (`user_id`,`created_at`),
  KEY `idx_review` (`review_status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='发票申请';

-- manual_refund_request：人工退款申请及审核
CREATE TABLE `manual_refund_request` (
  `request_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `payload_json` json NOT NULL,
  `refund_no` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`request_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- membership_card：会员卡(预留)
CREATE TABLE `membership_card` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `user_id` bigint unsigned NOT NULL,
  `card_type` enum('month','year','quarter') COLLATE utf8mb4_unicode_ci NOT NULL,
  `status` enum('active','expired','refunded') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active',
  `start_at` datetime(3) NOT NULL,
  `end_at` datetime(3) NOT NULL,
  `price_cents` bigint NOT NULL,
  `payment_order_id` bigint unsigned DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_user_status` (`user_id`,`status`),
  KEY `idx_end` (`end_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='会员卡(预留)';

-- online_card：用户在线卡绑定和挂失状态
CREATE TABLE `online_card` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `card_no` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `status` enum('active','lost','disabled','unbound') NOT NULL DEFAULT 'active',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `card_no` (`card_no`),
  KEY `idx_user` (`user_id`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- online_card_audit：在线卡操作审计
CREATE TABLE `online_card_audit` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `card_id` bigint unsigned NOT NULL,
  `actor_id` bigint unsigned NOT NULL,
  `action` varchar(32) NOT NULL,
  `detail` varchar(1000) NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- payment_callback_idempotent：微信支付回调幂等(30 天保留)
CREATE TABLE `payment_callback_idempotent` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `wechat_transaction_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `processed_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `request_digest` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_txn` (`wechat_transaction_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='微信支付回调幂等(30 天保留)';

-- payment_order：支付订单(支持 charge / wallet_recharge)
CREATE TABLE `payment_order` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `order_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `biz_type` enum('charge','wallet_recharge') COLLATE utf8mb4_unicode_ci NOT NULL,
  `biz_id` bigint unsigned NOT NULL COMMENT '关联 charge_order.id 或 wallet_txn.id',
  `user_id` bigint unsigned NOT NULL,
  `pay_method` enum('wechat','balance','mixed','coupon') COLLATE utf8mb4_unicode_ci NOT NULL,
  `total_cents` bigint NOT NULL COMMENT '订单总金额(分)',
  `paid_cents` bigint NOT NULL DEFAULT '0',
  `refunded_cents` bigint NOT NULL DEFAULT '0',
  `wechat_transaction_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '微信侧 transaction_id',
  `status` enum('initiated','paid','closed','refunded','partial_refunded','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'initiated',
  `paid_at` datetime(3) DEFAULT NULL,
  `closed_at` datetime(3) DEFAULT NULL,
  `expired_at` datetime(3) DEFAULT NULL,
  `failure_reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `client_ip` varchar(45) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_month` date NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  `deleted_by` bigint unsigned DEFAULT NULL,
  `discount_cents` bigint NOT NULL DEFAULT '0',
  `coupon_grant_id` bigint unsigned DEFAULT NULL,
  PRIMARY KEY (`id`,`created_month`),
  UNIQUE KEY `uk_order_no` (`order_no`,`created_month`),
  UNIQUE KEY `uk_wechat_txn` (`wechat_transaction_id`,`created_month`),
  KEY `idx_biz` (`biz_type`,`biz_id`),
  KEY `idx_user_created` (`user_id`,`created_at`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='支付订单(支持 charge / wallet_recharge)'
/*!50100 PARTITION BY RANGE (to_days(`created_month`))
(PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB,
 PARTITION p_2026m10 VALUES LESS THAN (740286) ENGINE = InnoDB,
 PARTITION p_2026m11 VALUES LESS THAN (740316) ENGINE = InnoDB,
 PARTITION p_2026m12 VALUES LESS THAN (740347) ENGINE = InnoDB,
 PARTITION p_2027q1 VALUES LESS THAN (740437) ENGINE = InnoDB,
 PARTITION p_2027q2 VALUES LESS THAN (740528) ENGINE = InnoDB,
 PARTITION p_2027q3 VALUES LESS THAN (740620) ENGINE = InnoDB,
 PARTITION p_2027q4 VALUES LESS THAN (740712) ENGINE = InnoDB,
 PARTITION p_max VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */;

-- port_view：找桩缓存(冗余自 admin_db)
CREATE TABLE `port_view` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `station_id` bigint unsigned NOT NULL,
  `station_name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `address` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `longitude` decimal(11,8) NOT NULL,
  `latitude` decimal(10,8) NOT NULL,
  `available_ports` int unsigned NOT NULL DEFAULT '0',
  `total_ports` int unsigned NOT NULL DEFAULT '0',
  `last_synced_at` datetime(3) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_geo` (`latitude`,`longitude`),
  KEY `idx_station` (`station_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='找桩缓存(冗余自 admin_db)';

-- refund_execution：退款渠道执行及重试
CREATE TABLE `refund_execution` (
  `refund_record_id` bigint unsigned NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`refund_record_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- refund_reconcile_diff：每日对账差异(微信账单 vs 内部退款)
CREATE TABLE `refund_reconcile_diff` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `reconcile_date` date NOT NULL,
  `internal_count` int unsigned NOT NULL,
  `wechat_count` int unsigned NOT NULL,
  `diff_count` int NOT NULL,
  `internal_cents` bigint NOT NULL,
  `wechat_cents` bigint NOT NULL,
  `diff_cents` bigint NOT NULL,
  `diffs_json` json DEFAULT NULL,
  `resolved` tinyint(1) NOT NULL DEFAULT '0',
  `resolved_at` datetime(3) DEFAULT NULL,
  `resolved_by` bigint unsigned DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_date` (`reconcile_date`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='每日对账差异(微信账单 vs 内部退款)';

-- refund_record：退款记录
CREATE TABLE `refund_record` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `refund_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `payment_order_id` bigint unsigned NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `biz_type` enum('charge','wallet_recharge') COLLATE utf8mb4_unicode_ci NOT NULL,
  `biz_id` bigint unsigned NOT NULL,
  `refund_cents` bigint NOT NULL,
  `reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `status` enum('pending','processing','success','failed','rejected') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `retry_count` int unsigned NOT NULL DEFAULT '0',
  `wechat_refund_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `claimed_by` bigint unsigned DEFAULT NULL COMMENT 'admin user id',
  `claimed_at` datetime(3) DEFAULT NULL,
  `completed_at` datetime(3) DEFAULT NULL,
  `failure_reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_month` date NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  `deleted_by` bigint unsigned DEFAULT NULL,
  `execution_policy` enum('manual_review','automatic') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'manual_review',
  `next_attempt_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`,`created_month`),
  UNIQUE KEY `uk_refund_no` (`refund_no`,`created_month`),
  KEY `idx_payment` (`payment_order_id`),
  KEY `idx_status` (`status`),
  KEY `idx_refund_dispatch` (`execution_policy`,`status`,`next_attempt_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='退款记录'
/*!50100 PARTITION BY RANGE (to_days(`created_month`))
(PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB,
 PARTITION p_2026m10 VALUES LESS THAN (740286) ENGINE = InnoDB,
 PARTITION p_2026m11 VALUES LESS THAN (740316) ENGINE = InnoDB,
 PARTITION p_2026m12 VALUES LESS THAN (740347) ENGINE = InnoDB,
 PARTITION p_2027q1 VALUES LESS THAN (740437) ENGINE = InnoDB,
 PARTITION p_2027q2 VALUES LESS THAN (740528) ENGINE = InnoDB,
 PARTITION p_2027q3 VALUES LESS THAN (740620) ENGINE = InnoDB,
 PARTITION p_2027q4 VALUES LESS THAN (740712) ENGINE = InnoDB,
 PARTITION p_max VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */;

-- refund_rejection：退款拒绝原因记录
CREATE TABLE `refund_rejection` (
  `refund_record_id` bigint unsigned NOT NULL,
  `actor_id` bigint unsigned NOT NULL,
  `reason` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`refund_record_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- refund_review：退款审核过程
CREATE TABLE `refund_review` (
  `refund_record_id` bigint unsigned NOT NULL,
  `snapshot_json` json NOT NULL,
  `first_signer` bigint unsigned NOT NULL,
  `first_comment` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `second_signer` bigint unsigned DEFAULT NULL,
  `second_comment` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `approved_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`refund_record_id`),
  CONSTRAINT `refund_review_chk_1` CHECK (((`second_signer` is null) or (`second_signer` <> `first_signer`)))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- refund_success_receipt：退款成功的幂等确认
CREATE TABLE `refund_success_receipt` (
  `refund_record_id` bigint unsigned NOT NULL,
  `wechat_refund_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`refund_record_id`),
  UNIQUE KEY `wechat_refund_id` (`wechat_refund_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- risk_freeze_log：风控冻结记录(本期仅频次触发)
CREATE TABLE `risk_freeze_log` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `user_id` bigint unsigned NOT NULL,
  `trigger_rule` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `frozen_action` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `window_minutes` int unsigned DEFAULT NULL,
  `threshold_value` int unsigned DEFAULT NULL,
  `actual_value` int unsigned DEFAULT NULL,
  `unfreeze_at` datetime(3) DEFAULT NULL,
  `status` enum('frozen','unfrozen') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'frozen',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_user_status` (`user_id`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='风控冻结记录(本期仅频次触发)';

-- user：终端用户
CREATE TABLE `user` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `openid` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '微信 openid',
  `unionid` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `nickname` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `avatar_url` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `phone_enc` varbinary(255) DEFAULT NULL COMMENT 'AES_ENCRYPT 加密',
  `phone_hash` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '查询用 SHA-256 hash(不可逆)',
  `gender` enum('unknown','male','female') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'unknown',
  `status` enum('active','frozen') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active',
  `first_seen_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `last_login_at` datetime(3) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  `deleted_by` bigint unsigned DEFAULT NULL,
  `inviter_id` bigint unsigned DEFAULT NULL COMMENT '介绍人 user.id，绑定后不可自行更改',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_openid` (`openid`,`deleted_at`),
  UNIQUE KEY `uk_phone_hash` (`phone_hash`),
  KEY `idx_status` (`status`),
  KEY `idx_user_inviter` (`inviter_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='终端用户';

-- user_login_identity：用户登录身份关联
CREATE TABLE `user_login_identity` (
  `openid` varbinary(64) NOT NULL,
  PRIMARY KEY (`openid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- wallet_account：钱包账户(1:1 with user)
CREATE TABLE `wallet_account` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `user_id` bigint unsigned NOT NULL,
  `balance_cents` bigint NOT NULL DEFAULT '0' COMMENT '余额(分)',
  `frozen_cents` bigint NOT NULL DEFAULT '0' COMMENT '冻结金额(提现中)',
  `status` enum('active','frozen') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active',
  `version` bigint unsigned NOT NULL DEFAULT '0' COMMENT '乐观锁',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  `deleted_by` bigint unsigned DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user` (`user_id`,`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='钱包账户(1:1 with user)';

-- wallet_recharge_request：钱包充值请求幂等键
CREATE TABLE `wallet_recharge_request` (
  `request_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `amount_cents` bigint NOT NULL,
  `payment_order_id` bigint unsigned DEFAULT NULL,
  `request_json` json DEFAULT NULL,
  `prepay_id` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`request_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- wallet_refund_part：钱包退款按原支付渠道拆分明细
CREATE TABLE `wallet_refund_part` (
  `refund_record_id` bigint unsigned NOT NULL,
  `request_id` varchar(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `wallet_account_id` bigint unsigned NOT NULL,
  `amount_cents` bigint NOT NULL,
  `settled` tinyint(1) NOT NULL DEFAULT '0',
  PRIMARY KEY (`refund_record_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- wallet_refund_request：钱包退款请求及处理结果
CREATE TABLE `wallet_refund_request` (
  `request_id` varchar(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `amount_cents` bigint NOT NULL,
  `reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `response_json` json DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`request_id`),
  KEY `idx_user_time` (`user_id`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- wallet_risk_freeze_link：钱包风控冻结关联
CREATE TABLE `wallet_risk_freeze_link` (
  `request_id` varchar(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `freeze_id` bigint unsigned NOT NULL,
  PRIMARY KEY (`request_id`),
  UNIQUE KEY `freeze_id` (`freeze_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- wallet_risk_release：钱包风控解除记录
CREATE TABLE `wallet_risk_release` (
  `request_id` varchar(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `actor_id` bigint unsigned NOT NULL,
  `comment` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `response_json` json NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`request_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- wallet_risk_review：钱包风控审核记录
CREATE TABLE `wallet_risk_review` (
  `request_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `actor_id` bigint unsigned NOT NULL,
  `approved` tinyint(1) NOT NULL,
  `comment` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `response_json` json NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`request_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- wallet_txn：钱包流水
CREATE TABLE `wallet_txn` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `txn_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `wallet_account_id` bigint unsigned NOT NULL,
  `direction` enum('in','out') COLLATE utf8mb4_unicode_ci NOT NULL,
  `amount_cents` bigint NOT NULL,
  `balance_after_cents` bigint NOT NULL,
  `biz_type` enum('recharge','pay','refund','gift','freeze','unfreeze','adjust') COLLATE utf8mb4_unicode_ci NOT NULL,
  `biz_ref` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '关联业务单号',
  `note` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_month` date NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`,`created_month`),
  UNIQUE KEY `uk_txn_no` (`txn_no`,`created_month`),
  KEY `idx_user_time` (`user_id`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='钱包流水'
/*!50100 PARTITION BY RANGE (to_days(`created_month`))
(PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB,
 PARTITION p_2026m10 VALUES LESS THAN (740286) ENGINE = InnoDB,
 PARTITION p_2026m11 VALUES LESS THAN (740316) ENGINE = InnoDB,
 PARTITION p_2026m12 VALUES LESS THAN (740347) ENGINE = InnoDB,
 PARTITION p_2027q1 VALUES LESS THAN (740437) ENGINE = InnoDB,
 PARTITION p_2027q2 VALUES LESS THAN (740528) ENGINE = InnoDB,
 PARTITION p_2027q3 VALUES LESS THAN (740620) ENGINE = InnoDB,
 PARTITION p_2027q4 VALUES LESS THAN (740712) ENGINE = InnoDB,
 PARTITION p_max VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */;

-- 初始化数据：内置权限、角色及系统默认配置。业务与演示数据另行创建。

-- +goose Down
-- 仅供一次性开发/测试库回退；删除当前库全部业务表。
DROP TABLE IF EXISTS `wallet_txn`;
DROP TABLE IF EXISTS `wallet_risk_review`;
DROP TABLE IF EXISTS `wallet_risk_release`;
DROP TABLE IF EXISTS `wallet_risk_freeze_link`;
DROP TABLE IF EXISTS `wallet_refund_request`;
DROP TABLE IF EXISTS `wallet_refund_part`;
DROP TABLE IF EXISTS `wallet_recharge_request`;
DROP TABLE IF EXISTS `wallet_account`;
DROP TABLE IF EXISTS `user_login_identity`;
DROP TABLE IF EXISTS `user`;
DROP TABLE IF EXISTS `risk_freeze_log`;
DROP TABLE IF EXISTS `refund_success_receipt`;
DROP TABLE IF EXISTS `refund_review`;
DROP TABLE IF EXISTS `refund_rejection`;
DROP TABLE IF EXISTS `refund_record`;
DROP TABLE IF EXISTS `refund_reconcile_diff`;
DROP TABLE IF EXISTS `refund_execution`;
DROP TABLE IF EXISTS `port_view`;
DROP TABLE IF EXISTS `payment_order`;
DROP TABLE IF EXISTS `payment_callback_idempotent`;
DROP TABLE IF EXISTS `online_card_audit`;
DROP TABLE IF EXISTS `online_card`;
DROP TABLE IF EXISTS `membership_card`;
DROP TABLE IF EXISTS `manual_refund_request`;
DROP TABLE IF EXISTS `invoice_request`;
DROP TABLE IF EXISTS `invoice_admin_review`;
DROP TABLE IF EXISTS `feedback`;
DROP TABLE IF EXISTS `event_outbox`;
DROP TABLE IF EXISTS `device_fault_report_event`;
DROP TABLE IF EXISTS `device_fault_report`;
DROP TABLE IF EXISTS `coupon_redemption`;
DROP TABLE IF EXISTS `coupon_grant_request`;
DROP TABLE IF EXISTS `coupon_grant`;
DROP TABLE IF EXISTS `coupon_activity_rule`;
DROP TABLE IF EXISTS `coupon`;
DROP TABLE IF EXISTS `charge_start_receipt`;
DROP TABLE IF EXISTS `charge_prepay`;
DROP TABLE IF EXISTS `charge_port_lock`;
DROP TABLE IF EXISTS `charge_payment_intent`;
DROP TABLE IF EXISTS `charge_order_pricing`;
DROP TABLE IF EXISTS `charge_order`;
DROP TABLE IF EXISTS `charge_meter_review`;
DROP TABLE IF EXISTS `charge_manual_settlement`;
DROP TABLE IF EXISTS `charge_fee_receipt`;
DROP TABLE IF EXISTS `charge_event_log`;
DROP TABLE IF EXISTS `charge_end_receipt`;
DROP TABLE IF EXISTS `charge_debt_receipt`;
DROP TABLE IF EXISTS `charge_debt`;
DROP TABLE IF EXISTS `charge_billing_job`;
DROP TABLE IF EXISTS `charge_billing_cutoff`;
DROP TABLE IF EXISTS `charge_bill_read`;
DROP TABLE IF EXISTS `charge_bill`;
DROP TABLE IF EXISTS `card_operation`;
DROP TABLE IF EXISTS `card_charge`;
DROP TABLE IF EXISTS `audit_log`;
DROP TABLE IF EXISTS `active_port_charge`;
