package charge

import (
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

func TestNoPowerRequiresContinuousMinuteAndFreshHeartbeat(t *testing.T) {
	now := time.Now().UTC()
	at := func(before time.Duration, power uint32) protocol.Event {
		return protocol.Event{Type: protocol.Heartbeat, ReceivedAt: now.Add(-before), ChargingPorts: []protocol.PortTelemetry{{Port: 1, PowerDeciWatts: power}}}
	}
	if noPowerForMinute([]protocol.Event{at(70*time.Second, 200), at(50*time.Second, 0), at(2*time.Second, 0)}, 1, now) {
		t.Fatal("stopped before full minute of zero power")
	}
	if !noPowerForMinute([]protocol.Event{at(90*time.Second, 200), at(70*time.Second, 0), at(2*time.Second, 0)}, 1, now) {
		t.Fatal("did not stop after a full minute")
	}
	if noPowerForMinute([]protocol.Event{at(90*time.Second, 200), at(70*time.Second, 0), at(40*time.Second, 0)}, 1, now) {
		t.Fatal("stale heartbeat must not imply unplug")
	}
	if noPowerForMinute([]protocol.Event{at(90*time.Second, 0), at(20*time.Second, 200), at(2*time.Second, 0)}, 1, now) {
		t.Fatal("positive power must reset the timer")
	}
	other := protocol.Event{Type: protocol.Heartbeat, ReceivedAt: now.Add(-2 * time.Second), ChargingPorts: []protocol.PortTelemetry{{Port: 2, PowerDeciWatts: 0}}}
	if noPowerForMinute([]protocol.Event{at(90*time.Second, 0), at(70*time.Second, 0), other}, 1, now) {
		t.Fatal("a heartbeat without this port is not zero-power evidence")
	}
}
