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
	for _, db := range []*gorm.DB{adb, udb, gdb} {
		var floor int64
		if e := db.Raw("SELECT COALESCE(MAX(id),0) FROM event_outbox").Row().Scan(&floor); e == nil {
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
			pagesTemplates = "name IN ('集成计费模板','坏时段','空档位','设备计费带费率','模式与费率不符','改价后的模板','模板副本','并发模板','已绑定模板','名称可更新','分页站点','更新站点','上下架套餐')"
			pagesPackages  = "name = '上下架套餐'"
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
				"DELETE FROM charge_offer WHERE station_id IN (SELECT id FROM station WHERE " + pagesStations + ") OR package_template_id IN (SELECT id FROM pricing_package_template WHERE " + pagesPackages + ")",
				"DELETE FROM station_recharge_package WHERE package_template_id IN (SELECT id FROM pricing_package_template WHERE " + pagesPackages + ")",
				"DELETE FROM pricing_package_template WHERE " + pagesPackages,
				"DELETE FROM station_policy WHERE station_id IN (SELECT id FROM station WHERE " + pagesStations + ")",
				"DELETE FROM station WHERE " + pagesStations,
				// 一条没被这次清理掉、活过了清理时机的计费规则会保留着它的版本计数，
				// 于是下一轮的 apply 就会被当成版本冲突拒掉——这种报错读起来像是一个
				// 产品缺陷，其实并不是。
				"DELETE FROM pricing_publication WHERE rule_id IN (SELECT id FROM pricing_rule WHERE template_id IN (SELECT id FROM pricing_template WHERE " + pagesTemplates + "))",
				"DELETE FROM pricing_switch_task WHERE template_id IN (SELECT id FROM pricing_template WHERE " + pagesTemplates + ")",
				// 这条没绑定的历史遗留规则既不属于任何一个模板，也不属于任何一个站点，
				// 所以只能直接按名字点名删——它是这里唯一一个两处引用都够不着的夹具。
				"DELETE FROM pricing_rule WHERE name = 'legacy unbound' OR template_id IN (SELECT id FROM pricing_template WHERE " + pagesTemplates + ") OR station_id IN (SELECT id FROM station WHERE " + pagesStations + ")",
				"DELETE FROM pricing_template WHERE " + pagesTemplates,
				"DELETE FROM announcement WHERE title = '测试公告' OR title LIKE '%pages%'",
				// 坐席的 DELETE 路由是"停用"不是物理删除（坐席记录要留痕），所以
				// 每跑一次就多一条停用坐席，后台列表会一版版变长。测试得自己摘。
				"DELETE FROM customer_service_config WHERE agent_wechat = 'pages_seat'",
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
				"DELETE FROM feedback WHERE content LIKE '%pages_seat%' OR user_id IN (SELECT id FROM user WHERE " + pagesUsers + ")",
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
			if e := group.db.Exec("DELETE FROM event_outbox WHERE id > ?", floors[group.db]).Error; e != nil {
				t.Logf("cleanup failed: %v :: outbox rows this run wrote", e)
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

	// 一个定价模板把费率、它的那些套餐以及展示开关一并装在同一个对象里
	// 整体保存，应用到站点时同样是整体复制一份过去用。
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
	// 一串没能延伸到午夜之前的时段，会让夜里的电价落空；这种模板在入口
	// 处就被拒掉，而不是先存进去、留一个窟窿在那里面。
	call(adminToken, "POST", "settings/pricing-templates", gin.H{"name": "坏时段", "spec": gin.H{
		"mode":     "server_energy",
		"electric": gin.H{"basis": "energy", "periods": []gin.H{{"end_minute": 720, "electric_cents": 50}}}}}, 400)
	// 一条一档都没有的功率阶梯，同样是没有办法定价的。
	call(adminToken, "POST", "settings/pricing-templates", gin.H{"name": "空档位", "spec": gin.H{
		"mode":     "server_realtime_power",
		"electric": gin.H{"basis": "realtime_power", "periods": []gin.H{{"end_minute": 1440}}}}}, 400)
	// 设备计费的模板身上若还带着电价，看起来就像配好了价，可这条路上根
	// 本来就没有任何地方会去读它。
	call(adminToken, "POST", "settings/pricing-templates", gin.H{"name": "设备计费带费率", "spec": gin.H{
		"mode":     "device_duration",
		"electric": gin.H{"basis": "energy", "periods": []gin.H{{"end_minute": 1440, "electric_cents": 50}}}}}, 400)
	// 模式与它自己的计费基准对不上，这样的费率表是没有谁会认的。
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
	// 套餐自成一个独立的池子，所以应用一份费率根本不会上架任何一条售卖记录。
	// 上架本身是另一件独立而且明确的动作；把这两件事混为一谈的结果，才让
	// 一个运
	// 个运营卖出了一个按早已不再运行的那份费率来定价的套餐出去。
	var offers int64
	adb.Table("charge_offer").Where("station_id=? AND status='active' AND deleted_at IS NULL", sid).Count(&offers)
	if offers != 0 {
		t.Fatalf("applying a tariff must not publish any package, got %d offers", offers)
	}
	// 一个站点挂两个套餐，正是残留的 UNIQUE(station_id) 会打破的那种场景，
	// 所以上面那次计数就是针对它的那个回归护栏。
	// 只要该模板的某条规则在站点上还处于生效，同一个模板就不能再被应用，站
	// 点必须先把它停用掉才行。
	apply["request_id"] = "aa000000-0000-4000-8000-000000000002"
	call(adminToken, "POST", applyPath, apply, 409)
	call(fin1, "POST", applyPath, apply, 403)
	call(adminToken, "POST", applyPath, gin.H{"request_id": "aa000000-0000-4000-8000-000000000003", "station_id": sid, "expected_version": 9}, 409)

	// 编辑这个模板，不能波及到站点当前正在据以计费的那一条规则本身去。
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

	// 复制会让原件继续运行下去，同时新建出一个彼此独立的模板出来，两者互不
	// 影响。
	copied := fmt.Sprintf("%.0f", data(call(adminToken, "POST", "settings/pricing-templates/"+pricingTemplateID+"/copy", gin.H{"name": "模板副本"}, 200))["id"].(float64))
	if copied == pricingTemplateID {
		t.Fatal("copy must create a new template")
	}
	detail := data(call(adminToken, "GET", "settings/pricing-templates/"+copied, nil, 200))
	if detail["name"] != "模板副本" {
		t.Fatal("copy must be readable under its new name")
	}

	// 在站点把那条规则停用之后，这个模板就可以再次被应用，站点也随之进
	// 入下一个版本里面去。
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

	// 两个运营各自拿着各自不同的模板，对着同一个站点版本去应用，不可能
	// 两个都赢的。
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
	// 这个站点已经跑在一份按计量计费的费率上，所以进到这里的每一块板子都
	// 必须申报自己能上报些什么。能力是随这一次导入一起传进来的，因为按一
	// 一台一台的方式去给整批设备做分类，并不是谁会去做的事。
	batch := gin.H{"import_id": "33333333-3333-4333-8333-333333333333", "devices": []gin.H{{"device_id": "PAGESDEV01", "vendor_id": vid, "station_id": sid, "port_count": 2, "model": "测试型号", "charge_mode": "server_energy", "reports_energy": true, "reports_segmented_power": true}}}
	call(adminToken, "POST", "device-imports", batch, 200)
	call(adminToken, "POST", "device-imports", batch, 200)
	// 什么都不申报的板子进不了按计量计费的站点。整批会被拒掉，而不是放进
	// 来一半，并且报错信息里还会点名指出是哪一块板子。
	call(adminToken, "POST", "device-imports", gin.H{"import_id": "33333333-3333-4333-8333-333333333334", "devices": []gin.H{{"device_id": "PAGESDEV02", "vendor_id": vid, "station_id": sid, "port_count": 2}}}, 409)
	call(adminToken, "GET", "devices?keyword=PAGESDEV01", nil, 200)
	// 退款策略是每种支付方式各两个独立字段，而这次写入在此之前拒绝了每
	// 一个请求：路由注册成 "/station-policies/:station_id"，可 pathID 读的是
	// c.Param("id")，于是参数到手里是空的，处理器对任何请求都回了 "ID 必
	// 须为正整数"。从来就没有人调用过它，所以它也从来没有失败过。
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
		// 这两个维度是作为两个字段回来的，不是合成一个字符串。单个枚举没法同
		// 时回答「什么时候」和「退到哪里」，这正是那次迁移要把它们拆开的原因。
		if item["scan_refund_path"] != "balance" || item["scan_refund_rule"] != "time_limited" {
			t.Fatalf("refund path and rule were not kept apart on read: %v", item)
		}
	}
	if !found {
		t.Fatal("the policy was accepted but is not in the list it is read from")
	}

	// 一个被下了架的套餐，必须还能够重新上架。
	//
	// 拦重复应用的那道检查把已停用的售卖记录也算成「还在卖」，于是一个把
	// 套餐下了架的运营，就再也把它放不回去：同一个请求永远会被 "该套餐已
	// 在此处上架" 拒掉，而且会一直这样被拒下去，不留任何例外。下架只要点
	// 一下，上来却没有路。
	pkg := gin.H{"name": "上下架套餐", "kind": "amount", "price_cents": 500,
		"duration_minutes": 0, "sort_order": 1, "status": "active"}
	packageID := data(call(adminToken, "POST", "settings/package-templates", pkg, 200))["id"]
	applied := data(call(adminToken, "POST",
		"settings/package-templates/"+fmt.Sprintf("%v", packageID)+"/apply",
		gin.H{"station_id": sid}, 200))
	offerID := fmt.Sprintf("%v", applied["offer_id"])
	// 同一个请求的重发就是重试，不是冲突：除非重复的那一次自己说明出来，
	// 否则调用方分不清拿回来的是一条重复单，还是一次失败。
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
	// 是一行，不是两行：多出来的那一行与原来那一行的区别，只在于充电用
	// 户最终能看到哪一条。
	var onSale int64
	if err := adb.Table("charge_offer").
		Where("station_id=? AND package_template_id=? AND status='active' AND deleted_at IS NULL", sid, packageID).
		Count(&onSale).Error; err != nil {
		t.Fatal(err)
	}
	if onSale != 1 {
		t.Fatalf("expected exactly one active offer at the target, found %d", onSale)
	}

	// 设备矩阵、切换日志和计量申报都能从定价页面点进去，而在此之前它们一
	// 个测试都没有。接连五个缺陷查下来，根子都在「没有任何测试执行过的路
	// 由」上，所以本文件现在守的规矩是：定价页面能走得到的每一条路由都在
	// 这里调一遍——并且把它写下去的东西读回来核对一遍，而不是只看有没有
	// 回 200 这个状态码就算通过了。
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
	// 不属于该站点的设备，会按名字被拒掉，而不是悄悄把那个 id 上碰巧放着
	// 的东西给重置掉它自己。
	call(adminToken, "POST", "settings/device-pricing/reset",
		gin.H{"station_id": sid, "device_id": "NOSUCHDEVICE01"}, 404)

	// 每一个读接口都要走一遍，而不是只走那些自以为会受影响的那些接口。
	//
	// Go 代码里写了、数据库却不接受的列或表达式，既不是编译错误，也不是
	// 单元测试失败——它是一个 500，只有拿真实的查询去打真实的 schema 才
	// 查得出来。这里就有一个这样的查询躲过了整整一轮评审，因为那个接口压
	// 根就没被调用过：IF() 只接三个参数，而列表查询却传了四个进去，于是套
	// 餐模板页在这段时间里全程都在返回 503，一直都是这样。
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
