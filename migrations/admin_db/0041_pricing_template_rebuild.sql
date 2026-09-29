-- +goose Up
-- Rebuilt from scratch: the project has no real operators or stations yet, so
-- the previous three-table split (pricing_rule_template + charge_package +
-- charge_offer.package_id) is replaced rather than migrated. The old model
-- kept a tariff and its charge packages on separate lifecycles, which meant an
-- operator could publish a station selling packages priced for a tariff that
-- no longer applied. A pricing template is now one object: tariff, packages
-- and display switches, applied to a station as a single copy.
--
-- The rows below are dropped, not converted. There is nothing to preserve and
-- carrying them forward would only produce tariffs the new engine cannot run.

-- Never referenced by any Go code; the name is reused below for the new model.
DROP TABLE IF EXISTS pricing_template;
DROP TABLE IF EXISTS pricing_rule_template;
DROP TABLE IF EXISTS charge_package;

DELETE FROM charge_offer;
-- The indexes on package_id must go explicitly. Dropping the column alone
-- leaves the composite indexes behind as a bare UNIQUE (station_id): MySQL
-- keeps a multi-column index and removes only the dropped column, which would
-- silently cap every station at a single offer.
ALTER TABLE charge_offer
  DROP INDEX uk_package_station,
  DROP INDEX idx_package,
  DROP COLUMN package_id,
  ADD COLUMN template_id BIGINT UNSIGNED DEFAULT NULL AFTER station_id,
  ADD COLUMN template_package_id BIGINT UNSIGNED DEFAULT NULL AFTER template_id,
  ADD KEY idx_template (template_id, template_package_id);

DELETE FROM pricing_rule;
ALTER TABLE pricing_rule
  DROP COLUMN mode,
  DROP COLUMN time_of_use_json,
  DROP COLUMN service_fee_cents_per_kwh,
  DROP COLUMN service_fee_cents_per_min,
  DROP COLUMN min_charge_cents,
  ADD COLUMN spec_json JSON NOT NULL COMMENT '完整计费口径，对应 pricing.Spec',
  ADD COLUMN channel ENUM('default','temp','card') NOT NULL DEFAULT 'default' COMMENT '计费通道，选择费率倍数';

-- A pricing template is the whole commercial offer: what is charged, on what,
-- which packages a charging user can pick, and what the mini program is allowed to show.
-- It is inert until applied to a station.
CREATE TABLE pricing_template (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  name VARCHAR(64) NOT NULL COMMENT '模板名称',
  remark VARCHAR(255) NOT NULL DEFAULT '' COMMENT '模板备注',
  spec_json JSON NOT NULL COMMENT '计费口径，对应 pricing.Spec',
  display_json JSON NOT NULL COMMENT '用户端展示开关，对应 pricing.Display',
  status ENUM('active','disabled') NOT NULL DEFAULT 'active',
  version INT UNSIGNED NOT NULL DEFAULT 1,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  deleted_at DATETIME(3) DEFAULT NULL,
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='计费模板';

-- Packages are children of a template rather than a resource of their own, so a
-- station can never sell a package that was priced against a different tariff.
-- kind reuses the settlement vocabulary: "amount" is a prepaid cap in cents,
-- "package" is a cap with a duration. A card is an amount cap that is consumed
-- independently, which the settlement path already prices the same way, so it
-- is expressed as an amount cap plus a flag rather than a third kind.
CREATE TABLE pricing_template_package (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  template_id BIGINT UNSIGNED NOT NULL,
  name VARCHAR(64) NOT NULL,
  kind ENUM('amount','package') NOT NULL,
  price_cents BIGINT NOT NULL DEFAULT 0 COMMENT 'amount 封顶金额；package 为 0，按费率结算',
  duration_minutes SMALLINT UNSIGNED NOT NULL DEFAULT 0,
  stop_when_full TINYINT(1) NOT NULL DEFAULT 0 COMMENT '充满自停',
  sort_order INT NOT NULL DEFAULT 0,
  status ENUM('active','disabled') NOT NULL DEFAULT 'active',
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  KEY idx_template (template_id, sort_order)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='计费模板下的充电套餐';

-- +goose Down
DROP TABLE IF EXISTS pricing_template_package;
DROP TABLE IF EXISTS pricing_template;
ALTER TABLE pricing_rule
  DROP COLUMN spec_json,
  DROP COLUMN channel,
  ADD COLUMN mode ENUM('kwh','minute','mixed') NOT NULL DEFAULT 'kwh',
  ADD COLUMN time_of_use_json JSON DEFAULT NULL,
  ADD COLUMN service_fee_cents_per_kwh BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN service_fee_cents_per_min BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN min_charge_cents BIGINT NOT NULL DEFAULT 0;
ALTER TABLE charge_offer
  DROP KEY idx_template,
  DROP COLUMN template_id,
  DROP COLUMN template_package_id;
