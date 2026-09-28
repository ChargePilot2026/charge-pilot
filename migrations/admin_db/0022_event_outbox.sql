-- D5 / D4:admin 服务补建事件 outbox
--
-- 背景:
--   - `admin_db` 原先**没有** outbox 表,admin 产出的 `pricing_rule_changed` /
--     `webhook_retry` 是直接 `xadd`,载荷零持久化 → 丢失后无法重放(§五 D4)。
--   - `settings.rs:41-51` 用 `let _ =` 吞掉发布错误:DB 已提交而事件永久丢失(§五 D5)。
--
-- 表结构对齐 `user_db.event_outbox`,并补两列以支撑 D4 的重放:
--   - `stream_message_id`:XADD 返回值回写,便于把 Redis 侧缺口精确映射回本行
--     (D4 ③:无此列时,Redis 缺口无法反查,重放范围只能保守)
--   - `idx_status_sched`:支撑"未发布"范围查询
--
-- 注:MySQL 8.0.13+ 允许列 DEFAULT 表达式,8.0.13 之前与 MariaDB 均不允许。
-- 本文件曾带 `created_month VARCHAR(16) NOT NULL DEFAULT DATE_FORMAT(...,'%Y-%m')`,
-- 在 MySQL 8.4(开发栈固定版本)上直接 1064 语法错误,整条迁移链在此中断。
-- 该列自建表起就没有任何读写方(outbox 的读写只看 status/scheduled_at/stream_message_id),
-- 保留一个无人使用的月份列只为分区是伪需求,故直接删除而非改成
-- generated column —— 要分区时再随分区键一起加。

CREATE TABLE IF NOT EXISTS `event_outbox` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `event_id` VARCHAR(64) NOT NULL,
  `stream` VARCHAR(64) NOT NULL COMMENT '目标 stream 名',
  `envelope_json` JSON NOT NULL,
  `status` ENUM('pending','published','failed') NOT NULL DEFAULT 'pending',
  `stream_message_id` VARCHAR(64) DEFAULT NULL COMMENT 'XADD 返回的 stream id,用于缺口反查',
  `retry_count` INT UNSIGNED NOT NULL DEFAULT 0,
  `last_error` VARCHAR(255) DEFAULT NULL,
  `scheduled_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `published_at` DATETIME(3) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_event` (`event_id`),
  KEY `idx_status_sched` (`status`, `scheduled_at`),
  KEY `idx_stream_msgid` (`stream`, `stream_message_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='事件 outbox(可靠发布)';
