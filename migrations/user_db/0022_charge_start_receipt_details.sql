-- +goose NO TRANSACTION
-- +goose Up

-- Extend the existing per-order receipt instead of creating a second table.
ALTER TABLE charge_start_receipt ADD COLUMN order_no VARCHAR(64) DEFAULT NULL;
ALTER TABLE charge_start_receipt ADD COLUMN device_id VARCHAR(64) DEFAULT NULL;
ALTER TABLE charge_start_receipt ADD COLUMN port_no TINYINT UNSIGNED DEFAULT NULL;
ALTER TABLE charge_start_receipt ADD COLUMN result_code TINYINT UNSIGNED DEFAULT NULL;
ALTER TABLE charge_start_receipt ADD COLUMN occurred_at DATETIME(3) DEFAULT NULL;
