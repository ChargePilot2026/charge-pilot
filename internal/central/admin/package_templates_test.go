package admin

import "testing"

func TestFixedDurationTemplateRequiresPriceAndDuration(t *testing.T) {
	base := packageTemplateInput{Name: "2小时5元", Kind: "package", PriceCents: 500, DurationMinutes: 120, Status: "active"}
	if !validPackageTemplate(base) {
		t.Fatal("priced duration package rejected")
	}
	for _, tc := range []packageTemplateInput{
		{Name: base.Name, Kind: base.Kind, DurationMinutes: 120, Status: "active"},
		{Name: base.Name, Kind: base.Kind, PriceCents: 500, Status: "active"},
		{Name: base.Name, Kind: base.Kind, PriceCents: 1000001, DurationMinutes: 120, Status: "active"},
		{Name: base.Name, Kind: base.Kind, PriceCents: 500, DurationMinutes: 601, Status: "active"},
		{Name: base.Name, Kind: base.Kind, PriceCents: 500, DurationMinutes: 120, MinChargeCents: 1, Status: "active"},
	} {
		if validPackageTemplate(tc) {
			t.Fatalf("invalid template accepted: %+v", tc)
		}
	}
	base.Kind = "amount"
	if validPackageTemplate(base) {
		t.Fatal("amount plan cannot carry duration")
	}
	base.DurationMinutes = 0
	if !validPackageTemplate(base) {
		t.Fatal("amount plan rejected")
	}
}
