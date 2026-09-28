package pricing

import (
	"errors"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/finance"
	"github.com/shopspring/decimal"
)

var ErrMeterReview = errors.New("实际计量不足或矛盾，需补充分时计量后核算")

type MeterSegment struct {
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	EnergyWh  uint32    `json:"energy_wh"`
}
type ActualMeter struct {
	StartedAt      time.Time      `json:"started_at"`
	EndedAt        time.Time      `json:"ended_at"`
	ChargedWh      uint32         `json:"charged_wh"`
	ChargedSeconds uint32         `json:"charged_seconds"`
	Segments       []MeterSegment `json:"segments,omitempty"`
}
type ActualFee struct {
	ElectricCents int64 `json:"electric_cents"`
	ServiceCents  int64 `json:"service_cents"`
	TotalCents    int64 `json:"total_cents"`
}

// PriceActual never treats the uniformly distributed estimate as a meter.
// Energy segments may span boundaries only where both effective rates match.
// Missing or contradictory variable-rate readings are sent to review.
func PriceActual(rule Rule, meter ActualMeter) (ActualFee, error) {
	if ValidateRule(rule) != nil || rule.ID == 0 || rule.Version == 0 {
		return ActualFee{}, ErrInvalidPricing
	}
	if meter.StartedAt.IsZero() || meter.EndedAt.Before(meter.StartedAt) || meter.EndedAt.Sub(meter.StartedAt) > 7*24*time.Hour || time.Duration(meter.ChargedSeconds)*time.Second > meter.EndedAt.Sub(meter.StartedAt)+5*time.Minute {
		return ActualFee{}, ErrMeterReview
	}
	schedule, err := compilePeriods(rule.Periods)
	if err != nil {
		return ActualFee{}, err
	}
	segments := meter.Segments
	if len(segments) == 0 {
		segments = []MeterSegment{{StartedAt: meter.StartedAt, EndedAt: meter.EndedAt, EnergyWh: meter.ChargedWh}}
	}
	if len(segments) > 10080 {
		return ActualFee{}, ErrMeterReview
	}
	var totalWh uint64
	cursor := meter.StartedAt
	slices := make([]finance.EnergySlice, 0, len(segments))
	for _, segment := range segments {
		if !segment.StartedAt.Equal(cursor) || segment.EndedAt.Before(segment.StartedAt) || segment.EndedAt.After(meter.EndedAt) || segment.StartedAt.Equal(segment.EndedAt) && segment.EnergyWh > 0 {
			return ActualFee{}, ErrMeterReview
		}
		cursor = segment.EndedAt
		totalWh += uint64(segment.EnergyWh)
		start := segment.StartedAt.In(beijing)
		period := schedule[start.Hour()*60+start.Minute()]
		electric, service := actualRates(rule, period)
		// Midnight and every minute boundary use [start,end), so energy ending
		// exactly at a tariff change remains in the preceding tariff.
		for at := start.Truncate(time.Minute).Add(time.Minute); at.Before(segment.EndedAt); at = at.Add(time.Minute) {
			e, s := actualRates(rule, schedule[at.Hour()*60+at.Minute()])
			if segment.EnergyWh > 0 && (e != electric || s != service) {
				return ActualFee{}, ErrMeterReview
			}
		}
		slices = append(slices, finance.EnergySlice{Energy: finance.EnergyUnit(int64(segment.EnergyWh) * 10), ElectricCentsPerKWh: electric, ServiceCentsPerKWh: service})
	}
	if !cursor.Equal(meter.EndedAt) || totalWh != uint64(meter.ChargedWh) {
		return ActualFee{}, ErrMeterReview
	}
	fee, err := finance.PriceEnergy(slices)
	if err != nil {
		return ActualFee{}, ErrInvalidPricing
	}
	// Round the complete service line once, including fractional charged minutes.
	serviceExact := decimal.Zero
	for _, slice := range slices {
		serviceExact = serviceExact.Add(decimal.New(int64(slice.Energy), -4).Mul(decimal.NewFromInt(slice.ServiceCentsPerKWh)))
	}
	if rule.Mode == "minute" || rule.Mode == "mixed" {
		serviceExact = serviceExact.Add(decimal.NewFromInt(int64(meter.ChargedSeconds)).Mul(decimal.NewFromInt(rule.ServiceCentsPerMinute)).Div(decimal.NewFromInt(60)))
	}
	serviceRounded := serviceExact.Round(0).BigInt()
	if !serviceRounded.IsInt64() {
		return ActualFee{}, ErrInvalidPricing
	}
	service := serviceRounded.Int64()
	electric := int64(fee.ElectricCents)
	// Publication bounds and the uint32 meter limit keep the sum within int64.
	total := electric + service
	if total < rule.MinimumCents {
		service += rule.MinimumCents - total
		total = rule.MinimumCents
	}
	return ActualFee{ElectricCents: electric, ServiceCents: service, TotalCents: total}, nil
}
func actualRates(rule Rule, period Period) (int64, int64) {
	service := rule.ServiceCentsPerKWh
	if period.ServicePriceCents != nil {
		service = *period.ServicePriceCents
	}
	if rule.Mode == "minute" {
		service = 0
	}
	return period.ElectricPriceCents, service
}
