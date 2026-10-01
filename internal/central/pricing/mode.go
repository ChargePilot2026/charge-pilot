package pricing

import "errors"

// ChargeExecutor 指定服务端或设备负责计价。
type ChargeExecutor string

const (
	// ExecutorServer 由平台按计量结果计价，设备负责上报。
	ExecutorServer ChargeExecutor = "server"
	// ExecutorDevice 由设备消耗已付款额度，服务端不重新计算账单。
	ExecutorDevice ChargeExecutor = "device"
)

// ChargeMode 以独立枚举表示服务端和设备的计费模式。
type ChargeMode string

const (
	// 服务端模式：费用由 Cost 按真实用量算出来。
	ModeServerRealtimePower ChargeMode = "server_realtime_power" // 按实时功率
	ModeServerMaxPower      ChargeMode = "server_max_power"      // 按最大功率
	ModeServerEnergy        ChargeMode = "server_energy"         // 按电量
	// 设备模式费用等于用户实际付款金额。
	ModeDeviceDuration ChargeMode = "device_duration" // 时长
	ModeDeviceEnergy   ChargeMode = "device_energy"   // 电量
	ModeDevicePower    ChargeMode = "device_power"    // 功率档位
)

var (
	// ErrNotServerBilled 表示当前模式由设备计费，不支持服务端费率计算。
	ErrNotServerBilled = errors.New("该计费方式由设备执行，服务端不计算费用")
)

// Executor 返回计费模式所属的执行方。
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

// ServerBilled 判断该模式是否由 Cost 计算权威费用。
func (m ChargeMode) ServerBilled() bool { return m.Executor() == ExecutorServer }

// IsDevice 判断该模式是否由设备控制充电结束。
func (e ChargeExecutor) IsDevice() bool { return e == ExecutorDevice }

// Valid 判断当前版本是否支持该计费模式。
func (m ChargeMode) Valid() bool { return m.Executor() != "" }

// ServerBasis 是电费按什么口径收取，它只对服务端计费模式有意义。
type ServerBasis string

const (
	// BasisRealtimePower 按每一片实际抽取的功率给这一片定价。
	BasisRealtimePower ServerBasis = "realtime_power"
	// BasisMaxPower 按整场充电的峰值功率选择费率。
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

// Valid 判断计价引擎是否支持该服务费口径。
func (b ServiceBasis) Valid() bool {
	switch b {
	case ServiceNone, ServiceEnergy, ServiceMinutePower, ServiceMinute, ServiceSession:
		return true
	default:
		return false
	}
}

// TierPriceBasis 区分实时功率的电量单价与历史换算快照。
// 新模板只允许 TierPerKWh；其它旧值仅用于兼容已发布规则和历史订单。
type TierPriceBasis string

const (
	// TierPerHourAtCeiling 是旧版错误标为“元/小时”的填写值。历史执行时
	// 先乘上限瓦数、除以 1000 并截到整数分，再乘电量；不可作为新时长计费。
	TierPerHourAtCeiling TierPriceBasis = "per_hour_at_ceiling"
	// TierPerKWh 把存下来的数字直接当作每 kWh 的分数来读。
	TierPerKWh TierPriceBasis = "per_kwh"
)
