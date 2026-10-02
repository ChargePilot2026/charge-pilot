package store

import (
	"context"
	"database/sql"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/google/uuid"
	"os"
	"testing"
	"time"
)

func TestLastHeartbeatIsMonotonicAndRegistrationCannotReplaceIt(t *testing.T) {
	address := os.Getenv("TEST_GATEWAY_DATABASE_URL")
	if address == "" {
		t.Skip("disposable gateway MySQL required")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	device := "heartbeat-" + uuid.NewString()
	vendor, err := db.Exec("INSERT INTO vendor(vendor_code,vendor_name,adapter_class) VALUES(?,?,'dc589')", device, "Heartbeat test")
	if err != nil {
		t.Fatal(err)
	}
	vendorID, _ := vendor.LastInsertId()
	defer db.Exec("DELETE FROM vendor WHERE id=?", vendorID)
	defer db.Exec("DELETE FROM device WHERE device_id=?", device)
	defer db.Exec("DELETE FROM device_event WHERE device_id=?", device)
	defer db.Exec("DELETE FROM telemetry WHERE device_id=?", device)
	if _, err := db.Exec("INSERT INTO device(device_id,vendor_id,port_count) VALUES(?,?,1)", device, vendorID); err != nil {
		t.Fatal(err)
	}
	sink := MySQLSink{DB: testGORMDB(t, db)}
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, at := range []time.Time{now, now.Add(-time.Minute)} {
		event := protocol.Event{Protocol: "dc589", DeviceID: device, Type: protocol.Heartbeat, ReceivedAt: at}
		defer db.Exec("DELETE FROM event_outbox WHERE event_id=?", eventKey(event))
		if err := sink.Record(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := sink.Register(ctx, protocol.Registration{Protocol: "dc589", DeviceID: device, ReceivedAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	var heartbeat sql.NullTime
	if err := db.QueryRow("SELECT last_heartbeat_at FROM device WHERE device_id=?", device).Scan(&heartbeat); err != nil {
		t.Fatal(err)
	}
	if !heartbeat.Valid || !heartbeat.Time.Equal(now) {
		t.Fatalf("heartbeat changed by older event/registration: %v", heartbeat)
	}
}
