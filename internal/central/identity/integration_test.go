package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/redis/go-redis/v9"
)

type fakeExchange struct{ openID string }

func (f fakeExchange) Exchange(context.Context, string) (WeChatIdentity, error) {
	return WeChatIdentity{OpenID: f.openID}, nil
}

// 只在调用方显式指定的、用完即弃的库上运行。
func TestLoginAndRefreshIntegration(t *testing.T) {
	url := os.Getenv("TEST_USER_DATABASE_URL")
	redisURL := os.Getenv("TEST_REDIS_URL")
	if url == "" || redisURL == "" {
		t.Skip("set disposable test database and Redis URLs")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	userORM, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	cache := redis.NewClient(options)
	defer cache.Close()
	store := UserStore{DB: userORM}
	openid := "go-test-login-" + uuidPart(t)
	defer db.ExecContext(ctx, "DELETE FROM wallet_account WHERE user_id IN (SELECT id FROM user WHERE openid = ?)", openid)
	defer db.ExecContext(ctx, "DELETE FROM user WHERE openid = ?", openid)
	defer db.ExecContext(ctx, "DELETE FROM user_login_identity WHERE openid = ?", []byte(openid))
	var wg sync.WaitGroup
	users := make([]User, 8)
	errs := make([]error, 8)
	for i := range users {
		wg.Add(1)
		go func(i int) { defer wg.Done(); users[i], errs[i] = store.Login(ctx, openid, "") }(i)
	}
	wg.Wait()
	newCount := 0
	for i, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
		if users[i].ID != users[0].ID {
			t.Fatal("duplicate user")
		}
		if users[i].IsNew {
			newCount++
		}
	}
	if newCount != 1 {
		t.Fatalf("created %d users", newCount)
	}
	if users[0].ID <= (1<<53)-1 || users[0].ID > (1<<63)-1 {
		t.Fatalf("new user ID is not a standard 63-bit Snowflake: %d", users[0].ID)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM wallet_account WHERE user_id = ?", users[0].ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("wallet count=%d err=%v", count, err)
	}
	profile, err := store.Profile(ctx, users[0].ID)
	if err != nil || profile.UserID != users[0].ID || profile.Wallet.AvailableCents != 0 {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
	sessions := Sessions{Redis: cache}
	sid, original, err := sessions.Create(ctx, users[0])
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Del(ctx, sessionKey(sid))
	rotated, rotatedSID, next, err := sessions.Rotate(ctx, original)
	if err != nil || rotated.ID != users[0].ID || rotatedSID != sid {
		t.Fatalf("rotate: %+v %s %v", rotated, rotatedSID, err)
	}
	if _, _, _, err := sessions.Rotate(ctx, original); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("old refresh reused: %v", err)
	}
	rotated, rotatedSID, newest, err := sessions.Rotate(ctx, next)
	if err != nil || rotated.ID != users[0].ID || rotatedSID != sid {
		t.Fatalf("second rotate lost the user ID: %+v %s %v", rotated, rotatedSID, err)
	}
	if err := sessions.Revoke(ctx, original); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sessions.Rotate(ctx, newest); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("logout did not revoke: %v", err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE user SET status = 'frozen' WHERE id = ?", users[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Login(ctx, openid, ""); !errors.Is(err, ErrUserFrozen) {
		t.Fatalf("frozen login: %v", err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE user SET status = 'active' WHERE id = ?", users[0].ID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
}

func TestAuthHTTPIntegration(t *testing.T) {
	url, redisURL := os.Getenv("TEST_USER_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if url == "" || redisURL == "" {
		t.Skip("set disposable test database and Redis URLs")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	userORM, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	cache := redis.NewClient(options)
	defer cache.Close()
	jwt, err := auth.NewJWT("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	openid := "go-test-http-" + uuidPart(t)
	defer db.ExecContext(ctx, "DELETE FROM wallet_account WHERE user_id IN (SELECT id FROM user WHERE openid = ?)", openid)
	defer db.ExecContext(ctx, "DELETE FROM user WHERE openid = ?", openid)
	defer db.ExecContext(ctx, "DELETE FROM user_login_identity WHERE openid = ?", []byte(openid))
	router := httpapi.NewRouter()
	API{WeChat: fakeExchange{openid}, Users: UserStore{DB: userORM}, Sessions: Sessions{Redis: cache}, JWT: jwt}.Register(router)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/public/auth/login", strings.NewReader(`{"code":"test-code"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("login: %d %s", response.Code, response.Body.String())
	}
	var login struct {
		Data struct {
			JWT     string `json:"jwt"`
			Refresh string `json:"refresh_token"`
			UserID  string `json:"user_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	userID, err := strconv.ParseUint(login.Data.UserID, 10, 64)
	if err != nil || userID <= (1<<53)-1 {
		t.Fatalf("login user ID must be an exact decimal Snowflake string: %q", login.Data.UserID)
	}
	claims, err := jwt.Verify(login.Data.JWT, "user", time.Now())
	if err != nil || claims.Subject != login.Data.UserID {
		t.Fatalf("JWT user ID differs from the login response: %+v %v", claims, err)
	}
	defer cache.Del(ctx, sessionKey(claims.SessionID))
	request = httptest.NewRequest(http.MethodGet, "/api/v1/user/profile", nil)
	request.Header.Set("Authorization", "Bearer "+login.Data.JWT)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("profile: %d %s", response.Code, response.Body.String())
	}
	var profile struct {
		Data struct {
			UserID string `json:"user_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &profile); err != nil || profile.Data.UserID != login.Data.UserID {
		t.Fatalf("profile user ID differs from the login response: %+v %v", profile, err)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/public/auth/refresh", nil)
	request.Header.Set("Authorization", "Bearer "+login.Data.Refresh)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("refresh: %d %s", response.Code, response.Body.String())
	}
	var refresh struct {
		Data struct {
			Refresh string `json:"refresh_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &refresh); err != nil {
		t.Fatal(err)
	}
	if refresh.Data.Refresh == login.Data.Refresh || refresh.Data.Refresh == "" {
		t.Fatal("refresh token not rotated")
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/public/auth/refresh", nil)
	request.Header.Set("Authorization", "Bearer "+refresh.Data.Refresh)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("second refresh lost the user ID: %d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/public/auth/refresh", nil)
	request.Header.Set("Authorization", "Bearer "+login.Data.Refresh)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatalf("old refresh reused: %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/public/auth/logout", nil)
	request.Header.Set("Authorization", "Bearer "+login.Data.Refresh)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("logout: %d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/user/profile", nil)
	request.Header.Set("Authorization", "Bearer "+login.Data.JWT)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatalf("revoked access token accepted: %d", response.Code)
	}
}

func TestLegacyUserKeepsIDIntegration(t *testing.T) {
	url := os.Getenv("TEST_USER_DATABASE_URL")
	if url == "" {
		t.Skip("set disposable test database URL")
	}
	ctx := context.Background()
	db, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	openid := "go-test-legacy-" + uuidPart(t)
	legacyID := uint64(time.Now().UnixNano()%1_000_000_000_000) + 1_000_000
	if _, err := db.ExecContext(ctx, "INSERT INTO user(id,openid) VALUES(?,?)", legacyID, openid); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, "DELETE FROM user WHERE openid=?", openid)
	defer db.ExecContext(ctx, "DELETE FROM user_login_identity WHERE openid=?", []byte(openid))
	store := UserStore{DB: orm}
	for range 2 {
		user, err := store.Login(ctx, openid, "")
		if err != nil || user.ID != legacyID || user.IsNew {
			t.Fatalf("legacy user was reassigned: %+v %v", user, err)
		}
	}
}

func TestLegacyNumericSessionRefreshIntegration(t *testing.T) {
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("set disposable Redis URL")
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	cache := redis.NewClient(options)
	defer cache.Close()
	ctx := context.Background()
	sessions := Sessions{Redis: cache}
	const legacyID = uint64(923456789012345678)
	sid, token, err := sessions.Create(ctx, User{ID: legacyID, OpenID: "go-test-legacy-" + uuidPart(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Del(ctx, sessionKey(sid))
	stored, err := cache.Get(ctx, sessionKey(sid)).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(stored, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["uid"] = json.RawMessage(strconv.FormatUint(legacyID, 10))
	stored, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Set(ctx, sessionKey(sid), stored, refreshTTL).Err(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		user, rotatedSID, next, err := sessions.Rotate(ctx, token)
		if err != nil || user.ID != legacyID || rotatedSID != sid {
			t.Fatalf("legacy session no longer refreshes: %+v %s %v", user, rotatedSID, err)
		}
		token = next
	}
	if err := sessions.Revoke(ctx, token); err != nil {
		t.Fatal(err)
	}
}

func uuidPart(t *testing.T) string {
	t.Helper()
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}
