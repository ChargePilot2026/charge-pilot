package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/charge"
	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/provision"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func TestAdminPagesIntegration(t *testing.T) {
	if os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable databases required")
	}
	ctx := context.Background()
	open := func(key string) *gorm.DB {
		db, e := dbconn.Open(ctx, os.Getenv(key))
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { db.Close() })
		orm, e := dbconn.WrapGORM(db)
		if e != nil {
			t.Fatal(e)
		}
		return orm
	}
	adb, udb, bdb, gdb := open("TEST_ADMIN_DATABASE_URL"), open("TEST_USER_DATABASE_URL"), open("TEST_BILLING_DATABASE_URL"), open("TEST_GATEWAY_DATABASE_URL")
	exec := func(db *gorm.DB, sql string, args ...any) {
		t.Helper()
		if e := db.Exec(sql, args...).Error; e != nil {
			t.Fatal(e)
		}
	}
	opts, _ := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	cache := redis.NewClient(opts)
	defer cache.Close()
	jwt, _ := auth.NewJWT(strings.Repeat("r", 32))
	a := API{Store: Store{DB: adb}, Sessions: Sessions{Redis: cache}, JWT: jwt}
	router := httpapi.NewRouter()
	a.Register(router)
	gr := httpapi.NewRouter()
	provision.API{DB: gdb, ServiceToken: "test-service"}.Register(gr)
	gateway := httptest.NewServer(gr)
	defer gateway.Close()
	ResourceAPI{Store: ResourceStore{AdminDB: adb, UserDB: udb, BillingDB: bdb}, Auth: a, GatewayURL: gateway.URL, ServiceToken: "test-service", ExportDir: t.TempDir()}.Register(router)
	token := func(name, role string) string {
		t.Helper()
		exec(adb, "INSERT INTO admin_user_role(username,password_hash,role_id) SELECT ?,?,id FROM role WHERE code=? AND deleted_at IS NULL", name, "unused-test-hash", role)
		var ac Account
		if e := adb.Table("admin_user_role").Where("username=?", name).Take(&ac).Error; e != nil {
			t.Fatal(e)
		}
		sid, _, e := a.Sessions.Create(ctx, ac)
		if e != nil {
			t.Fatal(e)
		}
		tok, e := jwt.Sign(auth.Claims{Subject: strconv.FormatUint(ac.ID, 10), Kind: "admin", SessionID: sid, IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()})
		if e != nil {
			t.Fatal(e)
		}
		return tok
	}
	adminToken := token("pages-admin", "customer_admin")
	fin1 := token("pages-fin1", "customer_finance")
	fin2 := token("pages-fin2", "customer_finance")
	call := func(token, method, path string, body any, status int) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(method, "/api/v1/admin/"+path, strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s %s got %d want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	data := func(v map[string]any) map[string]any { return v["data"].(map[string]any) }
	var exportCreator uint64
	if e := adb.Table("admin_user_role").Where("username = ?", "pages-admin").Pluck("id", &exportCreator).Error; e != nil || exportCreator == 0 {
		t.Fatalf("export fixture creator: %d %v", exportCreator, e)
	}
	exec(adb, "INSERT INTO export_task(task_no,resource,status,requested_by,row_count,file_path) VALUES('PAGES_EXPORT_DETAIL','orders','completed',?,2,'/private/tmp/internal-only.csv')", exportCreator)
	var exportID uint64
	if e := adb.Table("export_task").Where("task_no = ?", "PAGES_EXPORT_DETAIL").Pluck("id", &exportID).Error; e != nil || exportID == 0 {
		t.Fatalf("export fixture id: %d %v", exportID, e)
	}
	exportPath := fmt.Sprintf("exports/%d", exportID)
	if task := data(call(fin1, "GET", exportPath, nil, 200)); task["task_no"] != "PAGES_EXPORT_DETAIL" || task["file_path"] != nil {
		t.Fatalf("export detail leaked path or wrong task: %v", task)
	}
	call("", "GET", exportPath, nil, 401)
	call(fin1, "GET", "exports/not-an-id", nil, 400)
	call(fin1, "GET", "exports/999999999999", nil, 404)
	period := gin.H{"from": "2026-09-01", "to": "2026-09-30"}
	exec(udb, "INSERT INTO charge_bill(bill_no,charge_order_id,user_id,device_id,electric_cents,service_cents,total_cents,issued_at,created_month) VALUES('PAGES_BILL_1',900001,1,'PAGES_DEV',200,50,250,'2026-09-15','2026-09-01'),('PAGES_BILL_2',900002,1,'PAGES_DEV',300,75,375,'2026-10-01','2026-10-01')")
	exec(adb, "INSERT INTO finance_reconcile_log(reconcile_type,reconcile_date,internal_count,wechat_count,diff_count,internal_cents,wechat_cents,diff_cents) VALUES('wechat_pay','2026-09-15',1,1,0,250,250,0),('wechat_pay','2026-10-01',1,0,1,375,0,375)")
	call(adminToken, "POST", "exports", gin.H{"request_id": "bb000000-0000-4000-8000-000000000001", "resource": "bills", "format": "pdf"}, 400)
	call(adminToken, "POST", "exports", gin.H{"request_id": "bb000000-0000-4000-8000-000000000002", "resource": "orders", "format": "pdf"}, 400)
	call(fin1, "POST", "exports", gin.H{"request_id": "bb000000-0000-4000-8000-000000000003", "resource": "bills", "format": "pdf", "filter": period}, 403)
	createdPDF := data(call(adminToken, "POST", "exports", gin.H{"request_id": "bb000000-0000-4000-8000-000000000004", "resource": "bills", "format": "pdf", "filter": period}, 200))
	if createdPDF["status"] != "completed" || createdPDF["row_count"] != float64(1) {
		t.Fatalf("bill PDF export failed: %v", createdPDF)
	}
	var pdfTaskID uint64
	adb.Table("export_task").Where("task_no = ?", createdPDF["task_no"]).Pluck("id", &pdfTaskID)
	pdfReq := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/admin/exports/%d/download", pdfTaskID), nil)
	pdfReq.Header.Set("Authorization", "Bearer "+adminToken)
	pdfResponse := httptest.NewRecorder()
	router.ServeHTTP(pdfResponse, pdfReq)
	if pdfResponse.Code != 200 || !strings.HasPrefix(pdfResponse.Header().Get("Content-Type"), "application/pdf") || !strings.HasPrefix(pdfResponse.Body.String(), "%PDF-") {
		t.Fatalf("PDF download failed: %d %s", pdfResponse.Code, pdfResponse.Header().Get("Content-Type"))
	}
	replayedPDF := data(call(adminToken, "POST", "exports", gin.H{"request_id": "bb000000-0000-4000-8000-000000000004", "resource": "bills", "format": "pdf", "filter": period}, 200))
	if replayedPDF["task_no"] != createdPDF["task_no"] || replayedPDF["status"] != "completed" {
		t.Fatalf("bill PDF replay changed task: %v", replayedPDF)
	}
	call(adminToken, "POST", "exports", gin.H{"request_id": "bb000000-0000-4000-8000-000000000004", "resource": "bills", "format": "xlsx", "filter": period}, 409)
	createdXLSX := data(call(adminToken, "POST", "exports", gin.H{"request_id": "bb000000-0000-4000-8000-000000000005", "resource": "reconciles", "format": "xlsx", "filter": period}, 200))
	if createdXLSX["status"] != "completed" || createdXLSX["row_count"] != float64(1) {
		t.Fatalf("reconcile XLSX export failed: %v", createdXLSX)
	}
	var pdfPath string
	adb.Table("export_task").Where("id = ?", pdfTaskID).Pluck("file_path", &pdfPath)
	exec(adb, "UPDATE export_task SET expires_at = UTC_TIMESTAMP(3) - INTERVAL 1 SECOND WHERE id = ?", pdfTaskID)
	removed, err := (ExportTask{Store: ResourceStore{AdminDB: adb}, ExportDir: filepath.Dir(pdfPath)}).CleanupExpired(ctx)
	if err != nil || removed != 1 {
		t.Fatalf("expired export cleanup = %d, %v", removed, err)
	}
	if _, err := os.Stat(pdfPath); !os.IsNotExist(err) {
		t.Fatalf("expired export file remains: %v", err)
	}
	call(adminToken, "GET", fmt.Sprintf("exports/%d/download", pdfTaskID), nil, 410)
	for _, path := range []string{"stations", "devices", "orders", "users", "roles", "alerts", "announcements", "customer-service", "webhooks", "ota/packages", "ota/schedules", "settings/charge-rules", "whitelabel", "coupons", "feedback", "device-fault-reports", "billing/meter-reviews", "billing/settlements", "billing/invoices", "billing/refunds", "billing/wallet-risks", "device-imports"} {
		call(adminToken, "GET", path, nil, 200)
		call("", "GET", path, nil, 401)
	}
	for _, action := range []string{"propose", "decide"} {
		call("", "POST", "billing/meter-reviews/1/"+action, gin.H{}, 401)
		call(adminToken, "POST", "billing/meter-reviews/1/"+action, gin.H{}, 403)
		call(fin1, "POST", "billing/meter-reviews/1/"+action, gin.H{}, 400)
	}
	call(adminToken, "GET", "billing/meter-reviews?status=invalid", nil, 400)
	station := gin.H{"name": "分页站点", "longitude": 116.3, "latitude": 39.9, "status": "active"}
	sid := data(call(adminToken, "POST", "stations", station, 200))["id"]
	parties := []gin.H{{"party_code": "operator", "party_name": "运营方", "ratio_bp": 6000},
		{"party_code": "property", "party_name": "物业", "ratio_bp": 4000, "bank_account": "6222000012345678"}}
	call("", "POST", "settings/split-templates", gin.H{"code": "PAGES_SPLIT", "name": "页面验收分账", "mode": "mode_a", "parties": parties}, 401)
	template := data(call(adminToken, "POST", "settings/split-templates", gin.H{"code": "PAGES_SPLIT", "name": "页面验收分账", "mode": "mode_a", "parties": parties}, 200))
	call(adminToken, "POST", "settings/split-templates", gin.H{"code": "PAGES_SPLIT", "name": "重复编码", "mode": "mode_a", "parties": parties}, 409)
	templateID := uint64(template["id"].(float64))
	templatePath := fmt.Sprintf("settings/split-templates/%d", templateID)
	if template["status"] != "active" || len(template["parties"].([]any)) != 2 {
		t.Fatal("split template was not activated with two parties", template)
	}
	firstParty := template["parties"].([]any)[1].(map[string]any)
	if firstParty["bank_account_last4"] != "5678" {
		t.Fatal("split party bank account must be masked", firstParty)
	}
	call(fin1, "GET", templatePath, nil, 200)
	call(fin1, "GET", templatePath+"/parties", nil, 200)
	call(fin1, "GET", "settings/split-templates?status=active", nil, 200)
	call(adminToken, "POST", templatePath+"/parties", gin.H{"parties": []gin.H{{"party_code": "operator", "party_name": "运营方", "ratio_bp": 7000}, {"party_code": "property", "party_name": "物业", "ratio_bp": 2000}}}, 400)
	call(adminToken, "POST", templatePath+"/parties", gin.H{"parties": []gin.H{{"party_code": "operator", "party_name": "运营方", "ratio_bp": 5000}, {"party_code": "property", "party_name": "物业", "ratio_bp": 5000}}}, 200)
	station["status"] = "disabled"
	stationPath := fmt.Sprintf("stations/%.0f", sid)
	call(adminToken, "PUT", stationPath, station, 200)
	call(fin1, "PUT", stationPath+"/split-template", gin.H{"template_id": templateID, "expected_template_id": 0}, 403)
	call(adminToken, "PUT", stationPath+"/split-template", gin.H{"template_id": templateID, "expected_template_id": 0}, 200)
	call(adminToken, "PUT", stationPath+"/split-template", gin.H{"template_id": templateID, "expected_template_id": 0}, 409)
	station["status"] = "active"
	call(adminToken, "PUT", stationPath, station, 200)
	call(adminToken, "POST", templatePath+"/parties", gin.H{"parties": parties}, 409)
	call(adminToken, "PUT", templatePath, gin.H{"name": "已绑定模板", "mode": "mode_b", "status": "active"}, 409)
	call(adminToken, "PUT", templatePath, gin.H{"name": "名称可更新", "mode": "mode_a", "status": "active"}, 200)
	// A station code is gone as of admin_db/0044. A client still sending one
	// gets told so rather than having it silently dropped.
	station["code"] = "PAGES_STATION"
	call(adminToken, "POST", "stations", station, 400)
	station["name"] = "更新站点"
	delete(station, "code")
	call(adminToken, "PUT", fmt.Sprintf("stations/%.0f", sid), station, 200)
	call(adminToken, "GET", "stations?keyword=更新&page_size=1", nil, 200)
	call(adminToken, "GET", "stations?page_size=101", nil, 400)

	// A pricing template carries the tariff, its packages and the display
	// switches as one object, and is applied to a station as one copy.
	spec := gin.H{
		"mode":               "server_energy",
		"electric":           gin.H{"basis": "energy", "periods": []gin.H{{"end_minute": 1440, "electric_cents": 50}}},
		"service":            gin.H{"basis": "energy", "cents_per_kwh": 20},
		"min_electric_cents": 10,
	}
	tmpl := gin.H{
		"name": "集成计费模板", "remark": "集成用例",
		"spec":    spec,
		"display": gin.H{"show_energy": true, "show_power": true, "show_fee_split": true},
	}
	pricingTemplateID := fmt.Sprintf("%.0f", data(call(adminToken, "POST", "settings/pricing-templates", tmpl, 200))["id"].(float64))
	call(fin1, "POST", "settings/pricing-templates", tmpl, 403)
	// A chain of periods that stops short of midnight leaves the night
	// unpriced, and is refused at the door rather than stored as a hole.
	call(adminToken, "POST", "settings/pricing-templates", gin.H{"name": "坏时段", "spec": gin.H{
		"mode":     "server_energy",
		"electric": gin.H{"basis": "energy", "periods": []gin.H{{"end_minute": 720, "electric_cents": 50}}}}}, 400)
	// A power ladder with no rungs is equally unpriceable.
	call(adminToken, "POST", "settings/pricing-templates", gin.H{"name": "空档位", "spec": gin.H{
		"mode":     "server_realtime_power",
		"electric": gin.H{"basis": "realtime_power", "periods": []gin.H{{"end_minute": 1440}}}}}, 400)
	// A device-billed tariff that still carries a rate would look priced when
	// nothing on that path ever reads one.
	call(adminToken, "POST", "settings/pricing-templates", gin.H{"name": "设备计费带费率", "spec": gin.H{
		"mode":     "device_duration",
		"electric": gin.H{"basis": "energy", "periods": []gin.H{{"end_minute": 1440, "electric_cents": 50}}}}}, 400)
	// The mode and its own basis disagreeing is a tariff nobody agreed to.
	call(adminToken, "POST", "settings/pricing-templates", gin.H{"name": "模式与费率不符", "spec": gin.H{
		"mode":     "server_max_power",
		"electric": gin.H{"basis": "energy", "periods": []gin.H{{"end_minute": 1440, "electric_cents": 50}}}}}, 400)
	call(adminToken, "POST", "settings/pricing-templates/"+pricingTemplateID+"/apply", gin.H{"request_id": "aa000000-0000-4000-8000-000000000000", "station_id": 0, "expected_version": 0}, 400)
	call(adminToken, "POST", "settings/pricing-templates/999999999999/apply", gin.H{"request_id": "aa000000-0000-4000-8000-0000000000ff", "station_id": sid, "expected_version": 0}, 404)

	applyPath := "settings/pricing-templates/" + pricingTemplateID + "/apply"
	apply := gin.H{"request_id": "aa000000-0000-4000-8000-000000000001", "station_id": sid, "expected_version": 0}
	firstRule := data(call(adminToken, "POST", applyPath, apply, 200))
	replayRule := data(call(adminToken, "POST", applyPath, apply, 200))
	if firstRule["id"] != replayRule["id"] || replayRule["replayed"] != true {
		t.Fatal("apply replay must preserve rule")
	}
	// Packages are a pool of their own, so applying a tariff publishes no
	// offers at all. Publishing them is a separate, explicit act, and conflating
	// the two is what let an operator sell a package priced for a tariff that
	// was no longer running.
	var offers int64
	adb.Table("charge_offer").Where("station_id=? AND status='active' AND deleted_at IS NULL", sid).Count(&offers)
	if offers != 0 {
		t.Fatalf("applying a tariff must not publish any package, got %d offers", offers)
	}
	// Two packages at one station is the case a leftover UNIQUE(station_id)
	// would break, so the count above is the regression guard for it.
	// The same template cannot be applied while one of its rules is active at
	// the station; the station has to disable it first.
	apply["request_id"] = "aa000000-0000-4000-8000-000000000002"
	call(adminToken, "POST", applyPath, apply, 409)
	call(fin1, "POST", applyPath, apply, 403)
	call(adminToken, "POST", applyPath, gin.H{"request_id": "aa000000-0000-4000-8000-000000000003", "station_id": sid, "expected_version": 9}, 409)

	// Editing the template must not reach the rule the station is charging under.
	edited := gin.H{"name": "改价后的模板", "spec": gin.H{
		"mode":     "server_energy",
		"electric": gin.H{"basis": "energy", "periods": []gin.H{{"end_minute": 1440, "electric_cents": 99}}},
		"service":  gin.H{"basis": "energy", "cents_per_kwh": 20}, "min_electric_cents": 10},
		"expected_version": 1}
	call(adminToken, "PUT", "settings/pricing-templates/"+pricingTemplateID, edited, 200)
	var appliedSpec string
	adb.Table("pricing_rule").Where("id=?", firstRule["id"]).Pluck("spec_json", &appliedSpec)
	var snapshot struct {
		Electric struct {
			Periods []struct {
				ElectricCents int64 `json:"electric_cents"`
			} `json:"periods"`
		} `json:"electric"`
	}
	if err := json.Unmarshal([]byte(appliedSpec), &snapshot); err != nil || len(snapshot.Electric.Periods) != 1 || snapshot.Electric.Periods[0].ElectricCents != 50 {
		t.Fatalf("applied rule changed to %s when the template was edited; it is a snapshot", appliedSpec)
	}

	// Copying leaves the original running and creates an independent template.
	copied := fmt.Sprintf("%.0f", data(call(adminToken, "POST", "settings/pricing-templates/"+pricingTemplateID+"/copy", gin.H{"name": "模板副本"}, 200))["id"].(float64))
	if copied == pricingTemplateID {
		t.Fatal("copy must create a new template")
	}
	detail := data(call(adminToken, "GET", "settings/pricing-templates/"+copied, nil, 200))
	if detail["name"] != "模板副本" {
		t.Fatal("copy must be readable under its new name")
	}

	// After the station disables the rule, the template can be applied again and
	// the station moves to the next version.
	call(adminToken, "POST", fmt.Sprintf("settings/charge-rules/%.0f/disable", firstRule["id"]), nil, 200)
	call(adminToken, "POST", fmt.Sprintf("settings/charge-rules/%.0f/disable", firstRule["id"]), nil, 200)
	apply["request_id"] = "aa000000-0000-4000-8000-000000000004"
	apply["expected_version"] = 1
	secondRule := data(call(adminToken, "POST", applyPath, apply, 200))
	if secondRule["version"] != float64(2) {
		t.Fatal("rule version not allocated")
	}
	var oldStatus string
	adb.Table("pricing_rule").Where("id=?", firstRule["id"]).Pluck("status", &oldStatus)
	if oldStatus != "disabled" {
		t.Fatal("prior rule still active")
	}

	// Two operators applying different templates against the same station
	// version cannot both win.
	otherID := fmt.Sprintf("%.0f", data(call(adminToken, "POST", "settings/pricing-templates", gin.H{
		"name": "并发模板", "spec": gin.H{"mode": "server_energy",
			"electric": gin.H{"basis": "energy", "periods": []gin.H{{"end_minute": 1440, "electric_cents": 30}}}}}, 200))["id"].(float64))
	otherPath := "settings/pricing-templates/" + otherID + "/apply"
	codes := make(chan int, 2)
	for _, requestID := range []string{"aa000000-0000-4000-8000-000000000005", "aa000000-0000-4000-8000-000000000006"} {
		body := gin.H{"request_id": requestID, "station_id": sid, "expected_version": 2}
		payload, _ := json.Marshal(body)
		go func(payload []byte) {
			req := httptest.NewRequest("POST", "/api/v1/admin/"+otherPath, strings.NewReader(string(payload)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+adminToken)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			codes <- rec.Code
		}(payload)
	}
	code1, code2 := <-codes, <-codes
	if !((code1 == 200 && code2 == 409) || (code1 == 409 && code2 == 200)) {
		t.Fatalf("concurrent apply statuses %d,%d", code1, code2)
	}
	var activeRules int64
	adb.Table("pricing_rule").Where("station_id=? AND status='active'", sid).Count(&activeRules)
	if activeRules != 1 {
		t.Fatalf("active rules %d", activeRules)
	}
	exec(adb, "INSERT INTO pricing_rule(name,station_id,spec_json) VALUES('legacy unbound',NULL,'{}')")
	var unboundID uint64
	adb.Table("pricing_rule").Where("name='legacy unbound'").Pluck("id", &unboundID)
	call(adminToken, "POST", fmt.Sprintf("settings/charge-rules/%d/disable", unboundID), nil, 200)
	seat := gin.H{"agent_wechat": "pages_seat", "agent_name": "测试客服", "path": "https://example.com/support", "priority": 5, "enabled": true}
	seatID := data(call(adminToken, "POST", "customer-service", seat, 200))["id"]
	seat["priority"] = 8
	call(adminToken, "PUT", fmt.Sprintf("customer-service/%.0f", seatID), seat, 200)
	call(adminToken, "DELETE", fmt.Sprintf("customer-service/%.0f", seatID), nil, 200)
	call(adminToken, "GET", "customer-service", nil, 200)
	call(adminToken, "POST", "announcements", gin.H{"title": "测试公告", "content": "本地验收", "scope": "global", "start_at": time.Now().UTC()}, 200)
	hook := data(call(adminToken, "POST", "webhooks", gin.H{"name": "测试订阅", "url": "https://example.com/hook", "event_types": []string{"alert"}}, 200))
	if len(hook["secret"].(string)) != 64 {
		t.Fatal("missing one-time secret")
	}
	call(adminToken, "GET", "webhooks", nil, 200)
	call(adminToken, "PUT", "whitelabel", gin.H{"miniprogram_name": "验收站", "theme_color": "#1677ff"}, 200)
	exec(udb, "INSERT INTO user(openid,nickname) VALUES ('pages-user','页面验收')")
	var uid uint64
	udb.Table("user").Where("openid='pages-user'").Pluck("id", &uid)
	coupon := gin.H{"name": "测试优惠", "discount_type": "amount", "discount_value_cents": 100, "min_charge_cents": 0, "valid_hours": 24, "total_quota": 1, "per_user_quota": 1}
	cid := data(call(adminToken, "POST", "coupons", coupon, 200))["id"]
	cp := fmt.Sprintf("coupons/%.0f", cid)
	grant := gin.H{"request_id": "11111111-1111-4111-8111-111111111111", "user_id": uid}
	first := data(call(adminToken, "POST", cp+"/grants", grant, 200))
	again := data(call(adminToken, "POST", cp+"/grants", grant, 200))
	if first["coupon_grant_id"] != again["coupon_grant_id"] {
		t.Fatal("duplicate coupon")
	}
	grant["request_id"] = "22222222-2222-4222-8222-222222222222"
	call(adminToken, "POST", cp+"/grants", grant, 409)
	call(adminToken, "GET", cp+"/stats", nil, 200)
	call(adminToken, "PUT", cp, gin.H{"name": "已停用券", "status": "disabled"}, 200)
	exec(udb, "INSERT INTO feedback(user_id,category,content) VALUES (?,'complaint','页面测试')", uid)
	var fid uint64
	udb.Table("feedback").Where("user_id=?", uid).Pluck("id", &fid)
	fp := fmt.Sprintf("feedback/%d/reply", fid)
	call(adminToken, "POST", fp, gin.H{"action": "reply", "reply_content": "已处理"}, 200)
	call(adminToken, "POST", fp, gin.H{"action": "close"}, 200)
	var actor uint64
	adb.Table("admin_user_role").Where("username='pages-admin'").Pluck("id", &actor)
	exec(udb, "INSERT INTO device_fault_report(device_id,user_id,report_source,fault_type,description) VALUES ('PAGESDEV01',?,'user','other','页面测试')", uid)
	udb.Table("device_fault_report").Where("user_id=?", uid).Pluck("id", &fid)
	fp = fmt.Sprintf("device-fault-reports/%d", fid)
	call(adminToken, "POST", fp+"/dispatch", gin.H{"assigned_to": actor, "note": "请检查"}, 200)
	call(adminToken, "POST", fp+"/resolve", gin.H{"status": "fixed", "note": "已修复"}, 200)
	call(adminToken, "POST", fp+"/resolve", gin.H{"status": "closed"}, 200)
	call(adminToken, "GET", fp+"/history", nil, 200)
	exec(udb, "INSERT INTO invoice_request(invoice_no,user_id,biz_type,biz_id,total_cents,title) VALUES ('PAGES_INVOICE',?,'charge',1,100,'测试抬头')", uid)
	var iid uint64
	udb.Table("invoice_request").Where("invoice_no='PAGES_INVOICE'").Pluck("id", &iid)
	ip := fmt.Sprintf("billing/invoices/%d/approve", iid)
	invoice := gin.H{"invoice_url": "https://example.com/invoice.pdf"}
	call(adminToken, "POST", ip, invoice, 403)
	call(fin1, "POST", ip, invoice, 200)
	call(fin1, "POST", ip, invoice, 409)
	call(fin2, "POST", ip, gin.H{"invoice_url": "https://example.com/changed.pdf"}, 409)
	call(fin2, "POST", ip, invoice, 200)
	exec(gdb, "INSERT INTO vendor(vendor_code,vendor_name,adapter_class,protocol,status) VALUES ('PAGES_VENDOR','页面测试','dc589','tcp','enabled')")
	var vid uint64
	gdb.Table("vendor").Where("vendor_code='PAGES_VENDOR'").Pluck("id", &vid)
	// This station already runs a metered tariff, so a board arriving here has to
	// declare what it can report. The capability travels on the import because
	// classifying a fleet one device at a time is not something anyone does.
	batch := gin.H{"import_id": "33333333-3333-4333-8333-333333333333", "devices": []gin.H{{"device_id": "PAGESDEV01", "vendor_id": vid, "station_id": sid, "port_count": 2, "model": "测试型号", "charge_mode": "server_energy", "reports_energy": true, "reports_segmented_power": true}}}
	call(adminToken, "POST", "device-imports", batch, 200)
	call(adminToken, "POST", "device-imports", batch, 200)
	// A board that declares nothing cannot join a metered station. The whole batch
	// is refused rather than half of it, and the message names the board.
	call(adminToken, "POST", "device-imports", gin.H{"import_id": "33333333-3333-4333-8333-333333333334", "devices": []gin.H{{"device_id": "PAGESDEV02", "vendor_id": vid, "station_id": sid, "port_count": 2}}}, 409)
	call(adminToken, "GET", "devices?keyword=PAGESDEV01", nil, 200)
	// The refund policy is two independent fields per payment method, and this
	// write refused every single request until now: the route was registered as
	// "/station-policies/:station_id" while pathID reads c.Param("id"), so the
	// parameter arrived empty and the handler answered "ID 必须为正整数" to
	// everything. Nothing had ever called it, so nothing had ever failed.
	policy := gin.H{"force_recharge": true, "min_balance_cents": 1000,
		"scan_refund_path": "balance", "scan_refund_rule": "time_limited",
		"card_refund_path": "original", "card_refund_rule": "time_limited_prorated",
		"timeout_start_refund": true, "verify_phone_before_charge": false, "expected_version": 0}
	call(adminToken, "PUT", "settings/station-policies/"+fmt.Sprintf("%v", sid), policy, 200)
	saved, _ := data(call(adminToken, "GET", "settings/station-policies", nil, 200))["items"].([]any)
	found := false
	for _, row := range saved {
		item := row.(map[string]any)
		if fmt.Sprintf("%v", item["station_id"]) != fmt.Sprintf("%v", sid) {
			continue
		}
		found = true
		// The two dimensions come back as two fields, not one merged string. A
		// single enum cannot answer "when" and "where" at once, which is why the
		// migration split them.
		if item["scan_refund_path"] != "balance" || item["scan_refund_rule"] != "time_limited" {
			t.Fatalf("refund path and rule were not kept apart on read: %v", item)
		}
	}
	if !found {
		t.Fatal("the policy was accepted but is not in the list it is read from")
	}

	// A package that was taken off sale must be able to go back on sale.
	//
	// The check guarding a duplicate application counted a disabled offer as
	// still selling, so an operator who took a package down could never put it
	// back: the same request was refused with "该套餐已在此处上架" for good. Going
	// down was one click and coming back was impossible.
	pkg := gin.H{"name": "上下架套餐", "kind": "amount", "price_cents": 500,
		"duration_minutes": 0, "sort_order": 1, "status": "active"}
	packageID := data(call(adminToken, "POST", "settings/package-templates", pkg, 200))["id"]
	applied := data(call(adminToken, "POST",
		"settings/package-templates/"+fmt.Sprintf("%v", packageID)+"/apply",
		gin.H{"station_id": sid}, 200))
	offerID := fmt.Sprintf("%v", applied["offer_id"])
	// A retry of the same request is a retry, not a conflict: the caller cannot
	// tell a duplicate from a failure unless the duplicate says so itself.
	retry := data(call(adminToken, "POST",
		"settings/package-templates/"+fmt.Sprintf("%v", packageID)+"/apply",
		gin.H{"station_id": sid}, 200))
	if retry["replayed"] != true {
		t.Fatalf("a repeated apply did not report itself as a replay: %v", retry)
	}
	call(adminToken, "POST", "settings/charge-offers/"+offerID+"/disable", nil, 200)
	relisted := data(call(adminToken, "POST",
		"settings/package-templates/"+fmt.Sprintf("%v", packageID)+"/apply",
		gin.H{"station_id": sid}, 200))
	if relisted["relisted"] != true {
		t.Fatalf("putting a withdrawn package back on sale was not treated as a re-list: %v", relisted)
	}
	// One row, not two: a second row would differ from the first only in which
	// one a rider can see.
	var onSale int64
	if err := adb.Table("charge_offer").
		Where("station_id=? AND package_template_id=? AND status='active' AND deleted_at IS NULL", sid, packageID).
		Count(&onSale).Error; err != nil {
		t.Fatal(err)
	}
	if onSale != 1 {
		t.Fatalf("expected exactly one active offer at the target, found %d", onSale)
	}

	// The device matrix, the switch log and the metering declaration are all
	// reachable from the pricing screens, and until now none of them had a test.
	// Five defects in a row turned out to be routes no test executed, so the
	// rule this file follows now is that every route the pricing screens can
	// reach is called here — and what it wrote is read back, not merely
	// answered 200.
	matrix := data(call(adminToken, "GET",
		"settings/device-pricing?station_id="+fmt.Sprintf("%v", sid), nil, 200))
	rows, _ := matrix["items"].([]any)
	if len(rows) == 0 {
		t.Fatal("the matrix returned no rows for a station that has a published rule")
	}
	row := rows[0].(map[string]any)
	if row["spec_json"] == nil {
		t.Fatal("the matrix has no spec_json, so it cannot answer what a device is charging")
	}
	call(adminToken, "GET", "settings/switch-tasks", nil, 200)
	call(adminToken, "GET", "settings/pricing-template-candidates?station_id="+fmt.Sprintf("%v", sid), nil, 200)
	// A device that is not in the station is refused by name rather than silently
	// resetting whatever happens to be at that id.
	call(adminToken, "POST", "settings/device-pricing/reset",
		gin.H{"station_id": sid, "device_id": "NOSUCHDEVICE01"}, 404)

	// Every read endpoint is walked, not just the ones believed to be affected.
	//
	// A column or an expression the Go code names but the database does not
	// accept is not a compile error and not a unit-test failure — it is a 500
	// that only a real query against a real schema can find. One such query
	// survived a full round of review here because the endpoint was simply
	// never called: IF() takes three arguments, the list query passed it four,
	// and the package-template page returned 503 for the whole time.
	for _, path := range []string{
		"settings/pricing-templates",
		"settings/package-templates",
		"settings/station-policies",
		"settings/switch-tasks",
		"settings/pricing-template-candidates",
		"settings/device-pricing?station_id=" + fmt.Sprintf("%v", sid),
		"stations?page=1&page_size=5",
		"devices?page=1&page_size=5",
		"orders?page=1&page_size=5",
		"device-imports",
		"exports",
		"exports/resources",
	} {
		call(adminToken, "GET", path, nil, 200)
	}
	// Real rows exercise charge detail joins, manual reservations, double signing,
	// channel reconciliation, and duplicate provider receipt accounting.
	exec(udb, "INSERT INTO payment_order(order_no,biz_type,biz_id,user_id,pay_method,total_cents,paid_cents,wechat_transaction_id,status,created_month) VALUES ('PAGES_PAY','charge',0,?,'wechat',1000,1000,'SIMPAGES_PAY','paid',DATE_FORMAT(UTC_TIMESTAMP(),'%Y-%m-01'))", uid)
	var payID, orderID uint64
	udb.Table("payment_order").Where("order_no='PAGES_PAY'").Pluck("id", &payID)
	exec(udb, "INSERT INTO charge_order(order_no,user_id,device_id,port_no,payment_order_id,status,created_month) VALUES ('PAGES_ORDER',?,'PAGESDEV01',1,?,'completed',DATE_FORMAT(UTC_TIMESTAMP(),'%Y-%m-01'))", uid, payID)
	udb.Table("charge_order").Where("order_no='PAGES_ORDER'").Pluck("id", &orderID)
	exec(udb, "UPDATE payment_order SET biz_id=? WHERE id=?", orderID, payID)
	newTemplate := data(call(adminToken, "POST", "settings/split-templates", gin.H{"code": "PAGES_SPLIT_NEXT", "name": "新分账模板", "mode": "mode_b", "parties": parties}, 200))
	station["status"] = "disabled"
	call(adminToken, "PUT", stationPath, station, 200)
	exec(adb, "UPDATE station SET updated_at = DATE_SUB(UTC_TIMESTAMP(), INTERVAL 6 MINUTE) WHERE id = ?", sid)
	call(adminToken, "PUT", stationPath+"/split-template", gin.H{"template_id": newTemplate["id"], "expected_template_id": templateID}, 409)
	station["status"] = "active"
	call(adminToken, "PUT", stationPath, station, 200)
	exec(udb, `INSERT INTO charge_prepay(payment_order_id,prepay_id,params_json) VALUES (?,'SIM', '{"provider":"simulation"}')`, payID)
	op := fmt.Sprintf("orders/%d", orderID)
	call(adminToken, "GET", op, nil, 200)
	call(adminToken, "GET", op+"/timeline", nil, 200)
	refund := gin.H{"request_id": "44444444-4444-4444-8444-444444444444", "amount_cents": 400, "reason": "测试退款"}
	rf := data(call(adminToken, "POST", op+"/refunds", refund, 200))["refund_no"].(string)
	call(adminToken, "POST", op+"/refunds", refund, 200)
	rp := "billing/refunds/" + rf + "/approve"
	call(fin1, "POST", rp, gin.H{"approve_comment": "首次核实"}, 200)
	call(fin1, "POST", rp, gin.H{"approve_comment": "不能自签"}, 409)
	call(fin2, "POST", rp, gin.H{"approve_comment": "二次核实"}, 200)
	call(adminToken, "POST", "billing/refunds/"+rf+"/retry", gin.H{"reason": "测试立即执行"}, 200)
	var rid uint64
	udb.Table("refund_record").Where("refund_no=?", rf).Pluck("id", &rid)
	executor := charge.RefundExecutor{DB: udb, Provider: payment.Simulator{}, ProviderName: "simulation"}
	if e := executor.Execute(ctx, rid); e != nil {
		t.Fatal(e)
	}
	if e := executor.Execute(ctx, rid); e != nil {
		t.Fatal(e)
	}
	var refunded int64
	udb.Table("payment_order").Where("id=?", payID).Pluck("refunded_cents", &refunded)
	if refunded != 400 {
		t.Fatal("duplicate or absent refund", refunded)
	}
	// Wallet approval reserves only available balance; completion releases reservation
	// while deducting the provider-confirmed amount, exactly once.
	exec(udb, "INSERT INTO wallet_account(user_id,balance_cents,status) VALUES (?,500,'frozen')", uid)
	exec(udb, "INSERT INTO payment_order(order_no,biz_type,biz_id,user_id,pay_method,total_cents,paid_cents,wechat_transaction_id,status,created_month) VALUES ('PAGES_RECHARGE','wallet_recharge',99,?,'wechat',500,500,'SIMPAGES_RECHARGE','paid',DATE_FORMAT(UTC_TIMESTAMP(),'%Y-%m-01'))", uid)
	exec(udb, "INSERT INTO risk_freeze_log(user_id,trigger_rule,frozen_action) VALUES (?,'wallet_refund_frequency','wallet')", uid)
	var freezeID uint64
	udb.Table("risk_freeze_log").Where("user_id=?", uid).Pluck("id", &freezeID)
	reqID := "55555555-5555-4555-8555-555555555555"
	exec(udb, "INSERT INTO wallet_refund_request(request_id,user_id,amount_cents,reason) VALUES (?,?,300,'页面测试')", reqID, uid)
	exec(udb, "INSERT INTO wallet_risk_freeze_link(request_id,freeze_id) VALUES (?,?)", reqID, freezeID)
	wr := "billing/wallet-risks/" + reqID
	call(fin1, "GET", "billing/wallet-risks", nil, 200)
	call(fin1, "POST", wr+"/review", gin.H{"approved": true, "comment": "核实充值款"}, 200)
	call(fin1, "POST", wr+"/review", gin.H{"approved": true, "comment": "核实充值款"}, 200)
	call(fin1, "POST", wr+"/release", gin.H{"comment": "解除频次冻结"}, 200)
	udb.Table("wallet_refund_part").Where("request_id=?", reqID).Pluck("refund_record_id", &rid)
	if e := executor.Execute(ctx, rid); e != nil {
		t.Fatal(e)
	}
	if e := executor.Execute(ctx, rid); e != nil {
		t.Fatal(e)
	}
	var wallet struct {
		BalanceCents, FrozenCents int64
		Status                    string
	}
	udb.Table("wallet_account").Where("user_id=?", uid).Take(&wallet)
	if wallet.BalanceCents != 200 || wallet.FrozenCents != 0 || wallet.Status != "active" {
		t.Fatalf("wallet %+v", wallet)
	}

	// Confirmed terminal failures release the wallet reservation without debiting it.
	exec(udb, "INSERT INTO risk_freeze_log(user_id,trigger_rule,frozen_action) VALUES (?,'wallet_refund_frequency','wallet')", uid)
	udb.Table("risk_freeze_log").Where("user_id=?", uid).Order("id DESC").Limit(1).Pluck("id", &freezeID)
	failedReq := "88888888-8888-4888-8888-888888888888"
	exec(udb, "INSERT INTO wallet_refund_request(request_id,user_id,amount_cents) VALUES (?,?,100)", failedReq, uid)
	exec(udb, "INSERT INTO wallet_risk_freeze_link(request_id,freeze_id) VALUES (?,?)", failedReq, freezeID)
	call(fin1, "POST", "billing/wallet-risks/"+failedReq+"/review", gin.H{"approved": true, "comment": "失败回执测试"}, 200)
	udb.Table("wallet_refund_part").Where("request_id=?", failedReq).Pluck("refund_record_id", &rid)
	executor.Provider = closedRefundProvider{}
	if e := executor.Execute(ctx, rid); e != nil {
		t.Fatal(e)
	}
	udb.Table("wallet_account").Where("user_id=?", uid).Take(&wallet)
	if wallet.BalanceCents != 200 || wallet.FrozenCents != 0 {
		t.Fatalf("failed refund retained or spent balance: %+v", wallet)
	}
	var failedStatus string
	udb.Table("refund_record").Where("id=?", rid).Pluck("status", &failedStatus)
	if failedStatus != "failed" {
		t.Fatal(failedStatus)
	}

}

type closedRefundProvider struct{ payment.Simulator }

func (closedRefundProvider) CreateRefund(ctx context.Context, r payment.RefundRequest) (payment.RefundResult, error) {
	result, e := (payment.Simulator{}).CreateRefund(ctx, r)
	result.Status = "CLOSED"
	return result, e
}
