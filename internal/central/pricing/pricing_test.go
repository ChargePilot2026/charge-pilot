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
	// 验证设备计费规则不允许调用服务端费率计算，避免报价与实收来源混用。
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
	// 时段链未覆盖至午夜时应拒绝，避免夜间缺失电价。
	if err := ValidateSpec(base([]Period{{EndMinute: 720, ElectricCents: 100}})); err == nil {
		t.Fatal("a chain that does not reach 1440 must be rejected")
	}
	// 相同结束分钟违反时段链严格递增约束。
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
	// 验证阶梯从 0 开始且连续覆盖，拒绝存在计价空档的规则。
	if err := ValidateSpec(spec([]Tier{{MaxWatts: 1000, ElectricCents: 100}, {MaxWatts: 2000, ElectricCents: 200}, {MaxWatts: 1500, ElectricCents: 300}})); err == nil {
		t.Fatal("a ladder whose ceilings do not strictly increase must be rejected")
	}
	// 电量计费不支持阶梯，配置阶梯时必须拒绝。
	energy := Spec{Mode: ModeServerEnergy, Electric: &ElectricLine{Basis: BasisEnergy,
		Periods: []Period{{EndMinute: 1440, ElectricCents: 100, Tiers: []Tier{{MaxWatts: 1000, ElectricCents: 200}}}}}}
	if err := ValidateSpec(energy); err == nil {
		t.Fatal("an energy tariff carrying a ladder must be rejected")
	}
}

func TestModeAndBasisMustAgree(t *testing.T) {
	// 最大功率模式不得使用电量电价口径。
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
	// 一小时，一半在中午前按 50 分/kWh、一半在中午后按 80 分/kWh，合计 1kWh：
	// 0.5 * 50 + 0.5 * 80 = 65 分。
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
	// 1kWh 按 200 分/kWh，读数落在 1001-2000W 那一档。
	if fee.TotalCents != 200 {
		t.Fatalf("total = %d cents, want 200", fee.TotalCents)
	}
	// 验证超过最高功率档时继续使用最高档费率。
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
	// 功率等于档位上界时仍归入该档。
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

func TestIntegratedEnergyRatesPriceIndependently(t *testing.T) {
	spec := Spec{
		Mode:     ModeServerEnergy,
		Electric: &ElectricLine{Basis: BasisEnergy, Periods: []Period{{EndMinute: 1440, ElectricCents: 100, ServiceCents: 600}}},
	}
	fee, err := Cost(spec, hourUsage(1000, 500))
	if err != nil {
		t.Fatalf("Cost: %v", err)
	}
	if fee.ElectricCents != 100 || fee.ServiceCents != 600 {
		t.Fatalf("electric = %d, service = %d; want 100 and 600", fee.ElectricCents, fee.ServiceCents)
	}
}

func TestSchemeRejectsUnconfirmedCoefficientOrder(t *testing.T) {
	s := exampleScheme(ModeServerEnergy, []Period{{EndMinute: 1440, ElectricCents: 100}}, 300)
	s.Policy.ChannelBP = 5000
	if s.Validate() == nil {
		t.Fatal("unconfirmed channel coefficients must be rejected")
	}
}

func TestUnsegmentedMeterAcrossAnEnergyTariffChangeGoesToReview(t *testing.T) {
	// 验证全天均匀性检查包含电量费率，避免跨时段变化被误判为可均摊计量。
	spec := Spec{Mode: ModeServerEnergy, Electric: &ElectricLine{Basis: BasisEnergy, Periods: []Period{
		{EndMinute: 720, ElectricCents: 50},
		{EndMinute: 1440, ElectricCents: 80},
	}}}
	start := time.Date(2026, 9, 28, 3, 30, 0, 0, time.UTC) // 11:30 in Beijing
	meter := ActualMeter{StartedAt: start, EndedAt: start.Add(time.Hour), ChargedWh: 1000, ChargedSeconds: 3600}
	if _, err := PriceActual(Rule{ID: 1, Version: 1, Spec: spec}, meter); !errors.Is(err, ErrMeterReview) {
		t.Fatalf("an unsegmented meter across an energy tariff change must go to review, got %v", err)
	}
	// 同一次充电，只要把各时段电量实测出来就正常结算。
	meter.Segments = []MeterSegment{{StartedAt: start, EndedAt: start.Add(30 * time.Minute), EnergyWh: 400}, {StartedAt: start.Add(30 * time.Minute), EndedAt: start.Add(time.Hour), EnergyWh: 600}}
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
	// 高于算出金额的封顶绝不能变成附加费。
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
	// 设备计费按实际付款 100 分结算，不使用服务端重新计算的费用。
	spec := Spec{Mode: ModeDeviceDuration}
	offer := Offer{ID: 1, StationID: 1, Name: "1元60分钟", Mode: "duration", PriceCents: 100, DurationMinutes: 60}
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
	// 设备在自己的采样栅格上停下，100 分里花掉了 96 分。这 4 分的差额必须
	// 保持可见；把它抹平，就等于让一块采样栅格太粗的充电板从此不可见。
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
	// 按时长计费的充电若还带着余额，就等于给后面某个环节留了一次
	// 再定价的机会。
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
	// 验证服务端计费不生成设备自主计费指令；设备仅执行控制和上报计量。
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
	// 设备计费由设备控制结束，不触发平台费用停机。
	devicePlan, err := DecideStop(Spec{Mode: ModeDeviceDuration}, usage, time.Hour)
	if err != nil {
		t.Fatalf("DecideStop: %v", err)
	}
	if devicePlan.ShouldStop {
		t.Fatal("a device-billed session must never be stopped by the platform")
	}
}

func TestFirmwareLimitsAreEnforced(t *testing.T) {
	// 验证刷卡时长不能超过协议 uint16 分钟字段上限，不进行截断。
	spec := energySpec()
	spec.CardMaxMinutes = 4321
	if err := ValidateSpec(spec); err == nil {
		t.Fatal("a card session longer than the firmware field must be rejected")
	}
	spec.CardMaxMinutes = 4320
	if err := ValidateSpec(spec); err != nil {
		t.Fatalf("72-hour scheme ceiling must be accepted before device capability checking: %v", err)
	}
}

// 费用上限判断与最终结算必须使用相同计量和计价口径。
func TestStopAtMeterUsesTheSameMeterValidationAsSettlement(t *testing.T) {
	spec := energySpec()
	spec.SpendCapCents = 150
	rule := Rule{ID: 7, Version: 2, Spec: spec}
	start := time.Date(2026, 1, 2, 10, 0, 0, 0, beijing)

	// 1.00 元/kWh。两小时 2kWh 是 200 分，越过了 150 分的封顶。
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

// 验证消费封顶为 0 表示未配置上限；设备仍受下发时长或电量额度限制。
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

// 设备计费由设备自身额度控制结束，即使费率配置了封顶金额也不触发平台停机。
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

// 验证无效计量不触发消费封顶停机，交由计费核实流程处理。
func TestStopAtMeterSendsAnUnsoundMeterToReviewRatherThanStopping(t *testing.T) {
	spec := energySpec()
	spec.SpendCapCents = 1
	rule := Rule{ID: 7, Version: 2, Spec: spec}
	start := time.Date(2026, 1, 2, 10, 0, 0, 0, beijing)
	// 充电时长比整次充电还长，真实充电不可能出现这种情况。
	meter := ActualMeter{StartedAt: start, EndedAt: start.Add(time.Hour), ChargedWh: 5000, ChargedSeconds: 9999}
	if _, err := StopAtMeter(rule, meter); !errors.Is(err, ErrMeterReview) {
		t.Fatalf("err = %v, want ErrMeterReview", err)
	}
}
