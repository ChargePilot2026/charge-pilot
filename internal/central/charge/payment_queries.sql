-- name: PaymentIntentByRequest :one
SELECT intent_id, merchant_order_no, payment_order_id, user_id, openid,
       device_id, port_no, port_code, station_id, pricing_rule_id,
       pricing_rule_version, pricing_snapshot, estimated_kwh,
       estimated_minutes, electric_cents, service_cents, total_cents,
       charge_mode, charge_quantity, status, expires_at
FROM charge_payment_intent
WHERE user_id = ? AND client_request_id = ? LIMIT 1;

-- name: ExpireStaleIntents :exec
UPDATE charge_payment_intent SET status = 'expired'
WHERE status = 'initiated' AND expires_at < NOW(3);

-- name: UserOpenID :one
SELECT openid FROM user WHERE id = ? AND status = 'active' AND deleted_at IS NULL LIMIT 1;

-- name: InsertPaymentOrderForIntent :execresult
INSERT INTO payment_order
  (order_no, biz_type, biz_id, user_id, pay_method, total_cents,
   status, expired_at, created_month)
VALUES (?, 'charge', 0, ?, 'wechat', ?, 'initiated', ?, UTC_DATE());

-- name: InsertChargePaymentIntent :exec
INSERT INTO charge_payment_intent
  (intent_id, client_request_id, merchant_order_no, payment_order_id,
   user_id, openid, device_id, port_no, port_code, station_id,
   pricing_rule_id, pricing_rule_version, pricing_snapshot,
   estimated_kwh, estimated_minutes, electric_cents, service_cents,
   total_cents, charge_mode, charge_quantity, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CAST(sqlc.arg(estimated_kwh) AS DECIMAL(9,3)),
        ?, ?, ?, ?, ?, ?, ?);
