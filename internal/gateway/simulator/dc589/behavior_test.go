package dc589sim

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	wire "github.com/ChargePilot2026/charge-pilot/internal/protocol/dc589"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Capture actual encoded wire frames, independently inspect bytes/lengths and
// session IDs. The real TCP adapter suite remains in simulator_test.go.
type captureConn struct{ bytes.Buffer }

func (c *captureConn) Close() error                     { return nil }
func (c *captureConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *captureConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *captureConn) SetDeadline(time.Time) error      { return nil }
func (c *captureConn) SetReadDeadline(time.Time) error  { return nil }
func (c *captureConn) SetWriteDeadline(time.Time) error { return nil }
func (c *captureConn) frames(t *testing.T) []wire.Frame {
	t.Helper()
	var f []wire.Frame
	reader := bufio.NewReader(c)
	for c.Len() > 0 || reader.Buffered() > 0 {
		v, err := wire.ReadFrame(reader)
		if err != nil {
			t.Fatal(err)
		}
		f = append(f, v)
	}
	return f
}
func capturedBoard(t *testing.T, ports int) (*board, *captureConn) {
	t.Helper()
	config := quietConfig(t, ScenarioByTime).withDefaults()
	config.PortCount = ports
	c := &captureConn{}
	b := newBoard(config, c)
	b.online = true
	return b, c
}

func TestAdjustPowerWhileChargingUpdatesMeterAndTelemetry(t *testing.T) {
	b, conn := capturedBoard(t, 2)
	if err := b.start(context.Background(), wire.StartCommand{Port: 1, Mode: wire.ByTime, Quantity: 60, ConsumerType: 2}); err != nil {
		t.Fatal(err)
	}
	conn.frames(t)
	c := b.charging[1]
	for _, power := range []uint32{1500, 3500, 1000} {
		before := c.wattDeciSeconds
		if err := b.applyInput(Input{Type: "power", Port: 1, Power: power}); err != nil {
			t.Fatal(err)
		}
		b.advance()
		if b.charging[1] != c || c.wattDeciSeconds-before != uint64(power) {
			t.Fatal("power adjustment interrupted charging or used stale power")
		}
		if err := b.sendAllPorts(false, wire.Heartbeat); err != nil {
			t.Fatal(err)
		}
		frames := conn.frames(t)
		hb, err := wire.ParseHeartbeat(frames[len(frames)-1])
		if err != nil || len(hb.ChargingPorts) != 1 || hb.ChargingPorts[0].PowerDeciWatts != power {
			t.Fatalf("telemetry: %+v %v", hb, err)
		}
	}
	if b.physical(2).Power != b.config.PowerDeciWatts {
		t.Fatal("another port changed")
	}
	m := terminalModel{port: 1, state: b.snapshot()}
	if m.powerFields()[0].value != "100.0" {
		t.Fatal("power form did not retain current value")
	}
}

func TestHeartbeatPolicyKeepsChargingIntervalUntilAllPortsEnd(t *testing.T) {
	b, conn := capturedBoard(t, 2)
	b.heartbeat = time.NewTicker(time.Hour)
	defer func() { b.heartbeat.Stop() }()
	b.charging[1] = &charge{port: 1, mode: wire.ByTime, remaining: time.Hour, startedAt: time.Now()}
	b.charging[2] = &charge{port: 2, mode: wire.ByTime, remaining: time.Hour, startedAt: time.Now()}
	b.chargingHeartbeat()
	if b.snapshot().HeartbeatSeconds != 15 {
		t.Fatal("charging heartbeat is not 15s")
	}
	delete(b.charging, 1)
	b.chargingHeartbeat()
	if b.snapshot().HeartbeatSeconds != 15 {
		t.Fatal("another charging port must retain 15s")
	}
	delete(b.charging, 2)
	b.chargingHeartbeat()
	if b.snapshot().HeartbeatSeconds != 60 {
		t.Fatal("all idle ports must use 60s")
	}
	if len(conn.frames(t)) != 3 {
		t.Fatal("state transitions must report immediately")
	}
}
func TestTwentyPortsQueriesAndAsyncCorrelation(t *testing.T) {
	b, c := capturedBoard(t, 20)
	for p := byte(1); p <= 20; p++ {
		b.charging[p] = &charge{port: p, mode: wire.ByTime, remaining: time.Hour, startedAt: time.Now(), band: 1}
	}
	session := [6]byte{1, 2, 3, 4, 5, 6}
	if err := b.handle(context.Background(), wire.Frame{Command: 0xb0, Session: session, Data: []byte{0}}); err != nil {
		t.Fatal(err)
	}
	f := c.frames(t)
	if len(f) != 2 {
		t.Fatalf("twenty active ports need two frames: %d", len(f))
	}
	for _, v := range f {
		if v.Command != 0xb1 || len(v.Data) != 156 || v.Data[25] != 20 || v.Session != session {
			t.Fatalf("invalid B1: %+v", v)
		}
	}
	_ = b.handle(context.Background(), wire.Frame{Command: 0xb0, Session: session, Data: []byte{0xfe}})
	f = c.frames(t)
	if len(f) != 1 || len(f[0].Data) != 25 {
		t.Fatalf("short B1: %+v", f)
	}
	_ = b.handle(context.Background(), wire.Frame{Command: 0xb2, Session: session, Data: []byte{20}})
	f = c.frames(t)
	if len(f) != 1 || len(f[0].Data) != 36 || f[0].Data[0] != 20 || f[0].Session != session {
		t.Fatalf("B3: %+v", f)
	}
	if err := b.applyInput(Input{Type: "card", Port: 1, Card: 42}); err != nil {
		t.Fatal(err)
	}
	f = c.frames(t)
	if len(f) != 1 || f[0].Command != 0xb6 || f[0].Session != ([6]byte{}) {
		t.Fatalf("async B6 inherited correlation: %+v", f)
	}
}

func TestDurationExpiryBetweenTicksReportsPurchasedMinute(t *testing.T) {
	b, conn := capturedBoard(t, 1)
	now := time.Date(2026, 10, 1, 0, 0, 0, 800000000, time.UTC)
	b.clockOffset = now.Sub(time.Now())
	if err := b.start(context.Background(), wire.StartCommand{Port: 1, Mode: wire.ByTime, Quantity: 1, ConsumerType: 2}); err != nil {
		t.Fatal(err)
	}
	conn.frames(t)
	base := now.Truncate(time.Second)
	for i := range 60 {
		now = base.Add(time.Duration(i)*time.Second + 900*time.Millisecond)
		b.clockOffset = now.Sub(time.Now())
		b.advance()
	}
	if b.charging[1] == nil {
		t.Fatal("first partial tick expired a minute purchase early")
	}
	now = now.Add(time.Second)
	b.clockOffset = now.Sub(time.Now())
	b.advance()
	if b.charging[1] != nil {
		t.Fatal("duration purchase did not expire")
	}
	for _, frame := range conn.frames(t) {
		if frame.Command != wire.ChargeEnd {
			continue
		}
		report, err := wire.ParseChargeEnd(frame)
		if err != nil || report.ChargedSeconds != 60 {
			t.Fatalf("duration end=%+v err=%v", report, err)
		}
		return
	}
	t.Fatal("missing end report")
}
func TestConfigRoundTripRejectsInvalidAndKeepsRawFields(t *testing.T) {
	b, c := capturedBoard(t, 2)
	frame, _ := wire.BuildSetConfig(factoryTable())
	frame.Data[1] = 4
	binary.LittleEndian.PutUint16(frame.Data[30:32], 8888)
	_ = b.handle(context.Background(), frame)
	f := c.frames(t)
	if f[0].Command != 0xc4 || f[0].Data[0] != 0 {
		t.Fatal(f)
	}
	_ = b.handle(context.Background(), wire.Frame{Command: 0xc5, Data: []byte{0}})
	f = c.frames(t)
	if len(f) != 1 || f[0].Command != 0xc6 || !bytes.Equal(f[0].Data, frame.Data) {
		t.Fatalf("readback dropped volume/password: %+v", f)
	}
	bad := frame
	bad.Data = append([]byte(nil), frame.Data...)
	bad.Data[1] = 5
	_ = b.handle(context.Background(), bad)
	f = c.frames(t)
	if f[0].Data[0] != 2 || !bytes.Equal(b.rawConfig, frame.Data) {
		t.Fatal("invalid config replaced active config")
	}
}
func TestPowerControlLittleEndianQueryAndFailure(t *testing.T) {
	b, c := capturedBoard(t, 2)
	_ = b.handle(context.Background(), wire.Frame{Command: 0xe0, Data: []byte{1, 0, 0x34, 0x12, 0, 0}})
	f := c.frames(t)
	if b.removePower != 0x1234 || !bytes.Equal(f[0].Data, []byte{1, 0, 0x34, 0x12, 0, 0}) {
		t.Fatal(f)
	}
	_ = b.handle(context.Background(), wire.Frame{Command: 0xe0, Data: []byte{0, 0, 0, 0, 0, 0}})
	f = c.frames(t)
	if binary.LittleEndian.Uint16(f[0].Data[2:4]) != 0x1234 {
		t.Fatal(f)
	}
	_ = b.handle(context.Background(), wire.Frame{Command: 0xe0, Data: []byte{1, 9, 0, 0, 0, 0}})
	f = c.frames(t)
	if f[0].Data[0] != 2 || f[0].Data[2] != 0xf1 || f[0].Data[3] != 0xff {
		t.Fatal(f)
	}
}
func TestCardPresentationRequiresRemovalAndRecordsServerReplies(t *testing.T) {
	b, c := capturedBoard(t, 2)
	for range 2 {
		if err := b.applyInput(Input{Type: "card", Port: 1, Card: 42}); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.frames(t)) != 1 {
		t.Fatal("held card repeated event")
	}
	_ = b.applyInput(Input{Type: "remove-card", Port: 1})
	_ = b.applyInput(Input{Type: "card", Port: 1, Card: 42})
	if len(c.frames(t)) != 1 {
		t.Fatal("reswipe was suppressed")
	}
	d := make([]byte, 7)
	binary.LittleEndian.PutUint32(d[1:5], 42)
	binary.LittleEndian.PutUint16(d[5:7], 123)
	_ = b.handle(context.Background(), wire.Frame{Command: 0xd1, Data: d})
	if s := b.cards[42]; !s.Valid || s.BalanceUnits != 123 {
		t.Fatal(s)
	}
	_ = b.applyInput(Input{Type: "balance", Card: 42})
	f := c.frames(t)
	if f[0].Command != 0xd0 || len(f[0].Data) != 5 {
		t.Fatal(f)
	}
	d = append(d, 0, 0)
	d[0] = 1
	_ = b.handle(context.Background(), wire.Frame{Command: 0xbd, Data: d})
	if !b.cards[42].Denied || b.cards[42].Valid {
		t.Fatal(b.cards[42])
	}
}
func TestLocalStartAcknowledgementAndFaultReport(t *testing.T) {
	b, c := capturedBoard(t, 2)
	if err := b.applyInput(Input{Type: "local-start", Port: 1, Consumer: 1, Card: 42, Quantity: 60}); err != nil {
		t.Fatal(err)
	}
	f := c.frames(t)
	if f[0].Command != 0xb4 || f[0].Data[23] != 1 || binary.LittleEndian.Uint32(f[0].Data[32:36]) != 42 {
		t.Fatal(f)
	}
	_ = b.handle(context.Background(), wire.Frame{Command: 0xb5, Data: []byte{0, 1}})
	if len(b.pending) != 0 {
		t.Fatal("local report not acknowledged")
	}
	_ = b.applyInput(Input{Type: "fault", Port: 1, Code: 4})
	f = c.frames(t)
	if f[0].Command != 0xc0 || f[0].Data[1] != 4 {
		t.Fatal(f)
	}
	b.advance()
	f = c.frames(t)
	if len(b.charging) != 0 || f[0].Command != 0xbb || f[0].Data[29] != 1 || binary.LittleEndian.Uint32(f[0].Data[36:40]) != 42 {
		t.Fatal(f)
	}
	_ = b.handle(context.Background(), wire.Frame{Command: 0xc1, Data: []byte{0, 1}})
	_ = b.handle(context.Background(), wire.Frame{Command: 0xbc, Data: []byte{0, 1}})
	if len(b.pending) != 0 {
		t.Fatal("pending receipts retained after ack")
	}
}
func TestPersistentBoardRestoresChargesConfigurationAndReplayProtection(t *testing.T) {
	b, c := capturedBoard(t, 2)
	b.config.StateFile = filepath.Join(t.TempDir(), "board.json")
	b.config.Identity.SoftwareVersion++
	b.smoke = true
	b.completed["1:order"] = true
	b.charging[2] = &charge{port: 2, startedAt: time.Now(), remaining: time.Hour, mode: wire.ByTime, card: 42, band: 1}
	_ = b.applyInput(Input{Type: "fault", Port: 1, Code: 3})
	c.frames(t)
	fresh := newBoard(b.config, &captureConn{})
	if err := fresh.restore(); err != nil {
		t.Fatal(err)
	}
	if fresh.charging[2].card != 42 || !fresh.smoke || !fresh.completed["1:order"] || len(fresh.pending) != 1 || fresh.lastMeter.IsZero() {
		t.Fatal(fresh.snapshot())
	}
	other := b.config
	other.Identity.BoardID = "5348240514082653"
	if err := newBoard(other, &captureConn{}).restore(); err == nil {
		t.Fatal("different board accepted persisted state")
	}
}
func TestFullStopRemoveAndLongChargeBehavior(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    wire.ChargeMode
		unplug  bool
		wantEnd bool
	}{{"full", wire.ByTime, false, true}, {"unplug", wire.ByTime, true, true}, {"long ignores full", wire.LongTime, false, false}, {"long ignores unplug", wire.LongTime, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			b, c := capturedBoard(t, 2)
			b.configTable.StopWhenFull = 1
			b.configTable.FloatSeconds = 2
			b.configTable.RemoveSeconds = 2
			b.charging[1] = &charge{port: 1, startedAt: time.Now(), remaining: time.Hour, mode: tc.mode, band: 1}
			b.physical(1).Power = 20
			b.physical(1).Connected = !tc.unplug
			b.advance()
			b.advance()
			if (b.charging[1] == nil) != tc.wantEnd {
				t.Fatal(b.snapshot())
			}
			c.frames(t)
		})
	}
}
func TestRebootStopsChargingWithoutChangingFirmware(t *testing.T) {
	b, conn := capturedBoard(t, 2)
	if err := b.start(context.Background(), wire.StartCommand{Port: 1, Mode: wire.ByTime, Quantity: 60, ConsumerType: 2}); err != nil {
		t.Fatal(err)
	}
	conn.frames(t)
	identity := b.config.Identity
	if err := b.applyInput(Input{Type: "restart"}); err != nil {
		t.Fatal(err)
	}
	if b.config.Identity != identity || len(b.charging) != 0 {
		t.Fatal("reboot changed firmware identity or retained a charging session")
	}
	frames := conn.frames(t)
	if len(frames) != 1 || frames[0].Command != wire.ChargeEnd {
		t.Fatalf("reboot must report the interrupted charge: %+v", frames)
	}
}

func TestTUIActionsValidateRangesAndStayOnSelectedPort(t *testing.T) {
	m := terminalModel{port: 2, current: terminalActions[0], fields: []field{{"card", "42"}}}
	in, err := m.event()
	if err != nil || in.Port != 2 || in.Card != 42 {
		t.Fatal(in, err)
	}
	m.current = terminalActions[3]
	m.fields = []field{{"power", "7000"}}
	if _, err := m.event(); err == nil {
		t.Fatal("overflow power accepted")
	}
	m.fields[0].value = "150.5"
	in, err = m.event()
	if err != nil || in.Power != 1505 {
		t.Fatal(in, err)
	}
	m.width, m.height = 100, 30
	m.state.Ports = map[byte]*PhysicalPort{1: {Connected: true}, 2: {Connected: true}}
	m.logs = &terminalLog{}
	if !strings.Contains(m.View(), "当前端口 2") {
		t.Fatal("selection absent")
	}
}
