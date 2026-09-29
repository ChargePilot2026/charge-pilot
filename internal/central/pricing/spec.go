package pricing

import (
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrInvalidPricing  = errors.New("invalid quote input or pricing rule")
	ErrMeterReview     = errors.New("实际计量不足或矛盾，需补充分时计量后核算")
	ErrRuleUnavailable = errors.New("active station pricing rule unavailable")

	beijing = time.FixedZone("Asia/Shanghai", 8*3600)
)

// Basis is the single axis that decides what a tariff charges money on.
// Everything else in Spec only refines how that basis is priced.
type Basis string

const (
	// BasisEnergy charges cents-per-kWh against metered energy.
	BasisEnergy Basis = "energy"
	// BasisPowerTier charges graduated power tiers: each tier carries a
	// cents-per-hour rate that is converted to an equivalent cents-per-kWh
	// price using the tier ceiling.
	BasisPowerTier Basis = "power_tier"
	// BasisMaxPower charges one rate against the highest power seen, so a
	// session that peaks briefly still pays for the whole session.
	BasisMaxPower Basis = "max_power"
	// BasisPerMinute charges a flat rate per minute.
	BasisPerMinute Basis = "per_minute"
	// BasisPerSession charges a flat amount per session.
	BasisPerSession Basis = "per_session"
)

// ServiceMode selects what the service fee is charged on. It is deliberately
// not a UI tab: each value is just another basis applied to the service line.
type ServiceMode string

const (
	ServiceNone        ServiceMode = "none"
	ServiceEnergy      ServiceMode = "energy"
	ServiceMinute      ServiceMode = "minute"
	ServiceMinutePower ServiceMode = "minute_power"
	ServiceSession     ServiceMode = "session"
)

// TierPriceBasis records how a power tier's cents-per-hour rate becomes money.
// The industry quotes gradient pricing in cents-per-hour, but the meter only
// reports energy, so the conversion has to be an explicit, stored decision
// rather than an assumption buried in the settlement code.
type TierPriceBasis string

const (
	// TierPerHourAtCeiling converts as rate * (tier ceiling kW) = equivalent
	// cents per kWh. A 0.17 cents-per-hour tier capped at 200W bills 0.034
	// cents/kWh-equivalent... in cents terms: 0.17 * 0.2 kW = 0.034 yuan/kWh.
	TierPerHourAtCeiling TierPriceBasis = "per_hour_at_ceiling"
	// TierPerKWh reads the stored cents-per-hour number as a plain cents/kWh.
	TierPerKWh TierPriceBasis = "per_kwh"
)

// Window is a time-of-use rate applied across a contiguous clock range.
type Window struct {
	Start string `json:"start"`
	End   string `json:"end"`
	// CentsPerKWh prices energy and power tiers, and is also the fallback for
	// power above the top tier.
	CentsPerKWh int64 `json:"cents_per_kwh"`
	// CentsPerHourPerKW prices the whole session by its peak power. Only read
	// by BasisMaxPower, and only meaningful on an all-day window.
	CentsPerHourPerKW int64 `json:"cents_per_hour_per_kw,omitempty"`
	// ServiceCentsPerKWh overrides Spec.Service.CentsPerKWh inside this window.
	ServiceCentsPerKWh *int64 `json:"service_cents_per_kwh,omitempty"`
}

// Tier is one power band. Tiers are ordered and must not overlap.
type Tier struct {
	LowW  uint32 `json:"low_w"`
	HighW uint32 `json:"high_w"`
	// CentsPerHour is only meaningful when TierPriceBasis is per_hour_at_ceiling.
	CentsPerHour        int64 `json:"cents_per_hour"`
	ServiceCentsPerHour int64 `json:"service_cents_per_hour"`
}

// ServiceFee carries every service-fee rate; the active one is picked by Mode.
type ServiceFee struct {
	Mode            ServiceMode `json:"mode"`
	CentsPerKWh     int64       `json:"cents_per_kwh"`
	CentsPerMinute  int64       `json:"cents_per_minute"`
	CentsPerHour    int64       `json:"cents_per_hour"`
	CentsPerSession int64       `json:"cents_per_session"`
}

// Channel is how the session was started; it selects the rate multipliers.
type Channel string

const (
	ChannelDefault Channel = "default"
	ChannelTemp    Channel = "temp"
	ChannelCard    Channel = "card"
)

// Multiplier holds basis points where 10000 means 1.0x. Zero disables the
// override so an unset multiplier can never silently become free charging.
type Multiplier struct {
	TempElectricBP int32 `json:"temp_electric_bp"`
	TempServiceBP  int32 `json:"temp_service_bp"`
	CardElectricBP int32 `json:"card_electric_bp"`
	CardServiceBP  int32 `json:"card_service_bp"`
}

func (m Multiplier) electricBP(channel Channel) int32 {
	if channel == ChannelTemp && m.TempElectricBP > 0 {
		return m.TempElectricBP
	}
	if channel == ChannelCard && m.CardElectricBP > 0 {
		return m.CardElectricBP
	}
	return 10000
}

func (m Multiplier) serviceBP(channel Channel) int32 {
	if channel == ChannelTemp && m.TempServiceBP > 0 {
		return m.TempServiceBP
	}
	if channel == ChannelCard && m.CardServiceBP > 0 {
		return m.CardServiceBP
	}
	return 10000
}

// Spec is the complete, self-contained description of how money is computed.
// Estimate and settlement both run this same structure through Cost so the two
// sides can never drift apart.
type Spec struct {
	Basis Basis  `json:"basis"`
	Tiers []Tier `json:"tiers,omitempty"`
	// TierPriceBasis is only read for BasisPowerTier.
	TierPriceBasis TierPriceBasis `json:"tier_price_basis,omitempty"`
	// Windows must cover the whole day for energy and power_tier, and must be a
	// single all-day window for max_power. The flat bases ignore it entirely.
	Windows []Window `json:"windows"`
	// MaxPowerCentsPerHourPerKW is only read by BasisMaxPower.
	MaxPowerCentsPerHourPerKW int64 `json:"max_power_cents_per_hour_per_kw,omitempty"`
	// PerMinuteCents and PerSessionCents belong to the flat bases.
	PerMinuteCents  int64      `json:"per_minute_cents,omitempty"`
	PerSessionCents int64      `json:"per_session_cents,omitempty"`
	Service         ServiceFee `json:"service"`
	// FreeMinutes waives the whole session when it ends within this many
	// minutes. Zero disables the waiver.
	FreeMinutes int `json:"free_minutes"`
	// MinElectricCents floors the electric line only; the service line is never
	// used as a balance filler, unlike the old total-based minimum.
	MinElectricCents int64 `json:"min_electric_cents"`
	// LossRateBP inflates billable energy to cover line loss, 10000 = no loss.
	LossRateBP int32 `json:"loss_rate_bp"`
	// DefaultChannel is used when a session does not report one.
	DefaultChannel Channel    `json:"default_channel,omitempty"`
	Multipliers    Multiplier `json:"multipliers,omitempty"`
}

// Sample is one contiguous stretch of usage that stays inside a single rate
// window. Callers that cannot guarantee this must split before calling Cost.
type Sample struct {
	Start    time.Time
	End      time.Time
	EnergyWh uint64
	// PowerW is the average power over the sample. Zero means "derive it from
	// energy and duration", which is what estimation has to do.
	PowerW uint32
}

type Usage struct {
	Start    time.Time
	End      time.Time
	EnergyWh uint64
	Samples  []Sample
	Channel  Channel
}

func (u Usage) channel(spec Spec) Channel {
	if u.Channel != "" {
		return u.Channel
	}
	if spec.DefaultChannel != "" {
		return spec.DefaultChannel
	}
	return ChannelDefault
}

func (u Usage) minutes() int64 {
	if u.Start.IsZero() || u.End.IsZero() || !u.End.After(u.Start) {
		return 0
	}
	return int64(u.End.Sub(u.Start) / time.Minute)
}

// Fee is the priced result. Basis is echoed so callers can render the right
// unit without re-deriving it.
type Fee struct {
	Basis         Basis  `json:"basis"`
	ElectricCents int64  `json:"electric_cents"`
	ServiceCents  int64  `json:"service_cents"`
	TotalCents    int64  `json:"total_cents"`
	BillableWh    uint64 `json:"billable_wh"`
}

const maxRateCents = 1000000

// ValidateSpec is shared by publication and pricing: a tariff the editor
// accepts must always be one the settlement path can actually execute.
func ValidateSpec(spec Spec) error {
	switch spec.Basis {
	case BasisEnergy, BasisPowerTier, BasisMaxPower, BasisPerMinute, BasisPerSession:
	default:
		return ErrInvalidPricing
	}
	if spec.TierPriceBasis == "" {
		spec.TierPriceBasis = TierPerHourAtCeiling
	}
	switch spec.TierPriceBasis {
	case TierPerHourAtCeiling, TierPerKWh:
	default:
		return ErrInvalidPricing
	}
	if spec.FreeMinutes < 0 || spec.FreeMinutes > 1440 {
		return ErrInvalidPricing
	}
	if spec.MinElectricCents < 0 || spec.MinElectricCents > maxRateCents {
		return ErrInvalidPricing
	}
	if spec.LossRateBP < 0 || spec.LossRateBP > 100000 {
		return ErrInvalidPricing
	}
	for _, bp := range []int32{spec.Multipliers.TempElectricBP, spec.Multipliers.TempServiceBP,
		spec.Multipliers.CardElectricBP, spec.Multipliers.CardServiceBP} {
		if bp < 0 || bp > 100000 {
			return ErrInvalidPricing
		}
	}
	for _, amount := range []int64{spec.MaxPowerCentsPerHourPerKW, spec.PerMinuteCents, spec.PerSessionCents} {
		if amount < 0 || amount > maxRateCents {
			return ErrInvalidPricing
		}
	}
	for _, amount := range []int64{spec.Service.CentsPerKWh, spec.Service.CentsPerMinute,
		spec.Service.CentsPerHour, spec.Service.CentsPerSession} {
		if amount < 0 || amount > maxRateCents {
			return ErrInvalidPricing
		}
	}
	switch spec.Service.Mode {
	case "", ServiceNone:
		// An unset service mode means no service fee, so a zero-value Spec is
		// still a coherent tariff.
		spec.Service.Mode = ServiceNone
	case ServiceEnergy, ServiceMinute, ServiceMinutePower, ServiceSession:
	default:
		return ErrInvalidPricing
	}
	if spec.Basis == BasisPowerTier {
		if len(spec.Tiers) == 0 || len(spec.Tiers) > 32 {
			return ErrInvalidPricing
		}
		previousHigh := int64(-1)
		for _, tier := range spec.Tiers {
			if tier.HighW == 0 || int64(tier.LowW) >= int64(tier.HighW) || tier.HighW > 100000 {
				return ErrInvalidPricing
			}
			if int64(tier.LowW) <= previousHigh {
				return ErrInvalidPricing
			}
			if tier.CentsPerHour < 0 || tier.CentsPerHour > maxRateCents ||
				tier.ServiceCentsPerHour < 0 || tier.ServiceCentsPerHour > maxRateCents {
				return ErrInvalidPricing
			}
			previousHigh = int64(tier.HighW)
		}
	} else if len(spec.Tiers) > 0 {
		return ErrInvalidPricing
	}
	if spec.Basis == BasisPerSession || spec.Basis == BasisPerMinute {
		// These bases ignore time-of-use entirely, so requiring a full-day
		// window table would be noise the editor must fill in for nothing.
		if len(spec.Windows) > 0 {
			return ErrInvalidPricing
		}
		return nil
	}
	if spec.Basis == BasisMaxPower {
		// Peak-power pricing bills one peak against the whole session, so a
		// mid-session tariff change would have to be time-weighted. Rather
		// than implement that quietly, require a single all-day rate.
		if len(spec.Windows) != 1 || spec.Windows[0].Start != "00:00" || spec.Windows[0].End != "24:00" {
			return ErrInvalidPricing
		}
		if spec.Windows[0].CentsPerHourPerKW < 0 || spec.Windows[0].CentsPerHourPerKW > maxRateCents {
			return ErrInvalidPricing
		}
	}
	_, err := compileWindows(spec.Windows)
	return err
}

func compileWindows(windows []Window) ([1440]Window, error) {
	var schedule [1440]Window
	var filled [1440]bool
	if len(windows) == 0 || len(windows) > 48 {
		return schedule, ErrInvalidPricing
	}
	for _, window := range windows {
		start, err := clockMinute(window.Start)
		if err != nil || start == 1440 {
			return schedule, ErrInvalidPricing
		}
		end, err := clockMinute(window.End)
		if err != nil || end == start || window.CentsPerKWh < 0 || window.CentsPerKWh > maxRateCents {
			return schedule, ErrInvalidPricing
		}
		if window.ServiceCentsPerKWh != nil && (*window.ServiceCentsPerKWh < 0 || *window.ServiceCentsPerKWh > maxRateCents) {
			return schedule, ErrInvalidPricing
		}
		if end < start {
			end += 1440
		}
		if end-start > 1440 {
			return schedule, ErrInvalidPricing
		}
		for minute := start; minute < end; minute++ {
			index := minute % 1440
			if filled[index] {
				return schedule, ErrInvalidPricing
			}
			filled[index] = true
			schedule[index] = window
		}
	}
	for _, isFilled := range filled {
		if !isFilled {
			return schedule, ErrInvalidPricing
		}
	}
	return schedule, nil
}

// Period is the legacy time-of-use pair kept for the admin API payloads that
// still speak in period/price language.
type Period struct {
	Period             string `json:"period"`
	Start              string `json:"start"`
	End                string `json:"end"`
	ElectricPriceCents int64  `json:"electric_price_cents"`
	ServicePriceCents  *int64 `json:"service_price_cents,omitempty"`
}

func clockMinute(value string) (int, error) {
	if len(value) != 5 || value[2] != ':' || value[0] < '0' || value[0] > '9' || value[1] < '0' || value[1] > '9' || value[3] < '0' || value[3] > '9' || value[4] < '0' || value[4] > '9' {
		return 0, ErrInvalidPricing
	}
	hour, err := strconv.Atoi(value[:2])
	if err != nil {
		return 0, ErrInvalidPricing
	}
	minute, err := strconv.Atoi(value[3:])
	if err != nil || hour > 24 || minute > 59 || (hour == 24 && minute != 0) {
		return 0, ErrInvalidPricing
	}
	return hour*60 + minute, nil
}

// specIsUniformOver reports whether every minute of the session prices the same
// way. Where it does, distributing energy evenly is exact rather than an
// assumption, so an unsegmented meter can be settled without review.
func specIsUniformOver(spec Spec, start, end time.Time) bool {
	if spec.Basis != BasisEnergy && spec.Basis != BasisPowerTier {
		// Peak, per-minute and per-session tariffs do not read the rate off the
		// clock, so a flat spread never misprices them.
		return true
	}
	schedule, err := compileWindows(spec.Windows)
	if err != nil {
		return false
	}
	var first *Window
	at := start.In(beijing).Truncate(time.Minute)
	for !at.After(end.In(beijing).Truncate(time.Minute)) {
		window := schedule[at.Hour()*60+at.Minute()]
		if first == nil {
			first = &window
		} else if window.CentsPerKWh != first.CentsPerKWh {
			return false
		}
		at = at.Add(time.Minute)
	}
	return true
}

var energyPattern = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,3})?$`)

var (
	decimalZero     = decimal.Zero
	decimalOne      = decimal.NewFromInt(1)
	decimalThousand = decimal.NewFromInt(1000)
)

// parseEnergy bounds a quoted draw to what a two-wheeler session can plausibly
// consume, so a malformed or hostile input cannot reach the pricing engine.
func parseEnergy(value string) (decimal.Decimal, error) {
	kwh, err := decimal.NewFromString(value)
	if err != nil {
		return decimal.Zero, ErrInvalidPricing
	}
	if kwh.LessThan(decimal.New(1, -3)) || kwh.GreaterThan(decimal.NewFromInt(100)) {
		return decimal.Zero, ErrInvalidPricing
	}
	return kwh, nil
}
