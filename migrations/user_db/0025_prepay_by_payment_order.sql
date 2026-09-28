-- +goose NO TRANSACTION
-- +goose Up

-- Prepay happens before any charge_order exists. Keep the old test table but
-- key it by the payment order instead of a future charge order.
ALTER TABLE charge_prepay
  DROP PRIMARY KEY,
  CHANGE COLUMN charge_order_id payment_order_id BIGINT UNSIGNED NOT NULL,
  CHANGE COLUMN request_json params_json JSON NOT NULL,
  ADD PRIMARY KEY (payment_order_id);
