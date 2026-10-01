package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
)

// deviceSessionRow 映射 device_session；created_month 为分区键和联合主键的一部分，读写必须保留。
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
	// created_month 为 DATE，查询得到时间值，写入时格式化为日期。
	CreatedMonth time.Time `gorm:"column:created_month"`
}

func (deviceSessionRow) TableName() string { return "device_session" }

// RecordSession 保存连接终态，以 session_id 和开始时间对应的 created_month 寻址。
// 跨月结束不改变分区归属；同会话重复写入更新已有记录，保持关闭操作幂等。
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
	// 显式检查记录存在性并分别更新或插入，避免依赖分区表的 GORM 冲突目标推导。
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
