package charge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/google/uuid"
)

// 时长耗尽订单应在扫描中冻结计费截止点并向 gateway 下发停机；
// 证据取证与停机下发都经 HTTP，不直读 gateway 库。
func TestAutoStopFreezesCutoffAndRequestsStop(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" {
		t.Skip("MySQL with current order state columns required")
	}
	ctx := context.Background()
	conn, err := dbconn.Open(ctx, os.Getenv("TEST_USER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	db := testGORMDB(t, conn)

	tag := "autostop-" + uuid.NewString()
	started := time.Now().UTC().Add(-2 * time.Hour)
	month := time.Date(started.Year(), started.Month(), 1, 0, 0, 0, 0, time.UTC)
	if _, err := conn.ExecContext(ctx, "INSERT INTO user(openid) VALUES(?)", tag); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, "DELETE FROM user WHERE openid = ?", tag)
	var userID uint64
	if err := conn.QueryRowContext(ctx, "SELECT id FROM user WHERE openid = ?", tag).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO charge_order(order_no,user_id,device_id,port_no,status,started_at,created_month) VALUES(?,?,?,?,?,?,?)",
		tag, userID, "BILLING-TEST", 1, "charging", started, month); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, "DELETE FROM charge_order WHERE order_no = ?", tag)
	var orderID uint64
	if err := conn.QueryRowContext(ctx, "SELECT id FROM charge_order WHERE order_no = ?", tag).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	scheme := pricing.Scheme{Name: "停机方案", Packages: []pricing.Package{{ID: 1, Name: "1小时", Mode: "duration", PriceCents: 300, Minutes: 60}}}.Normalized()
	rule := pricing.Rule{ID: 1, StationID: 1, Version: 1, Spec: scheme.SpecFor(scheme.Packages[0])}
	offer := scheme.Offers(rule)[0]
	snap, _ := json.Marshal(map[string]any{"rule": rule, "offer": offer})
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO charge_order_pricing(charge_order_id,payment_intent_id,user_id,port_code,pricing_snapshot) VALUES(?,?,?,?,?)",
		orderID, uuid.NewString(), userID, tag, string(snap)); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, "DELETE FROM charge_order_pricing WHERE charge_order_id = ?", orderID)
	defer conn.ExecContext(ctx, "DELETE FROM charge_billing_cutoff WHERE charge_order_id = ?", orderID)

	var stopCalls atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/internal/charging-evidence":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Requests []struct {
					DeviceID string `json:"device_id"`
				} `json:"requests"`
			}
			if err := json.Unmarshal(body, &req); err != nil || len(req.Requests) != 1 {
				t.Errorf("evidence request invalid: %s", body)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"code":0,"data":{"evidence":[{"DeviceID":%q,"PortNo":1,"Samples":[],"Complete":true,"NextAfterID":0}]}}`, req.Requests[0].DeviceID)
		case "/api/v1/internal/charge-orders/stop":
			stopCalls.Add(1)
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected gateway call: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gateway.Close()

	stopper := AutoStopper{UserDB: db, Gateway: serviceclient.Client{Timeout: 5 * time.Second}, GatewayURL: gateway.URL, ServiceToken: "service"}
	stopped, err := stopper.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stopped != 1 {
		t.Fatalf("stopped=%d want=1", stopped)
	}
	if stopCalls.Load() != 1 {
		t.Fatalf("stop calls=%d want=1", stopCalls.Load())
	}
	var cutoff struct {
		CutoffAt time.Time `gorm:"column:cutoff_at"`
		Reason   string    `gorm:"column:reason"`
	}
	if err := db.Table("charge_billing_cutoff").Where("charge_order_id=?", orderID).Take(&cutoff).Error; err != nil {
		t.Fatalf("cutoff not frozen: %v", err)
	}
	want := started.Add(time.Hour)
	// datetime(3) 毫秒级舍入，按容差比较。
	if cutoff.CutoffAt.Sub(want).Abs() > time.Millisecond || cutoff.Reason != "duration_exhausted" {
		t.Fatalf("cutoff=%s reason=%s want=%s duration_exhausted", cutoff.CutoffAt, cutoff.Reason, want)
	}
}

// 停机扫描端点要求服务令牌。
func TestAutoStopAPIRequiresServiceToken(t *testing.T) {
	r := httpapi.NewRouter()
	(AutoStopAPI{ServiceToken: "service"}).Register(r)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/internal/charge-orders/auto-stop", nil)
	r.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("status=%d want=401", w.Code)
	}
}
