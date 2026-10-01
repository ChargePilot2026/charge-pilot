package admin

import (
	"encoding/json"
	"strings"
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
	if name := selectedSchemeName(got); name == nil || *name != "下单时方案" {
		t.Fatalf("selected scheme was replaced by purchased offer name: %v", name)
	}
	if name := selectedPackageName(got); name == nil || *name != "购买60分钟" {
		t.Fatalf("selected package name did not preserve the purchased offer: %v", name)
	}
}

func TestSelectedSchemeNameRequiresFrozenValidName(t *testing.T) {
	for _, selected := range []*OrderPackage{
		nil, {Offer: pricing.Offer{Name: "价格档名"}}, {Scheme: &pricing.Scheme{Name: "  "}},
	} {
		if name := selectedSchemeName(selected); name != nil {
			t.Fatalf("invented selected scheme name: %s", *name)
		}
	}
	if name := selectedSchemeName(&OrderPackage{Scheme: &pricing.Scheme{Name: " 下单冻结方案 "}}); name == nil || *name != "下单冻结方案" {
		t.Fatalf("valid frozen scheme name missing: %v", name)
	}
	if name := selectedSchemeName(&OrderPackage{Scheme: &pricing.Scheme{Name: strings.Repeat("历史名称", 20)}}); name == nil || *name != strings.Repeat("历史名称", 20) {
		t.Fatalf("historical frozen scheme name was hidden: %v", name)
	}
	if err := (ResourceStore{}).orderSchemeNames(nil, nil); err != nil {
		t.Fatalf("empty order page performed a database lookup: %v", err)
	}
}

func TestSelectedPackageNameUsesPurchasedOfferAndTrimsBlank(t *testing.T) {
	for _, selected := range []*OrderPackage{nil, {}, {Offer: pricing.Offer{Name: "  "}, Scheme: &pricing.Scheme{Name: "方案名"}}} {
		if name := selectedPackageName(selected); name != nil {
			t.Fatalf("invented purchased package name: %s", *name)
		}
	}
	if name := selectedPackageName(&OrderPackage{Offer: pricing.Offer{Name: " 1元 "}, Scheme: &pricing.Scheme{Name: "金额模式"}}); name == nil || *name != "1元" {
		t.Fatalf("purchased package name changed: %v", name)
	}
	for _, raw := range []string{"", "null", "{", `{}`, `{"rule":{"spec":{"scheme":{"name":"存在方案但无有效套餐"}}}}`} {
		selected := packageFromSnapshot([]byte(raw))
		if selectedSchemeName(selected) != nil || selectedPackageName(selected) != nil {
			t.Fatalf("invalid snapshot invented selection names: %q", raw)
		}
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
