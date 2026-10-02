package admin

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/delivery"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/google/uuid"
)

// 投递日志 upsert 与订阅清单解码：每个订阅 + 事件一行，重试覆盖旧行，
// attempt_count 由数据库自增。
func TestWebhookDispatchRoundTrip(t *testing.T) {
	if os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable central database required")
	}
	ctx := context.Background()
	conn, err := dbconn.Open(ctx, os.Getenv("TEST_ADMIN_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	db, err := dbconn.WrapGORM(conn)
	if err != nil {
		t.Fatal(err)
	}
	name := "dispatch-test-" + uuid.NewString()
	if err := db.Exec(`INSERT INTO webhook_subscription(name,url,secret,event_types,enabled) VALUES(?,?,?,?,1)`, name, "https://example.com/hook", "sec", `["charge_started","*"]`).Error; err != nil {
		t.Fatal(err)
	}
	var subID uint64
	if err := db.Table("webhook_subscription").Select("id").Where("name = ?", name).Scan(&subID).Error; err != nil || subID == 0 {
		t.Fatalf("subscription id=%d err=%v", subID, err)
	}
	t.Cleanup(func() {
		conn.ExecContext(ctx, "DELETE FROM webhook_delivery_log WHERE subscription_id = ?", subID)
		conn.ExecContext(ctx, "DELETE FROM webhook_subscription WHERE id = ?", subID)
	})

	dispatch := WebhookDispatch{AdminDB: db}
	subs, err := dispatch.ActiveSubscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sub := range subs {
		if sub.ID == subID {
			found = true
			if sub.Secret != "sec" || len(sub.EventTypes) != 2 {
				t.Fatalf("subscription %+v decoded wrong", sub)
			}
		}
	}
	if !found {
		t.Fatal("active subscription missing from list")
	}

	status := 200
	body := "ok"
	records := []delivery.DeliveryRecord{
		{SubscriptionID: subID, EventID: "evt-dispatch-1", EventType: "charge_started", RequestBody: `{"a":1}`, ResponseStatus: &status, ResponseBody: &body, DurationMS: 12},
	}
	if err := dispatch.RecordDeliveries(ctx, records); err != nil {
		t.Fatal(err)
	}
	var attempt uint32
	var savedBody string
	if err := conn.QueryRowContext(ctx, "SELECT attempt_count,request_body FROM webhook_delivery_log WHERE subscription_id = ? AND event_id = ?", subID, "evt-dispatch-1").Scan(&attempt, &savedBody); err != nil || attempt != 1 {
		t.Fatalf("first record attempt=%d err=%v", attempt, err)
	}
	// request_body 是 JSON 列，MySQL 会规范化空白，按语义比较。
	assertSameJSON(t, savedBody, `{"a":1}`)
	// 重试覆盖旧行且 attempt_count 自增。
	failedStatus := 500
	failedBody := "boom"
	records[0].ResponseStatus = &failedStatus
	records[0].ResponseBody = &failedBody
	records[0].Error = "receiver 500"
	if err := dispatch.RecordDeliveries(ctx, records); err != nil {
		t.Fatal(err)
	}
	var count int
	var lastError string
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*),MAX(error_msg) FROM webhook_delivery_log WHERE subscription_id = ? AND event_id = ?", subID, "evt-dispatch-1").Scan(&count, &lastError); err != nil || count != 1 || lastError != "receiver 500" {
		t.Fatalf("retry rows=%d error=%q err=%v", count, lastError, err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT attempt_count FROM webhook_delivery_log WHERE subscription_id = ? AND event_id = ?", subID, "evt-dispatch-1").Scan(&attempt); err != nil || attempt != 2 {
		t.Fatalf("attempt after retry=%d err=%v", attempt, err)
	}
}

// 监管报送租约：领取置 processing 并给 2 分钟租约；失败回执回到 queued
// 并按指数退避重排；租约失效的回执被跳过。
func TestRegulatoryDispatchClaimAndFinish(t *testing.T) {
	if os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable central database required")
	}
	ctx := context.Background()
	conn, err := dbconn.Open(ctx, os.Getenv("TEST_ADMIN_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	db, err := dbconn.WrapGORM(conn)
	if err != nil {
		t.Fatal(err)
	}
	dispatch := RegulatoryDispatch{AdminDB: db}

	eventID := uuid.NewString()
	if _, err := conn.ExecContext(ctx, `INSERT INTO regulatory_report(event_id,object_type,object_key,payload_json) VALUES(?,?,?,?)`,
		eventID, "device", "device-1", `{"device_id":"device-1"}`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.ExecContext(ctx, "DELETE FROM regulatory_report WHERE event_id = ?", eventID) })

	claimed, err := dispatch.Claim(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].EventID != eventID || claimed[0].LeaseToken == "" {
		t.Fatalf("claimed %+v", claimed)
	}
	// payload_json 是 JSON 列，MySQL 会规范化空白，按语义比较。
	assertSameJSON(t, string(claimed[0].Data), `{"device_id":"device-1"}`)
	// 同批第二次领取被租约挡住。
	again, err := dispatch.Claim(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("claimed while lease held: %+v", again)
	}

	// 失败回执：状态回到 queued、attempts=1、退避重排。
	result, err := dispatch.Finish(ctx, []FinishItem{{ID: claimed[0].ID, LeaseToken: claimed[0].LeaseToken, Delivered: false, Mode: "http", Error: "receiver offline"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Finished != 1 || result.Lost != 0 {
		t.Fatalf("finish result %+v", result)
	}
	var state string
	var attempts int
	var nextAt time.Time
	var lastError string
	if err := conn.QueryRowContext(ctx, "SELECT status,attempts,next_attempt_at,last_error FROM regulatory_report WHERE event_id = ?", eventID).
		Scan(&state, &attempts, &nextAt, &lastError); err != nil {
		t.Fatal(err)
	}
	if state != "queued" || attempts != 1 || lastError != "receiver offline" {
		t.Fatalf("state=%s attempts=%d error=%q", state, attempts, lastError)
	}
	if !nextAt.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("next_attempt_at=%s not backed off", nextAt)
	}

	// 租约失效的回执被跳过（lost），状态不变。
	if _, err := conn.ExecContext(ctx, "UPDATE regulatory_report SET next_attempt_at = ? WHERE event_id = ?", time.Now().UTC().Add(-time.Second), eventID); err != nil {
		t.Fatal(err)
	}
	expired, err := dispatch.Claim(ctx, 20)
	if err != nil || len(expired) == 0 {
		t.Fatalf("reclaim after backoff: items=%d err=%v", len(expired), err)
	}
	result, err = dispatch.Finish(ctx, []FinishItem{{ID: expired[0].ID, LeaseToken: "stale-token", Delivered: true, Mode: "http"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Finished != 0 || result.Lost != 1 {
		t.Fatalf("stale finish result %+v", result)
	}

	// 正常回执：delivered 并记录投递方式。
	result, err = dispatch.Finish(ctx, []FinishItem{{ID: expired[0].ID, LeaseToken: expired[0].LeaseToken, Delivered: true, Mode: "simulation"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Finished != 1 {
		t.Fatalf("delivered finish result %+v", result)
	}
	if err := conn.QueryRowContext(ctx, "SELECT status,delivered_mode FROM regulatory_report WHERE event_id = ?", eventID).Scan(&state, &lastError); err != nil || state != "delivered" || lastError != "simulation" {
		t.Fatalf("state=%s mode=%q err=%v", state, lastError, err)
	}
}

// 派发的内部端点要求服务令牌。
func TestDeliveryDispatchAPIRequiresServiceToken(t *testing.T) {
	router := httpapi.NewRouter()
	(DeliveryDispatchAPI{ServiceToken: "service"}).Register(router)
	cases := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/internal/webhook-subscriptions"},
		{"POST", "/api/v1/internal/webhook-deliveries/record"},
		{"POST", "/api/v1/internal/regulatory-reports/claim"},
		{"POST", "/api/v1/internal/regulatory-reports/finish"},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
		router.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatalf("%s %s status=%d want=401", tc.method, tc.path, w.Code)
		}
	}
}

// assertSameJSON 按语义比较两段 JSON 文本（MySQL JSON 列会规范化空白与键序）。
func assertSameJSON(t *testing.T, got, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		t.Fatalf("got invalid json %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("want invalid json %q: %v", want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("json mismatch: got %s want %s", got, want)
	}
}
