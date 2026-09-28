-- +goose NO TRANSACTION
-- +goose Up

-- Durable, vendor-neutral inbound events. Consumers use event_key for replay
-- safety; device acknowledgements are sent only after this row is committed.
CREATE TABLE IF NOT EXISTS device_event (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  event_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  protocol_name VARCHAR(32) NOT NULL,
  device_id VARCHAR(64) NOT NULL,
  event_type VARCHAR(32) NOT NULL,
  port_no TINYINT UNSIGNED NOT NULL DEFAULT 0,
  event_json JSON NOT NULL,
  received_at DATETIME(3) NOT NULL,
  processed_at DATETIME(3) DEFAULT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uk_event_key (event_key),
  KEY idx_unprocessed (processed_at, id),
  KEY idx_device_time (device_id, received_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
