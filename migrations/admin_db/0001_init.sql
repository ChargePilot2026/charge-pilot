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
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `user_coupon_id` bigint unsigned NOT NULL COMMENT '关联 user_db.coupon.id',
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务名称',
  `status` enum('active','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active' COMMENT '当前业务状态；取值 active / disabled',
  `grant_strategy` json DEFAULT NULL COMMENT '优惠券发放策略 JSON',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='运营优惠券管理(冗余自 user_db.coupon)';

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

-- admin_user_role：管理员账号
CREATE TABLE `admin_user_role` (
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
CREATE TABLE `charge_offer` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `station_id` bigint unsigned NOT NULL COMMENT '充电站点 ID',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '设备全局唯一编号',
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务名称',
  `mode` enum('amount','package') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '在售套餐模式；amount 金额预算、package 固定时长套餐',
  `price_cents` bigint NOT NULL COMMENT '销售价格，单位分',
  `duration_minutes` smallint unsigned NOT NULL DEFAULT '0' COMMENT '套餐充电时长，单位分钟',
  `status` enum('active','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active' COMMENT '当前业务状态；取值 active / disabled',
  `version` int unsigned NOT NULL DEFAULT '1' COMMENT '业务版本号',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `package_template_id` bigint unsigned DEFAULT NULL COMMENT '套餐模板 ID',
  `min_charge_cents` bigint NOT NULL DEFAULT '0' COMMENT '该套餐自身的最低扣费，单位分',
  `show_remark` tinyint(1) NOT NULL DEFAULT '0' COMMENT '是否在用户端显示套餐备注，0 否、1 是',
  `card_default` tinyint(1) NOT NULL DEFAULT '0' COMMENT '是否作为刷卡默认套餐，0 否、1 是',
  `stop_when_full` tinyint(1) NOT NULL DEFAULT '0' COMMENT '充满自停',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删；此前该表无此列，删除路径已在按它过滤',
  PRIMARY KEY (`id`),
  KEY `idx_station_status` (`station_id`,`status`),
  KEY `idx_device` (`device_id`,`status`),
  KEY `idx_package_template` (`package_template_id`,`station_id`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='站点或设备的在售充电套餐';

-- customer：客户(部署单位)
CREATE TABLE `customer` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务名称',
  `status` enum('active','suspended') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active' COMMENT '当前业务状态；取值 active / suspended',
  `contact_phone` varchar(32) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '联系电话',
  `contact_email` varchar(128) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '联系邮箱',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='客户(部署单位)';

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
  `install_at` datetime(3) DEFAULT NULL COMMENT '设备安装时间',
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
CREATE TABLE `event_outbox` (
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

-- invoice_review：发票审核
CREATE TABLE `invoice_review` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `invoice_request_id` bigint unsigned NOT NULL COMMENT '关联 user_db.invoice_request.id',
  `review_status` enum('pending','awaiting_second','approved','rejected') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '审核状态；取值 pending / awaiting_second / approved / rejected',
  `first_reviewer_id` bigint unsigned DEFAULT NULL COMMENT '一级审核管理员 ID',
  `first_reviewed_at` datetime(3) DEFAULT NULL COMMENT '一级审核时间',
  `second_reviewer_id` bigint unsigned DEFAULT NULL COMMENT '二级审核管理员 ID',
  `second_reviewed_at` datetime(3) DEFAULT NULL COMMENT '二级审核时间',
  `reviewed_by` bigint unsigned DEFAULT NULL COMMENT '审核人 ID',
  `reviewed_at` datetime(3) DEFAULT NULL COMMENT '审核完成时间',
  `reject_reason` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '拒绝申请的原因',
  `invoice_url` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '发票文件访问 URL',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_invoice_review_request` (`invoice_request_id`),
  KEY `idx_status` (`review_status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='发票审核';

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

-- pricing_package_template：充电套餐模板
CREATE TABLE `pricing_package_template` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `name` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务名称',
  `kind` enum('amount','package') COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'amount=预付封顶金额；package=封顶金额+时长',
  `price_cents` bigint NOT NULL DEFAULT '0' COMMENT 'amount 的封顶金额；package 为 0，按费率结算，单位分',
  `duration_minutes` smallint unsigned NOT NULL DEFAULT '0' COMMENT '套餐充电时长，单位分钟',
  `stop_when_full` tinyint(1) NOT NULL DEFAULT '0' COMMENT '充满自停；固件参数表中的同名开关',
  `min_charge_cents` bigint NOT NULL DEFAULT '0' COMMENT '该套餐自身的最低扣费，与模板级电费最低消费是两件事，单位分',
  `show_remark` tinyint(1) NOT NULL DEFAULT '0' COMMENT '用户端是否展示套餐备注',
  `card_default` tinyint(1) NOT NULL DEFAULT '0' COMMENT '刷卡时的默认套餐',
  `sort_order` int NOT NULL DEFAULT '0' COMMENT '显示排序号',
  `status` enum('active','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active' COMMENT '当前业务状态；取值 active / disabled',
  `version` int unsigned NOT NULL DEFAULT '1' COMMENT '套餐模板版本号',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  PRIMARY KEY (`id`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='充电套餐模板';

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

-- pricing_switch_task：设备计费方式切换任务
CREATE TABLE `pricing_switch_task` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `task_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '后台任务编号',
  `station_id` bigint unsigned NOT NULL COMMENT '充电站点 ID',
  `template_id` bigint unsigned NOT NULL COMMENT '计费模板 ID',
  `mode_before` varchar(32) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '切换前计费方式，NULL=该设备此前无独立规则',
  `mode_after` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '切换后的计费方式标识',
  `device_count` int unsigned NOT NULL DEFAULT '0' COMMENT '任务涉及的设备数量',
  `status` enum('pending','running','completed','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '当前业务状态；取值 pending / running / completed / failed',
  `requested_by` bigint unsigned NOT NULL COMMENT '任务发起管理员 ID',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `completed_at` datetime(3) DEFAULT NULL COMMENT '任务完成时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_task_no` (`task_no`),
  KEY `idx_station` (`station_id`,`created_at`),
  KEY `idx_status` (`status`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备计费方式切换任务';

-- pricing_switch_task_item：切换任务设备明细
CREATE TABLE `pricing_switch_task_item` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `task_id` bigint unsigned NOT NULL COMMENT '计费模式切换任务 ID',
  `device_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '设备全局唯一编号',
  `mode_before` varchar(32) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '切换前的计费方式标识',
  `mode_after` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '切换后的计费方式标识',
  `status` enum('pending','running','succeeded','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '当前业务状态；取值 pending / running / succeeded / failed',
  `offered_snapshot` json DEFAULT NULL COMMENT '下发后的套餐内容快照，如 0.1元/20分钟、2元/1分钟',
  `error_msg` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '执行错误信息',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `completed_at` datetime(3) DEFAULT NULL COMMENT '任务完成时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_task_device` (`task_id`,`device_id`),
  KEY `idx_status` (`status`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='切换任务设备明细';

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

-- refund_task：退款渠道查询及结果回报任务
CREATE TABLE `refund_task` (
  `refund_no` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '退款业务编号',
  `event_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '事件唯一标识，用于追踪和幂等处理',
  `stage` enum('queued','querying','reporting','done','manual_review') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'queued' COMMENT '退款处理阶段；取值 queued / querying / reporting / done / manual_review',
  `request_json` json DEFAULT NULL COMMENT '业务请求参数快照 JSON',
  `result_json` json DEFAULT NULL COMMENT '业务执行结果 JSON',
  `attempts` int unsigned NOT NULL DEFAULT '0' COMMENT '已尝试执行次数',
  `last_error` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '最近一次执行错误信息',
  `scheduled_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '计划执行时间',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  PRIMARY KEY (`refund_no`),
  KEY `idx_due` (`stage`,`scheduled_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='退款渠道查询及结果回报任务';

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

-- risk_config：风控配置
CREATE TABLE `risk_config` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `key` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '风控配置项唯一键',
  `value` json NOT NULL COMMENT '当前观测值或配置值',
  `description` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '业务说明',
  `updated_by` bigint unsigned DEFAULT NULL COMMENT '最后修改人 ID',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_key` (`key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='风控配置';

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

-- settled_record：分账出账
CREATE TABLE `settled_record` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `settlement_no` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '分账结算编号',
  `split_template_id` bigint unsigned NOT NULL COMMENT '分账模板 ID',
  `period_start` date NOT NULL COMMENT '结算周期开始时间',
  `period_end` date NOT NULL COMMENT '结算周期结束时间',
  `total_cents` bigint NOT NULL COMMENT '总金额，单位分',
  `status` enum('draft','confirmed','paid','disputed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'draft' COMMENT '当前业务状态；取值 draft / confirmed / paid / disputed',
  `reviewed_by` bigint unsigned DEFAULT NULL COMMENT '审核人 ID',
  `reviewed_at` datetime(3) DEFAULT NULL COMMENT '审核完成时间',
  `paid_at` datetime(3) DEFAULT NULL COMMENT '支付完成时间',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_no` (`settlement_no`),
  KEY `idx_period` (`period_start`,`period_end`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='分账出账';

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

-- station_policy：场地充值与退款策略
CREATE TABLE `station_policy` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `station_id` bigint unsigned NOT NULL COMMENT '充电站点 ID',
  `force_recharge` tinyint(1) NOT NULL DEFAULT '0' COMMENT '余额低于门槛时强制充值',
  `min_balance_cents` bigint NOT NULL DEFAULT '0' COMMENT '最低钱包余额，单位分',
  `scan_refund_path` enum('balance','original') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'balance' COMMENT '扫码退款的退费路径',
  `scan_refund_rule` enum('none','realtime','time_limited') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'none' COMMENT '扫码退费的退费规则',
  `card_refund_path` enum('balance','original') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'balance' COMMENT '刷卡充电的退款去向；取值 balance / original',
  `card_refund_rule` enum('none','realtime','time_limited_prorated') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'none' COMMENT '刷卡充电的退款计算规则；取值 none / realtime / time_limited_prorated',
  `timeout_start_refund` tinyint(1) NOT NULL DEFAULT '0' COMMENT '启动结果不确定时是否直接退款',
  `verify_phone_before_charge` tinyint(1) NOT NULL DEFAULT '0' COMMENT '下单前是否要求验证手机号，0 否、1 是',
  `version` int unsigned NOT NULL DEFAULT '1' COMMENT '业务版本号',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_station` (`station_id`,`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='场地充值与退款策略';

-- station_recharge_package：场地充值套餐
CREATE TABLE `station_recharge_package` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `station_id` bigint unsigned NOT NULL COMMENT '充电站点 ID',
  `name` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '后台限长 10 字，留出展示余量',
  `amount_cents` bigint NOT NULL COMMENT '金额，单位分',
  `bonus_cents` bigint NOT NULL DEFAULT '0' COMMENT '充值赠送金额，单位分',
  `sort_order` int NOT NULL DEFAULT '0' COMMENT '显示排序号',
  `status` enum('active','disabled') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active' COMMENT '当前业务状态；取值 active / disabled',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '软删除时间',
  PRIMARY KEY (`id`),
  KEY `idx_station` (`station_id`,`status`,`sort_order`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='场地充值套餐';

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
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (1,'device.operate','编辑资料及启停设备','device','编辑设备型号、序列号、安装时间、保修截止时间和标签，或切换运营状态；不停止已有订单','2026-09-30 19:22:11.260');
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
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (14,'feedback.read','查看用户评价与投诉','device','查看用户提交的评价、投诉和建议','2026-09-30 19:22:11.718');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (15,'feedback.reply','回复与关闭用户反馈','device','回复或关闭用户反馈','2026-09-30 19:22:11.718');
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
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (44,'alert.risk_config.update','修改风控配置','alert','修改风控阈值配置','2026-09-30 19:22:11.768');
INSERT INTO `permission` (`id`,`code`,`name`,`module`,`description`,`created_at`) VALUES (45,'membership.create','创建会员卡模板','membership','创建会员卡模板','2026-09-30 19:22:11.768');
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
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,44);
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (1,45);
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
INSERT INTO `role_permission` (`role_id`,`permission_id`) VALUES (2,44);
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
DROP TABLE IF EXISTS `invoice_review`;
DROP TABLE IF EXISTS `finance_reconcile_log`;
DROP TABLE IF EXISTS `export_task`;
DROP TABLE IF EXISTS `event_outbox`;
DROP TABLE IF EXISTS `device_meta`;
DROP TABLE IF EXISTS `device_import_identity`;
DROP TABLE IF EXISTS `device_import`;
DROP TABLE IF EXISTS `customer`;
DROP TABLE IF EXISTS `charge_offer`;
DROP TABLE IF EXISTS `audit_log`;
DROP TABLE IF EXISTS `announcement`;
DROP TABLE IF EXISTS `alert_event`;
DROP TABLE IF EXISTS `admin_user_role`;
DROP TABLE IF EXISTS `admin_field_mask`;
DROP TABLE IF EXISTS `admin_data_scope`;
DROP TABLE IF EXISTS `admin_coupon`;
