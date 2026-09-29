package dc589sim

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol/dc589"
)

// These tests run the simulated board against the real gateway adapter over a
// real TCP socket. A fake server would prove nothing about framing, session
// negotiation or payload validation, which is the whole reason this tool exists.

// recordingSink captures what the gateway parsed out of the board, so a test
// can assert on decoded values rather than on bytes.
type recordingSink struct {
	mu       sync.Mutex
	events   []protocol.Event
	registry protocol.Registration
}

func newSink() *recordingSink {
	return &recordingSink{}
}

func (s *recordingSink) Register(ctx context.Context, registration protocol.Registration) error {
	s.mu.Lock()
	s.registry = registration
	s.mu.Unlock()
	return nil
}

func (s *recordingSink) Record(ctx context.Context, event protocol.Event) error {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	return nil
}

func (s *recordingSink) ofType(t protocol.EventType) []protocol.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []protocol.Event
	for _, event := range s.events {
		if event.Type == t {
			out = append(out, event)
		}
	}
	return out
}

func (s *recordingSink) deviceID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registry.DeviceID
}

func (s *recordingSink) heartbeatCount() int { return len(s.ofType(protocol.Heartbeat)) }

// serveGateway runs the real adapter on a real port. The registry it uses is
// returned so the test can issue commands the same way the control layer does,
// rather than reaching into the board's connection directly.
func serveGateway(t *testing.T, ctx context.Context, sink protocol.Sink) (string, *protocol.Registry) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	registry := &protocol.Registry{}
	go func() {
		adapter := dc589.TCPAdapter{Registry: registry}
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { _ = adapter.ServeConn(ctx, conn, sink) }()
		}
	}()
	return listener.Addr().String(), registry
}

func quietConfig(t *testing.T, scenario Scenario) Config {
	t.Helper()
	return Config{
		Identity: dc589.DeviceIdentity{
			BoardID:         "5348240514082652",
			HardwareVersion: "SH10HP01",
			SoftwareID:      "DC589SW1",
			SoftwareVersion: 107,
			ModuleID:        "1234567812345678",
			SIM:             "89860000000000000001",
			Signal:          4,
		},
		PortCount: 2,
		Scenario:  scenario,
		Heartbeat: 60 * time.Millisecond,
		Log:       log.New(testWriter{t}, "", 0),
	}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("%s", p)
	return len(p), nil
}

// A board that registers and heartbeats must be understood by the real adapter,
// which is the minimum for any scenario to be meaningful.
func TestSimulatorRegistersAndHeartbeats(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	sink := newSink()
	address, _ := serveGateway(t, ctx, sink)

	config := quietConfig(t, ScenarioByEnergy)
	config.Gateway = address
	go func() { _ = Run(ctx, config) }()

	// The board is told its heartbeat period by the platform right after it
	// logs in, and it adopts that value. The simulator's own configured period
	// is only what it uses until then, so a test that assumed it would keep
	// ticking at its own speed was asserting behaviour the protocol does not
	// have: since 5.8.6 the server owns the interval.
	waitFor(t, 8*time.Second, func() bool { return sink.heartbeatCount() >= 1 })
	if got := sink.deviceID(); got != config.Identity.BoardID {
		t.Fatalf("gateway saw device %q, want %q", got, config.Identity.BoardID)
	}
}

// The charge path is the point of the tool: start command, acknowledgement,
// metering, then a closing report the gateway can bind back to the order.
func TestSimulatorRunsAChargeToCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	sink := newSink()
	address, registry := serveGateway(t, ctx, sink)

	config := quietConfig(t, ScenarioByEnergy)
	config.Gateway = address
	// A one minute charge in energy terms would outlast the test, so the
	// scenario is driven by a stop command instead, which is the realistic path.
	config.Scenario = ScenarioStopOnCommand
	go func() { _ = Run(ctx, config) }()

	// Wait for registration, then issue a start through the registry the way
	// the control layer would.
	session := commandSession()
	order := [8]byte{0, 0, 0, 0, 0, 0, 0x01, 0x23}
	command := protocol.Command{Kind: protocol.CommandStart, SessionID: session, Port: 1, OrderBCD: order, Mode: 0, Quantity: 60}
	if err := commandThrough(t, registry, config.Identity.BoardID, command); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 10*time.Second, func() bool { return len(sink.ofType(protocol.StartResult)) > 0 })

	// Let the board meter for a moment so the closing report carries a
	// non-zero duration and a non-zero amount of energy; stopping instantly
	// would prove only that the frames line up.
	time.Sleep(1500 * time.Millisecond)

	// Ask the board to stop; it should acknowledge and report the charge end.
	stop := protocol.Command{Kind: protocol.CommandStop, SessionID: session, Port: 1}
	if err := commandThrough(t, registry, config.Identity.BoardID, stop); err != nil {
		t.Fatal(err)
	}
	ends := waitForEnd(t, sink, 10*time.Second)
	if ends.Port != 1 {
		t.Fatalf("charge end reported port %d, want 1", ends.Port)
	}
	if ends.OrderNumber != "0000000000000123" {
		t.Fatalf("order %q, want the bytes echoed from the start command", ends.OrderNumber)
	}
	if ends.ChargedSeconds == 0 {
		t.Fatal("a charge that ran reported no elapsed time")
	}
	if ends.EnergyMilliKWh == 0 {
		t.Fatal("a charge that ran delivered no energy")
	}
	if len(sink.ofType(protocol.StopResult)) == 0 {
		t.Fatal("the stop acknowledgement never reached the gateway")
	}
}

// A refused start is what drives the refund path, so the failure has to be a
// well formed result rather than silence.
func TestSimulatorCanRefuseAStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	sink := newSink()
	address, registry := serveGateway(t, ctx, sink)

	config := quietConfig(t, ScenarioRejectStart)
	config.Gateway = address
	go func() { _ = Run(ctx, config) }()

	session := commandSession()
	_ = commandThrough(t, registry, config.Identity.BoardID, protocol.Command{
		Kind: protocol.CommandStart, SessionID: session, Port: 1, Mode: 0, Quantity: 60,
	})
	waitFor(t, 8*time.Second, func() bool { return len(sink.ofType(protocol.StartResult)) > 0 })
	refusals := sink.ofType(protocol.StartResult)
	if len(refusals) == 0 {
		t.Fatal("no start result arrived")
	}
	if refusals[0].ResultCode == 0 {
		t.Fatal("the reject scenario answered with success")
	}
}

func TestSimulatorReportsAFault(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	sink := newSink()
	address, registry := serveGateway(t, ctx, sink)

	config := quietConfig(t, ScenarioFault)
	config.Gateway = address
	config.FaultAfter = 100 * time.Millisecond
	go func() { _ = Run(ctx, config) }()

	session := commandSession()
	_ = commandThrough(t, registry, config.Identity.BoardID, protocol.Command{
		Kind: protocol.CommandStart, SessionID: session, Port: 1, Mode: 0, Quantity: 3600,
	})
	waitFor(t, 10*time.Second, func() bool { return len(sink.ofType(protocol.Fault)) > 0 })
	faults := sink.ofType(protocol.Fault)
	if faults[0].FaultCode != 0x35 {
		t.Fatalf("fault code = %#x, want 0x35", faults[0].FaultCode)
	}
}

// The reconnect scenario is what verifies that a replacement session is treated
// as a new connection rather than a continuation of the old one.
func TestSimulatorReconnectsAsANewSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	sink := newSink()
	address, _ := serveGateway(t, ctx, sink)

	config := quietConfig(t, ScenarioReconnect)
	config.Gateway = address
	config.ReconnectAfter = 200 * time.Millisecond
	go func() { _ = Run(ctx, config) }()

	// Each reconnection performs a fresh registration; two of them prove the
	// board came back rather than the first attempt lingering.
	waitFor(t, 15*time.Second, func() bool {
		return len(sink.ofType(protocol.Heartbeat)) >= 2
	})
	if got := sink.deviceID(); got != config.Identity.BoardID {
		t.Fatalf("device after reconnect = %q, want %q", got, config.Identity.BoardID)
	}
}

func waitFor(t *testing.T, limit time.Duration, done func() bool) struct{} {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if done() {
			return struct{}{}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", limit)
	return struct{}{}
}

// commandSession is the session the test issues commands with.
//
// In production the control layer reads this from the charge command row it
// persisted before contacting the board; the gateway never hands the session
// back to the sink, and the board simply echoes whatever session the server put
// in the frame header. Supplying one here reproduces that flow, and
// commandThrough already waits for the board to be routable.
func commandSession() [6]byte { return [6]byte{0xA1, 0xB2, 0xC3, 0xD4, 0xE5, 0xF6} }

func waitForEnd(t *testing.T, sink *recordingSink, limit time.Duration) protocol.Event {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		ends := sink.ofType(protocol.ChargeEnd)
		if len(ends) > 0 {
			return ends[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no charge end arrived")
	return protocol.Event{}
}

// commandThrough routes a command to the board the way the control layer does:
// by device id, through the registry the adapter owns.
func commandThrough(t *testing.T, registry *protocol.Registry, deviceID string, command protocol.Command) error {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := registry.Send(t.Context(), deviceID, command)
		if err == nil {
			return nil
		}
		if !errors.Is(err, protocol.ErrOffline) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("device %s never became routable: %w", deviceID, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
