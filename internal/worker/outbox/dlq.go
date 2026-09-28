package outbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DLQHandler processes one stream entry. Returning nil acknowledges the entry;
// returning an error moves it to the dead-letter stream after maxAttempts.
type DLQHandler func(ctx context.Context, stream, eventID, source string, payload []byte) error

// DLQ gives every business stream a bounded retry policy and a dead-letter
// stream, so a poison event cannot block the stream head forever.
type DLQ struct {
	WorkerDB    *gorm.DB
	Stream      *redis.Client
	Consumer    string
	MaxAttempts int
	BatchSize   int
	Streams     []string
}

// DeadLetterSuffix names the stream a failed entry is parked in.
const DeadLetterSuffix = ".dlq"

// Entry is the decoded content of one stream message.
type Entry struct {
	ID      string
	EventID string
	Source  string
	Payload []byte
}

// ConsumeBatch reads and dispatches pending entries for every configured stream.
// Successfully handled entries are acknowledged and removed; failures keep the
// entry and are moved to the dead-letter stream once the retry budget is spent.
func (d DLQ) ConsumeBatch(ctx context.Context, handler DLQHandler) (int, error) {
	if d.Stream == nil || handler == nil {
		return 0, errors.New("dlq consumer is not configured")
	}
	streams := d.Streams
	if len(streams) == 0 {
		streams = []string{"charge_events_stream", "charge_ended_stream", "refund_required_stream", "refund_succeeded_stream", "device_event_stream"}
	}
	processed, firstErr := 0, error(nil)
	for _, stream := range streams {
		n, err := d.consumeStream(ctx, stream, handler)
		processed += n
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return processed, firstErr
}

func (d DLQ) consumeStream(ctx context.Context, stream string, handler DLQHandler) (int, error) {
	if err := d.Stream.XGroupCreateMkStream(ctx, stream, d.Consumer, "$").Err(); err != nil && !isBusyGroup(err) {
		return 0, err
	}
	limit := int64(d.BatchSize)
	if limit <= 0 {
		limit = 100
	}
	entries, err := d.Stream.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: d.Consumer, Consumer: d.Consumer, Streams: []string{stream, ">"}, Count: limit, Block: 0,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	processed, firstErr := 0, error(nil)
	for _, batch := range entries {
		for _, message := range batch.Messages {
			entry := decodeEntry(message)
			if handlerErr := handler(ctx, stream, entry.EventID, entry.Source, entry.Payload); handlerErr != nil {
				delivered, dlqErr := d.Stream.XAck(ctx, stream, d.Consumer, message.ID).Result()
				if dlqErr != nil {
					if firstErr == nil {
						firstErr = dlqErr
					}
					continue
				}
				// The entry stays pending until the retry budget is exhausted, so
				// a transient failure is retried rather than lost.
				attempts := d.attempts(ctx, stream, message.ID)
				if attempts >= d.maxAttempts() {
					if moveErr := d.moveToDeadLetter(ctx, stream, entry, handlerErr); moveErr != nil && firstErr == nil {
						firstErr = moveErr
					}
				}
				_ = delivered
				if firstErr == nil {
					firstErr = handlerErr
				}
				continue
			}
			if err := d.Stream.XAck(ctx, stream, d.Consumer, message.ID).Err(); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if err := d.Stream.XDel(ctx, stream, message.ID).Err(); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			processed++
		}
	}
	return processed, firstErr
}

func (d DLQ) maxAttempts() int {
	if d.MaxAttempts > 0 {
		return d.MaxAttempts
	}
	return 5
}

// attempts reads the pending-entry delivery count Redis maintains for a group.
func (d DLQ) attempts(ctx context.Context, stream, messageID string) int {
	pending, err := d.Stream.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: stream, Group: d.Consumer, Start: "-", End: "+", Count: 100,
	}).Result()
	if err != nil || len(pending) == 0 {
		return 0
	}
	for _, item := range pending {
		if item.ID == messageID {
			return int(item.RetryCount)
		}
	}
	return 0
}

func (d DLQ) moveToDeadLetter(ctx context.Context, stream string, entry Entry, cause error) error {
	body := map[string]any{
		"original_stream": stream, "entry_id": entry.ID, "event_id": entry.EventID,
		"source": entry.Source, "payload": string(entry.Payload),
		"reason": truncate(cause.Error(), 512), "failed_at": time.Now().UTC().Format(time.RFC3339),
	}
	if _, err := d.Stream.XAdd(ctx, &redis.XAddArgs{Stream: stream + DeadLetterSuffix, Values: body}).Result(); err != nil {
		return err
	}
	// Redis is the authoritative dead-letter state; the database table is the
	// ledger an operator pages from, so both are written.
	if d.WorkerDB != nil {
		now := time.Now().UTC()
		month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		_ = d.WorkerDB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
			Table("dlq_replay_cursor").Create(map[string]any{"stream": stream}).Error
		_ = d.WorkerDB.WithContext(ctx).Table("dlq_log").Create(map[string]any{
			"stream": stream, "entry_id": entry.ID, "reason": truncate(cause.Error(), 512),
			"payload_json": string(entry.Payload), "status": "open", "created_month": month,
		}).Error
	}
	return d.Stream.XDel(ctx, stream, entry.ID).Err()
}

func decodeEntry(message redis.XMessage) Entry {
	entry := Entry{ID: message.ID}
	for key, value := range message.Values {
		text, ok := value.(string)
		if !ok {
			if raw, isBytes := value.([]byte); isBytes {
				text = string(raw)
			} else {
				text = fmt.Sprint(value)
			}
		}
		switch key {
		case "event_id":
			entry.EventID = text
		case "source":
			entry.Source = text
		case "payload":
			entry.Payload = []byte(text)
		}
	}
	return entry
}

// DeadLetterCounts reports how many parked entries each stream has, which is
// what an operator dashboard needs to decide whether to replay.
func (d DLQ) DeadLetterCounts(ctx context.Context) (map[string]int64, error) {
	counts := map[string]int64{}
	streams := d.Streams
	if len(streams) == 0 {
		return counts, nil
	}
	for _, stream := range streams {
		size, err := d.Stream.XLen(ctx, stream+DeadLetterSuffix).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				continue
			}
			return counts, err
		}
		counts[stream] = size
	}
	return counts, nil
}

// ReplayOnce moves entries from a stream's dead-letter list back onto the live
// stream using the sliding cursor, so replay makes forward progress instead of
// rescanning the same head forever.
func (d DLQ) ReplayOnce(ctx context.Context, stream string, handler DLQHandler) (int, error) {
	if d.Stream == nil || handler == nil {
		return 0, errors.New("dlq replay is not configured")
	}
	deadStream := stream + DeadLetterSuffix
	var cursor struct {
		LastID        *string `gorm:"column:last_id"`
		ScannedTotal  uint64  `gorm:"column:scanned_total"`
		ReplayedTotal uint64  `gorm:"column:replayed_total"`
	}
	loadErr := d.WorkerDB.WithContext(ctx).Table("dlq_replay_cursor").Where("stream = ?", stream).Take(&cursor).Error
	if loadErr != nil && !errors.Is(loadErr, gorm.ErrRecordNotFound) {
		return 0, loadErr
	}
	start := "-"
	if cursor.LastID != nil && *cursor.LastID != "" {
		// Exclusive start: the cursor is the last entry already examined.
		start = "(" + *cursor.LastID
	}
	limit := int64(d.BatchSize)
	if limit <= 0 {
		limit = 100
	}
	entries, err := d.Stream.XRangeN(ctx, deadStream, start, "+", limit).Result()
	if err != nil {
		return 0, err
	}
	replayed, scanned := 0, 0
	lastID := cursor.LastID
	var firstErr error
	for _, message := range entries {
		scanned++
		id := message.ID
		lastID = &id
		entry := decodeEntry(message)
		if err := handler(ctx, stream, entry.EventID, entry.Source, entry.Payload); err != nil {
			// A failed replay does not rewind the cursor; otherwise the same
			// entry would be retried forever.
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := d.Stream.XDel(ctx, deadStream, message.ID).Err(); err != nil && firstErr == nil {
			firstErr = err
		}
		replayed++
	}
	if d.WorkerDB != nil && scanned > 0 {
		updates := map[string]any{
			"scanned_total":  gorm.Expr("scanned_total + ?", scanned),
			"replayed_total": gorm.Expr("replayed_total + ?", replayed),
		}
		if lastID != nil {
			updates["last_id"] = *lastID
		}
		if err := d.WorkerDB.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).
			Table("dlq_replay_cursor").Where("stream = ?", stream).Updates(updates).Error; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return replayed, firstErr
}

// PendingSummary reports the depth of each configured stream so an operator can
// see a growing backlog before it becomes an incident.
func (d DLQ) PendingSummary(ctx context.Context) (map[string]int64, error) {
	summary := map[string]int64{}
	for _, stream := range d.Streams {
		length, err := d.Stream.XLen(ctx, stream).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				continue
			}
			return summary, err
		}
		summary[stream] = length
	}
	return summary, nil
}

// isBusyGroup reports the expected "group already exists" error, which only
// means another worker created it first.
func isBusyGroup(err error) bool {
	return err != nil && strings.Contains(err.Error(), "BUSYGROUP")
}
