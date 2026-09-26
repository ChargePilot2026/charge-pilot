CREATE TABLE IF NOT EXISTS refund_review (
  refund_record_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
  snapshot_json JSON NOT NULL,
  first_signer BIGINT UNSIGNED NOT NULL,
  first_comment VARCHAR(255) NOT NULL,
  second_signer BIGINT UNSIGNED NULL,
  second_comment VARCHAR(255) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  approved_at DATETIME(3) NULL,
  CHECK (second_signer IS NULL OR second_signer <> first_signer)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
