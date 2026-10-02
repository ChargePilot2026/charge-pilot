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

	"github.com/ChargePilot2026/charge-pilot/internal/central/channel"
	"github.com/ChargePilot2026/charge-pilot/internal/central/identity"
	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbutil"
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
	return accountRouterWithGateway(t, userDB, adminDB, userID, "")
}

// accountRouterWithGateway 在 accountRouter 的基础上注入 gateway 地址，
// 用于验证依赖设备运行态的接口。gatewayURL 为空表示未配置网关。
func accountRouterWithGateway(t *testing.T, userDB, adminDB *gorm.DB, userID uint64, gatewayURL string) http.Handler {
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
		GatewayURL: gatewayURL, ServiceToken: "svc", Prepay: channel.Simulator{},
	}.Register(router)
	identity.PhoneAPI{
		Auth:             identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: client}, Users: identity.UserStore{DB: userDB}},
		DB:               userDB,
		DevelopmentPhone: true,
	}.Register(router)
	payment.DevelopmentPaymentAPI{Auth: identity.SessionAuthenticator{JWT: jwt, Sessions: identity.Sessions{Redis: client}, Users: identity.UserStore{DB: userDB}}, DB: userDB, Store: payment.PaymentCallbackStore{DB: userDB, ExpectedProvider: "simulation", ExpectedMerchantID: "local-simulation", ExpectedAppID: "wx_local_dev"}}.Register(router)
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
	if err := userDB.Exec("INSERT INTO payment_order(order_no,biz_type,biz_id,user_id,pay_method,total_cents,paid_cents,status,wechat_transaction_id,created_month) VALUES(?,'wallet_recharge',0,?,'wechat',50000,50000,'paid',?,?)", "qa-refund-"+uuid.NewString(), userID, uuid.NewString(), dbutil.MonthStart()).Error; err != nil {
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

// createStation 写入一个指定创建时间的站点，用于验证未定位分支的排序。
func createStation(t *testing.T, adminDB *gorm.DB, name string, createdAt time.Time) uint64 {
	t.Helper()
	if err := adminDB.Exec(
		"INSERT INTO station(name,address,longitude,latitude,status,created_at) VALUES(?,?,?,?,'active',?)",
		name, "江西省赣州市", 114.9, 25.8, createdAt,
	).Error; err != nil {
		t.Fatal(err)
	}
	var id uint64
	adminDB.Table("station").Where("name = ?", name).Pluck("id", &id)
	return id
}

// createAnnouncement 写入一条公告，targetIDs 为空表示全局公告。
// target_ids 存的是 JSON 字符串数组，与后台创建公告的写入格式保持一致。
func createAnnouncement(t *testing.T, adminDB *gorm.DB, title, scope string, targetIDs []string) uint64 {
	t.Helper()
	encoded, err := json.Marshal(targetIDs)
	if err != nil {
		t.Fatal(err)
	}
	targets := "null"
	if scope != "global" {
		targets = string(encoded)
	}
	if err := adminDB.Exec(
		"INSERT INTO announcement(title,content,scope,target_ids,status,start_at,created_by) VALUES(?,?,?,?,'published',?,1)",
		title, "公告正文", scope, targets, time.Now().UTC().Add(-time.Hour),
	).Error; err != nil {
		t.Fatal(err)
	}
	var id uint64
	adminDB.Table("announcement").Where("title = ?", title).Pluck("id", &id)
	return id
}

// createStationDevice 写入一台归属站点的设备。
func createStationDevice(t *testing.T, adminDB *gorm.DB, deviceID string, stationID uint64) {
	t.Helper()
	if err := adminDB.Exec(
		"INSERT INTO device_meta(device_id,station_id,vendor_id,status) VALUES(?,?,1,'enabled')",
		deviceID, stationID,
	).Error; err != nil {
		t.Fatal(err)
	}
}

// TestStationDetailReturnsAnnouncementsAndDeviceStatus 验证站点详情返回站内公告与设备在线状态：
// 公告只包含全局公告和指向本站的公告；在线判定按最后心跳是否落在 1 小时内执行，
// 网关不可用时标记为状态未知而不是离线。
func TestStationDetailReturnsAnnouncementsAndDeviceStatus(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" || os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("disposable MySQL and Redis required")
	}
	userDB := openAccountDB(t, "TEST_USER_DATABASE_URL")
	adminDB := openAccountDB(t, "TEST_ADMIN_DATABASE_URL")
	userID := createUserWithWallet(t, userDB, 0)
	t.Cleanup(func() {
		userDB.Exec("DELETE FROM wallet_account WHERE user_id = ?", userID)
		userDB.Exec("DELETE FROM user WHERE id = ?", userID)
	})

	stationID := createStation(t, adminDB, "qa-detail-"+uuid.NewString()[:8], time.Now().UTC())
	otherID := createStation(t, adminDB, "qa-other-"+uuid.NewString()[:8], time.Now().UTC())
	t.Cleanup(func() { adminDB.Exec("DELETE FROM station WHERE id IN ?", []uint64{stationID, otherID}) })

	globalID := createAnnouncement(t, adminDB, "qa-global-"+uuid.NewString()[:8], "global", nil)
	mineID := createAnnouncement(t, adminDB, "qa-mine-"+uuid.NewString()[:8], "station", []string{strconv.FormatUint(stationID, 10)})
	foreignID := createAnnouncement(t, adminDB, "qa-foreign-"+uuid.NewString()[:8], "station", []string{strconv.FormatUint(otherID, 10)})
	t.Cleanup(func() { adminDB.Exec("DELETE FROM announcement WHERE id IN ?", []uint64{globalID, mineID, foreignID}) })
	online := "qa-online-" + uuid.NewString()[:8]
	offline := "qa-offline-" + uuid.NewString()[:8]
	createStationDevice(t, adminDB, online, stationID)
	createStationDevice(t, adminDB, offline, stationID)
	t.Cleanup(func() { adminDB.Exec("DELETE FROM device_meta WHERE device_id IN ?", []string{online, offline}) })

	recent := time.Now().UTC().Add(-10 * time.Minute)
	stale := time.Now().UTC().Add(-3 * time.Hour)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") != "svc" || r.URL.Path != "/api/v1/internal/device-summaries" {
			t.Errorf("unexpected gateway request: %s %s", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"items": []map[string]any{
				{"device_id": online, "last_heartbeat_at": recent},
				{"device_id": offline, "last_heartbeat_at": stale},
			},
		}})
	}))
	defer gateway.Close()

	status, body := callJSON(t, accountRouterWithGateway(t, userDB, adminDB, userID, gateway.URL),
		http.MethodGet, "/api/v1/user/station/"+strconv.FormatUint(stationID, 10), nil)
	if status != http.StatusOK {
		t.Fatalf("status %d body %v", status, body)
	}
	data, _ := body["data"].(map[string]any)

	titles := map[string]bool{}
	notices, _ := data["announcements"].([]any)
	for _, raw := range notices {
		notice, _ := raw.(map[string]any)
		titles[notice["title"].(string)] = true
	}
	if !titles[announcementTitle(t, adminDB, globalID)] || !titles[announcementTitle(t, adminDB, mineID)] {
		t.Fatalf("expected global + own station announcement, got %v", titles)
	}
	if titles[announcementTitle(t, adminDB, foreignID)] {
		t.Fatalf("announcement of another station must not be returned: %v", titles)
	}

	devices, _ := data["devices"].([]any)
	states := map[string]map[string]any{}
	for _, raw := range devices {
		device, _ := raw.(map[string]any)
		states[device["device_id"].(string)] = device
	}
	if len(states) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(states))
	}
	if onlineFlag, _ := states[online]["online"].(bool); !onlineFlag {
		t.Fatalf("device with 10-minute heartbeat must be online: %v", states[online])
	}
	if known, _ := states[online]["runtime_available"].(bool); !known {
		t.Fatalf("runtime must be marked available: %v", states[online])
	}
	if onlineFlag, _ := states[offline]["online"].(bool); onlineFlag {
		t.Fatalf("device with 3-hour heartbeat must be offline: %v", states[offline])
	}
}

// TestStationDetailMarksDeviceUnknownWhenGatewayDown 验证网关不可用时设备状态为未知，
// 而不是把查询失败伪装成离线。
func TestStationDetailMarksDeviceUnknownWhenGatewayDown(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" || os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("disposable MySQL and Redis required")
	}
	userDB := openAccountDB(t, "TEST_USER_DATABASE_URL")
	adminDB := openAccountDB(t, "TEST_ADMIN_DATABASE_URL")
	userID := createUserWithWallet(t, userDB, 0)
	t.Cleanup(func() {
		userDB.Exec("DELETE FROM wallet_account WHERE user_id = ?", userID)
		userDB.Exec("DELETE FROM user WHERE id = ?", userID)
	})
	stationID := createStation(t, adminDB, "qa-down-"+uuid.NewString()[:8], time.Now().UTC())
	t.Cleanup(func() { adminDB.Exec("DELETE FROM station WHERE id = ?", stationID) })
	device := "qa-down-dev-" + uuid.NewString()[:8]
	createStationDevice(t, adminDB, device, stationID)
	t.Cleanup(func() { adminDB.Exec("DELETE FROM device_meta WHERE device_id = ?", device) })

	// 指向一个没有监听的地址，模拟网关不可用。
	router := accountRouterWithGateway(t, userDB, adminDB, userID, "http://127.0.0.1:1")
	status, body := callJSON(t, router, http.MethodGet, "/api/v1/user/station/"+strconv.FormatUint(stationID, 10), nil)
	if status != http.StatusOK {
		t.Fatalf("status %d body %v", status, body)
	}
	data, _ := body["data"].(map[string]any)
	devices, _ := data["devices"].([]any)
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	row, _ := devices[0].(map[string]any)
	if known, _ := row["runtime_available"].(bool); known {
		t.Fatalf("runtime must be unknown when gateway is down: %v", row)
	}
	if onlineFlag, _ := row["online"].(bool); onlineFlag {
		t.Fatalf("device must not be reported online without runtime data: %v", row)
	}
}

// announcementTitle 读回公告标题，供断言按 id 引用，避免在测试里重复保存随机标题。
func announcementTitle(t *testing.T, adminDB *gorm.DB, id uint64) string {
	t.Helper()
	var title string
	if err := adminDB.Table("announcement").Where("id = ?", id).Pluck("title", &title).Error; err != nil {
		t.Fatal(err)
	}
	return title
}

// TestNearbyStationsWithoutCoordinatesReturnsLatest 验证未开启定位时按创建时间倒序
// 返回最近创建的站点，默认 10 条；distance_km 为 null 而不是 0，
// 避免把未知距离伪装成同址。
func TestNearbyStationsWithoutCoordinatesReturnsLatest(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" || os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("disposable MySQL and Redis required")
	}
	userDB := openAccountDB(t, "TEST_USER_DATABASE_URL")
	adminDB := openAccountDB(t, "TEST_ADMIN_DATABASE_URL")
	userID := createUserWithWallet(t, userDB, 0)
	t.Cleanup(func() {
		userDB.Exec("DELETE FROM wallet_account WHERE user_id = ?", userID)
		userDB.Exec("DELETE FROM user WHERE id = ?", userID)
	})
	router := accountRouter(t, userDB, adminDB, userID)

	// 本包其它测试也会写入站点，因此把创建时间放到未来，
	// 使这些站点稳定占据倒序结果的前列，断言不依赖用例执行顺序。
	base := time.Now().UTC().Add(time.Hour)
	const total = 12
	ids := make([]uint64, 0, total)
	for i := range total {
		ids = append(ids, createStation(t, adminDB, "qa-future-"+uuid.NewString()[:8], base.Add(time.Duration(i)*time.Minute)))
	}
	t.Cleanup(func() { adminDB.Exec("DELETE FROM station WHERE id IN ?", ids) })

	status, body := callJSON(t, router, http.MethodGet, "/api/v1/user/station/nearby", nil)
	if status != http.StatusOK {
		t.Fatalf("status %d body %v", status, body)
	}
	data, _ := body["data"].(map[string]any)
	if located, _ := data["located"].(bool); located {
		t.Fatalf("expected located=false, got %v", data["located"])
	}
	if size, _ := data["page_size"].(float64); int(size) != 10 {
		t.Fatalf("expected default page_size 10, got %v", data["page_size"])
	}
	items, _ := data["items"].([]any)
	if len(items) != 10 {
		t.Fatalf("expected 10 stations, got %d", len(items))
	}
	for i, raw := range items {
		item, _ := raw.(map[string]any)
		distance, present := item["distance_km"]
		if !present || distance != nil {
			t.Fatalf("item %d: distance_km must be null when location is off, got %v", i, distance)
		}
		id := uint64(item["id"].(float64))
		if want := ids[total-1-i]; id != want {
			t.Fatalf("item %d: got station %d, want newest-first %d", i, id, want)
		}
	}
}

// TestNearbyStationsRejectsPartialCoordinates 验证只提供一个坐标或坐标非法仍是请求错误，
// 避免把残缺坐标当作“未开启定位”静默降级。
func TestNearbyStationsRejectsPartialCoordinates(t *testing.T) {
	if os.Getenv("TEST_USER_DATABASE_URL") == "" || os.Getenv("TEST_ADMIN_DATABASE_URL") == "" || os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("disposable MySQL and Redis required")
	}
	userDB := openAccountDB(t, "TEST_USER_DATABASE_URL")
	adminDB := openAccountDB(t, "TEST_ADMIN_DATABASE_URL")
	userID := createUserWithWallet(t, userDB, 0)
	t.Cleanup(func() {
		userDB.Exec("DELETE FROM wallet_account WHERE user_id = ?", userID)
		userDB.Exec("DELETE FROM user WHERE id = ?", userID)
	})
	router := accountRouter(t, userDB, adminDB, userID)

	for _, path := range []string{
		"/api/v1/user/station/nearby?latitude=25.8",
		"/api/v1/user/station/nearby?longitude=114.9",
		"/api/v1/user/station/nearby?latitude=25.8&longitude=999",
		"/api/v1/user/station/nearby?latitude=not-a-number&longitude=114.9",
	} {
		status, body := callJSON(t, router, http.MethodGet, path, nil)
		if status != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d body %v", path, status, body)
		}
	}
}
