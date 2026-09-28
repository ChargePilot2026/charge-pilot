-- name: PaidStartAuthorization :one
SELECT c.id AS charge_order_id, c.order_no, c.user_id, c.device_id,
       c.port_no, p.id AS payment_order_id, c.charge_mode, c.charge_quantity
FROM charge_order AS c
JOIN payment_order AS p ON p.id = c.payment_order_id
  AND p.biz_type = 'charge' AND p.biz_id = c.id AND p.user_id = c.user_id
WHERE c.order_no = ? AND c.status = 'paid' AND c.deleted_at IS NULL
  AND p.status = 'paid' AND p.paid_cents >= p.total_cents
  AND p.total_cents > 0 AND p.deleted_at IS NULL
  AND ((c.charge_mode IN (0,4,10,12) AND c.charge_quantity BETWEEN 1 AND 600)
    OR (c.charge_mode IN (1,11) AND c.charge_quantity BETWEEN 1 AND 65535))
LIMIT 1;

-- name: LockOrderForStartResult :one
SELECT id, order_no, user_id, device_id, port_no, status
FROM charge_order WHERE id = ? AND order_no = ? AND deleted_at IS NULL
LIMIT 1 FOR UPDATE;

-- name: GetStartReceipt :one
SELECT command_id, charge_order_id, order_no, device_id, port_no,
       port_id, success, result_code, occurred_at
FROM charge_start_receipt WHERE command_id = ? LIMIT 1;

-- name: InsertStartReceipt :exec
INSERT INTO charge_start_receipt
  (command_id, charge_order_id, order_no, device_id, port_no,
   port_id, success, result_code, occurred_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: MarkOrderCharging :execresult
UPDATE charge_order SET status = 'charging', started_at = ?
WHERE id = ? AND order_no = ? AND status = 'paid' AND deleted_at IS NULL;

-- name: MarkOrderRefundingAfterStartFailure :execresult
UPDATE charge_order SET status = 'refunding', failure_reason = ?
WHERE id = ? AND order_no = ? AND status = 'paid' AND deleted_at IS NULL;

-- name: InsertActivePort :exec
INSERT INTO active_port_charge
  (port_id, device_id, port_no, charge_order_id, user_id, started_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: InsertStartEvent :exec
INSERT INTO charge_event_log
  (charge_order_id, event_id, event, actor, detail, occurred_at)
VALUES (?, ?, ?, 'gateway', ?, ?);

-- name: InsertStartOutbox :exec
INSERT INTO event_outbox (event_id, stream, envelope_json)
VALUES (?, ?, ?);

-- name: LockOrderForEnd :one
SELECT id, order_no, user_id, device_id, port_no, status, started_at
FROM charge_order WHERE id = ? AND order_no = ? AND deleted_at IS NULL
LIMIT 1 FOR UPDATE;

-- name: StartReceiptByOrder :one
SELECT command_id, success, port_id FROM charge_start_receipt
WHERE charge_order_id = ? LIMIT 1;

-- name: EndReceiptByOrder :one
SELECT stop_command_id, meter_json FROM charge_end_receipt
WHERE charge_order_id = ? LIMIT 1;

-- name: InsertEndReceipt :exec
INSERT INTO charge_end_receipt (charge_order_id, stop_command_id, meter_json)
VALUES (?, ?, ?);

-- name: MarkOrderCompleted :execresult
UPDATE charge_order SET status = 'completed', ended_at = ?,
  charged_kwh = CAST(sqlc.arg(charged_kwh) AS DECIMAL(12,4)), charged_seconds = ?
WHERE id = ? AND order_no = ? AND status = 'charging' AND deleted_at IS NULL;

-- name: DeleteActivePortAfterEnd :execresult
DELETE FROM active_port_charge
WHERE port_id = ? AND device_id = ? AND port_no = ? AND charge_order_id = ?;

-- name: ActiveStopAuthorization :one
SELECT c.id AS charge_order_id, c.order_no, c.user_id, c.device_id,
       c.port_no, s.port_id, s.command_id AS start_command_id
FROM charge_order AS c
JOIN charge_start_receipt AS s ON s.charge_order_id = c.id AND s.success = TRUE
WHERE c.order_no = ? AND c.user_id = ? AND c.status = 'charging'
  AND c.deleted_at IS NULL AND s.port_id IS NOT NULL
LIMIT 1;
