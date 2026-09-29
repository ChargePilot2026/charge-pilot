-- +goose NO TRANSACTION
-- +goose Up

-- An activity is a rule that grants a coupon when a customer hits a trigger.
-- The coupon itself already lives in `coupon`; this table only says when to hand
-- one out and how far the campaign may run.
--
-- grant_source on coupon_grant already reserves 'activity' and 'invite_reward',
-- and source_event_id (char 36) is the idempotency key, so the storage model for
-- automatic grants was designed ahead of this table.
CREATE TABLE IF NOT EXISTS coupon_activity_rule (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  rule_code VARCHAR(64) NOT NULL,
  name VARCHAR(128) NOT NULL,
  trigger_type ENUM('first_recharge','invite_reward','threshold_redeem','holiday') NOT NULL,
  -- The coupon handed to the customer who met the condition.
  coupon_id BIGINT UNSIGNED NOT NULL,
  -- Invite rewards pay two parties: the invitee meets the condition and the
  -- inviter is credited for it. NULL for every other trigger.
  inviter_coupon_id BIGINT UNSIGNED NULL,
  -- threshold_redeem only: the order total that qualifies, in cents. Money is
  -- never a float anywhere in this project.
  threshold_cents BIGINT NOT NULL DEFAULT 0,
  -- Campaign budget. 0 means unlimited for max_grants; per_user_limit still
  -- applies so a single customer cannot drain a campaign.
  max_grants INT NOT NULL DEFAULT 0,
  per_user_limit INT NOT NULL DEFAULT 1,
  status ENUM('active','disabled') NOT NULL DEFAULT 'active',
  start_at DATETIME(3) NOT NULL,
  end_at DATETIME(3) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  deleted_at DATETIME(3) NULL,
  deleted_by BIGINT UNSIGNED NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_activity_rule_code (rule_code),
  KEY idx_activity_rule_active (trigger_type, status, start_at, end_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Invite attribution. Without it an "invite a friend" campaign has no way to
-- tell who referred whom, and the reward could not be paid to the inviter.
--
-- The column is deliberately nullable: customers who registered before this
-- migration have no inviter, and a customer may arrive without a referral.
ALTER TABLE user
  ADD COLUMN inviter_id BIGINT UNSIGNED NULL COMMENT '介绍人 user.id，绑定后不可自行更改';

CREATE INDEX idx_user_inviter ON user (inviter_id);

-- +goose Down
DROP INDEX idx_user_inviter ON user;
ALTER TABLE user DROP COLUMN inviter_id;
DROP TABLE IF EXISTS coupon_activity_rule;
