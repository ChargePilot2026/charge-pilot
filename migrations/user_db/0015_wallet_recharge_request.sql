CREATE TABLE IF NOT EXISTS wallet_recharge_request (
 request_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
 user_id BIGINT UNSIGNED NOT NULL,
 amount_cents BIGINT NOT NULL,
 payment_order_id BIGINT UNSIGNED NULL,
 request_json JSON NULL,
 prepay_id VARCHAR(255) NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
