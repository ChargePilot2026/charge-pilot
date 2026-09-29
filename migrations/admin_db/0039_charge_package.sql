-- +goose Up
-- A charge package is a reusable template. It is not sellable on its own: an
-- operator applies it to a station, which is what creates a charge_offer.
CREATE TABLE charge_package (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  code VARCHAR(64) NOT NULL,
  name VARCHAR(128) NOT NULL,
  mode ENUM('amount','package') NOT NULL,
  price_cents BIGINT NOT NULL,
  duration_minutes SMALLINT UNSIGNED NOT NULL DEFAULT 0,
  status ENUM('active','disabled') NOT NULL DEFAULT 'active',
  version INT UNSIGNED NOT NULL DEFAULT 1,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  UNIQUE KEY uk_code (code),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='充电套餐模板';

-- charge_offer stays the sellable, station-scoped row that the mini program
-- reads. It keeps its own copy of the price and duration, so a later package
-- edit cannot retroactively change what a station sells or what a paid order
-- was settled against.
ALTER TABLE charge_offer
  ADD COLUMN package_id BIGINT UNSIGNED DEFAULT NULL AFTER station_id,
  ADD KEY idx_package (package_id);

-- One package may be applied to a station only once. Existing rows keep a null
-- package_id, so this covers applied packages without disturbing them.
ALTER TABLE charge_offer
  ADD UNIQUE KEY uk_package_station (package_id, station_id);

-- +goose Down
ALTER TABLE charge_offer DROP INDEX uk_package_station, DROP INDEX idx_package, DROP COLUMN package_id;
DROP TABLE charge_package;
