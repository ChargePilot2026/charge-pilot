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

-- name: LockIntentForCallback :one
SELECT intent_id, merchant_order_no, payment_order_id, user_id,
       device_id, port_no, port_code, pricing_snapshot, total_cents,
       charge_mode, charge_quantity, status, expires_at, charge_order_id
FROM charge_payment_intent WHERE merchant_order_no = ? LIMIT 1 FOR UPDATE;

-- name: LockPaymentForCallback :one
SELECT id, order_no, biz_id, user_id, total_cents, paid_cents,
       wechat_transaction_id, status
FROM payment_order WHERE id = ? AND order_no = ?
LIMIT 1 FOR UPDATE;

-- name: CallbackDigestByTransaction :one
SELECT request_digest FROM payment_callback_idempotent
WHERE wechat_transaction_id = ? LIMIT 1;

-- name: InsertCallbackDigest :exec
INSERT INTO payment_callback_idempotent (wechat_transaction_id, request_digest)
VALUES (?, ?);

-- name: MarkPaymentPaidByCallback :execresult
UPDATE payment_order SET status = 'paid', paid_cents = ?,
  wechat_transaction_id = ?, paid_at = ?
WHERE id = ? AND order_no = ? AND status = 'initiated'
  AND total_cents = ? AND paid_cents = 0;

-- name: CreateChargeOrderFromPayment :execresult
INSERT INTO charge_order
  (order_no, user_id, device_id, port_no, port_code, payment_order_id,
   status, charge_mode, charge_quantity, created_month)
VALUES (?, ?, ?, ?, ?, ?, 'paid', ?, ?, UTC_DATE());

-- name: BindPaymentToCharge :execresult
UPDATE payment_order SET biz_id = ? WHERE id = ? AND biz_id = 0 AND status = 'paid';

-- name: MarkIntentPaid :execresult
UPDATE charge_payment_intent SET status = 'paid', paid_at = ?, charge_order_id = ?
WHERE intent_id = ? AND status = 'initiated' AND charge_order_id IS NULL;

-- name: MarkIntentRefundRequired :execresult
UPDATE charge_payment_intent SET status = 'refund_required', paid_at = ?
WHERE intent_id = ? AND status IN ('initiated','expired');

-- name: InsertChargePricingSnapshot :exec
INSERT INTO charge_order_pricing
  (charge_order_id, payment_intent_id, user_id, port_code, pricing_snapshot)
VALUES (?, ?, ?, ?, ?);

-- name: InsertPaymentChargeEvent :exec
INSERT INTO charge_event_log
  (charge_order_id, event_id, event, actor, detail, occurred_at)
VALUES (?, ?, ?, 'payment_callback', ?, ?);

-- name: InsertPaymentOutbox :exec
INSERT INTO event_outbox (event_id, stream, envelope_json)
VALUES (?, ?, ?);

-- name: InsertLatePaymentRefund :exec
INSERT INTO refund_record
  (refund_no, payment_order_id, user_id, biz_type, biz_id,
   refund_cents, reason, status, created_month)
VALUES (?, ?, ?, 'charge', 0, ?, 'payment arrived after intent expired',
        'pending', UTC_DATE());
