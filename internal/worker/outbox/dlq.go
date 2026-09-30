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

// DLQHandler 处理一条流记录。返回 nil 表示确认该记录；
// 返回 error 表示重试次数超过 maxAttempts 后把它移入死信流。
type DLQHandler func(ctx context.Context, stream, eventID, source string, payload []byte) error

// DLQ 为每条业务流配一套有界重试策略和一条死信流，
// 这样一条毒丸事件不会永远堵在流头。
type DLQ struct {
	WorkerDB    *gorm.DB
	Stream      *redis.Client
	Consumer    string
	MaxAttempts int
	BatchSize   int
	Streams     []string
}

// DeadLetterSuffix 是失败记录被停放的死信流后缀。
const DeadLetterSuffix = ".dlq"

// Entry 是一条流消息解码后的内容。
type Entry struct {
	ID      string
	EventID string
	Source  string
	Payload []byte
}

// ConsumeBatch 读取并分发每条已配置流上的待处理记录。
// 处理成功的记录被确认并删除；失败的记录保留下来，
// 等重试预算耗尽后再移入死信流。
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
				// 记录在重试预算耗尽之前一直保持 pending 状态，
				// 这样临时故障会被重试而不是丢掉。
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

// attempts 读取 Redis 为该消费组维护的待处理记录投递次数。
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
	// Redis 是死信状态的权威来源；数据库表是运维值守的账本，
	// 所以两处都要写。
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

// DeadLetterCounts 报告每条流各自停放了多少条记录，
// 运维看板要靠它来决定是否重放。
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

// ReplayOnce 借助滑动游标把死信流里的记录搬回活跃流，
// 让重放能向前推进，
// 而不是永远重复扫同一个流头。
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
		// 排他起点：游标指向最后一条已经检查过的记录。
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
			// 重放失败不回退游标；
			// 否则同一条记录会被永远重试。
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

// PendingSummary 报告每条已配置流的积压深度，
// 让运维能在堆积变成事故之前就看见它。
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

// isBusyGroup 判定那个预期内的 "group already exists" 错误，
// 它只说明另一个 worker 先把组建起来了。
func isBusyGroup(err error) bool {
	return err != nil && strings.Contains(err.Error(), "BUSYGROUP")
}
