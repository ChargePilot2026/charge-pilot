-- +goose NO TRANSACTION
-- +goose Up

-- A scan is read-only. A payment intent is created only when the user elects
-- to pay; the charge_order is created by a verified success callback later.
CREATE TABLE IF NOT EXISTS charge_payment_intent (
  intent_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  client_request_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  merchant_order_no VARCHAR(64) NOT NULL,
  payment_order_id BIGINT UNSIGNED NOT NULL,
  user_id BIGINT UNSIGNED NOT NULL,
  openid VARCHAR(64) NOT NULL,
  device_id VARCHAR(64) NOT NULL,
  port_no TINYINT UNSIGNED NOT NULL,
  port_code VARCHAR(64) NOT NULL,
  station_id BIGINT UNSIGNED NOT NULL,
  pricing_rule_id BIGINT UNSIGNED NOT NULL,
  pricing_rule_version INT UNSIGNED NOT NULL,
  pricing_snapshot JSON NOT NULL,
  estimated_kwh DECIMAL(9,3) NOT NULL,
  estimated_minutes SMALLINT UNSIGNED NOT NULL,
  electric_cents BIGINT NOT NULL,
  service_cents BIGINT NOT NULL,
  total_cents BIGINT NOT NULL,
  charge_mode TINYINT UNSIGNED NOT NULL,
  charge_quantity SMALLINT UNSIGNED NOT NULL,
  status ENUM('initiated','paid','expired','closed','refund_required') NOT NULL DEFAULT 'initiated',
  active_port_code VARCHAR(64) GENERATED ALWAYS AS (CASE WHEN status = 'initiated' THEN port_code ELSE NULL END) STORED,
  expires_at DATETIME(3) NOT NULL,
  paid_at DATETIME(3) DEFAULT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (intent_id),
  UNIQUE KEY uk_user_request (user_id, client_request_id),
  UNIQUE KEY uk_merchant_order (merchant_order_no),
  UNIQUE KEY uk_payment_order (payment_order_id),
  UNIQUE KEY uk_active_port (active_port_code),
  KEY idx_expiry (status, expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
