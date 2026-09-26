ALTER TABLE refund_record MODIFY COLUMN status ENUM('pending','processing','success','failed','rejected') NOT NULL DEFAULT 'pending';
CREATE TABLE IF NOT EXISTS refund_rejection (
 refund_record_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
 actor_id BIGINT UNSIGNED NOT NULL,
 reason VARCHAR(255) NOT NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
