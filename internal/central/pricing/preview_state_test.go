package pricing

import (
	"testing"
	"time"
)

func TestPreviewStopsAtFirstMeasuredBudgetBoundary(t *testing.T) {
	s := exampleScheme(ModeServerRealtimePower, []Period{examplePeriod(80, 40)}, 100)
	start := time.Date(2026, 9, 30, 0, 0, 0, 0, beijing)
	m := exampleMeter(start, []int{30, 30, 30}, []uint32{180, 180, 600}, []uint32{100, 100, 100})
	got, err := s.Preview(1, m)
	if err != nil || got.CutoffAt == nil || !got.CutoffAt.Equal(start.Add(time.Hour)) || got.RawFee.TotalCents != 120 || got.Settlement.TotalCents != 100 || got.RefundCents != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestCardPreviewUsesConfirmedPurchasesAndWholeLimit(t *testing.T) {
	s := Scheme{Name: "刷卡", Packages: []Package{{ID: 1, Name: "120分钟", Mode: "duration", PriceCents: 200, Minutes: 120}}, Card: CardPolicy{PackageID: 1, MaxMinutes: 600}}.Normalized()
	start := time.Date(2026, 9, 30, 0, 0, 0, 0, beijing)
	m := exampleMeter(start, []int{150}, []uint32{180}, []uint32{200})
	for _, tc := range []struct {
		scenario             string
		wallet, total, after int64
		status               string
	}{
		{"card_extend", 1000, 250, 750, "calculated"},
		{"card_failed", 1000, 200, 800, "calculated"},
		{"card_unknown", 1000, 0, 600, "confirming"},
		{"card_extend", 300, 200, 100, "calculated"},
	} {
		got, err := s.PreviewScenario(1, m, tc.scenario, tc.wallet)
		if err != nil || got.Status != tc.status || got.Settlement.TotalCents != tc.total || got.WalletAfter == nil || *got.WalletAfter != tc.after {
			t.Fatalf("%s: %+v %v", tc.scenario, got, err)
		}
		if got.Status == "calculated" && got.Settlement.Executor != "server" {
			t.Fatalf("card preview executor: %+v", got.Settlement)
		}
	}
	s.Card.MaxMinutes = 180
	got, err := s.PreviewScenario(1, m, "card_extend", 1000)
	if err != nil || got.PaidCents != 200 {
		t.Fatalf("partial extension at limit: %+v %v", got, err)
	}
}
