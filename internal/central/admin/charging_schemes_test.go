package admin

import (
	"bytes"
	"encoding/json"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFullSchemePreviewIsReadOnlyAndRejectsMissingInputs(t *testing.T) {
	r := gin.New()
	r.POST("/preview", ResourceAPI{}.previewChargingScheme)
	start := time.Date(2026, 9, 30, 1, 0, 0, 0, time.UTC)
	s := pricing.Scheme{Name: "套餐", Packages: []pricing.Package{{ID: 1, Name: "120分钟", Mode: "duration", PriceCents: 300, Minutes: 120}}}
	for _, tc := range []struct {
		name   string
		scheme pricing.Scheme
		want   int
	}{
		{"valid complete scheme with no DB", s, 200}, {"missing required input", pricing.Scheme{}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"scheme": tc.scheme, "package_id": 1, "meter": pricing.ActualMeter{StartedAt: start, EndedAt: start.Add(61*time.Minute + 30*time.Second), ChargedSeconds: 3690, ChargedWh: 100}})
			req := httptest.NewRequest("POST", "/preview", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if w.Code == 200 && !bytes.Contains(w.Body.Bytes(), []byte(`"refund_cents":148`)) {
				t.Fatal(w.Body.String())
			}
		})
	}
}
