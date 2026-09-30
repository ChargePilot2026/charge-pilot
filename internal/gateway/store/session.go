package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
)

// deviceSessionRow 对应 gateway_db.device_session。该表按 created_month
// 分区，所以这一列属于主键，
// 每次读写都必须带上它。
type deviceSessionRow struct {
	ID           uint64    `gorm:"column:id;primaryKey"`
	SessionID    string    `gorm:"column:session_id"`
	DeviceID     string    `gorm:"column:device_id"`
	Protocol     string    `gorm:"column:protocol"`
	RemoteAddr   string    `gorm:"column:remote_addr"`
	StartedAt    time.Time `gorm:"column:started_at"`
	LastActiveAt time.Time `gorm:"column:last_active_at"`
	EndedAt      time.Time `gorm:"column:ended_at"`
	CloseReason  string    `gorm:"column:close_reason"`
	BytesIn      int64     `gorm:"column:bytes_in"`
	BytesOut     int64     `gorm:"column:bytes_out"`
	FramesIn     int64     `gorm:"column:frames_in"`
	FramesOut    int64     `gorm:"column:frames_out"`
	// created_month 是 DATE，所以读回来是时间值，写入时需要格式化。
	CreatedMonth time.Time `gorm:"column:created_month"`
}

func (deviceSessionRow) TableName() string { return "device_session" }

// RecordSession 写入一条设备连接的终态。
//
// 这一行的键是 (session_id， created_month)，而 created_month 由开始时间
// 推导、不是由结束时间推导：一条跨过午夜才关闭的连接，
// 完整地属于它开始的那一天；如果从结束时间去推导，
// 这一行就可能落进另一个分区，而之后读同一条会话时搜的却是
// 最初那个分区。
//
// 同一条会话被写两次按成功处理。重连风暴或者重试的关闭
// 都可能走到这条路径上，而第二行携带的是同样的数字，
// 所以覆盖比返回一个调用方无从处理的错误更安全。
func (s MySQLSink) RecordSession(ctx context.Context, record protocol.SessionRecord) error {
	if s.DB == nil {
		return errors.New("gateway database is unavailable")
	}
	if record.SessionID == "" || record.DeviceID == "" {
		return errors.New("session record requires session and device identity")
	}
	month := time.Date(record.StartedAt.UTC().Year(), record.StartedAt.UTC().Month(), record.StartedAt.UTC().Day(), 0, 0, 0, 0, time.UTC)
	row := deviceSessionRow{
		SessionID:    record.SessionID,
		DeviceID:     record.DeviceID,
		Protocol:     string(record.Transport),
		RemoteAddr:   record.RemoteAddr,
		StartedAt:    record.StartedAt,
		LastActiveAt: record.LastActive,
		EndedAt:      record.EndedAt,
		CloseReason:  record.CloseReason,
		BytesIn:      record.BytesIn,
		BytesOut:     record.BytesOut,
		FramesIn:     record.FramesIn,
		FramesOut:    record.FramesOut,
		CreatedMonth: month,
	}
	// 存在性是显式检查的，而不是依赖 upsert：
	// 这张表是分区表，推断出来的冲突目标会生成一句空的
	// ON DUPLICATE KEY 子句，MySQL 会把它当语法错误拒掉。
	var existing int64
	if err := s.DB.WithContext(ctx).Model(&deviceSessionRow{}).
		Where("session_id = ? AND created_month = ?", row.SessionID, month).
		Count(&existing).Error; err != nil {
		return fmt.Errorf("look up device session: %w", err)
	}
	if existing > 0 {
		if err := s.DB.WithContext(ctx).Model(&deviceSessionRow{}).
			Where("session_id = ? AND created_month = ?", row.SessionID, month).
			Updates(map[string]any{
				"ended_at":       row.EndedAt,
				"last_active_at": row.LastActiveAt,
				"close_reason":   row.CloseReason,
				"bytes_in":       row.BytesIn,
				"bytes_out":      row.BytesOut,
				"frames_in":      row.FramesIn,
				"frames_out":     row.FramesOut,
			}).Error; err != nil {
			return fmt.Errorf("update device session: %w", err)
		}
		return nil
	}
	if err := s.DB.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("insert device session: %w", err)
	}
	return nil
}
