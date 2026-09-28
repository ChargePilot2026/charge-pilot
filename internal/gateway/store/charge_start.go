package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrPortUnavailable = errors.New("device port is unavailable")
var ErrOrderConflict = errors.New("charge command identity conflict")

type PaidOrder struct {
	ChargeOrderID  uint64 `json:"charge_order_id"`
	PaymentOrderID uint64 `json:"payment_order_id"`
	OrderNo        string `json:"order_no"`
	UserID         uint64 `json:"user_id"`
	DeviceID       string `json:"device_id"`
	PortNo         uint8  `json:"port_no"`
	ChargeMode     uint8  `json:"charge_mode"`
	ChargeQuantity uint16 `json:"charge_quantity"`
}

type StartReservation struct {
	CommandID string           `json:"command_id"`
	Status    string           `json:"status"`
	OrderNo   string           `json:"order_no"`
	DeviceID  string           `json:"device_id"`
	PortNo    uint8            `json:"port_no"`
	Wire      protocol.Command `json:"-"`
	StopWire  protocol.Command `json:"-"`
}

// ReserveStart owns the port in gateway_db before any socket write. Existing
// commands are returned unchanged so retries never create a second START.
func (s MySQLSink) ReserveStart(ctx context.Context, paid PaidOrder) (StartReservation, error) {
	if s.DB == nil || paid.ChargeOrderID == 0 || paid.PaymentOrderID == 0 || paid.OrderNo == "" || paid.UserID == 0 || paid.DeviceID == "" || paid.PortNo == 0 || paid.ChargeQuantity == 0 {
		return StartReservation{}, ErrOrderConflict
	}
	mode, quantity := paid.ChargeMode, paid.ChargeQuantity
	if mode != 0 && mode != 1 && mode != 4 && mode != 10 && mode != 11 && mode != 12 {
		return StartReservation{}, ErrOrderConflict
	}
	if mode != 1 && mode != 11 && quantity > 600 {
		return StartReservation{}, ErrOrderConflict
	}
	orderBCD, err := numericOrderBCD(paid.ChargeOrderID)
	if err != nil {
		return StartReservation{}, err
	}
	var existing chargeCommandRow
	err = s.DB.WithContext(ctx).Where("order_no = ?", paid.OrderNo).Take(&existing).Error
	if err == nil {
		return reservationFromRow(existing, paid, orderBCD, mode, quantity)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return StartReservation{}, err
	}

	var reservation StartReservation
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var port devicePortRow
		err := tx.Table("device_port AS p").Select("p.id, p.port_code").
			Joins("JOIN device AS d ON d.device_id = p.device_id").
			Joins("JOIN vendor AS v ON v.id = d.vendor_id").
			Where(`p.device_id = ? AND p.port_no = ? AND p.deleted_at IS NULL AND p.status = 'idle' AND p.current_order_id IS NULL
				AND d.status = 'enabled' AND d.deleted_at IS NULL AND v.status = 'enabled' AND v.deleted_at IS NULL`, paid.DeviceID, paid.PortNo).
			Clauses(clause.Locking{Strength: "UPDATE"}).Take(&port).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPortUnavailable
		}
		if err != nil {
			return err
		}
		commandID, stopID := uuid.NewString(), uuid.NewString()
		var startSession, stopSession [6]byte
		if _, err := rand.Read(startSession[:]); err != nil {
			return err
		}
		if _, err := rand.Read(stopSession[:]); err != nil {
			return err
		}
		reserved := tx.Model(&devicePortRow{}).Where("id = ? AND status = 'idle' AND current_order_id IS NULL", port.ID).
			Update("current_order_id", paid.OrderNo)
		if err := requireOne(reserved); err != nil {
			return ErrPortUnavailable
		}
		command := chargeCommandRow{CommandID: commandID, StopCommandID: stopID,
			ChargeOrderID: paid.ChargeOrderID, PaymentOrderID: paid.PaymentOrderID, OrderNo: paid.OrderNo,
			UserID: paid.UserID, DeviceID: paid.DeviceID, PortNo: paid.PortNo, PortCode: port.PortCode,
			PortID: sql.NullInt64{Int64: int64(port.ID), Valid: true}, OwnsPort: true, Status: "pending",
			SessionID:     sql.NullString{String: hex.EncodeToString(startSession[:]), Valid: true},
			StopSessionID: sql.NullString{String: hex.EncodeToString(stopSession[:]), Valid: true}, ChargeMode: mode, Quantity: quantity}
		if err := tx.Create(&command).Error; err != nil {
			return fmt.Errorf("insert charge command: %w", err)
		}
		reservation = StartReservation{CommandID: commandID, Status: "pending", OrderNo: paid.OrderNo,
			DeviceID: paid.DeviceID, PortNo: paid.PortNo,
			Wire:     protocol.Command{Kind: protocol.CommandStart, SessionID: startSession, Port: paid.PortNo, OrderBCD: orderBCD, Mode: mode, Quantity: quantity},
			StopWire: protocol.Command{Kind: protocol.CommandStop, SessionID: stopSession, Port: paid.PortNo}}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if errors.Is(err, ErrPortUnavailable) {
		// A racing request for the same paid order may have acquired the port.
		var raced chargeCommandRow
		lookupErr := s.DB.WithContext(ctx).Where("order_no = ?", paid.OrderNo).Take(&raced).Error
		if lookupErr == nil {
			return reservationFromRow(raced, paid, orderBCD, mode, quantity)
		}
	}
	return reservation, err
}

func reservationFromRow(row chargeCommandRow, paid PaidOrder, orderBCD [8]byte, mode uint8, quantity uint16) (StartReservation, error) {
	if row.ChargeOrderID != paid.ChargeOrderID || row.PaymentOrderID != paid.PaymentOrderID || row.UserID != paid.UserID || row.DeviceID != paid.DeviceID || row.PortNo != paid.PortNo || row.ChargeMode != mode || row.Quantity != quantity {
		return StartReservation{}, ErrOrderConflict
	}
	var session [6]byte
	if !row.SessionID.Valid {
		return StartReservation{}, ErrOrderConflict
	}
	value, err := hex.DecodeString(row.SessionID.String)
	if err != nil || len(value) != 6 {
		return StartReservation{}, ErrOrderConflict
	}
	copy(session[:], value)
	if !row.StopSessionID.Valid {
		return StartReservation{}, ErrOrderConflict
	}
	stopValue, err := hex.DecodeString(row.StopSessionID.String)
	if err != nil || len(stopValue) != 6 {
		return StartReservation{}, ErrOrderConflict
	}
	var stopSession [6]byte
	copy(stopSession[:], stopValue)
	return StartReservation{CommandID: row.CommandID, Status: row.Status, OrderNo: paid.OrderNo,
		DeviceID: paid.DeviceID, PortNo: paid.PortNo,
		Wire:     protocol.Command{Kind: protocol.CommandStart, SessionID: session, Port: paid.PortNo, OrderBCD: orderBCD, Mode: mode, Quantity: quantity},
		StopWire: protocol.Command{Kind: protocol.CommandStop, SessionID: stopSession, Port: paid.PortNo}}, nil
}

func (s MySQLSink) MarkStartSent(ctx context.Context, commandID string) (bool, error) {
	result := s.DB.WithContext(ctx).Model(&chargeCommandRow{}).Where("command_id = ? AND status = 'pending'", commandID).
		Updates(map[string]any{"status": "sent", "sent_at": gorm.Expr("CURRENT_TIMESTAMP(3)")})
	return result.RowsAffected == 1, result.Error
}

func (s MySQLSink) StartStatus(ctx context.Context, orderNo string) (string, error) {
	var row chargeCommandRow
	if err := s.DB.WithContext(ctx).Select("status").Where("order_no = ?", orderNo).Take(&row).Error; err != nil {
		return "", err
	}
	return row.Status, nil
}

func (s MySQLSink) MarkStartStopping(ctx context.Context, commandID string, cause error) error {
	message := "uncertain TCP write"
	if cause != nil {
		message = cause.Error()
	}
	if len(message) > 255 {
		message = message[:255]
	}
	return s.DB.WithContext(ctx).Model(&chargeCommandRow{}).Where("command_id = ? AND status IN ('sent','acked')", commandID).
		Updates(map[string]any{"status": "stopping", "error": message}).Error
}

func numericOrderBCD(orderID uint64) ([8]byte, error) {
	var result [8]byte
	value := strconv.FormatUint(orderID, 10)
	if len(value) > 16 {
		return result, ErrOrderConflict
	}
	value = strings.Repeat("0", 16-len(value)) + value
	for i := range result {
		a, b := value[i*2], value[i*2+1]
		result[i] = (a-'0')<<4 | (b - '0')
	}
	return result, nil
}
