package pricing

import (
	"testing"
	"time"
)

func TestConfiguredOfferSettlement(t *testing.T) {
	start := time.Date(2026, 9, 29, 2, 0, 0, 0, time.UTC)
	rule := Rule{ID: 1, StationID: 2, Version: 1, Spec: Spec{
		Mode:     ModeServerEnergy,
		Electric: &ElectricLine{Basis: BasisEnergy, Periods: []Period{{EndMinute: 1440, ElectricCents: 100}}},
		Service:  &ServiceLine{Basis: ServiceEnergy, CentsPerKWh: 100},
	}}
	// 半小时 1kWh：100 分电费加 100 分服务费。
	meter := ActualMeter{StartedAt: start, EndedAt: start.Add(30 * time.Minute), ChargedWh: 1000, ChargedSeconds: 1800}
	tests := []struct {
		name  string
		offer Offer
		total int64
	}{
		// 固定时长套餐只为用掉的那一段时长收费，所以提前结束的充电
		// 会把剩下的部分退回去。
		{"package half used", Offer{ID: 1, StationID: 2, Name: "套餐", Mode: "package", PriceCents: 600, DurationMinutes: 60}, 100},
		{"amount cap", Offer{ID: 2, StationID: 2, Name: "金额", Mode: "amount", PriceCents: 100}, 100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PriceOfferActual(rule, &tc.offer, meter)
			if err != nil {
				t.Fatal(err)
			}
			if got.TotalCents != tc.total || got.ElectricCents+got.ServiceCents != tc.total {
				t.Fatalf("fee=%+v, want a total of %d split without loss", got, tc.total)
			}
		})
	}
	// 买了却完全没用的套餐必须全额退回。
	zero := ActualMeter{StartedAt: start, EndedAt: start, ChargedWh: 0, ChargedSeconds: 0}
	got, err := PriceOfferActual(rule, &tests[0].offer, zero)
	if err != nil || got.TotalCents != 0 {
		t.Fatalf("an unused package must be fully refunded: fee=%+v err=%v", got, err)
	}
}
