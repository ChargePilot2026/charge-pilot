package provision

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func vendorIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("TEST_GATEWAY_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_GATEWAY_DATABASE_URL to a migrated disposable gateway database")
	}
	db, err := dbconn.Open(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	var indexes int64
	if err := orm.Raw("SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'vendor' AND index_name = 'uk_vendor_live_code'").Scan(&indexes).Error; err != nil || indexes != 1 {
		t.Fatalf("gateway migration 0010 required: count=%d err=%v", indexes, err)
	}
	return orm
}

func vendorFixtureCode(t *testing.T, db *gorm.DB) string {
	t.Helper()
	code := fmt.Sprintf("VENDOR_TEST_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		for _, statement := range []string{
			"DELETE FROM device_port WHERE device_id IN (SELECT device_id FROM device WHERE vendor_id IN (SELECT id FROM vendor WHERE vendor_code = ?))",
			"DELETE FROM device_provision WHERE device_id IN (SELECT device_id FROM device WHERE vendor_id IN (SELECT id FROM vendor WHERE vendor_code = ?))",
			"DELETE FROM device WHERE vendor_id IN (SELECT id FROM vendor WHERE vendor_code = ?)",
			"DELETE FROM vendor WHERE vendor_code = ?",
		} {
			if err := db.Exec(statement, code).Error; err != nil {
				t.Errorf("fixture cleanup: %v", err)
			}
		}
	})
	return code
}

func vendorJSON(t *testing.T, in vendorInput) string {
	t.Helper()
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func decodeVendor(t *testing.T, rec *httptest.ResponseRecorder, expected int) Vendor {
	t.Helper()
	if rec.Code != expected {
		t.Fatalf("response: %d want %d: %s", rec.Code, expected, rec.Body.String())
	}
	var envelope struct {
		Code int    `json:"code"`
		Data Vendor `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil || envelope.Code != 0 {
		t.Fatalf("invalid success envelope: %s: %v", rec.Body.String(), err)
	}
	if strings.Contains(rec.Body.String(), "config_json") {
		t.Fatalf("private configuration leaked: %s", rec.Body.String())
	}
	return envelope.Data
}

func assertVendorStatus(t *testing.T, rec *httptest.ResponseRecorder, expected int) {
	t.Helper()
	if rec.Code != expected {
		t.Fatalf("response: %d want %d: %s", rec.Code, expected, rec.Body.String())
	}
}

func TestVendorCRUDIntegration(t *testing.T) {
	db := vendorIntegrationDB(t)
	router := vendorTestRouter(API{DB: db, ServiceToken: vendorTestToken})
	code := vendorFixtureCode(t, db)
	in := validVendorInput()
	in.VendorCode, in.VendorName = code, "  测试厂商  "
	row := decodeVendor(t, callVendor(router, http.MethodPost, "/api/v1/internal/vendors", vendorTestToken, vendorJSON(t, in)), 200)
	if row.ID == 0 || row.VendorName != "测试厂商" || row.EnabledAt == nil || row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() {
		t.Fatalf("created vendor: %+v", row)
	}
	path := "/api/v1/internal/vendors/" + strconv.FormatUint(row.ID, 10)
	if err := db.Table("vendor").Where("id = ?", row.ID).Update("config_json", `{"private":"credentials"}`).Error; err != nil {
		t.Fatal(err)
	}
	read := decodeVendor(t, callVendor(router, http.MethodGet, path, vendorTestToken, ""), 200)
	if read.ID != row.ID || read.VendorCode != code {
		t.Fatalf("wrong detail: %+v", read)
	}
	in.VendorCode = strings.ToLower(code)
	assertVendorStatus(t, callVendor(router, http.MethodPost, "/api/v1/internal/vendors", vendorTestToken, vendorJSON(t, in)), 409)
	in.VendorCode = code + "_EDITED"
	assertVendorStatus(t, callVendor(router, http.MethodPut, path, vendorTestToken, vendorJSON(t, in)), 409)
	in.VendorCode, in.VendorName, in.Status = code, "更新%厂商", "disabled"
	updated := decodeVendor(t, callVendor(router, http.MethodPut, path, vendorTestToken, vendorJSON(t, in)), 200)
	if updated.VendorName != in.VendorName || updated.Status != "disabled" || updated.EnabledAt == nil || !updated.EnabledAt.Equal(*row.EnabledAt) {
		t.Fatalf("updated vendor: %+v", updated)
	}
	var private string
	if err := db.Raw("SELECT config_json FROM vendor WHERE id = ?", row.ID).Scan(&private).Error; err != nil || !strings.Contains(private, "credentials") {
		t.Fatalf("private configuration changed: %q %v", private, err)
	}
	for _, keyword := range []string{"%", "更新"} {
		rec := callVendor(router, http.MethodGet, "/api/v1/internal/vendors?ids="+strconv.FormatUint(row.ID, 10)+"&status=disabled&keyword="+url.QueryEscape(keyword)+"&page_size=1", vendorTestToken, "")
		assertVendorStatus(t, rec, 200)
		var envelope struct {
			Data struct {
				Items    []Vendor `json:"items"`
				Total    int64    `json:"total"`
				Page     int      `json:"page"`
				PageSize int      `json:"page_size"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil || envelope.Data.Total != 1 || len(envelope.Data.Items) != 1 || envelope.Data.Items[0].ID != row.ID || envelope.Data.Page != 1 || envelope.Data.PageSize != 1 || strings.Contains(rec.Body.String(), "config_json") {
			t.Fatalf("invalid scoped list: %s: %v", rec.Body.String(), err)
		}
	}
	in.Status = "enabled"
	enabled := decodeVendor(t, callVendor(router, http.MethodPut, path, vendorTestToken, vendorJSON(t, in)), 200)
	if enabled.EnabledAt == nil || enabled.EnabledAt.Before(*row.EnabledAt) {
		t.Fatalf("enable timestamp: %+v", enabled)
	}
	if err := db.Table("vendor").Where("id = ?", row.ID).Update("deleted_at", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	assertVendorStatus(t, callVendor(router, http.MethodGet, path, vendorTestToken, ""), 404)
	assertVendorStatus(t, callVendor(router, http.MethodPut, path, vendorTestToken, vendorJSON(t, in)), 404)
	reused := decodeVendor(t, callVendor(router, http.MethodPost, "/api/v1/internal/vendors", vendorTestToken, vendorJSON(t, in)), 200)
	if reused.ID == row.ID {
		t.Fatal("reused code revived a deleted row instead of creating a new identity")
	}
}

func TestVendorConcurrentCodeCreationIntegration(t *testing.T) {
	db := vendorIntegrationDB(t)
	router := vendorTestRouter(API{DB: db, ServiceToken: vendorTestToken})
	code := vendorFixtureCode(t, db)
	in := validVendorInput()
	in.VendorCode = code
	body := vendorJSON(t, in)
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			<-start
			results <- callVendor(router, http.MethodPost, "/api/v1/internal/vendors", vendorTestToken, body)
		})
	}
	close(start)
	workers.Wait()
	close(results)
	successes := 0
	for rec := range results {
		switch rec.Code {
		case 200:
			successes++
		case 409:
		default:
			t.Fatalf("unexpected concurrent response: %d %s", rec.Code, rec.Body.String())
		}
	}
	var count int64
	if err := db.Table("vendor").Where("vendor_code = ? AND deleted_at IS NULL", code).Count(&count).Error; err != nil || count != 1 || successes != 1 {
		t.Fatalf("duplicate live code: successful requests=%d rows=%d err=%v", successes, count, err)
	}
}

func TestVendorDisableAndDeviceAdapterGuardIntegration(t *testing.T) {
	db := vendorIntegrationDB(t)
	router := vendorTestRouter(API{DB: db, ServiceToken: vendorTestToken})
	code := vendorFixtureCode(t, db)
	in := validVendorInput()
	in.VendorCode = code
	row := decodeVendor(t, callVendor(router, http.MethodPost, "/api/v1/internal/vendors", vendorTestToken, vendorJSON(t, in)), 200)
	path := "/api/v1/internal/vendors/" + strconv.FormatUint(row.ID, 10)
	deviceID := fmt.Sprintf("VENDORDEV%d", time.Now().UnixNano())
	if err := db.Table("device").Create(map[string]any{"device_id": deviceID, "vendor_id": row.ID, "port_count": 2, "status": "enabled"}).Error; err != nil {
		t.Fatal(err)
	}
	registration := protocol.Registration{Protocol: "dc589", DeviceID: deviceID, SoftwareVersion: "test", ReceivedAt: time.Now().UTC()}
	sink := store.MySQLSink{DB: db}
	if err := sink.Register(t.Context(), registration); err != nil {
		t.Fatalf("enabled vendor registration: %v", err)
	}
	in.Status = "disabled"
	decodeVendor(t, callVendor(router, http.MethodPut, path, vendorTestToken, vendorJSON(t, in)), 200)
	if err := sink.Register(t.Context(), registration); !errors.Is(err, store.ErrDeviceNotProvisioned) {
		t.Fatalf("disabled vendor registration accepted: %v", err)
	}
	in.AdapterClass = "future_adapter"
	assertVendorStatus(t, callVendor(router, http.MethodPut, path, vendorTestToken, vendorJSON(t, in)), 409)
	var stored Vendor
	if err := db.First(&stored, row.ID).Error; err != nil || stored.AdapterClass != "dc589" || stored.Status != "disabled" {
		t.Fatalf("rejected update changed vendor: %+v %v", stored, err)
	}
	// Historical adapters may still be renamed or disabled without inventing support.
	if err := db.Table("vendor").Where("id = ?", row.ID).Updates(map[string]any{"adapter_class": "legacy_adapter", "protocol": "mqtt"}).Error; err != nil {
		t.Fatal(err)
	}
	in.AdapterClass, in.Protocol, in.VendorName = "legacy_adapter", "mqtt", "历史厂商"
	decodeVendor(t, callVendor(router, http.MethodPut, path, vendorTestToken, vendorJSON(t, in)), 200)
	in.AdapterClass, in.Protocol = "dc589", "tcp"
	assertVendorStatus(t, callVendor(router, http.MethodPut, path, vendorTestToken, vendorJSON(t, in)), 409)
}

func TestVendorProvisionWaitsForStatusUpdateIntegration(t *testing.T) {
	db := vendorIntegrationDB(t)
	router := vendorTestRouter(API{DB: db, ServiceToken: vendorTestToken})
	code := vendorFixtureCode(t, db)
	in := validVendorInput()
	in.VendorCode = code
	row := decodeVendor(t, callVendor(router, http.MethodPost, "/api/v1/internal/vendors", vendorTestToken, vendorJSON(t, in)), 200)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	var locked Vendor
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	deviceID := fmt.Sprintf("VENDORDEV%d", time.Now().UnixNano())
	body := fmt.Sprintf(`{"devices":[{"device_id":%q,"vendor_id":%d,"station_id":1,"port_count":2}]}`, deviceID, row.ID)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- callVendor(router, http.MethodPost, "/api/v1/internal/devices/provision", vendorTestToken, body)
	}()
	select {
	case rec := <-done:
		t.Fatalf("provision did not wait for vendor lock: %d %s", rec.Code, rec.Body.String())
	case <-time.After(150 * time.Millisecond):
	}
	if err := tx.Table("vendor").Where("id = ?", row.ID).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	select {
	case rec := <-done:
		assertVendorStatus(t, rec, 409)
	case <-time.After(5 * time.Second):
		t.Fatal("provision did not finish after vendor lock was released")
	}
	var devices int64
	if err := db.Table("device").Where("vendor_id = ?", row.ID).Count(&devices).Error; err != nil || devices != 0 {
		t.Fatalf("disabled vendor gained a device: %d %v", devices, err)
	}
}
