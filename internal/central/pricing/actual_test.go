package pricing

import (
	"errors"
	"testing"
	"time"
)

func TestActualFeesUseMeasuredEnergyAndSeconds(t *testing.T) {
	start := time.Date(2026, 9, 29, 11, 30, 0, 0, beijing)
	rule := Rule{ID: 1, Version: 1, Mode: "mixed", ServiceCentsPerKWh: 25, ServiceCentsPerMinute: 1, Periods: []Period{{Start: "00:00", End: "12:00", ElectricPriceCents: 50}, {Start: "12:00", End: "24:00", ElectricPriceCents: 100}}}
	m := ActualMeter{StartedAt: start, EndedAt: start.Add(time.Hour), ChargedWh: 1000, ChargedSeconds: 3600}
	if _, err := PriceActual(rule, m); !errors.Is(err, ErrMeterReview) {
		t.Fatalf("missing variable-rate meter must require review: %v", err)
	}
	m.Segments = []MeterSegment{{StartedAt: start, EndedAt: start.Add(30 * time.Minute), EnergyWh: 200}, {StartedAt: start.Add(30 * time.Minute), EndedAt: m.EndedAt, EnergyWh: 800}}
	fee, err := PriceActual(rule, m)
	if err != nil || fee.ElectricCents != 90 || fee.ServiceCents != 85 || fee.TotalCents != 175 {
		t.Fatalf("measured fee %+v %v", fee, err)
	}
	m.Segments[1].EnergyWh = 799
	if _, err := PriceActual(rule, m); !errors.Is(err, ErrMeterReview) {
		t.Fatal("inconsistent total accepted")
	}
}
func TestActualFeeBoundaryRoundingAndNoGap(t *testing.T) {
	start := time.Date(2026, 9, 29, 11, 59, 0, 0, beijing)
	rule := Rule{ID: 1, Version: 1, Mode: "mixed", ServiceCentsPerKWh: 1, ServiceCentsPerMinute: 1, Periods: []Period{{Start: "00:00", End: "12:00", ElectricPriceCents: 50}, {Start: "12:00", End: "24:00", ElectricPriceCents: 100}}}
	m := ActualMeter{StartedAt: start, EndedAt: start.Add(time.Minute), ChargedWh: 500, ChargedSeconds: 30}
	fee, err := PriceActual(rule, m)
	if err != nil || fee.ElectricCents != 25 || fee.ServiceCents != 1 {
		t.Fatalf("round service only once: %+v %v", fee, err)
	}
	m.EndedAt = m.EndedAt.Add(time.Nanosecond)
	if _, err := PriceActual(rule, m); !errors.Is(err, ErrMeterReview) {
		t.Fatal("boundary crossing accepted")
	}
	m.Segments = []MeterSegment{{StartedAt: start.Add(time.Second), EndedAt: m.EndedAt, EnergyWh: 500}}
	if _, err := PriceActual(rule, m); !errors.Is(err, ErrMeterReview) {
		t.Fatal("meter gap accepted")
	}
}
