package charge

import (
	"errors"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"testing"
	"time"
)

func TestMeasuredSegmentsUseElapsedCumulativeMeter(t *testing.T) {
	start := time.Date(2026, 9, 28, 3, 30, 0, 0, time.UTC)
	end := protocol.Event{DeviceID: "meter-test", Port: 1, StartedAt: start, EndedAt: start.Add(time.Hour), ChargedSeconds: 3600, EnergyMilliKWh: 1000}
	reading := protocol.Event{DeviceID: end.DeviceID, Type: protocol.Heartbeat, ReceivedAt: start.Add(30*time.Minute + 2*time.Second), ChargingPorts: []protocol.PortTelemetry{{Port: 1, ChargedSeconds: 1800, ChargedMWh: 200000}}}
	segments := measuredSegments(start, end, []protocol.Event{reading})
	if len(segments) != 2 || segments[0].EnergyWh != 200 || segments[1].EnergyWh != 800 || !segments[0].EndedAt.Equal(start.Add(30*time.Minute)) {
		t.Fatalf("measured segments %+v", segments)
	}
	rule := pricing.Rule{ID: 1, Version: 1, Spec: pricing.Spec{Mode: pricing.ModeServerEnergy, Electric: &pricing.ElectricLine{Basis: pricing.BasisEnergy, Periods: []pricing.Period{{EndMinute: 720, ElectricCents: 50}, {EndMinute: 1440, ElectricCents: 80}}}, Service: &pricing.ServiceLine{Basis: pricing.ServiceEnergy, CentsPerKWh: 25}}}
	fee, err := pricing.PriceActual(rule, pricing.ActualMeter{StartedAt: start, EndedAt: end.EndedAt, ChargedWh: 1000, ChargedSeconds: 3600, Segments: segments})
	if err != nil || fee.TotalCents != 99 {
		t.Fatalf("measured tariff %+v %v", fee, err)
	}
	// 这份上报是在费率分界之后才到的；
	// 真正定位边界前那 200 Wh 的是它自己的已用时钟，
	// 而不是到达时间。
	//
	// 没有分段数据时，引擎只能把电量均摊，
	// 而在会话中途会变的费率下，
	// 均摊等于在猜运维会被收多少钱。
	// 这种情况会转人工复核，而不是直接结算。
	if _, err := pricing.PriceActual(rule, pricing.ActualMeter{StartedAt: start, EndedAt: end.EndedAt, ChargedWh: 1000, ChargedSeconds: 3600}); !errors.Is(err, pricing.ErrMeterReview) {
		t.Fatalf("an unsegmented meter across a tariff change must go to review, got %v", err)
	}
	// 单一费率是另一种情况：
	// 均摊的结果是精确的，
	// 所以同一份计量无需人工过目就能结算。
	flat := pricing.Rule{ID: 1, Version: 1, Spec: pricing.Spec{Mode: pricing.ModeServerEnergy,
		Electric: &pricing.ElectricLine{Basis: pricing.BasisEnergy, Periods: []pricing.Period{{EndMinute: 1440, ElectricCents: 50}}}}}
	if _, err := pricing.PriceActual(flat, pricing.ActualMeter{StartedAt: start, EndedAt: end.EndedAt, ChargedWh: 1000, ChargedSeconds: 3600}); err != nil {
		t.Fatalf("a flat tariff must settle an unsegmented meter: %v", err)
	}
	tests := []struct {
		name   string
		change func(*protocol.Event, *protocol.Event)
	}{
		{"missing device start", func(e, r *protocol.Event) { e.StartedAt = time.Time{} }},
		{"paused device clock", func(e, r *protocol.Event) { e.ChargedSeconds-- }},
		{"device clock skew", func(e, r *protocol.Event) { e.StartedAt = e.StartedAt.Add(-time.Minute); e.ChargedSeconds += 60 }},
		{"stale report", func(e, r *protocol.Event) { r.ReceivedAt = r.ReceivedAt.Add(time.Minute) }},
		{"meter exceeds total", func(e, r *protocol.Event) { r.ChargingPorts[0].ChargedMWh = 1001000 }},
		{"fractional Wh", func(e, r *protocol.Event) { r.ChargingPorts[0].ChargedMWh++ }},
		{"C2 no meter", func(e, r *protocol.Event) { r.Type = protocol.Telemetry }},
		{"wrong port", func(e, r *protocol.Event) { r.ChargingPorts[0].Port = 2 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, r := end, reading
			r.ChargingPorts = append([]protocol.PortTelemetry(nil), reading.ChargingPorts...)
			tc.change(&e, &r)
			if got := measuredSegments(start, e, []protocol.Event{r}); len(got) != 0 {
				t.Fatalf("unsafe segments %+v", got)
			}
		})
	}
	backwards := reading
	backwards.ChargingPorts = []protocol.PortTelemetry{{Port: 1, ChargedSeconds: 1900, ChargedMWh: 100000}}
	backwards.ReceivedAt = start.Add(1900 * time.Second)
	if got := measuredSegments(start, end, []protocol.Event{reading, backwards}); got != nil {
		t.Fatal("meter rollback accepted")
	}
	duplicate := reading
	if got := measuredSegments(start, end, []protocol.Event{reading, duplicate}); len(got) != 2 {
		t.Fatal("identical measurement not deduplicated")
	}
	duplicate.ChargingPorts = []protocol.PortTelemetry{{Port: 1, ChargedSeconds: 1800, ChargedMWh: 201000}}
	if got := measuredSegments(start, end, []protocol.Event{reading, duplicate}); got != nil {
		t.Fatal("same instant conflicting meter accepted")
	}
}
