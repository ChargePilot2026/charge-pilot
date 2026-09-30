-- +goose NO TRANSACTION
-- +goose Up

-- The station code was an operator-facing business identifier. It matched the
-- codes already on the customer's asset ledger, and the C-end detail endpoint
-- was addressed by it (GET /api/v1/user/station/:code). Both are being
-- retired, so the column goes with them.
--
-- Order matters. uk_code is (code, deleted_at), not a single-column index,
-- and this repo has already paid for learning that: dropping a column that
-- leads an index does not remove the index, it just clips the column out of
-- it and leaves a constraint behind. See 0041 and the note in
-- docs/migration/go-rebuild.md. Drop the index explicitly first.
DROP INDEX uk_code ON station;

ALTER TABLE station DROP COLUMN code;

-- station_code_identity existed for one reason: to make the code globally
-- unique and permanently un-reusable. The (code, deleted_at) unique index
-- could not do that job, because MySQL treats each NULL in a unique index as
-- distinct, so two live stations could both sit at code = NULL and neither
-- one was caught. The registry was the thing that actually enforced it. With
-- the code gone there is nothing left for it to police.
DROP TABLE IF EXISTS station_code_identity;

-- +goose Down

-- The operators' original codes are not recoverable from this schema; they
-- were only ever stored in the column being dropped. ST-<id> is a
-- deterministic stand-in so the column, its unique index and the C-end lookup
-- path work again. Re-keying existing stations to the real codes is manual.
CREATE TABLE IF NOT EXISTS station_code_identity (
  code VARCHAR(64) NOT NULL PRIMARY KEY
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE station ADD COLUMN code VARCHAR(64) NULL AFTER id;

-- Covers soft-deleted rows too: the NOT NULL change below would fail on them.
UPDATE station SET code = CONCAT('ST-', id) WHERE code IS NULL;

INSERT IGNORE INTO station_code_identity (code) SELECT code FROM station WHERE code IS NOT NULL;

ALTER TABLE station MODIFY COLUMN code VARCHAR(64) NOT NULL;
ALTER TABLE station ADD UNIQUE KEY uk_code (code, deleted_at);
