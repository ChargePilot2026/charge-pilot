-- +goose Up
-- Rebuilt from a real commercial back office rather than from the editor.
--
-- Four structural corrections to 0041:
--
-- 1. A tariff is assigned per DEVICE, not per station. One yard routinely runs
--    a duration tariff on one pile and a peak-power tariff on the next; a
--    station-level rule cannot express that. The station rule stays as the
--    default a device inherits unless it overrides it.
-- 2. Charge packages are NOT children of the tariff template. A package is an
--    allowance settled on its own price, so it is independent of whichever
--    tariff happens to be running. Tying them together meant an operator could
--    never pair one tariff with a different set of packages.
-- 3. The spec now carries a charge mode, and that mode is the primary axis:
--    either the platform prices the session from the meter, or the device
--    spends an allowance the platform already collected. A device-billed spec
--    therefore has no rates at all, which 0041's shape could not express.
-- 4. Pushing a tariff onto a device is a job with a result, not a transaction.
--    The back office keeps a switch log with a status, an operator, the
--    before/after mode and a snapshot of what was actually sent — including
--    records that sat in progress for days.

CREATE TABLE pricing_package_template (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  name VARCHAR(64) NOT NULL,
  kind ENUM('amount','package') NOT NULL COMMENT 'amount=预付封顶金额；package=封顶金额+时长',
  price_cents BIGINT NOT NULL DEFAULT 0 COMMENT 'amount 的封顶金额；package 为 0，按费率结算',
  duration_minutes SMALLINT UNSIGNED NOT NULL DEFAULT 0,
  stop_when_full TINYINT(1) NOT NULL DEFAULT 0 COMMENT '充满自停；固件参数表中的同名开关',
  min_charge_cents BIGINT NOT NULL DEFAULT 0 COMMENT '该套餐自身的最低扣费，与模板级电费最低消费是两件事',
  show_remark TINYINT(1) NOT NULL DEFAULT 0 COMMENT '用户端是否展示套餐备注',
  card_default TINYINT(1) NOT NULL DEFAULT 0 COMMENT '刷卡时的默认套餐',
  sort_order INT NOT NULL DEFAULT 0,
  status ENUM('active','disabled') NOT NULL DEFAULT 'active',
  version INT UNSIGNED NOT NULL DEFAULT 1,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  deleted_at DATETIME(3) DEFAULT NULL,
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='充电套餐模板';

-- Whatever packages the last round had already been applied are promoted to
-- standalone templates so operators do not re-enter them. Names are suffixed
-- with their old parent so two templates that used the same package name do not
-- silently collapse into one row.
--
-- The promotion and the drop are guarded because MySQL commits DDL
-- implicitly: a migration that fails halfway leaves its earlier statements
-- applied, so on a retry the source table may already be gone. Guarding what
-- carries data is what makes a retry safe. The CREATE TABLE statements are not
-- guarded, and cannot be: a duplicate there means the version ledger itself
-- was rolled back by hand, which is a deliberate act and not a crash.
SET @has_old := (SELECT COUNT(*) FROM information_schema.tables
                 WHERE table_schema = DATABASE() AND table_name = 'pricing_template_package');
SET @promote := IF(@has_old > 0,
  'INSERT INTO pricing_package_template (name, kind, price_cents, duration_minutes, stop_when_full, sort_order)
     SELECT CONCAT(name, '' - '', template_id), kind, price_cents, duration_minutes, stop_when_full, sort_order
     FROM pricing_template_package ORDER BY template_id, sort_order',
  'DO 0');
PREPARE promote_stmt FROM @promote;
EXECUTE promote_stmt;
DEALLOCATE PREPARE promote_stmt;

DROP TABLE IF EXISTS pricing_template_package;

-- No device-assignment table is created on purpose. The device a rule belongs
-- to is already carried by pricing_rule.device_id, and that row is what
-- settlement reads; a second table naming the same assignment would be a second
-- answer to the same question, free to disagree with the one that charges
-- money. The matrix screen derives the effective tariff from the rules
-- themselves, so there is nothing left for such a table to hold.

-- Yard-level policy: recharge and refund rules that are the same for every
-- device in the yard. Kept apart from the tariff because it is an operational
-- decision about money movement, not about how a session is priced.
--
-- Refund is stored as TWO columns because it is two independent questions. The
-- card in the real back office reads `限时退款(时效外不退款)-原路退回`: when a
-- refund is allowed, and where the money lands. A single enum can only ever
-- answer one of them.
CREATE TABLE station_policy (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  station_id BIGINT UNSIGNED NOT NULL,
  force_recharge TINYINT(1) NOT NULL DEFAULT 0 COMMENT '余额低于门槛时强制充值',
  min_balance_cents BIGINT NOT NULL DEFAULT 0,
  scan_refund_path ENUM('balance','original') NOT NULL DEFAULT 'balance' COMMENT '扫码退款的退费路径',
  scan_refund_rule ENUM('none','realtime','time_limited') NOT NULL DEFAULT 'none' COMMENT '扫码退费的退费规则',
  card_refund_path ENUM('balance','original') NOT NULL DEFAULT 'balance',
  card_refund_rule ENUM('none','realtime','time_limited_prorated') NOT NULL DEFAULT 'none',
  timeout_start_refund TINYINT(1) NOT NULL DEFAULT 0 COMMENT '启动结果不确定时是否直接退款',
  verify_phone_before_charge TINYINT(1) NOT NULL DEFAULT 0,
  version INT UNSIGNED NOT NULL DEFAULT 1,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  deleted_at DATETIME(3) DEFAULT NULL,
  UNIQUE KEY uk_station (station_id, deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='场地充值与退款策略';

-- "Recharge settings" in the real back office is not a balance threshold, it is
-- a set of prepaid packages: a name, what the customer pays, and how much is
-- given on top. The giveaway is the bonus — a threshold never has any.
CREATE TABLE station_recharge_package (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  station_id BIGINT UNSIGNED NOT NULL,
  name VARCHAR(32) NOT NULL COMMENT '后台限长 10 字，留出展示余量',
  amount_cents BIGINT NOT NULL,
  bonus_cents BIGINT NOT NULL DEFAULT 0,
  sort_order INT NOT NULL DEFAULT 0,
  status ENUM('active','disabled') NOT NULL DEFAULT 'active',
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  deleted_at DATETIME(3) DEFAULT NULL,
  KEY idx_station (station_id, status, sort_order)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='场地充值套餐';

-- Whether a device may be sold a power-based tariff is a fact about its
-- protocol, not an operator's choice. A device whose frames carry no power
-- cannot be priced by power: the resulting bill could be defended to nobody.
-- These flags are filled from what the protocol adapter actually parses and
-- default to 0, because an adapter that forgot to declare its capability must
-- fail closed rather than silently unlock billing modes it cannot support.
--
-- Each column is added under its own guard, for the same reason the package
-- promotion above is: a half-applied retry has to be able to finish, and
-- MySQL has no ADD COLUMN IF NOT EXISTS.
SET @col := (SELECT IF(COUNT(*) = 0,
  'ALTER TABLE device_meta ADD COLUMN charge_mode VARCHAR(32) NOT NULL DEFAULT ''device_duration'' COMMENT ''该设备当前计费方式''',
  'DO 0') FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'device_meta' AND column_name = 'charge_mode');
PREPARE add_col FROM @col;
EXECUTE add_col;
DEALLOCATE PREPARE add_col;

SET @col := (SELECT IF(COUNT(*) = 0,
  'ALTER TABLE device_meta ADD COLUMN reports_energy TINYINT(1) NOT NULL DEFAULT 0 COMMENT ''协议帧含电量''',
  'DO 0') FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'device_meta' AND column_name = 'reports_energy');
PREPARE add_col FROM @col;
EXECUTE add_col;
DEALLOCATE PREPARE add_col;

SET @col := (SELECT IF(COUNT(*) = 0,
  'ALTER TABLE device_meta ADD COLUMN reports_segmented_power TINYINT(1) NOT NULL DEFAULT 0 COMMENT ''协议帧含分段功率''',
  'DO 0') FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'device_meta' AND column_name = 'reports_segmented_power');
PREPARE add_col FROM @col;
EXECUTE add_col;
DEALLOCATE PREPARE add_col;

-- Pushing a new tariff onto a device is a job, not a transaction. The real back
-- office keeps a switch log with a status, an operator, the before/after mode
-- and a snapshot of the packages that were actually sent, and it contains a
-- record that sat "in progress" for days. A single synchronous apply cannot
-- express a device that never acknowledged, which is precisely the case an
-- operator most needs to see.
CREATE TABLE pricing_switch_task (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  task_no VARCHAR(64) NOT NULL,
  station_id BIGINT UNSIGNED NOT NULL,
  template_id BIGINT UNSIGNED NOT NULL,
  mode_before VARCHAR(32) DEFAULT NULL COMMENT '切换前计费方式，NULL=该设备此前无独立规则',
  mode_after VARCHAR(32) NOT NULL,
  device_count INT UNSIGNED NOT NULL DEFAULT 0,
  status ENUM('pending','running','completed','failed') NOT NULL DEFAULT 'pending',
  requested_by BIGINT UNSIGNED NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  completed_at DATETIME(3) DEFAULT NULL,
  UNIQUE KEY uk_task_no (task_no),
  KEY idx_station (station_id, created_at),
  KEY idx_status (status, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='设备计费方式切换任务';

CREATE TABLE pricing_switch_task_item (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  task_id BIGINT UNSIGNED NOT NULL,
  device_id VARCHAR(64) NOT NULL,
  mode_before VARCHAR(32) DEFAULT NULL,
  mode_after VARCHAR(32) NOT NULL,
  status ENUM('pending','running','succeeded','failed') NOT NULL DEFAULT 'pending',
  offered_snapshot JSON DEFAULT NULL COMMENT '下发后的套餐内容快照，如 0.1元/20分钟、2元/1分钟',
  error_msg VARCHAR(255) DEFAULT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  completed_at DATETIME(3) DEFAULT NULL,
  UNIQUE KEY uk_task_device (task_id, device_id),
  KEY idx_status (status, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='切换任务设备明细';

-- pricing_rule gains device_id: NULL means the station-wide default, otherwise
-- it is this device's own running tariff. A device rule is a copy of the
-- template, exactly like a station rule is, so editing a template never reaches
-- back into what a device is charging.
ALTER TABLE pricing_rule
  ADD COLUMN device_id VARCHAR(64) DEFAULT NULL AFTER station_id,
  ADD KEY idx_device (device_id, status);

-- charge_offer follows the same split: a package may be sold yard-wide or only
-- on one device. template_id goes away with it: an offer points at the package
-- it sells, and a package is no longer priced against a particular tariff, so
-- keeping a tariff pointer on the offer would claim a relationship that no
-- longer exists.
ALTER TABLE charge_offer
  DROP INDEX idx_template,
  DROP COLUMN template_id,
  ADD COLUMN device_id VARCHAR(64) DEFAULT NULL AFTER station_id,
  ADD COLUMN package_template_id BIGINT UNSIGNED DEFAULT NULL,
  ADD COLUMN min_charge_cents BIGINT NOT NULL DEFAULT 0 COMMENT '该套餐自身的最低扣费',
  ADD COLUMN show_remark TINYINT(1) NOT NULL DEFAULT 0,
  ADD COLUMN card_default TINYINT(1) NOT NULL DEFAULT 0,
  ADD COLUMN stop_when_full TINYINT(1) NOT NULL DEFAULT 0 COMMENT '充满自停',
  ADD COLUMN deleted_at DATETIME(3) DEFAULT NULL COMMENT '软删；此前该表无此列，删除路径已在按它过滤',
  DROP COLUMN template_package_id,
  ADD KEY idx_device (device_id, status),
  ADD KEY idx_package_template (package_template_id, station_id, status);

-- +goose Down
ALTER TABLE charge_offer
  DROP KEY idx_device, DROP KEY idx_package_template,
  DROP COLUMN device_id, DROP COLUMN package_template_id,
  DROP COLUMN min_charge_cents, DROP COLUMN show_remark, DROP COLUMN card_default,
  DROP COLUMN stop_when_full, DROP COLUMN deleted_at,
  ADD COLUMN template_id BIGINT UNSIGNED DEFAULT NULL AFTER station_id,
  ADD COLUMN template_package_id BIGINT UNSIGNED DEFAULT NULL AFTER template_id,
  ADD KEY idx_template (template_id, template_package_id);
ALTER TABLE pricing_rule DROP KEY idx_device, DROP COLUMN device_id;
ALTER TABLE device_meta
  DROP COLUMN charge_mode, DROP COLUMN reports_energy, DROP COLUMN reports_segmented_power;
DROP TABLE IF EXISTS pricing_switch_task_item;
DROP TABLE IF EXISTS pricing_switch_task;
DROP TABLE IF EXISTS station_recharge_package;
DROP TABLE IF EXISTS station_policy;
