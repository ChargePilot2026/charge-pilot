-- +goose NO TRANSACTION
-- +goose Up

-- WeChat now verifies the phone number server-side from a one-time phone code.
-- A phone hash is therefore an identity key and must not be bound to two users.
-- If this migration finds duplicate non-NULL hashes from the old client-trusted
-- endpoint, it intentionally fails so an operator can reconcile those accounts.
ALTER TABLE `user`
  DROP INDEX `idx_phone_hash`,
  ADD UNIQUE KEY `uk_phone_hash` (`phone_hash`);
