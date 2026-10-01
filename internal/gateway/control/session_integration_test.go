package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// sessionTestDB 是最后构建出的 session router 所用的连接句柄，
// 这样测试就能通过和 handler 同一条连接来准备数据并校验结果。
var sessionTestDB *gorm.DB

func sessionRouter(t *testing.T, api SessionAPI) *gin.Engine {
	t.Helper()
	url := os.Getenv("TEST_GATEWAY_DATABASE_URL")
	if url == "" {
		t.Skip("set a disposable gateway database URL")
	}
	db, err := dbconn.Open(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	sessionTestDB = orm
	api.DB = orm
	gin.SetMode(gin.TestMode)
	router := gin.New()
	api.Register(router)
	return router
}

// decodeCleanup 从统一响应信封的 data 字段解析清理计数。
func decodeCleanup(t *testing.T, raw []byte) struct {
	Closed int64 `json:"closed"`
} {
	t.Helper()
	var envelope struct {
		Data struct {
			Closed int64 `json:"closed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return envelope.Data
}

// shortID 让准备进去的 session id 落在 VARCHAR(64) 这一列的范围内。
func shortID(t *testing.T) string {
	t.Helper()
	seed := t.Name() + time.Now().Format("150405.000000")
	if len(seed) > 24 {
		seed = seed[len(seed)-24:]
	}
	return seed
}

func callSession(t *testing.T, router *gin.Engine, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("X-Service-Token", token)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

const sessionToken = "session-audit-test-token"

// seedOpenSession 创建未结束且已超过静默阈值的遗留会话夹具。
func seedOpenSession(t *testing.T, orm *gorm.DB, sessionID, deviceID string, idleFor time.Duration) time.Time {
	t.Helper()
	started := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	last := time.Now().UTC().Add(-idleFor)
	month := started.Format("2006-01-02")
	if err := orm.Table("device_session").Create(map[string]any{
		"session_id": sessionID, "device_id": deviceID, "protocol": "tcp",
		"remote_addr": "10.0.0.7:51000", "started_at": started, "last_active_at": last,
		"bytes_in": 128, "bytes_out": 32, "frames_in": 4, "frames_out": 2,
		"created_month": month,
	}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { orm.Exec("DELETE FROM device_session WHERE session_id = ?", sessionID) })
	return started
}

func TestSessionListIdleRequiresServiceToken(t *testing.T) {
	router := sessionRouter(t, SessionAPI{ServiceToken: sessionToken, IdleThreshold: time.Minute})
	for _, token := range []string{"", "wrong-token"} {
		if rec := callSession(t, router, http.MethodGet, "/api/v1/internal/device-sessions", token); rec.Code != http.StatusUnauthorized {
			t.Fatalf("token %q got %d, want 401", token, rec.Code)
		}
	}
	if rec := callSession(t, router, http.MethodPost, "/api/v1/internal/device-sessions/cleanup-idle", "wrong-token"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("cleanup with a bad token got %d, want 401", rec.Code)
	}
}

func TestCleanupIdleClosesAbandonedSessionsOnly(t *testing.T) {
	router := sessionRouter(t, SessionAPI{ServiceToken: sessionToken, IdleThreshold: 10 * time.Minute})
	abandoned := "sess-ab-" + shortID(t)
	active := "sess-ac-" + shortID(t)
	closed := "sess-cl-" + shortID(t)

	db := sessionTestDB
	seedOpenSession(t, db, abandoned, "QA-SESSION-CLEAN", time.Hour)
	seedOpenSession(t, db, active, "QA-SESSION-ACTIVE", time.Minute)

	// 一条已经正常关闭的会话，无论多老都绝不能被碰到：
	// 它的计数器是最终值，重新盖一遍就等于改写历史。
	started := time.Now().UTC().Add(-5 * time.Hour).Truncate(time.Millisecond)
	if err := db.Table("device_session").Create(map[string]any{
		"session_id": closed, "device_id": "QA-SESSION-CLOSED", "protocol": "tcp",
		"remote_addr": "10.0.0.8:52000", "started_at": started,
		"last_active_at": started.Add(time.Minute), "ended_at": started.Add(2 * time.Minute),
		"close_reason": "device_closed", "bytes_in": 10, "frames_in": 1,
		"created_month": started.Format("2006-01-02"),
	}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM device_session WHERE session_id = ?", closed) })

	rec := callSession(t, router, http.MethodPost, "/api/v1/internal/device-sessions/cleanup-idle", sessionToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("cleanup got %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeCleanup(t, rec.Body.Bytes())
	if body.Closed != 1 {
		t.Fatalf("closed %d sessions, want exactly the abandoned one", body.Closed)
	}

	var row struct {
		EndedAt     *time.Time `gorm:"column:ended_at"`
		CloseReason string     `gorm:"column:close_reason"`
		BytesIn     int64      `gorm:"column:bytes_in"`
	}
	if err := db.Table("device_session").Where("session_id = ?", abandoned).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.EndedAt == nil || row.CloseReason != "abandoned" {
		t.Fatalf("abandoned session not closed: ended=%v reason=%q", row.EndedAt, row.CloseReason)
	}
	if row.BytesIn != 128 {
		t.Fatalf("reaping changed the traffic counters: bytes_in=%d", row.BytesIn)
	}

	var stillOpen int64
	db.Table("device_session").Where("session_id = ? AND ended_at IS NULL", active).Count(&stillOpen)
	if stillOpen != 1 {
		t.Fatal("a session that was heard from recently was reaped")
	}
	var graceful struct {
		EndedAt     *time.Time `gorm:"column:ended_at"`
		CloseReason string     `gorm:"column:close_reason"`
	}
	if err := db.Table("device_session").Where("session_id = ?", closed).Take(&graceful).Error; err != nil {
		t.Fatal(err)
	}
	if graceful.EndedAt == nil {
		t.Fatal("an already closed session lost its terminal state")
	}
	if graceful.CloseReason != "device_closed" {
		t.Fatalf("a closed session was re-stamped as %q; its real reason must survive", graceful.CloseReason)
	}
}

// 验证重复清理不再次更新已关闭会话，也不重复累计回收数量。
func TestCleanupIdleIsRepeatable(t *testing.T) {
	router := sessionRouter(t, SessionAPI{ServiceToken: sessionToken, IdleThreshold: 10 * time.Minute})
	db := sessionTestDB
	seedOpenSession(t, db, "sess-rp-"+shortID(t), "QA-SESSION-REPEAT", time.Hour)

	for i := range 2 {
		rec := callSession(t, router, http.MethodPost, "/api/v1/internal/device-sessions/cleanup-idle", sessionToken)
		if rec.Code != http.StatusOK {
			t.Fatalf("run %d got %d: %s", i+1, rec.Code, rec.Body.String())
		}
		body := decodeCleanup(t, rec.Body.Bytes())
		want := int64(1)
		if i == 1 {
			want = 0
		}
		if body.Closed != want {
			t.Fatalf("run %d closed %d, want %d", i+1, body.Closed, want)
		}
	}
}

func TestListIdleReportsAbandonedSessions(t *testing.T) {
	router := sessionRouter(t, SessionAPI{ServiceToken: sessionToken, IdleThreshold: 10 * time.Minute})
	db := sessionTestDB
	sessionID := "sess-ls-" + shortID(t)
	seedOpenSession(t, db, sessionID, "QA-SESSION-LIST", time.Hour)

	rec := callSession(t, router, http.MethodGet, "/api/v1/internal/device-sessions", sessionToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("list got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "QA-SESSION-LIST") {
		t.Fatalf("abandoned session missing from the report: %s", rec.Body.String())
	}
}
