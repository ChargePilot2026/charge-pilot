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

// DLQ 为业务流提供有界重试及死信转移，防止持续失败事件阻塞消费。
type DLQ struct {
	WorkerDB    *gorm.DB
	Stream      *redis.Client
	Consumer    string
	MaxAttempts int
	BatchSize   int
	Streams     []string
}

// DeadLetterSuffix 是死信流名称后缀。
const DeadLetterSuffix = ".dlq"

// Entry 是一条流消息解码后的内容。
type Entry struct {
	ID      string
	EventID string
	Source  string
	Payload []byte
}

// ConsumeBatch 读取并分发每条已配置流上的记录。
// 先回收本组 pending（上一轮失败保留的记录在此重投），再取新消息；
// 处理成功的记录被确认并删除，失败的记录保留 pending，
// 等重试预算耗尽后再移入死信流。
func (d DLQ) ConsumeBatch(ctx context.Context, handler DLQHandler) (int, error) {
	if d.Stream == nil || handler == nil {
		return 0, errors.New("dlq consumer is not configured")
	}
	streams := d.Streams
	if len(streams) == 0 {
		streams = DefaultBusinessStreams
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

// DefaultBusinessStreams 是 DLQ 默认监控的业务流，
// 事件发布端（central/gateway 出队）与消费端共享这份清单。
var DefaultBusinessStreams = []string{"charge_events_stream", "charge_ended_stream", "refund_required_stream", "refund_succeeded_stream", "device_event_stream"}

func (d DLQ) consumeStream(ctx context.Context, stream string, handler DLQHandler) (int, error) {
	if err := d.Stream.XGroupCreateMkStream(ctx, stream, d.Consumer, "$").Err(); err != nil && !isBusyGroup(err) {
		return 0, err
	}
	limit := int64(d.BatchSize)
	if limit <= 0 {
		limit = 100
	}
	processed, firstErr := 0, error(nil)
	// 先回收 pending：失败未确认的记录由这里重投，预算耗尽转死信。
	if n, err := d.dispatch(ctx, stream, "0", limit, handler); err != nil {
		firstErr = err
	} else {
		processed += n
	}
	// 再取新消息；调用方按周期驱动，这里不阻塞。
	if n, err := d.dispatch(ctx, stream, ">", limit, handler); err != nil && firstErr == nil {
		firstErr = err
	} else {
		processed += n
	}
	return processed, firstErr
}

// dispatch 用同一处理语义消化一批记录（readID 为 ">" 取新消息，"0" 回收 pending）。
func (d DLQ) dispatch(ctx context.Context, stream, readID string, limit int64, handler DLQHandler) (int, error) {
	entries, err := d.Stream.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: d.Consumer, Consumer: d.Consumer, Streams: []string{stream, readID}, Count: limit, Block: -1,
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
				// 失败不确认：保留 pending 等待重投；
				// attempts 达到上限后移入死信流。
				if d.attempts(ctx, stream, message.ID) >= d.maxAttempts() {
					if moveErr := d.moveToDeadLetter(ctx, stream, entry, handlerErr); moveErr != nil && firstErr == nil {
						firstErr = moveErr
					}
				} else if firstErr == nil {
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

// attempts 读取指定消息在本消费组的待处理投递次数。
// 以消息 ID 为 XPENDING 区间，积压再多也只查这一条。
func (d DLQ) attempts(ctx context.Context, stream, messageID string) int {
	pending, err := d.Stream.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: stream, Group: d.Consumer, Start: messageID, End: messageID, Count: 1,
	}).Result()
	if err != nil || len(pending) == 0 {
		return 0
	}
	return int(pending[0].RetryCount)
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
	// Redis 保存可执行死信状态，数据库保存运维查询记录，两处均需更新。
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
	// 先确认再从源流删除：XDEL 不清消费组的 PEL 幽灵，
	// 不 XACK 的话 XPENDING 会一直挂着这条已删除记录。
	if err := d.Stream.XAck(ctx, stream, d.Consumer, entry.ID).Err(); err != nil {
		return err
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

// ReplayOnce 按滑动游标逐批重放死信，避免重复扫描同一批记录。
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
			// 单条重放失败不回退批次游标，避免阻塞后续记录。
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

// PendingSummary 返回各已配置流的积压数量。
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

// isBusyGroup 识别消费组已存在的 BUSYGROUP 错误。
func isBusyGroup(err error) bool {
	return err != nil && strings.Contains(err.Error(), "BUSYGROUP")
}
