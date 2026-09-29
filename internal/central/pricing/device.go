package pricing

import (
	"errors"
	"time"
)

var (
	// ErrNoPaidAmount is returned when a device-billed session is settled
	// without the amount that was actually collected. There is no defensible
	// substitute: the rider paid a specific number and the bill has to be it.
	ErrNoPaidAmount = errors.New("设备计费缺少实付金额，无法结算")
	// ErrStopRequired is returned when a server-billed session ends without a
	// confirmed stop. Leaving it unflagged would let an unbounded charge run
	// on the strength of a bug.
	ErrStopRequired = errors.New("服务端计费必须确认已下发停止指令")
)

// StopReason is why the device stopped. It is reported by the device, so an
// unknown value is recorded as-is rather than guessed at.
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
	// StopUnknown preserves a code this build does not recognise. The vendor
	// document has had a hole in this enumeration before, so a value we cannot
	// place is kept verbatim rather than folded into a neighbour.
	StopUnknown StopReason = "unknown"
)

// ControlInstruction is what the platform tells the device to do, written at
// payment time. Only the field the mode actually uses is meaningful; the rest
// are zero. A device-billed session has no rates at all, so there is nothing
// here that could be turned into a second bill.
type ControlInstruction struct {
	Mode ChargeMode `json:"mode"`
	// Minutes is device_duration's control quantity.
	Minutes uint16 `json:"minutes,omitempty"`
	// EnergyMilliWh is device_energy's control quantity.
	EnergyMilliWh uint64 `json:"energy_milli_wh,omitempty"`
	// BalanceCents is device_power's control quantity: the allowance the device
	// spends down. It equals what the rider paid.
	BalanceCents int64 `json:"balance_cents,omitempty"`
	// TierCentsPerHour travels with the balance so the device can convert the
	// power it is drawing into a running spend.
	TierCentsPerHour []int64 `json:"tier_cents_per_hour,omitempty"`
	// StopWhenFull and MaxMinutes shape how a duration session ends.
	StopWhenFull bool   `json:"stop_when_full,omitempty"`
	MaxMinutes   uint16 `json:"max_minutes,omitempty"`
}

// Valid reports whether the instruction is executable, and is the single place
// that knows which field belongs to which mode.
func (c ControlInstruction) Valid() bool {
	if !c.Mode.Valid() || !c.Mode.Executor().IsDevice() {
		return false
	}
	switch c.Mode {
	case ModeDeviceDuration:
		// The balance and ladder must be absent: a duration session runs on the
		// clock, and carrying a balance would let something later price it.
		return c.Minutes > 0 && c.Minutes <= 999 && c.BalanceCents == 0 && len(c.TierCentsPerHour) == 0
	case ModeDeviceEnergy:
		return c.EnergyMilliWh > 0 && c.BalanceCents == 0 && len(c.TierCentsPerHour) == 0
	case ModeDevicePower:
		return c.BalanceCents > 0 && c.TierCentsPerHour != nil && len(c.TierCentsPerHour) > 0 && len(c.TierCentsPerHour) <= 8
	default:
		return false
	}
}

// SessionActual is what the device reports it actually did. Reported is false
// when the value was inferred rather than read back, and the difference is
// stored rather than smoothed over: a fallback is an estimate, and once an
// estimate is filed as a measurement it can never be found again.
type SessionActual struct {
	StopReason  StopReason `json:"stop_reason"`
	UsedSeconds uint32     `json:"used_seconds"`
	UsedMilliWh uint64     `json:"used_milli_wh"`
	PeakWatts   uint32     `json:"peak_watts"`
	// SpentCents is device_power's running spend. It is normally slightly below
	// the balance actually collected, because the device stops on its sampling
	// grid rather than at the exact instant; the difference is a sampling
	// residual, not a shortfall.
	SpentCents int64 `json:"spent_cents"`
	Reported   bool  `json:"reported"`
}

// Settlement is the money a session actually produced, with the source it came
// from attached. Source exists because the two sides are not interchangeable:
// a server-billed amount is a computation, a device-billed amount is money
// already in hand, and a report that does not say which is not auditable.
type Settlement struct {
	Mode          ChargeMode `json:"mode"`
	Executor      string     `json:"executor"`
	ElectricCents int64      `json:"electric_cents"`
	ServiceCents  int64      `json:"service_cents"`
	TotalCents    int64      `json:"total_cents"`
	// ResidualCents is what the device did not manage to spend of the amount
	// collected under a power-billed mode. It is returned, not written off, so a
	// device whose sampling grid is too coarse stays visible.
	ResidualCents int64 `json:"residual_cents,omitempty"`
	// Estimated marks a total that was inferred rather than reported.
	Estimated  bool   `json:"estimated"`
	StopReason string `json:"stop_reason,omitempty"`
}

// SettleSession is the one place a session's money is decided.
//
// For a server-billed mode the amount must be exactly what Cost returns; any
// other source is a bug, and this is where that bug is caught rather than on an
// invoice. For a device-billed mode the amount is what was paid and nothing
// else is consulted, because recomputing it would produce a second figure for a
// bill that has already been settled.
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
		// A purchased package may only lower the bill, never raise it. Anything
		// else would let a package become a surcharge. A cap truncates the
		// total; a fixed-span package prorates it by how much of the span was
		// actually used, which is what makes an abandoned session refundable.
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
			// A cap or a pro-ration changes the total, so the split has to be
			// rescaled. Leaving the two components at their computed values
			// would produce a receipt whose parts do not add up to its total,
			// which is the first thing any reconciliation notices.
			electric, service = splitFee(fee, total)
		}
		settlement.ElectricCents, settlement.ServiceCents, settlement.TotalCents = electric, service, total
		return settlement, nil
	}
	// Device-billed: the amount is the money that was collected.
	if paid == nil || !paid.Valid() {
		return Settlement{}, ErrNoPaidAmount
	}
	amount := paid.PriceCents
	if paid.Mode == "package" && actual != nil && paid.DurationMinutes > 0 {
		// A package is bought for a span, and returning it early should not
		// charge the whole span. This is a refund question, not a pricing one,
		// so it only ever lowers the amount.
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

// splitFee rescales a priced fee onto a different total, keeping the two
// components summing exactly to it.
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

// StopPlan is what the server-billed path may do while a session runs.
//
// The firmware is still told a time or an energy allowance in this mode and
// still stops when that allowance runs out; what platform billing changes is
// that the board stops applying its power-band discount, so the platform
// prices the real draw. Bounding the charge is therefore a product decision
// rather than a protocol necessity — but once an operator sets a spend cap,
// enforcing it is the platform's job alone, because nothing else will.
type StopPlan struct {
	// ShouldStop is evaluated on every telemetry update.
	ShouldStop bool
	// Reason explains the decision to whoever reads the log.
	Reason string
	// AccruedCents is the running total the decision was made against.
	AccruedCents int64
	// GraceSeconds is how long the platform waits after asking the device to
	// stop before it treats the session as unbounded.
	GraceSeconds int
}

// DecideStop decides whether a running server-billed session has reached a
// spend cap.
//
// It is separated from the transport on purpose: the rule for when to cut a
// charge off has to be the same whether the decision is made by a poller, a
// webhook or an operator, and it has to be testable without a device.
func DecideStop(spec Spec, usage Usage, elapsed time.Duration) (StopPlan, error) {
	if ValidateSpec(spec) != nil {
		return StopPlan{}, ErrInvalidPricing
	}
	if !spec.Mode.ServerBilled() {
		// A device-billed session ends when the device's own allowance runs
		// out. Intervening would fight the board for control of a session the
		// platform is not paying for.
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
