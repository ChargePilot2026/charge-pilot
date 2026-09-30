package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
)

func TestChargeEndReplayStoresOneMeterAndOutboxEvent(t *testing.T) {
	url := os.Getenv("TEST_GATEWAY_DATABASE_URL")
	if url == "" {
		t.Skip("set a disposable gateway database URL")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	deviceID := "test-" + uuid.NewString()
	defer db.ExecContext(ctx, "DELETE FROM telemetry WHERE device_id = ?", deviceID)
	defer db.ExecContext(ctx, "DELETE FROM device_event WHERE device_id = ?", deviceID)
	keyEvent := protocol.Event{
		Protocol: "dc589", DeviceID: deviceID, Type: protocol.ChargeEnd,
		Port: 5, OrderNumber: "0000000000000123", EnergyMilliKWh: 1000,
		ReceivedAt: time.Now().UTC(), RawPayload: []byte{1, 2, 3},
	}
	key := eventKey(keyEvent)
	defer db.ExecContext(ctx, "DELETE FROM event_outbox WHERE event_id = ?", key)
	for range 2 {
		if err := (MySQLSink{DB: testGORMDB(t, db)}).Record(ctx, keyEvent); err != nil {
			t.Fatal(err)
		}
	}
	var eventCount, outboxCount, meterCount int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM device_event WHERE event_key = ?", key).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM event_outbox WHERE event_id = ?", key).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	var kwh string
	if err := db.QueryRowContext(ctx, "SELECT count(*), CAST(MAX(value_num) AS CHAR) FROM telemetry WHERE device_id = ? AND metric = 'meter_kwh'", deviceID).Scan(&meterCount, &kwh); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 || outboxCount != 1 || meterCount != 1 || kwh != "1.000000" {
		t.Fatalf("events=%d outbox=%d meters=%d kwh=%s", eventCount, outboxCount, meterCount, kwh)
	}
}

// The outbox is the only way a device event leaves the gateway, and every
// consumer reads it as the platform-wide envelope. Shipping the bare event
// instead meant the type a consumer looks for was simply absent — the event
// names it "Type" — and the whole event class was discarded downstream.
func TestDeviceEventOutboxUsesTheSharedEnvelope(t *testing.T) {
	url := os.Getenv("TEST_GATEWAY_DATABASE_URL")
	if url == "" {
		t.Skip("set a disposable gateway database URL")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	deviceID := "env-" + uuid.NewString()
	defer db.ExecContext(ctx, "DELETE FROM device_event WHERE device_id = ?", deviceID)
	event := protocol.Event{
		Protocol: "dc589", DeviceID: deviceID, Type: protocol.ChargeEnd, Port: 1,
		OrderNumber: "0000000000000042", EnergyMilliKWh: 3, ReceivedAt: time.Now().UTC(),
		RawPayload: []byte{0xBB, 0x01},
	}
	key := eventKey(event)
	defer db.ExecContext(ctx, "DELETE FROM event_outbox WHERE event_id = ?", key)
	if err := (MySQLSink{DB: testGORMDB(t, db)}).Record(ctx, event); err != nil {
		t.Fatal(err)
	}
	var envelope, stored string
	if err := db.QueryRowContext(ctx, "SELECT envelope_json FROM event_outbox WHERE event_id = ?", key).Scan(&envelope); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT event_json FROM device_event WHERE event_key = ?", key).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		EventID   string          `json:"event_id"`
		EventType string          `json:"event_type"`
		Source    string          `json:"source"`
		Data      json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(envelope), &decoded); err != nil {
		t.Fatalf("the outbox envelope is not the shape consumers parse: %v (%s)", err, envelope)
	}
	if decoded.EventType != string(protocol.ChargeEnd) {
		t.Fatalf("envelope event_type = %q, want %q", decoded.EventType, protocol.ChargeEnd)
	}
	if decoded.EventID != key || decoded.Source != "gateway" {
		t.Fatalf("envelope is missing its identity: %+v", decoded)
	}
	// The payload is compared after decoding rather than as text: the column is
	// a native JSON type, so MySQL re-formats what it stores and a byte
	// comparison would be asserting on its spacing.
	var carried struct {
		OrderNumber string `json:"OrderNumber"`
		RawPayload  []byte `json:"RawPayload"`
	}
	if err := json.Unmarshal(decoded.Data, &carried); err != nil {
		t.Fatal(err)
	}
	if carried.OrderNumber != "0000000000000042" {
		t.Fatalf("the envelope carries no device event payload: %s", decoded.Data)
	}
	var kept struct {
		OrderNumber string `json:"OrderNumber"`
		RawPayload  []byte `json:"RawPayload"`
	}
	if err := json.Unmarshal([]byte(stored), &kept); err != nil {
		t.Fatal(err)
	}
	// The replay record must stay exactly what the board sent, frame and all.
	if kept.RawPayload[0] != 0xBB || kept.RawPayload[1] != 0x01 {
		t.Fatalf("device_event.event_json lost the frame the board sent: %x", kept.RawPayload)
	}
}
