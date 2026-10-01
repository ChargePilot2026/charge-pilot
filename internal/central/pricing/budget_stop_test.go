package pricing

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func budgetMeterFixture() (Rule, *Offer, ActualMeter) {
	start := time.Date(2026, 10, 1, 3, 5, 40, 0, time.UTC)
	power := uint32(6000)
	rule := Rule{ID: 2, Version: 2, Spec: Spec{Mode: ModeServerMaxPower, Electric: &ElectricLine{Basis: BasisMaxPower, Periods: []Period{{EndMinute: 1440, Tiers: []Tier{
		{MaxWatts: 200, ElectricCents: 50, ServiceCents: 10},
		{MaxWatts: 1000, ElectricCents: 100, ServiceCents: 20},
		{MaxWatts: 3500, ElectricCents: 200, ServiceCents: 30},
	}}}}}}
	offer := &Offer{ID: 201, StationID: 1, Name: "1元预算", Mode: "amount", PriceCents: 100}
	meter := ActualMeter{StartedAt: start, EndedAt: start.Add(32 * time.Minute), ChargedSeconds: 1920, ChargedWh: 2000,
		Segments: []MeterSegment{
			{StartedAt: start, EndedAt: start.Add(3 * time.Second), EnergyWh: 1},
			{StartedAt: start.Add(3 * time.Second), EndedAt: start.Add(20 * time.Minute), EnergyWh: 1200, PowerW: &power, PeakW: power},
			{StartedAt: start.Add(20 * time.Minute), EndedAt: start.Add(90*time.Second + 20*time.Minute), EnergyWh: 100},
			{StartedAt: start.Add(90*time.Second + 20*time.Minute), EndedAt: start.Add(32 * time.Minute), EnergyWh: 699, PowerW: &power, PeakW: power},
		}}
	return rule, offer, meter
}

func TestBudgetStopsWithMissingInitialAndReconnectPower(t *testing.T) {
	rule, offer, meter := budgetMeterFixture()
	if _, err := PriceActual(rule, meter); !errors.Is(err, ErrMeterReview) {
		t.Fatalf("fixture must reproduce the original pricing rejection: %v", err)
	}
	originalTiers := append([]Tier(nil), rule.Spec.Electric.Periods[0].Tiers...)
	plan, fee, err := BudgetStopAtMeter(rule, offer, meter)
	if err != nil || !plan.ShouldStop || plan.AccruedCents != 123 || fee == nil || fee.TotalCents != 100 || fee.ElectricCents+fee.ServiceCents != 100 {
		t.Fatalf("budget not stopped/capped: plan=%+v fee=%+v err=%v", plan, fee, err)
	}
	if !reflect.DeepEqual(rule.Spec.Electric.Periods[0].Tiers, originalTiers) || meter.Segments[0].PowerW != nil || meter.Segments[2].PowerW != nil {
		t.Fatal("budget decision mutated frozen rules or final settlement evidence")
	}
	if _, err := PriceActual(rule, meter); !errors.Is(err, ErrMeterReview) {
		t.Fatal("budget decision weakened strict final pricing validation")
	}
}

func TestBudgetDoesNotStopBelowPriceOrDuringFreeTime(t *testing.T) {
	rule, offer, meter := budgetMeterFixture()
	offer.PriceCents = 200
	plan, fee, err := BudgetStopAtMeter(rule, offer, meter)
	if err != nil || plan.ShouldStop || fee != nil {
		t.Fatalf("stopped before budget exhausted: %+v %+v %v", plan, fee, err)
	}
	offer.PriceCents = 100
	rule.Spec.FreeMinutes = 40
	plan, fee, err = BudgetStopAtMeter(rule, offer, meter)
	if err != nil || plan.ShouldStop || fee != nil || plan.AccruedCents != 0 {
		t.Fatalf("free charging consumed budget: %+v %+v %v", plan, fee, err)
	}
}

func TestBudgetLowerBoundHandlesNonMonotonicPeakPrices(t *testing.T) {
	rule, offer, meter := budgetMeterFixture()
	for i := range meter.Segments {
		if meter.Segments[i].PowerW != nil {
			power := uint32(600)
			meter.Segments[i].PowerW, meter.Segments[i].PeakW = &power, power
		}
	}
	// A missed higher peak could select this cheaper tier. Do not stop based
	// on the more expensive observed lower tier while such a gap remains.
	rule.Spec.Electric.Periods[0].Tiers[2].ElectricCents = 10
	rule.Spec.Electric.Periods[0].Tiers[2].ServiceCents = 0
	plan, fee, err := BudgetStopAtMeter(rule, offer, meter)
	if err != nil || plan.ShouldStop || fee != nil || plan.AccruedCents != 5 {
		t.Fatalf("non-monotonic rates caused a premature stop: %+v %+v %v", plan, fee, err)
	}
}

func TestBudgetStopsWithoutInventingRealtimeFeeSplit(t *testing.T) {
	rule, offer, meter := budgetMeterFixture()
	rule.Spec.Mode, rule.Spec.Electric.Basis = ModeServerRealtimePower, BasisRealtimePower
	plan, fee, err := BudgetStopAtMeter(rule, offer, meter)
	if err != nil || !plan.ShouldStop || plan.AccruedCents < 100 || fee != nil {
		t.Fatalf("must stop but leave uncertain split for settlement: %+v %+v %v", plan, fee, err)
	}
}

func TestBudgetBoundsNeverAcceptConflictingMeters(t *testing.T) {
	for _, change := range []func(*ActualMeter){
		func(m *ActualMeter) { m.ChargedWh++ },
		func(m *ActualMeter) { m.Segments[1].StartedAt = m.Segments[1].StartedAt.Add(time.Second) },
		func(m *ActualMeter) { m.ReviewRequired = true },
	} {
		rule, offer, meter := budgetMeterFixture()
		change(&meter)
		if _, _, err := BudgetStopAtMeter(rule, offer, meter); !errors.Is(err, ErrMeterReview) {
			t.Fatalf("accepted conflicting meter: %v", err)
		}
	}
}

func TestBudgetEnergyBoundsDoNotDistributeMissingBoundaryEnergy(t *testing.T) {
	start := time.Date(2026, 10, 1, 3, 55, 0, 0, time.UTC) // 11:55 Beijing
	rule := Rule{ID: 1, Version: 1, Spec: Spec{Mode: ModeServerEnergy, Electric: &ElectricLine{Basis: BasisEnergy, Periods: []Period{
		{EndMinute: 720, ElectricCents: 100, ServiceCents: 10},
		{EndMinute: 1440, ElectricCents: 200, ServiceCents: 30},
	}}}}
	meter := ActualMeter{StartedAt: start, EndedAt: start.Add(10 * time.Minute), ChargedSeconds: 600, ChargedWh: 2000,
		Segments: []MeterSegment{{StartedAt: start, EndedAt: start.Add(10 * time.Minute), EnergyWh: 2000}}}
	offer := &Offer{ID: 101, StationID: 1, Name: "2元预算", Mode: "amount", PriceCents: 200}
	plan, fee, err := BudgetStopAtMeter(rule, offer, meter)
	if err != nil || !plan.ShouldStop || plan.AccruedCents != 220 || fee != nil {
		t.Fatalf("incorrect lower bound or invented split: %+v %+v %v", plan, fee, err)
	}
	if _, err := PriceActual(rule, meter); !errors.Is(err, ErrMeterReview) {
		t.Fatal("missing boundary readings became exact settlement evidence")
	}
}

func TestBudgetBoundsContainEveryPossibleMissingPeak(t *testing.T) {
	for _, rates := range [][]Tier{
		{{MaxWatts: 200, ElectricCents: 50, ServiceCents: 10}, {MaxWatts: 1000, ElectricCents: 100, ServiceCents: 20}, {MaxWatts: 3500, ElectricCents: 200, ServiceCents: 30}},
		{{MaxWatts: 200, ElectricCents: 100, ServiceCents: 30}, {MaxWatts: 1000, ElectricCents: 60, ServiceCents: 80}, {MaxWatts: 3500, ElectricCents: 10, ServiceCents: 5}},
	} {
		rule, _, meter := budgetMeterFixture()
		rule.Spec.Electric.Periods[0].Tiers = rates
		for _, observed := range []uint32{0, 200, 201, 1000, 1001, 6000} {
			for i := range meter.Segments {
				if meter.Segments[i].PowerW != nil {
					meter.Segments[i].PowerW, meter.Segments[i].PeakW = &observed, observed
				}
			}
			lower, err := budgetBound(rule, meter, false)
			if err != nil {
				t.Fatal(err)
			}
			upper, err := budgetBound(rule, meter, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, missing := range []uint32{0, 200, 201, 1000, 1001, 3500, 6000} {
				complete := meter
				complete.Segments = append([]MeterSegment(nil), meter.Segments...)
				for i := range complete.Segments {
					if complete.Segments[i].PowerW == nil {
						complete.Segments[i].PowerW, complete.Segments[i].PeakW = &missing, missing
					}
				}
				actual, err := PriceActual(rule, complete)
				if err != nil || actual.ElectricCents < lower.ElectricCents || actual.ElectricCents > upper.ElectricCents || actual.ServiceCents < lower.ServiceCents || actual.ServiceCents > upper.ServiceCents {
					t.Fatalf("invalid bounds observed=%d missing=%d lower=%+v actual=%+v upper=%+v err=%v", observed, missing, lower, actual, upper, err)
				}
			}
		}
	}
}
