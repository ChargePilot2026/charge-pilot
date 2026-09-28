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
	gatewaydb "github.com/ChargePilot2026/charge-pilot/internal/gateway/store/generated"
	"github.com/google/uuid"
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
	if existing, err := gatewaydb.New(s.DB).GetStartCommandByOrder(ctx, paid.OrderNo); err == nil {
		return reservationFromRow(existing, paid, orderBCD, mode, quantity)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return StartReservation{}, err
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return StartReservation{}, err
	}
	defer tx.Rollback()
	q := gatewaydb.New(tx)
	port, err := q.LockAvailablePort(ctx, gatewaydb.LockAvailablePortParams{DeviceID: paid.DeviceID, PortNo: paid.PortNo})
	if errors.Is(err, sql.ErrNoRows) {
		// A racing request for the same paid order may have acquired the port.
		if row, lookupErr := gatewaydb.New(s.DB).GetStartCommandByOrder(ctx, paid.OrderNo); lookupErr == nil {
			return reservationFromRow(row, paid, orderBCD, mode, quantity)
		}
		return StartReservation{}, ErrPortUnavailable
	}
	if err != nil {
		return StartReservation{}, err
	}
	commandID, stopID := uuid.NewString(), uuid.NewString()
	var startSession, stopSession [6]byte
	if _, err := rand.Read(startSession[:]); err != nil {
		return StartReservation{}, err
	}
	if _, err := rand.Read(stopSession[:]); err != nil {
		return StartReservation{}, err
	}
	owned, err := q.ReservePort(ctx, gatewaydb.ReservePortParams{CurrentOrderID: sql.NullString{String: paid.OrderNo, Valid: true}, ID: port.ID})
	if err != nil {
		return StartReservation{}, err
	}
	rows, err := owned.RowsAffected()
	if err != nil {
		return StartReservation{}, err
	}
	if rows != 1 {
		return StartReservation{}, ErrPortUnavailable
	}
	if err := q.InsertStartCommand(ctx, gatewaydb.InsertStartCommandParams{
		CommandID: commandID, StopCommandID: stopID,
		ChargeOrderID: paid.ChargeOrderID, PaymentOrderID: paid.PaymentOrderID,
		OrderNo: paid.OrderNo, UserID: paid.UserID, DeviceID: paid.DeviceID,
		PortNo: paid.PortNo, PortCode: port.PortCode, PortID: sql.NullInt64{Int64: int64(port.ID), Valid: true},
		SessionID:     sql.NullString{String: hex.EncodeToString(startSession[:]), Valid: true},
		StopSessionID: sql.NullString{String: hex.EncodeToString(stopSession[:]), Valid: true},
		ChargeMode:    mode, Quantity: quantity,
	}); err != nil {
		return StartReservation{}, fmt.Errorf("insert charge command: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return StartReservation{}, err
	}
	return StartReservation{CommandID: commandID, Status: "pending", OrderNo: paid.OrderNo, DeviceID: paid.DeviceID, PortNo: paid.PortNo,
		Wire:     protocol.Command{Kind: protocol.CommandStart, SessionID: startSession, Port: paid.PortNo, OrderBCD: orderBCD, Mode: mode, Quantity: quantity},
		StopWire: protocol.Command{Kind: protocol.CommandStop, SessionID: stopSession, Port: paid.PortNo}}, nil
}

func reservationFromRow(row gatewaydb.GetStartCommandByOrderRow, paid PaidOrder, orderBCD [8]byte, mode uint8, quantity uint16) (StartReservation, error) {
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
	return StartReservation{CommandID: row.CommandID, Status: string(row.Status), OrderNo: paid.OrderNo,
		DeviceID: paid.DeviceID, PortNo: paid.PortNo,
		Wire:     protocol.Command{Kind: protocol.CommandStart, SessionID: session, Port: paid.PortNo, OrderBCD: orderBCD, Mode: mode, Quantity: quantity},
		StopWire: protocol.Command{Kind: protocol.CommandStop, SessionID: stopSession, Port: paid.PortNo}}, nil
}

func (s MySQLSink) MarkStartSent(ctx context.Context, commandID string) (bool, error) {
	result, err := gatewaydb.New(s.DB).MarkStartSent(ctx, commandID)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (s MySQLSink) StartStatus(ctx context.Context, orderNo string) (string, error) {
	row, err := gatewaydb.New(s.DB).GetStartCommandByOrder(ctx, orderNo)
	if err != nil {
		return "", err
	}
	return string(row.Status), nil
}

func (s MySQLSink) MarkStartStopping(ctx context.Context, commandID string, cause error) error {
	message := "uncertain TCP write"
	if cause != nil {
		message = cause.Error()
	}
	if len(message) > 255 {
		message = message[:255]
	}
	return gatewaydb.New(s.DB).MarkStartStopping(ctx, gatewaydb.MarkStartStoppingParams{CommandID: commandID, Error: sql.NullString{String: message, Valid: true}})
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
