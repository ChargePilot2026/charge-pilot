package pricing

import (
	"math"
	"time"

	"github.com/shopspring/decimal"
)

const (
	// One slice per minute keeps a week-long session bounded while staying
	// fine enough that a tariff boundary is never approximated.
	maxSlices = 7 * 24 * 60
	// Line loss is quoted in basis points, so a 500 BP setting is 5%.
	maxLossRateBP = 100000
)

// Cost is the one and only pricing implementation. Estimation and settlement
// both feed a Usage into this function, so a quote can never be priced by
// different arithmetic than the eventual bill.
func Cost(spec Spec, usage Usage) (Fee, error) {
	if ValidateSpec(spec) != nil {
		return Fee{}, ErrInvalidPricing
	}
	if usage.Start.IsZero() || usage.End.Before(usage.Start) || usage.End.Sub(usage.Start) > 7*24*time.Hour {
		return Fee{}, ErrInvalidPricing
	}
	fee := Fee{Basis: spec.Basis}
	// A session that never drew power bills nothing. Unused prepaid packages
	// land here and must refund in full.
	if usage.EnergyWh == 0 {
		return fee, nil
	}
	// The free window is a waiver of the whole session, not a discount, so it
	// short-circuits before any rate is looked up.
	if spec.FreeMinutes > 0 && usage.minutes() <= int64(spec.FreeMinutes) {
		return fee, nil
	}
	channel := usage.channel(spec)
	billable := applyLossRate(usage.EnergyWh, spec.LossRateBP)
	// Loss is applied as an exact decimal factor on every slice. Scaling each
	// slice with integer rounding first would over-charge a long session by a
	// cent per slice, because the meter splits into one slice per minute.
	lossFactor := decimal.NewFromInt(int64(10000 + clampLossRate(spec.LossRateBP))).Div(decimal.NewFromInt(10000))

	electric := decimal.Zero
	service := decimal.Zero

	switch spec.Basis {
	case BasisPerSession:
		electric = decimal.NewFromInt(spec.PerSessionCents)
	case BasisPerMinute:
		electric = decimal.NewFromInt(spec.PerMinuteCents).Mul(decimal.NewFromInt(usage.minutes()))
	case BasisMaxPower:
		peak := uint32(0)
		for _, sample := range usage.Samples {
			if power := effectivePower(sample); power > peak {
				peak = power
			}
		}
		if peak == 0 {
			return Fee{}, ErrInvalidPricing
		}
		hours := decimal.NewFromInt(int64(usage.End.Sub(usage.Start))).Div(decimal.NewFromInt(int64(time.Hour)))
		rate := decimal.NewFromInt(spec.Windows[0].CentsPerHourPerKW).Mul(decimal.New(int64(peak), -3))
		electric = rate.Mul(hours)
	case BasisEnergy, BasisPowerTier:
		schedule, err := compileWindows(spec.Windows)
		if err != nil {
			return Fee{}, ErrInvalidPricing
		}
		slices, err := splitByWindow(usage, schedule)
		if err != nil {
			return Fee{}, ErrInvalidPricing
		}
		for _, slice := range slices {
			window := schedule[slice.Start.In(beijing).Hour()*60+slice.Start.In(beijing).Minute()]
			kwh := decimal.New(int64(slice.EnergyWh), -3).Mul(lossFactor)
			if spec.Basis == BasisEnergy {
				electric = electric.Add(kwh.Mul(decimal.NewFromInt(window.CentsPerKWh)))
			} else {
				tier, ok := spec.findTier(effectivePower(slice))
				rate := window.CentsPerKWh
				if ok {
					rate = spec.tierCentsPerKWh(tier)
				}
				electric = electric.Add(kwh.Mul(decimal.NewFromInt(rate)))
			}
			if spec.Service.Mode == ServiceEnergy {
				rate := spec.Service.CentsPerKWh
				if window.ServiceCentsPerKWh != nil {
					rate = *window.ServiceCentsPerKWh
				}
				service = service.Add(kwh.Mul(decimal.NewFromInt(rate)))
			}
			if spec.Service.Mode == ServiceMinutePower {
				if tier, ok := spec.findTier(effectivePower(slice)); ok {
					service = service.Add(kwh.Mul(decimal.NewFromInt(spec.tierServiceCentsPerKWh(tier))))
				}
			}
		}
	default:
		return Fee{}, ErrInvalidPricing
	}

	switch spec.Service.Mode {
	case ServiceMinute:
		service = decimal.NewFromInt(spec.Service.CentsPerMinute).Mul(decimal.NewFromInt(usage.minutes()))
	case ServiceSession:
		service = decimal.NewFromInt(spec.Service.CentsPerSession)
	case ServiceNone, ServiceEnergy, ServiceMinutePower:
		// Already accumulated above, or intentionally nothing.
	}

	electric = electric.Mul(decimal.NewFromInt(int64(spec.Multipliers.electricBP(channel)))).Div(decimal.NewFromInt(10000))
	service = service.Mul(decimal.NewFromInt(int64(spec.Multipliers.serviceBP(channel)))).Div(decimal.NewFromInt(10000))

	electricCents, ok := toCents(electric)
	if !ok {
		return Fee{}, ErrInvalidPricing
	}
	serviceCents, ok := toCents(service)
	if !ok {
		return Fee{}, ErrInvalidPricing
	}
	// The floor tops up the electric line only. Padding the service line, as the
	// old total-based minimum did, produced invoices whose service fee had
	// nothing to do with the published tariff.
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

// splitByWindow cuts samples at every minute boundary so each returned slice
// prices entirely under one tariff window. Power is a per-sample average, so it
// is carried across unchanged; energy is split with the remainder landing on
// the final slice so the parts always sum back to the whole.
func splitByWindow(usage Usage, schedule [1440]Window) ([]Sample, error) {
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
			// Sub-minute tails are folded into the last slice so no energy is
			// silently dropped from the bill.
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

func effectivePower(sample Sample) uint32 {
	if sample.PowerW > 0 {
		return sample.PowerW
	}
	seconds := sample.End.Sub(sample.Start).Seconds()
	if seconds <= 0 {
		return 0
	}
	// Round to the nearest watt so a derived power never sits just under a
	// tier ceiling it actually reached.
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

// findTier returns the band containing watts. Tiers are closed intervals, the
// way the industry quotes them ("0~200W", "201~250W"), so a reading that lands
// exactly on a ceiling stays in the cheaper band below it.
func (s Spec) findTier(watts uint32) (Tier, bool) {
	for _, tier := range s.Tiers {
		if watts >= tier.LowW && watts <= tier.HighW {
			return tier, true
		}
	}
	return Tier{}, false
}

// tierCentsPerKWh converts a tier's cents-per-hour rate into the equivalent
// cents per kWh. Which conversion applies is a commercial decision stored in
// the spec, not an assumption hidden in the math.
func (s Spec) tierCentsPerKWh(tier Tier) int64 {
	if s.TierPriceBasis == TierPerKWh {
		return tier.CentsPerHour
	}
	return tier.CentsPerHour * int64(tier.HighW) / 1000
}

func (s Spec) tierServiceCentsPerKWh(tier Tier) int64 {
	if s.Service.Mode != ServiceMinutePower {
		return 0
	}
	if s.TierPriceBasis == TierPerKWh {
		return tier.ServiceCentsPerHour
	}
	return tier.ServiceCentsPerHour * int64(tier.HighW) / 1000
}

func toCents(value decimal.Decimal) (int64, bool) {
	rounded := value.Round(0).BigInt()
	if !rounded.IsInt64() {
		return 0, false
	}
	return rounded.Int64(), true
}
