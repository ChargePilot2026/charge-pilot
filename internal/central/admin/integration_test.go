package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestAdminLoginLifecycle(t *testing.T) {
	url, redisURL := os.Getenv("TEST_ADMIN_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if url == "" || redisURL == "" {
		t.Skip("set disposable MySQL and Redis URLs")
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
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	cache := redis.NewClient(opts)
	defer cache.Close()
	store := Store{DB: orm}
	const username = "integration-admin"
	const password = "Local-test-password-2026"
	defer db.Exec("DELETE FROM audit_log WHERE actor_name = ?", username)
	defer db.Exec("DELETE FROM admin_user_role WHERE username = ?", username)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- store.Bootstrap(ctx, username, password) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Bootstrap(ctx, username, "Not-a-password-reset"); err != nil {
		t.Fatal(err)
	}
	// Bootstrap 只负责初始化一套全新安装，绝不重置已有密码，
	// 所以对着一个已经有账号的库，它会正确地拒绝建号。
	// 下面要验的是完整的登录生命周期，
	// 于是这里直接按"全新安装之后本该留下的样子"把这个账号准备好，
	// 而不是让 bootstrap 再做一遍——
	// 共享的开发库并不空，
	// 悄悄依赖它为空，正是这个测试曾经因为
	// 跟登录毫无关系的原因失败的原因。
	var created int
	if err := db.QueryRow("SELECT COUNT(*) FROM admin_user_role WHERE username = ?", username).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created == 0 {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			t.Fatal(err)
		}
		var role struct{ ID uint64 }
		if err := orm.Table("role").Where("code = 'customer_admin' AND deleted_at IS NULL").Take(&role).Error; err != nil {
			t.Fatal(err)
		}
		if err := orm.Transaction(func(tx *gorm.DB) error {
			return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&Account{Username: username, PasswordHash: string(hash), RoleID: role.ID, Status: "active"}).Error
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 而且这条规则本身值得写出来，而不是只留一个暗示：
	// 在一套不是全新的安装上，bootstrap 必须对已有的东西原样不动。
	if err := store.Bootstrap(ctx, "bootstrap-must-not-appear", "Never-created-2026"); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DELETE FROM admin_user_role WHERE username = ?", "bootstrap-must-not-appear")
	var intruders int
	if err := db.QueryRow("SELECT COUNT(*) FROM admin_user_role WHERE username = ?", "bootstrap-must-not-appear").Scan(&intruders); err != nil {
		t.Fatal(err)
	}
	if intruders != 0 {
		t.Fatal("bootstrap created an account on an installation that is not fresh")
	}
	a, err := store.Login(ctx, username, password, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM admin_user_role WHERE username = ?", username).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	jwt, _ := auth.NewJWT(strings.Repeat("x", 32))
	api := API{Store: store, Sessions: Sessions{Redis: cache}, JWT: jwt}
	router := httpapi.NewRouter()
	api.Register(router)
	router.GET("/protected", api.Require("dashboard.read"), func(c *gin.Context) { c.Status(200) })
	router.GET("/forbidden", api.Require("nonexistent.permission"), func(c *gin.Context) { c.Status(200) })
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.RemoteAddr = "192.0.2.42:1234"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	defer cache.Del(ctx, "admin:login-rate:192.0.2.42")
	rec := request("POST", "/api/v1/admin/auth/login", `{"username":"integration-admin","password":"Local-test-password-2026"}`, "")
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			Token   string `json:"token"`
			Refresh string `json:"refresh_token"`
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if request("GET", "/protected", "", env.Data.Token).Code != 200 {
		t.Fatal("permission check failed")
	}
	if request("GET", "/forbidden", "", env.Data.Token).Code != 403 {
		t.Fatal("missing permission accepted")
	}
	oldRefresh := env.Data.Refresh
	rec = request("POST", "/api/v1/admin/auth/refresh", `{"refresh_token":"`+oldRefresh+`"}`, "")
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	json.Unmarshal(rec.Body.Bytes(), &env)
	if request("POST", "/api/v1/admin/auth/refresh", `{"refresh_token":"`+oldRefresh+`"}`, "").Code != 401 {
		t.Fatal("old refresh accepted")
	}
	if request("POST", "/api/v1/admin/auth/logout", `{}`, env.Data.Token).Code != 200 {
		t.Fatal("logout failed")
	}
	if request("GET", "/api/v1/admin/auth/me", "", env.Data.Token).Code != 401 {
		t.Fatal("revoked token accepted")
	}
	for i := 1; i <= 5; i++ {
		_, err := store.Login(ctx, username, "incorrect", "127.0.0.1")
		if i < 5 && !errors.Is(err, ErrCredentials) || i == 5 && !errors.Is(err, ErrLocked) {
			t.Fatal(i, err)
		}
	}
	if _, err := store.Login(ctx, username, password, "127.0.0.1"); !errors.Is(err, ErrLocked) {
		t.Fatal("lock bypass", err)
	}
	if err := orm.Model(&Account{}).Where("id = ?", a.ID).Update("locked_until", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.Login(ctx, username, password, "127.0.0.1"); err != nil {
		t.Fatal("lock recovery", err)
	}
	sid, rt, err := api.Sessions.Create(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ChangePassword(ctx, a.ID, password, "New-local-password-2026", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	profile, err := store.Profile(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if matches, err := api.Sessions.Matches(ctx, sid, a.ID, profile.AuthVersion); err != nil || matches {
		t.Fatal("old session survived password change", err)
	}
	_ = api.Sessions.Revoke(ctx, rt)
	if _, err := store.Login(ctx, username, password, "127.0.0.1"); !errors.Is(err, ErrCredentials) {
		t.Fatal("old password accepted", err)
	}
	if _, err := store.Login(ctx, username, "New-local-password-2026", "127.0.0.1"); err != nil {
		t.Fatal("new password failed", err)
	}
	if err := orm.Model(&Account{}).Where("id = ?", a.ID).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.Login(ctx, username, password, "127.0.0.1"); !errors.Is(err, ErrCredentials) {
		t.Fatal("disabled accepted", err)
	}
}
