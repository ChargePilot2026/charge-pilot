-- +goose NO TRANSACTION
-- +goose Up

-- Delivery logs are an upsert target: one row per subscription and event, so a
-- retry updates the existing attempt instead of appending a duplicate. The
-- original index only covered (subscription_id, event_id) as a lookup helper,
-- which MySQL cannot use as an upsert conflict target.
ALTER TABLE webhook_delivery_log
  ADD UNIQUE KEY uk_sub_event (subscription_id, event_id);
