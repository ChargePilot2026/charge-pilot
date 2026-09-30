package pricing

// PriceOfferActual 是计费与充电两个流程调用的结算入口。它只是 SettleSession
// 之上的一层薄适配，而不是第二条计价路径：设备计费的情况由实际收走的金额
// 决定，把这套逻辑放在两个地方，正是报价与账单对不上「自己用的是哪一套」
// 的原因。
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
