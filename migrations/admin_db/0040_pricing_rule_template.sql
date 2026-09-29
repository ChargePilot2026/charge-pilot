-- +goose Up
-- A pricing rule template is a reusable tariff. It is not priced against
-- anything until it is applied to a station, exactly like a charge package.
CREATE TABLE pricing_rule_template (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  name VARCHAR(128) NOT NULL,
  mode ENUM('kwh','minute','mixed') NOT NULL DEFAULT 'kwh',
  time_of_use_json JSON NOT NULL,
  service_fee_cents_per_kwh BIGINT NOT NULL DEFAULT 0,
  service_fee_cents_per_min BIGINT NOT NULL DEFAULT 0,
  min_charge_cents BIGINT NOT NULL DEFAULT 0,
  version INT UNSIGNED NOT NULL DEFAULT 1,
  status ENUM('active','disabled') NOT NULL DEFAULT 'active',
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  deleted_at DATETIME(3) DEFAULT NULL,
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='计费规则模板';

-- pricing_rule stays the station-scoped, versioned row that billing reads.
-- It keeps its own copy of the tariff, so a later template edit cannot change
-- what a station is already charging or what a settled order used.
--
-- There is deliberately no unique key on (template_id, station_id): a station
-- accumulates history rows the same way it always has, one per version, with
-- the superseded ones disabled. "Already applied" is therefore a check on the
-- active row, not a constraint.
ALTER TABLE pricing_rule
  ADD COLUMN template_id BIGINT UNSIGNED DEFAULT NULL AFTER id,
  ADD KEY idx_template_station (template_id, station_id, status);

-- +goose Down
ALTER TABLE pricing_rule DROP INDEX idx_template_station, DROP COLUMN template_id;
DROP TABLE pricing_rule_template;
