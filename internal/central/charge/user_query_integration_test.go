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
	"gorm.io/gorm"
)

func curveFixture(t *testing.T, ctx context.Context, deviceID string) (orderID, userID uint64) {
	t.Helper()
	userDB, err := dbconn.Open(ctx, os.Getenv("TEST_USER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { userDB.Close() })
	userORM, err := dbconn.WrapGORM(userDB)
	if err != nil {
		t.Fatal(err)
	}
	suffix := uuid.NewString()[:8]
	openid := "QA-CURVE-" + suffix
	if err := userORM.Exec("INSERT INTO user(openid) VALUES(?)", openid).Error; err != nil {
		t.Fatal(err)
	}
	userORM.Table("user").Where("openid = ?", openid).Pluck("id", &userID)
	payNo := "QA-CURVE-PAY-" + suffix
	if err := userORM.Exec("INSERT INTO payment_order(order_no,biz_type,biz_id,user_id,pay_method,total_cents,paid_cents,status,created_month) VALUES(?,'charge',0,?,'wechat',5000,5000,'paid','2026-09-29')",
		payNo, userID).Error; err != nil {
		t.Fatal(err)
	}
	var paymentID uint64
	userORM.Table("payment_order").Where("order_no = ?", payNo).Pluck("id", &paymentID)
	orderNo := "QA-CURVE-ORD-" + suffix
	if err := userORM.Exec("INSERT INTO charge_order(order_no,user_id,device_id,port_no,payment_order_id,status,started_at,charged_kwh,charged_seconds,created_month) VALUES(?,?,?,1,?,'charging',?,1.500,1200,'2026-09-29')",
		orderNo, userID, deviceID, paymentID, time.Now().UTC().Add(-20*time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	userORM.Table("charge_order").Where("order_no = ?", orderNo).Pluck("id", &orderID)
	userORM.Exec("UPDATE payment_order SET biz_id = ? WHERE id = ?", orderID, paymentID)
	if err := userORM.Exec("INSERT INTO charge_fee_receipt(charge_order_id,calculation_no,result_json,shortfall_cents) VALUES(?,'QA-CURVE-FEE',?,0)",
		orderID, `{"electric_cents":300,"service_cents":75,"total_cents":375}`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		userORM.Exec("DELETE FROM charge_fee_receipt WHERE charge_order_id = ?", orderID)
		userORM.Exec("DELETE FROM charge_order WHERE id = ?", orderID)
		userORM.Exec("DELETE FROM payment_order WHERE id = ?", paymentID)
		userORM.Exec("DELETE FROM user WHERE id = ?", userID)
	})
	return orderID, userID
}

// fakeGateway answers the internal telemetry call so the curve path can be
// verified without a running gateway.
func userIDOf(t *testing.T, db *gorm.DB, orderID uint64) uint64 {
	t.Helper()
	var id uint64
	db.Table("charge_order").Where("id = ?", orderID).Pluck("user_id", &id)
	return id
}

func fakeGateway(points []map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "data": map[string]any{"device_id": "d", "series": points, "count": len(points)},
		})
	}))
}

// The curve is read from gateway telemetry, so a missing value_num mapping would
// surface here as a series full of nulls.
func TestUserCurveReadsTelemetryValues(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	ctx := context.Background()
	orderID, _ := curveFixture(t, ctx, "QA-CURVE-DEV")
	var orderNo string
	userDB, _ := dbconn.Open(ctx, os.Getenv("TEST_USER_DATABASE_URL"))
	defer userDB.Close()
	orm, _ := dbconn.WrapGORM(userDB)
	orm.Table("charge_order").Where("id = ?", orderID).Pluck("order_no", &orderNo)

	// Two time buckets, each carrying a different subset of metrics.
	first := time.Now().UTC().Add(-18 * time.Minute).Truncate(time.Second)
	second := first.Add(8 * time.Minute)
	gateway := fakeGateway([]map[string]any{
		{"ts": first.Format(time.RFC3339), "power_w": 1500.0, "voltage_v": 220.0, "meter_kwh": 0.1},
		{"ts": second.Format(time.RFC3339), "power_w": 2400.0, "voltage_v": 219.0, "meter_kwh": 0.9},
	})
	defer gateway.Close()

	// A real session is required: the curve handler goes through the same
	// authenticator as production, so the test signs a token and registers it.
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("redis required")
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { client.Close() })
	sid := "test-curve-" + uuid.NewString()[:8]
	if err := client.Set(context.Background(), "user:session:"+sid, "1", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	jwt, err := auth.NewJWT("curve-test-secret-at-least-32-bytes-long")
	if err != nil {
		t.Fatal(err)
	}
	userID := userIDOf(t, orm, orderID)
	token, err := jwt.Sign(auth.Claims{
		Subject: strconv.FormatUint(userID, 10), Kind: "user", SessionID: sid, OpenID: "qa-curve-user",
		IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	api := UserQueryAPI{
		Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: client}, Users: identity.UserStore{DB: orm}},
		DB:   orm, GatewayURL: gateway.URL, ServiceToken: "svc", Gateway: serviceclient.Client{},
	}
	api.Register(router)

	request := httptest.NewRequest("GET", "/api/v1/user/charge/ongoing/curve?order_id="+orderNo, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("curve status = %d: %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Series []struct {
				PowerW   *float64 `json:"power_w"`
				VoltageV *float64 `json:"voltage_v"`
				MeterKWh *float64 `json:"meter_kwh"`
				CurrentA *float64 `json:"current_a"`
			} `json:"series"`
			Count int `json:"count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Count != 2 || len(envelope.Data.Series) != 2 {
		t.Fatalf("series = %+v", envelope.Data.Series)
	}
	oldest := envelope.Data.Series[0]
	if oldest.PowerW == nil || *oldest.PowerW != 1500 {
		t.Fatalf("first power_w = %v, want 1500 (values were not decoded)", oldest.PowerW)
	}
	if oldest.MeterKWh == nil || *oldest.MeterKWh != 0.1 {
		t.Fatalf("first meter_kwh = %v", oldest.MeterKWh)
	}
	// A metric the device did not report stays null rather than becoming zero.
	if oldest.CurrentA != nil {
		t.Fatalf("unreported current_a should be null, got %v", *oldest.CurrentA)
	}
	if envelope.Data.Series[1].PowerW == nil || *envelope.Data.Series[1].PowerW != 2400 {
		t.Fatalf("second power_w = %v", envelope.Data.Series[1].PowerW)
	}
}

// Billing amounts live inside the fee receipt, and the order list must surface
// them rather than zeros.
func TestUserHistoryAndDetailExposeFees(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" || os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("disposable MySQL and Redis required")
	}
	ctx := context.Background()
	orderID, _ := curveFixture(t, ctx, "QA-CURVE-DEV")
	userDB, err := dbconn.Open(ctx, os.Getenv("TEST_USER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer userDB.Close()
	orm, err := dbconn.WrapGORM(userDB)
	if err != nil {
		t.Fatal(err)
	}
	options, err := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	defer client.Close()
	sid := "test-hist-" + uuid.NewString()[:8]
	if err := client.Set(ctx, "user:session:"+sid, "1", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	jwt, err := auth.NewJWT("curve-test-secret-at-least-32-bytes-long")
	if err != nil {
		t.Fatal(err)
	}
	userID := userIDOf(t, orm, orderID)
	token, err := jwt.Sign(auth.Claims{
		Subject: strconv.FormatUint(userID, 10), Kind: "user", SessionID: sid, OpenID: "qa-curve-user",
		IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	api := UserQueryAPI{
		Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: client}, Users: identity.UserStore{DB: orm}},
		DB:   orm, Gateway: serviceclient.Client{},
	}
	api.Register(router)

	var orderNo string
	orm.Table("charge_order").Where("id = ?", orderID).Pluck("order_no", &orderNo)

	request := httptest.NewRequest("GET", "/api/v1/user/charge/history?page=1&page_size=20", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("history status = %d: %s", response.Code, response.Body.String())
	}
	var history struct {
		Data struct {
			Total int64 `json:"total"`
			Items []struct {
				OrderNo       string `json:"order_no"`
				TotalCents    int64  `json:"total_cents"`
				ElectricCents int64  `json:"electric_cents"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	var mine *struct {
		OrderNo       string `json:"order_no"`
		TotalCents    int64  `json:"total_cents"`
		ElectricCents int64  `json:"electric_cents"`
	}
	for i := range history.Data.Items {
		if history.Data.Items[i].OrderNo == orderNo {
			mine = &history.Data.Items[i]
		}
	}
	if mine == nil {
		t.Fatalf("order %s missing from history", orderNo)
	}
	if mine.ElectricCents != 300 || mine.TotalCents != 375 {
		t.Fatalf("history fees = %+v, want 300/375", *mine)
	}

	request = httptest.NewRequest("GET", "/api/v1/user/charge/"+orderNo, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("detail status = %d: %s", response.Code, response.Body.String())
	}
	var detail struct {
		Data struct {
			TotalCents    int64 `json:"total_cents"`
			ServiceCents  int64 `json:"service_cents"`
			ShortfallCent int64 `json:"shortfall_cents"`
			Refunds       []any `json:"refunds"`
			Payment       struct {
				OrderNo string `json:"order_no"`
			} `json:"payment"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Data.TotalCents != 375 || detail.Data.ServiceCents != 75 {
		t.Fatalf("detail fees = %+v", detail.Data)
	}
	if detail.Data.Payment.OrderNo == "" {
		t.Fatalf("detail did not include the payment summary: %s", response.Body.String())
	}
	if detail.Data.Refunds == nil {
		t.Fatal("refunds should be an empty list, not null")
	}
}

// Another customer's order must be indistinguishable from a missing one.
func TestUserDetailHidesForeignOrder(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" || os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("disposable MySQL and Redis required")
	}
	ctx := context.Background()
	orderID, ownerID := curveFixture(t, ctx, "QA-CURVE-DEV")
	var orderNo string
	userDB, _ := dbconn.Open(ctx, os.Getenv("TEST_USER_DATABASE_URL"))
	// 用 t.Cleanup 而不是 defer 关连接：defer 在函数体退出时先跑，那时下面
	// 注册的清理还没轮到，连接已经关了，清理报 "sql: database is closed"
	// 然后把冒名账号永久留在库里。t.Cleanup 后进先出，晚注册的先跑。
	t.Cleanup(func() { userDB.Close() })
	orm, _ := dbconn.WrapGORM(userDB)
	orm.Table("charge_order").Where("id = ?", orderID).Pluck("order_no", &orderNo)

	// A second account must not be able to read the first one's order.
	// 这个冒名账号只在本测试里存在，名字带 qa-intruder- 前缀就是为了能被认出来
	// 摘掉：不清的话，后台「充电用户」列表里会一版版多出叫"用户 #900xxx"的空账号。
	intruderOpenID := "qa-intruder-" + uuid.NewString()[:8]
	orm.Exec("INSERT INTO user(openid) VALUES(?)", intruderOpenID)
	t.Cleanup(func() {
		if err := orm.Exec("DELETE FROM user WHERE openid = ?", intruderOpenID).Error; err != nil {
			t.Errorf("清理冒名账号夹具失败: %v", err)
		}
	})
	var intruderID uint64
	orm.Table("user").Where("openid LIKE 'qa-intruder-%'").Order("id DESC").Limit(1).Pluck("id", &intruderID)
	if intruderID == 0 || intruderID == ownerID {
		t.Fatal("fixture did not create a distinct second account")
	}
	options, _ := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	client := redis.NewClient(options)
	defer client.Close()
	sid := "test-intruder-" + uuid.NewString()[:8]
	client.Set(ctx, "user:session:"+sid, "1", time.Minute)
	jwt, _ := auth.NewJWT("curve-test-secret-at-least-32-bytes-long")
	token, err := jwt.Sign(auth.Claims{
		Subject: strconv.FormatUint(intruderID, 10), Kind: "user", SessionID: sid, OpenID: "qa-intruder",
		IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	UserQueryAPI{
		Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: client}, Users: identity.UserStore{DB: orm}},
		DB:   orm, Gateway: serviceclient.Client{},
	}.Register(router)

	request := httptest.NewRequest("GET", "/api/v1/user/charge/"+orderNo, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 404 {
		t.Fatalf("foreign order returned %d, want 404: %s", response.Code, response.Body.String())
	}
}
