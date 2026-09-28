-- +goose NO TRANSACTION
-- +goose Up

-- Persist idempotency for both activity events and operator-issued coupons.
ALTER TABLE coupon_grant
  ADD COLUMN source_event_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
  ADD UNIQUE KEY uk_coupon_grant_source_event (source_event_id);

CREATE TABLE IF NOT EXISTS coupon_grant_request (
  request_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  coupon_id BIGINT UNSIGNED NOT NULL,
  user_id BIGINT UNSIGNED NOT NULL,
  coupon_grant_id BIGINT UNSIGNED NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (request_id),
  UNIQUE KEY uk_coupon_grant_request_grant (coupon_grant_id),
  KEY idx_coupon_grant_request_coupon_user (coupon_id, user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='优惠券发放请求幂等回执';
