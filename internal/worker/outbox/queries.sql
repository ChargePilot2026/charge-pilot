-- name: LockPending :many
SELECT id, event_id, stream, envelope_json, retry_count FROM event_outbox
WHERE status IN ('pending', 'failed') AND scheduled_at <= NOW(3)
ORDER BY id LIMIT 100 FOR UPDATE SKIP LOCKED;

-- name: MarkPublished :exec
UPDATE event_outbox SET status = 'published', published_at = NOW(3), last_error = NULL
WHERE id = ?;

-- name: MarkRetry :exec
UPDATE event_outbox SET status = 'failed', retry_count = retry_count + 1,
last_error = ?, scheduled_at = DATE_ADD(NOW(3), INTERVAL ? SECOND)
WHERE id = ?;
