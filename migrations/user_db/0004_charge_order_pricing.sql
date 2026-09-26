CREATE TABLE IF NOT EXISTS charge_order_pricing (
  charge_order_id BIGINT UNSIGNED NOT NULL,
  quote_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  user_id BIGINT UNSIGNED NOT NULL,
  port_code VARCHAR(64) NOT NULL,
  quote_snapshot JSON NOT NULL,
  confirmed_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (charge_order_id),
  UNIQUE KEY uk_quote_id (quote_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
