-- name: GetEnabledDevice :one
SELECT d.id FROM device AS d
JOIN vendor AS v ON v.id = d.vendor_id
WHERE d.device_id = ? AND d.status = 'enabled' AND d.deleted_at IS NULL
  AND v.vendor_code = ? AND v.status = 'enabled' AND v.deleted_at IS NULL
LIMIT 1;

-- name: TouchDevice :exec
UPDATE device
SET firmware_version = ?, registered_at = COALESCE(registered_at, ?), last_seen_at = ?
WHERE id = ?;

-- name: InsertDeviceEvent :exec
INSERT INTO device_event (event_key, protocol_name, device_id, event_type, port_no, event_json, received_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE event_key = event_key;

-- name: InsertDeviceOutbox :exec
INSERT INTO event_outbox (event_id, stream, envelope_json)
VALUES (?, 'device_event_stream', ?)
ON DUPLICATE KEY UPDATE event_id = event_id;
