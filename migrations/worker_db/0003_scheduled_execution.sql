-- +goose Up
ALTER TABLE scheduled_task
  ADD COLUMN consecutive_fail_count INT UNSIGNED NOT NULL DEFAULT 0,
  ADD COLUMN lease_token VARCHAR(36) DEFAULT NULL,
  ADD COLUMN lease_until DATETIME(3) DEFAULT NULL,
  ADD KEY idx_due (enabled, next_run_at, lease_until);

ALTER TABLE task_execution_log
  ADD COLUMN triggered_by ENUM('cron','admin_api') NOT NULL DEFAULT 'cron',
  ADD COLUMN trigger_reason VARCHAR(256) DEFAULT NULL;

INSERT INTO scheduled_task (task_code, name, cron_expr, next_run_at)
VALUES
  ('alert_evaluate', '设备告警阈值扫描', '*/10 * * * * *', UTC_TIMESTAMP(3)),
  ('webhook_dispatch', 'Webhook 待投递事件扫描', '*/10 * * * * *', UTC_TIMESTAMP(3))
ON DUPLICATE KEY UPDATE task_code = VALUES(task_code);

-- +goose Down
DELETE FROM scheduled_task WHERE task_code IN ('alert_evaluate', 'webhook_dispatch');
ALTER TABLE task_execution_log DROP COLUMN trigger_reason, DROP COLUMN triggered_by;
ALTER TABLE scheduled_task DROP KEY idx_due, DROP COLUMN lease_until, DROP COLUMN lease_token, DROP COLUMN consecutive_fail_count;
