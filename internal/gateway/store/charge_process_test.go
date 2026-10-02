package store

import (
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

func TestHeartbeatEventKeyIdentifiesReceiptAndStructuredMeasurements(t *testing.T) {
	e := protocol.Event{Protocol: "dc589", DeviceID: "board", Type: protocol.Heartbeat,
		ReceivedAt:    time.Date(2026, 10, 1, 0, 0, 0, 1000000, time.UTC),
		ChargingPorts: []protocol.PortTelemetry{{Port: 1, PowerDeciWatts: 100}}}
	key := eventKey(e)
	if key != eventKey(e) {
		t.Fatal("same received event changed identity")
	}
	e.ReceivedAt = e.ReceivedAt.In(time.FixedZone("CST", 8*3600))
	if key != eventKey(e) {
		t.Fatal("same instant in another timezone changed identity")
	}
	e.ChargingPorts[0].PowerDeciWatts++
	if key == eventKey(e) {
		t.Fatal("different structured measurement lost identity")
	}
	e.RawPayload = []byte{1, 2, 3}
	key = eventKey(e)
	e.ReceivedAt = e.ReceivedAt.Add(15 * time.Second)
	if key == eventKey(e) {
		t.Fatal("new receipt of identical bytes collapsed into old sample")
	}
}
