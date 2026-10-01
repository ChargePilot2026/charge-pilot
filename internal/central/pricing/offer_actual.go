package pricing

// PriceOfferActual 将套餐实收及计量适配为 SettleSession 输入，供充电和计费流程共用。
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

// ActualFromMeter retains the actual duration and energy reported by the device.
func ActualFromMeter(meter ActualMeter) *SessionActual {
	actual := &SessionActual{
		UsedSeconds: meter.ChargedSeconds,
		UsedMilliWh: uint64(meter.ChargedWh) * 1000,
		Reported:    true,
	}
	for _, segment := range meter.Segments {
		if uint32(segment.PeakW) > actual.PeakWatts {
			actual.PeakWatts = segment.PeakW
		}
	}
	return actual
}
