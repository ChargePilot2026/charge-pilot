package pricing

import (
	"errors"
	"testing"
	"time"
)

func energySpec() Spec {
	return Spec{
		Mode:     ModeServerEnergy,
		Electric: &ElectricLine{Basis: BasisEnergy, Periods: []Period{{EndMinute: 1440, ElectricCents: 100}}},
	}
}

func realtimeSpec() Spec {
	return Spec{
		Mode: ModeServerRealtimePower,
		Electric: &ElectricLine{Basis: BasisRealtimePower, Periods: []Period{{
			EndMinute: 1440,
			Tiers:     []Tier{{MaxWatts: 1000, ElectricCents: 100}, {MaxWatts: 2000, ElectricCents: 200}},
		}}},
		TierPriceBasis: TierPerKWh,
	}
}

func hourUsage(wh uint64, watts uint32) Usage {
	start := time.Date(2026, 1, 2, 10, 0, 0, 0, beijing)
	return Usage{
		Start: start, End: start.Add(time.Hour), EnergyWh: wh,
		Samples: []Sample{{Start: start, End: start.Add(time.Hour), EnergyWh: wh, PowerW: watts}},
	}
}

func TestChargeModeExecutorSplit(t *testing.T) {
	cases := map[ChargeMode]ChargeExecutor{
		ModeServerRealtimePower: ExecutorServer,
		ModeServerMaxPower:      ExecutorServer,
		ModeServerEnergy:        ExecutorServer,
		ModeDeviceDuration:      ExecutorDevice,
		ModeDeviceEnergy:        ExecutorDevice,
		ModeDevicePower:         ExecutorDevice,
	}
	for mode, want := range cases {
		if got := mode.Executor(); got != want {
			t.Fatalf("mode %q executor = %q, want %q", mode, got, want)
		}
	}
	if ChargeMode("nonsense").Valid() {
		t.Fatal("an unknown mode must not be valid")
	}
}

func TestCostRefusesDeviceBilledModes(t *testing.T) {
	// A device-billed tariff has no rates, so there is nothing to compute. The
	// engine must say so rather than return a plausible number: a second
	// figure for an already-paid bill is a reconciliation incident, not a
	// rounding detail.
	for _, mode := range []ChargeMode{ModeDeviceDuration, ModeDeviceEnergy, ModeDevicePower} {
		spec := Spec{Mode: mode}
		if err := ValidateSpec(spec); err != nil {
			t.Fatalf("mode %q should be a valid spec: %v", mode, err)
		}
		if _, err := Cost(spec, hourUsage(1000, 100)); !errors.Is(err, ErrNotServerBilled) {
			t.Fatalf("mode %q: Cost returned %v, want ErrNotServerBilled", mode, err)
		}
	}
}

func TestDeviceBilledSpecRejectsRates(t *testing.T) {
	spec := Spec{Mode: ModeDeviceDuration, Electric: &ElectricLine{Basis: BasisEnergy, Periods: []Period{{EndMinute: 1440, ElectricCents: 100}}}}
	if err := ValidateSpec(spec); err == nil {
		t.Fatal("a device-billed tariff carrying an electricity rate must be rejected")
	}
}

func TestPeriodChainInvariants(t *testing.T) {
	base := func(periods []Period) Spec {
		return Spec{Mode: ModeServerEnergy, Electric: &ElectricLine{Basis: BasisEnergy, Periods: periods}}
	}
	if err := ValidateSpec(base([]Period{{EndMinute: 1440, ElectricCents: 100}})); err != nil {
		t.Fatalf("a single all-day period must be valid: %v", err)
	}
	// A chain that stops short of midnight leaves the night unpriced.
	if err := ValidateSpec(base([]Period{{EndMinute: 720, ElectricCents: 100}})); err == nil {
		t.Fatal("a chain that does not reach 1440 must be rejected")
	}
	// Two periods with the same end minute overlap silently if stored as pairs;
	// as a chain the non-increasing value is caught.
	if err := ValidateSpec(base([]Period{{EndMinute: 720, ElectricCents: 100}, {EndMinute: 720, ElectricCents: 200}})); err == nil {
		t.Fatal("a non-increasing chain must be rejected")
	}
}

func TestTierChainInvariants(t *testing.T) {
	spec := func(tiers []Tier) Spec {
		return Spec{Mode: ModeServerRealtimePower,
			Electric:       &ElectricLine{Basis: BasisRealtimePower, Periods: []Period{{EndMinute: 1440, Tiers: tiers}}},
			TierPriceBasis: TierPerKWh}
	}
	if err := ValidateSpec(spec([]Tier{{MaxWatts: 1000, ElectricCents: 100}})); err != nil {
		t.Fatalf("a single rung must be valid: %v", err)
	}
	// A ladder with a hole in it cannot price a reading that lands in the gap,
	// so a ladder that does not start at zero is rejected outright.
	if err := ValidateSpec(spec([]Tier{{MaxWatts: 1000, ElectricCents: 100}, {MaxWatts: 2000, ElectricCents: 200}, {MaxWatts: 1500, ElectricCents: 300}})); err == nil {
		t.Fatal("a ladder whose ceilings do not strictly increase must be rejected")
	}
	// An energy tariff has no ladder at all; carrying one is a field nothing
	// reads, and someone will eventually believe it.
	energy := Spec{Mode: ModeServerEnergy, Electric: &ElectricLine{Basis: BasisEnergy,
		Periods: []Period{{EndMinute: 1440, ElectricCents: 100, Tiers: []Tier{{MaxWatts: 1000, ElectricCents: 200}}}}}}
	if err := ValidateSpec(energy); err == nil {
		t.Fatal("an energy tariff carrying a ladder must be rejected")
	}
}

func TestModeAndBasisMustAgree(t *testing.T) {
	// A device configured for peak power but holding an energy tariff would
	// bill by something nobody agreed to.
	spec := Spec{Mode: ModeServerMaxPower, Electric: &ElectricLine{Basis: BasisEnergy, Periods: []Period{{EndMinute: 1440, ElectricCents: 100}}}}
	if err := ValidateSpec(spec); err == nil {
		t.Fatal("a mode that disagrees with its own basis must be rejected")
	}
}

func TestCostEnergyAcrossTwoPeriods(t *testing.T) {
	spec := Spec{Mode: ModeServerEnergy, Electric: &ElectricLine{Basis: BasisEnergy, Periods: []Period{
		{EndMinute: 12 * 60, ElectricCents: 50},
		{EndMinute: 1440, ElectricCents: 80},
	}}}
	start := time.Date(2026, 1, 2, 11, 0, 0, 0, beijing)
	// One hour, half before noon at 50c/kWh and half after at 80c/kWh, 1kWh
	// total: 0.5 * 50 + 0.5 * 80 = 65 cents.
	usage := Usage{Start: start, End: start.Add(2 * time.Hour), EnergyWh: 1000,
		Samples: []Sample{
			{Start: start, End: start.Add(time.Hour), EnergyWh: 500, PowerW: 500},
			{Start: start.Add(time.Hour), End: start.Add(2 * time.Hour), EnergyWh: 500, PowerW: 500},
		}}
	fee, err := Cost(spec, usage)
	if err != nil {
		t.Fatalf("Cost: %v", err)
	}
	if fee.TotalCents != 65 {
		t.Fatalf("total = %d cents, want 65", fee.TotalCents)
	}
}

func TestCostRealtimePowerUsesTheRungTheReadingFallsIn(t *testing.T) {
	spec := realtimeSpec()
	fee, err := Cost(spec, hourUsage(1000, 1500))
	if err != nil {
		t.Fatalf("Cost: %v", err)
	}
	// 1kWh at 200c/kWh for a reading inside the 1001-2000W rung.
	if fee.TotalCents != 200 {
		t.Fatalf("total = %d cents, want 200", fee.TotalCents)
	}
	// Above the top rung the top rate still applies; the rung is a ceiling, not
	// a limit beyond which the session becomes unpriceable.
	fee, err = Cost(spec, hourUsage(1000, 5000))
	if err != nil {
		t.Fatalf("Cost above the top rung: %v", err)
	}
	if fee.TotalCents != 200 {
		t.Fatalf("total above top rung = %d cents, want 200", fee.TotalCents)
	}
}

func TestCostRealtimePowerRungBoundaryIsInclusiveBelow(t *testing.T) {
	spec := realtimeSpec()
	// Exactly on a ceiling the reading stays in the cheaper rung below it.
	fee, err := Cost(spec, hourUsage(1000, 2000))
	if err != nil {
		t.Fatalf("Cost: %v", err)
	}
	if fee.TotalCents != 200 {
		t.Fatalf("total = %d cents, want 200 (the 1001-2000W rung)", fee.TotalCents)
	}
	fee, err = Cost(spec, hourUsage(1000, 2001))
	if err != nil {
		t.Fatalf("Cost: %v", err)
	}
	if fee.TotalCents != 200 {
		t.Fatalf("total = %d cents, want 200 (above the top rung still pays the top rate)", fee.TotalCents)
	}
}

func TestTwoLinesPriceIndependently(t *testing.T) {
	// Electricity by energy, service by the hour: a real and common pairing that
	// a single-basis model cannot express.
	spec := Spec{
		Mode:     ModeServerEnergy,
		Electric: &ElectricLine{Basis: BasisEnergy, Periods: []Period{{EndMinute: 1440, ElectricCents: 100}}},
		Service:  &ServiceLine{Basis: ServiceMinute, CentsPerMinute: 10},
	}
	fee, err := Cost(spec, hourUsage(1000, 500))
	if err != nil {
		t.Fatalf("Cost: %v", err)
	}
	if fee.ElectricCents != 100 || fee.ServiceCents != 600 {
		t.Fatalf("electric = %d, service = %d; want 100 and 600", fee.ElectricCents, fee.ServiceCents)
	}
}

func TestChannelMultiplierAppliesToElectricityOnly(t *testing.T) {
	// The published rate card exposes a card multiplier against electricity and
	// nothing else. Applying it to the service line would put a discount on an
	// invoice that never quoted one.
	spec := Spec{
		Mode:       ModeServerEnergy,
		Electric:   &ElectricLine{Basis: BasisEnergy, Periods: []Period{{EndMinute: 1440, ElectricCents: 100}}},
		Service:    &ServiceLine{Basis: ServiceMinute, CentsPerMinute: 10},
		Multiplier: &ChannelMultiplier{CardBP: 5000},
	}
	usage := hourUsage(1000, 500)
	usage.Channel = ChannelCard
	fee, err := Cost(spec, usage)
	if err != nil {
		t.Fatalf("Cost: %v", err)
	}
	if fee.ElectricCents != 50 {
		t.Fatalf("electric = %d, want 50 after a half-rate card multiplier", fee.ElectricCents)
	}
	if fee.ServiceCents != 600 {
		t.Fatalf("service = %d, want 600 untouched by the card multiplier", fee.ServiceCents)
	}
}

func TestUnsegmentedMeterAcrossAnEnergyTariffChangeGoesToReview(t *testing.T) {
	// A regression guard: the uniformity check once compared only the power
	// ladder, so an energy tariff looked flat at every hour of the day. A
	// session that spanned a rate change would then have been settled on a
	// guess about how much energy fell in each period — under-billing, with no
	// review to catch it.
	spec := Spec{Mode: ModeServerEnergy, Electric: &ElectricLine{Basis: BasisEnergy, Periods: []Period{
		{EndMinute: 720, ElectricCents: 50},
		{EndMinute: 1440, ElectricCents: 80},
	}}}
	start := time.Date(2026, 9, 28, 3, 30, 0, 0, time.UTC) // 11:30 in Beijing
	meter := ActualMeter{StartedAt: start, EndedAt: start.Add(time.Hour), ChargedWh: 1000, ChargedSeconds: 3600}
	if _, err := PriceActual(Rule{ID: 1, Version: 1, Spec: spec}, meter); !errors.Is(err, ErrMeterReview) {
		t.Fatalf("an unsegmented meter across an energy tariff change must go to review, got %v", err)
	}
	// The same session with the rates measured settles normally.
	meter.Segments = []MeterSegment{{StartedAt: start, EndedAt: start.Add(time.Hour), EnergyWh: 1000, PeakW: 500}}
	if _, err := PriceActual(Rule{ID: 1, Version: 1, Spec: spec}, meter); err != nil {
		t.Fatalf("a measured session must settle: %v", err)
	}
}

func TestSettleServerBilledTakesItsMoneyFromCost(t *testing.T) {
	spec := energySpec()
	meter := ActualMeter{
		StartedAt: time.Date(2026, 1, 2, 10, 0, 0, 0, beijing),
		EndedAt:   time.Date(2026, 1, 2, 11, 0, 0, 0, beijing),
		ChargedWh: 1000, ChargedSeconds: 3600,
		Segments: []MeterSegment{{StartedAt: time.Date(2026, 1, 2, 10, 0, 0, 0, beijing),
			EndedAt: time.Date(2026, 1, 2, 11, 0, 0, 0, beijing), EnergyWh: 1000, PeakW: 500}},
	}
	settlement, err := SettleSession(spec, meter, nil, ActualFromMeter(meter))
	if err != nil {
		t.Fatalf("SettleSession: %v", err)
	}
	if settlement.Executor != string(ExecutorServer) || settlement.TotalCents != 100 {
		t.Fatalf("settlement = %+v, want a server settlement of 100 cents", settlement)
	}
}

func TestSettleServerBilledCapOnlyEverLowers(t *testing.T) {
	spec := energySpec()
	meter := ActualMeter{
		StartedAt: time.Date(2026, 1, 2, 10, 0, 0, 0, beijing),
		EndedAt:   time.Date(2026, 1, 2, 11, 0, 0, 0, beijing),
		ChargedWh: 1000, ChargedSeconds: 3600,
		Segments: []MeterSegment{{StartedAt: time.Date(2026, 1, 2, 10, 0, 0, 0, beijing),
			EndedAt: time.Date(2026, 1, 2, 11, 0, 0, 0, beijing), EnergyWh: 1000, PeakW: 500}},
	}
	cap := Offer{ID: 1, StationID: 1, Name: "cap", Mode: "amount", PriceCents: 50}
	settlement, err := SettleSession(spec, meter, &cap, ActualFromMeter(meter))
	if err != nil {
		t.Fatalf("SettleSession: %v", err)
	}
	if settlement.TotalCents != 50 {
		t.Fatalf("total = %d, want the cap of 50", settlement.TotalCents)
	}
	// A cap above the computed fee must not become a surcharge.
	cap.PriceCents = 5000
	settlement, err = SettleSession(spec, meter, &cap, ActualFromMeter(meter))
	if err != nil {
		t.Fatalf("SettleSession: %v", err)
	}
	if settlement.TotalCents != 100 {
		t.Fatalf("total = %d, want the computed 100, never the cap", settlement.TotalCents)
	}
}

func TestSettleDeviceBilledTakesItsMoneyFromWhatWasPaid(t *testing.T) {
	// The rider paid 100 cents. The bill is 100 cents. Nothing in the pricing
	// engine gets a vote.
	spec := Spec{Mode: ModeDeviceDuration}
	offer := Offer{ID: 1, StationID: 1, Name: "1元60分钟", Mode: "package", PriceCents: 100, DurationMinutes: 60}
	meter := ActualMeter{StartedAt: time.Now(), EndedAt: time.Now().Add(time.Hour), ChargedWh: 5000, ChargedSeconds: 3600}
	settlement, err := SettleSession(spec, meter, &offer, &SessionActual{UsedSeconds: 3600, Reported: true, StopReason: StopExhaustedTime})
	if err != nil {
		t.Fatalf("SettleSession: %v", err)
	}
	if settlement.TotalCents != 100 || settlement.Executor != string(ExecutorDevice) {
		t.Fatalf("settlement = %+v, want a device settlement of 100 cents", settlement)
	}
	if settlement.ElectricCents != 0 {
		t.Fatalf("a device-billed settlement must not carry an electricity split: %d", settlement.ElectricCents)
	}
}

func TestSettleDeviceBilledWithoutPaymentIsRefused(t *testing.T) {
	spec := Spec{Mode: ModeDeviceDuration}
	meter := ActualMeter{StartedAt: time.Now(), EndedAt: time.Now().Add(time.Hour), ChargedWh: 100, ChargedSeconds: 60}
	if _, err := SettleSession(spec, meter, nil, nil); !errors.Is(err, ErrNoPaidAmount) {
		t.Fatalf("err = %v, want ErrNoPaidAmount", err)
	}
}

func TestDevicePowerResidualIsReportedNotWrittenOff(t *testing.T) {
	spec := Spec{Mode: ModeDevicePower}
	offer := Offer{ID: 1, StationID: 1, Name: "1元", Mode: "amount", PriceCents: 100}
	// The device stopped on its sampling grid and spent 96 of the 100 it was
	// given. The 4-cent difference has to stay visible; rounding it away is how
	// a board with too coarse a grid becomes invisible.
	settlement, err := SettleSession(spec, ActualMeter{}, &offer, &SessionActual{SpentCents: 96, Reported: true})
	if err != nil {
		t.Fatalf("SettleSession: %v", err)
	}
	if settlement.TotalCents != 100 || settlement.ResidualCents != 4 {
		t.Fatalf("settlement = %+v, want total 100 with a residual of 4", settlement)
	}
}

func TestControlInstructionValidPerMode(t *testing.T) {
	if !(ControlInstruction{Mode: ModeDeviceDuration, Minutes: 60}).Valid() {
		t.Fatal("a duration instruction with a span must be valid")
	}
	// A duration session that also carries a balance would let something later
	// price it a second way.
	if (ControlInstruction{Mode: ModeDeviceDuration, Minutes: 60, BalanceCents: 100}).Valid() {
		t.Fatal("a duration instruction carrying a balance must be invalid")
	}
	if !(ControlInstruction{Mode: ModeDeviceEnergy, EnergyMilliWh: 5000}).Valid() {
		t.Fatal("an energy instruction with a quota must be valid")
	}
	if !(ControlInstruction{Mode: ModeDevicePower, BalanceCents: 100, TierCentsPerHour: []int64{100, 200}}).Valid() {
		t.Fatal("a power instruction with a balance and a ladder must be valid")
	}
	if (ControlInstruction{Mode: ModeDevicePower, BalanceCents: 100}).Valid() {
		t.Fatal("a power instruction with no ladder must be invalid")
	}
	// A server-billed session is never handed a control instruction: the
	// platform prices it, the board only reports.
	if (ControlInstruction{Mode: ModeServerEnergy, Minutes: 60}).Valid() {
		t.Fatal("a server-billed instruction must be invalid")
	}
}

func TestDecideStopOnlyFiresUnderACap(t *testing.T) {
	usage := hourUsage(1000, 500)
	spec := energySpec()
	plan, err := DecideStop(spec, usage, time.Hour)
	if err != nil {
		t.Fatalf("DecideStop: %v", err)
	}
	if plan.ShouldStop {
		t.Fatal("with no cap configured the session must run to the granted allowance")
	}
	spec.SpendCapCents = 100
	plan, err = DecideStop(spec, usage, time.Hour)
	if err != nil {
		t.Fatalf("DecideStop: %v", err)
	}
	if !plan.ShouldStop || plan.AccruedCents != 100 {
		t.Fatalf("plan = %+v, want a stop at 100 cents", plan)
	}
	// A device-billed session is never cut short by the platform; the board
	// owns that decision and fighting it would stop a charge nothing is owed for.
	devicePlan, err := DecideStop(Spec{Mode: ModeDeviceDuration}, usage, time.Hour)
	if err != nil {
		t.Fatalf("DecideStop: %v", err)
	}
	if devicePlan.ShouldStop {
		t.Fatal("a device-billed session must never be stopped by the platform")
	}
}

func TestFirmwareLimitsAreEnforced(t *testing.T) {
	// The board stores a card session in one unsigned 16-bit field counted in
	// minutes, and rejects a value above this rather than truncating it.
	spec := energySpec()
	spec.CardMaxMinutes = 1000
	if err := ValidateSpec(spec); err == nil {
		t.Fatal("a card session longer than the firmware field must be rejected")
	}
	spec.CardMaxMinutes = 999
	if err := ValidateSpec(spec); err != nil {
		t.Fatalf("999 minutes must be accepted: %v", err)
	}
}

// The spend cap is only worth having if the ceiling is tested against the same
// meter the settlement would use. If the two paths disagree, the session stops
// either earlier or later than the money says it should, and neither number
// looks wrong on its own.
func TestStopAtMeterUsesTheSameMeterValidationAsSettlement(t *testing.T) {
	spec := energySpec()
	spec.SpendCapCents = 150
	rule := Rule{ID: 7, Version: 2, Spec: spec}
	start := time.Date(2026, 1, 2, 10, 0, 0, 0, beijing)

	// 1.00 元/kWh. Two hours at 1kWh is 200 分, over the 150 分 cap.
	meter := ActualMeter{StartedAt: start, EndedAt: start.Add(2 * time.Hour), ChargedWh: 2000, ChargedSeconds: 7200}
	plan, err := StopAtMeter(rule, meter)
	if err != nil {
		t.Fatalf("StopAtMeter: %v", err)
	}
	if !plan.ShouldStop {
		t.Fatalf("a 200 分 session did not trip a 150 分 cap: %+v", plan)
	}
	if plan.AccruedCents != 200 {
		t.Fatalf("accrued = %d, want 200", plan.AccruedCents)
	}
	fee, err := PriceActual(rule, meter)
	if err != nil {
		t.Fatalf("PriceActual: %v", err)
	}
	if fee.TotalCents != plan.AccruedCents {
		t.Fatalf("cap tested on %d but settlement would be %d", plan.AccruedCents, fee.TotalCents)
	}
}

// A cap of zero is not a cap of zero cents. It means the operator did not set
// one, and the session then ends on the allowance the board was given at start.
func TestStopAtMeterWithoutACapNeverStops(t *testing.T) {
	rule := Rule{ID: 7, Version: 2, Spec: energySpec()}
	start := time.Date(2026, 1, 2, 10, 0, 0, 0, beijing)
	meter := ActualMeter{StartedAt: start, EndedAt: start.Add(2 * time.Hour), ChargedWh: 2000, ChargedSeconds: 7200}
	plan, err := StopAtMeter(rule, meter)
	if err != nil {
		t.Fatalf("StopAtMeter: %v", err)
	}
	if plan.ShouldStop {
		t.Fatal("a session was stopped by a cap that was never set")
	}
	if plan.Reason == "" {
		t.Fatal("no reason was given, so the log cannot say why the session continued")
	}
}

// A device-billed session is ended by the board when its own allowance runs
// out. Intervening would fight the board for control of a session the platform
// is not paying for, even if the tariff happens to carry a cap.
func TestStopAtMeterLeavesDeviceBilledSessionsToTheDevice(t *testing.T) {
	spec := Spec{
		Mode:          ModeDeviceDuration,
		SpendCapCents: 1,
	}
	if err := ValidateSpec(spec); err != nil {
		t.Fatalf("spec should be valid: %v", err)
	}
	plan, err := StopAtMeter(Rule{ID: 7, Version: 2, Spec: spec}, ActualMeter{
		StartedAt: time.Date(2026, 1, 2, 10, 0, 0, 0, beijing),
		EndedAt:   time.Date(2026, 1, 2, 11, 0, 0, 0, beijing),
	})
	if err != nil {
		t.Fatalf("StopAtMeter: %v", err)
	}
	if plan.ShouldStop {
		t.Fatal("the platform cut off a session the device is paying for")
	}
}

// A cap measured on a meter that could not have been settled is a question for
// the billing review, not a reason to cut somebody's charge off mid-session.
func TestStopAtMeterSendsAnUnsoundMeterToReviewRatherThanStopping(t *testing.T) {
	spec := energySpec()
	spec.SpendCapCents = 1
	rule := Rule{ID: 7, Version: 2, Spec: spec}
	start := time.Date(2026, 1, 2, 10, 0, 0, 0, beijing)
	// The charged time is longer than the session, which no real session can be.
	meter := ActualMeter{StartedAt: start, EndedAt: start.Add(time.Hour), ChargedWh: 5000, ChargedSeconds: 9999}
	if _, err := StopAtMeter(rule, meter); !errors.Is(err, ErrMeterReview) {
		t.Fatalf("err = %v, want ErrMeterReview", err)
	}
}
