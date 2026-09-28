package pricing

import (
	"errors"
	"testing"
	"time"
)

func TestEstimateChargeKeepsElectricAndServiceSeparate(t *testing.T) {
	rule := Rule{ID: 1, StationID: 9, Version: 2, Mode: "kwh", ServiceCentsPerKWh: 40,
		Periods: []Period{{Period: "all", Start: "00:00", End: "24:00", ElectricPriceCents: 100}}}
	result, err := EstimateCharge(rule, "1.000", 60, time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC))
	if err != nil || result.ElectricCents != 100 || result.ServiceCents != 40 || result.TotalCents != 140 || result.ChargeQuantity != 60 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	rule.Mode = "minute"
	rule.ServiceCentsPerMinute = 2
	result, err = EstimateCharge(rule, "1", 60, time.Now())
	if err != nil || result.ElectricCents != 100 || result.ServiceCents != 120 || result.TotalCents != 220 {
		t.Fatalf("minute result=%+v err=%v", result, err)
	}
	rule.MinimumCents = 300
	result, err = EstimateCharge(rule, "1", 60, time.Now())
	if err != nil || result.ElectricCents != 100 || result.ServiceCents != 200 || result.TotalCents != 300 {
		t.Fatalf("minimum result=%+v err=%v", result, err)
	}
}

func TestEstimateRejectsIncompleteOrOverlappingTariff(t *testing.T) {
	rule := Rule{ID: 1, StationID: 9, Mode: "kwh", Periods: []Period{{Start: "00:00", End: "12:00", ElectricPriceCents: 100}}}
	if _, err := EstimateCharge(rule, "1", 60, time.Now()); !errors.Is(err, ErrInvalidPricing) {
		t.Fatalf("incomplete tariff: %v", err)
	}
	rule.Periods = append(rule.Periods, Period{Start: "11:00", End: "24:00", ElectricPriceCents: 100})
	if _, err := EstimateCharge(rule, "1", 60, time.Now()); !errors.Is(err, ErrInvalidPricing) {
		t.Fatalf("overlapping tariff: %v", err)
	}
	rule.Periods = []Period{{Start: "00:00", End: "24:00", ElectricPriceCents: 100}}
	if _, err := EstimateCharge(rule, "1.0001", 60, time.Now()); !errors.Is(err, ErrInvalidPricing) {
		t.Fatalf("sub-Wh input: %v", err)
	}
}
