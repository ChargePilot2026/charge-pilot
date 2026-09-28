-- +goose NO TRANSACTION
-- +goose Up

-- A charge whose actual fee exceeds the prepayment leaves a shortfall. The debt
-- needs its own lifecycle so an operator can see what is owed, who owes it, and
-- whether it has been collected, instead of only a column on the fee receipt.
CREATE TABLE IF NOT EXISTS charge_debt (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  debt_no VARCHAR(64) NOT NULL,
  charge_order_id BIGINT UNSIGNED NOT NULL,
  payment_order_id BIGINT UNSIGNED NOT NULL,
  user_id BIGINT UNSIGNED NOT NULL,
  debt_cents BIGINT NOT NULL,
  paid_cents BIGINT NOT NULL DEFAULT 0,
  status ENUM('unpaid','partial','settled','waived') NOT NULL DEFAULT 'unpaid',
  last_reminder_at DATETIME(3) DEFAULT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uk_debt_order (charge_order_id),
  UNIQUE KEY uk_debt_no (debt_no),
  KEY idx_user_status (user_id, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='充电欠费';

-- One settlement path per debt payment so a retried prepay cannot collect twice.
CREATE TABLE IF NOT EXISTS charge_debt_receipt (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  debt_id BIGINT UNSIGNED NOT NULL,
  payment_order_id BIGINT UNSIGNED NOT NULL,
  paid_cents BIGINT NOT NULL,
  channel_ref VARCHAR(64) NOT NULL,
  received_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uk_payment (payment_order_id),
  UNIQUE KEY uk_channel (channel_ref),
  KEY idx_debt (debt_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='欠费补缴入账回执';

-- Coupons are redeemed at payment time. The redemption record is separate from
-- the template so a coupon can be granted many times but used exactly once.
CREATE TABLE IF NOT EXISTS coupon_redemption (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  coupon_code VARCHAR(64) NOT NULL,
  user_id BIGINT UNSIGNED NOT NULL,
  biz_type VARCHAR(32) NOT NULL,
  biz_id BIGINT UNSIGNED NOT NULL,
  discount_cents BIGINT NOT NULL,
  order_no VARCHAR(64) NULL,
  redeemed_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uk_redemption (coupon_code),
  KEY idx_biz (biz_type, biz_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='优惠券核销记录';
