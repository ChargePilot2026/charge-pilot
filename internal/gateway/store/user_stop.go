package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	gatewaydb "github.com/ChargePilot2026/charge-pilot/internal/gateway/store/generated"
	"github.com/google/uuid"
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
	row, err := gatewaydb.New(s.DB).GetStopCommandByOrder(ctx, orderNo)
	if err != nil {
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
	} else if !errors.Is(err, sql.ErrNoRows) {
		return StopReservation{}, err
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return StopReservation{}, err
	}
	defer tx.Rollback()
	q := gatewaydb.New(tx)
	start, err := q.LockStartForUserStop(ctx, gatewaydb.LockStartForUserStopParams{ChargeOrderID: active.ChargeOrderID, OrderNo: active.OrderNo, UserID: active.UserID})
	if errors.Is(err, sql.ErrNoRows) {
		return StopReservation{}, ErrOrderConflict
	}
	if err != nil {
		return StopReservation{}, err
	}
	if start.Status != gatewaydb.ChargeCommandStatusAcked || start.CommandID != active.StartCommandID || start.DeviceID != active.DeviceID || start.PortNo != active.PortNo || !start.PortID.Valid || uint64(start.PortID.Int64) != active.PortID {
		return StopReservation{}, ErrOrderConflict
	}
	if _, err := q.LockOwnedChargingPort(ctx, gatewaydb.LockOwnedChargingPortParams{ID: active.PortID, DeviceID: active.DeviceID, PortNo: active.PortNo, CurrentOrderID: sql.NullString{String: active.OrderNo, Valid: true}}); err != nil {
		return StopReservation{}, ErrPortUnavailable
	}
	var session [6]byte
	if _, err := rand.Read(session[:]); err != nil {
		return StopReservation{}, err
	}
	commandID := uuid.NewString()
	if err := q.InsertUserStopCommand(ctx, gatewaydb.InsertUserStopCommandParams{CommandID: commandID, StartCommandID: active.StartCommandID, ChargeOrderID: active.ChargeOrderID, OrderNo: active.OrderNo, UserID: active.UserID, DeviceID: active.DeviceID, PortNo: active.PortNo, PortID: active.PortID, SessionID: sql.NullString{String: hex.EncodeToString(session[:]), Valid: true}}); err != nil {
		return StopReservation{}, err
	}
	if err := tx.Commit(); err != nil {
		return StopReservation{}, err
	}
	return StopReservation{CommandID: commandID, OrderNo: active.OrderNo, DeviceID: active.DeviceID, PortNo: active.PortNo, Status: "pending", Wire: protocol.Command{Kind: protocol.CommandStop, SessionID: session, Port: active.PortNo}}, nil
}

func (s MySQLSink) MarkUserStopSent(ctx context.Context, commandID string) error {
	_, err := gatewaydb.New(s.DB).MarkUserStopSent(ctx, commandID)
	return err
}

func (s MySQLSink) PendingUserStops(ctx context.Context) ([]StopReservation, error) {
	rows, err := gatewaydb.New(s.DB).PendingUserStops(ctx)
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

func stopReservationFromRow(row gatewaydb.GetStopCommandByOrderRow) (StopReservation, error) {
	job, err := stopJob(row.CommandID, row.OrderNo, row.DeviceID, row.PortNo, row.SessionID)
	if err != nil {
		return StopReservation{}, err
	}
	return StopReservation{CommandID: row.CommandID, OrderNo: row.OrderNo, DeviceID: row.DeviceID, PortNo: row.PortNo, Status: string(row.Status), Wire: job.Wire}, nil
}
