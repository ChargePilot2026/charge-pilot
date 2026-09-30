package charge

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

type scanSession struct{}

func (scanSession) Exists(context.Context, string) (bool, error) { return true, nil }

type scanUser struct{}

func (scanUser) Active(context.Context, uint64) (bool, error) { return true, nil }

type scanOperation string

func (s scanOperation) DeviceStatus(context.Context, string) (string, error) {
	if s == "error" {
		return "", errors.New("storage unavailable")
	}
	return string(s), nil
}

func TestScanIsPublicAndReturnsOnlyGatewayReadResult(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/scan/resolve" || r.URL.Query().Get("code") != "board:1" || r.Header.Get("X-Service-Token") != "service-token" {
			t.Errorf("unexpected gateway request %s", r.URL.String())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"kind":"port","device_id":"board","port":{"port_id":"board:1","device_id":"board","port_no":1,"port_status":"idle","online":true,"available":true}}}`))
	}))
	defer backend.Close()
	jwt, err := auth.NewJWT("test-scan-secret-must-have-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token, err := jwt.Sign(auth.Claims{Subject: "7", Kind: "user", OpenID: "openid", SessionID: "session", IssuedAt: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := httpapi.NewRouter()
	ScanAPI{Operations: scanOperation("enabled"), Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: scanSession{}, Users: scanUser{}}, GatewayURL: backend.URL, ServiceToken: "service-token"}.Register(router)
	for _, route := range []string{"/api/v1/user/scan/resolve", "/api/v1/user/scan/port"} {
		body := `{"code":"board:1"}`
		if route == "/api/v1/user/scan/port" {
			body = `{"port_id":"board:1"}`
		}
		unauth := httptest.NewRecorder()
		anonymous := httptest.NewRequest(http.MethodPost, route, bytes.NewBufferString(body))
		anonymous.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(unauth, anonymous)
		if unauth.Code != http.StatusOK || !bytes.Contains(unauth.Body.Bytes(), []byte(`"available":true`)) {
			t.Fatalf("%s anonymous status=%d body=%s", route, unauth.Code, unauth.Body.String())
		}
		request := httptest.NewRequest(http.MethodPost, route, bytes.NewBufferString(body))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"available":true`)) {
			t.Fatalf("%s status=%d body=%s", route, response.Code, response.Body.String())
		}
	}
}

func TestScanShowsOperationalStateAndFailsClosed(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"kind":"device","device_id":"board","ports":[{"port_id":"board:1","device_id":"board","port_no":1,"port_status":"idle","online":true,"available":true},{"port_id":"board:2","device_id":"board","port_no":2,"port_status":"charging","online":true,"available":false}]}}`))
	}))
	defer backend.Close()
	for _, status := range []string{"enabled", "disabled", "fault", "retired", "error"} {
		router := httpapi.NewRouter()
		ScanAPI{GatewayURL: backend.URL, ServiceToken: "service", Operations: scanOperation(status)}.Register(router)
		response := httptest.NewRecorder()
		request := httptest.NewRequest("POST", "/api/v1/user/scan/resolve", bytes.NewBufferString(`{"code":"board"}`))
		router.ServeHTTP(response, request)
		if status == "error" {
			if response.Code != 503 {
				t.Fatalf("operation outage: %d", response.Code)
			}
			continue
		}
		if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"device_status":"`+status+`"`)) {
			t.Fatalf("%s: %s", status, response.Body.String())
		}
		if status != "enabled" && bytes.Contains(response.Body.Bytes(), []byte(`"available":true`)) {
			t.Fatalf("disabled device selectable: %s", response.Body.String())
		}
		if !bytes.Contains(response.Body.Bytes(), []byte(`"port_status":"charging"`)) {
			t.Fatal("operational state erased physical charging state")
		}
	}
	_, status := (ScanAPI{GatewayURL: backend.URL, ServiceToken: "service"}).lookup(context.Background(), "board")
	if status != 503 {
		t.Fatal("missing operation guard allowed discovery")
	}
}

func TestDisabledDeviceCannotReadOffers(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"kind":"port","device_id":"board","port":{"port_id":"board:1","device_id":"board","port_no":1,"port_status":"idle","online":true,"available":true}}}`))
	}))
	defer backend.Close()
	router := httpapi.NewRouter()
	PaymentStartAPI{Scan: ScanAPI{GatewayURL: backend.URL, ServiceToken: "service", Operations: scanOperation("disabled")}}.Register(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("POST", "/api/v1/user/scan/offers", bytes.NewBufferString(`{"port_id":"board:1"}`)))
	if response.Code != 409 {
		t.Fatalf("disabled offers: %d %s", response.Code, response.Body.String())
	}
}

func TestCanonicalScanCode(t *testing.T) {
	for _, tc := range []struct {
		raw, code string
		valid     bool
	}{
		{"board:1", "board:1", true},
		{"https://charge.example/scan?code=board%3A1", "board:1", true},
		{"https://charge.example/scan?code=board:1&code=other", "", false},
		{"http://charge.example/scan?code=board:1", "", false},
		{"https://charge.example/scan?port=board:1", "", false},
	} {
		got, ok := canonicalScanCode(tc.raw)
		if got != tc.code || ok != tc.valid {
			t.Fatalf("scan code %q: code=%q valid=%t", tc.raw, got, ok)
		}
	}
}
