-- +goose NO TRANSACTION
-- +goose Up
-- Charging stations operate year-round; opening hours are no longer configured.
ALTER TABLE station DROP COLUMN open_hours;

-- +goose Down
-- The retired text values cannot be recovered by this schema rollback.
ALTER TABLE station ADD COLUMN open_hours VARCHAR(64) DEFAULT NULL AFTER status;
