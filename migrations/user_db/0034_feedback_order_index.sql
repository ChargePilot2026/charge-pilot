-- +goose NO TRANSACTION
-- +goose Up

-- The contract allows exactly one feedback per charge order, and the submission
-- path has to prove that before inserting. Without an index on order_id that
-- check degrades into a full table scan of every feedback ever submitted, so
-- the uniqueness guarantee would quietly get slower as the table grows.
--
-- This is deliberately a plain index rather than a UNIQUE one: the invariant is
-- enforced by the submission handler while holding the charge_order row lock,
-- which is what serialises two concurrent submissions for the same order. A
-- UNIQUE index would additionally reject a resubmission after a soft delete,
-- which the contract does not describe.
CREATE INDEX idx_feedback_order ON feedback (order_id);

-- +goose Down
DROP INDEX idx_feedback_order ON feedback;
