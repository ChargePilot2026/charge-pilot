-- +goose NO TRANSACTION
-- +goose Up

-- Coupons are identified by their primary key. The codes go with them.
--
-- coupon_redemption is the part that needs care: it has no coupon_id at all,
-- so coupon_code was its only route back to the coupon. That is a
-- string-reference where a foreign key belongs, and it is the reason the
-- column could not simply be dropped. Add the id, backfill, then drop the
-- code.
--
-- The backfill takes MIN(id) rather than joining directly. uk_code is
-- (code, deleted_at) and MySQL treats each NULL in a unique index as
-- distinct, so a code that was reused after a soft delete resolves to more
-- than one row. MIN(id) makes the choice deterministic and picks the oldest
-- row, which is the one that was actually live when the redemption happened.
--
-- The column stays NULL-able on purpose: a redemption whose code no longer
-- resolves to any coupon has to survive the migration rather than be dropped
-- by a NOT NULL change failing halfway.

ALTER TABLE coupon_redemption
  ADD COLUMN coupon_id BIGINT UNSIGNED NULL AFTER id;

UPDATE coupon_redemption cr
SET cr.coupon_id = (SELECT MIN(c.id) FROM coupon c WHERE c.code = cr.coupon_code);

CREATE INDEX idx_redemption_coupon ON coupon_redemption (coupon_id);

DROP INDEX uk_redemption ON coupon_redemption;
ALTER TABLE coupon_redemption DROP COLUMN coupon_code;

-- active_code is a generated column, not a stored value of its own:
--   GENERATED ALWAYS AS (IF(deleted_at IS NULL, code, NULL)) STORED
-- It existed to make "code is unique among live rows" expressible, because
-- the (code, deleted_at) unique index cannot say that on its own once code is
-- gone. It is derived from code, so it goes when code goes.

DROP INDEX uk_coupon_active_code ON coupon;
DROP INDEX uk_code ON coupon;
ALTER TABLE coupon DROP COLUMN active_code;
ALTER TABLE coupon DROP COLUMN code;

-- The activity rule is referenced by coupon_id already; rule_code was only
-- ever an external label for the rule.

DROP INDEX uk_activity_rule_code ON coupon_activity_rule;
ALTER TABLE coupon_activity_rule DROP COLUMN rule_code;

-- +goose Down

-- Re-derivable in the general case: every live row has an id, and the grants
-- that drove them still carry their own identifiers. The codes below are
-- stand-ins, not the original operator-facing values.
ALTER TABLE coupon ADD COLUMN code VARCHAR(64) NULL AFTER id;
UPDATE coupon SET code = CONCAT('CP-', id) WHERE code IS NULL AND deleted_at IS NULL;
ALTER TABLE coupon MODIFY COLUMN code VARCHAR(64) NOT NULL;
ALTER TABLE coupon ADD UNIQUE KEY uk_code (code, deleted_at);
ALTER TABLE coupon ADD COLUMN active_code VARCHAR(64)
  GENERATED ALWAYS AS (IF(deleted_at IS NULL, code, NULL)) STORED;
ALTER TABLE coupon ADD UNIQUE KEY uk_coupon_active_code (active_code);

ALTER TABLE coupon_activity_rule ADD COLUMN rule_code VARCHAR(64) NULL AFTER id;
UPDATE coupon_activity_rule SET rule_code = CONCAT('R-', id) WHERE rule_code IS NULL;
ALTER TABLE coupon_activity_rule MODIFY COLUMN rule_code VARCHAR(64) NOT NULL;
ALTER TABLE coupon_activity_rule ADD UNIQUE KEY uk_activity_rule_code (rule_code);

ALTER TABLE coupon_redemption ADD COLUMN coupon_code VARCHAR(64) NULL AFTER id;
UPDATE coupon_redemption cr
SET cr.coupon_code = (SELECT c.code FROM coupon c WHERE c.id = cr.coupon_id)
WHERE cr.coupon_code IS NULL;
ALTER TABLE coupon_redemption DROP INDEX idx_redemption_coupon;
ALTER TABLE coupon_redemption ADD UNIQUE KEY uk_redemption (coupon_code);
