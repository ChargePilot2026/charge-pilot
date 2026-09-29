-- +goose Up
CREATE TABLE regulatory_report (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  event_id CHAR(36) NOT NULL,
  object_type ENUM('operator','station','device','order','alert','battery') NOT NULL,
  object_key VARCHAR(128) NOT NULL,
  payload_json JSON NOT NULL,
  status ENUM('queued','processing','delivered') NOT NULL DEFAULT 'queued',
  attempts INT UNSIGNED NOT NULL DEFAULT 0,
  next_attempt_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  lease_token CHAR(36) DEFAULT NULL,
  lease_until DATETIME(3) DEFAULT NULL,
  last_error VARCHAR(512) DEFAULT NULL,
  delivered_at DATETIME(3) DEFAULT NULL,
  delivered_mode ENUM('simulation','http') DEFAULT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uk_event (event_id),
  KEY idx_due (status,next_attempt_at,lease_until),
  KEY idx_object (object_type,object_key,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='监管报送持久队列';

-- +goose Down
DROP TABLE regulatory_report;
