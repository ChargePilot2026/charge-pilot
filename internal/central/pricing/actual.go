package pricing

import (
	"time"
)

type MeterSegment struct {
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	EnergyWh  uint32    `json:"energy_wh"`
	// PeakW is the highest power reported inside the segment. It is what makes
	// power-tier and peak-power tariffs computable from the meter; when it is
	// zero the engine derives an average instead.
	PeakW uint32 `json:"peak_w,omitempty"`
}

type ActualMeter struct {
	StartedAt      time.Time      `json:"started_at"`
	EndedAt        time.Time      `json:"ended_at"`
	ChargedWh      uint32         `json:"charged_wh"`
	ChargedSeconds uint32         `json:"charged_seconds"`
	Segments       []MeterSegment `json:"segments,omitempty"`
}

type ActualFee = Fee

// PriceActual never treats the uniformly distributed estimate as a meter.
// Energy segments may span boundaries only where both effective rates match.
// Missing or contradictory variable-rate readings are sent to review.
//
// The arithmetic itself is Cost's: this function only decides whether the
// reported meter is trustworthy enough to hand over.
func PriceActual(rule Rule, meter ActualMeter) (Fee, error) {
	if ValidateSpec(rule.Spec) != nil || rule.ID == 0 || rule.Version == 0 {
		return Fee{}, ErrInvalidPricing
	}
	if meter.StartedAt.IsZero() || meter.EndedAt.Before(meter.StartedAt) || meter.EndedAt.Sub(meter.StartedAt) > 7*24*time.Hour || time.Duration(meter.ChargedSeconds)*time.Second > meter.EndedAt.Sub(meter.StartedAt)+5*time.Minute {
		return Fee{}, ErrMeterReview
	}
	segments := meter.Segments
	if len(segments) == 0 {
		segments = []MeterSegment{{StartedAt: meter.StartedAt, EndedAt: meter.EndedAt, EnergyWh: meter.ChargedWh}}
	}
	if len(segments) > maxSlices {
		return Fee{}, ErrMeterReview
	}
	usage := Usage{Start: meter.StartedAt, End: meter.EndedAt, EnergyWh: uint64(meter.ChargedWh), Channel: rule.Channel}
	// Spreading an unsegmented meter evenly is only exact when the rate does
	// not change during the session. Under a varying tariff it is a guess, and
	// the guess decides what the operator is charged, so it goes to review
	// instead. This is what makes measured segments worth collecting.
	if len(meter.Segments) == 0 && !specIsUniformOver(rule.Spec, meter.StartedAt, meter.EndedAt) {
		return Fee{}, ErrMeterReview
	}
	totalWh := uint64(0)
	cursor := meter.StartedAt
	for _, segment := range segments {
		if !segment.StartedAt.Equal(cursor) || segment.EndedAt.Before(segment.StartedAt) || segment.EndedAt.After(meter.EndedAt) || segment.StartedAt.Equal(segment.EndedAt) && segment.EnergyWh > 0 {
			return Fee{}, ErrMeterReview
		}
		cursor = segment.EndedAt
		totalWh += uint64(segment.EnergyWh)
		usage.Samples = append(usage.Samples, Sample{Start: segment.StartedAt, End: segment.EndedAt,
			EnergyWh: uint64(segment.EnergyWh), PowerW: segment.PeakW})
	}
	if !cursor.Equal(meter.EndedAt) || totalWh != uint64(meter.ChargedWh) {
		return Fee{}, ErrMeterReview
	}
	// dc589 reports energy, not per-segment power, so tiered and peak tariffs
	// price off the segment average. Documented as a known accuracy limit; the
	// alternative (sending every power-tier invoice to review) would make the
	// basis unusable rather than approximate.
	return Cost(rule.Spec, usage)
}
