-- +goose NO TRANSACTION
-- +goose Up

-- Two business codes that the application never needed.
--
-- customer.code is dead weight: nothing in the Go code, the admin web or the
-- tests ever reads or writes it. It carries a unique index and nothing else.
--
-- charge_offer.code is worse than dead -- it is generated. offerCodeFor()
-- derives it from the package template, the station and the device on every
-- apply, purely to satisfy uk_station_code. The row id already says which
-- offer this is, and the offer is looked up by station_id + device_id, never
-- by its code. Removing it also removes the reason the apply path had to
-- compute a digest of the device id (see the note in offerCodeFor: a device
-- id runs to 64 characters, so it had to be shortened to fit the code).
--
-- Drop the indexes explicitly before the columns. The same lesson as 0041 and
-- 0044: dropping a column that leads an index only clips the column out of
-- it and leaves the constraint behind.

DROP INDEX uk_station_code ON charge_offer;
ALTER TABLE charge_offer DROP COLUMN code;

DROP INDEX uk_code ON customer;
ALTER TABLE customer DROP COLUMN code;

-- +goose Down

-- The original codes cannot be reconstructed. The generated stand-ins below
-- only have to be unique, so deriving them from the primary key is enough to
-- get the columns and their unique indexes working again.
ALTER TABLE charge_offer ADD COLUMN code VARCHAR(64) NULL AFTER device_id;
UPDATE charge_offer SET code = CONCAT('O-', id) WHERE code IS NULL;
ALTER TABLE charge_offer MODIFY COLUMN code VARCHAR(64) NOT NULL;
ALTER TABLE charge_offer ADD UNIQUE KEY uk_station_code (station_id, code);

ALTER TABLE customer ADD COLUMN code VARCHAR(64) NULL AFTER name;
UPDATE customer SET code = CONCAT('C-', id) WHERE code IS NULL;
ALTER TABLE customer MODIFY COLUMN code VARCHAR(64) NOT NULL;
ALTER TABLE customer ADD UNIQUE KEY uk_code (code);
