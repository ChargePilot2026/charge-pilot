package charge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestSimulationHTTPPaymentCreatesChargeOnlyAfterCallback(t *testing.T) {
	userURL, adminURL := os.Getenv("TEST_USER_DATABASE_URL"), os.Getenv("TEST_ADMIN_DATABASE_URL")
	if userURL == "" || adminURL == "" {
		t.Skip("set disposable user and admin database URLs")
	}
	ctx := context.Background()
	userDB, err := dbconn.Open(ctx, userURL)
	if err != nil {
		t.Fatal(err)
	}
	defer userDB.Close()
	adminDB, err := dbconn.Open(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	station, err := adminDB.ExecContext(ctx, "INSERT INTO station (code,name,longitude,latitude) VALUES (?,?,113.90000000,22.50000000)", "pay-"+uuid.NewString(), "Payment Test")
	if err != nil {
		t.Fatal(err)
	}
	stationID, _ := station.LastInsertId()
	defer adminDB.ExecContext(ctx, "DELETE FROM station WHERE id = ?", stationID)
	rule, err := adminDB.ExecContext(ctx, "INSERT INTO pricing_rule (name,station_id,mode,time_of_use_json,service_fee_cents_per_kwh) VALUES (?,?,'kwh',?,40)", "Payment Test", stationID, `[{"period":"all","start":"00:00","end":"24:00","electric_price_cents":100}]`)
	if err != nil {
		t.Fatal(err)
	}
	ruleID, _ := rule.LastInsertId()
	defer adminDB.ExecContext(ctx, "DELETE FROM pricing_rule WHERE id = ?", ruleID)
	user, err := userDB.ExecContext(ctx, "INSERT INTO user (openid) VALUES (?)", "http-pay-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := user.LastInsertId()
	var merchantNo, intentID string
	defer func() {
		_, _ = userDB.ExecContext(ctx, "DELETE FROM event_outbox WHERE event_id IN (SELECT event_id FROM charge_event_log WHERE charge_order_id IN (SELECT id FROM charge_order WHERE user_id = ?))", userID)
		_, _ = userDB.ExecContext(ctx, "DELETE FROM charge_event_log WHERE charge_order_id IN (SELECT id FROM charge_order WHERE user_id = ?)", userID)
		_, _ = userDB.ExecContext(ctx, "DELETE FROM charge_order_pricing WHERE user_id = ?", userID)
		_, _ = userDB.ExecContext(ctx, "DELETE FROM charge_order WHERE user_id = ?", userID)
		_, _ = userDB.ExecContext(ctx, "DELETE FROM charge_prepay WHERE payment_order_id IN (SELECT id FROM payment_order WHERE user_id = ?)", userID)
		_, _ = userDB.ExecContext(ctx, "DELETE FROM charge_payment_intent WHERE user_id = ?", userID)
		_, _ = userDB.ExecContext(ctx, "DELETE FROM payment_order WHERE user_id = ?", userID)
		if intentID != "" {
			_, _ = userDB.ExecContext(ctx, "DELETE FROM payment_callback_idempotent WHERE wechat_transaction_id = ?", "SIMTX"+intentID)
		}
		_, _ = userDB.ExecContext(ctx, "DELETE FROM user WHERE id = ?", userID)
	}()
	portCode := "http-pay-device:1"
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("code") != portCode || r.Header.Get("X-Service-Token") != "test-service-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"kind": "port", "device_id": "http-pay-device", "station_id": stationID,
			"port": map[string]any{"port_id": portCode, "device_id": "http-pay-device", "port_no": 1, "port_status": "idle", "online": true, "available": true}}})
	}))
	defer gateway.Close()
	jwt, err := auth.NewJWT("test-payment-secret-must-have-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	access, err := jwt.Sign(auth.Claims{Subject: strconv.FormatInt(userID, 10), Kind: "user", OpenID: "test-openid", SessionID: "test-session", IssuedAt: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := httpapi.NewRouter()
	serviceToken := "test-service-token"
	callbackStore := PaymentCallbackStore{DB: userDB, ExpectedProvider: "simulation", ExpectedMerchantID: "local-simulation", ExpectedAppID: "wx_local_dev"}
	PaymentStartAPI{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: scanSession{}, Users: scanUser{}},
		Scan: ScanAPI{GatewayURL: gateway.URL, ServiceToken: serviceToken}, Pricing: pricing.Store{DB: adminDB},
		Intents: PaymentIntentStore{DB: userDB}, Provider: payment.Simulator{}}.Register(router)
	SimulationCallbackAPI{DB: userDB, Store: callbackStore, ServiceToken: serviceToken}.Register(router)
	requestBody, _ := json.Marshal(map[string]any{"client_request_id": uuid.NewString(), "port_id": portCode, "estimated_kwh": "1.000", "estimated_minutes": 60})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/user/scan/start", bytes.NewReader(requestBody))
	request.Header.Set("Authorization", "Bearer "+access)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			IntentID        string `json:"intent_id"`
			MerchantOrderNo string `json:"merchant_order_no"`
			TotalCents      int64  `json:"total_cents"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Data.TotalCents != 140 {
		t.Fatalf("start response=%s err=%v", response.Body.String(), err)
	}
	intentID, merchantNo = envelope.Data.IntentID, envelope.Data.MerchantOrderNo
	var chargeCount int
	if err := userDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM charge_order WHERE user_id = ?", userID).Scan(&chargeCount); err != nil || chargeCount != 0 {
		t.Fatalf("before callback charges=%d err=%v", chargeCount, err)
	}
	callbackBody, _ := json.Marshal(map[string]any{"merchant_order_no": merchantNo})
	callback := httptest.NewRequest(http.MethodPost, "/api/v1/internal/payments/simulate-success", bytes.NewReader(callbackBody))
	callback.Header.Set("X-Service-Token", serviceToken)
	callbackResponse := httptest.NewRecorder()
	router.ServeHTTP(callbackResponse, callback)
	if callbackResponse.Code != http.StatusOK {
		t.Fatalf("callback status=%d body=%s", callbackResponse.Code, callbackResponse.Body.String())
	}
	if err := userDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM charge_order WHERE user_id = ? AND status = 'paid'", userID).Scan(&chargeCount); err != nil || chargeCount != 1 {
		t.Fatalf("after callback charges=%d err=%v", chargeCount, err)
	}
}
