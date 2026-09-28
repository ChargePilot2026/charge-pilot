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

-- name: InsertDeviceEvent :execresult
INSERT INTO device_event (event_key, protocol_name, device_id, event_type, port_no, event_json, received_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE event_key = event_key;

-- name: InsertDeviceOutbox :exec
INSERT INTO event_outbox (event_id, stream, envelope_json)
VALUES (?, 'device_event_stream', ?)
ON DUPLICATE KEY UPDATE event_id = event_id;

-- name: InsertTelemetry :exec
INSERT INTO telemetry (device_id, port_no, metric, value_num, ts)
VALUES (?, ?, ?, CAST(sqlc.arg(value_num) AS DECIMAL(18,6)), ?);

-- name: TouchDeviceSeen :exec
UPDATE device SET last_seen_at = ? WHERE device_id = ? AND status = 'enabled' AND deleted_at IS NULL;

-- name: LockAvailablePort :one
SELECT p.id, p.port_code FROM device_port AS p
JOIN device AS d ON d.device_id = p.device_id
JOIN vendor AS v ON v.id = d.vendor_id
WHERE p.device_id = ? AND p.port_no = ? AND p.deleted_at IS NULL
  AND p.status = 'idle' AND p.current_order_id IS NULL
  AND d.status = 'enabled' AND d.deleted_at IS NULL
  AND v.status = 'enabled' AND v.deleted_at IS NULL
FOR UPDATE;

-- name: ReservePort :execresult
UPDATE device_port SET current_order_id = ?
WHERE id = ? AND status = 'idle' AND current_order_id IS NULL;

-- name: GetStartCommandByOrder :one
SELECT command_id, stop_command_id, charge_order_id, payment_order_id,
       order_no, user_id, device_id, port_no, port_code, port_id,
       status, session_id, stop_session_id, result_reported, charge_mode, quantity
FROM charge_command WHERE order_no = ? LIMIT 1;

-- name: LockStartForCompensation :one
SELECT command_id, order_no, device_id, port_no, port_id,
       stop_session_id, status
FROM charge_command WHERE order_no = ? LIMIT 1 FOR UPDATE;

-- name: InsertStartCommand :exec
INSERT INTO charge_command
  (command_id, stop_command_id, charge_order_id, payment_order_id,
   order_no, user_id, device_id, port_no, port_code, port_id,
   owns_port, status, session_id, stop_session_id, charge_mode, quantity)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, TRUE, 'pending', ?, ?, ?, ?);

-- name: MarkStartSent :execresult
UPDATE charge_command SET status = 'sent', sent_at = CURRENT_TIMESTAMP(3)
WHERE command_id = ? AND status = 'pending';

-- name: MarkStartStopping :exec
UPDATE charge_command SET status = 'stopping', error = ?
WHERE command_id = ? AND status IN ('sent','acked');

-- name: StoppingCommands :many
SELECT command_id, order_no, device_id, port_no, stop_session_id
FROM charge_command
WHERE status = 'stopping'
  AND (stop_sent_at IS NULL OR stop_sent_at < DATE_SUB(NOW(3), INTERVAL 10 SECOND))
ORDER BY updated_at LIMIT 50;

-- name: MarkStopSent :exec
UPDATE charge_command SET stop_sent_at = CURRENT_TIMESTAMP(3)
WHERE command_id = ? AND status = 'stopping';

-- name: MarkPendingStartRejected :execresult
UPDATE charge_command SET status = 'rejected', result_code = 254,
  ack_at = CURRENT_TIMESTAMP(3), error = 'start authorization revoked before send'
WHERE command_id = ? AND status = 'pending';

-- name: LockStartForStopAck :one
SELECT command_id, order_no, port_id FROM charge_command
WHERE device_id = ? AND port_no = ? AND stop_session_id = ?
  AND status = 'stopping' LIMIT 1 FOR UPDATE;

-- name: MarkStartCompensated :execresult
UPDATE charge_command SET status = 'rejected', result_code = 254,
  ack_at = ?, error = 'STOP compensation confirmed'
WHERE command_id = ? AND status = 'stopping';

-- name: ReleasePortAfterStop :execresult
UPDATE device_port SET status = 'idle', current_order_id = NULL
WHERE id = ? AND current_order_id = ? AND status IN ('idle','charging');

-- name: GetStopCommandByOrder :one
SELECT command_id, start_command_id, charge_order_id, order_no,
       user_id, device_id, port_no, port_id, status, session_id
FROM charge_stop_command WHERE order_no = ? LIMIT 1;

-- name: LockStartForUserStop :one
SELECT command_id, charge_order_id, order_no, user_id, device_id,
       port_no, port_id, status FROM charge_command
WHERE charge_order_id = ? AND order_no = ? AND user_id = ?
LIMIT 1 FOR UPDATE;

-- name: InsertUserStopCommand :exec
INSERT INTO charge_stop_command
  (command_id, start_command_id, charge_order_id, order_no,
   user_id, device_id, port_no, port_id, status, session_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?);

-- name: MarkUserStopSent :execresult
UPDATE charge_stop_command SET status = 'sent', sent_at = CURRENT_TIMESTAMP(3)
WHERE command_id = ? AND status IN ('pending','sent');

-- name: LockUserStopForAck :one
SELECT command_id, order_no, port_id, status FROM charge_stop_command
WHERE device_id = ? AND port_no = ? AND session_id = ?
LIMIT 1 FOR UPDATE;

-- name: MarkUserStopAcked :execresult
UPDATE charge_stop_command SET status = 'acked'
WHERE command_id = ? AND status = 'sent';

-- name: PendingUserStops :many
SELECT command_id, order_no, device_id, port_no, session_id
FROM charge_stop_command WHERE status = 'pending'
  OR (status = 'sent' AND sent_at < DATE_SUB(NOW(3), INTERVAL 10 SECOND))
ORDER BY created_at LIMIT 50;

-- name: LockOwnedChargingPort :one
SELECT id FROM device_port
WHERE id = ? AND device_id = ? AND port_no = ?
  AND current_order_id = ? AND status = 'charging'
LIMIT 1 FOR UPDATE;

-- name: LockStartForAck :one
SELECT command_id, order_no, port_id, status FROM charge_command
WHERE device_id = ? AND port_no = ? AND session_id = ?
LIMIT 1 FOR UPDATE;

-- name: MarkStartAcked :execresult
UPDATE charge_command SET status = 'acked', error = NULL, result_code = 0, ack_at = ?
WHERE command_id = ? AND status = 'sent';

-- name: MarkStartRejected :execresult
UPDATE charge_command SET status = 'rejected', error = ?, result_code = ?, ack_at = ?
WHERE command_id = ? AND status IN ('sent','stopping');

-- name: SetPortCharging :execresult
UPDATE device_port SET status = 'charging'
WHERE id = ? AND current_order_id = ? AND status = 'idle';

-- name: ReleaseReservedPort :execresult
UPDATE device_port SET current_order_id = NULL
WHERE id = ? AND current_order_id = ? AND status = 'idle';

-- name: ScanPortByCode :one
SELECT p.id, p.device_id, p.port_no, p.port_code, p.status,
       p.current_order_id, d.last_seen_at
FROM device_port AS p
JOIN device AS d ON d.device_id = p.device_id
JOIN vendor AS v ON v.id = d.vendor_id
WHERE p.port_code = ? AND p.deleted_at IS NULL
  AND d.status = 'enabled' AND d.deleted_at IS NULL
  AND v.status = 'enabled' AND v.deleted_at IS NULL
LIMIT 1;

-- name: ScanPortsByDevice :many
SELECT p.id, p.device_id, p.port_no, p.port_code, p.status,
       p.current_order_id, d.last_seen_at
FROM device_port AS p
JOIN device AS d ON d.device_id = p.device_id
JOIN vendor AS v ON v.id = d.vendor_id
WHERE d.device_id = ? AND p.deleted_at IS NULL
  AND d.status = 'enabled' AND d.deleted_at IS NULL
  AND v.status = 'enabled' AND v.deleted_at IS NULL
ORDER BY p.port_no;
