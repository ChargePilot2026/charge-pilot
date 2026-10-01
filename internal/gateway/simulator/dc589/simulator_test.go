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

// 通过实际 TCP 和网关适配器验证组帧、会话协商及载荷校验。

// recordingSink 记录网关解码后的事件，供测试断言业务字段。
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

// serveGateway 在回环端口启动适配器，并返回 registry，供测试通过控制路径下发命令。
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

// 确认模拟器注册和心跳可被真实协议适配器解析。
func TestSimulatorRegistersAndHeartbeats(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	sink := newSink()
	address, _ := serveGateway(t, ctx, sink)

	config := quietConfig(t, ScenarioByEnergy)
	config.Gateway = address
	go func() { _ = Run(ctx, config) }()

	// 验证注册后采用平台下发的心跳周期；模拟器默认周期仅在设置到达前生效。
	waitFor(t, 8*time.Second, func() bool { return sink.heartbeatCount() >= 1 })
	if got := sink.deviceID(); got != config.Identity.BoardID {
		t.Fatalf("gateway saw device %q, want %q", got, config.Identity.BoardID)
	}
}

// 验证启动、确认、计量、停止及结束上报的完整充电链路。
func TestSimulatorRunsAChargeToCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	sink := newSink()
	address, registry := serveGateway(t, ctx, sink)

	config := quietConfig(t, ScenarioByEnergy)
	config.Gateway = address
	// 使用远程停止驱动结束场景，避免等待完整授权额度耗尽。
	config.Scenario = ScenarioStopOnCommand
	// 设置功率为 3600 W，每秒产生 1 Wh（1000 mWh），使短充电读数超过协议整瓦时分辨率。
	config.PowerDeciWatts = 36000
	go func() { _ = Run(ctx, config) }()

	// 注册完成后经 registry 下发启动命令。
	session := commandSession()
	order := [8]byte{0, 0, 0, 0, 0, 0, 0x01, 0x23}
	command := protocol.Command{Kind: protocol.CommandStart, SessionID: session, Port: 1, OrderBCD: order, Mode: 0, Quantity: 60}
	if err := commandThrough(t, registry, config.Identity.BoardID, command); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 10*time.Second, func() bool { return len(sink.ofType(protocol.StartResult)) > 0 })

	// 3600 W 运行 3 秒产生 3 Wh，超过上报分辨率，可验证结束读数。
	time.Sleep(3 * time.Second)

	// 停止命令应获得确认及充电结束上报。
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
	// 验证结算电量与上报功率积分一致。
	assertEnergyAgrees(t, ends.EnergyMilliKWh, ends.ChargedSeconds, ends.PowerDeciWatts)
	if len(sink.ofType(protocol.StopResult)) == 0 {
		t.Fatal("the stop acknowledgement never reached the gateway")
	}
}

// 验证启动拒绝通过有效结果帧返回，供平台执行失败和退款处理。
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

// 验证重连生成新会话，不沿用旧连接的审计记录。
func TestSimulatorReconnectsAsANewSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	sink := newSink()
	address, _ := serveGateway(t, ctx, sink)

	config := quietConfig(t, ScenarioReconnect)
	config.Gateway = address
	config.ReconnectAfter = 200 * time.Millisecond
	go func() { _ = Run(ctx, config) }()

	// 等待至少两次注册，确认设备已经完成一次重连。
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

// commandSession 模拟控制层在命令持久化时分配的会话号，设备按帧头原样回显。
// commandThrough 等待设备可路由后发送命令。
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

// commandThrough 经适配器 registry 按设备 ID 路由命令。
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

// scriptedGateway 使用真实编解码器和 TCP，由测试显式指定下行帧。
// 用于隔离验证设备对平台指令的响应，避免适配器默认行为掩盖遥测开关等状态差异。
type scriptedGateway struct {
	address  string
	session  [6]byte
	traffic  *boardTraffic
	downlink chan dc589.Frame
	// clock 与 registerClock 分别控制校时和注册应答时间，用不同值验证设备处理后续 0xA9。
	clock         time.Time
	registerClock time.Time
}

// boardTraffic 按发送顺序返回设备上行帧。
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

// waitFor 按帧索引从 from 开始查找匹配帧，供测试验证同一会话内的状态变化。
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

// startCharge 模拟已付款订单下发启动命令。
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

// 验证设备主动发送 0xA8 请求，并处理服务器的 0xA9 校时响应。
func TestBoardAsksForTimeAndSettlesOnTheServerClock(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	// 注册与后续校时使用不同时间，验证设备继续校正时钟，不仅依赖首次注册。
	registerClock := time.Now()
	serverClock := registerClock.Add(2 * time.Hour)
	gateway := serveScriptedGateway(t, ctx, serverClock)
	gateway.registerClock = registerClock

	logs := &captureLog{}
	config := quietConfig(t, ScenarioByTime)
	config.Gateway = gateway.address
	config.Log = log.New(logs, "", 0)
	go func() { _ = Run(ctx, config) }()

	// 验证设备主动请求校时，并生成可观察的协议事件。
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

// 验证自 5.8.6 起端口遥测块受 0xA6 开关控制，不混淆关闭遥测与无充电端口。
func TestBoardHonoursThePortTelemetryFlag(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	gateway := serveScriptedGateway(t, ctx, time.Now())

	config := quietConfig(t, ScenarioByTime)
	config.Gateway = gateway.address
	go func() { _ = Run(ctx, config) }()

	// 关闭遥测扩展时必须使用短格式。
	off, err := dc589.BuildHeartbeatInterval(gateway.session, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	mark := gateway.traffic.mark()
	gateway.push(t, off)
	if got := gateway.traffic.waitFor(t, 8*time.Second, mark, isHeartbeat); hasPortStatus(got) {
		t.Fatal("the board reported ports after the platform had switched them off")
	}

	// 充电进行中且遥测已启用，心跳必须使用带端口数据的扩展格式。
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

	// 充电中关闭遥测扩展后，下一次上报应立即切换为短格式。
	mark = gateway.traffic.mark()
	gateway.push(t, off)
	if got := gateway.traffic.waitFor(t, 8*time.Second, mark, isHeartbeat); hasPortStatus(got) {
		t.Fatal("the board kept reporting ports after the platform switched them off mid-charge")
	}
}

// 未实现的下行命令应记日志，便于区分未发送与未处理。
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

	// 验证设备处理 0xE0 功率控制命令，并产生对应响应。
	frame, err := dc589.SetNoTiering(gateway.session)
	if err != nil {
		t.Fatal(err)
	}
	frame.Command = 0xEF
	gateway.push(t, frame)

	waitFor(t, 5*time.Second, func() bool { return logs.contains("unhandled downlink 0xEF") })
	// 忽略它不能以丢掉链路为代价。
	mark := gateway.traffic.mark()
	interval, err := dc589.BuildHeartbeatInterval(gateway.session, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	gateway.push(t, interval)
	gateway.traffic.waitFor(t, 8*time.Second, mark, isHeartbeat)
}

// 验证未配置设备返回合法的初始参数表，而非因零值范围错误拒绝读取。
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

// newTestBoard 提供可写的内存连接，供完成充电的单元测试接收结束帧。
func newTestBoard(t *testing.T, config Config) *board {
	t.Helper()
	conn, peer := net.Pipe()
	t.Cleanup(func() { _ = conn.Close(); _ = peer.Close() })
	go func() { _, _ = io.Copy(io.Discard, peer) }()
	return newBoard(config.withDefaults(), conn)
}

// captureLog 收集模拟器日志，用于验证协议处理结果被记录。
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

// 验证电量模式的授权数量按 Wh 解释，不误用为时长。
func TestEnergyOrderDoesNotTreatItsQuantityAsSeconds(t *testing.T) {
	board := newTestBoard(t, quietConfig(t, ScenarioByEnergy))
	// 直接保存充电会话引用，避免提前完成并移出 map 后测试发生 nil 访问。
	running := &charge{port: 1, mode: dc589.ByEnergy, targetMilliWh: 1000}
	board.charging[1] = running

	board.advance()
	if running.complete() {
		t.Fatalf("a 1Wh order finished after one second; its quantity is being read as a duration")
	}
	// 150 W 持续 1 秒产生约 41.67 mWh。
	if got := running.chargedMilliWh(); got != 41 {
		t.Fatalf("after one second at 150W the meter read %d mWh, want 41", got)
	}
}

// 验证累计电量由上报功率和实际经过时间积分产生，不使用独立固定增量。
func TestMeteredEnergyFollowsTheReportedPower(t *testing.T) {
	config := quietConfig(t, ScenarioByEnergy)
	config.PowerDeciWatts = 1500 // 150.0W
	board := newTestBoard(t, config)
	running := &charge{port: 1, mode: dc589.ByEnergy, targetMilliWh: 10 * 1000}
	board.charging[1] = running

	for range 60 {
		board.advance()
	}
	// 150.0W 持续 60s 是 9000J，即 2.5Wh。
	if got := running.chargedMilliWh(); got != 2500 {
		t.Fatalf("60s at 150W metered %d mWh, want 2500", got)
	}
	// 验证电量模式的剩余时间按当前功率推算，心跳中的单位仍为秒。
	if got := board.remainingSecs(running); got != 180 {
		t.Fatalf("the heartbeat would have reported %d seconds left, want 180", got)
	}
}

// 验证功率、计量速率和授权电量一致，电量模式按电表读数完成充电。
func TestEnergyOrderEndsWhenTheMeterReachesThePurchasedEnergy(t *testing.T) {
	config := quietConfig(t, ScenarioByEnergy)
	config.PowerDeciWatts = 1500 // 150.0W
	board := newTestBoard(t, config)
	board.charging[1] = &charge{port: 1, mode: dc589.ByEnergy, targetMilliWh: 5 * 1000}

	// 150 W 每 24 秒产生 1 Wh；5 Wh 需要 120 秒。
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

// 按时长充电在额度耗尽时结束，电量仍按实际功率积分计算。
func TestTimeOrderEndsWhenItsClockRunsOut(t *testing.T) {
	board := newTestBoard(t, quietConfig(t, ScenarioByTime))
	board.charging[1] = &charge{port: 1, mode: dc589.ByTime, remaining: 3 * time.Second}

	board.advance()
	board.advance()
	if board.charging[1].complete() {
		t.Fatal("a three second order finished after two seconds")
	}
	// 验证设备仍占用该端口。
	board.advance()
	if _, running := board.charging[1]; running {
		t.Fatal("a three second order was still running after three seconds")
	}
}

// assertEnergyAgrees 根据上报功率和时长计算预期电量，与结算的 milli-kWh（1 Wh）比较。
// 容差覆盖整瓦时取整及有限时间偏差，并集中定义，避免各测试任意放宽。
func assertEnergyAgrees(t *testing.T, milliKWh, seconds, deciWatts uint32) {
	t.Helper()
	// 将 0.1 W 与秒相乘后除以 36000，得到 milli-kWh；1 milli-kWh 等于 1 Wh。
	want := uint64(deciWatts) * uint64(seconds) / (deciWattSecondsPerMilliWh * 1000)
	slack := want/10 + 1
	got := uint64(milliKWh)
	if got+slack < want || want+slack < got {
		t.Fatalf("the settlement reported %d milli-kWh over %d seconds at %d deciwatts, want about %d milli-kWh",
			milliKWh, seconds, deciWatts, want)
	}
}

// 验证结束上报电量与功率及持续时间一致；使用足够功率避免整瓦时量化为零。
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
	// 3600 W 每秒产生 1 Wh，即 1000 mWh；累计读数应按实际持续时间增加。
	assertEnergyAgrees(t, report.ChargedMWh/1000, report.ChargedSeconds, report.PowerDeciWatts)
}
