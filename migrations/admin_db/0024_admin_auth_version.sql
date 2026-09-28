-- +goose NO TRANSACTION
-- +goose Up
ALTER TABLE admin_user_role ADD COLUMN auth_version BIGINT UNSIGNED NOT NULL DEFAULT 0;
