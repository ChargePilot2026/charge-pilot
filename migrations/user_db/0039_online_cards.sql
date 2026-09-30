-- +goose Up
CREATE TABLE online_card (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 card_no VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE,
 user_id BIGINT UNSIGNED NOT NULL,
 status ENUM('active','lost','disabled','unbound') NOT NULL DEFAULT 'active',
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
 KEY idx_user(user_id,status)
) ENGINE=InnoDB;
CREATE TABLE card_charge (
 charge_order_id BIGINT UNSIGNED PRIMARY KEY,
 card_id BIGINT UNSIGNED NOT NULL,
 port_code VARCHAR(64) NOT NULL,
 active_port VARCHAR(64) NULL UNIQUE,
 paid_cents BIGINT NOT NULL,
 purchased_minutes SMALLINT UNSIGNED NOT NULL,
 max_minutes SMALLINT UNSIGNED NOT NULL,
	card_no VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
	wallet_after_cents BIGINT NOT NULL,
 package_json JSON NOT NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB;
CREATE TABLE online_card_audit (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 card_id BIGINT UNSIGNED NOT NULL,
 actor_id BIGINT UNSIGNED NOT NULL,
 action VARCHAR(32) NOT NULL,
 detail VARCHAR(1000) NOT NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB;
CREATE TABLE card_operation (
 operation_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 device_id VARCHAR(64) NOT NULL,
 event_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 charge_order_id BIGINT UNSIGNED NOT NULL,
 card_id BIGINT UNSIGNED NOT NULL,
 kind ENUM('start','extend') NOT NULL,
 price_cents BIGINT NOT NULL,
 minutes SMALLINT UNSIGNED NOT NULL,
 status ENUM('confirming','confirmed','failed') NOT NULL DEFAULT 'confirming',
 failure_reason VARCHAR(255) DEFAULT NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 confirmed_at DATETIME(3) DEFAULT NULL,
 UNIQUE KEY uk_device_event(device_id,event_id),
 KEY idx_order(charge_order_id,status)
) ENGINE=InnoDB;

-- +goose Down
DROP TABLE card_operation;
DROP TABLE online_card_audit;
DROP TABLE card_charge;
DROP TABLE online_card;
