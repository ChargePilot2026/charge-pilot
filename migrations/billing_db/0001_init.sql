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
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `calculation_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '计费结果编号',
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
  `calculation_no` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '计费结果编号',
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

-- pricing_tier_snapshot：计费规则快照(版本化)
CREATE TABLE `pricing_tier_snapshot` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `fee_calculation_id` bigint unsigned NOT NULL COMMENT '计费结果 ID',
  `pricing_rule_id` bigint unsigned NOT NULL COMMENT '计费规则 ID',
  `version` int unsigned NOT NULL COMMENT '业务版本号',
  `snapshot_json` json NOT NULL COMMENT '计费规则完整快照(便于审计回放)',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`id`),
  KEY `idx_fee_calc` (`fee_calculation_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='计费规则快照(版本化)';

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
-- 仅供一次性开发/测试库回退；删除当前库全部业务表。
DROP TABLE IF EXISTS `withdraw_request`;
DROP TABLE IF EXISTS `settlement_party_amount`;
DROP TABLE IF EXISTS `settlement`;
DROP TABLE IF EXISTS `pricing_tier_snapshot`;
DROP TABLE IF EXISTS `manual_fee_review`;
DROP TABLE IF EXISTS `fee_receipt`;
DROP TABLE IF EXISTS `fee_delivery`;
DROP TABLE IF EXISTS `fee_calculation`;
