package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"net/http/httptest"
	"os"
	"testing"
)

func TestWholeSchemePublicationAndScopedInheritance(t *testing.T) {
	if os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	db := openFinanceDB(t, "TEST_ADMIN_DATABASE_URL")
	prefix := "scheme-" + uuid.NewString()
	var site, other uint64
	for i, target := range []*uint64{&site, &other} {
		name := fmt.Sprintf("%s-%d", prefix, i)
		if err := db.Exec("INSERT INTO station(name,longitude,latitude,status) VALUES(?,120,30,'active')", name).Error; err != nil {
			t.Fatal(err)
		}
		db.Table("station").Where("name=?", name).Pluck("id", target)
	}
	actor := uint64(910001)
	t.Cleanup(func() {
		for _, q := range []string{"DELETE FROM pricing_publication WHERE rule_id IN (SELECT id FROM pricing_rule WHERE station_id IN (?,?))", "DELETE FROM pricing_rule WHERE station_id IN (?,?)", "DELETE FROM device_meta WHERE station_id IN (?,?)", "DELETE FROM station WHERE id IN (?,?)"} {
			if err := db.Exec(q, site, other).Error; err != nil {
				t.Error(err)
			}
		}
		db.Exec("DELETE FROM admin_data_scope WHERE admin_user_id=?", actor)
		db.Exec("DELETE FROM audit_log WHERE actor_id=?", actor)
	})
	if err := db.Exec("INSERT INTO admin_data_scope(admin_user_id,scope_type,scope_id,created_by) VALUES(?,'station',?,?)", actor, site, actor).Error; err != nil {
		t.Fatal(err)
	}
	device := "SCHEMEDEV01"
	capJSON, _ := json.Marshal(pricing.Capabilities{StopPolicyVerified: true, Duration: true, MaxMinutes: 4320})
	if err := db.Exec("INSERT INTO device_meta(device_id,station_id,status,execution_capabilities) VALUES(?,?,'enabled',?)", device, site, string(capJSON)).Error; err != nil {
		t.Fatal(err)
	}
	api := ResourceAPI{Store: ResourceStore{AdminDB: db}}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("admin_profile", Profile{ID: actor, Role: "customer_admin", Permissions: []string{"pricing.read", "pricing.rule.update", "pricing.rule.create"}})
	})
	r.POST("/apply", api.applyChargingScheme)
	r.GET("/stations/:id", api.effectiveChargingScheme)
	r.POST("/stations/:id/inherit", api.inheritChargingScheme)
	call := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		var resp struct{ Data map[string]any }
		json.Unmarshal(w.Body.Bytes(), &resp)
		return resp.Data
	}
	scheme := pricing.Scheme{Name: "站点完整方案", Packages: []pricing.Package{{ID: 1, Name: "120分钟", Mode: "duration", PriceCents: 200, Minutes: 120}}}.Normalized()
	request := applySchemeInput{RequestID: uuid.NewString(), StationID: site, Scheme: &scheme}
	call("POST", "/apply", request, 200)
	if call("POST", "/apply", request, 200)["replayed"] != true {
		t.Fatal("publication replay not recognized")
	}
	request.RequestID = uuid.NewString()
	call("POST", "/apply", request, 409)
	request.StationID = other
	request.RequestID = uuid.NewString()
	call("POST", "/apply", request, 403)
	scheme.Name = "设备完整方案"
	scheme.Packages[0].PriceCents = 300
	request.StationID = site
	request.DeviceID = device
	request.RequestID = uuid.NewString()
	call("POST", "/apply", request, 200)
	path := fmt.Sprintf("/stations/%d?device_id=%s", site, device)
	if got := call("GET", path, nil, 200); got["inherited"] != false || got["scheme"].(map[string]any)["name"] != "设备完整方案" {
		t.Fatal(got)
	}
	call("POST", fmt.Sprintf("/stations/%d/inherit", site), map[string]any{"device_id": device, "expected_version": 1}, 200)
	if got := call("GET", path, nil, 200); got["inherited"] != true || got["scheme"].(map[string]any)["name"] != "站点完整方案" {
		t.Fatal(got)
	}
}
