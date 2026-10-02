package store

import (
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/google/uuid"
	"testing"
)

func TestCardEventIdentitySurvivesReplayWithoutCollapsingReswipe(t *testing.T) {
	e := protocol.Event{Type: protocol.CardSwipe, DeviceID: "card-device", Port: 2, CardNumber: 17564161, EventID: uuid.NewString(), RawPayload: []byte{2, 1, 2, 12, 1, 10, 0, 0, 0, 0}}
	first := eventKey(e)
	if first != e.EventID || eventKey(e) != first {
		t.Fatal("retry lost stable event identity")
	}
	e.EventID = uuid.NewString()
	if eventKey(e) == first {
		t.Fatal("separate reswipe collapsed into a raw payload hash")
	}
}
