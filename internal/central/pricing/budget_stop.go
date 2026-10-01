package pricing

import "errors"

// BudgetStopAtMeter keeps stopping separate from final settlement. Missing
// power fragments must not disable a budget forever: price a conservative
// lower bound, and stop only once even that bound reaches the purchase price.
// A final fee is returned only when both bounds agree, or strict pricing works.
func BudgetStopAtMeter(rule Rule, offer *Offer, meter ActualMeter) (StopPlan, *Fee, error) {
	if offer == nil || !offer.Valid() || offer.Mode != "amount" || !rule.Spec.Mode.ServerBilled() {
		return StopPlan{}, nil, ErrInvalidPricing
	}
	if scheme := rule.Spec.Scheme; scheme != nil {
		p, ok := scheme.Package(offer.PackageID)
		if !ok || p.Mode != offer.Mode || p.PriceCents != offer.PriceCents {
			return StopPlan{}, nil, ErrInvalidPricing
		}
	}
	fee, err := PriceActual(rule, meter)
	exact := err == nil
	if errors.Is(err, ErrMeterReview) && !meter.ReviewRequired && len(meter.Segments) > 0 {
		fee, err = budgetBound(rule, meter, false)
		if err == nil {
			upper, upperErr := budgetBound(rule, meter, true)
			exact = upperErr == nil && fee.ElectricCents == upper.ElectricCents && fee.ServiceCents == upper.ServiceCents
		}
	}
	if err != nil {
		return StopPlan{}, nil, err
	}
	plan := StopPlan{AccruedCents: fee.TotalCents, ShouldStop: fee.TotalCents >= offer.PriceCents}
	if !plan.ShouldStop {
		return plan, nil, nil
	}
	plan.Reason = "budget_exhausted"
	if !exact {
		// A stop decision does not authorize inventing the electric/service split.
		return plan, nil, nil
	}
	electric, service := splitFee(fee, offer.PriceCents)
	return plan, &Fee{ElectricCents: electric, ServiceCents: service, TotalCents: electric + service}, nil
}

// Bounds reuse the normal engine's complete-minute rounding, time periods and
// charging policy. Only the temporary tariff/evidence copies are changed;
// original telemetry and the final settlement meter remain untouched.
func budgetBound(rule Rule, meter ActualMeter, upper bool) (Fee, error) {
	if ValidateSpec(rule.Spec) != nil || rule.Spec.Electric == nil {
		return Fee{}, ErrInvalidPricing
	}
	line := *rule.Spec.Electric
	line.Periods = append([]Period(nil), line.Periods...)
	for pi := range line.Periods {
		period := &line.Periods[pi]
		if rule.Spec.Mode == ModeServerEnergy {
			// Unknown boundary energy gets the cheapest (or most expensive)
			// possible unit rates, never energy apportioned by elapsed time.
			for _, candidate := range rule.Spec.Electric.Periods {
				period.ElectricCents = boundRate(period.ElectricCents, candidate.ElectricCents, upper)
				period.ServiceCents = boundRate(period.ServiceCents, candidate.ServiceCents, upper)
			}
			continue
		}
		period.Tiers = append([]Tier(nil), period.Tiers...)
		for ti := range period.Tiers {
			first := ti
			if upper || rule.Spec.Mode == ModeServerRealtimePower && ti == 0 {
				first = 0
			}
			if rule.Spec.Mode == ModeServerRealtimePower && !upper && ti > 0 {
				continue
			}
			// For peak power, an unobserved peak can only be higher than the
			// observed one. Non-monotonic tier prices still need a lower bound.
			for _, candidate := range rule.Spec.Electric.Periods[pi].Tiers[first:] {
				period.Tiers[ti].ElectricCents = boundRate(period.Tiers[ti].ElectricCents, candidate.ElectricCents, upper)
				period.Tiers[ti].ServiceCents = boundRate(period.Tiers[ti].ServiceCents, candidate.ServiceCents, upper)
			}
		}
	}
	rule.Spec.Electric = &line
	meter.Segments = append([]MeterSegment(nil), meter.Segments...)
	zero := uint32(0)
	for i := range meter.Segments {
		segment := &meter.Segments[i]
		if segment.PowerW == nil && (rule.Spec.Mode == ModeServerRealtimePower || segment.PeakW == 0) {
			segment.PowerW = &zero
		}
	}
	return PriceActual(rule, meter)
}

func boundRate(a, b int64, upper bool) int64 {
	if upper {
		return max(a, b)
	}
	return min(a, b)
}
