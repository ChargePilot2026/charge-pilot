package admin

import (
	"encoding/json"
	"testing"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
)

func TestOrderPackageKeepsPurchasedOfferSeparateFromSchemePackages(t *testing.T) {
	// Even a same-ID package in the scheme must not replace the purchased offer.
	rule := pricing.Rule{Version: 3, Spec: pricing.Spec{Mode: pricing.ModeDeviceDuration,
		Scheme: &pricing.Scheme{Name: "下单时方案", Packages: []pricing.Package{{ID: 2, Name: "其他价格", Mode: "duration", PriceCents: 900, Minutes: 120}}}}}
	offer := pricing.Offer{ID: 102, PackageID: 2, StationID: 1, Name: "购买60分钟", Mode: "duration", PriceCents: 100, DurationMinutes: 60}
	raw, err := json.Marshal(map[string]any{"rule": rule, "offer": offer})
	if err != nil {
		t.Fatal(err)
	}
	got := packageFromSnapshot(raw)
	if got == nil || got.Offer != offer || got.RuleVersion != 3 || got.BillingMode != pricing.ModeDeviceDuration || got.Scheme.Name != "下单时方案" {
		t.Fatalf("unexpected frozen package: %+v", got)
	}
}

func TestOrderPackageMissingOrInvalidSnapshotDoesNotInventSelection(t *testing.T) {
	for _, raw := range []string{"", "null", "{", `{}`, `{"rule":{"version":1}}`, `{"offer":{"name":"无效套餐","price_cents":0}}`} {
		if got := packageFromSnapshot([]byte(raw)); got != nil {
			t.Fatalf("expected no selected package for %q, got %+v", raw, got)
		}
	}
}

func TestOrderPackageAllModes(t *testing.T) {
	for _, offer := range []pricing.Offer{
		{ID: 101, StationID: 1, Name: "2元预算", Mode: "amount", PriceCents: 200, MaxMinutes: 600},
		{ID: 102, StationID: 1, Name: "60分钟", Mode: "duration", PriceCents: 100, DurationMinutes: 60},
		{ID: 103, StationID: 1, Name: "2度", Mode: "energy", PriceCents: 220, EnergyWh: 2000},
	} {
		raw, err := json.Marshal(map[string]any{"offer": offer})
		if err != nil {
			t.Fatal(err)
		}
		got := packageFromSnapshot(raw)
		if got == nil || got.Offer != offer {
			t.Fatalf("lost purchased %s entitlement: %+v", offer.Mode, got)
		}
	}
}
