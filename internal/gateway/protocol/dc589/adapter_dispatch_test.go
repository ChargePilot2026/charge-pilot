package dc589

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
)

// recordingSink keeps every event the adapter produces so a test can assert on
// what an operator would actually be able to see afterwards.
type recordingSink struct {
	mu     sync.Mutex
	events []protocol.Event
}

func (s *recordingSink) Register(context.Context, protocol.Registration) error { return nil }

func (s *recordingSink) Record(_ context.Context, event protocol.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return nil
}

func (s *recordingSink) all() []protocol.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocol.Event(nil), s.events...)
}

func (s *recordingSink) findConfigResults() []protocol.Event {
	var out []protocol.Event
	for _, event := range s.all() {
		if event.Type == protocol.ConfigResult {
			out = append(out, event)
		}
	}
	return out
}

// serveWithFrames runs one adapter session over a real loopback socket, feeds
// it a login and then the given frames, and returns the sink. It is the
// shortest path from "a board sent this" to "an operator can see this", which
// is the property these tests are about.
func serveWithFrames(t *testing.T, replies []Frame) *recordingSink {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	sink := &recordingSink{}
	adapter := TCPAdapter{}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			served <- err
			return
		}
		served <- adapter.ServeConn(ctx, conn, sink)
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// The adapter sends the register reply, then the 0xA6 asking for the
	// heartbeat period. Drained in the background so nothing it writes can
	// block the frames under test.
	go func() {
		buf := make([]byte, 512)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}()

	login, err := BuildRegistration(testIdentity())
	if err != nil {
		t.Fatalf("build login: %v", err)
	}
	raw, err := Encode(login)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(raw); err != nil {
		t.Fatalf("write login: %v", err)
	}
	for _, reply := range replies {
		raw, err := Encode(reply)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write(raw); err != nil {
			t.Fatalf("write reply: %v", err)
		}
	}
	time.Sleep(250 * time.Millisecond)
	cancel()
	_ = conn.Close()
	select {
	case <-served:
	case <-time.After(3 * time.Second):
		t.Fatal("the adapter did not release the connection")
	}
	return sink
}

// A board that refuses a parameter table is telling us the exact field that was
// out of range. Before these replies were handled it fell through to the unknown
// branch, so the refusal was recorded as "sent a command we do not recognise"
// and the reason was gone — the same mistake the stop path made with 0x00 and
// 0x04, on a path nobody had exercised because nothing sent the request.
func TestConfigRejectionIsRecordedRatherThanCalledUnknown(t *testing.T) {
	// Code 5 is the document's "card charge amount out of range".
	sink := serveWithFrames(t, []Frame{{Command: ConfigAck, Data: []byte{5}}})

	results := sink.findConfigResults()
	if len(results) == 0 {
		t.Fatalf("a parameter-table refusal produced no config result; events were %+v", sink.all())
	}
	var rejection protocol.Event
	for _, event := range results {
		if event.ResultCode == 5 {
			rejection = event
		}
	}
	if rejection.Type != protocol.ConfigResult || rejection.ResultCode != 5 {
		t.Fatalf("config results = %+v, want one carrying code 5", results)
	}
	if rejection.Signal != ConfigAck {
		t.Fatalf("signal = %#x, want the 0xC4 that was answered", rejection.Signal)
	}
}

// A refusal must be distinguishable from a link that simply never answered.
// Both look like "no acknowledgement" from the outside, and only one of them
// means an operator has to go and change a number.
func TestConfigAcceptanceAndRefusalAreDistinguishable(t *testing.T) {
	sink := serveWithFrames(t, []Frame{{Command: ConfigAck, Data: []byte{0}}})
	results := sink.findConfigResults()
	if len(results) == 0 || results[0].ResultCode != 0 {
		t.Fatalf("an accepted table produced %+v, want a zero result code", results)
	}
}

// The board's own report of what it is running is ordinary traffic on a healthy
// link, so it must not be counted as an unrecognised command either — and the
// bytes must be kept exactly as they arrived, because RawPayload is the replay
// record.
func TestConfigReportIsNotTreatedAsUnknown(t *testing.T) {
	table := ConfigTable{RunMode: 0, LocalCoinTime: 10, LocalCardTime: 20, CardAmountCents: 500,
		TemperatureGuard: 0xFF, FloatSeconds: 300, FloatDeciWatts: 100, RemoveSeconds: 60}
	report, err := BuildConfigReport(table)
	if err != nil {
		t.Fatalf("build report: %v", err)
	}
	sink := serveWithFrames(t, []Frame{report})
	results := sink.findConfigResults()
	if len(results) == 0 {
		t.Fatalf("a configuration report produced no config result; events were %+v", sink.all())
	}
	if results[0].ResultCode != 0 {
		t.Fatalf("result code = %#x, want 0 for a report that decoded", results[0].ResultCode)
	}
	if len(results[0].RawPayload) != len(report.Data) {
		t.Fatalf("raw payload = %d bytes, want the %d that arrived", len(results[0].RawPayload), len(report.Data))
	}
	// A report this build cannot read is still visible as unreadable, rather
	// than stored as a blob that looks fine.
	sink = serveWithFrames(t, []Frame{{Command: ConfigReport, Data: []byte{0, 1, 2}}})
	results = sink.findConfigResults()
	if len(results) == 0 || results[0].ResultCode != 0xFF {
		t.Fatalf("an unreadable report produced %+v, want the undecodable marker", results)
	}
}

// The power-control reply is the same shape of problem: a board that refuses to
// drop its tiering has to be visible as a refusal.
func TestPowerControlRefusalIsRecorded(t *testing.T) {
	sink := serveWithFrames(t, []Frame{{Command: cmdPowerControlReply,
		Data: []byte{byte(PowerSet), byte(PowerOpRemove), 0xFF, 0xF1, 0, 0}}})
	results := sink.findConfigResults()
	if len(results) == 0 {
		t.Fatalf("a power-control refusal produced no config result; events were %+v", sink.all())
	}
	if results[0].ResultCode != 1 {
		t.Fatalf("result code = %#x, want the refusal marker", results[0].ResultCode)
	}
}
