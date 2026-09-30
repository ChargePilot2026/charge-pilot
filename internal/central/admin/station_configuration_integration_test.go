package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestStationConfigurationIntegration(t *testing.T) {
	if os.Getenv("TEST_ADMIN_DATABASE_URL") == "" || os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("disposable MySQL and Redis required")
	}
	db := openFinanceDB(t, "TEST_ADMIN_DATABASE_URL")
	ctx := context.Background()
	prefix := "station-test-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	stationIDs, templateIDs, packageIDs, accountIDs, roleIDs := []uint64{}, []uint64{}, []uint64{}, []uint64{}, []uint64{}
	t.Cleanup(func() {
		for _, tc := range []struct {
			sql string
			ids []uint64
		}{
			{"DELETE FROM pricing_switch_task_item WHERE task_id IN (SELECT id FROM pricing_switch_task WHERE station_id IN ?)", stationIDs},
			{"DELETE FROM pricing_switch_task WHERE station_id IN ?", stationIDs},
			{"DELETE FROM pricing_publication WHERE rule_id IN (SELECT id FROM pricing_rule WHERE station_id IN ?)", stationIDs},
			{"DELETE FROM pricing_rule WHERE station_id IN ?", stationIDs}, {"DELETE FROM charge_offer WHERE station_id IN ?", stationIDs},
			{"DELETE FROM station_policy WHERE station_id IN ?", stationIDs}, {"DELETE FROM device_meta WHERE station_id IN ?", stationIDs},
			{"DELETE FROM pricing_template WHERE id IN ?", templateIDs}, {"DELETE FROM pricing_package_template WHERE id IN ?", packageIDs},
			{"DELETE FROM audit_log WHERE actor_id IN ?", accountIDs}, {"DELETE FROM admin_data_scope WHERE admin_user_id IN ?", accountIDs},
			{"DELETE FROM admin_user_role WHERE id IN ?", accountIDs}, {"DELETE FROM role_permission WHERE role_id IN ?", roleIDs},
			{"DELETE FROM role WHERE id IN ?", roleIDs}, {"DELETE FROM station WHERE id IN ?", stationIDs},
		} {
			if len(tc.ids) > 0 {
				if err := db.Exec(tc.sql, tc.ids).Error; err != nil {
					t.Error(err)
				}
			}
		}
	})
	insert := func(table string, values map[string]any) uint64 {
		t.Helper()
		if err := db.Table(table).Create(values).Error; err != nil {
			t.Fatal(err)
		}
		var id uint64
		if err := db.Table(table).Where("name=?", values["name"]).Pluck("id", &id).Error; err != nil {
			t.Fatal(err)
		}
		return id
	}
	station := func(suffix string) uint64 {
		id := insert("station", map[string]any{"name": prefix + suffix, "longitude": 120, "latitude": 30, "status": "active"})
		stationIDs = append(stationIDs, id)
		return id
	}
	site, other, empty := station("-site"), station("-other"), station("-empty")
	follow, own, foreign, noDefault := prefix+"-follow", prefix+"-own", prefix+"-foreign", prefix+"-nodefault"
	for _, d := range []struct {
		id      string
		station uint64
		energy  bool
		mode    string
	}{{follow, site, true, "server_energy"}, {own, site, false, "device_duration"}, {foreign, other, true, "server_energy"}, {noDefault, empty, false, "device_duration"}} {
		if err := db.Table("device_meta").Create(map[string]any{"device_id": d.id, "station_id": d.station, "status": "enabled", "reports_energy": d.energy, "reports_segmented_power": false, "charge_mode": d.mode}).Error; err != nil {
			t.Fatal(err)
		}
	}
	energy := `{"mode":"server_energy","electric":{"basis":"energy","periods":[{"end_minute":1440,"electric_cents":50}]}}`
	duration := `{"mode":"device_duration"}`
	template := func(suffix, spec string) uint64 {
		id := insert("pricing_template", map[string]any{"name": prefix + suffix, "spec_json": spec, "display_json": "{}", "status": "active", "version": 1})
		templateIDs = append(templateIDs, id)
		return id
	}
	base, next, deviceTemplate := template("-base", energy), template("-next", energy), template("-duration", duration)
	rule := func(suffix string, sid uint64, did any, tid uint64, version int, spec string, future bool) uint64 {
		values := map[string]any{"name": prefix + suffix, "station_id": sid, "device_id": did, "template_id": tid, "version": version, "spec_json": spec, "channel": "default", "status": "active"}
		if future {
			values["effective_from"] = time.Now().Add(24 * time.Hour)
		}
		return insert("pricing_rule", values)
	}
	defaultID := rule("-default", site, nil, base, 2, energy, false)
	rule("-future-default", site, nil, base, 9, energy, true)
	ownID := rule("-independent", site, own, deviceTemplate, 1, duration, false)
	rule("-future-independent", site, own, deviceTemplate, 8, duration, true)
	foreignRule := rule("-foreign-rule", other, nil, base, 1, energy, false)
	noDefaultRule := rule("-nodefault-rule", empty, noDefault, deviceTemplate, 1, duration, false)
	packageTemplate := func(suffix string) uint64 {
		id := insert("pricing_package_template", map[string]any{"name": prefix + suffix, "kind": "amount", "price_cents": 100, "status": "active", "version": 1})
		packageIDs = append(packageIDs, id)
		return id
	}
	shared, unrelated, deleted := packageTemplate("-shared"), packageTemplate("-unrelated"), packageTemplate("-deleted")
	offer := func(suffix string, sid uint64, did any, pid uint64, status string, softDelete bool) uint64 {
		values := map[string]any{"name": prefix + suffix, "station_id": sid, "device_id": did, "package_template_id": pid, "mode": "amount", "price_cents": 100, "duration_minutes": 0, "status": status, "version": 1}
		if softDelete {
			values["deleted_at"] = time.Now()
		}
		return insert("charge_offer", values)
	}
	offer("-site-shared", site, nil, shared, "active", false)
	ownOffer := offer("-own-shared", site, own, shared, "active", false)
	offer("-site-unrelated", site, nil, unrelated, "active", false)
	offer("-site-deleted", site, nil, deleted, "active", true)
	offer("-own-disabled", site, own, unrelated, "disabled", false)
	foreignOffer := offer("-foreign-offer", other, nil, shared, "active", false)
	noDefaultOffer := offer("-nodefault-offer", empty, noDefault, shared, "active", false)
	opts, err := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cache := redis.NewClient(opts)
	t.Cleanup(func() { _ = cache.Close() })
	jwt, err := auth.NewJWT(strings.Repeat("station-test-secret", 3))
	if err != nil {
		t.Fatal(err)
	}
	authAPI := API{Store: Store{DB: db}, Sessions: Sessions{Redis: cache}, JWT: jwt}
	token := func(suffix, role string) (string, uint64) {
		t.Helper()
		username := prefix + suffix
		if err := db.Exec("INSERT INTO admin_user_role(username,password_hash,role_id) SELECT ?,'unused-test-hash',id FROM role WHERE code=? AND deleted_at IS NULL", username, role).Error; err != nil {
			t.Fatal(err)
		}
		var account Account
		if err := db.Where("username=?", username).Take(&account).Error; err != nil {
			t.Fatal(err)
		}
		accountIDs = append(accountIDs, account.ID)
		sid, refresh, err := authAPI.Sessions.Create(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = authAPI.Sessions.Revoke(ctx, refresh) })
		raw, err := jwt.Sign(auth.Claims{Subject: strconv.FormatUint(account.ID, 10), Kind: "admin", SessionID: sid, IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return raw, account.ID
	}
	full, fullID := token("-admin", "customer_admin")
	if err := db.Exec("INSERT INTO admin_data_scope(admin_user_id,scope_type,scope_id,created_by) VALUES (?,'station',?,?)", fullID, site, fullID).Error; err != nil {
		t.Fatal(err)
	}
	limitedToken := func(permission string) string {
		code := prefix + "-" + strings.ReplaceAll(permission, ".", "-")
		if err := db.Exec("INSERT INTO role(code,name) VALUES (?,?)", code, code).Error; err != nil {
			t.Fatal(err)
		}
		var id uint64
		if err := db.Table("role").Where("code=?", code).Pluck("id", &id).Error; err != nil {
			t.Fatal(err)
		}
		roleIDs = append(roleIDs, id)
		if err := db.Exec("INSERT INTO role_permission(role_id,permission_id) SELECT ?,id FROM permission WHERE code=?", id, permission).Error; err != nil {
			t.Fatal(err)
		}
		raw, _ := token("-"+permission, code)
		return raw
	}
	stationRead, pricingRead := limitedToken("station.read"), limitedToken("pricing.read")
	api := ResourceAPI{Store: ResourceStore{AdminDB: db}, Auth: authAPI}
	router := httpapi.NewRouter()
	api.Register(router)
	call := func(raw, method, path string, body any, status int) map[string]any {
		t.Helper()
		encoded, _ := json.Marshal(body)
		req := httptest.NewRequest(method, "/api/v1/admin/"+path, strings.NewReader(string(encoded)))
		req.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		if status == 200 && string(envelope.Data) != "null" && len(envelope.Data) > 0 {
			if envelope.Data[0] == '[' {
				var rows []any
				if err := json.Unmarshal(envelope.Data, &rows); err != nil {
					t.Fatal(err)
				}
				data = map[string]any{"__array": rows}
			} else if err := json.Unmarshal(envelope.Data, &data); err != nil {
				t.Fatal(err)
			}
		}
		return data
	}
	path := fmt.Sprintf("stations/%d/configuration", site)
	call(stationRead, "GET", path, nil, 403)
	call(pricingRead, "GET", path, nil, 403)
	config := call(full, "GET", path, nil, 200)
	if config["station_latest_version"] != float64(9) || config["default_rule"].(map[string]any)["id"] != float64(defaultID) || len(config["offers"].([]any)) != 4 {
		t.Fatalf("configuration not effective/scoped: %v", config)
	}
	if _, exists := config["default_rule"].(map[string]any)["spec_json"].(map[string]any); !exists {
		t.Fatal("rule spec must be parsed JSON")
	}
	matrix := call(full, "GET", fmt.Sprintf("settings/device-pricing?station_id=%d", site), nil, 200)
	for _, item := range matrix["items"].([]any) {
		row := item.(map[string]any)
		offers, err := (pricing.Store{DB: db}).ActiveOffers(ctx, site, row["device_id"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if row["offer_count"] != float64(len(offers)) || row["station_version"] != float64(2) {
			t.Fatalf("matrix != effective pricing: %v", row)
		}
		if row["device_id"] == own && (row["own_rule_id"] != float64(ownID) || row["own_latest_version"] != float64(8)) {
			t.Fatalf("device effective/latest mismatch: %v", row)
		}
	}
	for _, filter := range []string{"", "0", "-1", "nope", "1&station_id=2"} {
		call(full, "GET", "devices?station_id="+filter, nil, 400)
	}
	devices := call(full, "GET", fmt.Sprintf("devices?station_id=%d", site), nil, 200)
	if devices["total"] != float64(2) {
		t.Fatalf("station device filter failed: %v", devices)
	}
	stations := call(full, "GET", "stations", nil, 200)
	if stations["total"] != float64(1) {
		t.Fatalf("station scope ignored: %v", stations)
	}
	for _, endpoint := range []string{fmt.Sprintf("stations/%d", other), fmt.Sprintf("stations/%d/configuration", other), fmt.Sprintf("settings/device-pricing?station_id=%d", other), fmt.Sprintf("devices?station_id=%d", other)} {
		call(full, "GET", endpoint, nil, 403)
	}
	for _, preserve := range []bool{false, true} {
		candidates := call(full, "GET", fmt.Sprintf("settings/pricing-template-candidates?station_id=%d&preserve_device_overrides=%t", site, preserve), nil, 200)
		for _, item := range candidates["items"].([]any) {
			row := item.(map[string]any)
			if row["id"] == float64(next) {
				available := row["unavailable_reason"] == ""
				if available != preserve {
					t.Fatalf("candidate target set mismatch preserve=%t: %v", preserve, row)
				}
			}
		}
	}
	// An incompatible default must leave both the independent rule and offer untouched.
	call(full, "POST", "settings/device-pricing/reset", map[string]any{"station_id": site, "device_id": own, "keep_device_offers": true}, 409)
	status := func(table string, id uint64) string {
		var value string
		if err := db.Table(table).Where("id=?", id).Pluck("status", &value).Error; err != nil {
			t.Fatal(err)
		}
		return value
	}
	if status("pricing_rule", ownID) != "active" || status("charge_offer", ownOffer) != "active" {
		t.Fatal("failed reset changed data")
	}
	apply := map[string]any{"station_id": site, "expected_version": 9, "request_id": uuid.NewString(), "preserve_device_overrides": true}
	applyPath := fmt.Sprintf("settings/pricing-templates/%d/apply", next)
	pub := call(full, "POST", applyPath, apply, 200)
	replay := call(full, "POST", applyPath, apply, 200)
	if pub["version"] != float64(10) || pub["id"] != replay["id"] || replay["replayed"] != true {
		t.Fatalf("publication/version/replay invalid: %v %v", pub, replay)
	}
	call(full, "POST", fmt.Sprintf("settings/pricing-templates/%d/apply", base), apply, 409)
	var ownMode string
	db.Table("device_meta").Where("device_id=?", own).Pluck("charge_mode", &ownMode)
	if ownMode != "device_duration" || status("pricing_rule", ownID) != "active" {
		t.Fatal("station default replaced independent rule/mode")
	}
	call(full, "POST", applyPath, map[string]any{"station_id": site, "expected_version": 10, "request_id": uuid.NewString()}, 409)
	// A device-side default schedules only inheriting devices, with independent devices untouched.
	pub = call(full, "POST", fmt.Sprintf("settings/pricing-templates/%d/apply", deviceTemplate), map[string]any{"station_id": site, "expected_version": 10, "request_id": uuid.NewString(), "preserve_device_overrides": true}, 200)
	if pub["switch_pending"] != true {
		t.Fatalf("device-side switch state missing: %v", pub)
	}
	var taskDevices []string
	if err := db.Table("pricing_switch_task_item").Where("task_id=?", pub["switch_task_id"]).Pluck("device_id", &taskDevices).Error; err != nil {
		t.Fatal(err)
	}
	if len(taskDevices) != 1 || taskDevices[0] != follow {
		t.Fatalf("independent device scheduled by station publish: %v", taskDevices)
	}
	followTaskID := pub["switch_task_id"]
	reset := call(full, "POST", "settings/device-pricing/reset", map[string]any{"station_id": site, "device_id": own, "keep_device_offers": true}, 200)
	if reset["keep_device_offers"] != true || reset["switch_pending"] != true || status("pricing_rule", ownID) != "disabled" || status("charge_offer", ownOffer) != "active" {
		t.Fatalf("tariff-only reset wrong: %v", reset)
	}
	ownTaskID := reset["switch_task_id"]
	legacyOffer := offer("-legacy-reset", site, follow, shared, "active", false)
	call(full, "POST", "settings/device-pricing/reset", map[string]any{"station_id": site, "device_id": follow}, 200)
	if status("charge_offer", legacyOffer) != "disabled" {
		t.Fatal("legacy reset no longer disables device offers")
	}
	// All workspace mutations and reads must honor a station-scoped administrator.
	call(full, "POST", fmt.Sprintf("settings/pricing-templates/%d/apply", next), map[string]any{"station_id": other, "request_id": uuid.NewString()}, 403)
	call(full, "POST", fmt.Sprintf("settings/package-templates/%d/apply", shared), map[string]any{"station_id": other, "request_id": uuid.NewString()}, 403)
	call(full, "POST", fmt.Sprintf("settings/charge-rules/%d/disable", foreignRule), nil, 403)
	call(full, "POST", fmt.Sprintf("settings/charge-offers/%d/disable", foreignOffer), nil, 403)
	call(full, "POST", "settings/device-pricing/reset", map[string]any{"station_id": other, "device_id": foreign}, 403)
	// An unrestricted admin verifies the no-default case without changing any credentials.
	unrestricted, _ := token("-unrestricted", "customer_admin")
	call(unrestricted, "POST", "settings/device-pricing/reset", map[string]any{"station_id": empty, "device_id": noDefault, "keep_device_offers": true}, 409)
	if status("pricing_rule", noDefaultRule) != "active" || status("charge_offer", noDefaultOffer) != "active" {
		t.Fatal("missing-default reset changed data")
	}
	config = call(unrestricted, "GET", fmt.Sprintf("stations/%d/configuration", empty), nil, 200)
	if config["default_rule"] != nil || config["station_latest_version"] != float64(0) {
		t.Fatalf("empty default guessed: %v", config)
	}
	// Package apply accepts a correlation UUID and preserves natural-key replay semantics.
	pkgPath := fmt.Sprintf("settings/package-templates/%d/apply", deleted)
	pkgBody := map[string]any{"station_id": site, "device_id": own, "request_id": uuid.NewString()}
	first := call(full, "POST", pkgPath, pkgBody, 200)
	again := call(full, "POST", pkgPath, pkgBody, 200)
	if first["offer_id"] != again["offer_id"] || again["replayed"] != true {
		t.Fatalf("package replay wrong: %v %v", first, again)
	}
	// Vendor-scoped operators can view inherited defaults, but only manage and
	// inspect their own devices, even when another vendor shares the station.
	if err := db.Table("device_meta").Where("device_id=?", own).Update("vendor_id", 502).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("device_meta").Where("device_id=?", follow).Update("vendor_id", 501).Error; err != nil {
		t.Fatal(err)
	}
	vendorToken, vendorAccount := token("-vendor", "customer_admin")
	if err := db.Exec("INSERT INTO admin_data_scope(admin_user_id,scope_type,scope_id,created_by) VALUES (?,'vendor',502,?)", vendorAccount, vendorAccount).Error; err != nil {
		t.Fatal(err)
	}
	vendorMatrix := call(vendorToken, "GET", fmt.Sprintf("settings/device-pricing?station_id=%d", site), nil, 200)["items"].([]any)
	if len(vendorMatrix) != 1 || vendorMatrix[0].(map[string]any)["device_id"] != own {
		t.Fatalf("vendor scope ignored by matrix: %v", vendorMatrix)
	}
	config = call(vendorToken, "GET", fmt.Sprintf("stations/%d/configuration", site), nil, 200)
	if config["can_manage_default"] != false || config["default_rule"] == nil {
		t.Fatalf("vendor default visibility/management wrong: %v", config)
	}
	for _, raw := range config["offers"].([]any) {
		if did := raw.(map[string]any)["device_id"]; did != nil && did != own {
			t.Fatalf("vendor offer leaked: %v", raw)
		}
	}
	policies := call(vendorToken, "GET", fmt.Sprintf("settings/station-policies?station_id=%d", site), nil, 200)
	if policies["can_manage_default"] != false {
		t.Fatalf("vendor policy management allowed: %v", policies)
	}
	call(vendorToken, "POST", applyPath, map[string]any{"station_id": site, "request_id": uuid.NewString()}, 403)
	call(vendorToken, "POST", pkgPath, map[string]any{"station_id": site, "request_id": uuid.NewString()}, 403)
	call(vendorToken, "PUT", fmt.Sprintf("settings/station-policies/%d", site), map[string]any{"scan_refund_path": "original", "scan_refund_rule": "none", "card_refund_path": "original", "card_refund_rule": "none"}, 403)
	call(vendorToken, "POST", "settings/device-pricing/reset", map[string]any{"station_id": site, "device_id": follow}, 403)
	blockedDevices := []ImportDevice{{DeviceID: follow, StationID: site, VendorID: 501, PortCount: 2}}
	call(vendorToken, "POST", "device-imports", map[string]any{"import_id": uuid.NewString(), "devices": blockedDevices}, 403)
	call(full, "POST", "device-imports", map[string]any{"import_id": uuid.NewString(), "devices": []ImportDevice{{DeviceID: prefix + "-new", StationID: other, VendorID: 501, PortCount: 2}}}, 403)
	retryID := uuid.NewString()
	requestJSON, _ := json.Marshal(blockedDevices)
	if err := db.Table("device_import").Create(map[string]any{"import_id": retryID, "actor_id": fullID, "request_json": string(requestJSON), "status": "failed", "attempts": 0}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Table("device_import").Where("import_id=?", retryID).Delete(&ImportJob{}) })
	call(vendorToken, "POST", "device-imports/"+retryID+"/retry", nil, 403)
	var attempts uint32
	if err := db.Table("device_import").Where("import_id=?", retryID).Pluck("attempts", &attempts).Error; err != nil || attempts != 0 {
		t.Fatalf("rejected retry executed import: attempts=%d err=%v", attempts, err)
	}
	call(vendorToken, "GET", fmt.Sprintf("settings/pricing-template-candidates?station_id=%d&device_id=%s", site, follow), nil, 403)
	call(vendorToken, "POST", pkgPath, pkgBody, 200)
	call(vendorToken, "GET", fmt.Sprintf("settings/switch-tasks/%.0f", followTaskID), nil, 403)
	vendorTask := call(vendorToken, "GET", fmt.Sprintf("settings/switch-tasks/%.0f", ownTaskID), nil, 200)
	if items := vendorTask["items"].([]any); len(items) != 1 || items[0].(map[string]any)["device_id"] != own {
		t.Fatalf("vendor task detail leaked: %v", vendorTask)
	}
	visibleTasks := call(vendorToken, "GET", fmt.Sprintf("settings/switch-tasks?station_id=%d", site), nil, 200)["__array"].([]any)
	for _, raw := range visibleTasks {
		if task := raw.(map[string]any); task["id"] == followTaskID || task["device_count"] != float64(1) {
			t.Fatalf("vendor task list leaked: %v", task)
		}
	}
}
