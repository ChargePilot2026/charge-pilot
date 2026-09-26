-- A non-null primary key serializes competing imports of the same device.
-- Reservation and device/port creation commit or roll back together.
CREATE TABLE IF NOT EXISTS device_provision (
  device_id VARCHAR(64) NOT NULL,
  request_json JSON NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (device_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
