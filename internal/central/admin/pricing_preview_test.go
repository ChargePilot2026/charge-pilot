package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/gin-gonic/gin"
)

func previewSpec(mode pricing.ChargeMode) pricing.Spec {
	spec := pricing.Spec{Mode: mode, TierPriceBasis: pricing.TierPerKWh, Electric: &pricing.ElectricLine{Basis: mode.BasisFor(), Periods: []pricing.Period{
		{EndMinute: 720, ElectricCents: 100}, {EndMinute: 1440, ElectricCents: 200},
	}}}
	if mode != pricing.ModeServerEnergy {
		for i := range spec.Electric.Periods {
			spec.Electric.Periods[i].ElectricCents = 0
			spec.Electric.Periods[i].Tiers = []pricing.Tier{{MaxWatts: 300, ElectricCents: int64((i + 1) * 100), ServiceCents: 50}, {MaxWatts: 1000, ElectricCents: int64((i + 1) * 200), ServiceCents: 100}}
		}
	}
	return spec
}

func TestPricingPreviewCrossPeriodsAndModes(t *testing.T) {
	scenario := previewScenario{Name: "跨时段", StartMinute: 690, Channel: pricing.ChannelTemp, Segments: []previewSegment{{Minutes: 30, Watts: 200}, {Minutes: 30, Watts: 600}}}
	for _, tc := range []struct {
		mode              pricing.ChargeMode
		electric, service int64
	}{
		{pricing.ModeServerEnergy, 70, 8},
		{pricing.ModeServerRealtimePower, 130, 35},
		{pricing.ModeServerMaxPower, 200, 100},
	} {
		spec := previewSpec(tc.mode)
		if tc.mode == pricing.ModeServerEnergy {
			spec.Service = &pricing.ServiceLine{Basis: pricing.ServiceEnergy, CentsPerKWh: 20}
		} else {
			spec.Service = &pricing.ServiceLine{Basis: pricing.ServiceMinutePower}
		}
		result, ok := calculatePricingPreview(spec, scenario)
		if !ok || result.EnergyWh != 400 || result.Fee.ElectricCents != tc.electric || result.Fee.ServiceCents != tc.service {
			t.Fatalf("%s: %+v ok=%v", tc.mode, result, ok)
		}
	}
}

func TestPricingPreviewStrategiesAndNoMutation(t *testing.T) {
	spec := previewSpec(pricing.ModeServerEnergy)
	spec.LossRateBP = 1000
	spec.Multiplier = &pricing.ChannelMultiplier{TempBP: 20000, CardBP: 5000}
	spec.Service = &pricing.ServiceLine{Basis: pricing.ServiceSession, CentsPerSession: 25}
	spec.MinElectricCents, spec.SpendCapCents, spec.FreeMinutes, spec.CardMaxMinutes = 50, 60, 10, 30
	scenario := previewScenario{StartMinute: 0, Channel: pricing.ChannelCard, Segments: []previewSegment{{Minutes: 60, Watts: 200}}}
	before, _ := json.Marshal(spec)
	result, ok := calculatePricingPreview(spec, scenario)
	if !ok || result.Fee.ElectricCents != 50 || result.Fee.ServiceCents != 25 || result.BaseFee.ElectricCents != 22 || result.Fee.BillableWh != 220 || !result.CapReached || !result.CardLimitExceeded {
		t.Fatalf("strategy mismatch: %+v", result)
	}
	scenario.Segments[0].Minutes = 10
	result, ok = calculatePricingPreview(spec, scenario)
	if !ok || result.Fee.TotalCents != 0 {
		t.Fatal("free window not applied")
	}
	scenario.Segments[0] = previewSegment{Minutes: 60, Watts: 0}
	result, ok = calculatePricingPreview(spec, scenario)
	if !ok || result.Fee.TotalCents != 0 {
		t.Fatal("no-energy scenario charged")
	}
	after, _ := json.Marshal(spec)
	if !bytes.Equal(before, after) {
		t.Fatal("preview changed the draft")
	}
}

func TestPricingPreviewRequestValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	spec := previewSpec(pricing.ModeServerEnergy)
	valid := previewScenario{StartMinute: 1430, Channel: pricing.ChannelTemp, Segments: []previewSegment{{Minutes: 60, Watts: 300}}}
	for _, tc := range []struct {
		name   string
		input  pricingPreviewInput
		status int
	}{
		{"valid cross midnight", pricingPreviewInput{Spec: spec, Scenarios: []previewScenario{valid}}, http.StatusOK},
		{"empty", pricingPreviewInput{Spec: spec}, http.StatusBadRequest},
		{"device", pricingPreviewInput{Spec: pricing.Spec{Mode: pricing.ModeDeviceDuration}, Scenarios: []previewScenario{valid}}, http.StatusBadRequest},
		{"invalid channel", pricingPreviewInput{Spec: spec, Scenarios: []previewScenario{{Channel: "other", Segments: valid.Segments}}}, http.StatusBadRequest},
		{"out of range", pricingPreviewInput{Spec: spec, Scenarios: []previewScenario{{StartMinute: 1440, Channel: pricing.ChannelTemp, Segments: valid.Segments}}}, http.StatusBadRequest},
		{"unbounded duration", pricingPreviewInput{Spec: spec, Scenarios: []previewScenario{{Channel: pricing.ChannelTemp, Segments: []previewSegment{{Minutes: 1441, Watts: 300}}}}}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.POST("/preview", (ResourceAPI{}).previewPricingTemplate)
			body, _ := json.Marshal(tc.input)
			req := httptest.NewRequest(http.MethodPost, "/preview", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if tc.status == http.StatusOK && !bytes.Contains(rec.Body.Bytes(), []byte(`"total_cents"`)) {
				t.Fatalf("fee serialization mismatch: %s", rec.Body.String())
			}
		})
	}
}
