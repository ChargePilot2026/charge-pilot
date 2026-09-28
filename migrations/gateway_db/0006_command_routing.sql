-- +goose NO TRANSACTION
-- +goose Up

-- A physical port can have only one ownership row. The old non-unique index
-- was insufficient to enforce the charge-start exclusion rule.
ALTER TABLE device_port ADD UNIQUE KEY uk_device_port (device_id, port_no);
ALTER TABLE charge_command ADD KEY idx_ack_session (device_id, port_no, session_id);
ALTER TABLE charge_command ADD KEY idx_stop_ack_session (device_id, port_no, stop_session_id);
ALTER TABLE charge_command ADD COLUMN charge_mode TINYINT UNSIGNED NOT NULL DEFAULT 4;
ALTER TABLE charge_command ADD COLUMN quantity SMALLINT UNSIGNED NOT NULL DEFAULT 1;
