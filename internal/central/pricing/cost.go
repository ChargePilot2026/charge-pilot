package pricing

import (
	"math"
	"time"

	"github.com/shopspring/decimal"
)

const (
	// One slice per minute keeps a week-long session bounded while staying
	// fine enough that a tariff boundary is never approximated.
	maxSlices     = 7 * 24 * 60
	maxLossRateBP = 100000
)

// Cost is the one and only server-side pricing implementation. Estimation and
// settlement both feed a Usage into this function, so a quote can never be
// priced by different arithmetic than the eventual bill.
//
// It refuses device-billed modes outright. On those the money was collected at
// payment time and the device spends it down; recomputing a number here would
// produce a second, different figure for a bill that has already been paid,
// and a second figure is a reconciliation incident.
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
	serviceBasis := serviceBasisOf(spec)

	electric := decimal.Zero
	service := decimal.Zero

	switch spec.Electric.Basis {
	case BasisEnergy:
		// An energy tariff has no ladder: every slice inside a period pays that
		// period's single rate.
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
		// Peak-power billing prices the whole session against the rung its peak
		// reached, so the rung's stored number is a cents-per-hour rate and is
		// multiplied by hours directly. Converting it to a per-kWh equivalent
		// here would be the wrong operation: that conversion only makes sense
		// when the rate is applied to a quantity of energy.
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
			// An energy-based service fee under a peak-power electricity tariff
			// still needs a quantity of energy, so it is priced off the metered
			// total rather than the peak.
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
		// Already accumulated above, or intentionally nothing.
	}

	// The card and temporary rate card is published against electricity only —
	// the real back office exposes it under the electricity-rate section and
	// nothing applies it to the service line. So the service total is used
	// exactly as priced.
	electric = electric.Mul(decimal.NewFromInt(int64(spec.Multiplier.electricBP(channel)))).Div(decimal.NewFromInt(10000))

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

func serviceBasisOf(spec Spec) ServiceBasis {
	if spec.Service == nil {
		return ServiceNone
	}
	return spec.Service.Basis
}

// tierFor returns the rung a reading falls in. A reading above the top rung
// still pays the top rate: the top rung is a ceiling and the industry treats
// exceeding it as the last band, so this is a fallback rather than an error.
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

// splitByPeriod cuts samples at every minute boundary so each returned slice
// prices entirely under one tariff period. Power is a per-sample average, so it
// is carried across unchanged; energy is split with the remainder landing on
// the final slice so the parts always sum back to the whole.
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

// findTier returns the rung containing watts. Rungs are closed intervals that
// meet end to end — the next rung starts one watt above this one's ceiling —
// so a reading exactly on a ceiling stays in the cheaper band below it.
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

// tierCentsPerKWh converts a tier's stored rate into the equivalent cents per
// kWh. Which conversion applies is a commercial decision stored in the spec,
// not an assumption hidden in the math. It only applies to per-slice energy
// pricing; peak-power pricing uses the stored number as-is.
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
