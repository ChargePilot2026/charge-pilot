-- +goose Up
-- A stop command that the device refuses used to leave no trace.
--
-- The firmware answers a remote stop with 0 (no such port), 1 (port already
-- idle), 4 (port faulted) or 10 (stopped). Only 10 and 1 are treated as done;
-- the other two fell through and the command stayed in "sent" forever, with no
-- alert. On a platform-billed session that silence is the dangerous case: the
-- platform asked the charge to end and was never told whether it did.
--
-- "rejected" makes the refusal a terminal state that a query can find, and the
-- result code is stored so an operator can tell "the port is broken" from
-- "there is no such port".

ALTER TABLE charge_stop_command
  MODIFY COLUMN status ENUM('pending','sent','acked','rejected') NOT NULL DEFAULT 'pending',
  ADD COLUMN result_code TINYINT UNSIGNED DEFAULT NULL COMMENT '设备返回的停止应答码',
  ADD COLUMN rejected_at DATETIME(3) DEFAULT NULL,
  ADD KEY idx_rejected (status, rejected_at);

-- +goose Down
ALTER TABLE charge_stop_command
  DROP KEY idx_rejected,
  DROP COLUMN result_code,
  DROP COLUMN rejected_at,
  MODIFY COLUMN status ENUM('pending','sent','acked') NOT NULL DEFAULT 'pending';
