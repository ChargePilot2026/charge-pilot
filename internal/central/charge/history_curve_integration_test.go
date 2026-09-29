package charge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestHistoryCurveChecksOwnerAndForwardsOrderWindow(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" || os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("disposable MySQL and Redis required")
	}
	ctx := context.Background()
	orderID, userID := curveFixture(t, ctx, "QA-HISTORY-DEV")
	db, err := dbconn.Open(ctx, os.Getenv("TEST_USER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	orm, _ := dbconn.WrapGORM(db)
	var orderNo string
	orm.Table("charge_order").Where("id = ?", orderID).Pluck("order_no", &orderNo)
	ended := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	if err := orm.Table("charge_order").Where("id = ?", orderID).Update("ended_at", ended).Error; err != nil {
		t.Fatal(err)
	}
	called := false
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Header.Get("X-Service-Token") != "svc" || r.URL.Path != "/api/v1/internal/devices/QA-HISTORY-DEV/historical-curve" || r.URL.Query().Get("order_id") != orderNo || r.URL.Query().Get("port_no") != "1" || r.URL.Query().Get("granularity") != "hourly" || r.URL.Query().Get("ended_at") != ended.Format(time.RFC3339) {
			t.Errorf("gateway request mismatch: %s %s", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"granularity": "hourly", "boundary_approximate": true,
			"summary": map[string]any{"max_power_w": "750"},
			"series":  []map[string]any{{"ts": ended.Format(time.RFC3339), "power_w": 700.0}},
		}})
	}))
	defer gateway.Close()
	options, _ := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	cache := redis.NewClient(options)
	defer cache.Close()
	sid := "history-curve-" + uuid.NewString()[:8]
	if err := cache.Set(ctx, "user:session:"+sid, "1", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	jwt, _ := auth.NewJWT("curve-test-secret-at-least-32-bytes-long")
	token, err := jwt.Sign(auth.Claims{Subject: strconv.FormatUint(userID, 10), Kind: "user", SessionID: sid, OpenID: "qa-history-user", IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	UserQueryAPI{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: cache}, Users: identity.UserStore{DB: orm}}, DB: orm, GatewayURL: gateway.URL, ServiceToken: "svc", Gateway: serviceclient.Client{}}.Register(router)
	path := "/api/v1/user/charge/" + orderNo + "/curve?granularity=hourly"
	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest("GET", path, nil))
	if unauthorized.Code != 401 || called {
		t.Fatalf("unauthorized history curve status=%d gatewayCalled=%v", unauthorized.Code, called)
	}
	request := httptest.NewRequest("GET", path, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 || !called {
		t.Fatalf("history curve status=%d called=%v: %s", response.Code, called, response.Body.String())
	}
	var body struct {
		Data struct {
			Summary map[string]any `json:"summary"`
			Series  []any          `json:"series"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Summary["total_kwh"] != "1.5000" || body.Data.Summary["max_power_w"] != "750" || len(body.Data.Series) != 1 {
		t.Fatalf("historical response mismatch: %s", response.Body.String())
	}
}
