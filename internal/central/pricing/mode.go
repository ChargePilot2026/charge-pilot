package pricing

import "errors"

// ChargeExecutor 是整个计价领域的主轴：它只回答一个问题，而这个问题决定了下游
// 的所有行为——钱是服务端算的，还是设备算的。
type ChargeExecutor string

const (
	// ExecutorServer 表示平台按计量结果给这次充电计价，事后扣费，
	// 设备只负责上报。
	ExecutorServer ChargeExecutor = "server"
	// ExecutorDevice 表示钱在支付时就已经收走，设备只是把这份额度一分一分地
	// 花完为止；服务端在这条路径上从不重算账单。
	ExecutorDevice ChargeExecutor = "device"
)

// ChargeMode 是可计费配置的扁平集合。它刻意不做成两级类型：设备侧的模式根本
// 没有第二级属性，做成嵌套就会多出一个只有服务端能填、而设备模式却仍然能被
// 赋上值的字段。
type ChargeMode string

const (
	// 服务端模式：费用由 Cost 按真实用量算出来。
	ModeServerRealtimePower ChargeMode = "server_realtime_power" // 按实时功率
	ModeServerMaxPower      ChargeMode = "server_max_power"      // 按最大功率
	ModeServerEnergy        ChargeMode = "server_energy"         // 按电量
	// 设备模式：费用就是充电用户已经付掉的钱。
	ModeDeviceDuration ChargeMode = "device_duration" // 时长
	ModeDeviceEnergy   ChargeMode = "device_energy"   // 电量
	ModeDevicePower    ChargeMode = "device_power"    // 功率档位
)

var (
	// ErrNotServerBilled 在有人对设备计费的充电向计价引擎要一个数字时返回。
	// 这里没有正确答案，而猜一个正是报价与实收对不上的开始。
	ErrNotServerBilled = errors.New("该计费方式由设备执行，服务端不计算费用")
)

// Executor 报告这个模式站在这条分界线的哪一侧。
func (m ChargeMode) Executor() ChargeExecutor {
	switch m {
	case ModeServerRealtimePower, ModeServerMaxPower, ModeServerEnergy:
		return ExecutorServer
	case ModeDeviceDuration, ModeDeviceEnergy, ModeDevicePower:
		return ExecutorDevice
	default:
		return ""
	}
}

// ServerBilled 报告这个模式下 Cost 是不是权威口径。
func (m ChargeMode) ServerBilled() bool { return m.Executor() == ExecutorServer }

// IsDevice 报告是设备而不是平台来决定充电何时结束。
func (e ChargeExecutor) IsDevice() bool { return e == ExecutorDevice }

// Valid 报告这个模式是否是本版本知道怎么执行的模式。
func (m ChargeMode) Valid() bool { return m.Executor() != "" }

// ServerBasis 是电费按什么口径收取，它只对服务端计费模式有意义。
type ServerBasis string

const (
	// BasisRealtimePower 按每一片实际抽取的功率给这一片定价。
	BasisRealtimePower ServerBasis = "realtime_power"
	// BasisMaxPower 拿整次充电的峰值功率给这一次充电定价。
	BasisMaxPower ServerBasis = "max_power"
	// BasisEnergy 直接给电量定价，不存在需要去找的费率阶梯。
	BasisEnergy ServerBasis = "energy"
)

// BasisFor 返回一个服务端模式必须配置成的电量口径，使模式与电价表不会
// 各自漂移到描述两份不同的电价表。
func (m ChargeMode) BasisFor() ServerBasis {
	switch m {
	case ModeServerRealtimePower:
		return BasisRealtimePower
	case ModeServerMaxPower:
		return BasisMaxPower
	case ModeServerEnergy:
		return BasisEnergy
	default:
		return ""
	}
}

// ServiceBasis 是服务费按什么口径收取。它是独立的第二条线：有自己的开关、
// 自己的口径，并且可以与电费正在做的一切自由组合。
type ServiceBasis string

const (
	// ServiceNone 表示完全不收服务费。
	ServiceNone ServiceBasis = "none"
	// ServiceEnergy 按每 kWh 收。
	ServiceEnergy ServiceBasis = "energy"
	// ServiceMinutePower 按每 kWh 收，但单价走功率阶梯来定。
	ServiceMinutePower ServiceBasis = "minute_power"
	// ServiceMinute 按每分钟充电时长收一个固定费率。
	ServiceMinute ServiceBasis = "minute"
	// ServiceSession 整次充电收一个固定金额。
	ServiceSession ServiceBasis = "session"
)

// Valid 报告这个服务费口径是否是引擎能执行的。
func (b ServiceBasis) Valid() bool {
	switch b {
	case ServiceNone, ServiceEnergy, ServiceMinutePower, ServiceMinute, ServiceSession:
		return true
	default:
		return false
	}
}

// TierPriceBasis 记录功率档的每小时费率是怎么变成钱的。行业惯例用每小时的分数
// 报梯度电价，而计量报的是电量，所以这个换算是存下来的商业决策，而不是埋在
// 算式里的假设。
type TierPriceBasis string

const (
	// TierPerHourAtCeiling 按 费率 ×（该档上界，单位 kW）换算，
	// 得到等价的每 kWh 分数。
	TierPerHourAtCeiling TierPriceBasis = "per_hour_at_ceiling"
	// TierPerKWh 把存下来的数字直接当作每 kWh 的分数来读。
	TierPerKWh TierPriceBasis = "per_kwh"
)
