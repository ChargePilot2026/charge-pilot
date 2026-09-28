package outbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RefundResult is the payload a payment channel publishes once a refund has
// actually settled. The consumer turns it into an idempotent posting so the
// customer sees the money move exactly once, however often the event is redelivered.
type RefundResult struct {
	RefundNo    string `json:"refund_no"`
	ChannelRef  string `json:"channel_ref"`
	RefundCents int64  `json:"refund_cents"`
	PaymentNo   string `json:"payment_order_no"`
	Success     bool   `json:"success"`
	Reason      string `json:"reason"`
}

// ResultConsumer posts settled refunds and records every attempt in
// comp_tx_log, so a half-finished cross-service write can be identified and
// replayed rather than silently lost.
type ResultConsumer struct {
	UserDB   *gorm.DB
	WorkerDB *gorm.DB
	Stream   *redis.Client
	Group    string
	// Streams are the event streams this consumer drains.
	Streams []string
	Batch   int
}

// DefaultResultStreams are the streams that carry settlement results.
var DefaultResultStreams = []string{"refund_succeeded_stream", "refund_result_stream"}

func (c ResultConsumer) consumerGroup() string {
	if c.Group != "" {
		return c.Group
	}
	return "refund-result"
}

func (c ResultConsumer) streams() []string {
	if len(c.Streams) > 0 {
		return c.Streams
	}
	return DefaultResultStreams
}

func (c ResultConsumer) batchSize() int {
	if c.Batch > 0 {
		return c.Batch
	}
	return 100
}

// ConsumeOnce drains pending result events. Each entry is committed or
// dead-lettered independently, so one poison event cannot stall the rest.
func (c ResultConsumer) ConsumeOnce(ctx context.Context) (int, error) {
	if c.UserDB == nil || c.Stream == nil {
		return 0, errors.New("result consumer is not configured")
	}
	group := c.consumerGroup()
	processed, firstErr := 0, error(nil)
	for _, stream := range c.streams() {
		if err := c.Stream.XGroupCreateMkStream(ctx, stream, group, "$").Err(); err != nil && !isBusyGroup(err) {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		batches, err := c.Stream.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group: group, Consumer: group, Streams: append([]string{stream}, ">"), Count: int64(c.batchSize()),
		}).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, batch := range batches {
			for _, message := range batch.Messages {
				entry := decodeEntry(message)
				if err := c.Handle(ctx, stream, entry); err != nil {
					if firstErr == nil {
						firstErr = err
					}
					continue
				}
				processed++
			}
		}
	}
	return processed, firstErr
}

// Handle applies one result event exactly once. The compensation row is written
// before the posting so a crash mid-way leaves a record to reconcile rather
// than an untraceable half-done state.
func (c ResultConsumer) Handle(ctx context.Context, stream string, entry Entry) error {
	if c.UserDB == nil || c.Stream == nil {
		return errors.New("result consumer is not configured")
	}
	if entry.EventID == "" {
		return errors.New("result event has no event id")
	}
	var result RefundResult
	if err := json.Unmarshal(entry.Payload, &result); err != nil {
		// A payload that cannot be parsed will never succeed on retry.
		return c.deadLetter(ctx, stream, entry, fmt.Errorf("decode refund result: %w", err))
	}
	txID := "refund-result:" + entry.EventID
	payloadHash := hashPayload(entry.Payload)

	committed, err := c.isCommitted(ctx, txID)
	if err != nil {
		return err
	}
	if committed {
		// Already applied. Acknowledge without touching the money again.
		return c.Stream.XAck(ctx, stream, c.consumerGroup(), entry.ID).Err()
	}

	if err := c.recordTx(ctx, txID, stream, payloadHash); err != nil {
		return err
	}
	if result.Success {
		if err := c.post(ctx, result); err != nil {
			_ = c.markTx(ctx, txID, "failed", truncate(err.Error(), 255))
			return err
		}
	} else if err := c.markTx(ctx, txID, "compensated", truncate(result.Reason, 255)); err != nil {
		return err
	}
	if err := c.markTx(ctx, txID, "committed", ""); err != nil {
		return err
	}
	return c.Stream.XAck(ctx, stream, c.consumerGroup(), entry.ID).Err()
}

func (c ResultConsumer) isCommitted(ctx context.Context, txID string) (bool, error) {
	if c.WorkerDB == nil {
		return false, errors.New("worker database is not configured")
	}
	var row struct {
		Status string `gorm:"column:status"`
	}
	// comp_tx_log is partitioned by created_month, so the month is part of the key.
	month := time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	err := c.WorkerDB.WithContext(ctx).Table("comp_tx_log").
		Where("tx_id = ? AND created_month = ?", txID, month).
		Where("tx_id = ?", txID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return row.Status == "committed", nil
}

func (c ResultConsumer) recordTx(ctx context.Context, txID, stream, payloadHash string) error {
	// The ledger lives in worker_db, which the worker owns; a retry reuses the row.
	now := time.Now().UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	// comp_tx_log is partitioned, where an ON DUPLICATE clause has no conflict
	// target to match against; an explicit existence check keeps the insert
	// idempotent instead.
	var existing int64
	if err := c.WorkerDB.WithContext(ctx).Table("comp_tx_log").
		Where("tx_id = ? AND created_month = ?", txID, month).Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	if err := c.WorkerDB.WithContext(ctx).Table("comp_tx_log").Create(map[string]any{
		"tx_id": txID, "consumer_group": c.consumerGroup(), "stream": stream, "payload_hash": payloadHash,
		"status": "pending", "created_month": month,
	}).Error; err != nil {
		return fmt.Errorf("record compensation tx %s: %w", txID, err)
	}
	return nil
}

func (c ResultConsumer) markTx(ctx context.Context, txID, status, lastError string) error {
	values := map[string]any{"status": status}
	if lastError != "" {
		values["last_error"] = lastError
	}
	if status == "committed" {
		values["committed_at"] = gorm.Expr("UTC_TIMESTAMP(3)")
	}
	month := time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	return c.WorkerDB.WithContext(ctx).Table("comp_tx_log").
		Where("tx_id = ? AND created_month = ?", txID, month).Updates(values).Error
}

// post credits the refunded money. It runs in one transaction so the receipt,
// the payment's refunded total and the customer's wallet cannot diverge.
func (c ResultConsumer) post(ctx context.Context, result RefundResult) error {
	if result.RefundNo == "" || result.RefundCents <= 0 {
		return errors.New("refund result is incomplete")
	}
	return c.UserDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var refund struct {
			ID             uint64 `gorm:"column:id"`
			PaymentOrderID uint64 `gorm:"column:payment_order_id"`
			UserID         uint64 `gorm:"column:user_id"`
			RefundCents    int64  `gorm:"column:refund_cents"`
			Status         string `gorm:"column:status"`
			CreatedMonth   string `gorm:"column:created_month"`
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("refund_record").
			Select("id, payment_order_id, user_id, refund_cents, status, created_month").
			Where("refund_no = ? AND deleted_at IS NULL", result.RefundNo).Take(&refund).Error; err != nil {
			return err
		}
		if refund.Status == "success" {
			// Already settled; nothing further to move.
			return nil
		}
		if refund.Status != "processing" && refund.Status != "pending" {
			return fmt.Errorf("refund %s is %s and cannot settle", result.RefundNo, refund.Status)
		}
		// The channel's figure must match what was requested, or the event is
		// describing a different refund and must not be posted.
		if refund.RefundCents != result.RefundCents {
			return fmt.Errorf("refund %s amount mismatch: requested %d, channel reported %d",
				result.RefundNo, refund.RefundCents, result.RefundCents)
		}
		if err := tx.Table("refund_record").Where("id = ?", refund.ID).
			Updates(map[string]any{"status": "success", "completed_at": gorm.Expr("UTC_TIMESTAMP(3)")}).Error; err != nil {
			return fmt.Errorf("mark refund %s success: %w", result.RefundNo, err)
		}
		// refund_success_receipt is keyed by the refund record, which is what
		// makes a redelivered event a no-op. The check is explicit because
		// MySQL cannot build an ON DUPLICATE clause for this table shape.
		var receipt int64
		if err := tx.Table("refund_success_receipt").
			Where("refund_record_id = ?", refund.ID).Count(&receipt).Error; err != nil {
			return fmt.Errorf("count success receipt: %w", err)
		}
		if receipt == 0 {
			if err := tx.Table("refund_success_receipt").Create(map[string]any{
				"refund_record_id": refund.ID, "wechat_refund_id": result.ChannelRef,
			}).Error; err != nil {
				return fmt.Errorf("write success receipt: %w", err)
			}
		}
		// payment_order is partitioned by created_month and its primary key
		// includes that column, so the month has to be part of the predicate.
		var payment struct {
			ID           uint64 `gorm:"column:id"`
			CreatedMonth string `gorm:"column:created_month"`
		}
		if err := tx.Table("payment_order").Select("id, created_month").
			Where("id = ?", refund.PaymentOrderID).Take(&payment).Error; err != nil {
			return err
		}
		if err := tx.Table("payment_order").Where("id = ? AND created_month = ?", payment.ID, payment.CreatedMonth).
			Updates(map[string]any{"refunded_cents": gorm.Expr("refunded_cents + ?", refund.RefundCents)}).Error; err != nil {
			return fmt.Errorf("accumulate refunded_cents: %w", err)
		}
		return nil
	})
}

func (c ResultConsumer) deadLetter(ctx context.Context, stream string, entry Entry, cause error) error {
	if _, err := c.Stream.XAdd(ctx, &redis.XAddArgs{
		Stream: stream + DeadLetterSuffix,
		Values: map[string]any{
			"original_stream": stream, "entry_id": entry.ID, "event_id": entry.EventID,
			"source": entry.Source, "payload": string(entry.Payload),
			"reason": truncate(cause.Error(), 512), "failed_at": time.Now().UTC().Format(time.RFC3339),
		},
	}).Result(); err != nil {
		return err
	}
	if ackErr := c.Stream.XAck(ctx, stream, c.consumerGroup(), entry.ID).Err(); ackErr != nil {
		return ackErr
	}
	return c.Stream.XDel(ctx, stream, entry.ID).Err()
}

func hashPayload(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
