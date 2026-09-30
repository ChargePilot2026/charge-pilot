package pricing

import (
	"math"
	"time"

	"github.com/shopspring/decimal"
)

const (
	// 每分钟一片，让连续一周的充电也有上界，同时又足够细，不会把费率时段边界
	// 近似掉。
	maxSlices     = 7 * 24 * 60
	maxLossRateBP = 100000
)

// Cost 是服务端唯一的一套计价实现。预估与结算都把同一个 Usage 喂进这个函数，
// 所以报价永远不会用到与最终账单不同的算术。
//
// 它直接拒绝设备计费模式。那种模式下钱在支付时已经收走、由设备花下去；
// 在这里重算一遍，等于给一张已经付过钱的账单造出第二个、还不一样的数字，
// 而出现第二个数字就是一次对账事故。
func Cost(spec Spec, usage Usage) (Fee, error) {
	if ValidateSpec(spec) != nil {
		return Fee{}, ErrInvalidPricing
	}
	if !spec.Mode.ServerBilled() {
		return Fee{}, ErrNotServerBilled
	}
	if usage.Start.IsZero() || usage.End.Before(usage.Start) || usage.End.Sub(usage.Start) > 7*24*time.Hour {
		return Fee{}, ErrInvalidPricing
	}
	fee := Fee{Basis: spec.Electric.Basis}
	// 全程没有取过电的充电不收钱。没被用掉的预付套餐就落在这里，
	// 必须原路全额退回。
	if usage.EnergyWh == 0 {
		return fee, nil
	}
	// 免单时段是整单免掉而不是打折，所以它在查任何费率之前就短路返回。
	if spec.FreeMinutes > 0 && usage.minutes() <= int64(spec.FreeMinutes) {
		return fee, nil
	}
	channel := usage.channel(spec)
	billable := applyLossRate(usage.EnergyWh, spec.LossRateBP)
	// 线损以精确的小数系数作用在每一片上。如果先用整数取整去缩放每一片，
	// 一次长充电会因为计量按每分钟切成一片而在每一片上多收一分。
	lossFactor := decimal.NewFromInt(int64(10000 + clampLossRate(spec.LossRateBP))).Div(decimal.NewFromInt(10000))
	serviceBasis := serviceBasisOf(spec)

	electric := decimal.Zero
	service := decimal.Zero

	switch spec.Electric.Basis {
	case BasisEnergy:
		// 电量电价表没有阶梯：同一时段内的每一片都按该时段的单一电价计费。
		slices, err := splitByPeriod(usage, spec)
		if err != nil {
			return Fee{}, ErrInvalidPricing
		}
		for _, slice := range slices {
			period := periodAt(spec, slice.Start)
			kwh := decimal.New(int64(slice.EnergyWh), -3).Mul(lossFactor)
			electric = electric.Add(kwh.Mul(decimal.NewFromInt(period.ElectricCents)))
			if serviceBasis == ServiceEnergy {
				service = service.Add(kwh.Mul(decimal.NewFromInt(spec.Service.CentsPerKWh)))
			}
		}
	case BasisRealtimePower:
		slices, err := splitByPeriod(usage, spec)
		if err != nil {
			return Fee{}, ErrInvalidPricing
		}
		for _, slice := range slices {
			period := periodAt(spec, slice.Start)
			tier := tierFor(period, effectivePower(slice))
			kwh := decimal.New(int64(slice.EnergyWh), -3).Mul(lossFactor)
			electric = electric.Add(kwh.Mul(decimal.NewFromInt(tierCentsPerKWh(tier, spec.TierPriceBasis))))
			switch serviceBasis {
			case ServiceEnergy:
				service = service.Add(kwh.Mul(decimal.NewFromInt(spec.Service.CentsPerKWh)))
			case ServiceMinutePower:
				service = service.Add(kwh.Mul(decimal.NewFromInt(tier.ServiceCents)))
			}
		}
	case BasisMaxPower:
		// 峰值功率计费把整次充电按其峰值达到的那一档来计价，所以这一档存的数字
		// 是「每小时的分数」，直接乘以小时数即可。
		// 在这里把它换算成每 kWh 的等价单价是错误的操作：那种换算只有在费率
		// 作用于一整份电量时才讲得通。
		peak := uint32(0)
		for _, sample := range usage.Samples {
			if power := effectivePower(sample); power > peak {
				peak = power
			}
		}
		if peak == 0 {
			return Fee{}, ErrInvalidPricing
		}
		period := periodAt(spec, usage.Start)
		tier := tierFor(period, peak)
		hours := decimal.NewFromInt(int64(usage.End.Sub(usage.Start))).Div(decimal.NewFromInt(int64(time.Hour)))
		electric = decimal.NewFromInt(tier.ElectricCents).Mul(hours)
		switch serviceBasis {
		case ServiceMinutePower:
			service = decimal.NewFromInt(tier.ServiceCents).Mul(hours)
		case ServiceEnergy:
			// 峰值功率电价表下的按电量服务费仍然需要一份电量，所以它按计量到的
			// 电量总量计价，而不是按峰值。
			kwh := decimal.New(int64(usage.EnergyWh), -3).Mul(lossFactor)
			service = kwh.Mul(decimal.NewFromInt(spec.Service.CentsPerKWh))
		}
	}

	switch serviceBasis {
	case ServiceMinute:
		service = decimal.NewFromInt(spec.Service.CentsPerMinute).Mul(decimal.NewFromInt(usage.minutes()))
	case ServiceSession:
		service = decimal.NewFromInt(spec.Service.CentsPerSession)
	case ServiceNone, ServiceEnergy, ServiceMinutePower:
		// 上面已经累加过了，或者这里本来就什么都不要收。
	}

	// 卡与临时费率卡只针对电费发布——真实后台把它放在电价那一节下，
	// 也从来不把它作用到服务费那一行。所以服务费总额就按算出来的值用。
	electric = electric.Mul(decimal.NewFromInt(int64(spec.Multiplier.electricBP(channel)))).Div(decimal.NewFromInt(10000))

	electricCents, ok := toCents(electric)
	if !ok {
		return Fee{}, ErrInvalidPricing
	}
	serviceCents, ok := toCents(service)
	if !ok {
		return Fee{}, ErrInvalidPricing
	}
	// 兜底只补电费一项。像过去那样按总额兜底，会开出服务费与已发布电价表
	// 毫无关系的发票。
	if electricCents < spec.MinElectricCents {
		electricCents = spec.MinElectricCents
	}
	if electricCents > math.MaxInt64-serviceCents {
		return Fee{}, ErrInvalidPricing
	}
	fee.ElectricCents = electricCents
	fee.ServiceCents = serviceCents
	fee.TotalCents = electricCents + serviceCents
	fee.BillableWh = billable
	return fee, nil
}

func serviceBasisOf(spec Spec) ServiceBasis {
	if spec.Service == nil {
		return ServiceNone
	}
	return spec.Service.Basis
}

// tierFor 返回某个读数落在哪一档。读数超过最高档时仍按最高档付费：最高档是上界，
// 行业惯例也是把超出部分算进最后一档，所以这里是兜底而不是报错。
func tierFor(period Period, watts uint32) Tier {
	lower := uint32(0)
	for _, tier := range period.Tiers {
		if watts >= lower && watts <= uint32(tier.MaxWatts) {
			return tier
		}
		lower = uint32(tier.MaxWatts) + 1
	}
	return period.Tiers[len(period.Tiers)-1]
}

// splitByPeriod 在每个整分钟边界处切开采样，使返回的每一片都完全落在同一个
// 费率时段内。
// 功率是每个采样的平均值，因此原样带过去；电量则被切开，余数落在
// 最后一片上，保证各部分之和永远等于总量。
func splitByPeriod(usage Usage, spec Spec) ([]Sample, error) {
	if _, err := compilePeriods(spec.Electric.Periods); err != nil {
		return nil, err
	}
	source := usage.Samples
	if len(source) == 0 {
		if !usage.End.After(usage.Start) {
			return nil, ErrInvalidPricing
		}
		source = []Sample{{Start: usage.Start, End: usage.End, EnergyWh: usage.EnergyWh}}
	}
	slices := make([]Sample, 0, len(source))
	for _, sample := range source {
		if !sample.Start.Before(sample.End) || sample.Start.Before(usage.Start) || sample.End.After(usage.End) {
			return nil, ErrInvalidPricing
		}
		duration := sample.End.Sub(sample.Start)
		minutes := int(duration / time.Minute)
		if minutes <= 0 {
			if sample.EnergyWh > 0 {
				return nil, ErrInvalidPricing
			}
			continue
		}
		if len(slices)+minutes > maxSlices {
			return nil, ErrInvalidPricing
		}
		base := sample.EnergyWh / uint64(minutes)
		remainder := sample.EnergyWh % uint64(minutes)
		at := sample.Start
		for i := 0; i < minutes; i++ {
			next := at.Add(time.Minute)
			energy := base
			if uint64(i) < remainder {
				energy++
			}
			// 不足一分钟的尾巴被折进最后一片，这样不会有电量被悄悄从账单里丢掉。
			if next.After(sample.End) {
				next = sample.End
			}
			slices = append(slices, Sample{Start: at, End: next, EnergyWh: energy, PowerW: sample.PowerW})
			at = next
			if !at.Before(sample.End) {
				break
			}
		}
	}
	if len(slices) == 0 {
		return nil, ErrInvalidPricing
	}
	return slices, nil
}

func periodAt(spec Spec, at time.Time) Period {
	schedule, err := compilePeriods(spec.Electric.Periods)
	if err != nil {
		return Period{}
	}
	local := at.In(beijing)
	return schedule[local.Hour()*60+local.Minute()]
}

func effectivePower(sample Sample) uint32 {
	if sample.PowerW > 0 {
		return sample.PowerW
	}
	seconds := sample.End.Sub(sample.Start).Seconds()
	if seconds <= 0 {
		return 0
	}
	// 四舍五入到最接近的整数瓦，免得反推出的功率刚好卡在某档上界之下，
	// 而它其实已经达到那个上界。
	return uint32(math.Round(float64(sample.EnergyWh) * 3600 / seconds))
}

func clampLossRate(bp int32) int32 {
	if bp < 0 {
		return 0
	}
	if bp > maxLossRateBP {
		return maxLossRateBP
	}
	return bp
}

func applyLossRate(wh uint64, bp int32) uint64 {
	bp = clampLossRate(bp)
	if bp == 0 {
		return wh
	}
	scaled := decimal.NewFromInt(int64(wh)).Mul(decimal.NewFromInt(int64(10000 + bp))).Div(decimal.NewFromInt(10000))
	if !scaled.IsInteger() {
		scaled = scaled.Ceil()
	}
	if scaled.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
		return math.MaxUint64
	}
	return uint64(scaled.IntPart())
}

// findTier 返回包含 watts 的那一档。各档是首尾相接的闭区间——下一档从比这一档
// 上界高一瓦处开始——所以刚好等于某个上界的读数仍然留在下面那个更便宜的档里。
func findTier(tiers []Tier, watts uint32) (Tier, bool) {
	lower := uint32(0)
	for _, tier := range tiers {
		if watts >= lower && watts <= uint32(tier.MaxWatts) {
			return tier, true
		}
		lower = uint32(tier.MaxWatts) + 1
	}
	return Tier{}, false
}

// tierCentsPerKWh 把某一档存下来的费率换算成等价的每 kWh 分数。适用哪种换算
// 是存在 spec 里的商业决策，不是藏在算式里的假设。它只用于按片计电量；
// 峰值功率计费直接用存下来的那个数字。
func tierCentsPerKWh(tier Tier, basis TierPriceBasis) int64 {
	if basis == TierPerKWh {
		return tier.ElectricCents
	}
	return tier.ElectricCents * int64(tier.MaxWatts) / 1000
}

func toCents(value decimal.Decimal) (int64, bool) {
	rounded := value.Round(0).BigInt()
	if !rounded.IsInt64() {
		return 0, false
	}
	return rounded.Int64(), true
}
