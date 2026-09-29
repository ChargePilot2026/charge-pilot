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

// deciWattSecondsPerMilliWh converts the board's energy accumulator into
// milliwatt-hours: a milliwatt-hour is 3.6 watt-seconds, and the accumulator
// counts tenths of a watt-second, so one milliwatt-hour is 36 of them.
//
// Accumulating in 0.1W·s and converting only when a figure goes on the wire is
// what keeps the metering exact. The alternative — banking a fixed number of
// milliwatt-hours per tick — cannot be reconciled with the power the board
// reports, because the two then become independent constants: a board claiming
// 150W while banking 3.6kWh every hour over-delivers by 24x, and every number
// in the settlement still looks like a plausible one.
const deciWattSecondsPerMilliWh = 36

// charge is the state of one running charge.
type charge struct {
	port      byte
	orderBCD  [8]byte
	mode      dc589.ChargeMode
	startedAt time.Time
	// wattDeciSeconds is the energy delivered so far, in tenths of a watt-second.
	wattDeciSeconds uint64
	// targetMilliWh is what the order paid for, in milliwatt-hours. It is only
	// read on an energy-billed charge; a time-billed one carries its limit in
	// remaining instead.
	targetMilliWh uint32
	// remaining is the time left on a time-billed charge.
	remaining  time.Duration
	stopReason byte
	faultSent  bool
}

// energyBilled reports whether the order's quantity is an energy figure rather
// than a duration.
//
// The wire carries a single unsigned field for both, and the charge mode is the
// only thing that says which one it is. Reading an energy order as a duration
// is how a 1Wh order becomes a one-second charge that reports several times the
// energy it was sold.
func energyBilled(mode dc589.ChargeMode) bool {
	return mode == dc589.ByEnergy || mode == dc589.LongEnergy
}

// chargedMilliWh is the energy delivered so far.
func (c *charge) chargedMilliWh() uint32 {
	return uint32(c.wattDeciSeconds / deciWattSecondsPerMilliWh)
}

// remainingMilliWh is what the order still owes, never negative.
func (c *charge) remainingMilliWh() uint32 {
	if delivered := c.chargedMilliWh(); delivered < c.targetMilliWh {
		return c.targetMilliWh - delivered
	}
	return 0
}

// complete reports whether the charge has delivered everything the order paid
// for. An energy order finishes on the meter and a time order on the clock,
// because that is the thing the user actually bought.
func (c *charge) complete() bool {
	if energyBilled(c.mode) {
		return c.chargedMilliWh() >= c.targetMilliWh
	}
	return c.remaining <= 0
}

type board struct {
	config Config
	conn   net.Conn
	reader *bufio.Reader
	writer *frameWriter

	mu       sync.Mutex
	session  [6]byte
	charging map[byte]*charge
	// clockOffset is how far the board's own clock trails the server's, seeded
	// from the register reply and refreshed by every A9. Settlement is timed on
	// the timestamps the board stamps itself, so an uncorrected clock does not
	// merely look wrong in a log — it places the whole session in the wrong
	// time, and the readings that follow it are compared against those times.
	clockOffset time.Duration
	// heartbeat is re-armed whenever the platform sets a new period, and
	// portTelemetry records whether port data is included. Both are server
	// decisions since 5.8.6, so the board keeps them rather than assuming its
	// own defaults still apply.
	heartbeat     *time.Ticker
	portTelemetry bool
	configTable   dc589.ConfigTable
}

// factoryTable is the parameter table a board ships with.
//
// It exists because the zero value is not a legal table. The firmware refuses a
// temperature guard outside 50-100 with 0xFF as the only escape, and likewise
// bounds the float charge and the removal timer, so a board that had never been
// configured would fail its own validation and answer a read with "rejected" —
// the platform would conclude its tariff had failed to land when in fact nobody
// had ever sent one.
func factoryTable() dc589.ConfigTable {
	return dc589.ConfigTable{
		RunMode:          0,   // 先充电后按键
		LocalCoinTime:    60,  // 本地投币一次 60 分钟
		LocalCardTime:    60,  // 本地刷卡一次 60 分钟
		CardAmountCents:  100, // 刷卡一次 1.00 元
		CardRefund:       0,   // 刷卡不退费
		TierWatts:        [5]uint16{100, 200, 300, 400, 500},
		TierRatioPercent: [5]byte{100, 80, 60, 40, 20},
		StopWhenFull:     0,    // 充满不停
		FloatDeciWatts:   50,   // 浮充 5.0W
		FloatSeconds:     1800, // 浮充 30 分钟
		RemoveSeconds:    300,  // 5 分钟未拔则停
		TemperatureGuard: 80,   // 80℃ 保护
	}
}

func newBoard(config Config, conn net.Conn) *board {
	return &board{
		config:      config,
		conn:        conn,
		reader:      bufio.NewReader(conn),
		writer:      &frameWriter{conn: conn},
		charging:    map[byte]*charge{},
		configTable: factoryTable(),
	}
}

// now is the time the board believes it is, expressed in the civil timezone the
// protocol carries. encodeTime writes the calendar fields of whatever location
// it is handed, so a board stamping an event in the host's local zone would have
// it read back by the server as a different instant.
func (b *board) now() time.Time {
	b.mu.Lock()
	offset := b.clockOffset
	b.mu.Unlock()
	return dc589.Civil(time.Now().Add(offset))
}

// setClock adopts the server's time. The correction is kept as an offset rather
// than as a new time base, so the board's clock keeps advancing at the host's
// real rate and a second sync measures the drift instead of re-deriving it.
func (b *board) setClock(server time.Time) {
	offset := server.Sub(time.Now())
	b.mu.Lock()
	previous := b.clockOffset
	b.clockOffset = offset
	b.mu.Unlock()
	b.config.Log.Printf("server time %s adopted, clock moved %s", server.Format(time.RFC3339), (offset - previous).Truncate(time.Second))
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
	// The register reply already carries the server's time, so the board is
	// calibrated before it sends anything else. Reading it here rather than
	// waiting for the A9 means even a gateway that never answers a time request
	// cannot leave the board stamping settlements from an uncorrected clock.
	b.clockOffset = at.Sub(time.Now())
	b.mu.Unlock()
	b.config.Log.Printf("registered at %s with server time %s", b.config.Gateway, at.Format(time.RFC3339))
	// Ask for time explicitly as well. The server answers an A8 with an A9 and
	// records a time-sync event, and that event is the only place a clock
	// correction becomes visible in the session audit — without it, a board that
	// drifted after login would be corrected silently and never traceable.
	if err := b.writer.send(dc589.BuildTimeRequest()); err != nil {
		return fmt.Errorf("send time request: %w", err)
	}
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

	b.heartbeat = time.NewTicker(b.config.Heartbeat)
	// Through a closure, because the field is replaced every time the platform
	// sets a new period: evaluating it now would stop the original ticker on the
	// way out and leave the one actually in use running.
	defer func() { b.heartbeat.Stop() }()
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
		case <-b.heartbeat.C:
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
	case dc589.TimeReply:
		// A9 is the server's answer to the A8 this board sent at registration.
		// Handling A8 here instead was dead code on two counts: the gateway never
		// sends A8, because A8 travels board to server, and the A9 it does send
		// fell through to the default branch and was dropped without trace. The
		// correction therefore never happened even though the server was offering
		// one on every connection.
		server, err := dc589.ParseTimeReply(frame)
		if err != nil {
			return fmt.Errorf("parse time reply: %w", err)
		}
		b.setClock(server)
		return nil
	case dc589.HeartbeatInterval:
		// Since 5.8.6 this also decides whether port telemetry appears in each
		// heartbeat. A board that ignores it would be reporting whatever its
		// factory default was, which is precisely the ambiguity the command
		// exists to remove, so the simulator honours it and then uses the value
		// it was given rather than its own default.
		ok, err := dc589.ParseHeartbeatInterval(frame)
		if err != nil {
			return err
		}
		b.setHeartbeat(ok.Seconds)
		b.portTelemetry = ok.PortStatus
		return b.writer.send(dc589.BuildHeartbeatSetReply(ok))
	case dc589.SetConfig:
		return b.applyConfig(frame)
	case dc589.ReadConfig:
		if err := b.writer.send(dc589.BuildReadConfigAck()); err != nil {
			return err
		}
		report, err := dc589.BuildConfigReport(b.configTable)
		if err != nil {
			// A table the board could not encode is reported as a rejected
			// write rather than dropped, so the platform is never left waiting
			// for a read that will not come.
			return b.writer.send(dc589.BuildConfigAck(1))
		}
		return b.writer.send(report)
	case dc589.RegisterReply, dc589.HeartbeatReply, dc589.ChargeEndReply, dc589.FaultReply:
		// Acknowledgements of frames this board has already sent. Answering an
		// acknowledgement would start a loop, so silence is the correct reading
		// of the protocol rather than a gap in the switch.
		return nil
	default:
		// Anything else is a downlink this build does not implement, and
		// swallowing it is the one response that makes it undiagnosable: the
		// command vanishes with no record on either side, so "the simulator
		// never implemented it" and "the gateway never sent it" look identical
		// from the outside. Naming the byte is the whole difference — a remote
		// control or a power control showing up here means the product grew a
		// caller for a command the simulator is not yet answering.
		b.config.Log.Printf("unhandled downlink 0x%02X (%d bytes) ignored", frame.Command, len(frame.Data))
		return nil
	}
}

// setHeartbeat adopts the period the server asked for.
func (b *board) setHeartbeat(seconds uint16) {
	if seconds == 0 {
		return
	}
	if b.heartbeat != nil {
		b.heartbeat.Stop()
	}
	b.heartbeat = time.NewTicker(time.Duration(seconds) * time.Second)
}

// applyConfig accepts a parameter table the way a board would: it validates the
// ranges, and a rejected write leaves the previous table in place. A simulator
// that accepted everything would make the 0xC4 error path untestable, which is
// the only way the platform learns its tariff did not reach the device.
func (b *board) applyConfig(frame dc589.Frame) error {
	table, err := dc589.DecodeConfig(frame)
	if err != nil {
		return b.writer.send(dc589.BuildConfigAck(1))
	}
	b.configTable = table
	return b.writer.send(dc589.BuildConfigAck(0))
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
	running := &charge{port: port, orderBCD: command.OrderBCD, mode: command.Mode, startedAt: b.now()}
	// The quantity means minutes on a time order and watt-hours on an energy
	// one, which is why one field carries both and the mode is the only thing
	// that says which it is. Treating the energy figure as a count of seconds
	// finished a 1Wh order in one second while still reporting several watt-hours
	// against it.
	if energyBilled(command.Mode) {
		running.targetMilliWh = uint32(command.Quantity) * 1000
	} else {
		running.remaining = time.Duration(command.Quantity) * time.Minute
	}
	if b.config.Scenario == ScenarioStopOnCommand || b.config.Scenario == ScenarioFault {
		// These two end on a command or a fault rather than on their own, so the
		// limit only has to sit far enough away never to be the reason. It still
		// has to be a real figure in both modes: the heartbeat reports the
		// projected time left, and a charge answering "zero remaining" reads on
		// the platform as one that is about to end on its own.
		const unbounded = 24 * time.Hour
		running.remaining = unbounded
		if running.targetMilliWh == 0 {
			running.targetMilliWh = uint32(uint64(b.config.PowerDeciWatts) *
				uint64(unbounded/time.Second) / deciWattSecondsPerMilliWh)
		}
	}
	b.mu.Lock()
	b.charging[port] = running
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
	ended := b.now()
	frame, err := dc589.BuildChargeEnd(dc589.ChargeEndReport{
		Port:           running.port,
		OrderBCD:       running.orderBCD,
		StartedAt:      running.startedAt,
		EndedAt:        ended,
		ChargedMWh:     running.chargedMilliWh(),
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
	b.config.Log.Printf("finished charge on port %d: %d mWh over %s", running.port, running.chargedMilliWh(), ended.Sub(running.startedAt).Truncate(time.Second))
	return nil
}

// remainingSecs projects how much longer a charge will run, in seconds.
//
// The heartbeat's remaining field asks the same question in both billing modes,
// so an energy-billed charge is projected from the energy it still owes divided
// by the power it is actually drawing. Answering zero there would tell the
// platform a charge is about to end when it has barely begun.
func (b *board) remainingSecs(running *charge) uint32 {
	if !energyBilled(running.mode) {
		if running.remaining <= 0 {
			return 0
		}
		return uint32(running.remaining / time.Second)
	}
	if b.config.PowerDeciWatts == 0 {
		return 0
	}
	return uint32(uint64(running.remainingMilliWh()) * deciWattSecondsPerMilliWh / uint64(b.config.PowerDeciWatts))
}

// advance moves every running charge forward one second of simulated time.
func (b *board) advance() {
	b.mu.Lock()
	var finished []*charge
	for port, running := range b.charging {
		// One tick is one second, so the board delivers the power the platform
		// configured for that second. Deriving the energy from the same figure it
		// reports is the point: a settlement that disagrees with the power
		// reading by a constant ratio is one nobody ever catches, because the
		// energy still lands within a range an operator would call reasonable.
		running.wattDeciSeconds += uint64(b.config.PowerDeciWatts)
		if !energyBilled(running.mode) {
			running.remaining -= time.Second
		}
		if b.config.Scenario == ScenarioFault && !running.faultSent && time.Since(running.startedAt) >= b.config.FaultAfter {
			running.faultSent = true
			b.config.Log.Printf("reporting a fault on port %d", port)
			if frame, err := dc589.BuildFault(port, 0x35); err == nil {
				_ = b.writer.send(frame)
			}
		}
		if !running.complete() {
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

// sendHeartbeat reports the board and, when the platform asked for port data
// and something is charging, the state of each charging port.
//
// Whether the port block appears at all is the platform's decision since 5.8.6,
// taken in A6. Reporting it unconditionally would make "this build ignores what
// it was told" indistinguishable from "this build is fine", which is the exact
// ambiguity the command exists to remove — and it would mean a platform that
// had deliberately turned port telemetry off still saw it arrive.
func (b *board) sendHeartbeat() {
	b.mu.Lock()
	var status *dc589.PortStatus
	if b.portTelemetry && len(b.charging) > 0 {
		ports := make([]byte, b.config.PortCount)
		charging := make([]protocol.PortTelemetry, 0, len(b.charging))
		for port, running := range b.charging {
			ports[port-1] = 1
			charging = append(charging, protocol.PortTelemetry{
				Port:           port,
				RemainingSecs:  b.remainingSecs(running),
				ChargedSeconds: uint32(time.Since(running.startedAt).Seconds()),
				RemainingMWh:   running.remainingMilliWh(),
				ChargedMWh:     running.chargedMilliWh(),
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
