CREATE TABLE IF NOT EXISTS wallet_refund_request (
  request_id VARCHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  user_id BIGINT UNSIGNED NOT NULL,
  amount_cents BIGINT NOT NULL,
  reason VARCHAR(255) NULL,
  response_json JSON NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  KEY idx_user_time(user_id,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE IF NOT EXISTS wallet_refund_part (
  refund_record_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
  request_id VARCHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  wallet_account_id BIGINT UNSIGNED NOT NULL,
  amount_cents BIGINT NOT NULL,
  settled TINYINT(1) NOT NULL DEFAULT 0
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
