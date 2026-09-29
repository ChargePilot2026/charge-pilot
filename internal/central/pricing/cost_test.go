package pricing

import (
	"testing"
	"time"
)

func beijingAt(hour, minute int) time.Time {
	return time.Date(2026, 3, 1, hour, minute, 0, 0, beijing)
}

func allDay(cents int64) []Window {
	return []Window{{Start: "00:00", End: "24:00", CentsPerKWh: cents}}
}

func mustCost(t *testing.T, spec Spec, usage Usage) Fee {
	t.Helper()
	fee, err := Cost(spec, usage)
	if err != nil {
		t.Fatalf("Cost returned %v", err)
	}
	return fee
}

func flatUsage(start time.Time, minutes int, watts int, wh uint64) Usage {
	return Usage{
		Start: start, End: start.Add(time.Duration(minutes) * time.Minute), EnergyWh: wh,
		Samples: []Sample{{Start: start, End: start.Add(time.Duration(minutes) * time.Minute),
			EnergyWh: wh, PowerW: uint32(watts)}},
	}
}

func TestCostEnergySplitByWindow(t *testing.T) {
	spec := Spec{Basis: BasisEnergy, Windows: []Window{
		{Start: "00:00", End: "12:00", CentsPerKWh: 50},
		{Start: "12:00", End: "24:00", CentsPerKWh: 100},
	}}
	// 2000 Wh over 120 minutes straddling noon: roughly 1 kWh at 5 cents and
	// 1 kWh at 10. The per-minute remainder can move the split by a fraction
	// of a cent, so the assertion is a cent of tolerance around 150, not
	// equality - but it must not collapse to either all-cheap or all-expensive.
	usage := flatUsage(beijingAt(11, 0), 120, 0, 2000)
	fee := mustCost(t, spec, usage)
	if fee.ElectricCents < 149 || fee.ElectricCents > 151 {
		t.Fatalf("electric = %d, want ~150", fee.ElectricCents)
	}
	if fee.BillableWh != 2000 {
		t.Fatalf("billable = %d, want 2000", fee.BillableWh)
	}
}

func TestCostEnergyServiceAndMinuteFloor(t *testing.T) {
	service := int64(20)
	spec := Spec{Basis: BasisEnergy, Windows: []Window{
		{Start: "00:00", End: "24:00", CentsPerKWh: 50, ServiceCentsPerKWh: &service},
	}, Service: ServiceFee{Mode: ServiceEnergy, CentsPerKWh: 20}}
	fee := mustCost(t, spec, flatUsage(beijingAt(0, 0), 60, 0, 1000))
	if fee.ElectricCents != 50 || fee.ServiceCents != 20 || fee.TotalCents != 70 {
		t.Fatalf("fee = %+v", fee)
	}
}

func TestCostPowerTierUsesCeilingConversion(t *testing.T) {
	spec := Spec{Basis: BasisPowerTier, TierPriceBasis: TierPerHourAtCeiling,
		Tiers: []Tier{
			{LowW: 0, HighW: 200, CentsPerHour: 17},
			{LowW: 201, HighW: 250, CentsPerHour: 20},
			{LowW: 251, HighW: 750, CentsPerHour: 55},
		},
		Windows: allDay(100), // also the above-top-tier fallback
	}
	// 1 kWh drawn entirely in the 0~200W band at 17 cents/hour over a 200W
	// ceiling is 17 * 0.2 = 3.4 cents per kWh.
	fee := mustCost(t, spec, flatUsage(beijingAt(0, 0), 60, 200, 1000))
	if fee.ElectricCents != 3 {
		t.Fatalf("low tier electric = %d, want 3", fee.ElectricCents)
	}
	// Above the top tier the window fallback price applies instead.
	fee = mustCost(t, spec, flatUsage(beijingAt(0, 0), 60, 900, 1000))
	if fee.ElectricCents != 100 {
		t.Fatalf("above-top electric = %d, want 100", fee.ElectricCents)
	}
}

func TestCostPowerTierPerKWhBasis(t *testing.T) {
	spec := Spec{Basis: BasisPowerTier, TierPriceBasis: TierPerKWh,
		Tiers:   []Tier{{LowW: 0, HighW: 200, CentsPerHour: 40}},
		Windows: allDay(100),
	}
	fee := mustCost(t, spec, flatUsage(beijingAt(0, 0), 60, 100, 1000))
	if fee.ElectricCents != 40 {
		t.Fatalf("electric = %d, want 40", fee.ElectricCents)
	}
}

func TestCostPowerTierServiceFollowsSameBand(t *testing.T) {
	spec := Spec{Basis: BasisPowerTier, TierPriceBasis: TierPerHourAtCeiling,
		Tiers:   []Tier{{LowW: 0, HighW: 200, CentsPerHour: 17, ServiceCentsPerHour: 60}},
		Windows: allDay(100),
		Service: ServiceFee{Mode: ServiceMinutePower},
	}
	fee := mustCost(t, spec, flatUsage(beijingAt(0, 0), 60, 200, 1000))
	if fee.ServiceCents != 12 { // 60 cents/hour * 0.2 kW
		t.Fatalf("service = %d, want 12", fee.ServiceCents)
	}
}

func TestCostMaxPowerChargesThePeak(t *testing.T) {
	spec := Spec{Basis: BasisMaxPower,
		Windows: []Window{{Start: "00:00", End: "24:00", CentsPerHourPerKW: 60}},
	}
	// Half an hour peaking at 1500W: 1.5kW * 0.5h * 60 cents.
	usage := flatUsage(beijingAt(0, 0), 30, 1500, 500)
	fee := mustCost(t, spec, usage)
	if fee.ElectricCents != 45 {
		t.Fatalf("electric = %d, want 45", fee.ElectricCents)
	}
}

func TestCostPerMinuteAndPerSession(t *testing.T) {
	perMinute := mustCost(t, Spec{Basis: BasisPerMinute, PerMinuteCents: 5}, flatUsage(beijingAt(0, 0), 30, 0, 100))
	if perMinute.ElectricCents != 150 {
		t.Fatalf("per minute = %d, want 150", perMinute.ElectricCents)
	}
	perSession := mustCost(t, Spec{Basis: BasisPerSession, PerSessionCents: 300}, flatUsage(beijingAt(0, 0), 30, 0, 100))
	if perSession.ElectricCents != 300 {
		t.Fatalf("per session = %d, want 300", perSession.ElectricCents)
	}
}

func TestCostServiceModes(t *testing.T) {
	usage := flatUsage(beijingAt(0, 0), 30, 0, 1000)
	cases := []struct {
		name    string
		service ServiceFee
		want    int64
	}{
		{"none", ServiceFee{Mode: ServiceNone, CentsPerKWh: 20}, 0},
		{"energy", ServiceFee{Mode: ServiceEnergy, CentsPerKWh: 20}, 20},
		{"minute", ServiceFee{Mode: ServiceMinute, CentsPerMinute: 3}, 90},
		{"session", ServiceFee{Mode: ServiceSession, CentsPerSession: 200}, 200},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			spec := Spec{Basis: BasisEnergy, Windows: allDay(50), Service: testCase.service}
			if fee := mustCost(t, spec, usage); fee.ServiceCents != testCase.want {
				t.Fatalf("service = %d, want %d", fee.ServiceCents, testCase.want)
			}
		})
	}
}

func TestCostFreeMinutesWaivesWholeSession(t *testing.T) {
	spec := Spec{Basis: BasisEnergy, Windows: allDay(50), FreeMinutes: 10}
	fee := mustCost(t, spec, flatUsage(beijingAt(0, 0), 10, 0, 1000))
	if fee.TotalCents != 0 || fee.ElectricCents != 0 || fee.ServiceCents != 0 {
		t.Fatalf("free session still charged %+v", fee)
	}
	if fee.Basis != BasisEnergy {
		t.Fatalf("basis = %q, want it echoed even when free", fee.Basis)
	}
	charged := mustCost(t, spec, flatUsage(beijingAt(0, 0), 11, 0, 1000))
	if charged.ElectricCents != 50 {
		t.Fatalf("past the free window electric = %d, want 50", charged.ElectricCents)
	}
}

func TestCostMinimumTopsUpElectricNotService(t *testing.T) {
	spec := Spec{Basis: BasisEnergy, Windows: allDay(10), MinElectricCents: 100,
		Service: ServiceFee{Mode: ServiceEnergy, CentsPerKWh: 5}}
	fee := mustCost(t, spec, flatUsage(beijingAt(0, 0), 10, 0, 100))
	// 1 cent of real electric becomes 100; service stays at its own 0 cents
	// rather than being inflated into a balance filler.
	if fee.ElectricCents != 100 {
		t.Fatalf("electric = %d, want the 100 cent floor", fee.ElectricCents)
	}
}

func TestCostLossRateInflatesEnergy(t *testing.T) {
	spec := Spec{Basis: BasisEnergy, Windows: allDay(100), LossRateBP: 500}
	fee := mustCost(t, spec, flatUsage(beijingAt(0, 0), 60, 0, 1000))
	if fee.ElectricCents != 105 {
		t.Fatalf("electric = %d, want 105", fee.ElectricCents)
	}
	if fee.BillableWh != 1050 {
		t.Fatalf("billable = %d, want 1050", fee.BillableWh)
	}
}

func TestCostChannelMultipliers(t *testing.T) {
	spec := Spec{Basis: BasisEnergy, Windows: allDay(100),
		Multipliers: Multiplier{CardElectricBP: 8000}}
	card := flatUsage(beijingAt(0, 0), 60, 0, 1000)
	card.Channel = ChannelCard
	if fee := mustCost(t, spec, card); fee.ElectricCents != 80 {
		t.Fatalf("card electric = %d, want the 0.8x rate", fee.ElectricCents)
	}
	if fee := mustCost(t, spec, flatUsage(beijingAt(0, 0), 60, 0, 1000)); fee.ElectricCents != 100 {
		t.Fatalf("default channel must not inherit the card multiplier")
	}
}

func TestValidateSpecRejectsIncoherentSpecs(t *testing.T) {
	cases := map[string]Spec{
		"unknown basis":        {Basis: "seat"},
		"uncovered day":        {Basis: BasisEnergy, Windows: []Window{{Start: "00:00", End: "12:00", CentsPerKWh: 1}}},
		"overlapping windows":  {Basis: BasisEnergy, Windows: []Window{{Start: "00:00", End: "12:00", CentsPerKWh: 1}, {Start: "11:00", End: "24:00", CentsPerKWh: 1}}},
		"tiers out of order":   {Basis: BasisPowerTier, Windows: allDay(1), Tiers: []Tier{{LowW: 300, HighW: 500}, {LowW: 0, HighW: 200}}},
		"tiers overlap":        {Basis: BasisPowerTier, Windows: allDay(1), Tiers: []Tier{{LowW: 0, HighW: 300}, {LowW: 200, HighW: 500}}},
		"tier on energy basis": {Basis: BasisEnergy, Windows: allDay(1), Tiers: []Tier{{LowW: 0, HighW: 200}}},
		"window on flat basis": {Basis: BasisPerMinute, PerMinuteCents: 1, Windows: allDay(1)},
		"split max power":      {Basis: BasisMaxPower, Windows: []Window{{Start: "00:00", End: "12:00", CentsPerHourPerKW: 1}, {Start: "12:00", End: "24:00", CentsPerHourPerKW: 1}}},
		"unknown service mode": {Basis: BasisEnergy, Windows: allDay(1), Service: ServiceFee{Mode: "weekly"}},
		"negative rate":        {Basis: BasisEnergy, Windows: []Window{{Start: "00:00", End: "24:00", CentsPerKWh: -1}}},
	}
	for name, spec := range cases {
		if ValidateSpec(spec) == nil {
			t.Errorf("%s: expected the spec to be rejected", name)
		}
	}
}

func TestCostRejectsUnusableUsage(t *testing.T) {
	spec := Spec{Basis: BasisEnergy, Windows: allDay(50)}
	// A session that consumed nothing is not an error: it is a full refund.
	if fee, err := Cost(spec, Usage{Start: beijingAt(0, 0), End: beijingAt(0, 0)}); err != nil || fee.TotalCents != 0 {
		t.Fatalf("an unused session must bill nothing, got %+v / %v", fee, err)
	}
	overrun := Usage{Start: beijingAt(0, 0), End: beijingAt(1, 0), EnergyWh: 100,
		Samples: []Sample{{Start: beijingAt(0, 0), End: beijingAt(1, 10), EnergyWh: 100}}}
	if _, err := Cost(spec, overrun); err == nil {
		t.Fatal("expected a sample reaching past the session to be rejected")
	}
	backwards := Usage{Start: beijingAt(0, 0), End: beijingAt(1, 0), EnergyWh: 100,
		Samples: []Sample{{Start: beijingAt(0, 30), End: beijingAt(0, 10), EnergyWh: 100}}}
	if _, err := Cost(spec, backwards); err == nil {
		t.Fatal("expected a reversed sample to be rejected")
	}
}

func TestCostAndPriceActualAgreeOnTheSameSession(t *testing.T) {
	spec := Spec{Basis: BasisEnergy, Windows: allDay(60), Service: ServiceFee{Mode: ServiceMinute, CentsPerMinute: 2}}
	start := beijingAt(9, 0)
	fee := mustCost(t, spec, flatUsage(start, 90, 0, 1500))
	actual, err := PriceActual(Rule{ID: 1, Version: 1, Spec: spec}, ActualMeter{
		StartedAt: start, EndedAt: start.Add(90 * time.Minute), ChargedWh: 1500, ChargedSeconds: 5400,
		Segments: []MeterSegment{{StartedAt: start, EndedAt: start.Add(45 * time.Minute), EnergyWh: 700},
			{StartedAt: start.Add(45 * time.Minute), EndedAt: start.Add(90 * time.Minute), EnergyWh: 800}},
	})
	if err != nil {
		t.Fatalf("PriceActual returned %v", err)
	}
	if actual.TotalCents != fee.TotalCents {
		t.Fatalf("settled %d but a quote for the same usage would be %d", actual.TotalCents, fee.TotalCents)
	}
}

func TestPriceActualRejectsContradictoryMeter(t *testing.T) {
	rule := Rule{ID: 1, Version: 1, Spec: Spec{Basis: BasisEnergy, Windows: allDay(50)}}
	start := beijingAt(9, 0)
	cases := map[string]ActualMeter{
		"energy does not add up": {StartedAt: start, EndedAt: start.Add(time.Hour), ChargedWh: 1000,
			Segments: []MeterSegment{{StartedAt: start, EndedAt: start.Add(time.Hour), EnergyWh: 900}}},
		"segments do not cover the session": {StartedAt: start, EndedAt: start.Add(time.Hour), ChargedWh: 1000,
			Segments: []MeterSegment{{StartedAt: start, EndedAt: start.Add(30 * time.Minute), EnergyWh: 1000}}},
		"gap between segments": {StartedAt: start, EndedAt: start.Add(time.Hour), ChargedWh: 1000,
			Segments: []MeterSegment{{StartedAt: start, EndedAt: start.Add(20 * time.Minute), EnergyWh: 400},
				{StartedAt: start.Add(30 * time.Minute), EndedAt: start.Add(time.Hour), EnergyWh: 600}}},
		"energy with no duration": {StartedAt: start, EndedAt: start, ChargedWh: 1000},
	}
	for name, meter := range cases {
		if _, err := PriceActual(rule, meter); err == nil {
			t.Errorf("%s: expected the meter to be sent to review", name)
		}
	}
}

func TestEstimateChargeRunsTheSameEngine(t *testing.T) {
	rule := Rule{ID: 1, StationID: 1, Version: 1,
		Spec: Spec{Basis: BasisEnergy, Windows: allDay(75), Service: ServiceFee{Mode: ServiceMinute, CentsPerMinute: 1}}}
	estimate, err := EstimateCharge(rule, "1.500", 60, beijingAt(10, 0))
	if err != nil {
		t.Fatalf("EstimateCharge returned %v", err)
	}
	if estimate.ElectricCents != 113 || estimate.ServiceCents != 60 || estimate.TotalCents != 173 {
		t.Fatalf("estimate = %+v", estimate)
	}
	if estimate.Basis != BasisEnergy {
		t.Fatalf("basis = %q", estimate.Basis)
	}
}

func TestEstimateChargeRejectsOutOfRangeInput(t *testing.T) {
	rule := Rule{ID: 1, Version: 1, Spec: Spec{Basis: BasisEnergy, Windows: allDay(75)}}
	for _, input := range []struct {
		energy  string
		minutes uint16
	}{{"", 60}, {"-1", 60}, {"101", 60}, {"1.5", 0}, {"1.5", 601}} {
		if _, err := EstimateCharge(rule, input.energy, input.minutes, beijingAt(10, 0)); err == nil {
			t.Errorf("energy %q / %d minutes: expected rejection", input.energy, input.minutes)
		}
	}
}
