package pricing

import "github.com/shopspring/decimal"

// PriceOfferActual freezes the purchased cap or package terms at payment time.
// A package charges only for used seconds; excess use never creates a debt.
func PriceOfferActual(rule Rule, offer *Offer, meter ActualMeter) (ActualFee, error) {
	base, err := PriceActual(rule, meter)
	if err != nil || offer == nil {
		return base, err
	}
	if !offer.Valid() || offer.StationID != rule.StationID {
		return ActualFee{}, ErrInvalidPricing
	}
	total := base.TotalCents
	if offer.Mode == "package" {
		seconds := int64(meter.ChargedSeconds)
		limit := int64(offer.DurationMinutes) * 60
		if seconds > limit {
			seconds = limit
		}
		total = decimal.NewFromInt(offer.PriceCents).Mul(decimal.NewFromInt(seconds)).Div(decimal.NewFromInt(limit)).Round(0).IntPart()
	} else if total > offer.PriceCents {
		total = offer.PriceCents
	}
	if total < 0 {
		return ActualFee{}, ErrInvalidPricing
	}
	if base.TotalCents == 0 {
		return ActualFee{ServiceCents: total, TotalCents: total}, nil
	}
	electric := decimal.NewFromInt(base.ElectricCents).Mul(decimal.NewFromInt(total)).Div(decimal.NewFromInt(base.TotalCents)).Round(0).IntPart()
	if electric < 0 || electric > total {
		return ActualFee{}, ErrInvalidPricing
	}
	return ActualFee{ElectricCents: electric, ServiceCents: total - electric, TotalCents: total}, nil
}
