package pricing

import (
	"errors"
	"time"
)

var (
	// ErrNoPaidAmount 表示设备计费缺少实际付款金额，不能用估算值替代。
	ErrNoPaidAmount = errors.New("设备计费缺少实付金额，无法结算")
	// ErrStopRequired 表示服务端计费的结束记录缺少已确认停止指令，需进入补偿或核实流程。
	ErrStopRequired = errors.New("服务端计费必须确认已下发停止指令")
)

// StopReason 记录设备上报的停止原因；未知编码保留原值。
type StopReason string

const (
	StopExhaustedTime    StopReason = "exhausted_time"    // 时间用完
	StopExhaustedEnergy  StopReason = "exhausted_energy"  // 电量用完
	StopChargerRemoved   StopReason = "charger_removed"   // 移除充电器
	StopBatteryFull      StopReason = "battery_full"      // 充满自停
	StopFault            StopReason = "fault"             // 故障停止
	StopPowerTooHigh     StopReason = "power_too_high"    // 功率过大保护
	StopCardRefund       StopReason = "card_refund"       // 离线卡退费停止
	StopNoCharger        StopReason = "no_charger"        // 启动后未连接充电器
	StopRemote           StopReason = "remote"            // 远程停止
	StopSmoke            StopReason = "smoke"             // 烟感停止
	StopOverheat         StopReason = "overheat"          // 高温停止
	StopServerDisconnect StopReason = "server_disconnect" // 平台失联兜底
	StopUser             StopReason = "user"              // 用户主动结束
	// StopUnknown 表示本版本无法识别的停止原因编码，原始值继续保留。
	StopUnknown StopReason = "unknown"
)

// ControlInstruction 描述支付后下发的设备控制参数；仅当前模式需要的字段有效，其余为零值。
// 设备计费指令不包含服务端费率。
type ControlInstruction struct {
	Mode ChargeMode `json:"mode"`
	// Minutes 是 device_duration 的控制量。
	Minutes uint16 `json:"minutes,omitempty"`
	// EnergyMilliWh 是 device_energy 的控制量。
	EnergyMilliWh uint64 `json:"energy_milli_wh,omitempty"`
	// BalanceCents 是 device_power 的设备额度，等于用户实际付款金额。
	BalanceCents int64 `json:"balance_cents,omitempty"`
	// TierCentsPerHour 随余额一起下发，设备据此把当前抽取的功率换算成
	// 运行中的累计花费。
	TierCentsPerHour []int64 `json:"tier_cents_per_hour,omitempty"`
	// StopWhenFull 与 MaxMinutes 决定按时长计费的充电怎么结束。
	StopWhenFull bool   `json:"stop_when_full,omitempty"`
	MaxMinutes   uint16 `json:"max_minutes,omitempty"`
}

// Valid 报告这条指令能否执行，也是唯一知道「哪个字段属于哪个模式」的地方。
func (c ControlInstruction) Valid() bool {
	if !c.Mode.Valid() || !c.Mode.Executor().IsDevice() {
		return false
	}
	switch c.Mode {
	case ModeDeviceDuration:
		// 余额与阶梯必须缺席：按时长计费的充电是按时间走的，带着余额就等于
		// 给后面某个环节留了一次再定价的机会。
		return c.Minutes > 0 && c.Minutes <= 4320 && c.BalanceCents == 0 && len(c.TierCentsPerHour) == 0
	case ModeDeviceEnergy:
		return c.EnergyMilliWh > 0 && c.BalanceCents == 0 && len(c.TierCentsPerHour) == 0
	case ModeDevicePower:
		return c.BalanceCents > 0 && c.TierCentsPerHour != nil && len(c.TierCentsPerHour) > 0 && len(c.TierCentsPerHour) <= 8
	default:
		return false
	}
}

// SessionActual 记录设备执行结果。Reported 为 false 表示推算值，归档时需保留其估算来源。
type SessionActual struct {
	StopReason  StopReason `json:"stop_reason"`
	UsedSeconds uint32     `json:"used_seconds"`
	UsedMilliWh uint64     `json:"used_milli_wh"`
	PeakWatts   uint32     `json:"peak_watts"`
	// SpentCents 为 device_power 累计消费，单位为分；采样间隔可能产生未消费余额。
	SpentCents int64 `json:"spent_cents"`
	Reported   bool  `json:"reported"`
}

// Settlement 保存充电结算金额及执行来源。Executor 区分服务端计算与设备已收金额，供对账审计。
type Settlement struct {
	Mode          ChargeMode `json:"mode"`
	Executor      string     `json:"executor"`
	ElectricCents int64      `json:"electric_cents"`
	ServiceCents  int64      `json:"service_cents"`
	TotalCents    int64      `json:"total_cents"`
	// ResidualCents 为设备功率计费的未消费预付款，单位为分；该余额退还用户。
	ResidualCents int64 `json:"residual_cents,omitempty"`
	// Estimated 标记结算金额为推算值，而非设备上报值。
	Estimated  bool   `json:"estimated"`
	StopReason string `json:"stop_reason,omitempty"`
}

// SettleSession 统一生成充电结算。
// 服务端计费使用 Cost 结果；设备计费基于已支付金额及实际执行结果计算消费和退款，避免重复计价。
func SettleSession(spec Spec, meter ActualMeter, paid *Offer, actual *SessionActual) (Settlement, error) {
	if meter.ReviewRequired {
		return Settlement{}, ErrMeterReview
	}
	if spec.Scheme != nil && paid != nil {
		p, ok := spec.Scheme.Package(paid.PackageID)
		count := int64(paid.PurchaseCount)
		if count == 0 {
			count = 1
		}
		if !ok || p.PriceCents*count != paid.PriceCents || p.Mode != paid.Mode || p.Mode == "duration" && uint32(p.Minutes)*uint32(count) != uint32(paid.DurationMinutes) {
			return Settlement{}, ErrInvalidPricing
		}
		spec = spec.Scheme.SpecFor(p)
	}
	if ValidateSpec(spec) != nil {
		return Settlement{}, ErrInvalidPricing
	}
	settlement := Settlement{Mode: spec.Mode, Executor: string(spec.Mode.Executor())}
	if actual != nil {
		settlement.StopReason = string(actual.StopReason)
	}
	if paid != nil && paid.ServerDuration {
		settlement.Executor = "server"
	}
	// 固定时长套餐按购买快照的售价结算，与站点费率和功率无关。
	// 延迟停机也不超收；提前停止延续未使用时长退款规则。
	if paid != nil && paid.Mode == "duration" {
		if !paid.Valid() {
			return Settlement{}, ErrInvalidPricing
		}
		used := int64(meter.ChargedSeconds)
		if !spec.Mode.ServerBilled() && actual != nil {
			used = int64(actual.UsedSeconds)
		}
		used /= 60
		limit := int64(paid.DurationMinutes)
		amount := paid.PriceCents
		if used < limit {
			amount = amount * used / limit
		}
		// 套餐总价不含单独约定的电费拆分，与支付报价一致记入套餐服务费。
		settlement.ServiceCents, settlement.TotalCents = amount, amount
		settlement.Estimated = actual == nil || !actual.Reported
		return settlement, nil
	}
	if paid != nil && paid.Mode == "energy" {
		if !paid.Valid() || spec.Scheme == nil || spec.Scheme.Energy == nil {
			return Settlement{}, ErrInvalidPricing
		}
		wh := uint64(meter.ChargedWh)
		rate := spec.Scheme.Energy
		amount := min(int64(wh)*(rate.ElectricCents+rate.ServiceCents)/1000, paid.PriceCents)
		electric := amount * rate.ElectricCents / (rate.ElectricCents + rate.ServiceCents)
		settlement.ElectricCents = electric
		settlement.ServiceCents = amount - electric
		settlement.TotalCents = amount
		return settlement, nil
	}
	if spec.Mode.ServerBilled() {
		fee, err := PriceActual(Rule{ID: 1, Version: 1, Spec: spec}, meter)
		if err != nil {
			return Settlement{}, err
		}
		// 金额方案按现行费率计算，消费上限只压低账单，不增加费用。
		total := fee.TotalCents
		if paid != nil {
			if !paid.Valid() {
				return Settlement{}, ErrInvalidPricing
			}
			switch paid.Mode {
			case "amount":
				if total > paid.PriceCents {
					total = paid.PriceCents
				}
			}
		}
		if total < 0 {
			return Settlement{}, ErrInvalidPricing
		}
		electric, service := fee.ElectricCents, fee.ServiceCents
		if total != fee.TotalCents {
			// 封顶或折算改变总额后，按比例重算电费与服务费，使分项合计等于总额。
			electric, service = splitFee(fee, total)
		}
		settlement.ElectricCents, settlement.ServiceCents, settlement.TotalCents = electric, service, total
		return settlement, nil
	}
	// 设备计费金额等于实际付款金额。
	if paid == nil || !paid.Valid() {
		return Settlement{}, ErrNoPaidAmount
	}
	amount := paid.PriceCents
	settlement.TotalCents = amount
	settlement.Estimated = actual == nil || !actual.Reported
	if actual != nil && spec.Mode == ModeDevicePower && actual.SpentCents > 0 {
		settlement.ResidualCents = amount - actual.SpentCents
		if settlement.ResidualCents < 0 {
			return Settlement{}, ErrInvalidPricing
		}
	}
	return settlement, nil
}

// splitFee 把一个已计价的结果按新的总额重新分摊，并保证两项之和正好等于它。
func splitFee(fee Fee, total int64) (int64, int64) {
	if fee.TotalCents <= 0 {
		return 0, total
	}
	electric := max(fee.ElectricCents*total/fee.TotalCents, 0)
	if electric > total {
		electric = total
	}
	return electric, total - electric
}

// StopPlan 描述运行中服务端计费的停止决策。
// 设备继续执行下发的时长或电量安全额度；消费封顶由平台执行。
type StopPlan struct {
	// ShouldStop 在每次遥测更新时求值。
	ShouldStop bool
	// Reason 为停机判断原因。
	Reason string
	// AccruedCents 为判断时的累计总费用，单位分。
	AccruedCents int64
	// GraceSeconds 是平台要求设备停止之后，等多久才把这次充电视为失控。
	GraceSeconds int
}

// DecideStop 根据生效规则和当前消费判断服务端计费是否达到上限。
// 决策与传输层分离，供调度器或其他执行入口复用并独立测试。
func DecideStop(spec Spec, usage Usage, elapsed time.Duration) (StopPlan, error) {
	if ValidateSpec(spec) != nil {
		return StopPlan{}, ErrInvalidPricing
	}
	if !spec.Mode.ServerBilled() {
		// 设备计费由设备自身额度控制结束，不使用平台费用上限停机。
		return StopPlan{Reason: "设备计费由设备到量自行停止"}, nil
	}
	if spec.TimeCharge != nil && spec.TimeCharge.MaxMinutes > 0 && elapsed >= time.Duration(spec.TimeCharge.MaxMinutes)*time.Minute {
		return StopPlan{ShouldStop: true, Reason: "达到最长时长"}, nil
	}
	fee, err := Cost(spec, usage)
	if err != nil {
		return StopPlan{}, err
	}
	plan := StopPlan{AccruedCents: fee.TotalCents}
	if spec.SpendCapCents > 0 && fee.TotalCents >= spec.SpendCapCents {
		plan.ShouldStop = true
		plan.Reason = "已达到消费封顶"
		plan.GraceSeconds = spec.StopGraceSeconds
	} else if spec.SpendCapCents == 0 {
		plan.Reason = "未设置消费封顶，由下发的时间/电量到量停止"
	}
	return plan, nil
}
