package pricing

import (
	"testing"
	"time"
)

func TestConfiguredOfferSettlement(t *testing.T) {
	start := time.Date(2026, 9, 29, 2, 0, 0, 0, time.UTC)
	rule := Rule{ID: 1, StationID: 2, Version: 1, Spec: Spec{
		Mode:     ModeServerEnergy,
		Electric: &ElectricLine{Basis: BasisEnergy, Periods: []Period{{EndMinute: 1440, ElectricCents: 100, ServiceCents: 100}}},
	}}
	// 半小时 1kWh：100 分电费加 100 分服务费。
	meter := ActualMeter{StartedAt: start, EndedAt: start.Add(30 * time.Minute), ChargedWh: 1000, ChargedSeconds: 1800}
	tests := []struct {
		name  string
		offer Offer
		total int64
	}{
		// 固定时长套餐按实际使用时长收费，提前结束时退还剩余部分。
		{"package half used", Offer{ID: 1, StationID: 2, Name: "套餐", Mode: "duration", PriceCents: 600, DurationMinutes: 60}, 300},
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

// 固定售价不能随着站点费率、用电量或服务端/设备执行方式改变。
func TestFixedDurationOfferUsesItsPriceAndRefundsUnusedTime(t *testing.T) {
	offer := Offer{ID: 1, StationID: 2, Name: "2小时5元", Mode: "duration", PriceCents: 500, DurationMinutes: 120}
	for _, mode := range []ChargeMode{ModeServerEnergy, ModeServerRealtimePower, ModeServerMaxPower, ModeDeviceDuration} {
		spec := Spec{Mode: mode}
		if mode.ServerBilled() {
			spec = realtimeSpec()
			spec.Mode = mode
			spec.Electric.Basis = mode.BasisFor()
			if mode == ModeServerEnergy {
				spec.Electric.Periods[0] = Period{EndMinute: 1440, ElectricCents: 999999}
			}
		}
		for _, tc := range []struct {
			seconds uint32
			want    int64
		}{{0, 0}, {1, 0}, {3600, 250}, {7200, 500}, {7500, 500}} {
			// 缺少分时计量也不阻止固定售价结算。
			meter := ActualMeter{ChargedSeconds: tc.seconds}
			actual := &SessionActual{UsedSeconds: tc.seconds, Reported: true}
			got, err := SettleSession(spec, meter, &offer, actual)
			if err != nil || got.TotalCents != tc.want || got.ElectricCents+got.ServiceCents != tc.want {
				t.Fatalf("mode=%s seconds=%d got=%+v err=%v want=%d", mode, tc.seconds, got, err, tc.want)
			}
		}
	}
	invalid := offer
	invalid.PriceCents = 0
	if _, err := SettleSession(Spec{Mode: ModeDeviceDuration}, ActualMeter{}, &invalid, nil); err == nil {
		t.Fatal("unpriced duration package accepted")
	}
}
