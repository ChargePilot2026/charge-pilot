package store

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
)

func TestResolveScanIsReadOnlyAndHidesDisabledDevices(t *testing.T) {
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
	deviceID := "scan-" + uuid.NewString()
	vendorCode := "scan-" + uuid.NewString()
	portCode := deviceID + ":1"
	vendor, err := db.ExecContext(ctx, "INSERT INTO vendor (vendor_code,vendor_name,adapter_class) VALUES (?,?,'dc589')", vendorCode, "Scan Test")
	if err != nil {
		t.Fatal(err)
	}
	vendorID, err := vendor.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, "DELETE FROM vendor WHERE id = ?", vendorID)
	defer db.ExecContext(ctx, "DELETE FROM device WHERE device_id = ?", deviceID)
	defer db.ExecContext(ctx, "DELETE FROM device_port WHERE device_id = ?", deviceID)
	if _, err := db.ExecContext(ctx, "INSERT INTO device (device_id,vendor_id,port_count,station_id,last_seen_at) VALUES (?,?,2,9,NOW(3))", deviceID, vendorID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO device_port (device_id,port_no,port_code) VALUES (?,1,?),(?,2,?)", deviceID, portCode, deviceID, deviceID+":2"); err != nil {
		t.Fatal(err)
	}
	lookup := MySQLSink{DB: testGORMDB(t, db)}
	port, err := lookup.ResolveScan(ctx, portCode)
	if err != nil || port.Kind != "port" || port.StationID != 9 || port.Port == nil || !port.Port.Available || port.Port.PortID != portCode {
		t.Fatalf("port=%+v err=%v", port, err)
	}
	device, err := lookup.ResolveScan(ctx, deviceID)
	if err != nil || device.Kind != "device" || len(device.Ports) != 2 {
		t.Fatalf("device=%+v err=%v", device, err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE device SET status = 'disabled' WHERE device_id = ?", deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := lookup.ResolveScan(ctx, portCode); !errors.Is(err, ErrScanNotFound) {
		t.Fatalf("disabled port err=%v", err)
	}
}
