package outbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/charge"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RefundResult 是渠道确认退款到账后的结果事件；消费端通过幂等凭据避免重复入账。
type RefundResult struct {
	RefundNo    string `json:"refund_no"`
	ChannelRef  string `json:"channel_ref"`
	RefundCents int64  `json:"refund_cents"`
	PaymentNo   string `json:"payment_order_no"`
	Success     bool   `json:"success"`
	Reason      string `json:"reason"`
}

// ResultConsumer 应用已确认退款，并在 comp_tx_log 记录跨服务处理尝试，支持故障补偿。
type ResultConsumer struct {
	UserDB   *gorm.DB
	WorkerDB *gorm.DB
	Stream   *redis.Client
	Group    string
	// Streams 是本消费者要排空的事件流。
	Streams []string
	Batch   int
}

// DefaultResultStreams 是承载结算结果的流。
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

// ConsumeOnce 独立处理结果事件，成功确认，失败重试或转死信，不阻塞其他记录。
func (c ResultConsumer) ConsumeOnce(ctx context.Context) (int, error) {
	if c.UserDB == nil || c.Stream == nil {
		return 0, errors.New("result consumer is not configured")
	}
	group := c.consumerGroup()
	processed, firstErr := 0, error(nil)
	for _, stream := range c.streams() {
		if err := c.Stream.XGroupCreateMkStream(ctx, stream, group, "0").Err(); err != nil && !isBusyGroup(err) {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		batches, err := c.Stream.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group: group, Consumer: group, Streams: append([]string{stream}, ">"), Count: int64(c.batchSize()),
			// go-redis's default zero sends BLOCK 0 (wait forever). This
			// scheduled batch must return when empty so billing/refunds run again.
			Block: -1,
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

// Handle 先保存补偿记录，再幂等应用业务结果，供异常中断后的恢复与对账。
func (c ResultConsumer) Handle(ctx context.Context, stream string, entry Entry) error {
	if c.UserDB == nil || c.Stream == nil {
		return errors.New("result consumer is not configured")
	}
	if entry.EventID == "" {
		return errors.New("result event has no event id")
	}
	var result RefundResult
	if err := json.Unmarshal(entry.Payload, &result); err != nil {
		// 畸形载荷属于不可重试错误，直接转死信。
		return c.deadLetter(ctx, stream, entry, fmt.Errorf("decode refund result: %w", err))
	}
	txID := "refund-result:" + entry.EventID
	payloadHash := hashPayload(entry.Payload)

	committed, err := c.isCommitted(ctx, txID)
	if err != nil {
		return err
	}
	if committed {
		// 已应用的事件仅确认，不再次变更资金。
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
	// comp_tx_log 的联合键包含分区列 created_month。
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
	// 台账在 worker_db 里，而它归 worker 所有；重试时复用这一行。
	now := time.Now().UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	// 对分区补偿表显式检查存在性，避免依赖 GORM 自动推导冲突目标。
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

// post 在同一事务中更新退款回执、支付单已退款金额及钱包余额。
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
			// 已结算退款不重复更新余额。
			return nil
		}
		if refund.Status != "processing" && refund.Status != "pending" {
			return fmt.Errorf("refund %s is %s and cannot settle", result.RefundNo, refund.Status)
		}
		// 渠道金额必须与退款申请金额一致。
		if refund.RefundCents != result.RefundCents {
			return fmt.Errorf("refund %s amount mismatch: requested %d, channel reported %d",
				result.RefundNo, refund.RefundCents, result.RefundCents)
		}
		// Serialize cumulative payment amounts while retaining the existing refund lock.
		var payment charge.PaymentOrderRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", refund.PaymentOrderID).Take(&payment).Error; err != nil {
			return err
		}
		if payment.UserID != refund.UserID || payment.RefundedCents < 0 || payment.PaidCents <= 0 || refund.RefundCents > payment.PaidCents-payment.RefundedCents {
			return fmt.Errorf("refund %s exceeds the remaining paid amount", result.RefundNo)
		}
		if err := tx.Table("refund_record").Where("id = ?", refund.ID).
			Updates(map[string]any{"status": "success", "completed_at": gorm.Expr("UTC_TIMESTAMP(3)")}).Error; err != nil {
			return fmt.Errorf("mark refund %s success: %w", result.RefundNo, err)
		}
		// 按退款记录主键检查 refund_success_receipt，确保重复投递不重复入账。
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
		// payment_order 的主键包含分区列 created_month，查询必须同时指定月份。
		totalRefunded := payment.RefundedCents + refund.RefundCents
		status := charge.SettledPaymentStatus(payment.PaidCents, totalRefunded, payment.Status)
		if err := tx.Table("payment_order").Where("id = ? AND created_month = ?", payment.ID, payment.CreatedMonth).
			Updates(map[string]any{"refunded_cents": totalRefunded, "status": status}).Error; err != nil {
			return fmt.Errorf("accumulate refunded_cents: %w", err)
		}
		return charge.SyncOrderPaymentStatus(tx, payment.ID)
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
