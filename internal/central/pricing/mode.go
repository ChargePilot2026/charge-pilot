package pricing

import "errors"

// ChargeExecutor is the primary axis of the whole pricing domain: it answers
// the only question that changes every downstream behaviour — does the server
// compute the money, or does the device?
type ChargeExecutor string

const (
	// ExecutorServer means the platform prices the session from the meter and
	// bills afterwards. The device only reports.
	ExecutorServer ChargeExecutor = "server"
	// ExecutorDevice means the money was already collected at payment time and
	// the device merely spends that allowance down until it runs out. The
	// server never recomputes a bill on this path.
	ExecutorDevice ChargeExecutor = "device"
)

// ChargeMode is the flat set of billable configurations. It is deliberately
// flat rather than a two-level type: the device-side modes carry no secondary
// attribute at all, so a nested shape would leave a field that only the server
// side can fill and that a device mode can still be given a value in.
type ChargeMode string

const (
	// Server modes: the fee comes out of Cost against real usage.
	ModeServerRealtimePower ChargeMode = "server_realtime_power" // 按实时功率
	ModeServerMaxPower      ChargeMode = "server_max_power"      // 按最大功率
	ModeServerEnergy        ChargeMode = "server_energy"         // 按电量
	// Device modes: the fee is what the charging user already paid.
	ModeDeviceDuration ChargeMode = "device_duration" // 时长
	ModeDeviceEnergy   ChargeMode = "device_energy"   // 电量
	ModeDevicePower    ChargeMode = "device_power"    // 功率档位
)

var (
	// ErrNotServerBilled is returned when something asks the pricing engine
	// for a number on a device-billed session. There is no correct answer, and
	// guessing one is exactly how a quote stops matching what was collected.
	ErrNotServerBilled = errors.New("该计费方式由设备执行，服务端不计算费用")
)

// Executor reports which side of the split a mode sits on.
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

// ServerBilled reports whether Cost is authoritative for this mode.
func (m ChargeMode) ServerBilled() bool { return m.Executor() == ExecutorServer }

// IsDevice reports whether the device, rather than the platform, decides when
// the charge ends.
func (e ChargeExecutor) IsDevice() bool { return e == ExecutorDevice }

// Valid reports whether the mode is one this build knows how to execute.
func (m ChargeMode) Valid() bool { return m.Executor() != "" }

// ServerBasis is what the electric line is charged on, and it only ever means
// anything for a server-billed mode.
type ServerBasis string

const (
	// BasisRealtimePower prices each slice by the power it actually drew.
	BasisRealtimePower ServerBasis = "realtime_power"
	// BasisMaxPower prices the whole session against its peak power.
	BasisMaxPower ServerBasis = "max_power"
	// BasisEnergy prices energy directly. There is no ladder of rates to find.
	BasisEnergy ServerBasis = "energy"
)

// BasisFor returns the electric basis a server mode must be configured with, so
// the mode and the tariff cannot drift into describing two different tariffs.
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

// ServiceBasis is what the service line is charged on. It is a second,
// independent line: it has its own switch, its own basis, and combines freely
// with whatever the electric line is doing.
type ServiceBasis string

const (
	// ServiceNone means no service fee at all.
	ServiceNone ServiceBasis = "none"
	// ServiceEnergy charges per kWh.
	ServiceEnergy ServiceBasis = "energy"
	// ServiceMinutePower charges per kWh but priced through the power ladder.
	ServiceMinutePower ServiceBasis = "minute_power"
	// ServiceMinute charges a flat rate per minute of charging.
	ServiceMinute ServiceBasis = "minute"
	// ServiceSession charges one flat amount for the session.
	ServiceSession ServiceBasis = "session"
)

// Valid reports whether the service basis is one the engine can execute.
func (b ServiceBasis) Valid() bool {
	switch b {
	case ServiceNone, ServiceEnergy, ServiceMinutePower, ServiceMinute, ServiceSession:
		return true
	default:
		return false
	}
}

// TierPriceBasis records how a power tier's hourly rate becomes money. The
// trade quotes gradient pricing in cents-per-hour while the meter reports
// energy, so the conversion is a stored commercial decision rather than an
// assumption buried in the arithmetic.
type TierPriceBasis string

const (
	// TierPerHourAtCeiling converts as rate * (tier ceiling in kW), giving the
	// equivalent cents per kWh.
	TierPerHourAtCeiling TierPriceBasis = "per_hour_at_ceiling"
	// TierPerKWh reads the stored number as a plain cents-per-kWh.
	TierPerKWh TierPriceBasis = "per_kwh"
)
