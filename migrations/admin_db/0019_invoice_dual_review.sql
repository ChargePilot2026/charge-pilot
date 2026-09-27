ALTER TABLE invoice_review
  MODIFY COLUMN review_status ENUM('pending','awaiting_second','approved','rejected') NOT NULL DEFAULT 'pending',
  ADD COLUMN first_reviewer_id BIGINT UNSIGNED DEFAULT NULL AFTER review_status,
  ADD COLUMN first_reviewed_at DATETIME(3) DEFAULT NULL AFTER first_reviewer_id,
  ADD COLUMN second_reviewer_id BIGINT UNSIGNED DEFAULT NULL AFTER first_reviewed_at,
  ADD COLUMN second_reviewed_at DATETIME(3) DEFAULT NULL AFTER second_reviewer_id,
  ADD COLUMN invoice_url VARCHAR(512) DEFAULT NULL AFTER reject_reason;
