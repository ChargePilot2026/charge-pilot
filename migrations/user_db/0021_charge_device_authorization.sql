-- +goose NO TRANSACTION
-- +goose Up

-- A paid order may reach a device only with a persisted, immutable command
-- limit from the confirmed quote. NULL keeps legacy/incomplete orders closed.
ALTER TABLE charge_order ADD COLUMN charge_mode TINYINT UNSIGNED DEFAULT NULL;
ALTER TABLE charge_order ADD COLUMN charge_quantity SMALLINT UNSIGNED DEFAULT NULL;
