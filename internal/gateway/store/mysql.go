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
	err = queries.InsertDeviceEvent(ctx, gatewaydb.InsertDeviceEventParams{
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
	if err := queries.InsertDeviceOutbox(ctx, gatewaydb.InsertDeviceOutboxParams{EventID: key, EnvelopeJson: data}); err != nil {
		return fmt.Errorf("queue device event: %w", err)
	}
	return tx.Commit()
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
