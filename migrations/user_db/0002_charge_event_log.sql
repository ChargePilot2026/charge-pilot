CREATE TABLE IF NOT EXISTS charge_event_log (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  charge_order_id BIGINT UNSIGNED NOT NULL,
  event_id VARCHAR(64) NOT NULL,
  event VARCHAR(64) NOT NULL,
  actor VARCHAR(128) NOT NULL,
  detail VARCHAR(512) NOT NULL,
  occurred_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uk_event (event_id),
  KEY idx_order_time (charge_order_id, occurred_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
