package charge

import (
	"context"

	"github.com/ChargePilot2026/charge-pilot/internal/central/payment"
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
)

// Test fixtures freeze the complete scheme and exactly one configured package.
// IntentInput 已随迁 payment 包，本夹具留在 charge 供跨家族集成测试复用。
func completeIntent(in payment.IntentInput, cents int64) payment.IntentInput {
	s := pricing.Scheme{Name: "集成方案", Amount: &pricing.AmountMode{Algorithm: pricing.ModeServerEnergy, Periods: []pricing.Period{{EndMinute: 1440, ElectricCents: 100, ServiceCents: 40}}}, Packages: []pricing.Package{{ID: 1, Name: "金额", Mode: "amount", PriceCents: cents}}}.Normalized()
	in.Rule = pricing.Rule{ID: 3, StationID: in.Port.StationID, Version: 1, Spec: s.SpecFor(s.Packages[0])}
	offer := s.Offers(in.Rule)[0]
	in.Offer = &offer
	return in
}

// scanSession 与 payment 包 scan_test.go 内的同名助手保持一致：
// 会话存在、用户有效，供需要鉴权的跨家族集成测试构造会话。
type scanSession struct{}

func (scanSession) Exists(context.Context, string) (bool, error) { return true, nil }
