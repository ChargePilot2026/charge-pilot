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

// ActualFromMeter 从计量记录里推出设备上报了什么。
//
// 计量是平台自己的视角，所以这是一个推断出来的 actual，而不是从充电板回读
// 到的。它之所以仍以 actual 的身份传进去，正是为了让这次结算被标记为
// estimated——两者是不同的主张，而这个区别必须一路留到收据上。
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
