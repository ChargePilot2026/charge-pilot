-- +goose Up
ALTER TABLE charge_command
 ADD COLUMN consumer_type TINYINT UNSIGNED NOT NULL DEFAULT 2,
 ADD COLUMN card_number INT UNSIGNED NOT NULL DEFAULT 0,
 ADD COLUMN card_balance_units SMALLINT UNSIGNED NOT NULL DEFAULT 0;
CREATE TABLE card_event_delivery (
 event_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 response_json JSON DEFAULT NULL,
 status ENUM('pending','done') NOT NULL DEFAULT 'pending',
 attempts INT UNSIGNED NOT NULL DEFAULT 0,
 next_attempt_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 last_error VARCHAR(255) DEFAULT NULL,
 KEY idx_due(status,next_attempt_at)
) ENGINE=InnoDB;

-- +goose Down
DROP TABLE card_event_delivery;
ALTER TABLE charge_command DROP COLUMN consumer_type, DROP COLUMN card_number, DROP COLUMN card_balance_units;
