-- Global order idempotency independent of monthly fee_calculation partitions.
CREATE TABLE IF NOT EXISTS fee_receipt (
  charge_order_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
  source_json JSON NOT NULL,
  calculation_id BIGINT UNSIGNED NULL,
  calculation_no VARCHAR(64) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
