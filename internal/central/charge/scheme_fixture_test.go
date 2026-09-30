package charge

import "github.com/ChargePilot2026/charge-pilot/internal/central/pricing"

// Test fixtures freeze the complete scheme and exactly one configured package.
func completeIntent(in IntentInput, cents int64) IntentInput {
	s := pricing.Scheme{Name: "集成方案", Amount: &pricing.AmountMode{Algorithm: pricing.ModeServerEnergy, Periods: []pricing.Period{{EndMinute: 1440, ElectricCents: 100, ServiceCents: 40}}}, Packages: []pricing.Package{{ID: 1, Name: "金额", Mode: "amount", PriceCents: cents}}}.Normalized()
	in.Rule = pricing.Rule{ID: 3, StationID: in.Port.StationID, Version: 1, Spec: s.SpecFor(s.Packages[0])}
	offer := s.Offers(in.Rule)[0]
	in.Offer = &offer
	return in
}
