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

// RefundResult 是退款真正到账后支付渠道发布的事件体。
// 消费端把它转成幂等入账，因此无论事件被重复投递多少次，
// 用户看到的钱都只会变动一次。
type RefundResult struct {
	RefundNo    string `json:"refund_no"`
	ChannelRef  string `json:"channel_ref"`
	RefundCents int64  `json:"refund_cents"`
	PaymentNo   string `json:"payment_order_no"`
	Success     bool   `json:"success"`
	Reason      string `json:"reason"`
}

// ResultConsumer 把已结算的退款入账，并把每一次尝试都记到
// comp_tx_log，这样写到一半的跨服务操作能被识别出来重放，
// 而不是无声无息地丢掉。
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

// ConsumeOnce 排空待处理的结果事件。每条记录独立提交或进死信，
// 所以一条毒丸事件不会卡住其余的。
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

// Handle 把一条结果事件恰好应用一次。补偿行写在入账之前，
// 这样中途崩溃会留下一条可供对账的记录，
// 而不是一个无从追溯的半成品状态。
func (c ResultConsumer) Handle(ctx context.Context, stream string, entry Entry) error {
	if c.UserDB == nil || c.Stream == nil {
		return errors.New("result consumer is not configured")
	}
	if entry.EventID == "" {
		return errors.New("result event has no event id")
	}
	var result RefundResult
	if err := json.Unmarshal(entry.Payload, &result); err != nil {
		// 解析不了的负载，重试也永远不会成功。
		return c.deadLetter(ctx, stream, entry, fmt.Errorf("decode refund result: %w", err))
	}
	txID := "refund-result:" + entry.EventID
	payloadHash := hashPayload(entry.Payload)

	committed, err := c.isCommitted(ctx, txID)
	if err != nil {
		return err
	}
	if committed {
		// 已经应用过了。只做 ack，不再动钱。
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
	// comp_tx_log 按 created_month 分区，所以月份是键的一部分。
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
	// comp_tx_log 是分区表，ON DUPLICATE 子句没有可匹配的唯一键目标；
	// 所以改成显式查一次存在性，
	// 让插入保持幂等。
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

// post 把退掉的钱入账。整个过程跑在一个事务里，让回执、
// 支付单的已退总额和用户钱包不会各走各的。
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
			// 已经结算，没有钱需要再动了。
			return nil
		}
		if refund.Status != "processing" && refund.Status != "pending" {
			return fmt.Errorf("refund %s is %s and cannot settle", result.RefundNo, refund.Status)
		}
		// 渠道报出的金额必须与申请金额一致，否则这条事件
		// 描述的是另一笔退款，不能入账。
		if refund.RefundCents != result.RefundCents {
			return fmt.Errorf("refund %s amount mismatch: requested %d, channel reported %d",
				result.RefundNo, refund.RefundCents, result.RefundCents)
		}
		if err := tx.Table("refund_record").Where("id = ?", refund.ID).
			Updates(map[string]any{"status": "success", "completed_at": gorm.Expr("UTC_TIMESTAMP(3)")}).Error; err != nil {
			return fmt.Errorf("mark refund %s success: %w", result.RefundNo, err)
		}
		// refund_success_receipt 以退款记录为主键，这正是重复投递变成空操作的原因。
		// 这里显式查一次，
		// 因为 MySQL 无法为这种表结构构造 ON DUPLICATE 子句。
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
		// payment_order 按 created_month 分区，且主键包含这一列，
		// 所以月份必须写进查询条件。
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
