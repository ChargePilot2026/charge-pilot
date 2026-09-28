-- +goose Up
CREATE TABLE charge_meter_review (
 id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
 request_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 charge_order_id BIGINT UNSIGNED NOT NULL,
 original_json JSON NOT NULL,
 corrected_json JSON NOT NULL,
 reason VARCHAR(500) NOT NULL,
 status ENUM('awaiting_second','approved','rejected') NOT NULL DEFAULT 'awaiting_second',
 first_reviewer_id BIGINT UNSIGNED NOT NULL,
 second_reviewer_id BIGINT UNSIGNED NULL,
 reject_reason VARCHAR(500) NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 reviewed_at DATETIME(3) NULL,
 UNIQUE KEY uk_request(request_id),
 KEY idx_order(charge_order_id,id),
 CHECK(second_reviewer_id IS NULL OR second_reviewer_id<>first_reviewer_id)
) ENGINE=InnoDB;
