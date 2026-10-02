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

	"github.com/ChargePilot2026/charge-pilot/internal/central/channel"
	refundpkg "github.com/ChargePilot2026/charge-pilot/internal/central/refund"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/provision"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func TestAdminPagesIntegration(t *testing.T) {
	// 仅在全部数据库和 Redis 测试连接已配置时执行集成测试。
	for _, key := range []string{
		"TEST_ADMIN_DATABASE_URL", "TEST_USER_DATABASE_URL",
		"TEST_BILLING_DATABASE_URL", "TEST_GATEWAY_DATABASE_URL",
	} {
		if os.Getenv(key) == "" {
			t.Skipf("disposable databases required: %s is not set", key)
		}
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

	// 按依赖顺序删除本轮夹具，使用稳定标识和本轮创建的主键限定范围。
	// 记录清理错误；Outbox 按测试前的最大 ID 清理新增事件，避免影响已有记录。
	floors := map[*gorm.DB]int64{}
	outboxTable := func(db *gorm.DB) string {
		if db == adb {
			return "admin_event_outbox"
		}
		return "event_outbox"
	}
	for _, db := range []*gorm.DB{adb, udb, gdb} {
		var floor int64
		if e := db.Raw("SELECT COALESCE(MAX(id),0) FROM " + outboxTable(db)).Row().Scan(&floor); e == nil {
			floors[db] = floor
		}
	}
	// 站点可能在测试中改名，因此按创建响应中的主键清理。
	var pagesStationID uint64
	t.Cleanup(func() {
		const (
			pagesUsers     = "openid LIKE 'pages%'"
			pagesDevices   = "device_id LIKE 'PAGES%'"
			pagesTemplates = "name IN ('集成计费模板','坏时段','空档位','设备计费带费率','模式与费率不符','改价后的模板','模板副本','并发模板','已绑定模板','名称可更新','分页站点','更新站点','上下架套餐','旧版单位测试模板','旧版单位测试副本')"
			pagesPayment   = "order_no LIKE 'PAGES_%' OR wechat_transaction_id LIKE 'SIMPAGES%'"
		)
		// 未创建站点时使用 id=0 条件，避免清理已有业务站点。
		pagesStations := "id = 0"
		if pagesStationID != 0 {
			pagesStations = "id = " + strconv.FormatUint(pagesStationID, 10)
		}
		byDB := []struct {
			db    *gorm.DB
			stmts []string
		}{
			{adb, []string{
				// 先删除账号关联的导出任务，再删除账号，确保归属子查询仍可匹配。
				"DELETE FROM export_task WHERE task_no LIKE 'PAGES_%' OR task_no LIKE 'EXPBB000000%' OR requested_by IN (SELECT id FROM admin_user WHERE username LIKE 'pages-%')",
				"DELETE FROM admin_user WHERE username LIKE 'pages-%'",
				"DELETE FROM finance_reconcile_log WHERE reconcile_type = 'wechat_pay' AND reconcile_date IN ('2026-09-15','2026-10-01')",
				"DELETE FROM split_party WHERE split_template_id IN (SELECT id FROM split_template WHERE code LIKE 'PAGES_%')",
				"DELETE FROM split_template WHERE code LIKE 'PAGES_%'",
				// 按夹具标识清理站点。
				"DELETE FROM station WHERE " + pagesStations,
				// 清理计费规则及其版本状态，避免后续测试的方案发布发生版本冲突。
				"DELETE FROM pricing_publication WHERE rule_id IN (SELECT id FROM pricing_rule WHERE template_id IN (SELECT id FROM pricing_template WHERE " + pagesTemplates + "))",
				// 未关联站点或方案的历史规则按夹具名称清理。
				"DELETE FROM pricing_rule WHERE name = 'legacy unbound' OR template_id IN (SELECT id FROM pricing_template WHERE " + pagesTemplates + ") OR station_id IN (SELECT id FROM station WHERE " + pagesStations + ")",
				"DELETE FROM pricing_template WHERE " + pagesTemplates,
				"DELETE FROM announcement WHERE title = '测试公告' OR title LIKE '%pages%'",
				"DELETE FROM webhook_subscription WHERE name = '测试订阅' OR name LIKE '%pages%' OR url LIKE '%pages%'",
				// 按设备编号清理导入身份记录，覆盖失败任务留下的幂等状态。
				"DELETE FROM device_meta WHERE " + pagesDevices,
				"DELETE FROM device_import_identity WHERE " + pagesDevices,
				"DELETE FROM device_import WHERE import_id IN ('33333333-3333-4333-8333-333333333333','33333333-3333-4333-8333-333333333334')",
			}},
			{udb, []string{
				// 先清理退款的审核记录和回执，再删除由固定 request_id 识别的退款夹具。
				"DELETE FROM refund_success_receipt WHERE refund_record_id IN (SELECT id FROM refund_record WHERE user_id IN (SELECT id FROM user WHERE " + pagesUsers + "))",
				"DELETE FROM wallet_refund_part WHERE refund_record_id IN (SELECT id FROM refund_record WHERE user_id IN (SELECT id FROM user WHERE " + pagesUsers + "))",
				"DELETE FROM refund_review WHERE refund_record_id IN (SELECT id FROM refund_record WHERE user_id IN (SELECT id FROM user WHERE " + pagesUsers + "))",
				"DELETE FROM manual_refund_request WHERE request_id = '44444444-4444-4444-8444-444444444444'",
				"DELETE FROM refund_record WHERE user_id IN (SELECT id FROM user WHERE " + pagesUsers + ")",
				"DELETE FROM charge_bill WHERE bill_no LIKE 'PAGES_%'",
				"DELETE FROM charge_prepay WHERE payment_order_id IN (SELECT id FROM payment_order WHERE " + pagesPayment + ")",
				"DELETE FROM charge_order WHERE order_no LIKE 'PAGES_%' OR " + pagesDevices,
				"DELETE FROM payment_order WHERE " + pagesPayment,
				"DELETE FROM invoice_request WHERE invoice_no = 'PAGES_INVOICE'",
				"DELETE FROM feedback WHERE user_id IN (SELECT id FROM user WHERE " + pagesUsers + ")",
				"DELETE FROM device_fault_report WHERE " + pagesDevices,
				"DELETE FROM wallet_risk_freeze_link WHERE request_id IN ('55555555-5555-4555-8555-555555555555','88888888-8888-4888-8888-888888888888','44444444-4444-4444-8444-444444444444')",
				// 审核记录引用本轮创建的账号，必须一并清理，避免后续审核被判为他人重放。
				"DELETE FROM wallet_risk_review WHERE request_id IN ('55555555-5555-4555-8555-555555555555','88888888-8888-4888-8888-888888888888')",
				"DELETE FROM wallet_risk_release WHERE request_id IN ('55555555-5555-4555-8555-555555555555','88888888-8888-4888-8888-888888888888')",
				"DELETE FROM risk_freeze_log WHERE user_id IN (SELECT id FROM user WHERE " + pagesUsers + ")",
				"DELETE FROM wallet_refund_request WHERE request_id IN ('55555555-5555-4555-8555-555555555555','88888888-8888-4888-8888-888888888888') OR user_id IN (SELECT id FROM user WHERE " + pagesUsers + ")",
				"DELETE FROM wallet_account WHERE user_id IN (SELECT id FROM user WHERE " + pagesUsers + ")",
				"DELETE FROM coupon_grant_request WHERE coupon_id IN (SELECT id FROM coupon WHERE name IN ('测试优惠','已停用券'))",
				"DELETE FROM coupon_grant WHERE coupon_id IN (SELECT id FROM coupon WHERE name IN ('测试优惠','已停用券'))",
				"DELETE FROM coupon_activity_rule WHERE coupon_id IN (SELECT id FROM coupon WHERE name IN ('测试优惠','已停用券')) OR name LIKE 'pages%'",
				"DELETE FROM coupon WHERE name IN ('测试优惠','已停用券')",
				"DELETE FROM user WHERE " + pagesUsers,
			}},
			{gdb, []string{
				"DELETE FROM device_port WHERE " + pagesDevices,
				"DELETE FROM device WHERE " + pagesDevices,
				// 清理开通请求的幂等记录，避免旧 vendor_id 导致下次开通内容冲突。
				"DELETE FROM device_provision WHERE " + pagesDevices,
				"DELETE FROM vendor WHERE vendor_code = 'PAGES_VENDOR'",
			}},
		}
		for _, group := range byDB {
			for _, stmt := range group.stmts {
				if e := group.db.Exec(stmt).Error; e != nil {
					t.Logf("cleanup failed: %v :: %s", e, stmt)
				}
			}
			if floor, ok := floors[group.db]; ok {
				if e := group.db.Exec("DELETE FROM "+outboxTable(group.db)+" WHERE id > ?", floor).Error; e != nil {
					t.Logf("cleanup failed: %v :: outbox rows this run wrote", e)
				}
			}
		}
	})

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
		exec(adb, "INSERT INTO admin_user(username,password_hash,role_id) SELECT ?,?,id FROM role WHERE code=? AND deleted_at IS NULL", name, "unused-test-hash", role)
		var ac Account
		if e := adb.Table("admin_user").Where("username=?", name).Take(&ac).Error; e != nil {
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
	if e := adb.Table("admin_user").Where("username = ?", "pages-admin").Pluck("id", &exportCreator).Error; e != nil || exportCreator == 0 {
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
	for _, path := range []string{"stations", "devices", "orders", "users", "roles", "alerts", "announcements", "webhooks", "settings/charging-schemes", "whitelabel", "coupons", "feedback", "device-fault-reports", "billing/meter-reviews", "billing/settlements", "billing/invoices", "billing/refunds", "billing/wallet-risks", "device-imports"} {
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
	// 保存创建响应主键，供站点改名后清理。
	pagesStationID = uint64(sid.(float64))
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
	// 验证旧客户端提交已移除的站点 code 字段时得到明确参数错误。
	station["code"] = "PAGES_STATION"
	call(adminToken, "POST", "stations", station, 400)
	station["name"] = "更新站点"
	delete(station, "code")
	call(adminToken, "PUT", fmt.Sprintf("stations/%.0f", sid), station, 200)
	call(adminToken, "GET", "stations?keyword=更新&page_size=1", nil, 200)
	call(adminToken, "GET", "stations?page_size=101", nil, 400)

	// A scheme is copied as one complete publication; editing its template does not change the applied copy.
	scheme := gin.H{"name": "集成计费模板", "amount": gin.H{"algorithm": "server_energy", "periods": []gin.H{{"end_minute": 1440, "electric_cents": 50, "service_cents": 20}}}, "packages": []gin.H{{"id": 1, "name": "3元", "mode": "amount", "price_cents": 300}}}
	schemeTemplate := data(call(adminToken, "POST", "settings/charging-schemes", gin.H{"scheme": scheme}, 200))
	apply := gin.H{"request_id": "190291e8-22d3-48a3-9dcc-fd2718aa6831", "station_id": sid, "template_id": schemeTemplate["id"], "template_version": 1, "expected_version": 0}
	call(adminToken, "POST", "settings/charging-schemes/apply", apply, 200)
	replay := data(call(adminToken, "POST", "settings/charging-schemes/apply", apply, 200))
	if replay["replayed"] != true {
		t.Fatal(replay)
	}
	scheme["name"] = "改价后的模板"
	scheme["packages"] = []gin.H{{"id": 1, "name": "4元", "mode": "amount", "price_cents": 400}}
	call(adminToken, "PUT", fmt.Sprintf("settings/charging-schemes/%v", schemeTemplate["id"]), gin.H{"scheme": scheme, "expected_version": 1}, 200)
	effective := data(call(adminToken, "GET", fmt.Sprintf("stations/%v/charging-scheme", sid), nil, 200))
	frozen := effective["scheme"].(map[string]any)
	if frozen["name"] != "集成计费模板" {
		t.Fatal(effective)
	}
	call(adminToken, "PUT", fmt.Sprintf("settings/charging-schemes/%v", schemeTemplate["id"]), gin.H{"scheme": scheme, "expected_version": 1}, 409)
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
	adb.Table("admin_user").Where("username='pages-admin'").Pluck("id", &actor)
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
	// 验证导入设备按所属协议满足站点计费模式的能力要求。
	batch := gin.H{"import_id": "33333333-3333-4333-8333-333333333333", "devices": []gin.H{{"device_id": "PAGESDEV01", "vendor_id": vid, "station_id": sid, "port_count": 2, "model": "测试型号", "charge_mode": "server_energy", "reports_energy": true, "reports_segmented_power": true}}}
	call(adminToken, "POST", "device-imports", batch, 200)
	call(adminToken, "POST", "device-imports", batch, 200)
	// 验证协议能力不兼容时拒绝整批导入，并指出不兼容设备。
	call(adminToken, "POST", "device-imports", gin.H{"import_id": "33333333-3333-4333-8333-333333333334", "devices": []gin.H{{"device_id": "PAGESDEV02", "vendor_id": vid, "station_id": sid, "port_count": 2}}}, 200)
	call(adminToken, "GET", "devices?keyword=PAGESDEV01", nil, 200)
	call(adminToken, "GET", "settings/device-capabilities?station_id="+fmt.Sprintf("%v", sid)+"&device_id=PAGESDEV01", nil, 200)
	call(adminToken, "GET", fmt.Sprintf("stations/%v/charging-scheme?device_id=PAGESDEV01", sid), nil, 200)
	for _, retired := range []string{"settings/pricing-templates", "settings/package-templates", "settings/charge-rules", "settings/device-pricing", "settings/station-policies", "settings/switch-tasks", "ota/packages", "ota/schedules", "alert-rules", "alert-subscriptions"} {
		call(adminToken, "GET", retired, nil, 404)
	}
	// 遍历后台读接口，验证实际 SQL 与初始化 schema 兼容。
	// 该检查覆盖编译无法发现的列缺失、函数参数错误及查询映射问题。
	for _, path := range []string{
		"settings/charging-schemes",
		"settings/device-capabilities?station_id=" + fmt.Sprintf("%v", sid) + "&device_id=PAGESDEV01",
		"stations?page=1&page_size=5",
		"devices?page=1&page_size=5",
		"orders?page=1&page_size=5",
		"device-imports",
		"exports",
		"exports/resources",
	} {
		call(adminToken, "GET", path, nil, 200)
	}
	// 使用真实数据验证计费关联、退款预占、双人审核、对账及重复回执幂等入账。
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
	executor := refundpkg.RefundExecutor{DB: udb, Provider: channel.Simulator{}, ProviderName: "simulation"}
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
	// 审核通过预占可用余额；成功回执释放预占并按渠道金额扣款，重复回执不得重复扣款。
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

	// 渠道确认终态失败后释放退款预占，不扣减余额。
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

type closedRefundProvider struct{ channel.Simulator }

func (closedRefundProvider) CreateRefund(ctx context.Context, r channel.RefundRequest) (channel.RefundResult, error) {
	result, e := (channel.Simulator{}).CreateRefund(ctx, r)
	result.Status = "CLOSED"
	return result, e
}
