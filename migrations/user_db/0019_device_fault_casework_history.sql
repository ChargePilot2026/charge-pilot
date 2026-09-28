-- +goose NO TRANSACTION
-- +goose Up

-- Durable status and inspection notes for device fault reports.
CREATE TABLE IF NOT EXISTS device_fault_report_event (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  report_id BIGINT UNSIGNED NOT NULL,
  actor_id BIGINT UNSIGNED DEFAULT NULL,
  event_type ENUM('reported','dispatched','reassigned','fixed','closed','migration_baseline') NOT NULL,
  from_status VARCHAR(24) DEFAULT NULL,
  to_status VARCHAR(24) DEFAULT NULL,
  assigned_to BIGINT UNSIGNED DEFAULT NULL,
  note VARCHAR(2000) DEFAULT NULL,
  user_visible TINYINT(1) NOT NULL DEFAULT 1,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_fault_event_report_time (report_id, created_at, id),
  KEY idx_fault_event_actor_time (actor_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备报修状态与巡检处理记录';

-- Preserve each existing report's current state while explicitly identifying it as a migration baseline.
INSERT INTO device_fault_report_event (report_id, event_type, to_status, assigned_to, note, user_visible, created_at)
SELECT r.id, 'migration_baseline', r.status, r.assigned_to,
       '迁移前状态快照；此前的处理历史未单独记录。', 0, UTC_TIMESTAMP(3)
FROM device_fault_report r
WHERE NOT EXISTS (
  SELECT 1 FROM device_fault_report_event e WHERE e.report_id = r.id
);
