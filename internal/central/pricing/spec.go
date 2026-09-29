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

const maxRateCents = 1000000

// Tier is one rung of a power ladder. Only the ceiling is stored: the floor of
// a tier is always the previous tier's ceiling plus one watt, which is how the
// editor guarantees the bands cannot overlap. Storing both ends independently
// is what lets a tariff end up with a hole in it, and a hole in a ladder is a
// session that cannot be priced at all.
type Tier struct {
	// MaxWatts is the inclusive upper bound. The first tier must be 0 and the
	// values must strictly increase.
	MaxWatts int `json:"max_watts"`
	// ElectricCents is cents per kWh when the electric basis is energy, and
	// cents per hour when it is a power basis — see TierPriceBasis.
	ElectricCents int64 `json:"electric_cents"`
	// ServiceCents is only read when the service line is ServiceMinutePower.
	ServiceCents int64 `json:"service_cents,omitempty"`
}

// Period is one time-of-use slice of the day. The day is stored as a chain of
// end minutes rather than as independent start/end pairs, for the same reason
// the tiers are stored as ceilings: a chain cannot contain a gap, whereas a
// pair of times very easily can.
type Period struct {
	// EndMinute is minutes from midnight, 1..1440. Values must strictly
	// increase and the last one must be exactly 1440, so the day is covered
	// with no hole and no overlap.
	EndMinute int `json:"end_minute"`
	// ElectricCents is the single electricity rate for this period, and is the
	// only rate field when the basis is energy — an energy tariff has no
	// ladder, and offering one would be offering a field nothing reads.
	ElectricCents int64 `json:"electric_cents,omitempty"`
	// Tiers is the power ladder that applies inside this period, and is empty
	// when the electric basis is energy.
	Tiers []Tier `json:"tiers,omitempty"`
}

// ElectricLine prices the electricity itself. It is required for a
// server-billed mode and absent for a device-billed one, because on a
// device-billed session there is no electricity price anywhere in the system.
type ElectricLine struct {
	Basis   ServerBasis `json:"basis"`
	Periods []Period    `json:"periods"`
}

// ServiceLine prices the service fee. A nil *ServiceLine and a ServiceNone basis
// both mean no service fee; the pointer is kept so "not configured" and
// "configured as zero" stay distinguishable when an editor round-trips it.
type ServiceLine struct {
	Basis           ServiceBasis `json:"basis"`
	CentsPerKWh     int64        `json:"cents_per_kwh,omitempty"`
	CentsPerMinute  int64        `json:"cents_per_minute,omitempty"`
	CentsPerSession int64        `json:"cents_per_session,omitempty"`
}

// Channel is how the session was started; it selects the rate multipliers.
type Channel string

const (
	ChannelDefault Channel = "default"
	ChannelTemp    Channel = "temp"
	ChannelCard    Channel = "card"
)

// ChannelMultiplier holds basis points where 10000 means 1.0x. It applies to
// the electric line only: the real back office shows the card rate as a
// multiplier on the electricity charge and nothing else, so folding a service
// multiplier in here would invent a discount the tariff never published.
type ChannelMultiplier struct {
	TempBP int32 `json:"temp_bp"`
	CardBP int32 `json:"card_bp"`
}

func (m *ChannelMultiplier) electricBP(channel Channel) int32 {
	if m == nil {
		return 10000
	}
	if channel == ChannelTemp && m.TempBP > 0 {
		return m.TempBP
	}
	if channel == ChannelCard && m.CardBP > 0 {
		return m.CardBP
	}
	return 10000
}

// TimeCharge carries the settings that shape how a duration-billed session ends
// rather than what it costs.
type TimeCharge struct {
	// StopWhenFull ends the session when the battery is full instead of running
	// the clock out.
	StopWhenFull bool `json:"stop_when_full"`
	// MaxMinutes caps a duration session. Zero means uncapped.
	MaxMinutes uint16 `json:"max_minutes,omitempty"`
	// FloatPowerDeciWatts and FloatSeconds describe the low-current tail after
	// the battery stops accepting full current.
	FloatPowerDeciWatts uint16 `json:"float_power_deci_watts,omitempty"`
	FloatSeconds        uint16 `json:"float_seconds,omitempty"`
}

// Spec is the complete, self-contained description of how a session is priced.
// Estimation and settlement both run this same structure through Cost, so the
// two can never drift apart.
type Spec struct {
	Mode ChargeMode `json:"mode"`
	// Electric is read for server-billed modes only. Device-billed modes leave
	// it zero and ValidateSpec rejects one that tries to set it, because a
	// rate on a device-billed tariff is a rate that will never be used and
	// someone will eventually believe it is.
	Electric *ElectricLine `json:"electric,omitempty"`
	// Service is the second, independent line. Either may combine with the
	// other freely — energy-based electricity with hourly service is a real
	// and common combination.
	Service *ServiceLine `json:"service,omitempty"`
	// Multiplier is a rate card of its own and belongs to the electric line.
	Multiplier *ChannelMultiplier `json:"multiplier,omitempty"`
	// TierPriceBasis is only read by BasisRealtimePower, where a stored
	// cents-per-hour rung has to become cents per kWh before it can be applied
	// to a slice of energy. It stays an explicit stored choice because the
	// industry conversion is a commercial decision, not an arithmetic fact.
	TierPriceBasis TierPriceBasis `json:"tier_price_basis,omitempty"`
	// LossRateBP inflates billable energy to cover line loss; 10000 is no loss.
	LossRateBP int32 `json:"loss_rate_bp,omitempty"`
	// FreeMinutes waives the whole session when it ends within this many
	// minutes. Zero disables the waiver.
	FreeMinutes int `json:"free_minutes,omitempty"`
	// MinElectricCents floors the electric line only. The service line is never
	// used as a balance filler.
	MinElectricCents int64 `json:"min_electric_cents,omitempty"`
	// TimeCharge only applies to duration-billed sessions.
	TimeCharge *TimeCharge `json:"time_charge,omitempty"`
	// SpendCapCents is the optional ceiling a running server-billed session is
	// stopped at. Zero means no ceiling, in which case the session ends when
	// the time or energy the platform granted runs out. It is a product control,
	// not a protocol requirement: the board still honours its own allowance.
	SpendCapCents int64 `json:"spend_cap_cents,omitempty"`
	// StopGraceSeconds is how long the platform waits after asking the device
	// to stop before treating the session as unbounded.
	StopGraceSeconds int `json:"stop_grace_seconds,omitempty"`
	// DefaultChargeWay is the default way a session is authorised on this
	// device. It is presentation and policy, not arithmetic.
	DefaultChargeWay string `json:"default_charge_way,omitempty"`
	// CardMaxMinutes is the longest card-started session allowed.
	CardMaxMinutes uint16 `json:"card_max_minutes,omitempty"`
	// Display is what the mini program may reveal. It never changes what is
	// charged, which is why it sits beside the spec rather than inside it.
	Display Display `json:"display,omitempty"`
}

// Sample is one contiguous stretch of usage that stays inside a single rate
// period. Callers that cannot guarantee this must split before calling Cost.
type Sample struct {
	Start    time.Time
	End      time.Time
	EnergyWh uint64
	// PowerW is the power over the sample. Zero means "derive it from energy
	// and duration", which is what estimation has to do.
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
	Basis         ServerBasis `json:"basis"`
	ElectricCents int64       `json:"electric_cents"`
	ServiceCents  int64       `json:"service_cents"`
	TotalCents    int64       `json:"total_cents"`
	BillableWh    uint64      `json:"billable_wh"`
}

// ValidateSpec is shared by publication and by pricing: a tariff the editor
// accepts must always be one the settlement path can actually execute.
func ValidateSpec(spec Spec) error {
	if !spec.Mode.Valid() {
		return ErrInvalidPricing
	}
	if spec.FreeMinutes < 0 || spec.FreeMinutes > 1440 {
		return ErrInvalidPricing
	}
	if spec.MinElectricCents < 0 || spec.MinElectricCents > maxRateCents {
		return ErrInvalidPricing
	}
	if spec.SpendCapCents < 0 || spec.SpendCapCents > maxRateCents {
		return ErrInvalidPricing
	}
	if spec.StopGraceSeconds < 0 || spec.StopGraceSeconds > 3600 {
		return ErrInvalidPricing
	}
	if spec.LossRateBP < 0 || spec.LossRateBP > 100000 {
		return ErrInvalidPricing
	}
	if spec.Multiplier != nil {
		for _, bp := range []int32{spec.Multiplier.TempBP, spec.Multiplier.CardBP} {
			if bp < 0 || bp > 100000 {
				return ErrInvalidPricing
			}
		}
	}
	if spec.CardMaxMinutes > 999 {
		// The firmware's card session is one unsigned 16-bit field counted in
		// minutes, and a value above this is rejected by the board rather than
		// silently truncated.
		return ErrInvalidPricing
	}
	if spec.TimeCharge != nil {
		if spec.TimeCharge.MaxMinutes > 999 || spec.TimeCharge.FloatSeconds > 10800 || spec.TimeCharge.FloatPowerDeciWatts > 500 {
			return ErrInvalidPricing
		}
	}
	if spec.Service != nil {
		if !spec.Service.Basis.Valid() {
			return ErrInvalidPricing
		}
		for _, amount := range []int64{spec.Service.CentsPerKWh, spec.Service.CentsPerMinute, spec.Service.CentsPerSession} {
			if amount < 0 || amount > maxRateCents {
				return ErrInvalidPricing
			}
		}
	}
	if !spec.Mode.ServerBilled() {
		// A device-billed tariff carries no rates at all. Rejecting stray ones
		// is cheaper than shipping a tariff that reads as priced when it is not.
		if spec.Electric != nil || spec.Service != nil || spec.Multiplier != nil {
			return ErrInvalidPricing
		}
		return nil
	}
	if spec.Electric == nil {
		return ErrInvalidPricing
	}
	if spec.Electric.Basis != spec.Mode.BasisFor() {
		// The mode and the tariff must describe the same tariff. A device
		// configured for peak power but holding an energy tariff would bill by
		// something nobody agreed to.
		return ErrInvalidPricing
	}
	for _, period := range spec.Electric.Periods {
		if err := validateTiers(spec.Electric.Basis, period); err != nil {
			return err
		}
	}
	_, err := compilePeriods(spec.Electric.Periods)
	return err
}

// compilePeriods expands the stored chain into a per-minute lookup, and is
// where the chain invariant is enforced: strictly increasing end minutes that
// begin at zero and finish at 1440. That single rule makes a gap or an overlap
// unrepresentable, so there is no separate coverage check to get wrong.
func compilePeriods(periods []Period) ([1440]Period, error) {
	var schedule [1440]Period
	if len(periods) == 0 || len(periods) > 48 {
		return schedule, ErrInvalidPricing
	}
	cursor := 0
	for _, period := range periods {
		if period.EndMinute <= cursor || period.EndMinute > 1440 {
			return schedule, ErrInvalidPricing
		}
		for minute := cursor; minute < period.EndMinute; minute++ {
			schedule[minute] = period
		}
		cursor = period.EndMinute
	}
	if cursor != 1440 {
		return schedule, ErrInvalidPricing
	}
	return schedule, nil
}

// validateTiers enforces the ladder chain inside one period, and keeps the two
// shapes from coexisting. Whether a ladder is allowed at all is decided by the
// electric basis: energy has exactly one rate, the power bases have exactly one
// ladder, and a period carrying both is a tariff the operator cannot explain.
func validateTiers(basis ServerBasis, period Period) error {
	if period.ElectricCents < 0 || period.ElectricCents > maxRateCents {
		return ErrInvalidPricing
	}
	if basis == BasisEnergy {
		if len(period.Tiers) > 0 {
			return ErrInvalidPricing
		}
		return nil
	}
	if period.ElectricCents != 0 || len(period.Tiers) == 0 || len(period.Tiers) > 8 {
		return ErrInvalidPricing
	}
	previous := -1
	for _, tier := range period.Tiers {
		if tier.MaxWatts < 0 || tier.MaxWatts <= previous || tier.MaxWatts > 9990 {
			return ErrInvalidPricing
		}
		if tier.ElectricCents < 0 || tier.ElectricCents > maxRateCents ||
			tier.ServiceCents < 0 || tier.ServiceCents > maxRateCents {
			return ErrInvalidPricing
		}
		previous = tier.MaxWatts
	}
	return nil
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
	if spec.Electric == nil || (spec.Electric.Basis != BasisEnergy && spec.Electric.Basis != BasisRealtimePower) {
		// Peak-power and flat tariffs do not read the rate off the clock, so an
		// even spread never misprices them.
		return true
	}
	schedule, err := compilePeriods(spec.Electric.Periods)
	if err != nil {
		return false
	}
	var first *Period
	at := start.In(beijing).Truncate(time.Minute)
	for !at.After(end.In(beijing).Truncate(time.Minute)) {
		period := schedule[at.Hour()*60+at.Minute()]
		if first == nil {
			first = &period
		} else if !sameRate(*first, period) {
			return false
		}
		at = at.Add(time.Minute)
	}
	return true
}

// sameRate reports whether two periods price identically. It compares the
// single rate and the ladder both, because a basis carries exactly one of them
// and comparing only the ladder would make every energy tariff look uniform —
// which would let a session that spans a tariff change settle on a guess
// instead of going to review.
func sameRate(a, b Period) bool {
	if a.ElectricCents != b.ElectricCents || len(a.Tiers) != len(b.Tiers) {
		return false
	}
	for i := range a.Tiers {
		if a.Tiers[i] != b.Tiers[i] {
			return false
		}
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
