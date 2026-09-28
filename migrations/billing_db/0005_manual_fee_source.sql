-- +goose NO TRANSACTION
-- +goose Up
ALTER TABLE manual_fee_review
 ADD COLUMN source_json JSON NULL,
 ADD COLUMN status ENUM('pending','resolved') NOT NULL DEFAULT 'pending',
 ADD COLUMN resolved_at DATETIME(3) NULL;
