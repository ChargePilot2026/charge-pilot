-- +goose NO TRANSACTION
-- +goose Up

-- Templates are immutable once bound to a station. Their codes and party keys
-- must remain unique even while deleted_at is NULL in MySQL.
ALTER TABLE split_template
  DROP INDEX uk_code,
  ADD UNIQUE KEY uk_split_template_code (code);

ALTER TABLE split_party
  ADD UNIQUE KEY uk_split_party_code (split_template_id, party_code);
