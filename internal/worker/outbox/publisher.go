package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Publisher struct {
	Source string
	DB     *gorm.DB
	Stream *redis.Client
}

type eventOutboxRow struct {
	ID           uint64         `gorm:"column:id;primaryKey"`
	EventID      string         `gorm:"column:event_id"`
	Stream       string         `gorm:"column:stream"`
	EnvelopeJSON []byte         `gorm:"column:envelope_json"`
	RetryCount   uint32         `gorm:"column:retry_count"`
	LastError    sql.NullString `gorm:"column:last_error"`
}

func (eventOutboxRow) TableName() string { return "event_outbox" }

// PublishBatch 会一直锁住 MySQL 的行，直到 Redis 确认收到这次发布。
// 如果 MySQL 提交失败，Redis 侧可能已经收到一份重复消息；
// 消费端必须按 event_id 去重。
func (p Publisher) PublishBatch(ctx context.Context) (int, error) {
	if p.DB == nil || p.Stream == nil || p.Source == "" {
		return 0, fmt.Errorf("outbox publisher is not configured")
	}
	table := "event_outbox"
	if p.Source == "admin" {
		table = "admin_event_outbox"
	}
	published := 0
	err := p.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []eventOutboxRow
		if err := tx.Table(table).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status IN ('pending','failed') AND scheduled_at <= NOW(3)").Order("id").Limit(100).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			pushCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			_, pushErr := p.Stream.XAdd(pushCtx, &redis.XAddArgs{Stream: row.Stream, Values: map[string]any{
				"event_id": row.EventID, "source": p.Source, "payload": string(row.EnvelopeJSON),
			}}).Result()
			cancel()
			if pushErr != nil {
				backoff := 1 << min(row.RetryCount, 8)
				update := tx.Table(table).Where("id = ?", row.ID).Updates(map[string]any{
					"status": "failed", "retry_count": gorm.Expr("retry_count + 1"),
					"last_error":   truncate(pushErr.Error(), 255),
					"scheduled_at": gorm.Expr("DATE_ADD(NOW(3), INTERVAL ? SECOND)", backoff),
				})
				if update.Error != nil {
					return update.Error
				}
				continue
			}
			update := tx.Table(table).Where("id = ?", row.ID).Updates(map[string]any{
				"status": "published", "published_at": gorm.Expr("NOW(3)"), "last_error": nil,
			})
			if update.Error != nil {
				return update.Error
			}
			published++
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return published, fmt.Errorf("publish outbox batch: %w", err)
	}
	return published, nil
}

func truncate(value string, max int) string {
	if len(value) > max {
		return value[:max]
	}
	return value
}
