-- +goose NO TRANSACTION
-- +goose Up

-- Reconciliation must be reproducible: rerunning a date overwrites the
-- recorded comparison instead of stacking a second row for the same day.
ALTER TABLE finance_reconcile_log
  ADD UNIQUE KEY uk_type_date (reconcile_type, reconcile_date);
