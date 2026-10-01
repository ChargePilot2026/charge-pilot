package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/auth"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func TestSelfMFASettingsUsesPendingProofAndRevokesSessionsSafely(t *testing.T) {
	if os.Getenv("TEST_ADMIN_DATABASE_URL") == "" || os.Getenv("TEST_REDIS_URL") == "" {
		t.Skip("local MySQL and Redis required")
	}
	ctx := context.Background()
	// 并发请求使用各自的真实事务连接；只创建 UUID 隔离夹具并按标识清理。
	db := openFinanceDB(t, "TEST_ADMIN_DATABASE_URL")
	opts, err := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cache := redis.NewClient(opts)
	defer cache.Close()
	tag := strings.ReplaceAll(uuid.NewString(), "-", "")
	role := struct {
		ID         uint64
		Code, Name string
	}{Code: "self_mfa_" + tag, Name: "本人安全自定义角色"}
	var actor Account
	t.Cleanup(func() {
		if actor.ID != 0 {
			if err := db.Table("audit_log").Where("actor_id=? AND actor_name=?", actor.ID, actor.Username).Delete(map[string]any{}).Error; err != nil {
				t.Errorf("cleanup private MFA audits: %v", err)
			}
			if err := db.Where("id=? AND username=?", actor.ID, actor.Username).Delete(&Account{}).Error; err != nil {
				t.Errorf("cleanup private MFA account: %v", err)
			}
		}
		if role.ID != 0 {
			if err := db.Table("role").Where("id=? AND code=?", role.ID, role.Code).Delete(map[string]any{}).Error; err != nil {
				t.Errorf("cleanup private MFA role: %v", err)
			}
		}
	})
	if err := db.Table("role").Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	const password = "Fixture-password-2026"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	actor = Account{Username: "self-mfa-" + tag, DisplayName: sql.NullString{String: "本人安全测试账号", Valid: true}, PasswordHash: string(hash), RoleID: role.ID, Status: "active", AuthVersion: 1}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	j, err := auth.NewJWT(strings.Repeat("m", 32))
	if err != nil {
		t.Fatal(err)
	}
	api := API{Store: Store{DB: db}, Sessions: Sessions{Redis: cache}, JWT: j}
	var sessionIDs []string
	defer func() {
		keys := []string{mfaPendingKey(actor.ID), mfaLockKey(actor.ID), mfaSettingsRateKey(actor.ID)}
		for _, sid := range sessionIDs {
			keys = append(keys, sessionKey(sid))
		}
		if err := cache.Del(ctx, keys...).Err(); err != nil {
			t.Errorf("MFA Redis fixture cleanup: %v", err)
		}
	}()
	newSession := func() string {
		t.Helper()
		var current Account
		if err := db.Where("id=?", actor.ID).Take(&current).Error; err != nil {
			t.Fatal(err)
		}
		sid, _, err := api.Sessions.Create(ctx, current)
		if err != nil {
			t.Fatal(err)
		}
		sessionIDs = append(sessionIDs, sid)
		now := time.Now()
		token, err := j.Sign(auth.Claims{Subject: fmt.Sprint(actor.ID), Kind: "admin", SessionID: sid, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	token := newSession()
	router := httpapi.NewRouter()
	api.Register(router)
	request := func(token string, input any, resetRate bool) *httptest.ResponseRecorder {
		t.Helper()
		if resetRate {
			if err := cache.Del(ctx, mfaSettingsRateKey(actor.ID)).Err(); err != nil {
				t.Fatal(err)
			}
		}
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/mfa-settings", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		reply := httptest.NewRecorder()
		router.ServeHTTP(reply, req)
		return reply
	}
	checkState := func(enabled bool, version uint64, secret string) {
		t.Helper()
		var row Account
		if err := db.Where("id=?", actor.ID).Take(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.MFAEnabled != enabled || row.AuthVersion != version || (secret == "" && row.MFASecret != nil) || (secret != "" && (row.MFASecret == nil || *row.MFASecret != secret)) {
			t.Fatalf("MFA state changed unexpectedly: enabled=%v version=%d", row.MFAEnabled, row.AuthVersion)
		}
	}
	expect := func(reply *httptest.ResponseRecorder, want int) {
		t.Helper()
		if reply.Code != want {
			t.Fatalf("MFA response=%d want=%d", reply.Code, want)
		}
	}
	enrol := func(token string) mfaSettingsResult {
		t.Helper()
		reply := request(token, map[string]any{"action": "enrol", "password": password}, true)
		expect(reply, 200)
		var env struct {
			Data mfaSettingsResult `json:"data"`
		}
		if err := json.Unmarshal(reply.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		var raw struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(reply.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		if len(env.Data.EnrollmentID) != 43 || env.Data.Secret == "" || env.Data.OTPAuthURI == "" || env.Data.ExpiresIn != 300 || env.Data.MFAEnabled || env.Data.SessionsRevoked || raw.Data["code"] != nil {
			t.Fatal("enrollment contract omitted pending proof or returned a current code")
		}
		checkState(false, 1, "")
		return env.Data
	}
	confirm := func(id, code string) map[string]any {
		return map[string]any{"action": "confirm", "enrollment_id": id, "code": code}
	}
	code := func(secret string) string {
		t.Helper()
		v, err := TOTPCode(secret, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	badCode := func(secret string) string {
		t.Helper()
		for _, v := range []string{"000000", "111111", "222222", "333333"} {
			if VerifyTOTP(secret, v, time.Now().UTC()) != nil {
				return v
			}
		}
		t.Fatal("no invalid fixture code")
		return ""
	}

	// A role with no administrator management permission can manage only its own MFA.
	profile, err := api.Store.Profile(ctx, actor.ID)
	if err != nil || len(profile.Permissions) != 0 || profile.MFAEnabled || profile.RoleName != role.Name {
		t.Fatalf("fixture identity: %v", err)
	}
	expect(request(token, map[string]any{"action": "enrol", "password": "wrong-password"}, true), 400)
	if cache.Exists(ctx, mfaPendingKey(actor.ID)).Val() != 0 {
		t.Fatal("wrong password created pending enrollment")
	}
	first := enrol(token)
	expect(request(token, confirm(first.EnrollmentID, badCode(first.Secret)), true), 400)
	checkState(false, 1, "")
	if cache.Exists(ctx, mfaPendingKey(actor.ID)).Val() != 1 {
		t.Fatal("bad code consumed pending proof")
	}
	otherSession := newSession()
	expect(request(otherSession, confirm(first.EnrollmentID, code(first.Secret)), true), 409)
	second := enrol(token)
	if second.EnrollmentID == first.EnrollmentID || second.Secret == first.Secret {
		t.Fatal("new enrollment reused prior proof")
	}
	expect(request(token, confirm(first.EnrollmentID, code(first.Secret)), true), 409)
	raw, err := cache.Get(ctx, mfaPendingKey(actor.ID)).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var pending mfaEnrollment
	if json.Unmarshal(raw, &pending) != nil {
		t.Fatal("pending fixture corrupt")
	}
	pending.ExpiresAt = time.Now().UTC().Add(-time.Second)
	raw, _ = json.Marshal(pending)
	if err := cache.Set(ctx, mfaPendingKey(actor.ID), raw, mfaEnrollmentTTL).Err(); err != nil {
		t.Fatal(err)
	}
	expect(request(token, confirm(second.EnrollmentID, code(second.Secret)), true), 409)
	third := enrol(token)
	// 缓存故障不能启用账号，也不能消费另一连接里尚有效的登记。
	raw, err = cache.Get(ctx, mfaPendingKey(actor.ID)).Bytes()
	if err != nil || json.Unmarshal(raw, &pending) != nil {
		t.Fatalf("pending proof before dependency failure: %v", err)
	}
	closedCache := redis.NewClient(opts)
	if err := closedCache.Close(); err != nil {
		t.Fatal(err)
	}
	brokenAPI := API{Store: Store{DB: db}, Sessions: Sessions{Redis: closedCache}}
	if _, err := brokenAPI.applyMFASettings(ctx, profile, pending.SessionID, mfaSettingsInput{Action: "confirm", EnrollmentID: third.EnrollmentID, Code: code(third.Secret)}, "127.0.0.1"); err == nil {
		t.Fatal("cache failure unexpectedly enabled MFA")
	}
	checkState(false, 1, "")
	if cache.Exists(ctx, mfaPendingKey(actor.ID)).Val() != 1 {
		t.Fatal("failed dependency consumed pending proof")
	}
	// 审计和凭证写入必须同事务；审计失败时密钥和版本一起回滚。
	callbackName := "fixture:mfa-audit-failure-" + tag
	callbackRegistered := true
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_log" {
			tx.AddError(errors.New("fixture audit unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if callbackRegistered {
			_ = db.Callback().Create().Remove(callbackName)
		}
	}()
	expect(request(token, confirm(third.EnrollmentID, code(third.Secret)), true), 503)
	checkState(false, 1, "")
	if err := db.Callback().Create().Remove(callbackName); err != nil {
		t.Fatal(err)
	}
	callbackRegistered = false
	third = enrol(token)
	if err := cache.Set(ctx, mfaLockKey(actor.ID), "fixture-other-operation", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	expect(request(token, map[string]any{"action": "enrol", "password": password}, true), 409)
	if err := cache.Del(ctx, mfaLockKey(actor.ID)).Err(); err != nil {
		t.Fatal(err)
	}
	if value := cache.Get(ctx, mfaPendingKey(actor.ID)).Val(); !strings.Contains(value, third.EnrollmentID) {
		t.Fatal("concurrent registration replaced protected pending proof")
	}
	if err := cache.Del(ctx, mfaSettingsRateKey(actor.ID)).Err(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- request(token, confirm(third.EnrollmentID, code(third.Secret)), false)
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for reply := range results {
		if reply.Code == 200 {
			success++
			var env struct {
				Data mfaSettingsResult `json:"data"`
			}
			if err := json.Unmarshal(reply.Body.Bytes(), &env); err != nil || !env.Data.MFAEnabled || !env.Data.SessionsRevoked || env.Data.Secret != "" || env.Data.EnrollmentID != "" {
				t.Fatalf("confirm response omitted revocation or exposed a secret: %v", err)
			}
		} else if reply.Code != 409 && reply.Code != 401 {
			t.Fatalf("unexpected concurrent confirmation status: %d", reply.Code)
		}
	}
	if success != 1 {
		t.Fatalf("pending proof reused: successes=%d", success)
	}
	checkState(true, 2, third.Secret)
	profile, err = api.Store.Profile(ctx, actor.ID)
	if err != nil || !profile.MFAEnabled || profile.RoleName != role.Name {
		t.Fatalf("enabled MFA not reflected in live profile: %v", err)
	}
	if cache.Exists(ctx, mfaPendingKey(actor.ID)).Val() != 0 {
		t.Fatal("successful confirm left reusable pending proof")
	}
	expect(request(token, confirm(third.EnrollmentID, code(third.Secret)), true), 401)
	token = newSession()
	expect(request(token, map[string]any{"action": "enrol", "password": password}, true), 409)
	checkState(true, 2, third.Secret)
	expect(request(token, map[string]any{"action": "disable", "password": "wrong-password", "code": code(third.Secret)}, true), 400)
	expect(request(token, map[string]any{"action": "disable", "password": password, "code": badCode(third.Secret)}, true), 400)
	checkState(true, 2, third.Secret)
	disabled := request(token, map[string]any{"action": "disable", "password": password, "code": code(third.Secret)}, true)
	expect(disabled, 200)
	var disabledEnv struct {
		Data mfaSettingsResult `json:"data"`
	}
	if err := json.Unmarshal(disabled.Body.Bytes(), &disabledEnv); err != nil || disabledEnv.Data.MFAEnabled || !disabledEnv.Data.SessionsRevoked {
		t.Fatalf("disable response omitted state or revocation: %v", err)
	}
	checkState(false, 3, "")
	expect(request(token, map[string]any{"action": "enrol", "password": password}, true), 401)
	var audits int64
	if err := db.Table("audit_log").Where("actor_id=? AND action IN ('mfa_enable','mfa_disable')", actor.ID).Count(&audits).Error; err != nil || audits != 2 {
		t.Fatalf("security changes missing safe transactional audit: count=%d err=%v", audits, err)
	}
	var sensitive int64
	if err := db.Table("audit_log").Where("actor_id=? AND (before_json IS NOT NULL OR after_json IS NOT NULL)", actor.ID).Count(&sensitive).Error; err != nil || sensitive != 0 {
		t.Fatalf("MFA audit persisted a secret snapshot: count=%d err=%v", sensitive, err)
	}
	token = newSession()
	for i := 0; i < 6; i++ {
		want := 400
		if i == 5 {
			want = 429
		}
		expect(request(token, map[string]any{"action": "enrol", "password": "wrong-password"}, i == 0), want)
	}
	checkState(false, 3, "")
}
