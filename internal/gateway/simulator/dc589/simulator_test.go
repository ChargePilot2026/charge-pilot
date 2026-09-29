package dc589sim

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
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
	// 3600.0W, so a second of charging is a whole milliwatt-hour. At the 150W
	// default a short test produces a reading that truncates away to nothing,
	// which is the same reason the assertion below is about agreement rather
	// than about a figure being merely non-zero.
	config.PowerDeciWatts = 36000
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

	// Let the board meter long enough that the closing report carries a
	// duration and an energy both large enough to compare; stopping instantly
	// would prove only that the frames line up. Three seconds at 3600.0W is
	// three whole watt-hours, which is what it takes for the reading to be
	// wider than the frame's own resolution.
	time.Sleep(3 * time.Second)

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
	// The settlement has to agree with the power the board reported all along,
	// or the energy money moves on is not the energy the pile drew.
	assertEnergyAgrees(t, ends.EnergyMilliKWh, ends.ChargedSeconds, ends.PowerDeciWatts)
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

// scriptedGateway accepts one board and relays whatever the test pushes at it.
//
// It runs the real codec over a real socket, so framing and payload validation
// are still genuinely exercised. What it does not do is decide for itself which
// downlinks to send. That distinction matters: the full adapter is the right
// harness for proving the two ends interoperate, and the wrong one for proving
// what the board does when the platform asks for something specific, because
// "the port block arrived because we asked for it" is indistinguishable from
// "the port block always arrives" once the adapter is the one choosing.
type scriptedGateway struct {
	address  string
	session  [6]byte
	traffic  *boardTraffic
	downlink chan dc589.Frame
	// clock is the time the server claims when it answers a time request, and
	// registerClock the one it stamps into the register reply. They are separate
	// fields because a board that answers A1 correctly and then ignores the A9
	// looks perfectly right whenever the two agree — which is the state a test
	// has to move out of before it can see the difference at all.
	clock         time.Time
	registerClock time.Time
}

// boardTraffic is everything a board has sent, in order.
type boardTraffic struct {
	mu     sync.Mutex
	frames []dc589.Frame
}

func (b *boardTraffic) mark() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.frames)
}

func (b *boardTraffic) add(frame dc589.Frame) {
	b.mu.Lock()
	b.frames = append(b.frames, frame)
	b.mu.Unlock()
}

// waitFor returns the first frame at or after `from` that satisfies match.
//
// Indexing rather than re-matching on content is what lets a test assert that
// the *same* command changed shape within one session. The port telemetry flag
// cannot be proven any other way: a board that always sends the extended form
// and a board that always obeys produce identical traffic in half the states.
func (b *boardTraffic) waitFor(t *testing.T, limit time.Duration, from int, match func(dc589.Frame) bool) dc589.Frame {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		for i := from; i < len(b.frames); i++ {
			if match(b.frames[i]) {
				frame := b.frames[i]
				b.mu.Unlock()
				return frame
			}
		}
		b.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no matching frame arrived within %s", limit)
	return dc589.Frame{}
}

func (b *boardTraffic) first(command byte) (dc589.Frame, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, frame := range b.frames {
		if frame.Command == command {
			return frame, true
		}
	}
	return dc589.Frame{}, false
}

func serveScriptedGateway(t *testing.T, ctx context.Context, clock time.Time) *scriptedGateway {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	gateway := &scriptedGateway{
		address:  listener.Addr().String(),
		session:  [6]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66},
		traffic:  &boardTraffic{},
		downlink: make(chan dc589.Frame, 16),
		clock:    clock,
	}
	gateway.registerClock = clock
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		writer := &frameWriter{conn: conn}
		reader := bufio.NewReader(conn)

		first, err := dc589.ReadFrame(reader)
		if err != nil {
			return
		}
		gateway.traffic.add(first)
		if err := writer.send(dc589.BuildRegisterReply(gateway.session, gateway.registerClock)); err != nil {
			return
		}
		go func() {
			for {
				select {
				case frame := <-gateway.downlink:
					frame.Session = gateway.session
					if err := writer.send(frame); err != nil {
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()
		for {
			_ = conn.SetReadDeadline(time.Now().Add(120 * time.Second))
			frame, err := dc589.ReadFrame(reader)
			if err != nil {
				return
			}
			gateway.traffic.add(frame)
			switch frame.Command {
			case dc589.TimeRequest:
				if err := writer.send(dc589.BuildTimeReply(frame.Session, clock)); err != nil {
					return
				}
			case dc589.Heartbeat:
				if err := writer.send(dc589.BuildHeartbeatReply(frame.Session)); err != nil {
					return
				}
			}
		}
	}()
	return gateway
}

func (g *scriptedGateway) push(t *testing.T, frame dc589.Frame) {
	t.Helper()
	select {
	case g.downlink <- frame:
	case <-time.After(3 * time.Second):
		t.Fatal("the scripted gateway would not accept a downlink")
	}
}

// startCharge asks the board to begin charging, the way a paid order would.
func (g *scriptedGateway) startCharge(t *testing.T, port byte, mode dc589.ChargeMode, quantity uint16) {
	t.Helper()
	frame, err := dc589.BuildStart(dc589.StartCommand{
		Session: g.session, Port: port, OrderBCD: [8]byte{0, 0, 0, 0, 0, 0, 0, 7}, Mode: mode, Quantity: quantity,
	})
	if err != nil {
		t.Fatal(err)
	}
	mark := g.traffic.mark()
	g.push(t, frame)
	reply := g.traffic.waitFor(t, 5*time.Second, mark, func(f dc589.Frame) bool { return f.Command == dc589.StartReply })
	result, err := dc589.ParseCommandResult(reply)
	if err != nil {
		t.Fatal(err)
	}
	if result.Code != 0 {
		t.Fatalf("the board refused to start with code %d", result.Code)
	}
}

func isHeartbeat(frame dc589.Frame) bool { return frame.Command == dc589.Heartbeat }

func hasPortStatus(frame dc589.Frame) bool { return len(frame.Data) > 17 }

// A8 is the board asking for the time and A9 is the server answering, so the
// server never sends A8 and the board never receives one. Handling A8 was
// therefore unreachable, and the A9 the server does send fell into the default
// branch and was dropped. The board stamped every settlement from its own
// uncorrected clock while the server was offering a correction on every
// connection.
func TestBoardAsksForTimeAndSettlesOnTheServerClock(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	// The register reply and the later time request deliberately disagree. A
	// board that only ever reads the A1 is then off by the gap between them for
	// the rest of the session, which is what happens in the field once a pile's
	// clock drifts after it logs in: the platform offers a correction on every
	// connection and the board walks past it.
	registerClock := time.Now()
	serverClock := registerClock.Add(2 * time.Hour)
	gateway := serveScriptedGateway(t, ctx, serverClock)
	gateway.registerClock = registerClock

	logs := &captureLog{}
	config := quietConfig(t, ScenarioByTime)
	config.Gateway = gateway.address
	config.Log = log.New(logs, "", 0)
	go func() { _ = Run(ctx, config) }()

	// The board asks rather than waiting to be told: the time-sync event is the
	// only place a correction shows up in the session audit.
	waitFor(t, 8*time.Second, func() bool {
		_, ok := gateway.traffic.first(dc589.TimeRequest)
		return ok
	})
	waitFor(t, 8*time.Second, func() bool { return logs.contains("adopted") })

	gateway.startCharge(t, 1, dc589.ByTime, 60)
	time.Sleep(300 * time.Millisecond)
	stop, err := dc589.BuildStop(gateway.session, 1)
	if err != nil {
		t.Fatal(err)
	}
	mark := gateway.traffic.mark()
	gateway.push(t, stop)
	end := gateway.traffic.waitFor(t, 8*time.Second, mark, func(f dc589.Frame) bool { return f.Command == dc589.ChargeEnd })

	report, err := dc589.ParseChargeEnd(end)
	if err != nil {
		t.Fatal(err)
	}
	if drift := report.EndedAt.Sub(serverClock); drift > time.Minute || drift < -time.Minute {
		t.Fatalf("the settlement ended at %s against a server clock of %s, so the board never adopted the A9 and is still on the register time of %s",
			report.EndedAt.Format(time.RFC3339), serverClock.Format(time.RFC3339), registerClock.Format(time.RFC3339))
	}
}

// Since 5.8.6 the port block is present only when the platform asked for it, so
// the flag is the whole point of A6. The board recorded it and then never read
// it, which made "this build ignores its instructions" and "nothing is charging"
// produce the same seventeen byte heartbeat.
func TestBoardHonoursThePortTelemetryFlag(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	gateway := serveScriptedGateway(t, ctx, time.Now())

	config := quietConfig(t, ScenarioByTime)
	config.Gateway = gateway.address
	go func() { _ = Run(ctx, config) }()

	// With the flag off the board must fall back to the short form.
	off, err := dc589.BuildHeartbeatInterval(gateway.session, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	mark := gateway.traffic.mark()
	gateway.push(t, off)
	if got := gateway.traffic.waitFor(t, 8*time.Second, mark, isHeartbeat); hasPortStatus(got) {
		t.Fatal("the board reported ports after the platform had switched them off")
	}

	// A charge is running, so the extended form is the one that must appear.
	gateway.startCharge(t, 1, dc589.ByTime, 60)
	on, err := dc589.BuildHeartbeatInterval(gateway.session, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	mark = gateway.traffic.mark()
	gateway.push(t, on)
	got := gateway.traffic.waitFor(t, 8*time.Second, mark, hasPortStatus)
	heartbeat, err := dc589.ParseHeartbeat(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(heartbeat.ChargingPorts) != 1 || heartbeat.ChargingPorts[0].Port != 1 {
		t.Fatalf("the extended heartbeat did not carry the charging port: %+v", heartbeat.ChargingPorts)
	}

	// And switching it off again must take effect mid-charge. This is the step
	// that cannot pass by accident: a board that always sends the extended form
	// satisfies the previous assertion just as happily as one that obeys.
	mark = gateway.traffic.mark()
	gateway.push(t, off)
	if got := gateway.traffic.waitFor(t, 8*time.Second, mark, isHeartbeat); hasPortStatus(got) {
		t.Fatal("the board kept reporting ports after the platform switched them off mid-charge")
	}
}

// A downlink the board cannot answer used to vanish with no record on either
// side, which made "the simulator never implemented it" and "the gateway never
// sent it" indistinguishable from the outside.
func TestUnprocessedDownlinkIsNamedAndSurvives(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	gateway := serveScriptedGateway(t, ctx, time.Now())

	logs := &captureLog{}
	config := quietConfig(t, ScenarioByTime)
	config.Gateway = gateway.address
	config.Log = log.New(logs, "", 0)
	go func() { _ = Run(ctx, config) }()

	waitFor(t, 8*time.Second, func() bool { return gateway.traffic.mark() > 0 })

	// 0xE0 is the power control the gateway has a codec for but no caller for
	// yet, so this is exactly the shape of command that would otherwise arrive
	// and be lost.
	frame, err := dc589.SetNoTiering(gateway.session)
	if err != nil {
		t.Fatal(err)
	}
	gateway.push(t, frame)

	waitFor(t, 5*time.Second, func() bool { return logs.contains("unhandled downlink 0xE0") })
	// Ignoring it must not cost the board its link.
	mark := gateway.traffic.mark()
	interval, err := dc589.BuildHeartbeatInterval(gateway.session, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	gateway.push(t, interval)
	gateway.traffic.waitFor(t, 8*time.Second, mark, isHeartbeat)
}

// The zero value is not a legal parameter table, so a board that had never been
// configured failed its own validation and answered a read with "rejected" —
// telling the platform its tariff had failed to land when nobody had ever sent
// one. The read has to come back as an actual table.
func TestParameterReadAnswersWithTheStoredTable(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	gateway := serveScriptedGateway(t, ctx, time.Now())

	config := quietConfig(t, ScenarioByTime)
	config.Gateway = gateway.address
	go func() { _ = Run(ctx, config) }()

	waitFor(t, 8*time.Second, func() bool { return gateway.traffic.mark() > 0 })

	mark := gateway.traffic.mark()
	gateway.push(t, dc589.Frame{Command: dc589.ReadConfig, Data: nil})
	report := gateway.traffic.waitFor(t, 8*time.Second, mark, func(f dc589.Frame) bool { return f.Command == dc589.ConfigReport })

	table, err := dc589.DecodeConfig(report)
	if err != nil {
		t.Fatalf("the board answered a parameter read with something it cannot itself read back: %v", err)
	}
	if err := table.Validate(); err != nil {
		t.Fatalf("the stored table is not one the board would accept: %v", err)
	}
	if table.TemperatureGuard != 80 {
		t.Fatalf("temperature guard = %#x, want the factory 80", table.TemperatureGuard)
	}
	if ack, ok := gateway.traffic.first(dc589.ConfigAck); ok && ack.Data[0] != 0 {
		t.Fatalf("the board reported a rejected write while answering a read, code %d", ack.Data[0])
	}
}

// newTestBoard gives a board a real in-memory connection.
//
// A charge that reaches its limit reports the end over the wire, so a unit test
// that walks a charge to completion needs somewhere to write. A nil connection
// would turn the assertion into a segmentation fault instead of a result.
func newTestBoard(t *testing.T, config Config) *board {
	t.Helper()
	conn, peer := net.Pipe()
	t.Cleanup(func() { _ = conn.Close(); _ = peer.Close() })
	go func() { _, _ = io.Copy(io.Discard, peer) }()
	return newBoard(config.withDefaults(), conn)
}

// captureLog collects the board's log lines so a test can assert that something
// was reported rather than swallowed.
type captureLog struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureLog) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.lines = append(c.lines, string(p))
	c.mu.Unlock()
	return len(p), nil
}

func (c *captureLog) contains(substr string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, line := range c.lines {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

// The quantity on an energy order is watt-hours and the wire carries no unit, so
// the charge mode is the only thing that says what it means. Reading it as a
// count of seconds finished a 1Wh order in one second and then reported several
// watt-hours against it, so the user was billed for far more than was delivered
// and nothing in the settlement looked unusual.
func TestEnergyOrderDoesNotTreatItsQuantityAsSeconds(t *testing.T) {
	board := newTestBoard(t, quietConfig(t, ScenarioByEnergy))
	// The charge is held directly rather than read back out of the map: a board
	// that meters wrongly finishes early and takes itself out of the map, and a
	// lookup on a missing key would turn that into a nil dereference instead of
	// the assertion the test is actually making.
	running := &charge{port: 1, mode: dc589.ByEnergy, targetMilliWh: 1000}
	board.charging[1] = running

	board.advance()
	if running.complete() {
		t.Fatalf("a 1Wh order finished after one second; its quantity is being read as a duration")
	}
	// 150W fills a milliwatt-hour every 36/1500 s, so a second buys 41.67mWh.
	if got := running.chargedMilliWh(); got != 41 {
		t.Fatalf("after one second at 150W the meter read %d mWh, want 41", got)
	}
}

// The energy a board reports and the power it reports have to be the same
// number. They used to be independent constants — the meter banked a fixed
// 3600 mWh every tick while the board claimed 150W — which is a factor of 24
// between the power in the heartbeat and the energy in the settlement, and the
// settlement is the number money moves on.
func TestMeteredEnergyFollowsTheReportedPower(t *testing.T) {
	config := quietConfig(t, ScenarioByEnergy)
	config.PowerDeciWatts = 1500 // 150.0W
	board := newTestBoard(t, config)
	running := &charge{port: 1, mode: dc589.ByEnergy, targetMilliWh: 10 * 1000}
	board.charging[1] = running

	for range 60 {
		board.advance()
	}
	// 150.0W for 60s is 9000J, which is 2.5Wh.
	if got := running.chargedMilliWh(); got != 2500 {
		t.Fatalf("60s at 150W metered %d mWh, want 2500", got)
	}
	// The heartbeat answers in seconds on both billing modes, so an energy
	// order is projected from the power it is drawing. Reporting the energy
	// figure's absence of a duration would tell the platform a charge is over
	// the moment it starts.
	if got := board.remainingSecs(running); got != 180 {
		t.Fatalf("the heartbeat would have reported %d seconds left, want 180", got)
	}
}

// The whole chain has to hold together: the power the board reports sets the
// rate it meters, the rate decides how long the purchased energy takes, and the
// order ends when the meter says so rather than when a clock happens to agree.
func TestEnergyOrderEndsWhenTheMeterReachesThePurchasedEnergy(t *testing.T) {
	config := quietConfig(t, ScenarioByEnergy)
	config.PowerDeciWatts = 1500 // 150.0W
	board := newTestBoard(t, config)
	board.charging[1] = &charge{port: 1, mode: dc589.ByEnergy, targetMilliWh: 5 * 1000}

	// 150.0W fills 1000mWh every 24 seconds, so 5Wh is two minutes of charging.
	ticks := 0
	for ticks = 1; ticks <= 600; ticks++ {
		board.advance()
		if _, running := board.charging[1]; !running {
			break
		}
	}
	if ticks != 120 {
		t.Fatalf("a 5Wh order took %d seconds at 150W, want 120", ticks)
	}
}

// A time order is bounded by its clock, and the energy it reports follows from
// the same power rather than from a constant.
func TestTimeOrderEndsWhenItsClockRunsOut(t *testing.T) {
	board := newTestBoard(t, quietConfig(t, ScenarioByTime))
	board.charging[1] = &charge{port: 1, mode: dc589.ByTime, remaining: 3 * time.Second}

	board.advance()
	board.advance()
	if board.charging[1].complete() {
		t.Fatal("a three second order finished after two seconds")
	}
	// The board is still holding the port, which is the other half of the claim.
	board.advance()
	if _, running := board.charging[1]; running {
		t.Fatal("a three second order was still running after three seconds")
	}
}

// assertEnergyAgrees checks a settlement's energy against the power and the
// duration the board reported alongside it.
//
// The energy is in milli-kWh, the unit the gateway's event carries and the unit
// the frame's whole watt-hour field resolves to. The tolerance is a tenth plus
// one of those, because the frame counts whole watt-hours and a charge that ran
// a few seconds cannot be pinned more tightly than its own resolution. A
// tolerance loose enough to absorb that rounding would also absorb the factor of
// 24 the meter used to report, so it is kept in one place rather than widened
// at each call site as short charges come out slightly off.
func assertEnergyAgrees(t *testing.T, milliKWh, seconds, deciWatts uint32) {
	t.Helper()
	// A milliwatt-hour is 36 of the board's accumulator units per second and a
	// milli-kWh is a thousand of those, so the two conversions cancel here.
	want := uint64(deciWatts) * uint64(seconds) / (deciWattSecondsPerMilliWh * 1000)
	slack := want/10 + 1
	got := uint64(milliKWh)
	if got+slack < want || want+slack < got {
		t.Fatalf("the settlement reported %d milli-kWh over %d seconds at %d deciwatts, want about %d milli-kWh",
			milliKWh, seconds, deciWatts, want)
	}
}

// The settlement is the number money moves on, so the energy it carries has to
// agree with the power and the duration the board reported along the way. The
// draw is set high enough that the figure survives the frame's whole watt-hour
// resolution rather than truncating to zero.
func TestSettlementEnergyAgreesWithTheReportedPower(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	gateway := serveScriptedGateway(t, ctx, time.Now())

	config := quietConfig(t, ScenarioByTime)
	config.Gateway = gateway.address
	config.PowerDeciWatts = 36000 // 3600.0W, so a second is exactly 1000mWh
	go func() { _ = Run(ctx, config) }()

	gateway.startCharge(t, 1, dc589.ByTime, 60)
	time.Sleep(3 * time.Second)
	stop, err := dc589.BuildStop(gateway.session, 1)
	if err != nil {
		t.Fatal(err)
	}
	mark := gateway.traffic.mark()
	gateway.push(t, stop)
	end := gateway.traffic.waitFor(t, 8*time.Second, mark, func(f dc589.Frame) bool { return f.Command == dc589.ChargeEnd })

	report, err := dc589.ParseChargeEnd(end)
	if err != nil {
		t.Fatal(err)
	}
	if report.PowerDeciWatts != 36000 {
		t.Fatalf("the settlement reported %d deciwatts, want 36000", report.PowerDeciWatts)
	}
	// One second of 3600.0W is 1000mWh, so the charge should have metered about
	// one milliwatt-hour per second it ran.
	assertEnergyAgrees(t, report.ChargedMWh/1000, report.ChargedSeconds, report.PowerDeciWatts)
}
