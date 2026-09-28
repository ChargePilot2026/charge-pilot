-- +goose NO TRANSACTION
-- +goose Up
ALTER TABLE refund_record
 ADD COLUMN execution_policy ENUM('manual_review','automatic') NOT NULL DEFAULT 'manual_review',
 ADD COLUMN next_attempt_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 ADD KEY idx_refund_dispatch (execution_policy,status,next_attempt_at);
-- Historical requests require review; only explicitly authorized new requests run.
