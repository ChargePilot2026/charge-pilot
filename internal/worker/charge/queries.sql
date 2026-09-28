-- name: UnreportedStartResults :many
SELECT command_id, charge_order_id, order_no, device_id, port_no,
       port_id, status, result_code, ack_at
FROM charge_command
WHERE status IN ('acked','rejected') AND result_reported = FALSE
  AND result_code IS NOT NULL AND ack_at IS NOT NULL
  AND (sqlc.arg(command_filter) = '' OR command_id = sqlc.arg(command_filter))
ORDER BY updated_at LIMIT 50;

-- name: MarkStartResultReported :exec
UPDATE charge_command SET result_reported = TRUE
WHERE command_id = ? AND status IN ('acked','rejected')
  AND result_reported = FALSE;

-- name: PendingChargeEnds :many
SELECT id, event_json FROM device_event
WHERE event_type = 'charge_end' AND processed_at IS NULL
  AND JSON_EXTRACT(event_json, '$.ConsumerType') = 2
ORDER BY id LIMIT 50;

-- name: StartCommandForEnd :one
SELECT command_id, stop_command_id, charge_order_id, order_no,
       device_id, port_no, port_id, status
FROM charge_command WHERE charge_order_id = ? LIMIT 1;

-- name: ReleasePortAfterFinalizedEnd :execresult
UPDATE device_port SET status = 'idle', current_order_id = NULL
WHERE id = ? AND current_order_id = ? AND status = 'charging';

-- name: PortEndState :one
SELECT status, current_order_id FROM device_port WHERE id = ? LIMIT 1;

-- name: MarkChargeEndProcessed :exec
UPDATE device_event SET processed_at = CURRENT_TIMESTAMP(3)
WHERE id = ? AND processed_at IS NULL;
