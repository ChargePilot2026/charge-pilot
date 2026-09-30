-- +goose NO TRANSACTION
-- +goose Up
ALTER TABLE device ADD COLUMN last_heartbeat_at DATETIME(3) DEFAULT NULL AFTER last_seen_at;
-- Preserve only actual heartbeat evidence; registration timestamps are not heartbeats.
UPDATE device d JOIN (SELECT device_id, MAX(received_at) AS heartbeat_at FROM device_event WHERE event_type='heartbeat' GROUP BY device_id) e ON e.device_id=d.device_id SET d.last_heartbeat_at=e.heartbeat_at;

-- +goose Down
ALTER TABLE device DROP COLUMN last_heartbeat_at;
