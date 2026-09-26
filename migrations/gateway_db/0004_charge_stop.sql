CREATE TABLE IF NOT EXISTS charge_stop_command (
  command_id VARCHAR(36) NOT NULL,
  start_command_id VARCHAR(36) NOT NULL,
  charge_order_id BIGINT UNSIGNED NOT NULL,
  order_no VARCHAR(64) NOT NULL,
  user_id BIGINT UNSIGNED NOT NULL,
  device_id VARCHAR(64) NOT NULL,
  port_no TINYINT UNSIGNED NOT NULL,
  port_id BIGINT UNSIGNED NOT NULL,
  status ENUM('pending','sent','acked') NOT NULL DEFAULT 'pending',
  session_id VARCHAR(36) DEFAULT NULL,
  sent_at DATETIME(3) DEFAULT NULL,
  meter_json JSON DEFAULT NULL,
  result_reported BOOLEAN NOT NULL DEFAULT FALSE,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY(command_id),
  UNIQUE KEY uk_order(charge_order_id),
  KEY idx_unreported(result_reported,charge_order_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS event_outbox (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  event_id VARCHAR(64) NOT NULL,
  stream VARCHAR(64) NOT NULL,
  envelope_json JSON NOT NULL,
  status ENUM('pending','published','failed') NOT NULL DEFAULT 'pending',
  retry_count INT UNSIGNED NOT NULL DEFAULT 0,
  last_error VARCHAR(255) DEFAULT NULL,
  scheduled_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  published_at DATETIME(3) DEFAULT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY(id),
  UNIQUE KEY uk_event(event_id),
  KEY idx_status_sched(status,scheduled_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
