package charge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func openAccountDB(t *testing.T, key string) *gorm.DB {
	t.Helper()
	db, err := dbconn.Open(context.Background(), os.Getenv(key))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	return orm
}

// accountRouter 构造一个带真实会话的路由，
// 让处理器里的归属与状态校验完全按生产的样子跑。
func accountRouter(t *testing.T, userDB, adminDB *gorm.DB, userID uint64) http.Handler {
	t.Helper()
	options, err := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { client.Close() })
	ctx := context.Background()
	sid := "test-acct-" + uuid.NewString()[:8]
	if err := client.Set(ctx, "user:session:"+sid, "1", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	jwt, err := auth.NewJWT("account-test-secret-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Sign(auth.Claims{
		Subject: strconv.FormatUint(userID, 10), Kind: "user", SessionID: sid, OpenID: "qa-account",
		IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	UserAccountAPI{
		Auth:   identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: client}, Users: identity.UserStore{DB: userDB}},
		UserDB: userDB, AdminDB: adminDB, Gateway: serviceclient.Client{},
		DevelopmentPhone: true, Prepay: payment.Simulator{},
	}.Register(router)
	DevelopmentPaymentAPI{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: client}, Users: identity.UserStore{DB: userDB}}, DB: userDB, Store: PaymentCallbackStore{DB: userDB, ExpectedProvider: "simulation", ExpectedMerchantID: "local-simulation", ExpectedAppID: "wx_local_dev"}}.Register(router)
	// 使用请求包装器注入 Bearer 令牌，覆盖已注册路由；Gin 的 Use 只影响后续注册路由。
	return &authenticatedRouter{Engine: router, token: token}
}

// authenticatedRouter 让每个请求都带上会话 token。
type authenticatedRouter struct {
	*gin.Engine
	token string
}

func (a *authenticatedRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.Header.Set("Authorization", "Bearer "+a.token)
	a.Engine.ServeHTTP(w, r)
}

func callJSON(t *testing.T, router http.Handler, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var request *http.Request
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request = httptest.NewRequest(method, path, strings.NewReader(string(encoded)))
		request.Header.Set("Content-Type", "application/json")
	} else {
		request = httptest.NewRequest(method, path, nil)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var envelope map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &envelope)
	return response.Code, envelope
}

// TestPhoneBindRejectsNumberAlreadyOwned 验证手机号唯一绑定、明文存储及解绑状态。
func TestPhoneBindRejectsNumberAlreadyOwned(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" || os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("disposable MySQL and Redis required")
	}
	userDB := openAccountDB(t, "TEST_USER_DATABASE_URL")
	adminDB := openAccountDB(t, "TEST_ADMIN_DATABASE_URL")

	first, second := createTwoUsers(t, userDB)
	t.Cleanup(func() {
		userDB.Exec("UPDATE user SET phone = NULL WHERE id IN ?", []uint64{first, second})
		userDB.Exec("DELETE FROM wallet_account WHERE user_id IN ?", []uint64{first, second})
		userDB.Exec("DELETE FROM user WHERE id IN ?", []uint64{first, second})
	})
	router := accountRouter(t, userDB, adminDB, second)

	// 第二个账号要认领第一个账号已经占用的号码。
	if err := userDB.Exec("UPDATE user SET phone = ? WHERE id = ?", "13900000001", first).Error; err != nil {
		t.Fatal(err)
	}
	code, body := callJSON(t, router, "POST", "/api/v1/user/phone/bind", map[string]any{"phone": "13900000001"})
	if code != 409 {
		t.Fatalf("duplicate phone returned %d: %s", code, body["message"])
	}

	// 格式不对的号码在任何查询之前就被拒绝。
	if code, _ := callJSON(t, router, "POST", "/api/v1/user/phone/bind", map[string]any{"phone": "12345"}); code != 400 {
		t.Fatalf("malformed phone returned %d, want 400", code)
	}
	// 未被占用的号码绑定成功，返回时已做脱敏。
	if code, body := callJSON(t, router, "POST", "/api/v1/user/phone/bind", map[string]any{"phone": " 13900000002 "}); code != 200 {
		t.Fatalf("bind returned %d: %s", code, body["message"])
	}
	var stored struct {
		Phone *string
	}
	if err := userDB.Table("user").Select("phone").Where("id = ?", second).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Phone == nil || *stored.Phone != "13900000002" {
		t.Fatal("phone was not stored as normalized plaintext")
	}
	if err := userDB.Exec("INSERT INTO wallet_account(user_id) VALUES(?)", second).Error; err != nil {
		t.Fatal(err)
	}
	profile, err := (identity.UserStore{DB: userDB}).Profile(t.Context(), second)
	if err != nil || !profile.PhoneBound {
		t.Fatalf("profile binding state: %+v %v", profile, err)
	}
	if code, _ := callJSON(t, router, "POST", "/api/v1/user/phone/unbind", nil); code != 200 {
		t.Fatalf("unbind returned %d", code)
	}
	var remaining int64
	if err := userDB.Table("user").Where("id = ? AND phone IS NULL", second).Count(&remaining).Error; err != nil || remaining != 1 {
		t.Fatalf("unbind did not clear phone: %d %v", remaining, err)
	}
	profile, err = (identity.UserStore{DB: userDB}).Profile(t.Context(), second)
	if err != nil || profile.PhoneBound {
		t.Fatalf("profile unbinding state: %+v %v", profile, err)
	}
}

func TestConcurrentPhoneBindingKeepsOneOwner(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" || os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("disposable MySQL and Redis required")
	}
	db := openAccountDB(t, "TEST_USER_DATABASE_URL")
	adminDB := openAccountDB(t, "TEST_ADMIN_DATABASE_URL")
	first, second := createTwoUsers(t, db)
	t.Cleanup(func() { db.Exec("DELETE FROM user WHERE id IN ?", []uint64{first, second}) })
	routers := []http.Handler{accountRouter(t, db, adminDB, first), accountRouter(t, db, adminDB, second)}
	start := make(chan struct{})
	statuses := make(chan int, 2)
	var group sync.WaitGroup
	for _, router := range routers {
		group.Go(func() {
			<-start
			code, _ := callJSON(t, router, "POST", "/api/v1/user/phone/bind", map[string]any{"phone": "13900000009"})
			statuses <- code
		})
	}
	close(start)
	group.Wait()
	close(statuses)
	counts := map[int]int{}
	for code := range statuses {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("binding responses: %v", counts)
	}
	var owners int64
	if err := db.Table("user").Where("phone = ?", "13900000009").Count(&owners).Error; err != nil || owners != 1 {
		t.Fatalf("phone owners: %d %v", owners, err)
	}
}

// TestWalletRefundFreezesBalanceOnClaim 验证退款申请立即冻结对应余额，防止同一资金同时消费与退款。
func TestWalletRefundFreezesBalanceOnClaim(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" || os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("disposable MySQL and Redis required")
	}
	userDB := openAccountDB(t, "TEST_USER_DATABASE_URL")
	adminDB := openAccountDB(t, "TEST_ADMIN_DATABASE_URL")
	userID := createUserWithWallet(t, userDB, 50000)
	if err := userDB.Exec("INSERT INTO payment_order(order_no,biz_type,biz_id,user_id,pay_method,total_cents,paid_cents,status,wechat_transaction_id,created_month) VALUES(?,'wallet_recharge',0,?,'wechat',50000,50000,'paid',?,?)", "qa-refund-"+uuid.NewString(), userID, uuid.NewString(), utcDate()).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		userDB.Exec("DELETE p FROM wallet_refund_part p JOIN refund_record r ON r.id=p.refund_record_id WHERE r.user_id=?", userID)
		userDB.Exec("DELETE FROM refund_record WHERE user_id=?", userID)
		userDB.Exec("DELETE FROM payment_order WHERE user_id=?", userID)
		userDB.Exec("DELETE FROM wallet_refund_request WHERE user_id = ?", userID)
		userDB.Exec("DELETE FROM wallet_account WHERE user_id = ?", userID)
		userDB.Exec("DELETE FROM user WHERE id = ?", userID)
	})
	router := accountRouter(t, userDB, adminDB, userID)

	balance := readBalance(t, router)
	if balance["available_cents"] != float64(50000) {
		t.Fatalf("starting balance = %v", balance)
	}

	requestID := uuid.NewString()
	code, body := callJSON(t, router, "POST", "/api/v1/user/wallet/refund", map[string]any{
		"request_id": requestID, "amount_cents": 30000, "reason": "验收提取",
	})
	if code != 200 {
		t.Fatalf("refund claim returned %d: %s", code, body["message"])
	}
	balance = readBalance(t, router)
	if balance["frozen_cents"] != float64(30000) || balance["available_cents"] != float64(20000) {
		t.Fatalf("balance after claim = %v, want 30000 frozen / 20000 available", balance)
	}

	// 申领金额超过剩余余额必须被拒绝。
	code, _ = callJSON(t, router, "POST", "/api/v1/user/wallet/refund", map[string]any{
		"request_id": uuid.NewString(), "amount_cents": 90000, "reason": "超额",
	})
	if code != 409 {
		t.Fatalf("over-balance claim returned %d, want 409", code)
	}

	// 同一个请求号是幂等的，不能第二次冻结。
	if code, _ := callJSON(t, router, "POST", "/api/v1/user/wallet/refund", map[string]any{
		"request_id": requestID, "amount_cents": 30000, "reason": "验收提取",
	}); code != 200 {
		t.Fatalf("replayed claim returned %d", code)
	}
	balance = readBalance(t, router)
	if balance["frozen_cents"] != float64(30000) {
		t.Fatalf("replay froze twice: %v", balance)
	}
}

func readBalance(t *testing.T, router http.Handler) map[string]any {
	t.Helper()
	code, body := callJSON(t, router, "GET", "/api/v1/user/wallet/balance", nil)
	if code != 200 {
		t.Fatalf("balance returned %d: %+v", code, body)
	}
	data, _ := body["data"].(map[string]any)
	return data
}

func createTwoUsers(t *testing.T, db *gorm.DB) (uint64, uint64) {
	t.Helper()
	ids := make([]uint64, 0, 2)
	for range 2 {
		openid := "qa-phone-" + uuid.NewString()[:8]
		if err := db.Exec("INSERT INTO user(openid) VALUES(?)", openid).Error; err != nil {
			t.Fatal(err)
		}
		var id uint64
		db.Table("user").Where("openid = ?", openid).Pluck("id", &id)
		ids = append(ids, id)
	}
	return ids[0], ids[1]
}

func createUserWithWallet(t *testing.T, db *gorm.DB, balance int64) uint64 {
	t.Helper()
	openid := "qa-wallet-" + uuid.NewString()[:8]
	if err := db.Exec("INSERT INTO user(openid) VALUES(?)", openid).Error; err != nil {
		t.Fatal(err)
	}
	var id uint64
	db.Table("user").Where("openid = ?", openid).Pluck("id", &id)
	if err := db.Exec("INSERT INTO wallet_account(user_id,balance_cents,frozen_cents,status) VALUES(?,?,0,'active')", id, balance).Error; err != nil {
		t.Fatal(err)
	}
	return id
}
