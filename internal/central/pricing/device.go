package pricing

import (
	"errors"
	"time"
)

var (
	// ErrNoPaidAmount 在设备计费的充电拿不出实际收款金额时返回。这里没有站得住脚的
	// 替代值：充电用户付的是一个确定的数，账单就必须是这个数。
	ErrNoPaidAmount = errors.New("设备计费缺少实付金额，无法结算")
	// ErrStopRequired 在服务端计费的充电结束时没有拿到已确认的停止指令时返回。
	// 不把这个状态标出来，就等于让一次没人管得住的充电靠一个 bug 一直跑下去。
	ErrStopRequired = errors.New("服务端计费必须确认已下发停止指令")
)

// StopReason 是设备停止的原因。它由设备上报，因此遇到认不出的取值时原样记录，
// 而不是猜一个。
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
	// StopUnknown 保留本版本认不出的编码。厂商文档里这个枚举曾经漏过项，所以
	// 无法归位的取值原样保存，而不是并进相邻的取值里。
	StopUnknown StopReason = "unknown"
)

// ControlInstruction 是平台在支付时下发给设备、告诉它怎么做的指令。只有当前模式
// 真正用到的那个字段有意义，其余字段都是零值。设备计费的充电完全不带费率，所以
// 这里没有任何字段能被拿去再算一笔钱。
type ControlInstruction struct {
	Mode ChargeMode `json:"mode"`
	// Minutes 是 device_duration 的控制量。
	Minutes uint16 `json:"minutes,omitempty"`
	// EnergyMilliWh 是 device_energy 的控制量。
	EnergyMilliWh uint64 `json:"energy_milli_wh,omitempty"`
	// BalanceCents 是 device_power 的控制量：设备要花掉的那份额度，
	// 它等于充电用户实际付的金额。
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
		return c.Minutes > 0 && c.Minutes <= 999 && c.BalanceCents == 0 && len(c.TierCentsPerHour) == 0
	case ModeDeviceEnergy:
		return c.EnergyMilliWh > 0 && c.BalanceCents == 0 && len(c.TierCentsPerHour) == 0
	case ModeDevicePower:
		return c.BalanceCents > 0 && c.TierCentsPerHour != nil && len(c.TierCentsPerHour) > 0 && len(c.TierCentsPerHour) <= 8
	default:
		return false
	}
}

// SessionActual 是设备回报它实际做了什么。Reported 为 false 表示这个值是推算出来的
// 而不是从充电板回读到的，而这一区别是被存下来的，不是被抹平的：兜底值就是
// 估算值，估算一旦被当成计量归档就再也找不回来了。
type SessionActual struct {
	StopReason  StopReason `json:"stop_reason"`
	UsedSeconds uint32     `json:"used_seconds"`
	UsedMilliWh uint64     `json:"used_milli_wh"`
	PeakWatts   uint32     `json:"peak_watts"`
	// SpentCents 是 device_power 运行中的累计花费。它通常略低于实际收回的余额，
	// 因为设备是在自己的采样栅格上停下的，而不是在那个精确瞬间停的。
	// 这点差额是采样残差而不是少收。
	SpentCents int64 `json:"spent_cents"`
	Reported   bool  `json:"reported"`
}

// Settlement 是一次充电实际产生的钱，并带上它是从哪来的。之所以要记下 Executor，
// 是因为两者不能互换：服务端计费的金额是一次计算，设备计费的金额是已经到手的
// 钱，一份说不清来源的报表没法审计。
type Settlement struct {
	Mode          ChargeMode `json:"mode"`
	Executor      string     `json:"executor"`
	ElectricCents int64      `json:"electric_cents"`
	ServiceCents  int64      `json:"service_cents"`
	TotalCents    int64      `json:"total_cents"`
	// ResidualCents 是在功率计费口径下，设备没能花完的那部分已收金额。
	// 它被退回而不是被核销，这样采样栅格太粗的设备才不会被藏起来。
	ResidualCents int64 `json:"residual_cents,omitempty"`
	// Estimated 标记这个总额是推算出来的，而不是上报上来的。
	Estimated  bool   `json:"estimated"`
	StopReason string `json:"stop_reason,omitempty"`
}

// SettleSession 是一次充电的钱被定下来的唯一地方。
//
// 服务端计费模式下，金额必须与 Cost 返回的完全一致；任何别的来源都是 bug，
// 而这个 bug 是在这里被抓住的，而不是等它出现在账单上。设备计费模式下，
// 金额就是实际付出的钱，别的什么都不看——重新算一遍等于给一张已经结清的
// 账单再造一个数字。
func SettleSession(spec Spec, meter ActualMeter, paid *Offer, actual *SessionActual) (Settlement, error) {
	if ValidateSpec(spec) != nil {
		return Settlement{}, ErrInvalidPricing
	}
	settlement := Settlement{Mode: spec.Mode, Executor: string(spec.Mode.Executor())}
	if actual != nil {
		settlement.StopReason = string(actual.StopReason)
	}
	if spec.Mode.ServerBilled() {
		fee, err := PriceActual(Rule{ID: 1, Version: 1, Spec: spec}, meter)
		if err != nil {
			return Settlement{}, err
		}
		// 买下的套餐只允许把账单往下压，绝不允许往上抬；否则套餐就变成了附加费。
		// 封顶型套餐（amount）是把总额直接截断到购买价，固定时长套餐（package）
		// 则按实际用掉的时长占整段时长的比例折算。
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
			case "package":
				limit := int64(paid.DurationMinutes) * 60
				used := int64(meter.ChargedSeconds)
				if used < limit {
					if limit > 0 {
						total = total * used / limit
					}
				}
			}
		}
		if total < 0 {
			return Settlement{}, ErrInvalidPricing
		}
		electric, service := fee.ElectricCents, fee.ServiceCents
		if total != fee.TotalCents {
			// 封顶或折算都会改掉总额，所以两项拆分必须同比例重算。留着原来算出的
			// 金额不动，就会开出一张「分项加起来不等于合计」的收据，这正是任何
			// 一次对账最先撞上的问题。
			electric, service = splitFee(fee, total)
		}
		settlement.ElectricCents, settlement.ServiceCents, settlement.TotalCents = electric, service, total
		return settlement, nil
	}
	// 设备计费：金额就是已经收走的那笔钱。
	if paid == nil || !paid.Valid() {
		return Settlement{}, ErrNoPaidAmount
	}
	amount := paid.PriceCents
	if paid.Mode == "package" && actual != nil && paid.DurationMinutes > 0 {
		// 套餐是按一整段时长买的，提前退不应该把整段时长都收走。这是退款问题
		// 而不是定价问题，所以它只会把金额往下压。
		limit := int64(paid.DurationMinutes) * 60
		used := int64(actual.UsedSeconds)
		if used < limit {
			amount = amount * used / limit
		}
	}
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
	electric := fee.ElectricCents * total / fee.TotalCents
	if electric < 0 {
		electric = 0
	}
	if electric > total {
		electric = total
	}
	return electric, total - electric
}

// StopPlan 是服务端计费路径在充电运行期间可以做的事。
//
// 这个模式下固件仍会被告知一个时长或电量额度，也仍会在额度用完时停下；
// 平台计费改变的只是充电板不再对自己的功率档打折，于是平台按真实取电计费。
// 所以给充电封顶是产品决策而不是协议必需——但运营方一旦设了消费封顶，
// 执行它就只是平台自己的事了，因为再没有别人会执行。
type StopPlan struct {
	// ShouldStop 在每次遥测更新时求值。
	ShouldStop bool
	// Reason 向读日志的人解释这个决定。
	Reason string
	// AccruedCents 是做这个决定时对照的累计总额。
	AccruedCents int64
	// GraceSeconds 是平台要求设备停止之后，等多久才把这次充电视为失控。
	GraceSeconds int
}

// DecideStop 判断一次运行中的服务端计费充电是否已经触到它那份电价表所声明的
// 消费封顶。
//
// 它与传输层分开是故意的：什么时候切断充电这条规则，无论是轮询器、webhook
// 还是运营人员来执行，都必须是同一条，而且必须能在没有设备的情况下测试。
func DecideStop(spec Spec, usage Usage, elapsed time.Duration) (StopPlan, error) {
	if ValidateSpec(spec) != nil {
		return StopPlan{}, ErrInvalidPricing
	}
	if !spec.Mode.ServerBilled() {
		// 设备计费的充电在设备自己的额度用完时结束。插手进去就是去和充电板抢
		// 一次平台并不付费的充电的控制权。
		return StopPlan{Reason: "设备计费由设备到量自行停止"}, nil
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
