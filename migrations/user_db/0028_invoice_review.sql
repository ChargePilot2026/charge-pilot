-- +goose Up
-- Co-locate approvals with the invoice to make second approval and issuance atomic.
CREATE TABLE invoice_admin_review (
 invoice_request_id BIGINT UNSIGNED PRIMARY KEY,
 review_status ENUM('awaiting_second','approved','rejected') NOT NULL,
 first_reviewer_id BIGINT UNSIGNED NULL,
 second_reviewer_id BIGINT UNSIGNED NULL,
 invoice_url VARCHAR(512) NULL,
 reject_reason VARCHAR(255) NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
 CHECK(second_reviewer_id IS NULL OR second_reviewer_id <> first_reviewer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
