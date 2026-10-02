package control

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
)

// 校验批量取证端点的令牌与请求体边界；同时覆盖 TelemetryAPI 全量路由
// 注册（gin 静态段与 :device_id 通配冲突会在此 panic）。
func TestChargingEvidenceRequiresServiceTokenAndValidBounds(t *testing.T) {
	r := httpapi.NewRouter()
	(TelemetryAPI{ServiceToken: "service"}).Register(r)
	post := func(token, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/v1/internal/charging-evidence", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("X-Service-Token", token)
		}
		r.ServeHTTP(w, req)
		return w
	}
	started := `2026-10-01T00:00:00Z`
	if w := post("", `{"requests":[{"device_id":"d1","port_no":1,"started_at":"`+started+`"}]}`); w.Code != 401 {
		t.Fatalf("missing token status=%d", w.Code)
	}
	if w := post("wrong", `{"requests":[{"device_id":"d1","port_no":1,"started_at":"`+started+`"}]}`); w.Code != 401 {
		t.Fatalf("wrong token status=%d", w.Code)
	}
	for name, body := range map[string]string{
		"empty body":       `{}`,
		"over batch limit": `{"requests":[` + strings.Repeat(`{"device_id":"d1","port_no":1,"started_at":"`+started+`"},`, 21) + `{"device_id":"d1","port_no":1,"started_at":"` + started + `"}]}`,
		"zero port":        `{"requests":[{"device_id":"d1","port_no":0,"started_at":"` + started + `"}]}`,
		"empty device":     `{"requests":[{"device_id":"","port_no":1,"started_at":"` + started + `"}]}`,
		"long device":      `{"requests":[{"device_id":"` + strings.Repeat("a", 65) + `","port_no":1,"started_at":"` + started + `"}]}`,
		"port overflow":    `{"requests":[{"device_id":"d1","port_no":256,"started_at":"` + started + `"}]}`,
		"zero time":        `{"requests":[{"device_id":"d1","port_no":1,"started_at":"0001-01-01T00:00:00Z"}]}`,
		"future time":      `{"requests":[{"device_id":"d1","port_no":1,"started_at":"2999-01-01T00:00:00Z"}]}`,
		"stale window":     `{"requests":[{"device_id":"d1","port_no":1,"started_at":"2020-01-01T00:00:00Z"}]}`,
		"malformed json":   `{"requests":[`,
	} {
		t.Run(name, func(t *testing.T) {
			if w := post("service", body); w.Code != 400 {
				t.Fatalf("status=%d want=400 body=%s", w.Code, w.Body.String())
			}
		})
	}
}

// GET 端点的 after_id 游标参数校验。
func TestChargingSamplesRejectsInvalidCursor(t *testing.T) {
	r := httpapi.NewRouter()
	(TelemetryAPI{ServiceToken: "service"}).Register(r)
	for _, query := range []string{"?after_id=-1", "?after_id=abc", "?after_id=18446744073709551616"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/internal/devices/d1/charging-samples?port_no=1&started_at=2026-10-01T00:00:00Z"+query, nil)
		req.Header.Set("X-Service-Token", "service")
		r.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("query=%s status=%d want=400", query, w.Code)
		}
	}
}
