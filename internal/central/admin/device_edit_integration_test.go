package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestDeviceEditingPersistsMetadataWithinScopeAndAuditTransaction(t *testing.T) {
	if os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("MySQL with seeded permissions required")
	}
	db := openFinanceDB(t, "TEST_ADMIN_DATABASE_URL")
	tag := strings.ReplaceAll(uuid.NewString(), "-", "")
	var roleID uint64
	if err := db.Table("role").Where("code=? AND deleted_at IS NULL", "customer_ops").Pluck("id", &roleID).Error; err != nil || roleID == 0 {
		t.Fatalf("fixture role: id=%d err=%v", roleID, err)
	}
	actor := Account{Username: "device-edit-" + tag, PasswordHash: "unused-fixture-hash", RoleID: roleID, Status: "active", AuthVersion: 1}
	var stations []Station
	var devices []Device
	t.Cleanup(func() {
		cleanup := func(query string, args ...any) {
			t.Helper()
			if err := db.Exec(query, args...).Error; err != nil {
				t.Errorf("fixture cleanup: %v", err)
			}
		}
		cleanup("DELETE FROM audit_log WHERE actor_id=? AND actor_name=?", actor.ID, actor.Username)
		cleanup("DELETE FROM admin_data_scope WHERE admin_user_id=? AND created_by=?", actor.ID, actor.ID)
		for _, device := range devices {
			cleanup("DELETE FROM device_meta WHERE id=? AND device_id=?", device.ID, device.DeviceID)
		}
		for _, station := range stations {
			cleanup("DELETE FROM station WHERE id=? AND name=?", station.ID, station.Name)
		}
		cleanup("DELETE FROM admin_user_role WHERE id=? AND username=?", actor.ID, actor.Username)
	})
	if err := db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"own", "other"} {
		station := Station{Name: "device-edit-" + tag + "-" + suffix, Longitude: 120, Latitude: 30, Status: "active"}
		if err := db.Create(&station).Error; err != nil {
			t.Fatal(err)
		}
		stations = append(stations, station)
	}
	// Future fixture timestamp forces same-millisecond advancement independently of clock speed.
	original := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
	for i, station := range []uint64{stations[0].ID, stations[1].ID, stations[0].ID, stations[0].ID} {
		vendor := uint64(1)
		if i == 2 {
			vendor = 2
		}
		device := Device{DeviceID: fmt.Sprintf("edit-%s-%d", tag, i), StationID: &station, VendorID: &vendor,
			Status: "enabled", ProtocolAdapter: "dc589", ChargeMode: "device_duration", UpdatedAt: original}
		record := struct {
			ID                                  uint64
			DeviceID                            string
			StationID, VendorID                 uint64
			Status, ProtocolAdapter, ChargeMode string
			UpdatedAt                           time.Time
		}{DeviceID: device.DeviceID, StationID: station, VendorID: vendor,
			Status: device.Status, ProtocolAdapter: device.ProtocolAdapter, ChargeMode: device.ChargeMode, UpdatedAt: original}
		if err := db.Table("device_meta").Create(&record).Error; err != nil {
			t.Fatal(err)
		}
		device.ID = record.ID
		devices = append(devices, device)
	}
	if err := db.Table("device_meta").Where("id=?", devices[3].ID).Update("deleted_at", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO admin_data_scope(admin_user_id,scope_type,scope_id,created_by) VALUES(?,'station',?,?),(?,'vendor',1,?)", actor.ID, stations[0].ID, actor.ID, actor.ID, actor.ID).Error; err != nil {
		t.Fatal(err)
	}
	api := ResourceAPI{Store: ResourceStore{AdminDB: db}}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("admin_profile", Profile{ID: actor.ID, Username: actor.Username, Permissions: []string{"device.read", "device.operate"}})
	})
	router.GET("/devices", api.devices)
	router.GET("/devices/:id", api.device)
	router.PUT("/devices/:id", api.updateDevice)
	router.PUT("/devices/:id/status", api.setDeviceStatus)
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(method, path, bytes.NewReader(encoded)))
		return response
	}
	read := func(device Device) Device {
		t.Helper()
		var row Device
		if err := db.Table("device_meta").Where("id=? AND device_id=?", device.ID, device.DeviceID).Take(&row).Error; err != nil {
			t.Fatal(err)
		}
		if err := row.normalizeMetadata(); err != nil {
			t.Fatal(err)
		}
		return row
	}
	payload := func(version time.Time) map[string]any {
		body := deviceEditPayload()
		body["expected_updated_at"] = version.In(time.FixedZone("UTC+8", 8*3600)).Format(time.RFC3339Nano)
		body["model"], body["serial_no"] = " 双路型号 ", " SERIAL-"+tag+" "
		body["install_at"], body["warranty_until"] = "2026-10-01T09:00:00.1239+08:00", "2027-10-01T09:00:00+08:00"
		body["tags"] = []string{" 烟感 ", "测试夹具"}
		return body
	}
	own := devices[0]
	response := request(http.MethodPut, "/devices/"+own.DeviceID, payload(original))
	if response.Code != http.StatusOK {
		t.Fatalf("save returned %d: %s", response.Code, response.Body.String())
	}
	saved := read(own)
	if saved.Model == nil || *saved.Model != "双路型号" || saved.SerialNo == nil || *saved.SerialNo != "SERIAL-"+tag || len(saved.Tags) != 2 || saved.Tags[0] != "烟感" || saved.InstallAt.Format(time.RFC3339Nano) != "2026-10-01T01:00:00.123Z" || !saved.UpdatedAt.Equal(original.Add(time.Millisecond)) {
		t.Fatalf("saved metadata: %+v", saved)
	}
	if saved.Status != own.Status || saved.ProtocolAdapter != own.ProtocolAdapter || saved.ChargeMode != own.ChargeMode || *saved.StationID != *own.StationID || *saved.VendorID != *own.VendorID {
		t.Fatalf("metadata editing changed device identity or operation fields: %+v", saved)
	}
	for _, device := range devices[1:] {
		response := request(http.MethodPut, "/devices/"+device.DeviceID, payload(read(device).UpdatedAt))
		if response.Code != http.StatusNotFound || read(device).Model != nil {
			t.Fatalf("outside-scope/deleted device update: %d %s", response.Code, response.Body.String())
		}
	}
	response = request(http.MethodPut, "/devices/"+own.DeviceID, payload(original))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "设备资料已被修改") || !strings.Contains(response.Body.String(), `"code":2009`) {
		t.Fatalf("stale form returned %d: %s", response.Code, response.Body.String())
	}
	clear := deviceEditPayload()
	clear["model"], clear["serial_no"], clear["expected_updated_at"] = "  ", "", saved.UpdatedAt.Format(time.RFC3339Nano)
	response = request(http.MethodPut, "/devices/"+own.DeviceID, clear)
	if response.Code != http.StatusOK {
		t.Fatalf("clear returned %d: %s", response.Code, response.Body.String())
	}
	cleared := read(own)
	if cleared.Model != nil || cleared.SerialNo != nil || cleared.InstallAt != nil || cleared.WarrantyUntil != nil || len(cleared.Tags) != 0 || cleared.Tags == nil || !cleared.UpdatedAt.After(saved.UpdatedAt) {
		t.Fatalf("clear failed: %+v", cleared)
	}
	// Two concurrent complete forms using the same version may never both succeed.
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, model := range []string{"Concurrent A", "Concurrent B"} {
		body := payload(cleared.UpdatedAt)
		body["model"] = model
		wg.Go(func() { codes <- request(http.MethodPut, "/devices/"+own.DeviceID, body).Code })
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusConflict] != 1 {
		t.Fatalf("concurrent edit outcomes: %v", counts)
	}
	beforeFailure := read(own)
	// Audit failure rolls back the metadata and its version together.
	callback := "fixture_audit_failure_" + tag
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_log" {
			tx.AddError(fmt.Errorf("fixture audit write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Create().Remove(callback) })
	response = request(http.MethodPut, "/devices/"+own.DeviceID, payload(beforeFailure.UpdatedAt))
	db.Callback().Create().Remove(callback)
	if response.Code != http.StatusServiceUnavailable || !read(own).UpdatedAt.Equal(beforeFailure.UpdatedAt) || *read(own).Model != *beforeFailure.Model {
		t.Fatalf("audit failure did not roll back device edit: %d %s", response.Code, response.Body.String())
	}
	var audits []struct{ BeforeJSON, AfterJSON string }
	if err := db.Table("audit_log").Select("before_json,after_json").Where("actor_id=? AND actor_name=? AND action='device.update'", actor.ID, actor.Username).Order("id").Scan(&audits).Error; err != nil || len(audits) != 3 {
		t.Fatalf("edit audits: count=%d err=%v", len(audits), err)
	}
	var before, after map[string]any
	if err := json.Unmarshal([]byte(audits[1].BeforeJSON), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(audits[1].AfterJSON), &after); err != nil {
		t.Fatal(err)
	}
	if before["model"] != "双路型号" || after["model"] != nil || after["serial_no"] != nil || after["install_at"] != nil || after["warranty_until"] != nil || len(after["tags"].([]any)) != 0 {
		t.Fatalf("clear audit snapshots: before=%v after=%v", before, after)
	}
	// A status change also advances the version, so an open edit form becomes stale.
	response = request(http.MethodPut, "/devices/"+own.DeviceID+"/status", gin.H{"status": "disabled"})
	if response.Code != http.StatusOK {
		t.Fatalf("status update: %d %s", response.Code, response.Body.String())
	}
	response = request(http.MethodPut, "/devices/"+own.DeviceID, payload(beforeFailure.UpdatedAt))
	if response.Code != http.StatusConflict {
		t.Fatalf("edit ignored concurrent status update: %d %s", response.Code, response.Body.String())
	}
	for _, path := range []string{"/devices/" + own.DeviceID, "/devices?keyword=edit-" + tag} {
		response := request(http.MethodGet, path, nil)
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "tags_json") || !strings.Contains(response.Body.String(), `"serial_no":`) || !strings.Contains(response.Body.String(), `"warranty_until":`) || !strings.Contains(response.Body.String(), `"updated_at":`) || !strings.Contains(response.Body.String(), `"tags":[`) {
			t.Fatalf("metadata response %s: %d %s", path, response.Code, response.Body.String())
		}
	}
	page, err := api.Store.Devices(context.Background(), PageQuery{Page: 1, PageSize: 10, Keyword: "edit-" + tag}, DataScope{StationIDs: []uint64{stations[0].ID}, VendorIDs: []uint64{1}})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].DeviceID != own.DeviceID {
		t.Fatalf("list scope: %+v err=%v", page, err)
	}
}
