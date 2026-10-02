package charge

import (
	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"testing"
	"time"
)

func TestLiveMeterKeepsLastEvidenceWhenOfflineAndIsolatesPort(t *testing.T) {
	start := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	samples := []protocol.Event{}
	for i := 1; i <= 4; i++ {
		samples = append(samples, protocol.Event{Type: protocol.Heartbeat, DeviceID: "device", ReceivedAt: start.Add(time.Duration(i*15) * time.Second), ChargingPorts: []protocol.PortTelemetry{{Port: 1, ChargedSeconds: uint32(i * 15), ChargedMWh: uint32(i * 1000), PowerDeciWatts: 1500}, {Port: 2, ChargedSeconds: 1000, ChargedMWh: 900000, PowerDeciWatts: 4000}}})
	}
	view, meter, ok := liveMeterEvidence("device", 1, start, samples, start.Add(70*time.Second))
	if !ok || view.Stale || view.KWh != 0.004 || view.Seconds != 60 || meter.ChargedWh != 4 || len(meter.Segments) != 4 {
		t.Fatal(view, meter, ok)
	}
	if meter.Segments[0].PowerW == nil || *meter.Segments[0].PowerW != 150 {
		t.Fatal("initial estimate did not use observed load")
	}
	view, meter, ok = liveMeterEvidence("device", 1, start, samples, start.Add(time.Hour))
	if !ok || !view.Stale || view.Seconds != 60 || !meter.EndedAt.Equal(start.Add(time.Minute)) {
		t.Fatal("offline time advanced billing", view, meter)
	}
	if _, _, ok := liveMeterEvidence("device", 3, start, samples, start.Add(time.Hour)); ok {
		t.Fatal("another port's meter was used")
	}
	if _, _, ok := liveMeterEvidence("device", 1, start.Add(time.Hour), samples, start.Add(2*time.Hour)); ok {
		t.Fatal("previous order's meter was used")
	}
}

func TestLiveEstimateHandlesShortSamplingGapWithoutWeakeningSettlement(t *testing.T) {
	start := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	samples := []protocol.Event{}
	for i, seconds := range []int{15, 30, 95, 110} {
		samples = append(samples, protocol.Event{Type: protocol.Heartbeat, DeviceID: "device", ReceivedAt: start.Add(time.Duration(seconds) * time.Second), ChargingPorts: []protocol.PortTelemetry{{Port: 1, ChargedSeconds: uint32(seconds), ChargedMWh: uint32((i + 1) * 1000), PowerDeciWatts: 1500}}})
	}
	_, meter, ok := liveMeterEvidence("device", 1, start, samples, start.Add(115*time.Second))
	if !ok || len(meter.Segments) != 4 || meter.Segments[2].PowerW == nil {
		t.Fatal("short gap did not produce a current estimate", meter)
	}
	// The shared final-settlement evidence builder must still leave that gap missing.
	end := protocol.Event{DeviceID: "device", Port: 1, StartedAt: start, EndedAt: start.Add(110 * time.Second), ReceivedAt: start.Add(110 * time.Second), ChargedSeconds: 110, EnergyMilliKWh: 4}
	strict := pricing.MeasuredSegments(start, end, samples)
	if len(strict) != 4 || strict[2].PowerW != nil {
		t.Fatal("live approximation leaked into final settlement", strict)
	}
}
