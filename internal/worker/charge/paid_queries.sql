-- name: PaidChargeOrdersToStart :many
SELECT c.id, c.order_no
FROM charge_order AS c
JOIN charge_payment_intent AS i ON i.charge_order_id = c.id
  AND i.user_id = c.user_id AND i.status = 'paid'
JOIN payment_order AS p ON p.id = i.payment_order_id
  AND p.biz_type = 'charge' AND p.biz_id = c.id
  AND p.user_id = c.user_id AND p.status = 'paid'
  AND p.total_cents > 0 AND p.paid_cents >= p.total_cents
WHERE c.status = 'paid' AND c.deleted_at IS NULL
  AND c.id > ?
ORDER BY c.id
LIMIT 100;
