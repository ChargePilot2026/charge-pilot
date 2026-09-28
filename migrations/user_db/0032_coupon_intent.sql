-- +goose NO TRANSACTION
-- +goose Up

-- A coupon changes what the customer is charged, so the discount has to be part
-- of the frozen payment snapshot. Without these columns a later replay could
-- recompute a different payable amount than the one the customer confirmed.
ALTER TABLE charge_payment_intent
  ADD COLUMN coupon_grant_id BIGINT UNSIGNED NULL DEFAULT NULL,
  ADD COLUMN discount_cents BIGINT NOT NULL DEFAULT 0 AFTER total_cents;

ALTER TABLE payment_order
  ADD COLUMN discount_cents BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN coupon_grant_id BIGINT UNSIGNED NULL DEFAULT NULL;

-- Redemption must be visible next to the charge it discounted.
ALTER TABLE charge_order
  ADD COLUMN discount_cents BIGINT NOT NULL DEFAULT 0;
