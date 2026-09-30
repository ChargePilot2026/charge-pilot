package control

import (
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"net/http/httptest"
	"testing"
)

func TestDeviceSummaryIsAuthenticatedAndBounded(t *testing.T) {
	r := httpapi.NewRouter()
	(DeviceSummaryAPI{ServiceToken: "service"}).Register(r)
	for _, tc := range []struct {
		query, token string
		status       int
	}{{"?device_id=board", "", 401}, {"", "service", 400}, {"?device_id=", "service", 400}} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/internal/device-summaries"+tc.query, nil)
		req.Header.Set("X-Service-Token", tc.token)
		r.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("%s: %d", tc.query, w.Code)
		}
	}
}
