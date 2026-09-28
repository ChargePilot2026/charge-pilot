package charge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
)

type liveSession struct{}

func (liveSession) Exists(context.Context, string) (bool, error) { return true, nil }

type liveUser struct{}

func (liveUser) Active(context.Context, uint64) (bool, error) { return true, nil }

func TestUserStopUsesJWTIdentityAndDoesNotClaimPowerOff(t *testing.T) {
	jwt, err := auth.NewJWT("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	token, err := jwt.Sign(auth.Claims{Subject: "42", Kind: "user", OpenID: "wx-test", SessionID: "sid", IssuedAt: now, ExpiresAt: now + 600})
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OrderNo string `json:"order_no"`
			UserID  uint64 `json:"user_id"`
		}
		if r.Header.Get("X-Service-Token") != "secret" || json.NewDecoder(r.Body).Decode(&body) != nil || body.OrderNo != "ORD-123" || body.UserID != 42 {
			t.Errorf("bad gateway request: %+v", body)
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"code":0,"data":{"accepted":true,"stopped":false,"command_id":"abc","status":"sent"}}`))
	}))
	defer gateway.Close()
	router := httpapi.NewRouter()
	UserStopAPI{JWT: jwt, Sessions: liveSession{}, Users: liveUser{}, GatewayURL: gateway.URL, ServiceToken: "secret"}.Register(router)
	request := func(authHeader string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/user/charge/stop", strings.NewReader(`{"order_no":"ORD-123"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader)
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		return resp
	}
	if response := request(""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized stop: %d", response.Code)
	}
	response := request("Bearer " + token)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"stopped":false`) {
		t.Fatalf("stop response: %d %s", response.Code, response.Body.String())
	}
}
