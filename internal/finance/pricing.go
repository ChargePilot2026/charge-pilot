package finance

import (
	"errors"
	"math"

	"github.com/shopspring/decimal"
)

// EnergyUnit is 0.0001 kWh, matching the DECIMAL(12,4) meter fields.
type EnergyUnit int64

type EnergySlice struct {
	Energy              EnergyUnit
	ElectricCentsPerKWh int64
	ServiceCentsPerKWh  int64
}

type ChargeFee struct {
	ElectricCents Money
	ServiceCents  Money
	TotalCents    Money
}

var ErrInvalidTariff = errors.New("invalid tariff or energy")

// PriceEnergy keeps both fee lines separate through calculation and rounds each
// line once, half up to a cent, after summing all time-of-use slices.
func PriceEnergy(slices []EnergySlice) (ChargeFee, error) {
	if len(slices) == 0 {
		return ChargeFee{}, ErrInvalidTariff
	}
	electric := decimal.Zero
	service := decimal.Zero
	for _, slice := range slices {
		if slice.Energy < 0 || slice.ElectricCentsPerKWh < 0 || slice.ServiceCentsPerKWh < 0 {
			return ChargeFee{}, ErrInvalidTariff
		}
		energyKWh := decimal.New(int64(slice.Energy), -4)
		electric = electric.Add(energyKWh.Mul(decimal.NewFromInt(slice.ElectricCentsPerKWh)))
		service = service.Add(energyKWh.Mul(decimal.NewFromInt(slice.ServiceCentsPerKWh)))
	}
	e, ok := roundedCents(electric)
	if !ok {
		return ChargeFee{}, ErrInvalidTariff
	}
	s, ok := roundedCents(service)
	if !ok || e > math.MaxInt64-s {
		return ChargeFee{}, ErrInvalidTariff
	}
	return ChargeFee{ElectricCents: e, ServiceCents: s, TotalCents: e + s}, nil
}

func roundedCents(value decimal.Decimal) (Money, bool) {
	adjusted := value.Round(0).BigInt()
	if !adjusted.IsInt64() {
		return 0, false
	}
	return Money(adjusted.Int64()), true
}
