package control

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func processControlTransaction(t *testing.T) *gorm.DB {
	t.Helper()
	address := os.Getenv("TEST_GATEWAY_DATABASE_URL")
	if address == "" {
		t.Skip("set a gateway MySQL URL")
	}
	db, err := dbconn.Open(t.Context(), address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tx := testGORMDB(t, db).Begin(&sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	return tx
}

type processResponse struct {
	Items       []ChargeProcessPoint `json:"items"`
	NextAfterID *uint64              `json:"next_after_id"`
}

func getProcess(t *testing.T, r *gin.Engine, path string) processResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("X-Service-Token", "service")
	r.ServeHTTP(w, req)
	var response struct{ Data processResponse }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil {
		t.Fatalf("response=%d %s", w.Code, w.Body.String())
	}
	return response.Data
}

func TestChargeProcessCursorReturnsEverySampleAndStrictOrderIdentity(t *testing.T) {
	tx := processControlTransaction(t)
	order, device := "page-"+uuid.NewString(), "device-"+uuid.NewString()
	base := time.Now().UTC().Truncate(time.Millisecond)
	for i := range 8 {
		identityOrder, identityDevice, identityID, identityPort := order, device, uint64(7123), uint8(1)
		switch i {
		case 5:
			identityOrder = "other-" + uuid.NewString()
		case 6:
			identityID = 7124
		case 7:
			identityDevice, identityPort = "other-"+uuid.NewString(), 2
		}
		row := map[string]any{"event_key": uuid.NewString(), "charge_order_id": identityID,
			"order_no": identityOrder, "device_id": identityDevice, "port_no": identityPort,
			// Deliberately descending timestamps: cursor ordering must be by ID.
			"ts": base.Add(-time.Duration(i) * time.Second), "power_deciwatts": i * 125,
			"charged_seconds": i * 15, "remaining_seconds": max(0, 100-i*15),
			"charged_mwh": i * 23000, "remaining_mwh": (5 - min(i, 5)) * 123000,
			"signal_strength": 0, "port_status": nil, "voltage_v": nil, "temperature_c": nil, "device_status": nil}
		if i == 1 {
			row["port_status"], row["voltage_v"], row["temperature_c"], row["device_status"] = 0, 0, -3, 0
		}
		if err := tx.Table("charge_process").Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	r := httpapi.NewRouter()
	(TelemetryAPI{DB: tx, ServiceToken: "service"}).Register(r)
	path := fmt.Sprintf("/api/v1/internal/charge-orders/%s/process?charge_order_id=7123&device_id=%s&port_no=1&limit=2", order, device)
	all := []ChargeProcessPoint{}
	after := uint64(0)
	for range 4 {
		page := getProcess(t, r, path+fmt.Sprintf("&after_id=%d", after))
		all = append(all, page.Items...)
		if page.NextAfterID == nil {
			break
		}
		if len(page.Items) != 2 || *page.NextAfterID != page.Items[1].ID || *page.NextAfterID <= after {
			t.Fatalf("invalid cursor page: %+v", page)
		}
		after = *page.NextAfterID
	}
	if len(all) != 5 {
		t.Fatalf("truncated or mixed order identity: %+v", all)
	}
	for i, point := range all {
		if i > 0 && point.ID <= all[i-1].ID {
			t.Fatalf("cursor repeated or reordered: %+v", all)
		}
		if point.PowerW != float64(i)*12.5 || point.ChargedKWh != float64(i*23000)/1e6 || point.RemainingKWh != float64((5-i)*123000)/1e6 || point.ChargedSeconds != uint32(i*15) || point.RemainingSeconds != uint32(100-i*15) || point.SignalStrength != 0 || point.TS.Location() != time.UTC {
			t.Fatalf("unit conversion/zero/UTC failure at %d: %+v", i, point)
		}
	}
	if all[0].PortStatus != nil || all[0].VoltageV != nil || all[0].TemperatureC != nil || all[0].DeviceStatus != nil || all[1].PortStatus == nil || *all[1].PortStatus != 0 || all[1].VoltageV == nil || *all[1].VoltageV != 0 || all[1].TemperatureC == nil || *all[1].TemperatureC != -3 || all[1].DeviceStatus == nil || *all[1].DeviceStatus != 0 {
		t.Fatalf("optional readings changed null/zero: %+v", all[:2])
	}
	empty := getProcess(t, r, path+fmt.Sprintf("&after_id=%d", all[len(all)-1].ID))
	if empty.Items == nil || len(empty.Items) != 0 || empty.NextAfterID != nil {
		t.Fatalf("empty result missing stable shape: %+v", empty)
	}
	// No arbitrary default page-size truncation of this complete small order.
	full := getProcess(t, r, fmt.Sprintf("/api/v1/internal/charge-orders/%s/process?charge_order_id=7123&device_id=%s&port_no=1", order, device))
	if len(full.Items) != 5 || full.NextAfterID != nil {
		t.Fatalf("default pagination incorrect: %+v", full)
	}
}

func TestDeviceSummariesExposeAllReportedPortsAndNullableZeroSignal(t *testing.T) {
	tx := processControlTransaction(t)
	device, emptyDevice := "summary-"+uuid.NewString(), "empty-"+uuid.NewString()
	at := time.Now().UTC().Truncate(time.Millisecond)
	var vendor struct{ ID uint64 }
	if err := tx.Exec("INSERT INTO vendor(vendor_code,vendor_name,adapter_class) VALUES(?,?,'dc589')", uuid.NewString(), "Process summary fixture").Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Raw("SELECT LAST_INSERT_ID() AS id").Scan(&vendor).Error; err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{device, emptyDevice} {
		row := map[string]any{"device_id": id, "vendor_id": vendor.ID, "port_count": 3}
		if i == 0 {
			row["signal_strength"], row["signal_at"], row["last_heartbeat_at"] = 0, at, at
		}
		if err := tx.Table("device").Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 3; i++ {
		row := map[string]any{"device_id": device, "port_no": i, "port_code": uuid.NewString()}
		if i == 1 {
			row["reported_status"], row["reported_status_at"] = 0, at
		}
		if i == 3 {
			row["deleted_at"] = at
		}
		if err := tx.Table("device_port").Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	r := httpapi.NewRouter()
	(DeviceSummaryAPI{DB: tx, ServiceToken: "service"}).Register(r)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/internal/device-summaries?device_id=%s&device_id=%s", device, emptyDevice), nil)
	req.Header.Set("X-Service-Token", "service")
	r.ServeHTTP(w, req)
	var response struct {
		Data struct{ Items []DeviceSummary }
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Data.Items) != 2 {
		t.Fatalf("summary=%d %s", w.Code, w.Body.String())
	}
	byID := map[string]DeviceSummary{}
	for _, row := range response.Data.Items {
		byID[row.DeviceID] = row
	}
	reported, blank := byID[device], byID[emptyDevice]
	if reported.SignalStrength == nil || *reported.SignalStrength != 0 || reported.SignalAt == nil || !reported.SignalAt.Equal(at) || len(reported.Ports) != 2 || reported.Ports[0].PortNo != 1 || reported.Ports[0].StatusCode == nil || *reported.Ports[0].StatusCode != 0 || reported.Ports[0].StatusAt == nil || !reported.Ports[0].StatusAt.Equal(at) || reported.Ports[1].PortNo != 2 || reported.Ports[1].StatusCode != nil || reported.Ports[1].StatusAt != nil {
		t.Fatalf("reported summary lost zero/null/deleted port: %+v", reported)
	}
	if blank.SignalStrength != nil || blank.SignalAt != nil || blank.Ports == nil || len(blank.Ports) != 0 {
		t.Fatalf("never-reported device invented state: %+v", blank)
	}
}
