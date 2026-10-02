package control

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
)

// worker-sync 全部端点要求服务令牌，未配置令牌时一律 401。
func TestWorkerSyncAPIRequiresServiceToken(t *testing.T) {
	router := httpapi.NewRouter()
	(TelemetryAPI{ServiceToken: "service"}).Register(router)
	cases := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/internal/start-results"},
		{"POST", "/api/v1/internal/start-results/mark-reported"},
		{"GET", "/api/v1/internal/end-events"},
		{"GET", "/api/v1/internal/charge-commands/by-order/1"},
		{"POST", "/api/v1/internal/device-events/mark-processed"},
		{"POST", "/api/v1/internal/ports/release"},
		{"GET", "/api/v1/internal/devices/d-1/meter-samples?ack_at=2026-09-29T12:00:00Z&end_at=2026-09-29T12:30:00Z&end_id=1"},
		{"POST", "/api/v1/internal/charge-end-deliveries/freeze"},
		{"GET", "/api/v1/internal/card-events"},
		{"POST", "/api/v1/internal/card-events/decide-begin"},
		{"POST", "/api/v1/internal/card-events/decide-finish"},
		{"POST", "/api/v1/internal/card-events/advance"},
		{"GET", "/api/v1/internal/ports/resolve?device_id=d-1&port_no=1"},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
		router.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatalf("%s %s status=%d want=401", tc.method, tc.path, w.Code)
		}
	}
}
