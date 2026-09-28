package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	outboxdb "github.com/ChargePilot2026/charge-pilot/internal/worker/outbox/generated"
	"github.com/redis/go-redis/v9"
)

type Publisher struct {
	Source string
	DB     *sql.DB
	Stream *redis.Client
}

// PublishBatch keeps MySQL rows locked until Redis acknowledges the publish.
// Redis may receive a duplicate if MySQL commit fails; consumers must dedupe event_id.
func (p Publisher) PublishBatch(ctx context.Context) (int, error) {
	tx, err := p.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	q := outboxdb.New(tx)
	rows, err := q.LockPending(ctx)
	if err != nil {
		return 0, err
	}
	published := 0
	for _, row := range rows {
		pushCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, pushErr := p.Stream.XAdd(pushCtx, &redis.XAddArgs{Stream: row.Stream, Values: map[string]any{
			"event_id": row.EventID, "source": p.Source, "payload": string(row.EnvelopeJson),
		}}).Result()
		cancel()
		if pushErr != nil {
			backoff := 1 << min(row.RetryCount, 8)
			if err := q.MarkRetry(ctx, outboxdb.MarkRetryParams{LastError: sql.NullString{String: truncate(pushErr.Error(), 255), Valid: true}, Column2: backoff, ID: row.ID}); err != nil {
				return published, err
			}
			continue
		}
		if err := q.MarkPublished(ctx, row.ID); err != nil {
			return published, err
		}
		published++
	}
	if err := tx.Commit(); err != nil {
		return published, fmt.Errorf("commit outbox: %w", err)
	}
	return published, nil
}

func truncate(value string, max int) string {
	if len(value) > max {
		return value[:max]
	}
	return value
}
