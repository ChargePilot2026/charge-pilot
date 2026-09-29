package pricing

// PriceOfferActual is the settlement entry point the billing and charge flows
// call. It is a thin adapter over SettleSession rather than a second pricing
// path: the device-billed case is decided by the amount that was actually
// collected, and having that logic live in two places is how a quote and a bill
// end up disagreeing about which one they used.
func PriceOfferActual(rule Rule, offer *Offer, meter ActualMeter) (ActualFee, error) {
	settlement, err := SettleSession(rule.Spec, meter, offer, ActualFromMeter(meter))
	if err != nil {
		return ActualFee{}, err
	}
	return ActualFee{
		ElectricCents: settlement.ElectricCents,
		ServiceCents:  settlement.ServiceCents,
		TotalCents:    settlement.TotalCents,
	}, nil
}

// ActualFromMeter derives what the device reported from the meter record.
//
// The meter is the platform's own view, so this is an inferred actual rather
// than one read back from the board. It is passed as an actual precisely so
// that the settlement can be marked estimated — the two are different claims
// and the difference has to survive into the receipt.
func ActualFromMeter(meter ActualMeter) *SessionActual {
	actual := &SessionActual{
		UsedSeconds: meter.ChargedSeconds,
		UsedMilliWh: uint64(meter.ChargedWh) * 1000,
		Reported:    false,
	}
	for _, segment := range meter.Segments {
		if uint32(segment.PeakW) > actual.PeakWatts {
			actual.PeakWatts = segment.PeakW
		}
	}
	return actual
}
