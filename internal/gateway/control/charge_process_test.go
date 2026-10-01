package control

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
)

func TestChargeProcessRequiresServiceTokenAndValidBounds(t *testing.T) {
	r := httpapi.NewRouter()
	(TelemetryAPI{ServiceToken: "service"}).Register(r)
	for _, tc := range []struct {
		name, order, query, token string
		status                    int
	}{
		{"missing token", "order", "", "", 401},
		{"wrong token", "order", "", "wrong", 401},
		{"empty order", "%20", "", "service", 400},
		{"long order", strings.Repeat("a", 65), "", "service", 400},
		{"negative cursor", "order", "?after_id=-1", "service", 400},
		{"empty cursor", "order", "?after_id=", "service", 400},
		{"cursor overflow", "order", "?after_id=18446744073709551616", "service", 400},
		{"empty limit", "order", "?limit=", "service", 400},
		{"zero limit", "order", "?limit=0", "service", 400},
		{"large limit", "order", "?limit=2001", "service", 400},
		{"zero order ID", "order", "?charge_order_id=0", "service", 400},
		{"empty device", "order", "?device_id=", "service", 400},
		{"long device", "order", "?device_id=" + strings.Repeat("a", 65), "service", 400},
		{"zero port", "order", "?port_no=0", "service", 400},
		{"port overflow", "order", "?port_no=256", "service", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/api/v1/internal/charge-orders/"+tc.order+"/process"+tc.query, nil)
			req.Header.Set("X-Service-Token", tc.token)
			r.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}
