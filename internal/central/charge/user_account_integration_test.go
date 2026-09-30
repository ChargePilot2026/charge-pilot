package charge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/phonecrypto"
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
		PhoneKey:         []byte("account-test-phone-key-32-bytes!"),
		DevelopmentPhone: true, Prepay: payment.Simulator{},
	}.Register(router)
	DevelopmentPaymentAPI{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: client}, Users: identity.UserStore{DB: userDB}}, DB: userDB, Store: PaymentCallbackStore{DB: userDB, ExpectedProvider: "simulation", ExpectedMerchantID: "local-simulation", ExpectedAppID: "wx_local_dev"}}.Register(router)
	// gin 的 Use 只对其后注册的路由生效，
	// 所以 bearer token 改由这个 wrapper 打到每个请求上。
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

// TestPhoneBindRejectsNumberAlreadyOwned —— 一个手机号只能对应一个账号。
// 这道检查必须发生在写入之前，否则两个账号可能短暂共用同一个号码。
func TestPhoneBindRejectsNumberAlreadyOwned(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" || os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("disposable MySQL and Redis required")
	}
	userDB := openAccountDB(t, "TEST_USER_DATABASE_URL")
	adminDB := openAccountDB(t, "TEST_ADMIN_DATABASE_URL")

	first, second := createTwoUsers(t, userDB)
	t.Cleanup(func() {
		userDB.Exec("UPDATE user SET phone_enc = NULL, phone_hash = NULL WHERE id IN ?", []uint64{first, second})
		userDB.Exec("DELETE FROM user WHERE id IN ?", []uint64{first, second})
	})
	router := accountRouter(t, userDB, adminDB, second)

	// 第二个账号要认领第一个账号已经占用的号码。
	if err := userDB.Exec("UPDATE user SET phone_hash = ? WHERE id = ?", phonecrypto.Hash("13900000001"), first).Error; err != nil {
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
	if code, body := callJSON(t, router, "POST", "/api/v1/user/phone/bind", map[string]any{"phone": "13900000002"}); code != 200 {
		t.Fatalf("bind returned %d: %s", code, body["message"])
	}
	var stored struct {
		Encrypted []byte `gorm:"column:phone_enc"`
		Hash      string `gorm:"column:phone_hash"`
	}
	if err := userDB.Table("user").Select("phone_enc, phone_hash").Where("id = ?", second).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if len(stored.Encrypted) == 0 || stored.Hash != phonecrypto.Hash("13900000002") {
		t.Fatal("phone was not stored encrypted with its hash")
	}
	// 明文不允许出现在存储 blob 里的任何位置。
	if json.Valid(stored.Encrypted) && string(stored.Encrypted) == "13900000002" {
		t.Fatal("phone was stored in the clear")
	}
}

// TestWalletRefundFreezesBalanceOnClaim —— 钱包退款一申请就冻结资金，之后批准才是真正打款。
// 没有这道冻结，同一笔余额可能同时供一笔充电和一笔退款。
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
