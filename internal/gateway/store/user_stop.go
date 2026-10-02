package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ActiveOrder struct {
	ChargeOrderID  uint64 `json:"charge_order_id"`
	OrderNo        string `json:"order_no"`
	UserID         uint64 `json:"user_id"`
	DeviceID       string `json:"device_id"`
	PortNo         uint8  `json:"port_no"`
	PortID         uint64 `json:"port_id"`
	StartCommandID string `json:"start_command_id"`
}

type StopReservation struct {
	CommandID string           `json:"command_id"`
	OrderNo   string           `json:"order_no"`
	DeviceID  string           `json:"device_id"`
	PortNo    uint8            `json:"port_no"`
	Status    string           `json:"status"`
	Wire      protocol.Command `json:"-"`
}

func (s MySQLSink) ExistingUserStop(ctx context.Context, orderNo string, userID uint64) (StopReservation, error) {
	var row chargeStopCommandRow
	if err := s.DB.WithContext(ctx).Where("order_no = ?", orderNo).Take(&row).Error; err != nil {
		return StopReservation{}, err
	}
	if row.UserID != userID {
		return StopReservation{}, ErrOrderConflict
	}
	return stopReservationFromRow(row)
}

func (s MySQLSink) ReserveUserStop(ctx context.Context, active ActiveOrder) (StopReservation, error) {
	if active.ChargeOrderID == 0 || active.OrderNo == "" || active.UserID == 0 || active.DeviceID == "" || active.PortNo == 0 || active.PortID == 0 || active.StartCommandID == "" {
		return StopReservation{}, ErrOrderConflict
	}
	if existing, err := s.ExistingUserStop(ctx, active.OrderNo, active.UserID); err == nil {
		return existing, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return StopReservation{}, err
	}
	var reservation StopReservation
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var start chargeCommandRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id = ? AND order_no = ? AND user_id = ?", active.ChargeOrderID, active.OrderNo, active.UserID).Take(&start).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrOrderConflict
		}
		if err != nil {
			return err
		}
		if start.Status != "acked" || start.CommandID != active.StartCommandID || start.DeviceID != active.DeviceID || start.PortNo != active.PortNo || !start.PortID.Valid || uint64(start.PortID.Int64) != active.PortID {
			return ErrOrderConflict
		}
		var port devicePortRow
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND device_id = ? AND port_no = ? AND current_order_id = ? AND status = 'charging'", active.PortID, active.DeviceID, active.PortNo, active.OrderNo).Take(&port).Error
		if err != nil {
			return ErrPortUnavailable
		}
		var session [6]byte
		if _, err := rand.Read(session[:]); err != nil {
			return err
		}
		commandID := uuid.NewString()
		command := chargeStopCommandRow{CommandID: commandID, StartCommandID: active.StartCommandID, ChargeOrderID: active.ChargeOrderID,
			OrderNo: active.OrderNo, UserID: active.UserID, DeviceID: active.DeviceID, PortNo: active.PortNo,
			PortID: sql.NullInt64{Int64: int64(active.PortID), Valid: true}, Status: "pending",
			SessionID: sql.NullString{String: hex.EncodeToString(session[:]), Valid: true}}
		if err := tx.Create(&command).Error; err != nil {
			return err
		}
		reservation = StopReservation{CommandID: commandID, OrderNo: active.OrderNo, DeviceID: active.DeviceID, PortNo: active.PortNo,
			Status: "pending", Wire: protocol.Command{Kind: protocol.CommandStop, SessionID: session, Port: active.PortNo}}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return StopReservation{}, err
	}
	return reservation, nil
}

func (s MySQLSink) MarkUserStopSent(ctx context.Context, commandID string) error {
	return s.DB.WithContext(ctx).Model(&chargeStopCommandRow{}).Where("command_id = ? AND status IN ('pending','sent')", commandID).
		Updates(map[string]any{"status": "sent", "sent_at": gorm.Expr("CURRENT_TIMESTAMP(3)")}).Error
}

func (s MySQLSink) PendingUserStops(ctx context.Context) ([]StopReservation, error) {
	var rows []chargeStopCommandRow
	err := s.DB.WithContext(ctx).Where("status = 'pending' OR (status = 'sent' AND sent_at < DATE_SUB(NOW(3), INTERVAL 10 SECOND))").
		Order("created_at").Limit(50).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]StopReservation, 0, len(rows))
	for _, row := range rows {
		wire, err := stopJob(row.CommandID, row.OrderNo, row.DeviceID, row.PortNo, row.SessionID)
		if err != nil {
			return nil, err
		}
		result = append(result, StopReservation{CommandID: row.CommandID, OrderNo: row.OrderNo, DeviceID: row.DeviceID, PortNo: row.PortNo, Status: "pending", Wire: wire.Wire})
	}
	return result, nil
}

func stopReservationFromRow(row chargeStopCommandRow) (StopReservation, error) {
	job, err := stopJob(row.CommandID, row.OrderNo, row.DeviceID, row.PortNo, row.SessionID)
	if err != nil {
		return StopReservation{}, err
	}
	return StopReservation{CommandID: row.CommandID, OrderNo: row.OrderNo, DeviceID: row.DeviceID,
		PortNo: row.PortNo, Status: row.Status, Wire: job.Wire}, nil
}
