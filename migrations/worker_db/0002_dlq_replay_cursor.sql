-- +goose NO TRANSACTION
-- +goose Up

-- D21:DLQ 重放滑动游标
--
-- 背景:
--   `worker/src/services/retry.rs` 的 `replay_once` 每轮用
--   `XRANGE <dlq> - + COUNT <batch>` 取**最早**的一批。若 DLQ 的增长速率
--   高于每轮清理速率(默认 86400 秒一轮),队首永远是同一批 —— 重放侧
--   永远在处理陈旧数据,新积压被饿死,且已重放的条目因 XDEL 只在成功后
--   移除,失败条目会无限占住队首窗口。
--
-- 为什么不复用 `dlq_log`:
--   该表是「DB 侧死信台账」,而当前实现里**无人写入**(真源是 Redis 的
--   `<stream>.dlq`)。把游标塞进去会让「谁在维护这一行」变得含糊:
--   Redis 是权威状态,DB 只是重放进度的记账。分表可以让游标的
--   「一行 = 一条流的水位」语义保持干净。
--
-- 游标语义:
--   `last_id` = 该流**已扫描到的最后一个 entry id**(含)。
--   下一轮用 `XRANGE <dlq> (<last_id> +` 从它之后开始 —— 增量扫描,
--   不再从头扫。首轮没有记录时 `last_id` 为空,退化为 `XRANGE - +`。
--   游标**只增不减**:重放失败不倒退水位,否则会无限重扫同一条。

CREATE TABLE IF NOT EXISTS `dlq_replay_cursor` (
  `stream` VARCHAR(64) NOT NULL COMMENT '业务流名(不含 .dlq 后缀)',
  `last_id` VARCHAR(64) DEFAULT NULL COMMENT '已扫描到的最后一个 DLQ entry id;NULL = 尚未开始',
  `scanned_total` BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '累计扫描条目数(运维观测用)',
  `replayed_total` BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '累计成功重放条目数',
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`stream`),
  KEY `idx_updated` (`updated_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='DLQ 重放滑动游标(D21)';
