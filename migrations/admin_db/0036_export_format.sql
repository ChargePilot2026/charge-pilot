-- +goose Up
ALTER TABLE export_task
  ADD COLUMN file_format ENUM('csv','xlsx','pdf') NOT NULL DEFAULT 'csv' AFTER resource;

-- +goose Down
ALTER TABLE export_task DROP COLUMN file_format;
