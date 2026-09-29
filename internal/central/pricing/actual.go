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
	usage, err := usageFromMeter(rule, meter)
	if err != nil {
		return Fee{}, err
	}
	// dc589 reports energy, not per-segment power, so tiered and peak tariffs
	// price off the segment average. Documented as a known accuracy limit; the
	// alternative (sending every power-tier invoice to review) would make the
	// basis unusable rather than approximate.
	return Cost(rule.Spec, usage)
}

// usageFromMeter turns a meter record into the usage the engine costs.
//
// The validation is the substance here, not a formality: it decides whether the
// reported meter is trustworthy enough to hand to Cost. It lives in its own
// function because a spend cap has to be tested against exactly the same meter
// the settlement will use — a cap measured on a looser reading is a cap that
// fires at the wrong moment, and nobody would notice until an invoice was wrong.
func usageFromMeter(rule Rule, meter ActualMeter) (Usage, error) {
	if ValidateSpec(rule.Spec) != nil || rule.ID == 0 || rule.Version == 0 {
		return Usage{}, ErrInvalidPricing
	}
	if meter.StartedAt.IsZero() || meter.EndedAt.Before(meter.StartedAt) || meter.EndedAt.Sub(meter.StartedAt) > 7*24*time.Hour || time.Duration(meter.ChargedSeconds)*time.Second > meter.EndedAt.Sub(meter.StartedAt)+5*time.Minute {
		return Usage{}, ErrMeterReview
	}
	segments := meter.Segments
	if len(segments) == 0 {
		segments = []MeterSegment{{StartedAt: meter.StartedAt, EndedAt: meter.EndedAt, EnergyWh: meter.ChargedWh}}
	}
	if len(segments) > maxSlices {
		return Usage{}, ErrMeterReview
	}
	usage := Usage{Start: meter.StartedAt, End: meter.EndedAt, EnergyWh: uint64(meter.ChargedWh), Channel: rule.Channel}
	// Spreading an unsegmented meter evenly is only exact when the rate does
	// not change during the session. Under a varying tariff it is a guess, and
	// the guess decides what the operator is charged, so it goes to review
	// instead. This is what makes measured segments worth collecting.
	if len(meter.Segments) == 0 && !specIsUniformOver(rule.Spec, meter.StartedAt, meter.EndedAt) {
		return Usage{}, ErrMeterReview
	}
	totalWh := uint64(0)
	cursor := meter.StartedAt
	for _, segment := range segments {
		if !segment.StartedAt.Equal(cursor) || segment.EndedAt.Before(segment.StartedAt) || segment.EndedAt.After(meter.EndedAt) || segment.StartedAt.Equal(segment.EndedAt) && segment.EnergyWh > 0 {
			return Usage{}, ErrMeterReview
		}
		cursor = segment.EndedAt
		totalWh += uint64(segment.EnergyWh)
		usage.Samples = append(usage.Samples, Sample{Start: segment.StartedAt, End: segment.EndedAt,
			EnergyWh: uint64(segment.EnergyWh), PowerW: segment.PeakW})
	}
	if !cursor.Equal(meter.EndedAt) || totalWh != uint64(meter.ChargedWh) {
		return Usage{}, ErrMeterReview
	}
	return usage, nil
}

// StopAtMeter decides whether a running session has reached the spend cap its
// tariff declares, measured on a meter record.
//
// The meter goes through the same validation a settlement would, so a session is
// never cut off on a reading that could not have been billed. A tariff the
// engine can no longer price, or one that is not server-billed, produces a plan
// that says so rather than an error: a stop rule derived from a tariff the
// engine rejects is not a reason to cut somebody's charge off.
func StopAtMeter(rule Rule, meter ActualMeter) (StopPlan, error) {
	usage, err := usageFromMeter(rule, meter)
	if err != nil {
		return StopPlan{}, err
	}
	elapsed := meter.EndedAt.Sub(meter.StartedAt)
	return DecideStop(rule.Spec, usage, elapsed)
}
