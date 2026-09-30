-- +goose NO TRANSACTION
-- +goose Up

-- ChargePilot admin_db 初始化结构
-- 后台管理：身份权限、站点设备、充电方案、运营与审计
-- 发布前基线：直接修改 CREATE TABLE，重建空库；不叠加增量 ALTER。
-- 表间关联由所属服务维护；此文件只初始化当前库。
-- 计量、事件和审计表保留月分区及 p_max，后续月份由维护任务创建。

SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci;
SET sql_mode = 'STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION,ERROR_FOR_DIVISION_BY_ZERO,NO_ZERO_DATE,NO_ZERO_IN_DATE';

-- admin_coupon：运营优惠券管理(冗余自 user_db.coupon)
CREATE TABLE `admin_coupon` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `user_coupon_id` bigint unsigned NOT NULL COMMENT '关联 user_db.coupon.id',
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `status` enum('active','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active',
  `grant_strategy` json DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='运营优惠券管理(冗余自 user_db.coupon)';

-- admin_data_scope：后台账号数据范围
CREATE TABLE `admin_data_scope` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `admin_user_id` bigint unsigned NOT NULL,
  `scope_type` enum('station','vendor') COLLATE utf8mb4_unicode_ci NOT NULL,
  `scope_id` bigint unsigned NOT NULL,
  `created_by` bigint unsigned NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user_scope` (`admin_user_id`,`scope_type`,`scope_id`),
  KEY `idx_user` (`admin_user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='后台账号数据范围';

-- admin_field_mask：角色字段脱敏规则
CREATE TABLE `admin_field_mask` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `role_id` bigint unsigned NOT NULL,
  `resource` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `field` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_role_resource_field` (`role_id`,`resource`,`field`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='角色字段脱敏规则';

-- admin_user_role：管理员账号
CREATE TABLE `admin_user_role` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `username` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `display_name` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `password_hash` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `phone` varchar(32) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `email` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `role_id` bigint unsigned DEFAULT NULL,
  `mfa_secret` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT 'TOTP 密钥(base32，启用前为待确认状态)',
  `mfa_enabled` tinyint(1) NOT NULL DEFAULT '0',
  `status` enum('active','disabled','locked') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active',
  `last_login_at` datetime(3) DEFAULT NULL,
  `failed_login_count` int unsigned NOT NULL DEFAULT '0',
  `locked_until` datetime(3) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  `deleted_by` bigint unsigned DEFAULT NULL,
  `active_username` varchar(64) COLLATE utf8mb4_unicode_ci GENERATED ALWAYS AS (if((`deleted_at` is null),`username`,NULL)) STORED,
  `auth_version` bigint unsigned NOT NULL DEFAULT '0',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_username` (`username`,`deleted_at`),
  UNIQUE KEY `uk_active_username` (`active_username`),
  KEY `idx_mfa_state` (`mfa_enabled`,`mfa_secret`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='管理员账号';

-- alert_event：告警事件
CREATE TABLE `alert_event` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `rule_id` bigint unsigned DEFAULT NULL,
  `severity` enum('warning','critical','fatal') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'warning',
  `metric` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `value` decimal(18,6) DEFAULT NULL,
  `threshold` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `status` enum('active','acknowledged','resolved','auto_resolved') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active',
  `acked_by` bigint unsigned DEFAULT NULL,
  `acked_at` datetime(3) DEFAULT NULL,
  `resolved_at` datetime(3) DEFAULT NULL,
  `note` text COLLATE utf8mb4_unicode_ci,
  `event_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `created_month` date NOT NULL,
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

-- alert_rule：告警规则
CREATE TABLE `alert_rule` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `device_id_pattern` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '*',
  `metric` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `op` enum('>','<','!=','between','==') COLLATE utf8mb4_unicode_ci NOT NULL,
  `threshold` json NOT NULL,
  `window_seconds` int unsigned NOT NULL DEFAULT '60',
  `severity` enum('warning','critical','fatal') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'warning',
  `enabled` tinyint(1) NOT NULL DEFAULT '1',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_metric_enabled` (`metric`,`enabled`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='告警规则';

-- alert_subscription：告警订阅
CREATE TABLE `alert_subscription` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `rule_id` bigint unsigned DEFAULT NULL,
  `severity` enum('warning','critical','fatal') COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `webhook_subscription_id` bigint unsigned DEFAULT NULL,
  `admin_user_id` bigint unsigned DEFAULT NULL,
  `enabled` tinyint(1) NOT NULL DEFAULT '1',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='告警订阅';

-- announcement：公告
CREATE TABLE `announcement` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `title` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `title_i18n` json DEFAULT NULL,
  `content` text COLLATE utf8mb4_unicode_ci NOT NULL,
  `content_i18n` json DEFAULT NULL,
  `scope` enum('global','station','city') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'global',
  `target_ids` json DEFAULT NULL,
  `priority` tinyint unsigned NOT NULL DEFAULT '0',
  `start_at` datetime(3) NOT NULL,
  `end_at` datetime(3) DEFAULT NULL,
  `status` enum('draft','published','expired') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'draft',
  `created_by` bigint unsigned NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_status_window` (`status`,`start_at`,`end_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='公告';

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

-- charge_offer：charge offer业务记录
-- device_id 为 NULL 表示站点默认；设备独立方案优先，完整方案含各模式与套餐。
CREATE TABLE `charge_offer` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `station_id` bigint unsigned NOT NULL,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `mode` enum('amount','package') COLLATE utf8mb4_unicode_ci NOT NULL,
  `price_cents` bigint NOT NULL,
  `duration_minutes` smallint unsigned NOT NULL DEFAULT '0',
  `status` enum('active','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active',
  `version` int unsigned NOT NULL DEFAULT '1',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `package_template_id` bigint unsigned DEFAULT NULL,
  `min_charge_cents` bigint NOT NULL DEFAULT '0' COMMENT '该套餐自身的最低扣费',
  `show_remark` tinyint(1) NOT NULL DEFAULT '0',
  `card_default` tinyint(1) NOT NULL DEFAULT '0',
  `stop_when_full` tinyint(1) NOT NULL DEFAULT '0' COMMENT '充满自停',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删；此前该表无此列，删除路径已在按它过滤',
  PRIMARY KEY (`id`),
  KEY `idx_station_status` (`station_id`,`status`),
  KEY `idx_device` (`device_id`,`status`),
  KEY `idx_package_template` (`package_template_id`,`station_id`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- customer：客户(部署单位)
CREATE TABLE `customer` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `status` enum('active','suspended') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active',
  `contact_phone` varchar(32) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `contact_email` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='客户(部署单位)';

-- customer_service_config：客服坐席
CREATE TABLE `customer_service_config` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `agent_wechat` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '客服坐席微信号',
  `agent_name` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `path` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '微信客服入口 URL',
  `priority` int unsigned NOT NULL DEFAULT '0',
  `enabled` tinyint(1) NOT NULL DEFAULT '1',
  `working_hours_json` json DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='客服坐席';

-- device_import：device import业务记录
CREATE TABLE `device_import` (
  `import_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `actor_id` bigint unsigned NOT NULL,
  `request_json` json NOT NULL,
  `status` enum('pending','completed','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `last_error` varchar(1024) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `attempts` int unsigned NOT NULL DEFAULT '0',
  `retryable` tinyint(1) NOT NULL DEFAULT '1',
  `next_attempt_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`import_id`),
  KEY `idx_retry` (`retryable`,`next_attempt_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- device_import_identity：device import identity业务记录
CREATE TABLE `device_import_identity` (
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `request_json` json NOT NULL,
  PRIMARY KEY (`device_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- device_meta：设备元数据(冗余自 gateway_db)
-- protocol_adapter 在创建/导入时由服务器按厂商协议填写；设备能力由协议决定。
-- status 为后台运营状态，独立于 TCP 在线状态；禁用后不接受新启动/加时。
CREATE TABLE `device_meta` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `station_id` bigint unsigned DEFAULT NULL,
  `vendor_id` bigint unsigned DEFAULT NULL,
  `model` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `protocol_adapter` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '创建设备时选定的通信协议',
  `charge_mode` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'device_duration',
  `serial_no` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `install_at` datetime(3) DEFAULT NULL,
  `warranty_until` datetime(3) DEFAULT NULL,
  `status` enum('enabled','disabled','retired','fault') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'enabled',
  `tags_json` json DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_device_id` (`device_id`,`deleted_at`),
  KEY `idx_station` (`station_id`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备元数据(冗余自 gateway_db)';

-- event_outbox：事件 outbox(可靠发布)
CREATE TABLE `event_outbox` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `event_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `stream` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '目标 stream 名',
  `envelope_json` json NOT NULL,
  `status` enum('pending','published','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `stream_message_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT 'XADD 返回的 stream id,用于缺口反查',
  `retry_count` int unsigned NOT NULL DEFAULT '0',
  `last_error` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `scheduled_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `published_at` datetime(3) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_event` (`event_id`),
  KEY `idx_status_sched` (`status`,`scheduled_at`),
  KEY `idx_stream_msgid` (`stream`,`stream_message_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='事件 outbox(可靠发布)';

-- export_task：导出任务
CREATE TABLE `export_task` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `task_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `resource` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `file_format` enum('csv','xlsx','pdf') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'csv',
  `status` enum('pending','running','completed','failed','expired') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `requested_by` bigint unsigned NOT NULL,
  `filter_json` json DEFAULT NULL,
  `row_count` int unsigned NOT NULL DEFAULT '0',
  `file_path` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `error_msg` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `expires_at` datetime(3) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `completed_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_task_no` (`task_no`),
  KEY `idx_status` (`status`,`expires_at`),
  KEY `idx_requester` (`requested_by`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='导出任务';

-- finance_reconcile_log：财务对账日志
CREATE TABLE `finance_reconcile_log` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `reconcile_type` enum('wechat_refund','wechat_pay','split','withdraw') COLLATE utf8mb4_unicode_ci NOT NULL,
  `reconcile_date` date NOT NULL,
  `internal_count` int unsigned NOT NULL,
  `wechat_count` int unsigned NOT NULL,
  `diff_count` int NOT NULL,
  `internal_cents` bigint NOT NULL,
  `wechat_cents` bigint NOT NULL,
  `diff_cents` bigint NOT NULL,
  `diffs_json` json DEFAULT NULL,
  `resolved` tinyint(1) NOT NULL DEFAULT '0',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_type_date` (`reconcile_type`,`reconcile_date`),
  KEY `idx_type_date` (`reconcile_type`,`reconcile_date`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='财务对账日志';

-- invoice_review：发票审核
CREATE TABLE `invoice_review` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `invoice_request_id` bigint unsigned NOT NULL COMMENT '关联 user_db.invoice_request.id',
  `review_status` enum('pending','awaiting_second','approved','rejected') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `first_reviewer_id` bigint unsigned DEFAULT NULL,
  `first_reviewed_at` datetime(3) DEFAULT NULL,
  `second_reviewer_id` bigint unsigned DEFAULT NULL,
  `second_reviewed_at` datetime(3) DEFAULT NULL,
  `reviewed_by` bigint unsigned DEFAULT NULL,
  `reviewed_at` datetime(3) DEFAULT NULL,
  `reject_reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `invoice_url` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_invoice_review_request` (`invoice_request_id`),
  KEY `idx_status` (`review_status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='发票审核';

-- ota_package：OTA 固件包
CREATE TABLE `ota_package` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `vendor_id` bigint unsigned DEFAULT NULL,
  `version` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `storage_url` varchar(512) COLLATE utf8mb4_unicode_ci NOT NULL,
  `size_bytes` bigint unsigned NOT NULL,
  `checksum_sha256` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `sign` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `release_notes` text COLLATE utf8mb4_unicode_ci,
  `status` enum('draft','published','archived') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'draft',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_code_version` (`code`,`version`,`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='OTA 固件包';

-- ota_schedule：OTA 推送计划
CREATE TABLE `ota_schedule` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `package_id` bigint unsigned NOT NULL,
  `target_filter_json` json DEFAULT NULL,
  `rollout_strategy` enum('all','canary','batch','manual') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'all',
  `batch_size` int unsigned DEFAULT NULL,
  `status` enum('pending','running','completed','cancelled','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `scheduled_at` datetime(3) DEFAULT NULL,
  `started_at` datetime(3) DEFAULT NULL,
  `completed_at` datetime(3) DEFAULT NULL,
  `created_by` bigint unsigned NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='OTA 推送计划';

-- permission：权限码
-- 权限由后端路由守卫执行；角色授权以初始化种子为准。
CREATE TABLE `permission` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '如 orders.read / orders.refund.approve',
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `module` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'orders / devices / billing / ...',
  `description` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_code` (`code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='权限码';

-- pricing_package_template：充电套餐模板
CREATE TABLE `pricing_package_template` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `kind` enum('amount','package') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'amount=预付封顶金额；package=封顶金额+时长',
  `price_cents` bigint NOT NULL DEFAULT '0' COMMENT 'amount 的封顶金额；package 为 0，按费率结算',
  `duration_minutes` smallint unsigned NOT NULL DEFAULT '0',
  `stop_when_full` tinyint(1) NOT NULL DEFAULT '0' COMMENT '充满自停；固件参数表中的同名开关',
  `min_charge_cents` bigint NOT NULL DEFAULT '0' COMMENT '该套餐自身的最低扣费，与模板级电费最低消费是两件事',
  `show_remark` tinyint(1) NOT NULL DEFAULT '0' COMMENT '用户端是否展示套餐备注',
  `card_default` tinyint(1) NOT NULL DEFAULT '0' COMMENT '刷卡时的默认套餐',
  `sort_order` int NOT NULL DEFAULT '0',
  `status` enum('active','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active',
  `version` int unsigned NOT NULL DEFAULT '1',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='充电套餐模板';

-- pricing_publication：pricing publication业务记录
CREATE TABLE `pricing_publication` (
  `request_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `actor_id` bigint unsigned NOT NULL,
  `payload_hash` char(64) CHARACTER SET ascii COLLATE ascii_general_ci NOT NULL,
  `rule_id` bigint unsigned NOT NULL,
  `version` int unsigned NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`request_id`),
  UNIQUE KEY `uk_rule` (`rule_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- pricing_rule：计费规则
-- device_id 为 NULL 表示站点默认；设备独立方案优先，完整方案含各模式与套餐。
CREATE TABLE `pricing_rule` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `template_id` bigint unsigned DEFAULT NULL,
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `name_i18n` json DEFAULT NULL,
  `station_id` bigint unsigned DEFAULT NULL,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `version` int unsigned NOT NULL DEFAULT '1',
  `status` enum('active','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active',
  `effective_from` datetime(3) DEFAULT NULL,
  `effective_to` datetime(3) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  `spec_json` json NOT NULL COMMENT '完整计费口径，对应 pricing.Spec',
  `channel` enum('default','temp','card') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'default' COMMENT '计费通道，选择费率倍数',
  PRIMARY KEY (`id`),
  KEY `idx_station_status` (`station_id`,`status`),
  KEY `idx_template_station` (`template_id`,`station_id`,`status`),
  KEY `idx_device` (`device_id`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='计费规则';

-- pricing_switch_task：设备计费方式切换任务
CREATE TABLE `pricing_switch_task` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `task_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `station_id` bigint unsigned NOT NULL,
  `template_id` bigint unsigned NOT NULL,
  `mode_before` varchar(32) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '切换前计费方式，NULL=该设备此前无独立规则',
  `mode_after` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL,
  `device_count` int unsigned NOT NULL DEFAULT '0',
  `status` enum('pending','running','completed','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `requested_by` bigint unsigned NOT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `completed_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_task_no` (`task_no`),
  KEY `idx_station` (`station_id`,`created_at`),
  KEY `idx_status` (`status`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备计费方式切换任务';

-- pricing_switch_task_item：切换任务设备明细
CREATE TABLE `pricing_switch_task_item` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `task_id` bigint unsigned NOT NULL,
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `mode_before` varchar(32) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `mode_after` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL,
  `status` enum('pending','running','succeeded','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `offered_snapshot` json DEFAULT NULL COMMENT '下发后的套餐内容快照，如 0.1元/20分钟、2元/1分钟',
  `error_msg` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `completed_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_task_device` (`task_id`,`device_id`),
  KEY `idx_status` (`status`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='切换任务设备明细';

-- pricing_template：计费模板
CREATE TABLE `pricing_template` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '模板名称',
  `remark` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '模板备注',
  `spec_json` json NOT NULL COMMENT '计费口径，对应 pricing.Spec',
  `display_json` json NOT NULL COMMENT '用户端展示开关，对应 pricing.Display',
  `status` enum('active','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active',
  `version` int unsigned NOT NULL DEFAULT '1',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='计费模板';

-- refund_task：refund task业务记录
CREATE TABLE `refund_task` (
  `refund_no` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `event_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `stage` enum('queued','querying','reporting','done','manual_review') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'queued',
  `request_json` json DEFAULT NULL,
  `result_json` json DEFAULT NULL,
  `attempts` int unsigned NOT NULL DEFAULT '0',
  `last_error` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `scheduled_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`refund_no`),
  KEY `idx_due` (`stage`,`scheduled_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- regulatory_report：监管报送持久队列
CREATE TABLE `regulatory_report` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `event_id` char(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `object_type` enum('operator','station','device','order','alert','battery') COLLATE utf8mb4_unicode_ci NOT NULL,
  `object_key` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `payload_json` json NOT NULL,
  `status` enum('queued','processing','delivered') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'queued',
  `attempts` int unsigned NOT NULL DEFAULT '0',
  `next_attempt_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `lease_token` char(36) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `lease_until` datetime(3) DEFAULT NULL,
  `last_error` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `delivered_at` datetime(3) DEFAULT NULL,
  `delivered_mode` enum('simulation','http') COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_event` (`event_id`),
  KEY `idx_due` (`status`,`next_attempt_at`,`lease_until`),
  KEY `idx_object` (`object_type`,`object_key`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='监管报送持久队列';

-- risk_config：风控配置
CREATE TABLE `risk_config` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `key` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `value` json NOT NULL,
  `description` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `updated_by` bigint unsigned DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_key` (`key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='风控配置';

-- role：角色
CREATE TABLE `role` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `description` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `is_builtin` tinyint(1) NOT NULL DEFAULT '0' COMMENT '内置不可删',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  `active_code` varchar(64) COLLATE utf8mb4_unicode_ci GENERATED ALWAYS AS (if((`deleted_at` is null),`code`,NULL)) STORED,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_code` (`code`,`deleted_at`),
  UNIQUE KEY `uk_active_code` (`active_code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='角色';

-- role_permission：角色-权限映射
-- 权限由后端路由守卫执行；角色授权以初始化种子为准。
CREATE TABLE `role_permission` (
  `role_id` bigint unsigned NOT NULL,
  `permission_id` bigint unsigned NOT NULL,
  PRIMARY KEY (`role_id`,`permission_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='角色-权限映射';

-- settled_record：分账出账
CREATE TABLE `settled_record` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `settlement_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `split_template_id` bigint unsigned NOT NULL,
  `period_start` date NOT NULL,
  `period_end` date NOT NULL,
  `total_cents` bigint NOT NULL,
  `status` enum('draft','confirmed','paid','disputed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'draft',
  `reviewed_by` bigint unsigned DEFAULT NULL,
  `reviewed_at` datetime(3) DEFAULT NULL,
  `paid_at` datetime(3) DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_no` (`settlement_no`),
  KEY `idx_period` (`period_start`,`period_end`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='分账出账';

-- split_party：分账参与方
CREATE TABLE `split_party` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `split_template_id` bigint unsigned NOT NULL,
  `party_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `party_name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `ratio_bp` int unsigned NOT NULL COMMENT '万分比(basis point,合计 10000)',
  `bank_account` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `bank_name` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_split_party_code` (`split_template_id`,`party_code`),
  KEY `idx_template` (`split_template_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='分账参与方';

-- split_template：分账模板
CREATE TABLE `split_template` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `name_i18n` json DEFAULT NULL,
  `mode` enum('mode_a','mode_b') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'mode_a' COMMENT 'A: 全分账;B: 仅服务费',
  `status` enum('active','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_split_template_code` (`code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='分账模板';

-- station：充电站点
-- 充电站全年无休，不设置营业时间。
CREATE TABLE `station` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `address` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `longitude` decimal(11,8) NOT NULL,
  `latitude` decimal(10,8) NOT NULL,
  `contact_phone` varchar(32) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `status` enum('active','disabled','construction') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active',
  `pricing_template_id` bigint unsigned DEFAULT NULL,
  `split_template_id` bigint unsigned DEFAULT NULL,
  `config_json` json DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  `deleted_by` bigint unsigned DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_geo` (`latitude`,`longitude`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='充电站点';

-- station_policy：场地充值与退款策略
CREATE TABLE `station_policy` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `station_id` bigint unsigned NOT NULL,
  `force_recharge` tinyint(1) NOT NULL DEFAULT '0' COMMENT '余额低于门槛时强制充值',
  `min_balance_cents` bigint NOT NULL DEFAULT '0',
  `scan_refund_path` enum('balance','original') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'balance' COMMENT '扫码退款的退费路径',
  `scan_refund_rule` enum('none','realtime','time_limited') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'none' COMMENT '扫码退费的退费规则',
  `card_refund_path` enum('balance','original') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'balance',
  `card_refund_rule` enum('none','realtime','time_limited_prorated') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'none',
  `timeout_start_refund` tinyint(1) NOT NULL DEFAULT '0' COMMENT '启动结果不确定时是否直接退款',
  `verify_phone_before_charge` tinyint(1) NOT NULL DEFAULT '0',
  `version` int unsigned NOT NULL DEFAULT '1',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_station` (`station_id`,`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='场地充值与退款策略';

-- station_recharge_package：场地充值套餐
CREATE TABLE `station_recharge_package` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `station_id` bigint unsigned NOT NULL,
  `name` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '后台限长 10 字，留出展示余量',
  `amount_cents` bigint NOT NULL,
  `bonus_cents` bigint NOT NULL DEFAULT '0',
  `sort_order` int NOT NULL DEFAULT '0',
  `status` enum('active','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_station` (`station_id`,`status`,`sort_order`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='场地充值套餐';

-- webhook_delivery_log：Webhook 投递日志
CREATE TABLE `webhook_delivery_log` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `subscription_id` bigint unsigned NOT NULL,
  `event_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `event_type` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `request_body` json NOT NULL,
  `response_status` int DEFAULT NULL,
  `response_body` text COLLATE utf8mb4_unicode_ci,
  `error_msg` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `attempt_count` int unsigned NOT NULL DEFAULT '1',
  `duration_ms` int unsigned DEFAULT NULL,
  `delivered_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_sub_event` (`subscription_id`,`event_id`),
  KEY `idx_sub_event` (`subscription_id`,`event_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Webhook 投递日志';

-- webhook_subscription：Webhook 订阅
CREATE TABLE `webhook_subscription` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `url` varchar(512) COLLATE utf8mb4_unicode_ci NOT NULL,
  `secret` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'HMAC 密钥',
  `event_types` json NOT NULL COMMENT '订阅的事件类型列表',
  `headers_json` json DEFAULT NULL,
  `enabled` tinyint(1) NOT NULL DEFAULT '1',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Webhook 订阅';

-- whitelabel_config：白标配置
CREATE TABLE `whitelabel_config` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  `logo_url` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `mini_program_name` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `mini_program_appid` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `theme_color` varchar(16) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `contact_phone` varchar(32) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `about_text` text COLLATE utf8mb4_unicode_ci,
  `config_json` json DEFAULT NULL,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='白标配置';

-- 初始化数据：内置权限、角色及系统默认配置。业务与演示数据另行创建。

-- permission 默认记录
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (1,'device.operate','启用禁用设备','device','切换设备运营状态，不停止已有订单','2026-09-30 19:22:11.260');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (2,'station.read','查看站点','station',NULL,'2026-09-30 19:22:11.651');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (3,'station.create','新增站点','station',NULL,'2026-09-30 19:22:11.651');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (4,'station.update','编辑站点','station',NULL,'2026-09-30 19:22:11.651');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (5,'station.delete','删除站点','station',NULL,'2026-09-30 19:22:11.651');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (6,'device.read','查看设备','device',NULL,'2026-09-30 19:22:11.655');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (7,'finance.refund.read','查看退款记录','finance',NULL,'2026-09-30 19:22:11.672');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (8,'finance.refund.retry','重试异常退款任务','finance',NULL,'2026-09-30 19:22:11.676');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (9,'order.refund.review','双签审核退款','finance',NULL,'2026-09-30 19:22:11.680');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (10,'order.refund.create','发起人工退款申请','finance',NULL,'2026-09-30 19:22:11.683');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (11,'finance.wallet_risk.review','钱包退款风控审核','finance',NULL,'2026-09-30 19:22:11.688');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (12,'finance.wallet_risk.release','解除钱包退款风控冻结','finance',NULL,'2026-09-30 19:22:11.692');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (13,'invoice.review','审核发票','finance',NULL,'2026-09-30 19:22:11.713');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (14,'feedback.read','查看用户评价与投诉','customer_service','查看用户提交的评价、投诉和建议','2026-09-30 19:22:11.718');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (15,'feedback.reply','回复与关闭用户反馈','customer_service','回复或关闭用户反馈','2026-09-30 19:22:11.718');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (16,'fault.read','查看设备报修','inspection','查看用户和巡检提交的设备报修','2026-09-30 19:22:11.718');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (17,'fault.dispatch','派单与处理设备报修','inspection','指派巡检人员并更新报修处理状态','2026-09-30 19:22:11.718');
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
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (34,'pricing.template.create','创建计费模板','pricing','创建计费模板','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (35,'finance.split_template.create','创建分账模板','finance','创建分账模板','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (36,'finance.split_party.create','维护分账参与方','finance','向分账模板增删参与方、比例与收款信息','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (37,'finance.withdraw.create','发起提现申请','finance','创建提现申请','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (38,'finance.withdraw.review','审核提现申请','finance','审批或驳回提现申请','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (39,'alert.ack','确认告警','alert','确认/忽略告警事件','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (40,'alert.rule.create','创建告警规则','alert','创建告警规则','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (41,'alert.rule.update','编辑告警规则','alert','修改告警规则','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (42,'alert.rule.delete','删除告警规则','alert','软删除告警规则','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (43,'alert.subscription.create','创建告警订阅','alert','创建告警订阅','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (44,'alert.risk_config.update','修改风控配置','alert','修改风控阈值配置','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (45,'membership.create','创建会员卡模板','membership','创建会员卡模板','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (46,'settings.ota.update','修改 OTA 配置','settings','修改 OTA 全局配置','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (47,'ota.package.create','创建 OTA 固件包','ota','上传并创建 OTA 固件包','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (48,'ota.package.delete','删除 OTA 固件包','ota','软删除 OTA 固件包','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (49,'ota.schedule.create','创建 OTA 升级计划','ota','创建 OTA 升级计划','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (50,'ota.schedule.trigger','触发 OTA 升级计划','ota','立即触发 OTA 升级计划','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (51,'announcement.create','创建公告','announcement','创建公告','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (52,'announcement.update','编辑公告','announcement','修改公告','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (53,'announcement.delete','删除公告','announcement','软删除公告','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (54,'customer_service.create','创建客服配置','customer_service','创建客服入口配置','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (55,'customer_service.update','编辑客服配置','customer_service','修改客服入口配置','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (56,'customer_service.delete','删除客服配置','customer_service','软删除客服入口配置','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (57,'fault.resolve','处理设备故障','fault','标记设备故障已处理','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (58,'webhook.create','创建 Webhook 订阅','webhook','创建 Webhook 订阅','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (59,'webhook.update','编辑 Webhook 订阅','webhook','修改 Webhook 订阅','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (60,'webhook.delete','删除 Webhook 订阅','webhook','软删除 Webhook 订阅','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (61,'export.create','创建导出任务','export','创建数据导出任务','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (62,'order.read','查看充电订单','order',NULL,'2026-09-30 19:22:11.886');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (63,'admin_user.read','查看管理员','admin_user',NULL,'2026-09-30 19:22:11.892');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (64,'alert.read','查看告警','alert',NULL,'2026-09-30 19:22:11.892');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (65,'announcement.read','查看公告','announcement',NULL,'2026-09-30 19:22:11.892');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (66,'customer_service.read','查看客服','customer_service',NULL,'2026-09-30 19:22:11.892');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (67,'webhook.read','查看 Webhook','webhook',NULL,'2026-09-30 19:22:11.892');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (68,'ota.read','查看 OTA','ota',NULL,'2026-09-30 19:22:11.892');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (69,'pricing.read','查看计费规则','pricing',NULL,'2026-09-30 19:22:11.892');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (70,'finance.read','查看财务记录','finance',NULL,'2026-09-30 19:22:11.892');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (71,'device.import','导入设备','device',NULL,'2026-09-30 19:22:11.892');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (76,'pricing.rule.update','停用计费规则','pricing',NULL,'2026-09-30 19:22:11.915');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (77,'billing.meter.review','实际计量双人核实','billing',NULL,'2026-09-30 19:22:11.922');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (78,'coupon.activity.read','查看活动规则','coupon','查看优惠券活动规则与发放统计','2026-09-30 19:22:12.008');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (79,'coupon.activity.manage','管理活动规则','coupon','创建、调整与停用优惠券活动规则','2026-09-30 19:22:12.008');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (80,'audit.read','查看审计日志','audit','查看操作审计日志与操作前后快照','2026-09-30 19:22:12.041');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (81,'charge_user.read','查看充电用户','charge_user','查看充电用户列表与档案,含完整手机号——持有本权限等同于持有全部充电用户手机号,授权需谨慎','2026-09-30 19:22:12.553');
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
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,34);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,35);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,36);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,37);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,38);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,39);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,40);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,41);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,42);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,43);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,44);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,45);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,46);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,47);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,48);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,49);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,50);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,51);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,52);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,53);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,54);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,55);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,56);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,57);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,58);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,59);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,60);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,61);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,62);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,63);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,64);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,65);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,66);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,67);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,68);
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
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,40);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,41);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,42);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,43);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,44);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,47);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,48);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,49);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,50);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,51);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,52);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,53);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,54);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,55);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,56);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,58);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,59);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,60);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,64);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,65);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,66);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,67);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,68);
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
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (3,66);
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

-- +goose Down
-- 仅供一次性开发/测试库回退；删除当前库全部业务表。
DROP TABLE IF EXISTS `whitelabel_config`;
DROP TABLE IF EXISTS `webhook_subscription`;
DROP TABLE IF EXISTS `webhook_delivery_log`;
DROP TABLE IF EXISTS `station_recharge_package`;
DROP TABLE IF EXISTS `station_policy`;
DROP TABLE IF EXISTS `station`;
DROP TABLE IF EXISTS `split_template`;
DROP TABLE IF EXISTS `split_party`;
DROP TABLE IF EXISTS `settled_record`;
DROP TABLE IF EXISTS `role_permission`;
DROP TABLE IF EXISTS `role`;
DROP TABLE IF EXISTS `risk_config`;
DROP TABLE IF EXISTS `regulatory_report`;
DROP TABLE IF EXISTS `refund_task`;
DROP TABLE IF EXISTS `pricing_template`;
DROP TABLE IF EXISTS `pricing_switch_task_item`;
DROP TABLE IF EXISTS `pricing_switch_task`;
DROP TABLE IF EXISTS `pricing_rule`;
DROP TABLE IF EXISTS `pricing_publication`;
DROP TABLE IF EXISTS `pricing_package_template`;
DROP TABLE IF EXISTS `permission`;
DROP TABLE IF EXISTS `ota_schedule`;
DROP TABLE IF EXISTS `ota_package`;
DROP TABLE IF EXISTS `invoice_review`;
DROP TABLE IF EXISTS `finance_reconcile_log`;
DROP TABLE IF EXISTS `export_task`;
DROP TABLE IF EXISTS `event_outbox`;
DROP TABLE IF EXISTS `device_meta`;
DROP TABLE IF EXISTS `device_import_identity`;
DROP TABLE IF EXISTS `device_import`;
DROP TABLE IF EXISTS `customer_service_config`;
DROP TABLE IF EXISTS `customer`;
DROP TABLE IF EXISTS `charge_offer`;
DROP TABLE IF EXISTS `audit_log`;
DROP TABLE IF EXISTS `announcement`;
DROP TABLE IF EXISTS `alert_subscription`;
DROP TABLE IF EXISTS `alert_rule`;
DROP TABLE IF EXISTS `alert_event`;
DROP TABLE IF EXISTS `admin_user_role`;
DROP TABLE IF EXISTS `admin_field_mask`;
DROP TABLE IF EXISTS `admin_data_scope`;
DROP TABLE IF EXISTS `admin_coupon`;
