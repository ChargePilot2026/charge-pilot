package control

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
)

func TestPortTelemetryIsolatedWithSharedBoardReadings(t *testing.T) {
	url := os.Getenv("TEST_GATEWAY_DATABASE_URL")
	if url == "" {
		t.Skip("set disposable gateway database URL")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	device := "port-isolation-" + uuid.NewString()
	defer db.ExecContext(ctx, "DELETE FROM telemetry WHERE device_id=?", device)
	at := time.Now().UTC().Truncate(time.Second)
	for _, row := range []struct {
		port   sql.NullInt16
		metric string
		value  int
	}{
		{sql.NullInt16{Int16: 1, Valid: true}, "power_w", 150},
		{sql.NullInt16{Int16: 2, Valid: true}, "power_w", 350},
		{sql.NullInt16{Int16: 1, Valid: true}, "meter_kwh", 0},
		{sql.NullInt16{}, "voltage_v", 220},
	} {
		if _, err := db.ExecContext(ctx, "INSERT INTO telemetry(device_id,port_no,metric,value_num,ts) VALUES(?,?,?,?,?)", device, row.port, row.metric, row.value, at); err != nil {
			t.Fatal(err)
		}
	}
	points, err := (TelemetryAPI{DB: testGORMDB(t, db)}).readPort(ctx, device, 1, at.Add(-time.Second), at.Add(time.Second), 10, "telemetry")
	if err != nil || len(points) != 1 {
		t.Fatalf("points=%+v err=%v", points, err)
	}
	p := points[0]
	if p.PowerW == nil || *p.PowerW != 150 || p.VoltageV == nil || *p.VoltageV != 220 || p.MeterKWh == nil || *p.MeterKWh != 0 {
		t.Fatalf("cross-port or missing board readings: %+v", p)
	}
}
