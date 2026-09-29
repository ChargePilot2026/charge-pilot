package dc589

import (
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
)

// TestUnknownCommandDoesNotKillTheConnection locks in the robustness rule that
// the vendor document's own wording makes necessary: it marks the platform
// parameter request, the charging band report and the remote-control reply as
// things some boards send and some do not. A build that drops the connection on
// an unrecognised command would make every board running older firmware
// unreachable, and the failure would look like a network problem rather than a
// missing feature.
func TestUnknownCommandDoesNotKillTheConnection(t *testing.T) {
	frame := Frame{Command: 0xC7, Session: [6]byte{1, 2, 3, 4, 5, 6}, Data: []byte{0}}
	raw, err := Encode(frame)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// The frame is well formed: it is unknown, not malformed. The decoder must
	// hand it back rather than reject it, so the adapter can skip it.
	decoded, err := Decode(raw)
	if err != nil {
		t.Fatalf("a well-formed unknown command must still decode: %v", err)
	}
	if decoded.Command != 0xC7 {
		t.Fatalf("command = 0x%02x, want 0xC7", decoded.Command)
	}
}

func TestStartRejectsLongRunModes(t *testing.T) {
	// The document states the long-run variants are not used in normal
	// operation. They also bypass the time and energy limits the platform sets,
	// so an ordinary charge request must not be able to reach one.
	for _, mode := range []ChargeMode{LongTime, LongEnergy, LongPlatformBilling} {
		if _, err := BuildStart(StartCommand{Session: [6]byte{1}, Port: 1, OrderBCD: [8]byte{1}, Mode: mode, Quantity: 60}); err == nil {
			t.Fatalf("long-run mode %d must be rejected for a routine start", mode)
		}
	}
	for _, mode := range []ChargeMode{ByTime, ByEnergy, PlatformBilling} {
		if _, err := BuildStart(StartCommand{Session: [6]byte{1}, Port: 1, OrderBCD: [8]byte{1}, Mode: mode, Quantity: 60}); err != nil {
			t.Fatalf("normal mode %d must be accepted: %v", mode, err)
		}
	}
}

func TestStartRejectsReservedChargeTypes(t *testing.T) {
	// 2 (pay by amount) and 3 (stop when full) are reserved in the document and
	// the hardware implements neither. Offering them would produce a start the
	// board cannot honour.
	for _, mode := range []ChargeMode{2, 3, 5, 9, 13} {
		if mode.IsNormal() {
			t.Fatalf("charge type %d must not be treated as normal", mode)
		}
	}
}

func TestSessionAuditRecordsUnknownCommands(t *testing.T) {
	audit := newAudit()
	if len(audit.UnknownCommands()) != 0 {
		t.Fatal("a fresh audit must have recorded nothing")
	}
	audit.Unknown(0xC7)
	audit.Unknown(0xC7)
	audit.Unknown(0xB4)
	got := audit.UnknownCommands()
	if len(got) != 2 || got[0] != 0xB4 || got[1] != 0xC7 {
		t.Fatalf("unknown commands = %v, want a distinct ascending [180 199]", got)
	}
}

func newAudit() *protocol.SessionAudit {
	return protocol.NewSessionAudit(protocol.TransportTCP, "sess", "127.0.0.1:1", time.Now())
}
