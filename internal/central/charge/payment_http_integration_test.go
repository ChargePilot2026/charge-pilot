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
	adminORM, err := dbconn.WrapGORM(adminDB)
	if err != nil {
		t.Fatal(err)
	}
	station, err := adminDB.ExecContext(ctx, "INSERT INTO station (name,longitude,latitude) VALUES (?,113.90000000,22.50000000)", "Payment Test")
	if err != nil {
		t.Fatal(err)
	}
	stationID, _ := station.LastInsertId()
	defer adminDB.ExecContext(ctx, "DELETE FROM station WHERE id = ?", stationID)
	scheme := pricing.Scheme{Name: "Payment Test", Packages: []pricing.Package{{ID: 1, Name: "60 minute package", Mode: "duration", PriceCents: 600, Minutes: 60}}}.Normalized()
	specJSON, _ := json.Marshal(scheme.SpecFor(scheme.Packages[0]))
	rule, err := adminDB.ExecContext(ctx, "INSERT INTO pricing_rule (name,station_id,spec_json) VALUES (?,?,?)", scheme.Name, stationID, string(specJSON))
	if err != nil {
		t.Fatal(err)
	}
	ruleID, _ := rule.LastInsertId()
	defer adminDB.ExecContext(ctx, "DELETE FROM pricing_rule WHERE id=?", ruleID)
	offerID := ruleID*100 + 1
	capabilities, _ := json.Marshal(pricing.Capabilities{StopPolicyVerified: true, Duration: true, MaxMinutes: 600})
	if _, err := adminDB.ExecContext(ctx, "INSERT INTO device_meta(device_id,station_id,status,execution_capabilities) VALUES('http-pay-device',?,'enabled',?)", stationID, string(capabilities)); err != nil {
		t.Fatal(err)
	}
	defer adminDB.ExecContext(ctx, "DELETE FROM device_meta WHERE device_id='http-pay-device'")
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
	callbackStore := PaymentCallbackStore{DB: testGORMDB(t, userDB), ExpectedProvider: "simulation", ExpectedMerchantID: "local-simulation", ExpectedAppID: "wx_local_dev"}
	PaymentStartAPI{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: scanSession{}, Users: scanUser{}},
		Scan: ScanAPI{Operations: DeviceOperationStore{DB: adminORM}, GatewayURL: gateway.URL, ServiceToken: serviceToken}, Pricing: pricing.Store{DB: adminORM},
		Intents: PaymentIntentStore{DB: testGORMDB(t, userDB)}, Provider: payment.Simulator{}}.Register(router)
	SimulationCallbackAPI{DB: testGORMDB(t, userDB), Store: callbackStore, ServiceToken: serviceToken}.Register(router)
	offersBody, _ := json.Marshal(map[string]any{"port_id": portCode})
	offersRequest := httptest.NewRequest(http.MethodPost, "/api/v1/user/scan/offers", bytes.NewReader(offersBody))
	offersResponse := httptest.NewRecorder()
	router.ServeHTTP(offersResponse, offersRequest)
	if offersResponse.Code != http.StatusOK || !bytes.Contains(offersResponse.Body.Bytes(), []byte(`"price_cents":600`)) {
		t.Fatalf("anonymous offers status=%d body=%s", offersResponse.Code, offersResponse.Body.String())
	}
	manualBody, _ := json.Marshal(map[string]any{"client_request_id": uuid.NewString(), "port_id": portCode, "offer_id": offerID, "estimated_kwh": "9"})
	manualRequest := httptest.NewRequest(http.MethodPost, "/api/v1/user/scan/start", bytes.NewReader(manualBody))
	manualRequest.Header.Set("Authorization", "Bearer "+access)
	manualResponse := httptest.NewRecorder()
	router.ServeHTTP(manualResponse, manualRequest)
	if manualResponse.Code != http.StatusBadRequest {
		t.Fatalf("client supplied charging amount was accepted: %d %s", manualResponse.Code, manualResponse.Body.String())
	}
	requestBody, _ := json.Marshal(map[string]any{"client_request_id": uuid.NewString(), "port_id": portCode, "offer_id": offerID})
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
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Data.TotalCents != 600 {
		t.Fatalf("start response=%s err=%v", response.Body.String(), err)
	}
	intentID, merchantNo = envelope.Data.IntentID, envelope.Data.MerchantOrderNo
	if _, err := adminDB.ExecContext(ctx, "UPDATE pricing_rule SET status='disabled',version=version+1 WHERE id=?", ruleID); err != nil {
		t.Fatal(err)
	}
	retry := httptest.NewRequest(http.MethodPost, "/api/v1/user/scan/start", bytes.NewReader(requestBody))
	retry.Header.Set("Authorization", "Bearer "+access)
	retryResponse := httptest.NewRecorder()
	router.ServeHTTP(retryResponse, retry)
	if retryResponse.Code != http.StatusOK || !bytes.Contains(retryResponse.Body.Bytes(), []byte(merchantNo)) || !bytes.Contains(retryResponse.Body.Bytes(), []byte(`"total_cents":600`)) {
		t.Fatalf("frozen checkout replay status=%d body=%s", retryResponse.Code, retryResponse.Body.String())
	}
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
