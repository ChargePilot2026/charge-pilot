-- +goose NO TRANSACTION
-- +goose Up

ALTER TABLE charge_payment_intent
  ADD COLUMN charge_order_id BIGINT UNSIGNED DEFAULT NULL,
  ADD UNIQUE KEY uk_charge_order (charge_order_id);

-- These columns predate the decision to create charge orders only on payment
-- callbacks. Their data is only test data and can be migrated in place.
ALTER TABLE charge_order_pricing
  CHANGE COLUMN quote_id payment_intent_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  CHANGE COLUMN quote_snapshot pricing_snapshot JSON NOT NULL,
  RENAME INDEX uk_quote_id TO uk_payment_intent;
