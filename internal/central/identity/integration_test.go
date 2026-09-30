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
	openid := "go-test-login-identity-20260928"
	_, _ = db.ExecContext(ctx, "DELETE FROM wallet_account WHERE user_id IN (SELECT id FROM user WHERE openid = ?)", openid)
	_, _ = db.ExecContext(ctx, "DELETE FROM user WHERE openid = ?", openid)
	_, _ = db.ExecContext(ctx, "DELETE FROM user_login_identity WHERE openid = ?", []byte(openid))
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
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM wallet_account WHERE user_id = ?", users[0].ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("wallet count=%d err=%v", count, err)
	}
	profile, err := store.Profile(ctx, users[0].ID)
	if err != nil || profile.UserID != users[0].ID || profile.Wallet.AvailableCents != 0 || profile.MembershipCard != nil {
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
	if err := sessions.Revoke(ctx, original); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sessions.Rotate(ctx, next); !errors.Is(err, ErrInvalidRefresh) {
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
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/user/profile", nil)
	request.Header.Set("Authorization", "Bearer "+login.Data.JWT)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("profile: %d %s", response.Code, response.Body.String())
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

func uuidPart(t *testing.T) string {
	t.Helper()
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}
