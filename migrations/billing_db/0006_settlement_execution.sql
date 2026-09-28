-- +goose NO TRANSACTION
-- +goose Up

-- Settlement execution: one settlement per fee calculation, idempotent on the
-- calculation so a repeated billing dispatch never creates a second split.
ALTER TABLE settlement
  ADD COLUMN split_template_code VARCHAR(64) NULL AFTER split_template_id,
  ADD COLUMN split_pool_excluded_electric_cents BIGINT NOT NULL DEFAULT 0
    COMMENT 'mode_b 下不进入分账池的电费金额' AFTER split_pool_cents,
  ADD COLUMN electric_cents BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN service_cents BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN generation INT UNSIGNED NOT NULL DEFAULT 1 AFTER fee_calculation_id;

-- A meter correction may require a new split, so the guard covers the
-- calculation plus its generation rather than the calculation alone.
ALTER TABLE settlement
  ADD UNIQUE KEY uk_fee_generation (fee_calculation_id, generation);

ALTER TABLE settlement_party_amount
  ADD COLUMN electric_cents BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN service_cents BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN generation INT UNSIGNED NOT NULL DEFAULT 1,
  ADD KEY idx_party_code (party_code),
  ADD KEY idx_settlement_party (settlement_id, party_id);

-- Withdrawal balance checks always filter by party and open status.
ALTER TABLE withdraw_request
  ADD KEY idx_party_status (party_id, status);

-- Mode_B excludes the electricity component from the split pool; the excluded
-- amount is retained on the settlement header for reconciliation.
UPDATE settlement
   SET electric_cents = total_cents - split_pool_cents
 WHERE split_pool_cents < total_cents;
