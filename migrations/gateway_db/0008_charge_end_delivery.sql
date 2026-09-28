-- +goose Up
CREATE TABLE charge_end_delivery (
 device_event_id BIGINT UNSIGNED PRIMARY KEY,
 charge_order_id BIGINT UNSIGNED NOT NULL,
 payload_json JSON NOT NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 KEY idx_order(charge_order_id)
) ENGINE=InnoDB;
