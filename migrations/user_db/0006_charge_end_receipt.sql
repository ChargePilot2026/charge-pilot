CREATE TABLE IF NOT EXISTS charge_end_receipt (
  charge_order_id BIGINT UNSIGNED NOT NULL,
  stop_command_id VARCHAR(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  meter_json JSON NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY(charge_order_id),
  UNIQUE KEY uk_stop_command(stop_command_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
