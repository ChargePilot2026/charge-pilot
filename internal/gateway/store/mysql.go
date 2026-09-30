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
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrDeviceNotProvisioned = errors.New("device is not provisioned for this protocol")

type MySQLSink struct{ DB *gorm.DB }

func (s MySQLSink) Register(ctx context.Context, registration protocol.Registration) error {
	if s.DB == nil {
		return errors.New("gateway database is unavailable")
	}
	var device deviceRow
	// The vendor is matched on its adapter class, not on its business code.
	// vendor_code is an operator-facing identifier ("V-DC589") and says nothing
	// about what a device speaks; adapter_class is the field that names the
	// dialect, and it is the same field the provisioning endpoint checks before
	// it will create a device. Matching vendor_code against the protocol name
	// instead meant no provisioned device could ever register, which stayed
	// invisible because the listener discarded the resulting error.
	err := s.DB.WithContext(ctx).Table("device AS d").Select("d.id").
		Joins("JOIN vendor AS v ON v.id = d.vendor_id").
		Where("d.device_id = ? AND d.status = 'enabled' AND d.deleted_at IS NULL AND v.adapter_class = ? AND v.status = 'enabled' AND v.deleted_at IS NULL", registration.DeviceID, registration.Protocol).
		Take(&device).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrDeviceNotProvisioned
	}
	if err != nil {
		return fmt.Errorf("verify device: %w", err)
	}
	now := registration.ReceivedAt.UTC()
	result := s.DB.WithContext(ctx).Model(&deviceRow{}).Where("id = ?", device.ID).Updates(map[string]any{
		"firmware_version": registration.SoftwareVersion,
		"registered_at":    gorm.Expr("COALESCE(registered_at, ?)", now),
		"last_seen_at":     now,
	})
	if result.Error != nil {
		return fmt.Errorf("record device registration: %w", result.Error)
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
	// The outbox carries the system-wide envelope — event_id, event_type,
	// source, occurred_at and a data payload — because that is the shape every
	// consumer parses and the shape the rest of the platform publishes in.
	// Shipping the bare event instead meant nothing downstream could read it: the
	// event's own field is "Type", not "event_type", so a consumer looking for
	// the type found nothing and the entry was discarded.
	//
	// device_event.event_json keeps the event exactly as it arrived, because
	// that column is the replay record and re-encoding it would mean the thing
	// you replay is no longer the thing the board sent.
	envelope, err := json.Marshal(map[string]any{
		"event_id":    key,
		"event_type":  string(event.Type),
		"source":      "gateway",
		"occurred_at": event.ReceivedAt.UTC(),
		"data":        json.RawMessage(data),
	})
	if err != nil {
		return fmt.Errorf("marshal device event envelope: %w", err)
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		inserted := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&deviceEventRow{
			EventKey: key, Protocol: event.Protocol, DeviceID: event.DeviceID,
			EventType: string(event.Type), PortNo: event.Port, EventJSON: data, ReceivedAt: event.ReceivedAt.UTC(),
		})
		if inserted.Error != nil {
			return fmt.Errorf("persist device event: %w", inserted.Error)
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&deviceOutboxRow{EventID: key, Stream: "device_event_stream", EnvelopeJSON: envelope}).Error; err != nil {
			return fmt.Errorf("queue device event: %w", err)
		}
		if inserted.RowsAffected == 1 && event.Type == protocol.StartResult {
			if err := applyStartAck(ctx, tx, event); err != nil {
				return fmt.Errorf("apply start acknowledgement: %w", err)
			}
		}
		if inserted.RowsAffected == 1 && event.Type == protocol.StopResult {
			if err := applyStopAck(ctx, tx, event); err != nil {
				return fmt.Errorf("apply stop acknowledgement: %w", err)
			}
			if err := applyUserStopAck(ctx, tx, event); err != nil {
				return fmt.Errorf("apply user stop acknowledgement: %w", err)
			}
		}
		if event.Type == protocol.Heartbeat {
			if err := tx.Model(&deviceRow{}).Where("device_id = ? AND status = 'enabled' AND deleted_at IS NULL", event.DeviceID).
				Update("last_seen_at", event.ReceivedAt.UTC()).Error; err != nil {
				return fmt.Errorf("touch device heartbeat: %w", err)
			}
		}
		if inserted.RowsAffected == 1 {
			if err := insertMeasurements(ctx, tx, event); err != nil {
				return fmt.Errorf("persist device telemetry: %w", err)
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

// stopAckAccepted reports whether a stop reply means the charge is over.
//
// 0x10 is a clean stop and 0x01 means the port was already idle, which reaches
// the same end state. 0x00 (no such port) and 0x04 (port faulted) do not: the
// platform asked a charge to end and was told it did not, and that has to be
// recorded rather than dropped on the floor.
func stopAckAccepted(code uint8) bool { return code == 0x10 || code == 0x01 }

func applyUserStopAck(ctx context.Context, tx *gorm.DB, event protocol.Event) error {
	var command chargeStopCommandRow
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id = ? AND port_no = ? AND session_id = ?", event.DeviceID, event.Port, hex.EncodeToString(event.SessionID[:])).Take(&command).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if stopAckAccepted(event.ResultCode) {
		if command.Status == "sent" {
			return tx.Model(&chargeStopCommandRow{}).Where("command_id = ? AND status = 'sent'", command.CommandID).
				Updates(map[string]any{"status": "acked", "result_code": int16(event.ResultCode)}).Error
		}
		return nil
	}
	// The device refused. Without this the row would sit in "sent" for ever and
	// nothing would distinguish a charge that is still running from one that
	// was never asked to stop.
	if command.Status == "sent" {
		return tx.Model(&chargeStopCommandRow{}).Where("command_id = ? AND status = 'sent'", command.CommandID).
			Updates(map[string]any{
				"status":      "rejected",
				"result_code": int16(event.ResultCode),
				"rejected_at": event.ReceivedAt.UTC(),
			}).Error
	}
	return nil
}

func applyStartAck(ctx context.Context, tx *gorm.DB, event protocol.Event) error {
	var command chargeCommandRow
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id = ? AND port_no = ? AND session_id = ?", event.DeviceID, event.Port, hex.EncodeToString(event.SessionID[:])).Take(&command).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Preserve unmatched ACKs for investigation; they cannot change an order.
		return nil
	}
	if err != nil {
		return err
	}
	if !command.PortID.Valid || command.PortID.Int64 <= 0 {
		return ErrOrderConflict
	}
	if event.ResultCode == 0 && command.Status == "sent" {
		changed := tx.Model(&chargeCommandRow{}).Where("command_id = ? AND status = 'sent'", command.CommandID).
			Updates(map[string]any{"status": "acked", "error": nil, "result_code": 0, "ack_at": event.ReceivedAt.UTC()})
		if err := requireOne(changed); err != nil {
			return ErrOrderConflict
		}
		port := tx.Model(&devicePortRow{}).Where("id = ? AND current_order_id = ? AND status = 'idle'", command.PortID.Int64, command.OrderNo).Update("status", "charging")
		if err := requireOne(port); err != nil {
			return ErrPortUnavailable
		}
	}
	if event.ResultCode != 0 && (command.Status == "sent" || command.Status == "stopping") {
		updated := tx.Model(&chargeCommandRow{}).Where("command_id = ? AND status IN ('sent','stopping')", command.CommandID).
			Updates(map[string]any{"status": "rejected", "error": fmt.Sprintf("device rejected START code %d", event.ResultCode), "result_code": event.ResultCode, "ack_at": event.ReceivedAt.UTC()})
		if updated.Error != nil {
			return updated.Error
		}
		return tx.Model(&devicePortRow{}).Where("id = ? AND current_order_id = ? AND status = 'idle'", command.PortID.Int64, command.OrderNo).
			Update("current_order_id", nil).Error
	}
	return nil
}

func applyStopAck(ctx context.Context, tx *gorm.DB, event protocol.Event) error {
	var command chargeCommandRow
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_id = ? AND port_no = ? AND stop_session_id = ? AND status = 'stopping'", event.DeviceID, event.Port, hex.EncodeToString(event.SessionID[:])).Take(&command).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	// 0x10 means output stopped; 0x01 means the port was already idle. A refusal
	// leaves the command in "stopping", because the port really is still
	// charging and pretending otherwise would free a port that is in use.
	if !stopAckAccepted(event.ResultCode) {
		return nil
	}
	if !command.PortID.Valid || command.PortID.Int64 <= 0 {
		return ErrOrderConflict
	}
	changed := tx.Model(&chargeCommandRow{}).Where("command_id = ? AND status = 'stopping'", command.CommandID).
		Updates(map[string]any{"status": "rejected", "result_code": 254, "ack_at": event.ReceivedAt.UTC(), "error": "STOP compensation confirmed"})
	if err := requireOne(changed); err != nil {
		return ErrOrderConflict
	}
	port := tx.Model(&devicePortRow{}).Where("id = ? AND current_order_id = ? AND status IN ('idle','charging')", command.PortID.Int64, command.OrderNo).
		Updates(map[string]any{"status": "idle", "current_order_id": nil})
	if err := requireOne(port); err != nil {
		return ErrPortUnavailable
	}
	return nil
}

func insertMeasurements(ctx context.Context, tx *gorm.DB, event protocol.Event) error {
	// Collected as we go so the rollups can be written in one statement per
	// granularity instead of one per metric.
	rollup := make([]AggregateSample, 0, 8)
	insert := func(port sql.NullInt16, metric, value string) error {
		rollup = append(rollup, AggregateSample{DeviceID: event.DeviceID, Port: port, Metric: metric,
			Value: value, TS: event.ReceivedAt.UTC()})
		return tx.WithContext(ctx).Create(&telemetryRow{DeviceID: event.DeviceID, PortNo: port, Metric: metric,
			ValueNum: value, TS: event.ReceivedAt.UTC()}).Error
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
		if err := insert(id, "meter_kwh", decimal.NewFromInt(int64(event.EnergyMilliKWh)).Shift(-3).StringFixed(3)); err != nil {
			return err
		}
	}
	return refreshAggregates(ctx, tx, rollup)
}

func requireOne(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
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
