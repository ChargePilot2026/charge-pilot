// Package refund 承载退款家族的表记录类型：refund_record 及其回执表。
// 退款执行器（RefundExecutor）与内部派发端点（RefundAPI）逻辑见 executor.go / http.go。
package refund

import (
	"database/sql"
	"time"
)

type RefundRecord struct {
	ExecutionPolicy string         `gorm:"column:execution_policy;default:manual_review"`
	NextAttemptAt   time.Time      `gorm:"column:next_attempt_at;default:CURRENT_TIMESTAMP(3)"`
	RetryCount      uint32         `gorm:"column:retry_count"`
	ID              uint64         `gorm:"column:id;primaryKey"`
	RefundNo        string         `gorm:"column:refund_no"`
	PaymentOrderID  uint64         `gorm:"column:payment_order_id"`
	UserID          uint64         `gorm:"column:user_id"`
	BizType         string         `gorm:"column:biz_type"`
	BizID           uint64         `gorm:"column:biz_id"`
	RefundCents     int64          `gorm:"column:refund_cents"`
	Reason          sql.NullString `gorm:"column:reason"`
	Status          string         `gorm:"column:status"`
	CreatedMonth    time.Time      `gorm:"column:created_month;primaryKey"`
}

func (RefundRecord) TableName() string { return "refund_record" }
