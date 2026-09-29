-- +goose Up
ALTER TABLE charge_payment_intent ADD COLUMN offer_id BIGINT UNSIGNED NULL;

-- +goose Down
ALTER TABLE charge_payment_intent DROP COLUMN offer_id;
