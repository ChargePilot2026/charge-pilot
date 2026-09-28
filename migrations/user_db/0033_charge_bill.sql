-- +goose NO TRANSACTION
-- +goose Up

-- A bill is what the customer is shown after a charge settles. It is written
-- once the fee is known, so the amount a customer reads is the amount that was
-- actually calculated rather than an estimate taken at payment time.
CREATE TABLE IF NOT EXISTS charge_bill (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  bill_no VARCHAR(64) NOT NULL,
  charge_order_id BIGINT UNSIGNED NOT NULL,
  payment_order_id BIGINT UNSIGNED NULL,
  user_id BIGINT UNSIGNED NOT NULL,
  device_id VARCHAR(64) NOT NULL,
  port_no TINYINT UNSIGNED NOT NULL DEFAULT 0,
  electric_cents BIGINT NOT NULL DEFAULT 0,
  service_cents BIGINT NOT NULL DEFAULT 0,
  total_cents BIGINT NOT NULL DEFAULT 0,
  prepaid_cents BIGINT NOT NULL DEFAULT 0,
  refund_cents BIGINT NOT NULL DEFAULT 0,
  shortfall_cents BIGINT NOT NULL DEFAULT 0,
  charged_kwh DECIMAL(12, 4) NULL,
  charged_seconds INT UNSIGNED NULL,
  status ENUM('issued','settled','voided') NOT NULL DEFAULT 'issued',
  issued_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  settled_at DATETIME(3) NULL,
  created_month DATE NOT NULL,
  PRIMARY KEY (id, created_month),
  -- One bill per charge: a replayed billing dispatch must not bill twice.
  UNIQUE KEY uk_order (charge_order_id, created_month),
  UNIQUE KEY uk_bill_no (bill_no, created_month),
  KEY idx_user (user_id, issued_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='充电账单';

-- Read state is per customer and must not require a client-side store that a
-- reinstall would lose.
CREATE TABLE IF NOT EXISTS charge_bill_read (
  bill_id BIGINT UNSIGNED NOT NULL,
  created_month DATE NOT NULL,
  user_id BIGINT UNSIGNED NOT NULL,
  read_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (bill_id, created_month),
  KEY idx_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='账单已读标记';
