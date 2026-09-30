package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/gin-gonic/gin"
)

func TestPricingTemplateWritesRejectLegacyUnitsBeforeStorage(t *testing.T) {
	for _, basis := range []pricing.TierPriceBasis{"", pricing.TierPerHourAtCeiling, "unknown", pricing.TierPerKWh} {
		in := pricingTemplateInput{Name: "测试电价", Spec: pricing.Spec{
			Mode: pricing.ModeServerRealtimePower, TierPriceBasis: basis,
			Electric: &pricing.ElectricLine{Basis: pricing.BasisRealtimePower, Periods: []pricing.Period{
				{EndMinute: 1440, Tiers: []pricing.Tier{{MaxWatts: 200, ElectricCents: 100}}},
			}},
		}, ExpectedVersion: 1}
		if basis == pricing.TierPerKWh {
			if !validTemplate(in) {
				t.Fatal("new per-kWh template rejected")
			}
			continue
		}
		if validTemplate(in) {
			t.Fatalf("legacy template accepted: %q", basis)
		}
		body, _ := json.Marshal(in)
		for _, method := range []string{http.MethodPost, http.MethodPut} {
			router := gin.New()
			api := ResourceAPI{}
			router.POST("/templates", api.createPricingTemplate)
			router.PUT("/templates/:id", api.updatePricingTemplate)
			url := "/templates"
			if method == http.MethodPut {
				url += "/1"
			}
			request := httptest.NewRequest(method, url, bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "旧版电价") {
				t.Fatalf("%s %q: status=%d body=%s", method, basis, recorder.Code, recorder.Body.String())
			}
		}
	}
}
