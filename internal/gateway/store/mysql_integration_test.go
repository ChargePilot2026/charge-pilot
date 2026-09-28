package store

import (
	"context"
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
