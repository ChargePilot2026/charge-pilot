-- Persist the command identity so late ACKs cannot reverse an earlier result.
CREATE TABLE IF NOT EXISTS charge_start_receipt (
  charge_order_id BIGINT UNSIGNED NOT NULL,
  command_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  success BOOLEAN NOT NULL,
  port_id BIGINT UNSIGNED DEFAULT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (charge_order_id),
  UNIQUE KEY uk_command (command_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
