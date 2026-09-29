// Package dc589sim runs a local stand-in for a dc589 charging board.
//
// It speaks the real 5.8.9 wire protocol over TCP, so it exercises the same
// framing, the same payload validation and the same session handling the
// gateway meets on a physical board. It is not a mock of the gateway: it is a
// second implementation of the device end of the same contract.
//
// The reason it exists is that the charge path cannot otherwise be walked end
// to end without vendor hardware. It is a development and test tool and must
// never be wired into a production process.
//
// Simulators are per protocol: a second vendor, or an MQTT board, gets its own
// package beside this one rather than a flag on a shared implementation,
// because the behaviours they can express are not the same. The package is
// named dc589sim rather than dc589 so it can still import the protocol codec it
// implements.
package dc589sim

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol/dc589"
)

// Scenario selects the behaviour under test. Every scenario shares the same
// register and heartbeat path and differs only in what the board does once it
// is asked to charge, so a failure points at one behaviour.
type Scenario string

const (
	// ScenarioByEnergy charges until the requested energy is delivered.
	ScenarioByEnergy Scenario = "by-energy"
	// ScenarioByTime charges until the requested duration elapses.
	ScenarioByTime Scenario = "by-time"
	// ScenarioStopOnCommand charges until the server sends B9, exercising the
	// stop path without waiting for a full charge.
	ScenarioStopOnCommand Scenario = "stop-on-command"
	// ScenarioRejectStart answers B8 with a failure, which drives the server's
	// refund-on-rejection path.
	ScenarioRejectStart Scenario = "reject-start"
	// ScenarioFault reports a fault shortly after a charge starts.
	ScenarioFault Scenario = "fault"
	// ScenarioSilent registers and then stops talking, so the read timeout and
	// the abandoned-session cleanup can be observed.
	ScenarioSilent Scenario = "silent"
	// ScenarioReconnect drops and re-registers, verifying session replacement
	// and the connection audit.
	ScenarioReconnect Scenario = "reconnect"
)

// Config is everything a simulated board needs. Rates use the units the
// protocol uses so they can be compared directly with the wire fields.
type Config struct {
	Identity  dc589.DeviceIdentity
	PortCount int
	Gateway   string
	Scenario  Scenario
	Heartbeat time.Duration
	// PowerDeciWatts is the constant draw while charging, in tenths of a watt.
	PowerDeciWatts uint32
	// ReconnectAfter is the pause before reconnecting in ScenarioReconnect.
	ReconnectAfter time.Duration
	// FaultAfter delays the fault report in ScenarioFault.
	FaultAfter time.Duration
	// Log receives progress lines. Nil discards them.
	Log *log.Logger
}

func (c Config) withDefaults() Config {
	if c.Heartbeat <= 0 {
		c.Heartbeat = 15 * time.Second
	}
	if c.PowerDeciWatts == 0 {
		c.PowerDeciWatts = 1500
	}
	if c.PortCount <= 0 {
		c.PortCount = 2
	}
	if c.ReconnectAfter <= 0 {
		c.ReconnectAfter = 3 * time.Second
	}
	if c.FaultAfter <= 0 {
		c.FaultAfter = 5 * time.Second
	}
	if c.Log == nil {
		c.Log = log.New(discard{}, "", 0)
	}
	return c
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Run connects and serves until the context is cancelled. The reconnect
// scenario re-establishes the connection on its own; every other scenario
// returns when the connection ends.
func Run(ctx context.Context, config Config) error {
	config = config.withDefaults()
	for {
		err := serveOnce(ctx, config)
		if ctx.Err() != nil {
			return nil
		}
		if config.Scenario != ScenarioReconnect {
			return err
		}
		if err != nil {
			config.Log.Printf("connection ended: %v; reconnecting in %s", err, config.ReconnectAfter)
		} else {
			config.Log.Printf("reconnecting in %s", config.ReconnectAfter)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(config.ReconnectAfter):
		}
	}
}

func serveOnce(ctx context.Context, config Config) error {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", config.Gateway)
	if err != nil {
		return fmt.Errorf("dial gateway: %w", err)
	}
	board := newBoard(config, conn)

	// Closing the connection is how cancellation interrupts a blocking read.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	if err := board.register(ctx); err != nil {
		return err
	}
	if config.Scenario == ScenarioSilent {
		config.Log.Printf("scenario %s: registered, staying silent", config.Scenario)
		<-ctx.Done()
		return nil
	}
	return board.loop(ctx)
}

// charge is the state of one running charge.
type charge struct {
	port       byte
	orderBCD   [8]byte
	startedAt  time.Time
	charged    uint32 // milliwatt-hours delivered so far
	remaining  time.Duration
	stopReason byte
	faultSent  bool
}

type board struct {
	config Config
	conn   net.Conn
	reader *bufio.Reader
	writer *frameWriter

	mu       sync.Mutex
	session  [6]byte
	charging map[byte]*charge
}

func newBoard(config Config, conn net.Conn) *board {
	return &board{
		config:   config,
		conn:     conn,
		reader:   bufio.NewReader(conn),
		writer:   &frameWriter{conn: conn},
		charging: map[byte]*charge{},
	}
}

// register performs the A0/A1 exchange and adopts the session bytes the server
// issues. Every later frame carries them, which is how the server correlates
// frames to this connection.
func (b *board) register(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	frame, err := dc589.BuildRegistration(b.config.Identity)
	if err != nil {
		return fmt.Errorf("build registration: %w", err)
	}
	if err := b.writer.send(frame); err != nil {
		return fmt.Errorf("send registration: %w", err)
	}
	reply, err := b.readFrame()
	if err != nil {
		return fmt.Errorf("read register reply: %w", err)
	}
	status, at, err := dc589.ParseRegisterReply(reply)
	if err != nil {
		return fmt.Errorf("parse register reply: %w", err)
	}
	if status != 0 {
		return fmt.Errorf("gateway refused the registration with status %d", status)
	}
	b.mu.Lock()
	b.session = reply.Session
	b.mu.Unlock()
	b.config.Log.Printf("registered at %s with server time %s", b.config.Gateway, at.Format(time.RFC3339))
	return nil
}

// loop multiplexes the three things a board does at once: answering server
// commands, heartbeating, and letting a charge run to its conclusion.
func (b *board) loop(ctx context.Context) error {
	frames := make(chan dc589.Frame, 16)
	readErr := make(chan error, 1)
	go func() {
		for {
			frame, err := b.readFrame()
			if err != nil {
				readErr <- err
				return
			}
			select {
			case frames <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()

	ticker := time.NewTicker(b.config.Heartbeat)
	defer ticker.Stop()
	// Metering advances on its own cadence so a charge progresses between
	// heartbeats even when the server is not asking.
	meter := time.NewTicker(time.Second)
	defer meter.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-readErr:
			return err
		case frame := <-frames:
			if err := b.handle(ctx, frame); err != nil {
				return err
			}
		case <-meter.C:
			b.advance()
		case <-ticker.C:
			b.sendHeartbeat()
		}
	}
}

// handle answers one server frame.
func (b *board) handle(ctx context.Context, frame dc589.Frame) error {
	switch frame.Command {
	case dc589.StartCharge:
		command, err := dc589.ParseStartCommand(frame)
		if err != nil {
			return fmt.Errorf("parse start command: %w", err)
		}
		return b.start(ctx, command)
	case dc589.StopCharge:
		port, err := dc589.ParseStopCommand(frame)
		if err != nil {
			return fmt.Errorf("parse stop command: %w", err)
		}
		return b.stop(port, 0)
	case dc589.TimeRequest:
		if len(frame.Data) != 6 {
			return nil
		}
		return b.writer.send(dc589.BuildTimeRequest())
	default:
		// Register, heartbeat, charge-end and fault replies need no answer.
		return nil
	}
}

// start begins or refuses a charge. A refusal is what the server turns into a
// refund, so it is answered with a non-zero result code.
func (b *board) start(ctx context.Context, command dc589.StartCommand) error {
	code := byte(0)
	if b.config.Scenario == ScenarioRejectStart {
		code = 1
	}
	reply, err := dc589.BuildCommandResult(dc589.StartReply, code, command.Port)
	if err != nil {
		return err
	}
	if err := b.writer.send(reply); err != nil {
		return err
	}
	if code != 0 {
		b.config.Log.Printf("refused start on port %d", command.Port)
		return nil
	}

	port := command.Port
	if port == 0 || int(port) > b.config.PortCount {
		return fmt.Errorf("start for port %d, but the board has %d ports", port, b.config.PortCount)
	}
	now := time.Now()
	// The quantity means minutes for a time-based charge and milliwatt-hours
	// for an energy-based one, which is why it is a single field on the wire.
	remaining := time.Duration(command.Quantity) * time.Minute
	if command.Mode == dc589.ByEnergy || command.Mode == dc589.LongEnergy {
		remaining = time.Duration(command.Quantity) * time.Millisecond * 1000
	}
	if b.config.Scenario == ScenarioStopOnCommand {
		remaining = time.Hour // effectively unbounded; B9 ends it
	}
	b.mu.Lock()
	b.charging[port] = &charge{port: port, orderBCD: command.OrderBCD, startedAt: now, remaining: remaining}
	b.mu.Unlock()
	b.config.Log.Printf("started charge on port %d, mode %d, quantity %d", port, command.Mode, command.Quantity)
	return nil
}

// stop ends a charge and reports it with BB, which is what closes the order.
// A stop for a port that is not charging is still acknowledged: it is a
// legitimate late command, not a reason to drop the connection.
func (b *board) stop(port byte, reason byte) error {
	ack, err := dc589.BuildCommandResult(dc589.StopReply, 0, port)
	if err != nil {
		return err
	}
	b.mu.Lock()
	running, ok := b.charging[port]
	if ok {
		delete(b.charging, port)
	}
	b.mu.Unlock()
	if err := b.writer.send(ack); err != nil {
		return err
	}
	if !ok {
		b.config.Log.Printf("stop for idle port %d acknowledged", port)
		return nil
	}
	return b.reportEnd(running, reason)
}

func (b *board) reportEnd(running *charge, reason byte) error {
	ended := time.Now()
	frame, err := dc589.BuildChargeEnd(dc589.ChargeEndReport{
		Port:           running.port,
		OrderBCD:       running.orderBCD,
		StartedAt:      running.startedAt,
		EndedAt:        ended,
		ChargedMWh:     running.charged,
		PowerDeciWatts: b.config.PowerDeciWatts,
		StopReason:     reason,
		ConsumerType:   2,
	})
	if err != nil {
		return fmt.Errorf("build charge end: %w", err)
	}
	if err := b.writer.send(frame); err != nil {
		return err
	}
	b.config.Log.Printf("finished charge on port %d: %d mWh over %s", running.port, running.charged, ended.Sub(running.startedAt).Truncate(time.Second))
	return nil
}

// advance moves every running charge forward one second of simulated time.
func (b *board) advance() {
	b.mu.Lock()
	var finished []*charge
	for port, running := range b.charging {
		// A milliwatt-hour per second is 3.6 kWh per hour, the rate a board
		// would report at roughly 3.6 kW.
		running.charged += 3600
		running.remaining -= time.Second
		if b.config.Scenario == ScenarioFault && !running.faultSent && time.Since(running.startedAt) >= b.config.FaultAfter {
			running.faultSent = true
			b.config.Log.Printf("reporting a fault on port %d", port)
			if frame, err := dc589.BuildFault(port, 0x35); err == nil {
				_ = b.writer.send(frame)
			}
		}
		if running.remaining > 0 {
			continue
		}
		switch b.config.Scenario {
		case ScenarioStopOnCommand, ScenarioFault:
			// These end on a command or a fault rather than on their own.
			continue
		default:
			finished = append(finished, running)
			delete(b.charging, port)
		}
	}
	b.mu.Unlock()
	for _, running := range finished {
		if err := b.reportEnd(running, 0); err != nil {
			b.config.Log.Printf("charge end failed: %v", err)
		}
	}
}

// sendHeartbeat reports the board and, when something is charging, the state of
// each charging port. The extended form is what feeds the live curve.
func (b *board) sendHeartbeat() {
	b.mu.Lock()
	var status *dc589.PortStatus
	if len(b.charging) > 0 {
		ports := make([]byte, b.config.PortCount)
		charging := make([]protocol.PortTelemetry, 0, len(b.charging))
		for port, running := range b.charging {
			ports[port-1] = 1
			charging = append(charging, protocol.PortTelemetry{
				Port:           port,
				RemainingSecs:  uint32(max64(running.remaining.Seconds(), 0)),
				ChargedSeconds: uint32(time.Since(running.startedAt).Seconds()),
				ChargedMWh:     running.charged,
				PowerDeciWatts: b.config.PowerDeciWatts,
			})
		}
		status = &dc589.PortStatus{PortStates: ports, Charging: charging, TemperatureC: 25}
	}
	b.mu.Unlock()

	frame, err := dc589.BuildHeartbeat(b.config.Identity, status)
	if err != nil {
		b.config.Log.Printf("build heartbeat: %v", err)
		return
	}
	if err := b.writer.send(frame); err != nil {
		b.config.Log.Printf("send heartbeat: %v", err)
	}
}

func (b *board) readFrame() (dc589.Frame, error) {
	_ = b.conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	return dc589.ReadFrame(b.reader)
}

func max64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// frameWriter serialises writes. The heartbeat, the command replies and the
// charge-end report all originate from different places in the loop, and two
// frames interleaved on the wire would desynchronise the server's reader.
type frameWriter struct {
	conn net.Conn
	mu   sync.Mutex
}

func (w *frameWriter) send(frame dc589.Frame) error {
	raw, err := dc589.Encode(frame)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err = w.conn.Write(raw)
	return err
}
