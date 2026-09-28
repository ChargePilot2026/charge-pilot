package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/store/generated"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var ErrDeviceNotProvisioned = errors.New("device is not provisioned for this protocol")

type MySQLSink struct{ DB *sql.DB }

func (s MySQLSink) Register(ctx context.Context, registration protocol.Registration) error {
	if s.DB == nil {
		return errors.New("gateway database is unavailable")
	}
	queries := gatewaydb.New(s.DB)
	id, err := queries.GetEnabledDevice(ctx, gatewaydb.GetEnabledDeviceParams{DeviceID: registration.DeviceID, VendorCode: registration.Protocol})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDeviceNotProvisioned
	}
	if err != nil {
		return fmt.Errorf("verify device: %w", err)
	}
	err = queries.TouchDevice(ctx, gatewaydb.TouchDeviceParams{
		FirmwareVersion: sql.NullString{String: registration.SoftwareVersion, Valid: true},
		RegisteredAt:    sql.NullTime{Time: registration.ReceivedAt.UTC(), Valid: true},
		LastSeenAt:      sql.NullTime{Time: registration.ReceivedAt.UTC(), Valid: true},
		ID:              id,
	})
	if err != nil {
		return fmt.Errorf("record device registration: %w", err)
	}
	return nil
}

func (s MySQLSink) Record(ctx context.Context, event protocol.Event) error {
	if s.DB == nil {
		return errors.New("gateway database is unavailable")
	}
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal device event: %w", err)
	}
	key := eventKey(event)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	queries := gatewaydb.New(tx)
	inserted, err := queries.InsertDeviceEvent(ctx, gatewaydb.InsertDeviceEventParams{
		EventKey:     key,
		ProtocolName: event.Protocol,
		DeviceID:     event.DeviceID,
		EventType:    string(event.Type),
		PortNo:       event.Port,
		EventJson:    data,
		ReceivedAt:   event.ReceivedAt.UTC(),
	})
	if err != nil {
		return fmt.Errorf("persist device event: %w", err)
	}
	rows, err := inserted.RowsAffected()
	if err != nil {
		return fmt.Errorf("check device event persistence: %w", err)
	}
	if err := queries.InsertDeviceOutbox(ctx, gatewaydb.InsertDeviceOutboxParams{EventID: key, EnvelopeJson: data}); err != nil {
		return fmt.Errorf("queue device event: %w", err)
	}
	if rows == 1 && event.Type == protocol.StartResult {
		if err := applyStartAck(ctx, queries, event); err != nil {
			return fmt.Errorf("apply start acknowledgement: %w", err)
		}
	}
	if rows == 1 && event.Type == protocol.StopResult {
		if err := applyStopAck(ctx, queries, event); err != nil {
			return fmt.Errorf("apply stop acknowledgement: %w", err)
		}
		if err := applyUserStopAck(ctx, queries, event); err != nil {
			return fmt.Errorf("apply user stop acknowledgement: %w", err)
		}
	}
	if event.Type == protocol.Heartbeat {
		if err := queries.TouchDeviceSeen(ctx, gatewaydb.TouchDeviceSeenParams{LastSeenAt: sql.NullTime{Time: event.ReceivedAt.UTC(), Valid: true}, DeviceID: event.DeviceID}); err != nil {
			return fmt.Errorf("touch device heartbeat: %w", err)
		}
	}
	if rows == 1 {
		if err := insertMeasurements(ctx, queries, event); err != nil {
			return fmt.Errorf("persist device telemetry: %w", err)
		}
	}
	return tx.Commit()
}

func applyUserStopAck(ctx context.Context, queries *gatewaydb.Queries, event protocol.Event) error {
	command, err := queries.LockUserStopForAck(ctx, gatewaydb.LockUserStopForAckParams{DeviceID: event.DeviceID, PortNo: event.Port, SessionID: sql.NullString{String: hex.EncodeToString(event.SessionID[:]), Valid: true}})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if event.ResultCode != 0x10 && event.ResultCode != 0x01 {
		return nil
	}
	if command.Status == gatewaydb.ChargeStopCommandStatusSent {
		_, err = queries.MarkUserStopAcked(ctx, command.CommandID)
	}
	return err
}

func applyStartAck(ctx context.Context, queries *gatewaydb.Queries, event protocol.Event) error {
	command, err := queries.LockStartForAck(ctx, gatewaydb.LockStartForAckParams{
		DeviceID: event.DeviceID, PortNo: event.Port,
		SessionID: sql.NullString{String: hex.EncodeToString(event.SessionID[:]), Valid: true},
	})
	if errors.Is(err, sql.ErrNoRows) {
		// Preserve unmatched ACKs for investigation; they cannot change an order.
		return nil
	}
	if err != nil {
		return err
	}
	if !command.PortID.Valid || command.PortID.Int64 <= 0 {
		return ErrOrderConflict
	}
	if event.ResultCode == 0 && command.Status == gatewaydb.ChargeCommandStatusSent {
		changed, err := queries.MarkStartAcked(ctx, gatewaydb.MarkStartAckedParams{CommandID: command.CommandID, AckAt: sql.NullTime{Time: event.ReceivedAt.UTC(), Valid: true}})
		if err != nil {
			return err
		}
		count, err := changed.RowsAffected()
		if err != nil || count != 1 {
			return ErrOrderConflict
		}
		port, err := queries.SetPortCharging(ctx, gatewaydb.SetPortChargingParams{ID: uint64(command.PortID.Int64), CurrentOrderID: sql.NullString{String: command.OrderNo, Valid: true}})
		if err != nil {
			return err
		}
		count, err = port.RowsAffected()
		if err != nil || count != 1 {
			return ErrPortUnavailable
		}
	}
	if event.ResultCode != 0 && (command.Status == gatewaydb.ChargeCommandStatusSent || command.Status == gatewaydb.ChargeCommandStatusStopping) {
		if _, err := queries.MarkStartRejected(ctx, gatewaydb.MarkStartRejectedParams{CommandID: command.CommandID, Error: sql.NullString{String: fmt.Sprintf("device rejected START code %d", event.ResultCode), Valid: true}, ResultCode: sql.NullInt16{Int16: int16(event.ResultCode), Valid: true}, AckAt: sql.NullTime{Time: event.ReceivedAt.UTC(), Valid: true}}); err != nil {
			return err
		}
		if _, err := queries.ReleaseReservedPort(ctx, gatewaydb.ReleaseReservedPortParams{ID: uint64(command.PortID.Int64), CurrentOrderID: sql.NullString{String: command.OrderNo, Valid: true}}); err != nil {
			return err
		}
	}
	return nil
}

func applyStopAck(ctx context.Context, queries *gatewaydb.Queries, event protocol.Event) error {
	command, err := queries.LockStartForStopAck(ctx, gatewaydb.LockStartForStopAckParams{
		DeviceID: event.DeviceID, PortNo: event.Port,
		StopSessionID: sql.NullString{String: hex.EncodeToString(event.SessionID[:]), Valid: true},
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// 0x10 means output stopped; 0x01 means the port was already idle.
	if event.ResultCode != 0x10 && event.ResultCode != 0x01 {
		return nil
	}
	if !command.PortID.Valid || command.PortID.Int64 <= 0 {
		return ErrOrderConflict
	}
	result, err := queries.MarkStartCompensated(ctx, gatewaydb.MarkStartCompensatedParams{CommandID: command.CommandID, AckAt: sql.NullTime{Time: event.ReceivedAt.UTC(), Valid: true}})
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ErrOrderConflict
	}
	port, err := queries.ReleasePortAfterStop(ctx, gatewaydb.ReleasePortAfterStopParams{ID: uint64(command.PortID.Int64), CurrentOrderID: sql.NullString{String: command.OrderNo, Valid: true}})
	if err != nil {
		return err
	}
	count, err = port.RowsAffected()
	if err != nil || count != 1 {
		return ErrPortUnavailable
	}
	return nil
}

func insertMeasurements(ctx context.Context, queries *gatewaydb.Queries, event protocol.Event) error {
	insert := func(port sql.NullInt16, metric, value string) error {
		return queries.InsertTelemetry(ctx, gatewaydb.InsertTelemetryParams{
			DeviceID: event.DeviceID, PortNo: port, Metric: metric,
			ValueNum: value, Ts: event.ReceivedAt.UTC(),
		})
	}
	if event.Type == protocol.Heartbeat {
		if err := insert(sql.NullInt16{}, "signal", decimal.NewFromInt(int64(event.Signal)).String()); err != nil {
			return err
		}
		if event.PortStates != nil {
			if err := insert(sql.NullInt16{}, "voltage_v", decimal.NewFromInt(int64(event.VoltageV)).String()); err != nil {
				return err
			}
			if err := insert(sql.NullInt16{}, "temperature_c", decimal.NewFromInt(int64(event.TemperatureC)).String()); err != nil {
				return err
			}
		}
	}
	for _, port := range event.ChargingPorts {
		id := sql.NullInt16{Int16: int16(port.Port), Valid: true}
		if port.ChargedMWh != 0 {
			if err := insert(id, "meter_kwh", decimal.NewFromInt(int64(port.ChargedMWh)).Shift(-6).StringFixed(6)); err != nil {
				return err
			}
		}
		if err := insert(id, "power_w", decimal.NewFromInt(int64(port.PowerDeciWatts)).Shift(-1).StringFixed(1)); err != nil {
			return err
		}
	}
	if event.Type == protocol.ChargeEnd {
		id := sql.NullInt16{Int16: int16(event.Port), Valid: true}
		return insert(id, "meter_kwh", decimal.NewFromInt(int64(event.EnergyMilliKWh)).Shift(-3).StringFixed(3))
	}
	return nil
}

func eventKey(event protocol.Event) string {
	if event.Type == protocol.Heartbeat || event.Type == protocol.Telemetry {
		return uuid.NewString()
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(event.Protocol))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(event.DeviceID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(event.Type))
	_, _ = hash.Write(event.SessionID[:])
	_, _ = hash.Write(event.RawPayload)
	return hex.EncodeToString(hash.Sum(nil))
}
