-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE charge_billing_job (
 charge_order_id BIGINT UNSIGNED PRIMARY KEY,
 status ENUM('pending','manual_review','done') NOT NULL DEFAULT 'pending',
 attempts INT UNSIGNED NOT NULL DEFAULT 0,
 next_attempt_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 last_error VARCHAR(255) NULL,
 updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
 KEY idx_due(status,next_attempt_at)
) ENGINE=InnoDB;
INSERT IGNORE INTO charge_billing_job(charge_order_id)
 SELECT e.charge_order_id FROM charge_end_receipt e LEFT JOIN charge_fee_receipt f ON f.charge_order_id=e.charge_order_id WHERE f.charge_order_id IS NULL;
