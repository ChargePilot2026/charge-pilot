package charge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	centralcharge "github.com/ChargePilot2026/charge-pilot/internal/central/charge"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/google/uuid"
)

func TestPaidStartDispatchRequiresCallbackCreatedOrder(t *testing.T) {
	url := os.Getenv("TEST_USER_DATABASE_URL")
	if url == "" {
		t.Skip("set disposable user database URL")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.ExecContext(ctx, "INSERT INTO user (openid) VALUES (?)", "dispatch-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	transactionID := "sim-" + uuid.NewString()
	defer func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM event_outbox WHERE event_id IN (SELECT event_id FROM charge_event_log WHERE charge_order_id IN (SELECT id FROM charge_order WHERE user_id = ?))", userID)
		_, _ = db.ExecContext(ctx, "DELETE FROM charge_event_log WHERE charge_order_id IN (SELECT id FROM charge_order WHERE user_id = ?)", userID)
		_, _ = db.ExecContext(ctx, "DELETE FROM charge_order_pricing WHERE user_id = ?", userID)
		_, _ = db.ExecContext(ctx, "DELETE FROM charge_order WHERE user_id = ?", userID)
		_, _ = db.ExecContext(ctx, "DELETE FROM charge_payment_intent WHERE user_id = ?", userID)
		_, _ = db.ExecContext(ctx, "DELETE FROM payment_order WHERE user_id = ?", userID)
		_, _ = db.ExecContext(ctx, "DELETE FROM payment_callback_idempotent WHERE wechat_transaction_id = ?", transactionID)
		_, _ = db.ExecContext(ctx, "DELETE FROM user WHERE id = ?", userID)
	}()
	intent, err := (centralcharge.PaymentIntentStore{DB: db}).Reserve(ctx, centralcharge.IntentInput{
		UserID: uint64(userID), ClientRequestID: uuid.NewString(),
		Port: centralcharge.ScanResult{Kind: "port", DeviceID: "dispatch-device", StationID: 9,
			Port: &centralcharge.ScanPort{PortID: "dispatch-device:1", DeviceID: "dispatch-device", PortNo: 1, Online: true, Available: true}},
		Energy: "1", Minutes: 60,
		Rule: pricing.Rule{ID: 3, StationID: 9, Version: 1, Mode: "kwh", ServiceCentsPerKWh: 40,
			Periods: []pricing.Period{{Start: "00:00", End: "24:00", ElectricPriceCents: 100}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/internal/charge-orders/start" || r.Header.Get("X-Service-Token") != "test-service-token" {
			t.Errorf("unexpected internal request: %s", r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var body struct {
			OrderNo string `json:"order_no"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.OrderNo == "" {
			t.Errorf("invalid start request: %+v %v", body, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"order_no": body.OrderNo, "status": "pending"}})
	}))
	defer server.Close()
	starter := PaidStarter{UserDB: db, GatewayURL: server.URL, ServiceToken: "test-service-token"}
	if _, err := starter.DispatchBatch(ctx); err != nil || calls != 0 {
		t.Fatalf("before callback: calls=%d err=%v", calls, err)
	}
	callback := centralcharge.PaymentCallbackStore{DB: db, ExpectedProvider: "simulation", ExpectedMerchantID: "test-merchant", ExpectedAppID: "test-app"}
	confirmed, err := callback.Apply(ctx, centralcharge.VerifiedPayment{Provider: "simulation", MerchantID: "test-merchant", AppID: "test-app",
		MerchantOrderNo: intent.MerchantOrderNo, TransactionID: transactionID, OpenID: intent.OpenID,
		PaidCents: intent.Estimate.TotalCents, PaidAt: time.Now().UTC()})
	if err != nil || confirmed.ChargeOrderID == 0 {
		t.Fatalf("callback=%+v err=%v", confirmed, err)
	}
	if count, err := starter.DispatchBatch(ctx); err != nil || count != 1 || calls != 1 {
		t.Fatalf("after callback: accepted=%d calls=%d err=%v", count, calls, err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE charge_order SET status = 'charging' WHERE id = ?", confirmed.ChargeOrderID); err != nil {
		t.Fatal(err)
	}
	if count, err := starter.DispatchBatch(ctx); err != nil || count != 0 || calls != 1 {
		t.Fatalf("after charging: accepted=%d calls=%d err=%v", count, calls, err)
	}
}
