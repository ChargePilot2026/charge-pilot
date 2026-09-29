package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
)

// deviceSessionRow mirrors gateway_db.device_session. The table is partitioned
// by created_month, so that column is part of the primary key and must be
// supplied on every write and read.
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
	// created_month is a DATE, so it is read back as a time and formatted on write.
	CreatedMonth time.Time `gorm:"column:created_month"`
}

func (deviceSessionRow) TableName() string { return "device_session" }

// RecordSession writes the terminal state of one device connection.
//
// The row is keyed by (session_id, created_month) and created_month is derived
// from the start time, not the end time: a connection that opens before midnight
// and closes after it belongs entirely to the session's start day, and deriving
// from the end would let the write land in a different partition from the one a
// later read of the same session searches.
//
// Writing a session twice is treated as success. A reconnect storm or a retried
// close can both reach this path, and the second row carries the same numbers,
// so overwriting is safer than surfacing an error the caller cannot act on.
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
	// Existence is checked explicitly rather than relying on an upsert: this
	// table is partitioned, and an inferred conflict target produces an empty
	// ON DUPLICATE KEY clause, which MySQL rejects as a syntax error.
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
