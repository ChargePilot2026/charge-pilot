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
	// 设备计费的电价表不带任何费率，因此根本无从计算。引擎必须明说这一点，
	// 而不是给出一个看起来合理的数字：给一张已经付过钱的账单再造一个数字是
	// 一次对账事故，不是可以四舍五入带过的细节。
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
	// 不到午夜就结束的链，会让夜间那一段没有任何电价。
	if err := ValidateSpec(base([]Period{{EndMinute: 720, ElectricCents: 100}})); err == nil {
		t.Fatal("a chain that does not reach 1440 must be rejected")
	}
	// 两个时段用相同的结束分钟：存成起止时间对时会无声地重叠，
	// 存成链时这个不递增的值会被当场抓住。
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
	// 中间有洞的阶梯没法给落在洞里的读数定价，所以不从 0 开始的阶梯
	// 会被直接拒掉。
	if err := ValidateSpec(spec([]Tier{{MaxWatts: 1000, ElectricCents: 100}, {MaxWatts: 2000, ElectricCents: 200}, {MaxWatts: 1500, ElectricCents: 300}})); err == nil {
		t.Fatal("a ladder whose ceilings do not strictly increase must be rejected")
	}
	// 电量电价表根本没有阶梯；带一个就等于带一个没人读的字段，
	// 而总有人会相信它是生效的。
	energy := Spec{Mode: ModeServerEnergy, Electric: &ElectricLine{Basis: BasisEnergy,
		Periods: []Period{{EndMinute: 1440, ElectricCents: 100, Tiers: []Tier{{MaxWatts: 1000, ElectricCents: 200}}}}}}
	if err := ValidateSpec(energy); err == nil {
		t.Fatal("an energy tariff carrying a ladder must be rejected")
	}
}

func TestModeAndBasisMustAgree(t *testing.T) {
	// 一台按峰值功率配置的设备却挂着一份按电量计价的电价表，
	// 就会按一个谁也没同意过的口径收钱。
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
	// 超过最高档时仍按最高档的费率；最高档是上界，而不是一个一旦越过
	// 这次充电就没法计价的限额。
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
	// 正好等于某个上界时，读数仍留在下面那个更便宜的档里。
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
	// 电费按电量、服务费按小时：一种真实且常见的搭配，
	// 单一口径的模型根本表达不出来。
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
	// 已发布的费率卡只对电费暴露一个卡倍率，此外什么都没有。把它也作用到
	// 服务费那一行，等于给一张从未这么报价过的发票打上一个折扣。
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
	// 一条回归防线：均匀性判断曾经只比功率阶梯，于是每份电量电价表在一天里的
	// 每个小时看起来都是平的。跨过电价变更点的充电就会靠「各时段大概分到多少
	// 电量」这句猜测结算——这是少收，而且没有任何复核环节会发现它。
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
	// 充电用户付了 100 分，账单就是 100 分。计价引擎里没有任何东西有发言权，
	// 哪怕算出来的数正好不一样。
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
	// 服务端计费的充电从来不会拿到控制指令：它由平台计价，
	// 充电板只负责上报。
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
	// 设备计费的充电绝不会被平台提前切掉；这个决定属于充电板，
	// 和它对着干会停掉一笔平台并不欠费的充电。
	devicePlan, err := DecideStop(Spec{Mode: ModeDeviceDuration}, usage, time.Hour)
	if err != nil {
		t.Fatalf("DecideStop: %v", err)
	}
	if devicePlan.ShouldStop {
		t.Fatal("a device-billed session must never be stopped by the platform")
	}
}

func TestFirmwareLimitsAreEnforced(t *testing.T) {
	// 充电板把刷卡充电存在一个以分钟计的无符号 16 位字段里，
	// 超过这个值它会直接拒绝，而不是截断。
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

// 消费封顶只有在「上限是拿结算将会用的同一份计量试算出来的」时才值得拥有。
// 两条路径一旦不一致，充电就会比金额该有的时刻更早或更晚停止，
// 而这两个数字单看哪一个都不像错的。
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

// 封顶为 0 不是「上限是 0 分」。它的意思是运营方没有设，
// 于是充电在开始时下发给充电板的那份额度用完时结束。
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

// 设备计费的充电在自己的额度用完时由充电板结束。插手进去就是去和充电板抢
// 一次平台并不付费的充电的控制权，哪怕那份电价表恰好带着一个封顶也一样。
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

// 用一份本来就没法结算的计量去量封顶，那是留给计费复核的问题，
// 不是在充电进行到一半时切断别人充电的理由。
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
