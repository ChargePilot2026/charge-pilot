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
	// 四个库缺一不可：只设了 admin 一个就去连另外三个，报出来的是 "invalid MySQL URL"，
	// 看起来像代码坏了，其实是没配齐环境。
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

	// 本测试用一组固定的标识符搭出整套夹具，所以在没有做清理的情况下，对
	// 某一个库而言它只能跑过一次：第二次跑会在它遇到的第一个唯一键上撞车，
	// 并报出的是一个产品缺陷，而真正的原因其实是它自己上一轮跑完留下的那些
	// 残迹。它所创建的一切都在这里被删掉，按各自所属的那个库分组，匹配的时候
	// 用的是它自己用到的那些标记，而不是按 id 去认——因为 id 正是每次跑都会
	// 变的东西。
	//
	// 失败的语句会被报出来，而不是被吞掉。一条被悄悄跳过的 delete，和一条真
	// 正删干净的 delete 看起来完全一样，直到下一轮跑在上面绊了一跤才暴露出来。
	// event_outbox 是只追加的，它以由所发生的事情推导出来的 event id 为键，
	// 所以本轮写下去的那一行，正是下一轮会绊到的那一行。事先记下这张表原本
	// 到了哪里，就能只删掉本轮写下去的那些行，而且不碰本来就存在的行，这比
	// 去猜它的命名规则要稳健得多。
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
	// 站点在跑到一半时会被改名（分页站点 → 更新站点），所以创建时记下的名字
	// 到清理时已经对不上了：按名字删等于什么都没删，每跑一次就往站点列表里
	// 塞一条，几轮之后 demo 站点被挤出首页。这里按接口回给本轮的主键删——
	// 改名动不了主键。
	var pagesStationID uint64
	t.Cleanup(func() {
		const (
			pagesUsers     = "openid LIKE 'pages%'"
			pagesDevices   = "device_id LIKE 'PAGES%'"
			pagesTemplates = "name IN ('集成计费模板','坏时段','空档位','设备计费带费率','模式与费率不符','改价后的模板','模板副本','并发模板','已绑定模板','名称可更新','分页站点','更新站点','上下架套餐','旧版单位测试模板','旧版单位测试副本')"
			pagesPayment   = "order_no LIKE 'PAGES_%' OR wechat_transaction_id LIKE 'SIMPAGES%'"
		)
		// 本轮没建出站点时（测试提前失败）退化成 id = 0，删不到任何东西，
		// 不会误伤库里原有的站点。
		pagesStations := "id = 0"
		if pagesStationID != 0 {
			pagesStations = "id = " + strconv.FormatUint(pagesStationID, 10)
		}
		byDB := []struct {
			db    *gorm.DB
			stmts []string
		}{
			{adb, []string{
				// 导出任务记着自己的创建人，所以必须排在账号之前删：反过来子查询就在
				// 这些账号里找不到人，本轮创建的导出任务就会因此活下来，下一轮用同样
				// request id 的那个请求就被它卡住。
				"DELETE FROM export_task WHERE task_no LIKE 'PAGES_%' OR task_no LIKE 'EXPBB000000%' OR requested_by IN (SELECT id FROM admin_user_role WHERE username LIKE 'pages-%')",
				"DELETE FROM admin_user_role WHERE username LIKE 'pages-%'",
				"DELETE FROM finance_reconcile_log WHERE reconcile_type = 'wechat_pay' AND reconcile_date IN ('2026-09-15','2026-10-01')",
				"DELETE FROM split_party WHERE split_template_id IN (SELECT id FROM split_template WHERE code LIKE 'PAGES_%')",
				"DELETE FROM split_template WHERE code LIKE 'PAGES_%'",
				// 上架动作把套餐模板复制成一条按站点售卖的 charge_offer。两者都不
				// 清就会一版版攒起来：套餐模板池里堆着几十条同名模板，售卖记录还
				// 指向早就删掉的站点，变成后台再也查不出来的孤儿。
				"DELETE FROM station WHERE " + pagesStations,
				// 一条没被这次清理掉、活过了清理时机的计费规则会保留着它的版本计数，
				// 于是下一轮的 apply 就会被当成版本冲突拒掉——这种报错读起来像是一个
				// 产品缺陷，其实并不是。
				"DELETE FROM pricing_publication WHERE rule_id IN (SELECT id FROM pricing_rule WHERE template_id IN (SELECT id FROM pricing_template WHERE " + pagesTemplates + "))",
				// 这条没绑定的历史遗留规则既不属于任何一个模板，也不属于任何一个站点，
				// 所以只能直接按名字点名删——它是这里唯一一个两处引用都够不着的夹具。
				"DELETE FROM pricing_rule WHERE name = 'legacy unbound' OR template_id IN (SELECT id FROM pricing_template WHERE " + pagesTemplates + ") OR station_id IN (SELECT id FROM station WHERE " + pagesStations + ")",
				"DELETE FROM pricing_template WHERE " + pagesTemplates,
				"DELETE FROM announcement WHERE title = '测试公告' OR title LIKE '%pages%'",
				"DELETE FROM webhook_subscription WHERE name = '测试订阅' OR name LIKE '%pages%' OR url LIKE '%pages%'",
				// 匹配的是板子本身，而不是它随哪一批导入进来的：一批失败的导入照样
				// 会记下它当时见到的那个板子身份，而挡下下一次针对同一块板子的那次
				// 尝试的，正是留下来的这一行。
				"DELETE FROM device_meta WHERE " + pagesDevices,
				"DELETE FROM device_import_identity WHERE " + pagesDevices,
				"DELETE FROM device_import WHERE import_id IN ('33333333-3333-4333-8333-333333333333','33333333-3333-4333-8333-333333333334')",
			}},
			{udb, []string{
				// 退款链路挂在固定的 request id 上，会在它后面的五张表里留下数据行；
				// 审核记录和回执都是从这张单据派生出来的，所以必须先删，否则它们就
				// 会像定价规则当初那样，把这张单据变成一个查不到主人的孤儿。
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
				// 审核表和解冻表记的都是谁签的字。正因为记了人，它们就成了每一轮各
				// 自的一份：每一轮跑起来都会新造一个审核人账号，所以上一轮遗留下来
				// 的那条审核记录，读起来就像是另一个人发起的重放，于是被当作冲突而
				// 拒掉下去，而不是被当成一次正常的新审核。
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
				// 开通只有在请求逐字重复的情况下才是幂等的，所以一条「上一轮留下、而
				// 那一轮的 vendor 行后来已经被删掉」的记录，并不是什么无害的重复：
				// 它身上带着的是那个旧的 vendor id，于是下一次尝试就会以参数不匹配被
				// 拒掉下去，而不是被当成一条全新的记录接受掉。
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
	// 记下主键，清理时按它删：这个站点稍后会被改名，按名字认不准。
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
	// 站点 code 从 admin_db/0044 起就已经没有了。还在传 code 的客户端会被明确
	// 告知这件事，而不是让它被悄悄丢掉。
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
	// 这个站点已经跑在一份按计量计费的费率上，所以进到这里的每一块板子都
	// 必须申报自己能上报些什么。能力是随这一次导入一起传进来的，因为按一
	// 一台一台的方式去给整批设备做分类，并不是谁会去做的事。
	batch := gin.H{"import_id": "33333333-3333-4333-8333-333333333333", "devices": []gin.H{{"device_id": "PAGESDEV01", "vendor_id": vid, "station_id": sid, "port_count": 2, "model": "测试型号", "charge_mode": "server_energy", "reports_energy": true, "reports_segmented_power": true}}}
	call(adminToken, "POST", "device-imports", batch, 200)
	call(adminToken, "POST", "device-imports", batch, 200)
	// 什么都不申报的板子进不了按计量计费的站点。整批会被拒掉，而不是放进
	// 来一半，并且报错信息里还会点名指出是哪一块板子。
	call(adminToken, "POST", "device-imports", gin.H{"import_id": "33333333-3333-4333-8333-333333333334", "devices": []gin.H{{"device_id": "PAGESDEV02", "vendor_id": vid, "station_id": sid, "port_count": 2}}}, 200)
	call(adminToken, "GET", "devices?keyword=PAGESDEV01", nil, 200)
	call(adminToken, "GET", "settings/device-capabilities?station_id="+fmt.Sprintf("%v", sid)+"&device_id=PAGESDEV01", nil, 200)
	call(adminToken, "GET", fmt.Sprintf("stations/%v/charging-scheme?device_id=PAGESDEV01", sid), nil, 200)
	for _, retired := range []string{"settings/pricing-templates", "settings/package-templates", "settings/charge-rules", "settings/device-pricing", "settings/station-policies", "settings/switch-tasks", "ota/packages", "ota/schedules", "alert-rules", "alert-subscriptions"} {
		call(adminToken, "GET", retired, nil, 404)
	}
	// 每一个读接口都要走一遍，而不是只走那些自以为会受影响的那些接口。
	//
	// Go 代码里写了、数据库却不接受的列或表达式，既不是编译错误，也不是
	// 单元测试失败——它是一个 500，只有拿真实的查询去打真实的 schema 才
	// 查得出来。这里就有一个这样的查询躲过了整整一轮评审，因为那个接口压
	// 根就没被调用过：IF() 只接三个参数，而列表查询却传了四个进去，于是套
	// 餐模板页在这段时间里全程都在返回 503，一直都是这样。
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
	// 真实的数据行用来跑通计费明细的联表、人工预占、双人签核、渠道对账，
	// 以及渠道回执重复到达时的那一笔记账方式。
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
	// 钱包退款的审核通过时只预占可用余额；完成时释放掉这笔预占，同时扣
	// 掉渠道所确认的那笔金额，而且只会扣一次，不会重复扣。
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

	// 确认已经走到终态失败的那笔退款，会释放钱包上的那笔预占，但不会扣款。
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
