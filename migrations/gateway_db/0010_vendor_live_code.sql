-- +goose NO TRANSACTION
-- +goose Up

-- The original (vendor_code, deleted_at) unique key permits duplicate live rows
-- because deleted_at is NULL. A generated key enforces case-insensitive live
-- uniqueness while allowing reuse of a soft-deleted code. Existing duplicate
-- live codes deliberately cause this migration to fail; reconcile those rows
-- before rerunning it, without automatically deleting or rewriting device links.
ALTER TABLE vendor
  ADD COLUMN live_vendor_code VARCHAR(64)
    GENERATED ALWAYS AS (CASE WHEN deleted_at IS NULL THEN vendor_code ELSE NULL END) STORED,
  ADD UNIQUE KEY uk_vendor_live_code (live_vendor_code);

-- +goose Down
ALTER TABLE vendor
  DROP INDEX uk_vendor_live_code,
  DROP COLUMN live_vendor_code;
