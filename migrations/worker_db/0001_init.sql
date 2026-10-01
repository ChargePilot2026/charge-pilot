-- +goose NO TRANSACTION
-- +goose Up

-- ChargePilot worker_db 初始化结构
-- 后台任务：事件消费、定时执行、重试与死信
-- 发布前基线：直接修改 CREATE TABLE，重建空库；不叠加增量 ALTER。
-- 表间关联由所属服务维护；此文件只初始化当前库。
-- 计量、事件和审计表保留月分区及 p_max，后续月份由维护任务创建。

SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci;
SET sql_mode = 'STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION,ERROR_FOR_DIVISION_BY_ZERO,NO_ZERO_DATE,NO_ZERO_IN_DATE';

-- comp_tx_log：跨服务补偿事务
CREATE TABLE `comp_tx_log` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `tx_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '补偿事务幂等标识',
  `consumer_group` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '事件消费组标识',
  `stream` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'Redis Stream 名称',
  `payload_hash` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '请求或事件载荷摘要，用于检验幂等重试内容一致性',
  `status` enum('pending','committed','compensated','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '当前业务状态；取值 pending / committed / compensated / failed',
  `retry_count` int unsigned NOT NULL DEFAULT '0' COMMENT '已执行重试次数',
  `last_error` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '最近一次执行错误信息',
  `created_month` date NOT NULL COMMENT '月分区归属日期，取对应月份第一天',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `committed_at` datetime(3) DEFAULT NULL COMMENT '补偿事务提交时间',
  PRIMARY KEY (`id`,`created_month`),
  UNIQUE KEY `uk_tx` (`tx_id`,`created_month`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='跨服务补偿事务'
/*!50100 PARTITION BY RANGE (to_days(`created_month`))
(PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB,
 PARTITION p_2026m10 VALUES LESS THAN (740286) ENGINE = InnoDB,
 PARTITION p_2026m11 VALUES LESS THAN (740316) ENGINE = InnoDB,
 PARTITION p_2026m12 VALUES LESS THAN (740347) ENGINE = InnoDB,
 PARTITION p_max VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */;

-- dlq_log：死信队列日志
CREATE TABLE `dlq_log` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `stream` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'Redis Stream 名称',
  `entry_id` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'Redis Stream 消息 ID',
  `reason` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '业务处理或异常原因',
  `payload_json` json DEFAULT NULL COMMENT '任务执行或投递载荷 JSON',
  `status` enum('open','replayed','closed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'open' COMMENT '当前业务状态；取值 open / replayed / closed',
  `resolved_by` bigint unsigned DEFAULT NULL COMMENT '处理人 ID',
  `resolved_at` datetime(3) DEFAULT NULL COMMENT '异常恢复或处理完成时间',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `created_month` date NOT NULL COMMENT '月分区归属日期，取对应月份第一天',
  PRIMARY KEY (`id`,`created_month`),
  KEY `idx_stream_status` (`stream`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='死信队列日志'
/*!50100 PARTITION BY RANGE (to_days(`created_month`))
(PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB,
 PARTITION p_2026m10 VALUES LESS THAN (740286) ENGINE = InnoDB,
 PARTITION p_2026m11 VALUES LESS THAN (740316) ENGINE = InnoDB,
 PARTITION p_2026m12 VALUES LESS THAN (740347) ENGINE = InnoDB,
 PARTITION p_max VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */;

-- dlq_replay_cursor：DLQ 重放滑动游标(D21)
CREATE TABLE `dlq_replay_cursor` (
  `stream` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务流名(不含 .dlq 后缀)',
  `last_id` varchar(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '已扫描到的最后一个 DLQ entry id;NULL = 尚未开始',
  `scanned_total` bigint unsigned NOT NULL DEFAULT '0' COMMENT '累计扫描条目数(运维观测用)',
  `replayed_total` bigint unsigned NOT NULL DEFAULT '0' COMMENT '累计成功重放条目数',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  PRIMARY KEY (`stream`),
  KEY `idx_updated` (`updated_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='DLQ 重放滑动游标(D21)';

-- retry_queue：重试队列(Webhook / 支付 / OTA)
CREATE TABLE `retry_queue` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `queue_name` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT 'webhook_retry / pay_retry / ota_retry',
  `payload_json` json NOT NULL COMMENT '任务执行或投递载荷 JSON',
  `run_at` datetime(3) NOT NULL COMMENT '重试任务计划执行时间',
  `attempt_count` int unsigned NOT NULL DEFAULT '0' COMMENT '已尝试执行次数',
  `max_attempts` int unsigned NOT NULL DEFAULT '5' COMMENT '最多允许的执行尝试次数',
  `last_error` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '最近一次执行错误信息',
  `status` enum('pending','running','done','failed') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending' COMMENT '当前业务状态；取值 pending / running / done / failed',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  PRIMARY KEY (`id`),
  KEY `idx_queue_run` (`queue_name`,`run_at`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='重试队列(Webhook / 支付 / OTA)';

-- scheduled_task：定时任务定义
CREATE TABLE `scheduled_task` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `task_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '如 alert_scan / billing_cycle_daily',
  `name` varchar(128) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '业务名称',
  `cron_expr` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '定时任务 Cron 表达式',
  `enabled` tinyint(1) NOT NULL DEFAULT '1' COMMENT '是否启用，0 否、1 是',
  `last_run_at` datetime(3) DEFAULT NULL COMMENT '任务上次执行时间',
  `next_run_at` datetime(3) DEFAULT NULL COMMENT '定时任务下次执行时间',
  `config_json` json DEFAULT NULL COMMENT '业务配置 JSON',
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '记录创建时间',
  `updated_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '记录更新时间',
  `consecutive_fail_count` int unsigned NOT NULL DEFAULT '0' COMMENT '定时任务连续执行失败次数',
  `lease_token` varchar(36) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '任务领取租约令牌，用于校验当前执行者',
  `lease_until` datetime(3) DEFAULT NULL COMMENT '任务租约到期时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_code` (`task_code`),
  KEY `idx_due` (`enabled`,`next_run_at`,`lease_until`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='定时任务定义';

-- task_execution_log：定时任务执行日志
CREATE TABLE `task_execution_log` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '记录主键 ID',
  `task_code` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '定时任务唯一业务编码',
  `started_at` datetime(3) NOT NULL COMMENT '业务启动时间',
  `finished_at` datetime(3) DEFAULT NULL COMMENT '任务执行结束时间',
  `status` enum('running','success','failed','partial') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'running' COMMENT '当前业务状态；取值 running / success / failed / partial',
  `affected_rows` bigint unsigned DEFAULT NULL COMMENT '本次任务影响的记录数',
  `error_msg` varchar(512) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '执行错误信息',
  `created_month` date NOT NULL COMMENT '月分区归属日期，取对应月份第一天',
  `triggered_by` enum('cron','admin_api') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'cron' COMMENT '定时任务触发来源；取值 cron / admin_api',
  `trigger_reason` varchar(256) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '本次任务运行的触发原因',
  PRIMARY KEY (`id`,`created_month`),
  KEY `idx_task_time` (`task_code`,`started_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='定时任务执行日志'
/*!50100 PARTITION BY RANGE (to_days(`created_month`))
(PARTITION p_init VALUES LESS THAN (740255) ENGINE = InnoDB,
 PARTITION p_2026m10 VALUES LESS THAN (740286) ENGINE = InnoDB,
 PARTITION p_2026m11 VALUES LESS THAN (740316) ENGINE = InnoDB,
 PARTITION p_2026m12 VALUES LESS THAN (740347) ENGINE = InnoDB,
 PARTITION p_max VALUES LESS THAN MAXVALUE ENGINE = InnoDB) */;

-- 初始化数据：内置权限、角色及系统默认配置。业务与演示数据另行创建。

-- scheduled_task 默认记录
INSERT INTO `scheduled_task` (`id`,`task_code`,`name`,`cron_expr`,`enabled`,`last_run_at`,`next_run_at`,`config_json`,`created_at`,`updated_at`,`consecutive_fail_count`,`lease_token`,`lease_until`) VALUES (1,'alert_evaluate','设备告警阈值扫描','*/10 * * * * *',1,NULL,'2026-09-30 19:22:13.142',NULL,'2026-09-30 19:22:13.142','2026-09-30 19:22:13.142',0,NULL,NULL);
INSERT INTO `scheduled_task` (`id`,`task_code`,`name`,`cron_expr`,`enabled`,`last_run_at`,`next_run_at`,`config_json`,`created_at`,`updated_at`,`consecutive_fail_count`,`lease_token`,`lease_until`) VALUES (2,'webhook_dispatch','Webhook 待投递事件扫描','*/10 * * * * *',1,NULL,'2026-09-30 19:22:13.142',NULL,'2026-09-30 19:22:13.142','2026-09-30 19:22:13.142',0,NULL,NULL);

-- +goose Down
-- 仅供一次性开发/测试库回退；删除当前库全部业务表。
DROP TABLE IF EXISTS `task_execution_log`;
DROP TABLE IF EXISTS `scheduled_task`;
DROP TABLE IF EXISTS `retry_queue`;
DROP TABLE IF EXISTS `dlq_replay_cursor`;
DROP TABLE IF EXISTS `dlq_log`;
DROP TABLE IF EXISTS `comp_tx_log`;
