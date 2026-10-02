-- +goose NO TRANSACTION
-- +goose Up

-- central_db：用户、运营及计费领域共享一个事务数据库。
SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci;
SET sql_mode = 'STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION,ERROR_FOR_DIVISION_BY_ZERO,NO_ZERO_DATE,NO_ZERO_IN_DATE';

-- user_db 领域表
-- active_port_charge：端口当前充电占用(跨月唯一性兜底)
CREATE TABLE `active_port_charge` (
  `port_id` bigint unsigned NOT NULL COMMENT '复合 ID: device_id+port_no',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `port_no` tinyint unsigned NOT NULL COMMENT '设备充电端口号，从 1 开始',
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `started_at` datetime(3) NOT NULL COMMENT '业务启动时间',
  `ended_at` datetime(3) DEFAULT NULL COMMENT '业务结束时间',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  PRIMARY KEY (`port_id`),
  UNIQUE KEY `uk_device_port` (`device_id`,`port_no`),
  KEY `idx_order` (`charge_order_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='端口当前充电占用(跨月唯一性兜底)';


-- card_charge：在线卡充电会话与累计购买时长
CREATE TABLE `card_charge` (
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `card_id` bigint unsigned NOT NULL COMMENT '在线卡 ID',
  `port_code` varchar(64) NOT NULL COMMENT '充电端口二维码唯一编码',
  `active_port` varchar(64) DEFAULT NULL COMMENT '当前在线卡充电占用的端口编码，结束后清空以释放唯一占用',
  `paid_cents` bigint NOT NULL COMMENT '已支付金额，单位分',
  `purchased_minutes` smallint unsigned NOT NULL COMMENT '在线卡会话累计购买时长，单位分钟',
  `max_minutes` smallint unsigned NOT NULL COMMENT '本次在线卡会话允许累计购买的时长上限，单位分钟',
  `card_no` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '在线卡卡号',
  `wallet_after_cents` bigint NOT NULL COMMENT '在线卡扣款后的钱包余额，单位分',
  `package_json` json NOT NULL COMMENT '在线卡购买的冻结套餐快照 JSON',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`charge_order_id`),
  UNIQUE KEY `active_port` (`active_port`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='在线卡充电会话与累计购买时长';

-- card_operation：在线卡刷卡操作幂等结果和扣款记录
CREATE TABLE `card_operation` (
  `operation_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '在线卡操作 ID',
  `device_id` varchar(64) NOT NULL COMMENT '设备全局唯一编号',
  `event_id` varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '事件唯一标识，用于追踪和幂等处理',
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `card_id` bigint unsigned NOT NULL COMMENT '在线卡 ID',
  `kind` enum('start','extend') NOT NULL COMMENT '在线卡刷卡操作；start 启动充电、extend 增加时长',
  `price_cents` bigint NOT NULL COMMENT '销售价格，单位分',
  `minutes` smallint unsigned NOT NULL COMMENT '本次在线卡操作购买的时长，单位分钟',
  `status` enum('confirming','confirmed','failed') NOT NULL DEFAULT 'confirming' COMMENT '当前业务状态；取值 confirming / confirmed / failed',
  `failure_reason` varchar(255) DEFAULT NULL COMMENT '业务执行失败原因',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `confirmed_at` datetime(3) DEFAULT NULL COMMENT '业务确认时间',
  PRIMARY KEY (`operation_id`),
  UNIQUE KEY `uk_device_event` (`device_id`,`event_id`),
  KEY `idx_order` (`charge_order_id`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='在线卡刷卡操作幂等结果和扣款记录';

-- charge_bill：充电账单
CREATE TABLE `charge_bill` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `bill_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '充电账单编号',
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `payment_order_id` bigint unsigned DEFAULT NULL COMMENT '支付订单 ID',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `port_no` tinyint unsigned NOT NULL DEFAULT '0' COMMENT '设备充电端口号，从 1 开始',
  `electric_cents` bigint NOT NULL DEFAULT '0' COMMENT '电费，单位分',
  `service_cents` bigint NOT NULL DEFAULT '0' COMMENT '服务费，单位分',
  `total_cents` bigint NOT NULL DEFAULT '0' COMMENT '总金额，单位分',
  `prepaid_cents` bigint NOT NULL DEFAULT '0' COMMENT '预付金额，单位分',
  `refund_cents` bigint NOT NULL DEFAULT '0' COMMENT '本次退款金额，单位分',
  `shortfall_cents` bigint NOT NULL DEFAULT '0' COMMENT '结算费用缺口，单位分',
  `charged_kwh` decimal(12,4) DEFAULT NULL COMMENT '累计充电电量，单位 kWh',
  `charged_seconds` int unsigned DEFAULT NULL COMMENT '累计充电时长，单位秒',
  `status` enum('issued','settled','voided') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'issued' COMMENT '当前业务状态；取值 issued / settled / voided',
  `issued_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '账单签发时间',
  `settled_at` datetime(3) DEFAULT NULL COMMENT '账单结算时间',
  `created_month` date NOT NULL COMMENT '月分区归属日期，取对应月份第一天',
  PRIMARY KEY (`id`,`created_month`),
  UNIQUE KEY `uk_order` (`charge_order_id`,`created_month`),
  UNIQUE KEY `uk_bill_no` (`bill_no`,`created_month`),
  KEY `idx_user` (`user_id`,`issued_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='充电账单';

-- charge_bill_read：账单已读标记
CREATE TABLE `charge_bill_read` (
  `bill_id` bigint unsigned NOT NULL COMMENT '充电账单 ID',
  `created_month` date NOT NULL COMMENT '月分区归属日期，取对应月份第一天',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `read_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '账单阅读时间',
  PRIMARY KEY (`bill_id`,`created_month`),
  KEY `idx_user` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='账单已读标记';

-- charge_billing_cutoff：订单首次计费截止点
CREATE TABLE `charge_billing_cutoff` (
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `cutoff_at` datetime(3) NOT NULL COMMENT '首次计费截止时间',
  `reason` varchar(64) NOT NULL COMMENT '业务处理或异常原因',
  `electric_cents` bigint DEFAULT NULL COMMENT '预算耗尽时冻结的实收电费，单位分',
  `service_cents` bigint DEFAULT NULL COMMENT '预算耗尽时冻结的实收服务费，单位分',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`charge_order_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='订单首次计费截止点';

-- charge_billing_job：待结算订单任务及重试状态
CREATE TABLE `charge_billing_job` (
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `status` enum('pending','manual_review','done') NOT NULL DEFAULT 'pending' COMMENT '当前业务状态；取值 pending / manual_review / done',
  `attempts` int unsigned NOT NULL DEFAULT '0' COMMENT '已尝试执行次数',
  `next_attempt_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '下次执行尝试时间',
  `last_error` varchar(255) DEFAULT NULL COMMENT '最近一次执行错误信息',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  PRIMARY KEY (`charge_order_id`),
  KEY `idx_due` (`status`,`next_attempt_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='待结算订单任务及重试状态';

-- charge_end_receipt：设备结束事件的幂等回执
CREATE TABLE `charge_end_receipt` (
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `stop_command_id` varchar(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT '设备停止命令 ID',
  `meter_json` json NOT NULL COMMENT '设备最终计量和停止原因 JSON',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`charge_order_id`),
  UNIQUE KEY `uk_stop_command` (`stop_command_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备结束事件的幂等回执';

-- charge_event_log：订单状态事件及时间线
CREATE TABLE `charge_event_log` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `event_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '事件唯一标识，用于追踪和幂等处理',
  `event` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '订单事件名称',
  `actor` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '事件操作方标识',
  `detail` varchar(512) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '事件详细说明',
  `occurred_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '事件发生时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_event` (`event_id`),
  KEY `idx_order_time` (`charge_order_id`,`occurred_at`,`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='订单状态事件及时间线';

-- charge_fee_receipt：计费消费事件幂等回执
CREATE TABLE `charge_fee_receipt` (
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `calculation_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '新C充电订单对应B前缀编号；历史FEE计费编号保留',
  `result_json` json NOT NULL COMMENT '业务执行结果 JSON',
  `shortfall_cents` bigint NOT NULL DEFAULT '0' COMMENT '结算费用缺口，单位分',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`charge_order_id`),
  UNIQUE KEY `calculation_no` (`calculation_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='计费消费事件幂等回执';

-- charge_manual_settlement：无法自动计量时的人工最终结算与审计
CREATE TABLE `charge_manual_settlement` (
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `request_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '幂等请求 ID',
  `actor_id` bigint unsigned NOT NULL COMMENT '操作人 ID',
  `electric_cents` bigint NOT NULL COMMENT '电费，单位分',
  `service_cents` bigint NOT NULL COMMENT '服务费，单位分',
  `reason` varchar(1000) NOT NULL COMMENT '业务处理或异常原因',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`charge_order_id`),
  UNIQUE KEY `request_id` (`request_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='无法自动计量时的人工最终结算与审计';

-- charge_meter_review：充电计量异常核对
CREATE TABLE `charge_meter_review` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `request_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '幂等请求 ID',
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `original_json` json NOT NULL COMMENT '待核实的原始计量数据 JSON',
  `corrected_json` json NOT NULL COMMENT '人工修正后的计量数据 JSON',
  `reason` varchar(500) NOT NULL COMMENT '业务处理或异常原因',
  `status` enum('awaiting_second','approved','rejected') NOT NULL DEFAULT 'awaiting_second' COMMENT '当前业务状态；取值 awaiting_second / approved / rejected',
  `first_reviewer_id` bigint unsigned NOT NULL COMMENT '一级审核管理员 ID',
  `second_reviewer_id` bigint unsigned DEFAULT NULL COMMENT '二级审核管理员 ID',
  `reject_reason` varchar(500) DEFAULT NULL COMMENT '拒绝申请的原因',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `reviewed_at` datetime(3) DEFAULT NULL COMMENT '审核完成时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_request` (`request_id`),
  KEY `idx_order` (`charge_order_id`,`id`),
  CONSTRAINT `charge_meter_review_chk_1` CHECK (((`second_reviewer_id` is null) or (`second_reviewer_id` <> `first_reviewer_id`)))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='充电计量异常核对';

-- charge_order：充电订单(纯生命周期)
CREATE TABLE `charge_order` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `order_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'C+北京时间启动请求年月日时分秒+设备编号+两位端口号01–99',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `port_no` tinyint unsigned NOT NULL COMMENT '设备充电端口号，从 1 开始',
  `port_code` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '充电端口二维码唯一编码',
  `payment_order_id` bigint unsigned DEFAULT NULL COMMENT '支付订单 ID',
  `status` enum('pending_payment','paid','charging','completed','cancelled','failed','refunding','refunded') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending_payment' COMMENT '内部充电生命周期；用于启动授权、结束和退款流转，取值 pending_payment / paid / charging / completed / cancelled / failed / refunding / refunded',
  `business_status` enum('pending_start','charging','completed') COLLATE utf8mb4_unicode_ci GENERATED ALWAYS AS (CASE WHEN `status` IN ('pending_payment','paid') THEN 'pending_start' WHEN `status` = 'charging' THEN 'charging' ELSE 'completed' END) STORED COMMENT '持久化业务状态：pending_start 待启动、charging 充电中、completed 已完成；由内部生命周期生成，不随支付退款状态变化',
  `payment_status` enum('pending','paid','refunded','partial_refunded') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '独立支付状态：pending 待支付、paid 已支付、refunded 已退款、partial_refunded 已部分退款；仅随实际到账或退款成功同步',
  `started_at` datetime(3) DEFAULT NULL COMMENT '业务启动时间',
  `ended_at` datetime(3) DEFAULT NULL COMMENT '业务结束时间',
  `charged_kwh` decimal(12,4) DEFAULT NULL COMMENT '累计充电电量，单位 kWh',
  `charged_seconds` int unsigned DEFAULT NULL COMMENT '累计充电时长，单位秒',
  `peak_kwh` decimal(12,4) DEFAULT NULL COMMENT '峰时段充电电量，单位 kWh',
  `off_kwh` decimal(12,4) DEFAULT NULL COMMENT '谷时段充电电量，单位 kWh',
  `electric_cents` bigint DEFAULT NULL COMMENT '电费，单位分',
  `service_cents` bigint DEFAULT NULL COMMENT '服务费，单位分',
  `total_cents` bigint DEFAULT NULL COMMENT '总金额，单位分',
  `failure_reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '业务执行失败原因',
  `created_month` date NOT NULL COMMENT '月分区归属日期，取对应月份第一天',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  `deleted_by` bigint unsigned DEFAULT NULL COMMENT '删除人 ID',
  `charge_mode` tinyint unsigned DEFAULT NULL COMMENT '设备协议充电模式码，由协议适配器解释',
  `charge_quantity` smallint unsigned DEFAULT NULL COMMENT '下发设备的充电数量，单位由充电模式和协议适配器确定',
  `discount_cents` bigint NOT NULL DEFAULT '0' COMMENT '订单优惠抵扣金额，单位分',
  PRIMARY KEY (`id`,`created_month`),
  UNIQUE KEY `uk_order_no` (`order_no`,`created_month`),
  KEY `idx_user_created` (`user_id`,`created_at`),
  KEY `idx_status` (`status`),
  KEY `idx_business_status` (`business_status`),
  KEY `idx_payment_status` (`payment_status`),
  KEY `idx_payment` (`payment_order_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='充电订单(内部生命周期及独立业务、支付状态)'
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
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `payment_intent_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '支付意图 ID',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `port_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '充电端口二维码唯一编码',
  `pricing_snapshot` json NOT NULL COMMENT '订单冻结的完整计费方案快照 JSON',
  `confirmed_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '业务确认时间',
  PRIMARY KEY (`charge_order_id`),
  UNIQUE KEY `uk_payment_intent` (`payment_intent_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='订单冻结的完整方案与计算快照';

-- charge_payment_intent：支付前冻结方案及端口预占
CREATE TABLE `charge_payment_intent` (
  `intent_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '支付意图 ID',
  `client_request_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '客户端幂等请求 ID',
  `merchant_order_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '支付渠道商户订单编号',
  `payment_order_id` bigint unsigned NOT NULL COMMENT '支付订单 ID',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `openid` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '微信用户在当前应用下的唯一标识',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `port_no` tinyint unsigned NOT NULL COMMENT '设备充电端口号，从 1 开始',
  `port_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '充电端口二维码唯一编码',
  `station_id` bigint unsigned NOT NULL COMMENT '充电站点 ID',
  `pricing_rule_id` bigint unsigned NOT NULL COMMENT '计费规则 ID',
  `pricing_rule_version` int unsigned NOT NULL COMMENT '冻结使用的计费规则版本',
  `pricing_snapshot` json NOT NULL COMMENT '订单冻结的完整计费方案快照 JSON',
  `estimated_kwh` decimal(9,3) NOT NULL COMMENT '支付前预估电量，单位 kWh',
  `estimated_minutes` smallint unsigned NOT NULL COMMENT '支付前预估时长，单位分钟',
  `electric_cents` bigint NOT NULL COMMENT '电费，单位分',
  `service_cents` bigint NOT NULL COMMENT '服务费，单位分',
  `total_cents` bigint NOT NULL COMMENT '总金额，单位分',
  `discount_cents` bigint NOT NULL DEFAULT '0' COMMENT '订单优惠抵扣金额，单位分',
  `charge_mode` tinyint unsigned NOT NULL COMMENT '设备协议充电模式码，由协议适配器解释',
  `charge_quantity` smallint unsigned NOT NULL COMMENT '下发设备的充电数量，单位由充电模式和协议适配器确定',
  `status` enum('initiated','paid','expired','closed','refund_required') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'initiated' COMMENT '当前业务状态；取值 initiated / paid / expired / closed / refund_required',
  `active_port_code` varchar(64) COLLATE utf8mb4_unicode_ci GENERATED ALWAYS AS ((case when (`status` = _utf8mb4'initiated') then `port_code` else NULL end)) STORED COMMENT '待支付意图占用的端口编码，用于约束同一端口仅一个有效预占',
  `expires_at` datetime(3) NOT NULL COMMENT '到期时间',
  `paid_at` datetime(3) DEFAULT NULL COMMENT '支付完成时间',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `charge_order_id` bigint unsigned DEFAULT NULL COMMENT '充电订单 ID',
  `coupon_grant_id` bigint unsigned DEFAULT NULL COMMENT '已发放优惠券 ID',
  `offer_id` bigint unsigned DEFAULT NULL COMMENT '在售充电套餐 ID',
  PRIMARY KEY (`intent_id`),
  UNIQUE KEY `uk_user_request` (`user_id`,`client_request_id`),
  UNIQUE KEY `uk_merchant_order` (`merchant_order_no`),
  UNIQUE KEY `uk_payment_order` (`payment_order_id`),
  UNIQUE KEY `uk_active_port` (`active_port_code`),
  UNIQUE KEY `uk_charge_order` (`charge_order_id`),
  KEY `idx_expiry` (`status`,`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='支付前冻结方案及端口预占';

-- charge_prepay：预付支付确认结果
CREATE TABLE `charge_prepay` (
  `payment_order_id` bigint unsigned NOT NULL COMMENT '支付订单 ID',
  `params_json` json NOT NULL COMMENT '支付渠道预支付参数 JSON',
  `prepay_id` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '支付渠道预支付会话标识',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`payment_order_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='预付支付确认结果';

-- charge_start_receipt：设备启动确认和幂等摘要
CREATE TABLE `charge_start_receipt` (
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `command_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '设备指令 ID',
  `success` tinyint(1) NOT NULL COMMENT '设备启动是否成功，0 否、1 是',
  `port_id` bigint unsigned DEFAULT NULL COMMENT '充电端口 ID',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `order_no` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '业务订单编号',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '设备全局唯一编号',
  `port_no` tinyint unsigned DEFAULT NULL COMMENT '设备充电端口号，从 1 开始',
  `result_code` tinyint unsigned DEFAULT NULL COMMENT '设备指令应答结果码',
  `occurred_at` datetime(3) DEFAULT NULL COMMENT '事件发生时间',
  PRIMARY KEY (`charge_order_id`),
  UNIQUE KEY `uk_command` (`command_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备启动确认和幂等摘要';

-- coupon：优惠券模板
CREATE TABLE `coupon` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务名称',
  `name_i18n` json DEFAULT NULL COMMENT '按语言保存的名称文案 JSON',
  `discount_type` enum('amount','percentage','time_free') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '优惠计算类型；取值 amount / percentage / time_free',
  `discount_value_cents` bigint DEFAULT NULL COMMENT 'amount 模式:减 N 分，单位分',
  `discount_percent` decimal(5,2) DEFAULT NULL COMMENT 'percentage 模式:N% 折扣',
  `min_charge_cents` bigint NOT NULL DEFAULT '0' COMMENT '最低消费门槛，单位分',
  `valid_hours` int unsigned NOT NULL DEFAULT '24' COMMENT '领取后有效小时',
  `total_quota` int unsigned NOT NULL DEFAULT '0' COMMENT '0=无限',
  `per_user_quota` int unsigned NOT NULL DEFAULT '1' COMMENT '单个用户可领取的优惠券数量上限',
  `status` enum('active','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active' COMMENT '当前业务状态；取值 active / disabled',
  `start_at` datetime(3) DEFAULT NULL COMMENT '有效期开始时间',
  `end_at` datetime(3) DEFAULT NULL COMMENT '有效期结束时间',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  `deleted_by` bigint unsigned DEFAULT NULL COMMENT '删除人 ID',
  `free_minutes` int unsigned DEFAULT NULL COMMENT '优惠券免费充电时长，单位分钟',
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='优惠券模板';

-- coupon_activity_rule：优惠券活动规则及版本
CREATE TABLE `coupon_activity_rule` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务名称',
  `trigger_type` enum('first_recharge','invite_reward','threshold_redeem','holiday') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '优惠券活动触发方式；取值 first_recharge / invite_reward / threshold_redeem / holiday',
  `coupon_id` bigint unsigned NOT NULL COMMENT '优惠券模板 ID',
  `inviter_coupon_id` bigint unsigned DEFAULT NULL COMMENT '邀请奖励优惠券模板 ID',
  `threshold_cents` bigint NOT NULL DEFAULT '0' COMMENT '活动触发金额门槛，单位分',
  `max_grants` int NOT NULL DEFAULT '0' COMMENT '活动最多发券数量',
  `per_user_limit` int NOT NULL DEFAULT '1' COMMENT '同一用户可参与活动的次数上限',
  `status` enum('active','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active' COMMENT '当前业务状态；取值 active / disabled',
  `start_at` datetime(3) NOT NULL COMMENT '有效期开始时间',
  `end_at` datetime(3) NOT NULL COMMENT '有效期结束时间',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  `deleted_by` bigint unsigned DEFAULT NULL COMMENT '删除人 ID',
  PRIMARY KEY (`id`),
  KEY `idx_activity_rule_active` (`trigger_type`,`status`,`start_at`,`end_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='优惠券活动规则及版本';

-- coupon_grant：优惠券发放记录
CREATE TABLE `coupon_grant` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `coupon_id` bigint unsigned NOT NULL COMMENT '优惠券模板 ID',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `grant_source` enum('register','activity','invite','manual','invite_reward') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '优惠券发放来源；取值 register / activity / invite / manual / invite_reward',
  `status` enum('unused','used','expired') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'unused' COMMENT '当前业务状态；取值 unused / used / expired',
  `used_payment_order_id` bigint unsigned DEFAULT NULL COMMENT '使用该优惠券的支付订单 ID',
  `used_at` datetime(3) DEFAULT NULL COMMENT '优惠券使用时间',
  `expired_at` datetime(3) NOT NULL COMMENT '失效时间',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  `source_event_id` char(36) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL COMMENT '来源事件 ID',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_coupon_grant_source_event` (`source_event_id`),
  KEY `idx_user_status` (`user_id`,`status`),
  KEY `idx_coupon` (`coupon_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='优惠券发放记录';

-- coupon_grant_request：优惠券发放请求幂等回执
CREATE TABLE `coupon_grant_request` (
  `request_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '幂等请求 ID',
  `coupon_id` bigint unsigned NOT NULL COMMENT '优惠券模板 ID',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `coupon_grant_id` bigint unsigned NOT NULL COMMENT '已发放优惠券 ID',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`request_id`),
  UNIQUE KEY `uk_coupon_grant_request_grant` (`coupon_grant_id`),
  KEY `idx_coupon_grant_request_coupon_user` (`coupon_id`,`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='优惠券发放请求幂等回执';

-- coupon_redemption：优惠券核销记录
CREATE TABLE `coupon_redemption` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `coupon_id` bigint unsigned DEFAULT NULL COMMENT '优惠券模板 ID',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `biz_type` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '关联业务类型',
  `biz_id` bigint unsigned NOT NULL COMMENT '关联业务 ID',
  `discount_cents` bigint NOT NULL COMMENT '订单优惠抵扣金额，单位分',
  `order_no` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '业务订单编号',
  `redeemed_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '优惠券核销时间',
  PRIMARY KEY (`id`),
  KEY `idx_biz` (`biz_type`,`biz_id`),
  KEY `idx_redemption_coupon` (`coupon_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='优惠券核销记录';

-- device_fault_report：设备报修
CREATE TABLE `device_fault_report` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `user_id` bigint unsigned DEFAULT NULL COMMENT '充电用户 ID',
  `report_source` enum('user','inspect','monitor') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'user' COMMENT '设备报修来源；取值 user / inspect / monitor',
  `fault_type` enum('mechanical','electrical','communication','display','other') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '报修故障分类；取值 mechanical / electrical / communication / display / other',
  `description` text COLLATE utf8mb4_unicode_ci COMMENT '业务说明',
  `images_json` json DEFAULT NULL COMMENT '报修或反馈附件图片地址列表 JSON',
  `status` enum('open','dispatched','fixed','closed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'open' COMMENT '当前业务状态；取值 open / dispatched / fixed / closed',
  `assigned_to` bigint unsigned DEFAULT NULL COMMENT '指派处理人的管理员 ID',
  `resolved_at` datetime(3) DEFAULT NULL COMMENT '异常恢复或处理完成时间',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  PRIMARY KEY (`id`),
  KEY `idx_device_status` (`device_id`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备报修';

-- device_fault_report_event：设备报修状态与巡检处理记录
CREATE TABLE `device_fault_report_event` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `report_id` bigint unsigned NOT NULL COMMENT '设备报修 ID',
  `actor_id` bigint unsigned DEFAULT NULL COMMENT '操作人 ID',
  `event_type` enum('reported','dispatched','reassigned','fixed','closed','migration_baseline') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '事件类型；取值 reported / dispatched / reassigned / fixed / closed / migration_baseline',
  `from_status` varchar(24) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '流转前状态',
  `to_status` varchar(24) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '流转后状态',
  `assigned_to` bigint unsigned DEFAULT NULL COMMENT '指派处理人的管理员 ID',
  `note` varchar(2000) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '业务备注或处理说明',
  `user_visible` tinyint(1) NOT NULL DEFAULT '1' COMMENT '流转记录是否对报修用户可见，0 否、1 是',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`id`),
  KEY `idx_fault_event_report_time` (`report_id`,`created_at`,`id`),
  KEY `idx_fault_event_actor_time` (`actor_id`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备报修状态与巡检处理记录';

-- event_outbox：事件 outbox(可靠发布)
CREATE TABLE `event_outbox` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `event_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '事件唯一标识，用于追踪和幂等处理',
  `stream` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '目标 stream 名',
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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='事件 outbox(可靠发布)';

-- feedback：评价/投诉
CREATE TABLE `feedback` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `order_id` bigint unsigned DEFAULT NULL COMMENT '充电订单 ID',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '设备全局唯一编号',
  `rating` tinyint unsigned DEFAULT NULL COMMENT '1-5 星',
  `category` enum('rating','complaint','suggestion') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '反馈分类；取值 rating / complaint / suggestion',
  `content` text COLLATE utf8mb4_unicode_ci COMMENT '正文内容',
  `images_json` json DEFAULT NULL COMMENT '报修或反馈附件图片地址列表 JSON',
  `status` enum('pending','processed','closed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '当前业务状态；取值 pending / processed / closed',
  `replied_by` bigint unsigned DEFAULT NULL COMMENT '回复人 ID',
  `replied_at` datetime(3) DEFAULT NULL COMMENT '用户反馈回复时间',
  `reply_content` text COLLATE utf8mb4_unicode_ci COMMENT '后台对用户反馈的回复内容',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  PRIMARY KEY (`id`),
  KEY `idx_user_created` (`user_id`,`created_at`),
  KEY `idx_feedback_order` (`order_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='评价/投诉';

-- invoice_admin_review：发票审核记录
CREATE TABLE `invoice_admin_review` (
  `invoice_request_id` bigint unsigned NOT NULL COMMENT '发票申请 ID',
  `review_status` enum('awaiting_second','approved','rejected') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '审核状态；取值 awaiting_second / approved / rejected',
  `first_reviewer_id` bigint unsigned DEFAULT NULL COMMENT '一级审核管理员 ID',
  `second_reviewer_id` bigint unsigned DEFAULT NULL COMMENT '二级审核管理员 ID',
  `invoice_url` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '发票文件访问 URL',
  `reject_reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '拒绝申请的原因',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  PRIMARY KEY (`invoice_request_id`),
  CONSTRAINT `invoice_admin_review_chk_1` CHECK (((`second_reviewer_id` is null) or (`second_reviewer_id` <> `first_reviewer_id`)))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='发票审核记录';

-- invoice_request：发票申请
CREATE TABLE `invoice_request` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `invoice_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '已开具的发票号码',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `biz_type` enum('charge','wallet_recharge') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '关联业务类型；取值 charge / wallet_recharge',
  `biz_id` bigint unsigned NOT NULL COMMENT '关联业务 ID',
  `total_cents` bigint NOT NULL COMMENT '总金额，单位分',
  `invoice_type` enum('normal','vat_special') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'normal' COMMENT '申请发票类型；取值 normal / vat_special',
  `title` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '发票抬头',
  `tax_no` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '发票抬头的纳税人识别号',
  `email` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '联系邮箱',
  `review_status` enum('pending','approved','rejected','issued') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '审核状态；取值 pending / approved / rejected / issued',
  `reviewed_by` bigint unsigned DEFAULT NULL COMMENT '审核人 ID',
  `reviewed_at` datetime(3) DEFAULT NULL COMMENT '审核完成时间',
  `reject_reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '拒绝申请的原因',
  `invoice_url` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '发票文件访问 URL',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_invoice_no` (`invoice_no`),
  KEY `idx_user_created` (`user_id`,`created_at`),
  KEY `idx_review` (`review_status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='发票申请';

-- manual_refund_request：人工退款申请及审核
CREATE TABLE `manual_refund_request` (
  `request_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '幂等请求 ID',
  `payload_json` json NOT NULL COMMENT '任务执行或投递载荷 JSON',
  `refund_no` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '退款业务编号',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`request_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='人工退款申请及审核';


-- online_card：用户在线卡绑定和挂失状态
CREATE TABLE `online_card` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `card_no` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '在线卡卡号',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `status` enum('active','lost','disabled','unbound') NOT NULL DEFAULT 'active' COMMENT '当前业务状态；取值 active / lost / disabled / unbound',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `card_no` (`card_no`),
  KEY `idx_user` (`user_id`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='用户在线卡绑定和挂失状态';

-- online_card_audit：在线卡操作审计
CREATE TABLE `online_card_audit` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `card_id` bigint unsigned NOT NULL COMMENT '在线卡 ID',
  `actor_id` bigint unsigned NOT NULL COMMENT '操作人 ID',
  `action` varchar(32) NOT NULL COMMENT '操作动作',
  `detail` varchar(1000) NOT NULL COMMENT '事件详细说明',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='在线卡操作审计';

-- payment_callback_idempotent：微信支付回调幂等(30 天保留)
CREATE TABLE `payment_callback_idempotent` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `wechat_transaction_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '微信支付渠道交易编号',
  `processed_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '事件处理完成时间',
  `request_digest` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '支付回调请求内容摘要',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_txn` (`wechat_transaction_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='微信支付回调幂等(30 天保留)';

-- payment_order：支付订单(支持 charge / wallet_recharge)
CREATE TABLE `payment_order` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `order_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'P+独立Snowflake支付单号；历史编号保留',
  `biz_type` enum('charge','wallet_recharge') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '关联业务类型；取值 charge / wallet_recharge',
  `biz_id` bigint unsigned NOT NULL COMMENT '关联 charge_order.id 或 wallet_txn.id',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `pay_method` enum('wechat','balance','mixed','coupon') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '订单支付方式；取值 wechat / balance / mixed / coupon',
  `total_cents` bigint NOT NULL COMMENT '订单总金额(分)，单位分',
  `paid_cents` bigint NOT NULL DEFAULT '0' COMMENT '已支付金额，单位分',
  `refunded_cents` bigint NOT NULL DEFAULT '0' COMMENT '累计已退款金额，单位分',
  `wechat_transaction_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '微信侧 transaction_id',
  `status` enum('initiated','paid','closed','refunded','partial_refunded','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'initiated' COMMENT '当前业务状态；取值 initiated / paid / closed / refunded / partial_refunded / failed',
  `paid_at` datetime(3) DEFAULT NULL COMMENT '支付完成时间',
  `closed_at` datetime(3) DEFAULT NULL COMMENT '订单关闭时间',
  `expired_at` datetime(3) DEFAULT NULL COMMENT '失效时间',
  `failure_reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '业务执行失败原因',
  `client_ip` varchar(45) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '请求客户端 IP 地址',
  `created_month` date NOT NULL COMMENT '月分区归属日期，取对应月份第一天',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  `deleted_by` bigint unsigned DEFAULT NULL COMMENT '删除人 ID',
  `discount_cents` bigint NOT NULL DEFAULT '0' COMMENT '订单优惠抵扣金额，单位分',
  `coupon_grant_id` bigint unsigned DEFAULT NULL COMMENT '已发放优惠券 ID',
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




-- refund_record：退款记录
CREATE TABLE `refund_record` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `refund_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '退款业务编号',
  `payment_order_id` bigint unsigned NOT NULL COMMENT '支付订单 ID',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `biz_type` enum('charge','wallet_recharge') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '关联业务类型；取值 charge / wallet_recharge',
  `biz_id` bigint unsigned NOT NULL COMMENT '退款关联的原支付业务 ID',
  `refund_cents` bigint NOT NULL COMMENT '本次退款金额，单位分',
  `reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '业务处理或异常原因',
  `status` enum('pending','processing','success','failed','rejected') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '当前业务状态；取值 pending / processing / success / failed / rejected',
  `retry_count` int unsigned NOT NULL DEFAULT '0' COMMENT '已执行重试次数',
  `wechat_refund_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '微信支付渠道退款编号',
  `claimed_by` bigint unsigned DEFAULT NULL COMMENT '退款任务领取执行者标识',
  `claimed_at` datetime(3) DEFAULT NULL COMMENT '退款任务领取时间',
  `completed_at` datetime(3) DEFAULT NULL COMMENT '任务完成时间',
  `failure_reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '业务执行失败原因',
  `created_month` date NOT NULL COMMENT '月分区归属日期，取对应月份第一天',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  `deleted_by` bigint unsigned DEFAULT NULL COMMENT '删除人 ID',
  `execution_policy` enum('manual_review','automatic') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'manual_review' COMMENT '退款渠道执行策略；取值 manual_review / automatic',
  `next_attempt_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '下次执行尝试时间',
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
  `refund_record_id` bigint unsigned NOT NULL COMMENT '退款记录 ID',
  `actor_id` bigint unsigned NOT NULL COMMENT '操作人 ID',
  `reason` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务处理或异常原因',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`refund_record_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='退款拒绝原因记录';

-- refund_review：退款审核过程
CREATE TABLE `refund_review` (
  `refund_record_id` bigint unsigned NOT NULL COMMENT '退款记录 ID',
  `snapshot_json` json NOT NULL COMMENT '业务审核或规则配置快照 JSON',
  `first_signer` bigint unsigned NOT NULL COMMENT '一级退款审核管理员 ID',
  `first_comment` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '一级退款审核意见',
  `second_signer` bigint unsigned DEFAULT NULL COMMENT '二级退款审核管理员 ID',
  `second_comment` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '二级退款审核意见',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `approved_at` datetime(3) DEFAULT NULL COMMENT '审核通过时间',
  PRIMARY KEY (`refund_record_id`),
  CONSTRAINT `refund_review_chk_1` CHECK (((`second_signer` is null) or (`second_signer` <> `first_signer`)))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='退款审核过程';

-- refund_success_receipt：退款成功的幂等确认
CREATE TABLE `refund_success_receipt` (
  `refund_record_id` bigint unsigned NOT NULL COMMENT '退款记录 ID',
  `wechat_refund_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '微信支付渠道退款编号',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`refund_record_id`),
  UNIQUE KEY `wechat_refund_id` (`wechat_refund_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='退款成功的幂等确认';

-- risk_freeze_log：风控冻结记录(本期仅频次触发)
CREATE TABLE `risk_freeze_log` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `trigger_rule` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '触发冻结的风控规则名称',
  `frozen_action` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '风控冻结限制的业务操作',
  `reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '业务处理或异常原因',
  `window_minutes` int unsigned DEFAULT NULL COMMENT '风控统计窗口长度，单位分钟',
  `threshold_value` int unsigned DEFAULT NULL COMMENT '触发风控的阈值',
  `actual_value` int unsigned DEFAULT NULL COMMENT '触发风控时实际观测值',
  `unfreeze_at` datetime(3) DEFAULT NULL COMMENT '冻结解除时间',
  `status` enum('frozen','unfrozen') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'frozen' COMMENT '当前业务状态；取值 frozen / unfrozen',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`id`),
  KEY `idx_user_status` (`user_id`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='风控冻结记录(本期仅频次触发)';

-- snowflake_state：用户和支付编号共享的持久化 Snowflake 分配状态
CREATE TABLE `snowflake_state` (
  `id` tinyint unsigned NOT NULL COMMENT '分配器标识；当前仅共享节点 0，固定为 1',
  `last_millisecond` bigint unsigned NOT NULL DEFAULT '0' COMMENT '相对 2020-01-01 UTC 的最后逻辑毫秒；防止回拨或重启重复分配',
  `sequence` smallint unsigned NOT NULL DEFAULT '0' COMMENT '同一逻辑毫秒内的 12 位序列，取值 0–4095',
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Snowflake 编号分配状态，业务事务内行锁串行分配';
INSERT INTO `snowflake_state` (`id`,`last_millisecond`,`sequence`) VALUES (1,0,0);

-- user：终端用户
CREATE TABLE `user` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT 'Snowflake 用户 ID，由业务显式分配；兼容历史自增 ID',
  `openid` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '微信 openid',
  `unionid` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '微信开放平台用户统一标识',
  `nickname` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '用户昵称',
  `avatar_url` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '用户头像 URL',
  `phone` varchar(11) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '明文中国大陆手机号；未绑定为 NULL',
  `gender` enum('unknown','male','female') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'unknown' COMMENT '用户性别；unknown 未知、male 男、female 女',
  `status` enum('active','frozen') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active' COMMENT '当前业务状态；取值 active / frozen',
  `first_seen_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '首次识别用户时间',
  `last_login_at` datetime(3) DEFAULT NULL COMMENT '最近登录时间',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  `deleted_by` bigint unsigned DEFAULT NULL COMMENT '删除人 ID',
  `inviter_id` bigint unsigned DEFAULT NULL COMMENT '介绍人 user.id，绑定后不可自行更改',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_openid` (`openid`,`deleted_at`),
  UNIQUE KEY `uk_phone` (`phone`),
  KEY `idx_status` (`status`),
  KEY `idx_user_inviter` (`inviter_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='终端用户';

-- user_login_identity：用户登录身份关联
CREATE TABLE `user_login_identity` (
  `openid` varbinary(64) NOT NULL COMMENT '微信用户在当前应用下的唯一标识',
  PRIMARY KEY (`openid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='用户登录身份关联';

-- wallet_account：钱包账户(1:1 with user)
CREATE TABLE `wallet_account` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `balance_cents` bigint NOT NULL DEFAULT '0' COMMENT '余额(分)，单位分',
  `frozen_cents` bigint NOT NULL DEFAULT '0' COMMENT '冻结金额(提现中)，单位分',
  `status` enum('active','frozen') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active' COMMENT '当前业务状态；取值 active / frozen',
  `version` bigint unsigned NOT NULL DEFAULT '0' COMMENT '乐观锁',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  `deleted_by` bigint unsigned DEFAULT NULL COMMENT '删除人 ID',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user` (`user_id`,`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='钱包账户(1:1 with user)';

-- wallet_recharge_request：钱包充值请求幂等键
CREATE TABLE `wallet_recharge_request` (
  `request_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '幂等请求 ID',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `amount_cents` bigint NOT NULL COMMENT '金额，单位分',
  `payment_order_id` bigint unsigned DEFAULT NULL COMMENT '支付订单 ID',
  `request_json` json DEFAULT NULL COMMENT '业务请求参数快照 JSON',
  `prepay_id` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '支付渠道预支付会话标识',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`request_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='钱包充值请求幂等键';

-- wallet_refund_part：钱包退款按原支付渠道拆分明细
CREATE TABLE `wallet_refund_part` (
  `refund_record_id` bigint unsigned NOT NULL COMMENT '退款记录 ID',
  `request_id` varchar(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '幂等请求 ID',
  `wallet_account_id` bigint unsigned NOT NULL COMMENT '钱包账户 ID',
  `amount_cents` bigint NOT NULL COMMENT '金额，单位分',
  `settled` tinyint(1) NOT NULL DEFAULT '0' COMMENT '退款分摊是否已结算，0 否、1 是',
  PRIMARY KEY (`refund_record_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='钱包退款按原支付渠道拆分明细';

-- wallet_refund_request：钱包退款请求及处理结果
CREATE TABLE `wallet_refund_request` (
  `request_id` varchar(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '幂等请求 ID',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `amount_cents` bigint NOT NULL COMMENT '金额，单位分',
  `reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '业务处理或异常原因',
  `response_json` json DEFAULT NULL COMMENT '业务处理响应快照 JSON',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`request_id`),
  KEY `idx_user_time` (`user_id`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='钱包退款请求及处理结果';

-- wallet_risk_freeze_link：钱包风控冻结关联
CREATE TABLE `wallet_risk_freeze_link` (
  `request_id` varchar(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '幂等请求 ID',
  `freeze_id` bigint unsigned NOT NULL COMMENT '风控冻结记录 ID',
  PRIMARY KEY (`request_id`),
  UNIQUE KEY `freeze_id` (`freeze_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='钱包风控冻结关联';

-- wallet_risk_release：钱包风控解除记录
CREATE TABLE `wallet_risk_release` (
  `request_id` varchar(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '幂等请求 ID',
  `actor_id` bigint unsigned NOT NULL COMMENT '操作人 ID',
  `comment` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '操作或审核意见',
  `response_json` json NOT NULL COMMENT '业务处理响应快照 JSON',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`request_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='钱包风控解除记录';

-- wallet_risk_review：钱包风控审核记录
CREATE TABLE `wallet_risk_review` (
  `request_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '幂等请求 ID',
  `actor_id` bigint unsigned NOT NULL COMMENT '操作人 ID',
  `approved` tinyint(1) NOT NULL COMMENT '是否审核通过，0 否、1 是',
  `comment` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '操作或审核意见',
  `response_json` json NOT NULL COMMENT '业务处理响应快照 JSON',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`request_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='钱包风控审核记录';

-- wallet_txn：钱包流水
CREATE TABLE `wallet_txn` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `txn_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '钱包资金流水编号',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `wallet_account_id` bigint unsigned NOT NULL COMMENT '钱包账户 ID',
  `direction` enum('in','out') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '资金流向，in 入账、out 出账；取值 in / out',
  `amount_cents` bigint NOT NULL COMMENT '金额，单位分',
  `balance_after_cents` bigint NOT NULL COMMENT '交易后的钱包余额，单位分',
  `biz_type` enum('recharge','pay','refund','gift','freeze','unfreeze','adjust') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '关联业务类型；取值 recharge / pay / refund / gift / freeze / unfreeze / adjust',
  `biz_ref` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '关联业务单号',
  `note` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '业务备注或处理说明',
  `created_month` date NOT NULL COMMENT '月分区归属日期，取对应月份第一天',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
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

-- admin_db 领域表
-- admin_data_scope：后台账号数据范围
CREATE TABLE `admin_data_scope` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `admin_user_id` bigint unsigned NOT NULL COMMENT '管理员 ID',
  `scope_type` enum('station','vendor') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '后台数据权限范围类型；取值 station / vendor',
  `scope_id` bigint unsigned NOT NULL COMMENT '数据权限范围对象 ID',
  `created_by` bigint unsigned NOT NULL COMMENT '创建人 ID',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user_scope` (`admin_user_id`,`scope_type`,`scope_id`),
  KEY `idx_user` (`admin_user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='后台账号数据范围';

-- admin_field_mask：角色字段脱敏规则
CREATE TABLE `admin_field_mask` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `role_id` bigint unsigned NOT NULL COMMENT '管理员角色 ID',
  `resource` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '目标业务资源标识',
  `field` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '需要脱敏的字段名',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_role_resource_field` (`role_id`,`resource`,`field`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='角色字段脱敏规则';

-- admin_user：管理员账号（一账号一角色，role_id 内联）
CREATE TABLE `admin_user` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `username` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '管理员登录用户名',
  `display_name` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '管理员显示名称',
  `password_hash` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '管理员密码的单向哈希值',
  `phone` varchar(32) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '管理员联系电话',
  `email` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '联系邮箱',
  `role_id` bigint unsigned DEFAULT NULL COMMENT '管理员角色 ID',
  `mfa_secret` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT 'TOTP 密钥(base32，启用前为待确认状态)',
  `mfa_enabled` tinyint(1) NOT NULL DEFAULT '0' COMMENT '是否启用多因素认证，0 否、1 是',
  `status` enum('active','disabled','locked') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active' COMMENT '当前业务状态；取值 active / disabled / locked',
  `last_login_at` datetime(3) DEFAULT NULL COMMENT '最近登录时间',
  `failed_login_count` int unsigned NOT NULL DEFAULT '0' COMMENT '连续登录失败次数',
  `locked_until` datetime(3) DEFAULT NULL COMMENT '登录锁定到期时间',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  `deleted_by` bigint unsigned DEFAULT NULL COMMENT '删除人 ID',
  `active_username` varchar(64) COLLATE utf8mb4_unicode_ci GENERATED ALWAYS AS (if((`deleted_at` is null),`username`,NULL)) STORED COMMENT '未删除管理员的用户名，用于约束有效用户名唯一',
  `auth_version` bigint unsigned NOT NULL DEFAULT '0' COMMENT '认证版本号，递增后旧登录凭证失效',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_username` (`username`,`deleted_at`),
  UNIQUE KEY `uk_active_username` (`active_username`),
  KEY `idx_mfa_state` (`mfa_enabled`,`mfa_secret`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='管理员账号';

-- alert_event：告警事件
CREATE TABLE `alert_event` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `rule_id` bigint unsigned DEFAULT NULL COMMENT '历史阈值规则 ID；当前设备上报告警为 NULL',
  `severity` enum('warning','critical','fatal') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'warning' COMMENT '告警严重程度；取值 warning / critical / fatal',
  `metric` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '告警指标；smoke 烟雾、high_temperature 高温、device_fault 设备故障、port_fault_N 端口故障，或遥测指标',
  `value` decimal(18,6) DEFAULT NULL COMMENT '告警观测值；设备故障时为协议故障码',
  `threshold` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '历史阈值规则触发阈值；当前设备上报告警为 NULL',
  `status` enum('active','acknowledged','resolved','auto_resolved') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active' COMMENT '当前业务状态；取值 active / acknowledged / resolved / auto_resolved',
  `acked_by` bigint unsigned DEFAULT NULL COMMENT '确认人 ID',
  `acked_at` datetime(3) DEFAULT NULL COMMENT '确认时间',
  `resolved_at` datetime(3) DEFAULT NULL COMMENT '异常恢复或处理完成时间',
  `note` text COLLATE utf8mb4_unicode_ci COMMENT '业务备注或处理说明',
  `event_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '告警来源事件标识；设备告警对应最近一次故障报告的事件键',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `created_month` date NOT NULL COMMENT '月分区归属日期，取对应月份第一天',
  PRIMARY KEY (`id`,`created_month`),
  KEY `idx_device_status` (`device_id`,`status`),
  KEY `idx_event` (`event_id`,`created_month`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='告警事件'
/*!50100 PARTITION BY RANGE (to_days(`created_month`))
(PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB,
 PARTITION p_2026m10 VALUES LESS THAN (740286) ENGINE = InnoDB,
 PARTITION p_2026m11 VALUES LESS THAN (740316) ENGINE = InnoDB,
 PARTITION p_2026m12 VALUES LESS THAN (740347) ENGINE = InnoDB,
 PARTITION p_max VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */;

-- announcement：公告
CREATE TABLE `announcement` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `title` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务标题',
  `title_i18n` json DEFAULT NULL COMMENT '按语言保存的标题文案 JSON',
  `content` text COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '正文内容',
  `content_i18n` json DEFAULT NULL COMMENT '按语言保存的正文文案 JSON',
  `scope` enum('global','station','city') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'global' COMMENT '公告的可见范围；取值 global / station / city',
  `target_ids` json DEFAULT NULL COMMENT '公告目标对象 ID 列表 JSON',
  `priority` tinyint unsigned NOT NULL DEFAULT '0' COMMENT '显示或执行优先级',
  `start_at` datetime(3) NOT NULL COMMENT '有效期开始时间',
  `end_at` datetime(3) DEFAULT NULL COMMENT '有效期结束时间',
  `status` enum('draft','published','expired') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'draft' COMMENT '当前业务状态；取值 draft / published / expired',
  `created_by` bigint unsigned NOT NULL COMMENT '创建人 ID',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  PRIMARY KEY (`id`),
  KEY `idx_status_window` (`status`,`start_at`,`end_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='公告';

-- audit_log：审计日志
CREATE TABLE `audit_log` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `actor_id` bigint unsigned NOT NULL COMMENT '操作人 ID',
  `actor_name` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '操作人名称快照',
  `module` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '功能模块标识',
  `action` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '操作动作',
  `target_type` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '审计操作的目标资源类型',
  `target_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '审计目标对象 ID',
  `request_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '幂等请求 ID',
  `before_json` json DEFAULT NULL COMMENT '操作前的业务数据快照 JSON',
  `after_json` json DEFAULT NULL COMMENT '操作后的业务数据快照 JSON',
  `client_ip` varchar(45) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '请求客户端 IP 地址',
  `created_month` date NOT NULL COMMENT '月分区归属日期，取对应月份第一天',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
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

-- charge_offer：站点或设备的在售充电套餐
-- device_id 为 NULL 表示站点默认；设备独立方案优先，完整方案含各模式与套餐。


-- device_import：设备导入请求及执行重试状态
CREATE TABLE `device_import` (
  `import_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备导入请求 ID',
  `actor_id` bigint unsigned NOT NULL COMMENT '操作人 ID',
  `request_json` json NOT NULL COMMENT '业务请求参数快照 JSON',
  `status` enum('pending','completed','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '当前业务状态；取值 pending / completed / failed',
  `last_error` varchar(1024) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '最近一次执行错误信息',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `attempts` int unsigned NOT NULL DEFAULT '0' COMMENT '已尝试执行次数',
  `retryable` tinyint(1) NOT NULL DEFAULT '1' COMMENT '当前失败是否允许自动重试，0 否、1 是',
  `next_attempt_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '下次执行尝试时间',
  PRIMARY KEY (`import_id`),
  KEY `idx_retry` (`retryable`,`next_attempt_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备导入请求及执行重试状态';

-- device_import_identity：设备导入时的请求身份和参数校验快照
CREATE TABLE `device_import_identity` (
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `request_json` json NOT NULL COMMENT '业务请求参数快照 JSON',
  PRIMARY KEY (`device_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备导入时的请求身份和参数校验快照';

-- device_meta：设备元数据(冗余自 gateway_db)
-- protocol_adapter 在创建/导入时由服务器按厂商协议填写；设备能力由协议决定。
-- status 为后台运营状态，独立于 TCP 在线状态；禁用后不接受新启动/加时。
CREATE TABLE `device_meta` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `station_id` bigint unsigned DEFAULT NULL COMMENT '充电站点 ID',
  `vendor_id` bigint unsigned DEFAULT NULL COMMENT '设备厂商 ID',
  `model` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '设备型号',
  `protocol_adapter` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '创建设备时选定的通信协议',
  `charge_mode` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'device_duration' COMMENT '设备计费方式标识，如 device_duration、device_energy、server_realtime_power',
  `serial_no` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '设备出厂序列号',
  `warranty_until` datetime(3) DEFAULT NULL COMMENT '设备保修截止日期',
  `status` enum('enabled','disabled','retired','fault') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'enabled' COMMENT '当前业务状态；取值 enabled / disabled / retired / fault',
  `tags_json` json DEFAULT NULL COMMENT '设备标签列表 JSON',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_device_id` (`device_id`,`deleted_at`),
  KEY `idx_station` (`station_id`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备元数据(冗余自 gateway_db)';

-- event_outbox：事件 outbox(可靠发布)
CREATE TABLE `admin_event_outbox` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `event_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '事件唯一标识，用于追踪和幂等处理',
  `stream` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '目标 stream 名',
  `envelope_json` json NOT NULL COMMENT '待发布事件的统一信封 JSON',
  `status` enum('pending','published','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '当前业务状态；取值 pending / published / failed',
  `stream_message_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT 'XADD 返回的 stream id,用于缺口反查',
  `retry_count` int unsigned NOT NULL DEFAULT '0' COMMENT '已执行重试次数',
  `last_error` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '最近一次执行错误信息',
  `scheduled_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '计划执行时间',
  `published_at` datetime(3) DEFAULT NULL COMMENT '事件发布完成时间',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_event` (`event_id`),
  KEY `idx_status_sched` (`status`,`scheduled_at`),
  KEY `idx_stream_msgid` (`stream`,`stream_message_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='事件 outbox(可靠发布)';

-- export_task：导出任务
CREATE TABLE `export_task` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `task_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '后台任务编号',
  `resource` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '目标业务资源标识',
  `file_format` enum('csv','xlsx','pdf') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'csv' COMMENT '导出文件格式；取值 csv / xlsx / pdf',
  `status` enum('pending','running','completed','failed','expired') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '当前业务状态；取值 pending / running / completed / failed / expired',
  `requested_by` bigint unsigned NOT NULL COMMENT '任务发起管理员 ID',
  `filter_json` json DEFAULT NULL COMMENT '导出任务筛选条件 JSON',
  `row_count` int unsigned NOT NULL DEFAULT '0' COMMENT '导出的业务记录数量',
  `file_path` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '导出文件保存路径',
  `error_msg` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '执行错误信息',
  `expires_at` datetime(3) DEFAULT NULL COMMENT '到期时间',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `completed_at` datetime(3) DEFAULT NULL COMMENT '任务完成时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_task_no` (`task_no`),
  KEY `idx_status` (`status`,`expires_at`),
  KEY `idx_requester` (`requested_by`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='导出任务';

-- finance_reconcile_log：财务对账日志
CREATE TABLE `finance_reconcile_log` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `reconcile_type` enum('wechat_refund','wechat_pay','split','withdraw') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '对账业务类型；取值 wechat_refund / wechat_pay / split / withdraw',
  `reconcile_date` date NOT NULL COMMENT '对账业务日期',
  `internal_count` int unsigned NOT NULL COMMENT '内部账单记录数量',
  `wechat_count` int unsigned NOT NULL COMMENT '微信账单记录数量',
  `diff_count` int NOT NULL COMMENT '对账记录数量差额',
  `internal_cents` bigint NOT NULL COMMENT '内部账单汇总金额，单位分',
  `wechat_cents` bigint NOT NULL COMMENT '微信账单汇总金额，单位分',
  `diff_cents` bigint NOT NULL COMMENT '对账金额差额，单位分',
  `diffs_json` json DEFAULT NULL COMMENT '对账差异明细 JSON',
  `resolved` tinyint(1) NOT NULL DEFAULT '0' COMMENT '是否已处理对账差异，0 否、1 是',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_type_date` (`reconcile_type`,`reconcile_date`),
  KEY `idx_type_date` (`reconcile_type`,`reconcile_date`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='财务对账日志';


-- permission：权限码
-- 权限由后端路由守卫执行；角色授权以初始化种子为准。
CREATE TABLE `permission` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '如 orders.read / orders.refund.approve',
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务名称',
  `module` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'orders / devices / billing / ...',
  `description` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '业务说明',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_code` (`code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='权限码';


-- pricing_publication：计费规则发布版本和请求幂等摘要
CREATE TABLE `pricing_publication` (
  `request_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '幂等请求 ID',
  `actor_id` bigint unsigned NOT NULL COMMENT '操作人 ID',
  `payload_hash` char(64) CHARACTER SET ascii COLLATE ascii_general_ci NOT NULL COMMENT '请求或事件载荷摘要，用于检验幂等重试内容一致性',
  `rule_id` bigint unsigned NOT NULL COMMENT '已发布计费规则 ID',
  `version` int unsigned NOT NULL COMMENT '业务版本号',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`request_id`),
  UNIQUE KEY `uk_rule` (`rule_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='计费规则发布版本和请求幂等摘要';

-- pricing_rule：计费规则
-- device_id 为 NULL 表示站点默认；设备独立方案优先，完整方案含各模式与套餐。
CREATE TABLE `pricing_rule` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `template_id` bigint unsigned DEFAULT NULL COMMENT '计费模板 ID',
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务名称',
  `name_i18n` json DEFAULT NULL COMMENT '按语言保存的名称文案 JSON',
  `station_id` bigint unsigned DEFAULT NULL COMMENT '充电站点 ID',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '设备全局唯一编号',
  `version` int unsigned NOT NULL DEFAULT '1' COMMENT '业务版本号',
  `status` enum('active','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active' COMMENT '当前业务状态；取值 active / disabled',
  `effective_from` datetime(3) DEFAULT NULL COMMENT '计费规则生效时间',
  `effective_to` datetime(3) DEFAULT NULL COMMENT '计费规则失效时间',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  `spec_json` json NOT NULL COMMENT '完整计费口径，对应 pricing.Spec',
  `channel` enum('default','temp','card') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'default' COMMENT '计费通道，选择费率倍数',
  PRIMARY KEY (`id`),
  KEY `idx_station_status` (`station_id`,`status`),
  KEY `idx_template_station` (`template_id`,`station_id`,`status`),
  KEY `idx_device` (`device_id`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='计费规则';



-- pricing_template：计费模板
CREATE TABLE `pricing_template` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `name` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '模板名称',
  `remark` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '模板备注',
  `spec_json` json NOT NULL COMMENT '计费口径，对应 pricing.Spec',
  `display_json` json NOT NULL COMMENT '用户端展示开关，对应 pricing.Display',
  `status` enum('active','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active' COMMENT '当前业务状态；取值 active / disabled',
  `version` int unsigned NOT NULL DEFAULT '1' COMMENT '业务版本号',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  PRIMARY KEY (`id`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='计费模板';


-- regulatory_report：监管报送持久队列
CREATE TABLE `regulatory_report` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `event_id` char(36) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '事件唯一标识，用于追踪和幂等处理',
  `object_type` enum('operator','station','device','order','alert','battery') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '监管报送对象类型；取值 operator / station / device / order / alert / battery',
  `object_key` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '监管报送对象的业务标识',
  `payload_json` json NOT NULL COMMENT '任务执行或投递载荷 JSON',
  `status` enum('queued','processing','delivered') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'queued' COMMENT '当前业务状态；取值 queued / processing / delivered',
  `attempts` int unsigned NOT NULL DEFAULT '0' COMMENT '已尝试执行次数',
  `next_attempt_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '下次执行尝试时间',
  `lease_token` char(36) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '任务领取租约令牌，用于校验当前执行者',
  `lease_until` datetime(3) DEFAULT NULL COMMENT '任务租约到期时间',
  `last_error` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '最近一次执行错误信息',
  `delivered_at` datetime(3) DEFAULT NULL COMMENT '投递成功时间',
  `delivered_mode` enum('simulation','http') COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '监管报送实际使用的投递方式；取值 simulation / http',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_event` (`event_id`),
  KEY `idx_due` (`status`,`next_attempt_at`,`lease_until`),
  KEY `idx_object` (`object_type`,`object_key`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='监管报送持久队列';


-- role：角色
CREATE TABLE `role` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务唯一编码',
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务名称',
  `description` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '业务说明',
  `is_builtin` tinyint(1) NOT NULL DEFAULT '0' COMMENT '内置不可删',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  `active_code` varchar(64) COLLATE utf8mb4_unicode_ci GENERATED ALWAYS AS (if((`deleted_at` is null),`code`,NULL)) STORED COMMENT '未删除角色的编码，用于约束有效角色编码唯一',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_code` (`code`,`deleted_at`),
  UNIQUE KEY `uk_active_code` (`active_code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='角色';

-- role_permission：角色-权限映射
-- 权限由后端路由守卫执行；角色授权以初始化种子为准。
CREATE TABLE `role_permission` (
  `role_id` bigint unsigned NOT NULL COMMENT '管理员角色 ID',
  `permission_id` bigint unsigned NOT NULL COMMENT '权限 ID',
  PRIMARY KEY (`role_id`,`permission_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='角色-权限映射';


-- split_party：分账参与方
CREATE TABLE `split_party` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `split_template_id` bigint unsigned NOT NULL COMMENT '分账模板 ID',
  `party_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '分账参与方业务编码',
  `party_name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '分账参与方名称快照',
  `ratio_bp` int unsigned NOT NULL COMMENT '万分比(basis point,合计 10000)',
  `bank_account` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '收款银行账号',
  `bank_name` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '收款银行名称',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_split_party_code` (`split_template_id`,`party_code`),
  KEY `idx_template` (`split_template_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='分账参与方';

-- split_template：分账模板
CREATE TABLE `split_template` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务唯一编码',
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务名称',
  `name_i18n` json DEFAULT NULL COMMENT '按语言保存的名称文案 JSON',
  `mode` enum('mode_a','mode_b') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'mode_a' COMMENT 'A: 全分账;B: 仅服务费',
  `status` enum('active','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active' COMMENT '当前业务状态；取值 active / disabled',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_split_template_code` (`code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='分账模板';

-- station：充电站点
-- 充电站全年无休，不设置营业时间。
CREATE TABLE `station` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务名称',
  `address` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '站点地址',
  `longitude` decimal(11,8) NOT NULL COMMENT '经度，单位十进制度',
  `latitude` decimal(10,8) NOT NULL COMMENT '纬度，单位十进制度',
  `contact_phone` varchar(32) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '联系电话',
  `status` enum('active','disabled','construction') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active' COMMENT '当前业务状态；取值 active / disabled / construction',
  `pricing_template_id` bigint unsigned DEFAULT NULL COMMENT '计费模板 ID',
  `split_template_id` bigint unsigned DEFAULT NULL COMMENT '分账模板 ID',
  `config_json` json DEFAULT NULL COMMENT '业务配置 JSON',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  `deleted_by` bigint unsigned DEFAULT NULL COMMENT '删除人 ID',
  PRIMARY KEY (`id`),
  KEY `idx_geo` (`latitude`,`longitude`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='充电站点';



-- webhook_delivery_log：Webhook 投递日志
CREATE TABLE `webhook_delivery_log` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `subscription_id` bigint unsigned NOT NULL COMMENT 'Webhook 订阅 ID',
  `event_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '事件唯一标识，用于追踪和幂等处理',
  `event_type` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '事件类型',
  `request_body` json NOT NULL COMMENT 'Webhook 请求正文',
  `response_status` int DEFAULT NULL COMMENT 'Webhook 接收端 HTTP 状态码',
  `response_body` text COLLATE utf8mb4_unicode_ci COMMENT 'Webhook 接收端响应正文',
  `error_msg` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '执行错误信息',
  `attempt_count` int unsigned NOT NULL DEFAULT '1' COMMENT '已尝试执行次数',
  `duration_ms` int unsigned DEFAULT NULL COMMENT '请求执行耗时，单位毫秒',
  `delivered_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '投递成功时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_sub_event` (`subscription_id`,`event_id`),
  KEY `idx_sub_event` (`subscription_id`,`event_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Webhook 投递日志';

-- webhook_subscription：Webhook 订阅
CREATE TABLE `webhook_subscription` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务名称',
  `url` varchar(512) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'Webhook 接收端 URL',
  `secret` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'HMAC 密钥',
  `event_types` json NOT NULL COMMENT '订阅的事件类型列表',
  `headers_json` json DEFAULT NULL COMMENT 'Webhook 自定义请求头 JSON',
  `enabled` tinyint(1) NOT NULL DEFAULT '1' COMMENT '是否启用，0 否、1 是',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Webhook 订阅';

-- whitelabel_config：白标配置
CREATE TABLE `whitelabel_config` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务名称',
  `logo_url` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '白标品牌标志图片 URL',
  `mini_program_name` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '白标微信小程序名称',
  `mini_program_appid` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '白标微信小程序 AppID',
  `theme_color` varchar(16) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '白标主题颜色',
  `contact_phone` varchar(32) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '联系电话',
  `about_text` text COLLATE utf8mb4_unicode_ci COMMENT '关于页面的介绍文案',
  `config_json` json DEFAULT NULL COMMENT '业务配置 JSON',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='白标配置';

-- 初始化数据：内置权限、角色及系统默认配置。业务与演示数据另行创建。

-- permission 默认记录
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (1,'device.operate','编辑资料及启停设备','device','编辑设备型号、序列号、保修截止时间和标签，或切换运营状态；不停止已有订单','2026-09-30 19:22:11.260');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (2,'station.read','查看站点','station',NULL,'2026-09-30 19:22:11.651');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (3,'station.create','新增站点','station',NULL,'2026-09-30 19:22:11.651');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (4,'station.update','编辑站点','station',NULL,'2026-09-30 19:22:11.651');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (5,'station.delete','删除站点','station',NULL,'2026-09-30 19:22:11.651');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (6,'device.read','查看设备','device',NULL,'2026-09-30 19:22:11.655');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (7,'finance.refund.read','查看退款记录','finance',NULL,'2026-09-30 19:22:11.672');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (8,'finance.refund.retry','重试异常退款任务','finance',NULL,'2026-09-30 19:22:11.676');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (9,'order.refund.review','双签审核退款','refund',NULL,'2026-09-30 19:22:11.680');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (10,'order.refund.create','发起人工退款申请','refund',NULL,'2026-09-30 19:22:11.683');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (11,'finance.wallet_risk.review','钱包退款风控审核','refund',NULL,'2026-09-30 19:22:11.688');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (12,'finance.wallet_risk.release','解除钱包退款风控冻结','finance',NULL,'2026-09-30 19:22:11.692');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (13,'invoice.review','审核发票','finance',NULL,'2026-09-30 19:22:11.713');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (14,'feedback.read','查看用户评价与投诉','feedback','查看用户提交的评价、投诉和建议','2026-09-30 19:22:11.718');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (15,'feedback.reply','回复与关闭用户反馈','feedback','回复或关闭用户反馈','2026-09-30 19:22:11.718');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (16,'fault.read','查看设备报修','fault','查看用户和巡检提交的设备报修','2026-09-30 19:22:11.718');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (17,'fault.dispatch','派单与处理设备报修','fault','指派巡检人员并更新报修处理状态','2026-09-30 19:22:11.718');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (18,'whitelabel.read','查看白标配置','settings','查看租户品牌和联系信息','2026-09-30 19:22:11.724');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (19,'whitelabel.update','更新白标配置','settings','更新租户品牌、服务入口和展示信息','2026-09-30 19:22:11.724');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (20,'dashboard.read','查看运营仪表盘','dashboard','查看充电订单、结算金额与待处理告警汇总','2026-09-30 19:22:11.729');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (21,'coupon.read','查看优惠券','coupon','查询优惠券模板和发放统计','2026-09-30 19:22:11.763');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (22,'coupon.create','创建优惠券','coupon','创建优惠券模板','2026-09-30 19:22:11.763');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (23,'coupon.update','编辑优惠券','coupon','修改优惠券名称、状态和结束时间','2026-09-30 19:22:11.763');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (24,'coupon.delete','删除优惠券','coupon','软删除优惠券模板','2026-09-30 19:22:11.763');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (25,'coupon.grant','发放优惠券','coupon','向指定用户发放优惠券','2026-09-30 19:22:11.763');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (26,'admin_user.create','创建后台账号','admin_user','创建后台管理员账号','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (27,'admin_user.update','编辑后台账号','admin_user','修改账号资料、角色归属与状态','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (28,'admin_user.delete','删除后台账号','admin_user','软删除后台管理员账号','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (29,'admin_user.reset_password','重置后台账号密码','admin_user','重置指定后台账号的登录密码','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (30,'role.create','创建角色','role','创建角色','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (31,'role.update','编辑角色权限','role','修改角色信息与权限集合','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (32,'role.delete','删除角色','role','软删除角色','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (33,'pricing.rule.create','创建计费规则','pricing','创建计费规则(直接改变计费输入)','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (35,'finance.split_template.create','创建分账模板','finance','创建分账模板','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (36,'finance.split_party.create','维护分账参与方','finance','向分账模板增删参与方、比例与收款信息','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (37,'finance.withdraw.create','发起提现申请','finance','创建提现申请','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (38,'finance.withdraw.review','审核提现申请','finance','审批或驳回提现申请','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (39,'alert.ack','确认告警','alert','确认/忽略告警事件','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (51,'announcement.create','创建公告','announcement','创建公告','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (52,'announcement.update','编辑公告','announcement','修改公告','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (53,'announcement.delete','删除公告','announcement','软删除公告','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (57,'fault.resolve','处理设备故障','fault','标记设备故障已处理','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (58,'webhook.create','创建 Webhook 订阅','webhook','创建 Webhook 订阅','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (59,'webhook.update','编辑 Webhook 订阅','webhook','修改 Webhook 订阅','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (60,'webhook.delete','删除 Webhook 订阅','webhook','软删除 Webhook 订阅','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (61,'export.create','创建导出任务','export','创建数据导出任务','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (62,'order.read','查看充电订单','order',NULL,'2026-09-30 19:22:11.886');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (63,'admin_user.read','查看管理员','admin_user',NULL,'2026-09-30 19:22:11.892');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (64,'alert.read','查看告警','alert',NULL,'2026-09-30 19:22:11.892');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (65,'announcement.read','查看公告','announcement',NULL,'2026-09-30 19:22:11.892');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (67,'webhook.read','查看 Webhook','webhook',NULL,'2026-09-30 19:22:11.892');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (69,'pricing.read','查看计费规则','pricing',NULL,'2026-09-30 19:22:11.892');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (70,'finance.read','查看财务记录','finance',NULL,'2026-09-30 19:22:11.892');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (71,'device.import','导入设备','device',NULL,'2026-09-30 19:22:11.892');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (76,'pricing.rule.update','停用计费规则','pricing',NULL,'2026-09-30 19:22:11.915');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (77,'billing.meter.review','实际计量双人核实','billing',NULL,'2026-09-30 19:22:11.922');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (78,'coupon.activity.read','查看活动规则','coupon','查看优惠券活动规则与发放统计','2026-09-30 19:22:12.008');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (79,'coupon.activity.manage','管理活动规则','coupon','创建、调整与停用优惠券活动规则','2026-09-30 19:22:12.008');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (80,'audit.read','查看审计日志','audit','查看操作审计日志与操作前后快照','2026-09-30 19:22:12.041');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (81,'user.read','查看充电用户','user','查看充电用户列表与档案,含完整手机号——持有本权限等同于持有全部充电用户手机号,授权需谨慎','2026-09-30 19:22:12.553');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (82,'vendor.read','查看厂商','vendor',NULL,'2026-09-30 19:22:12.560');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (83,'vendor.create','新建厂商','vendor',NULL,'2026-09-30 19:22:12.560');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (84,'vendor.update','编辑及启停厂商','vendor',NULL,'2026-09-30 19:22:12.560');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (85,'online_card.manage','管理在线卡绑定与状态','user',NULL,'2026-09-30 19:22:12.567');

-- role 默认记录
INSERT INTO `role` (`id`,`code`,`name`,`description`,`is_builtin`,`created_at`,`updated_at`,`deleted_at`) VALUES (1,'customer_admin','客户管理员',NULL,1,'2026-09-30 19:22:11.854','2026-09-30 19:22:11.854',NULL);
INSERT INTO `role` (`id`,`code`,`name`,`description`,`is_builtin`,`created_at`,`updated_at`,`deleted_at`) VALUES (2,'customer_ops','运营',NULL,1,'2026-09-30 19:22:11.894','2026-09-30 19:22:11.894',NULL);
INSERT INTO `role` (`id`,`code`,`name`,`description`,`is_builtin`,`created_at`,`updated_at`,`deleted_at`) VALUES (3,'customer_cs','客服',NULL,1,'2026-09-30 19:22:11.894','2026-09-30 19:22:11.894',NULL);
INSERT INTO `role` (`id`,`code`,`name`,`description`,`is_builtin`,`created_at`,`updated_at`,`deleted_at`) VALUES (4,'customer_finance','财务',NULL,1,'2026-09-30 19:22:11.894','2026-09-30 19:22:11.894',NULL);

-- role_permission 默认记录
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,1);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,2);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,3);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,4);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,5);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,6);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,7);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,8);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,9);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,10);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,11);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,12);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,13);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,14);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,15);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,16);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,17);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,18);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,19);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,20);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,21);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,22);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,23);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,24);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,25);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,26);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,27);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,28);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,29);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,30);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,31);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,32);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,33);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,35);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,36);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,37);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,38);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,39);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,51);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,52);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,53);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,57);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,58);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,59);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,60);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,61);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,62);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,63);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,64);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,65);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,67);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,69);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,70);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,71);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,76);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,77);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,78);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,79);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,80);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,81);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,82);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,83);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,84);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,85);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,1);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,2);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,3);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,4);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,5);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,6);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,14);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,15);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,20);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,21);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,22);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,23);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,24);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,25);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,39);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,51);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,52);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,53);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,58);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,59);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,60);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,64);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,65);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,67);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,71);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,78);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,80);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,82);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,83);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,84);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (3,14);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (3,15);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (3,16);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (3,17);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (3,20);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (3,57);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (3,62);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (3,63);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (3,81);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (4,7);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (4,8);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (4,9);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (4,10);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (4,11);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (4,12);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (4,13);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (4,20);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (4,35);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (4,36);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (4,37);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (4,38);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (4,62);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (4,70);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (4,77);

-- billing_db 领域表
-- fee_calculation：计费明细
CREATE TABLE `fee_calculation` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `calculation_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '新C充电订单对应B前缀编号；历史FEE计费编号保留',
  `order_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务订单编号',
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `user_id` bigint unsigned NOT NULL COMMENT '充电用户 ID',
  `station_id` bigint unsigned DEFAULT NULL COMMENT '充电站点 ID',
  `pricing_rule_id` bigint unsigned DEFAULT NULL COMMENT '计费规则 ID',
  `pricing_rule_version` int unsigned DEFAULT NULL COMMENT '冻结使用的计费规则版本',
  `charged_kwh` decimal(12,4) NOT NULL COMMENT '累计充电电量，单位 kWh',
  `charged_seconds` int unsigned NOT NULL COMMENT '累计充电时长，单位秒',
  `peak_kwh` decimal(12,4) DEFAULT NULL COMMENT '峰时段充电电量，单位 kWh',
  `off_kwh` decimal(12,4) DEFAULT NULL COMMENT '谷时段充电电量，单位 kWh',
  `electric_cents` bigint NOT NULL COMMENT '电费，单位分',
  `service_cents` bigint NOT NULL COMMENT '服务费，单位分',
  `total_cents` bigint NOT NULL COMMENT '总金额，单位分',
  `breakdown_json` json DEFAULT NULL COMMENT '费用计算明细 JSON',
  `created_month` date NOT NULL COMMENT '月分区归属日期，取对应月份第一天',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
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
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `payload_json` json NOT NULL COMMENT '任务执行或投递载荷 JSON',
  `delivered` tinyint(1) NOT NULL DEFAULT '0' COMMENT '计费结果是否已投递成功，0 否、1 是',
  `attempts` int unsigned NOT NULL DEFAULT '0' COMMENT '已尝试执行次数',
  `scheduled_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '计划执行时间',
  `delivered_at` datetime(3) DEFAULT NULL COMMENT '投递成功时间',
  PRIMARY KEY (`charge_order_id`),
  KEY `idx_due` (`delivered`,`scheduled_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='计费结果投递任务';

-- fee_receipt：计费事件幂等回执
CREATE TABLE `fee_receipt` (
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `source_json` json NOT NULL COMMENT '来源事件和计量数据快照 JSON',
  `calculation_id` bigint unsigned DEFAULT NULL COMMENT '计费结果 ID',
  `calculation_no` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '新C充电订单对应B前缀编号；历史FEE计费编号保留',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`charge_order_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='计费事件幂等回执';

-- manual_fee_review：人工定价兜底单(D16)
CREATE TABLE `manual_fee_review` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `charge_order_id` bigint unsigned NOT NULL COMMENT '充电订单 ID',
  `order_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务订单编号',
  `reason` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '无法自动计费的原因(供人工定价参考)',
  `energy_wh` bigint unsigned NOT NULL DEFAULT '0' COMMENT '设备上报的总电量，单位 Wh',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `created_month` varchar(7) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'YYYY-MM,供月度归档扫描',
  `source_json` json DEFAULT NULL COMMENT '来源事件和计量数据快照 JSON',
  `status` enum('pending','resolved') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '当前业务状态；取值 pending / resolved',
  `resolved_at` datetime(3) DEFAULT NULL COMMENT '异常恢复或处理完成时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_order` (`charge_order_id`),
  KEY `idx_month` (`created_month`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='人工定价兜底单(D16)';


-- settlement：分账汇总
CREATE TABLE `settlement` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `settlement_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '分账结算编号',
  `split_template_id` bigint unsigned NOT NULL COMMENT '分账模板 ID',
  `split_template_code` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '分账模板编码快照',
  `mode` enum('mode_a','mode_b') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'mode_a' COMMENT '业务计算模式；取值 mode_a / mode_b',
  `fee_calculation_id` bigint unsigned NOT NULL COMMENT '计费结果 ID',
  `generation` int unsigned NOT NULL DEFAULT '1' COMMENT '分账生成批次，用于区分同一计费结果的重算版本',
  `order_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务订单编号',
  `total_cents` bigint NOT NULL COMMENT '总金额，单位分',
  `split_pool_cents` bigint NOT NULL COMMENT '进入分账池的金额，单位分',
  `split_pool_excluded_electric_cents` bigint NOT NULL DEFAULT '0' COMMENT 'mode_b 下不进入分账池的电费金额，单位分',
  `status` enum('pending','confirmed','paid','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '当前业务状态；取值 pending / confirmed / paid / failed',
  `created_month` date NOT NULL COMMENT '月分区归属日期，取对应月份第一天',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `confirmed_at` datetime(3) DEFAULT NULL COMMENT '业务确认时间',
  `paid_at` datetime(3) DEFAULT NULL COMMENT '支付完成时间',
  `electric_cents` bigint NOT NULL DEFAULT '0' COMMENT '电费，单位分',
  `service_cents` bigint NOT NULL DEFAULT '0' COMMENT '服务费，单位分',
  PRIMARY KEY (`id`,`created_month`),
  UNIQUE KEY `uk_no` (`settlement_no`,`created_month`),
  UNIQUE KEY `uk_fee_generation` (`fee_calculation_id`,`generation`),
  KEY `idx_fee_calc` (`fee_calculation_id`,`created_month`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='分账汇总';

-- settlement_party_amount：分账参与方金额
CREATE TABLE `settlement_party_amount` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `settlement_id` bigint unsigned NOT NULL COMMENT '分账结算 ID',
  `party_id` bigint unsigned NOT NULL COMMENT '分账参与方 ID',
  `party_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '分账参与方业务编码',
  `party_name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '分账参与方名称快照',
  `ratio_bp` int unsigned NOT NULL COMMENT '分账比例，单位基点，10000 表示 100%',
  `amount_cents` bigint NOT NULL COMMENT '金额，单位分',
  `status` enum('pending','paid','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '当前业务状态；取值 pending / paid / failed',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `electric_cents` bigint NOT NULL DEFAULT '0' COMMENT '电费，单位分',
  `service_cents` bigint NOT NULL DEFAULT '0' COMMENT '服务费，单位分',
  `generation` int unsigned NOT NULL DEFAULT '1' COMMENT '分账生成批次，用于区分同一计费结果的重算版本',
  PRIMARY KEY (`id`),
  KEY `idx_settlement` (`settlement_id`),
  KEY `idx_party_code` (`party_code`),
  KEY `idx_settlement_party` (`settlement_id`,`party_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='分账参与方金额';

-- withdraw_request：提现申请
CREATE TABLE `withdraw_request` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `withdraw_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '提现申请编号',
  `party_id` bigint unsigned NOT NULL COMMENT '分账参与方 ID',
  `party_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '分账参与方业务编码',
  `amount_cents` bigint NOT NULL COMMENT '金额，单位分',
  `bank_account` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '收款银行账号',
  `bank_name` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '收款银行名称',
  `status` enum('pending','approved','rejected','paid','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '当前业务状态；取值 pending / approved / rejected / paid / failed',
  `reviewed_by` bigint unsigned DEFAULT NULL COMMENT '审核人 ID',
  `reviewed_at` datetime(3) DEFAULT NULL COMMENT '审核完成时间',
  `reject_reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '拒绝申请的原因',
  `paid_at` datetime(3) DEFAULT NULL COMMENT '支付完成时间',
  `note` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '业务备注或处理说明',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_no` (`withdraw_no`),
  KEY `idx_party_status` (`party_id`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='提现申请';

-- 初始化数据：内置权限、角色及系统默认配置。业务与演示数据另行创建。

-- +goose Down
-- 仅用于可丢弃的开发库。
DROP TABLE IF EXISTS `withdraw_request`;
DROP TABLE IF EXISTS `settlement_party_amount`;
DROP TABLE IF EXISTS `settlement`;
DROP TABLE IF EXISTS `manual_fee_review`;
DROP TABLE IF EXISTS `fee_receipt`;
DROP TABLE IF EXISTS `fee_delivery`;
DROP TABLE IF EXISTS `fee_calculation`;
DROP TABLE IF EXISTS `whitelabel_config`;
DROP TABLE IF EXISTS `webhook_subscription`;
DROP TABLE IF EXISTS `webhook_delivery_log`;
DROP TABLE IF EXISTS `station`;
DROP TABLE IF EXISTS `split_template`;
DROP TABLE IF EXISTS `split_party`;
DROP TABLE IF EXISTS `role_permission`;
DROP TABLE IF EXISTS `role`;
DROP TABLE IF EXISTS `regulatory_report`;
DROP TABLE IF EXISTS `pricing_template`;
DROP TABLE IF EXISTS `pricing_rule`;
DROP TABLE IF EXISTS `pricing_publication`;
DROP TABLE IF EXISTS `permission`;
DROP TABLE IF EXISTS `finance_reconcile_log`;
DROP TABLE IF EXISTS `export_task`;
DROP TABLE IF EXISTS `admin_event_outbox`;
DROP TABLE IF EXISTS `device_meta`;
DROP TABLE IF EXISTS `device_import_identity`;
DROP TABLE IF EXISTS `device_import`;
DROP TABLE IF EXISTS `audit_log`;
DROP TABLE IF EXISTS `announcement`;
DROP TABLE IF EXISTS `alert_event`;
DROP TABLE IF EXISTS `admin_user`;
DROP TABLE IF EXISTS `admin_field_mask`;
DROP TABLE IF EXISTS `admin_data_scope`;
DROP TABLE IF EXISTS `snowflake_state`;
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
DROP TABLE IF EXISTS `payment_order`;
DROP TABLE IF EXISTS `payment_callback_idempotent`;
DROP TABLE IF EXISTS `online_card_audit`;
DROP TABLE IF EXISTS `online_card`;
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
DROP TABLE IF EXISTS `charge_payment_intent`;
DROP TABLE IF EXISTS `charge_order_pricing`;
DROP TABLE IF EXISTS `charge_order`;
DROP TABLE IF EXISTS `charge_meter_review`;
DROP TABLE IF EXISTS `charge_manual_settlement`;
DROP TABLE IF EXISTS `charge_fee_receipt`;
DROP TABLE IF EXISTS `charge_event_log`;
DROP TABLE IF EXISTS `charge_end_receipt`;
DROP TABLE IF EXISTS `charge_billing_job`;
DROP TABLE IF EXISTS `charge_billing_cutoff`;
DROP TABLE IF EXISTS `charge_bill_read`;
DROP TABLE IF EXISTS `charge_bill`;
DROP TABLE IF EXISTS `card_operation`;
DROP TABLE IF EXISTS `card_charge`;
DROP TABLE IF EXISTS `active_port_charge`;
