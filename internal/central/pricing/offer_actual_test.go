package pricing

import (
	"testing"
	"time"
)

func TestConfiguredOfferSettlement(t *testing.T) {
	start := time.Date(2026, 9, 29, 2, 0, 0, 0, time.UTC)
	rule := Rule{ID: 1, StationID: 2, Version: 1, Mode: "kwh", Periods: []Period{{Period: "flat", Start: "00:00", End: "24:00", ElectricPriceCents: 100}}, ServiceCentsPerKWh: 100}
	meter := ActualMeter{StartedAt: start, EndedAt: start.Add(30 * time.Minute), ChargedWh: 1000, ChargedSeconds: 1800}
	tests := []struct {
		name  string
		offer Offer
		total int64
	}{
		{"package half used", Offer{ID: 1, StationID: 2, Code: "P60", Name: "套餐", Mode: "package", PriceCents: 600, DurationMinutes: 60}, 300},
		{"amount cap", Offer{ID: 2, StationID: 2, Code: "A1", Name: "金额", Mode: "amount", PriceCents: 100}, 100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PriceOfferActual(rule, &tc.offer, meter)
			if err != nil {
				t.Fatal(err)
			}
			if got.TotalCents != tc.total || got.ElectricCents+got.ServiceCents != tc.total {
				t.Fatalf("fee=%+v", got)
			}
		})
	}
	zero := ActualMeter{StartedAt: start, EndedAt: start, ChargedWh: 0, ChargedSeconds: 0}
	got, err := PriceOfferActual(rule, &tests[0].offer, zero)
	if err != nil || got.TotalCents != 0 {
		t.Fatalf("unused package must be fully refunded: fee=%+v err=%v", got, err)
	}
}
