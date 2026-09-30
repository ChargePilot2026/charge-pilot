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

// 这些测试让模拟板通过真实 TCP socket 面对真实的网关适配器。
// 假服务器证明不了组帧、会话协商或载荷
// 校验，而这三样正是本工具存在的理由。

// recordingSink 记录网关从板子解析出的内容，
// 这样测试断言的是解码后的值而不是字节。
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

// serveGateway 在真实端口上跑真实适配器。
// 它使用的 registry 会一并返回，
// 好让测试像控制层那样下发命令，而不是直接伸手进板子的连接里。
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

// 能注册并发心跳的板子必须被真实适配器看懂，
// 这是任何场景有意义的前提。
func TestSimulatorRegistersAndHeartbeats(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	sink := newSink()
	address, _ := serveGateway(t, ctx, sink)

	config := quietConfig(t, ScenarioByEnergy)
	config.Gateway = address
	go func() { _ = Run(ctx, config) }()

	// 板子登录后平台会立刻告诉它心跳周期，它就采用这个值。
	// 模拟器自己配置的周期只是它在此之前用的，
	// 所以一个假定它会按自己速度一直跳的测试，
	// 断言的是协议并不具备的行为：
	// 自 5.8.6 起间隔由服务器说了算。
	waitFor(t, 8*time.Second, func() bool { return sink.heartbeatCount() >= 1 })
	if got := sink.deviceID(); got != config.Identity.BoardID {
		t.Fatalf("gateway saw device %q, want %q", got, config.Identity.BoardID)
	}
}

// 充电链路才是本工具的意义所在：启动命令、确认、
// 计量，然后是一份网关能绑回订单的收尾上报。
func TestSimulatorRunsAChargeToCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	sink := newSink()
	address, registry := serveGateway(t, ctx, sink)

	config := quietConfig(t, ScenarioByEnergy)
	config.Gateway = address
	// 按电量来算的话，一分钟的充电会超过测试时长，
	// 所以改用停止命令驱动场景，这也是真实路径。
	config.Scenario = ScenarioStopOnCommand
	// 取 3600.0W，
	// 于是充电一秒就是整整一毫瓦时。在 150W 默认值下，
	// 短测试得到的读数会被截断成 0，
	// 这也正是下面的断言考究的是一致性而不是「非零即可」的原因。
	config.PowerDeciWatts = 36000
	go func() { _ = Run(ctx, config) }()

	// 等注册完成，
	// 然后像控制层那样经 registry 下发启动命令。
	session := commandSession()
	order := [8]byte{0, 0, 0, 0, 0, 0, 0x01, 0x23}
	command := protocol.Command{Kind: protocol.CommandStart, SessionID: session, Port: 1, OrderBCD: order, Mode: 0, Quantity: 60}
	if err := commandThrough(t, registry, config.Identity.BoardID, command); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 10*time.Second, func() bool { return len(sink.ofType(protocol.StartResult)) > 0 })

	// 让板子计够时间，使收尾上
	// 报里的时长和电量都大到可比；
	// 立刻停止只能证明帧对得上。3600.0W
	// 下三秒就是整整三瓦时，
	// 正好让读数宽过帧自身的分辨率。
	time.Sleep(3 * time.Second)

	// 让板子停止；它应当确认并上报充电结束。
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
	// 结算必须和板子一路报上来的功率一致，
	// 否则钱据以结算的电量就不是桩实际取的电量。
	assertEnergyAgrees(t, ends.EnergyMilliKWh, ends.ChargedSeconds, ends.PowerDeciWatts)
	if len(sink.ofType(protocol.StopResult)) == 0 {
		t.Fatal("the stop acknowledgement never reached the gateway")
	}
}

// 被拒的启动才是驱动退款路径的东西，
// 所以失败必须是一个格式良好的结果，而不是沉默。
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

// reconnect 场景验证的正是：被替换掉的会
// 话会被当成一条新连接，而不是旧连接的延续。
func TestSimulatorReconnectsAsANewSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	sink := newSink()
	address, _ := serveGateway(t, ctx, sink)

	config := quietConfig(t, ScenarioReconnect)
	config.Gateway = address
	config.ReconnectAfter = 200 * time.Millisecond
	go func() { _ = Run(ctx, config) }()

	// 每次重连都会重新注册一次；
	// 两次就足以证明板子真的回来了，而不是第一次尝试还挂着。
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

// commandSession 是测试下发命令时用的会话。
//
// 生产中控制层从联系板子之前持久化的充电命令记录里读这个值；
// 网关从不会把会话交还给 sink，
// 板子也只是原样回显服务器放进帧头的会
// 话。在这里造一个就复现了这条流程，而
// commandThrough 本身会等到板子可路由为止。
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

// commandThrough 像控制层那样把命令路由到板子：按设备 id，
// 经适配器持有的 registry。
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

// scriptedGateway 接入一块板子，转发测试推给它的任何东西。
//
// 它跑的是真实编解码器和真实 socket，
// 所以组帧与载荷校验仍被真正验证到。
// 它唯一不做的是自己决定下发哪些下行。
// 这个区别很关键：完整适配器适合证明两端互通，
// 却不适合证明「平台要求某件事时板子会怎么做」，
// 因为一旦由适配器来选，
// 「端口块出现是因为我们要求了」与「端口块总是出现」就分不出来了。
type scriptedGateway struct {
	address  string
	session  [6]byte
	traffic  *boardTraffic
	downlink chan dc589.Frame
	// clock 是服务器回答对时请求时声称的时间，registerClock 是它盖进注册回包的那个。
	// 分成两个字段是因为：
	// 一块正确处理了 A1 随后又忽略 A9 的板子，只要两者一
	// 致看上去就完全正常——而这正是测试必须先走出来的状态，
	// 否则根本看不出差别。
	clock         time.Time
	registerClock time.Time
}

// boardTraffic 是板子发出过的全部帧，按顺序排列。
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

// waitFor 返回 `from` 及其之后第一帧满足 match 的帧。
//
// 用下标而不是按内容重新匹配，才能让测试
// 断言*同一条*命令在一个会话内改变了形态。
// 端口遥测开关没法用别的方式证明：
// 一直发扩展格式的板子和一直听话的板子，在一半的状态下产生的流量完全相同。
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

// startCharge 让板子开始充电，就像一笔已付款的订单那样。
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

// A8 是板子索要时间，A9 是服务器回答，
// 所以服务器从不下发 A8，
// 板子也从来收不到 A8。
// 于是处理 A8 根本不可达，而服务器确实下发的 A9 则掉进 default 分支被丢弃。
// 服务器每次连接都在提供校正的同时，
// 板子却一直用自己未校正的时钟给每笔结算打戳。
func TestBoardAsksForTimeAndSettlesOnTheServerClock(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	// 注册回包和后来的对时请求被故意设成不
	// 一致。只读 A1 的板子此后整个会话都会偏上两者的差值，而这
	// 正是现场一块桩登录后时钟漂移时的情形：
	// 平台每次连接都主动提供校正，
	// 板子却走了过去。
	registerClock := time.Now()
	serverClock := registerClock.Add(2 * time.Hour)
	gateway := serveScriptedGateway(t, ctx, serverClock)
	gateway.registerClock = registerClock

	logs := &captureLog{}
	config := quietConfig(t, ScenarioByTime)
	config.Gateway = gateway.address
	config.Log = log.New(logs, "", 0)
	go func() { _ = Run(ctx, config) }()

	// 板子是主动索要而不是等着被告知：对时
	// 事件是时钟校正在会话审计里唯一显形之处。
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

// 自 5.8.6 起端口块只在平台要求时才出现，
// 所以这个开关就是 A6 的全部意义。
// 板子记下了它却从不读取，
// 于是「本版本无视指令」与「没有充电在进行」会产出同一个 17 字节心跳。
func TestBoardHonoursThePortTelemetryFlag(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	gateway := serveScriptedGateway(t, ctx, time.Now())

	config := quietConfig(t, ScenarioByTime)
	config.Gateway = gateway.address
	go func() { _ = Run(ctx, config) }()

	// 开关关着时板子必须退回短格式。
	off, err := dc589.BuildHeartbeatInterval(gateway.session, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	mark := gateway.traffic.mark()
	gateway.push(t, off)
	if got := gateway.traffic.waitFor(t, 8*time.Second, mark, isHeartbeat); hasPortStatus(got) {
		t.Fatal("the board reported ports after the platform had switched them off")
	}

	// 有一笔充电在进行，所以这时必须出现扩展格式。
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

	// 而且在充电中途再关掉也必须立刻生效。
	// 这一步不可能蒙混过关：
	// 一直发扩展格式的板子同样能心安理得地满足上一条断言。
	mark = gateway.traffic.mark()
	gateway.push(t, off)
	if got := gateway.traffic.waitFor(t, 8*time.Second, mark, isHeartbeat); hasPortStatus(got) {
		t.Fatal("the board kept reporting ports after the platform switched them off mid-charge")
	}
}

// 板子答不了的下行过去会无影无踪，
// 两边都没有记录，
// 于是「模拟器没实现」与「网关没发」从外面看无法区分。
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

	// 0xE0 是网关已有编解码器、
	// 却还没有调用方的功率控制，
	// 所以它正是那种「本来会到达然后丢失」的命令的典型形态。
	frame, err := dc589.SetNoTiering(gateway.session)
	if err != nil {
		t.Fatal(err)
	}
	gateway.push(t, frame)

	waitFor(t, 5*time.Second, func() bool { return logs.contains("unhandled downlink 0xE0") })
	// 忽略它不能以丢掉链路为代价。
	mark := gateway.traffic.mark()
	interval, err := dc589.BuildHeartbeatInterval(gateway.session, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	gateway.push(t, interval)
	gateway.traffic.waitFor(t, 8*time.Second, mark, isHeartbeat)
}

// 零值不是一张合法的参数表，
// 所以从未被配置过的板子会在自己的校验里失败，对一次读操作回「拒绝」
// ——在压根没人发过费率的情况下告诉平台费率没送达。
// 这次读必须回的是一张真实的表。
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

// newTestBoard 给板子一条真实的内存连接。
//
// 跑到上限的充电会通过链路上报结束，
// 所以一个把充电走到完成的单元测试需要有个地方可写。
// 连接为 nil 会让断言变成段错误而不是一个结果。
func newTestBoard(t *testing.T, config Config) *board {
	t.Helper()
	conn, peer := net.Pipe()
	t.Cleanup(func() { _ = conn.Close(); _ = peer.Close() })
	go func() { _, _ = io.Copy(io.Discard, peer) }()
	return newBoard(config.withDefaults(), conn)
}

// captureLog 收集板子的日志行，
// 好让测试断言某件事确实被上报了，而不是被吞掉。
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

// 按电量下单时数量单位是瓦时，而线路不带单位，
// 所以唯一能说明它含义的就是充电模式。
// 当成秒数读会让一笔 1Wh 的订单一秒就结束，
// 随后还按好几瓦时上报，
// 于是用户被按远超实际送出量的金额计费，而结算里看不出任何异常。
func TestEnergyOrderDoesNotTreatItsQuantityAsSeconds(t *testing.T) {
	board := newTestBoard(t, quietConfig(t, ScenarioByEnergy))
	// 这里直接持有这笔充电而不从 map 里读回来：
	// 计量有错的板子会提前结束并把自己从 map 里摘掉，
	// 查询一个不存在的 key 会把这件事变成
	// nil 解引用，而不是测试真正要做的断言。
	running := &charge{port: 1, mode: dc589.ByEnergy, targetMilliWh: 1000}
	board.charging[1] = running

	board.advance()
	if running.complete() {
		t.Fatalf("a 1Wh order finished after one second; its quantity is being read as a duration")
	}
	// 150W 每 36/1500 秒填满一毫瓦时，所以一秒买到 41.67mWh。
	if got := running.chargedMilliWh(); got != 41 {
		t.Fatalf("after one second at 150W the meter read %d mWh, want 41", got)
	}
}

// 板子上报的电量与它上报的功率必须是同一个数字来源。
// 它们过去是两个互不相干的常数——电表每个
// tick 固定存 3600 mWh，而板子声称 150W—
// —等于心跳里的功率和结算里的电量之间差
// 24 倍，而结算正是钱据以移动的数字。
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
	// 心跳在两种计费模式下都用秒作单位，
	// 所以按电量下单的充电要按它当前取的功率推算。
	// 若按电量数字那样只报一个没有时长的值，
	// 等于告诉平台这笔充电刚开始就结束了。
	if got := board.remainingSecs(running); got != 180 {
		t.Fatalf("the heartbeat would have reported %d seconds left, want 180", got)
	}
}

// 整条链必须自洽：板子上报的功率决定它的计量速率，
// 速率决定买到的电量要充多久，
// 订单结束以电表为准，而不是碰巧与某个时钟一致。
func TestEnergyOrderEndsWhenTheMeterReachesThePurchasedEnergy(t *testing.T) {
	config := quietConfig(t, ScenarioByEnergy)
	config.PowerDeciWatts = 1500 // 150.0W
	board := newTestBoard(t, config)
	board.charging[1] = &charge{port: 1, mode: dc589.ByEnergy, targetMilliWh: 5 * 1000}

	// 150.0W 每 24 秒填满 1000mWh，所以 5Wh 是两分钟的充电。
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

// 按时间下单由时钟封顶，而它上报的电量同样来自那个功率，
// 不来自某个常数。
func TestTimeOrderEndsWhenItsClockRunsOut(t *testing.T) {
	board := newTestBoard(t, quietConfig(t, ScenarioByTime))
	board.charging[1] = &charge{port: 1, mode: dc589.ByTime, remaining: 3 * time.Second}

	board.advance()
	board.advance()
	if board.charging[1].complete() {
		t.Fatal("a three second order finished after two seconds")
	}
	// 板子还占着这个端口，这是该结论的另一半。
	board.advance()
	if _, running := board.charging[1]; running {
		t.Fatal("a three second order was still running after three seconds")
	}
}

// assertEnergyAgrees 用板子一并上
// 报的功率和时长校验一份结算的电量。
//
// 电量单位是毫千瓦时，也就是网关事件承载的单
// 位，也正是帧里整瓦时字段换算到的单位。
// 容差取该单位的十分之一加一，
// 因为帧按整瓦时计数，只跑了几秒的充
// 电不可能比它自身的分辨率卡得更准。
// 宽到足以吸收这种取整的容差，也会把电表过去上报的那个 24 倍偏差一并吸收掉，
// 所以容差只放在这一处，而不是随着短充电略有偏差在各调用点被逐个放宽。
func assertEnergyAgrees(t *testing.T, milliKWh, seconds, deciWatts uint32) {
	t.Helper()
	// 一毫瓦时是板子累加器每秒的 36 个单位，而一毫千瓦时是这些单位的一千倍，
	// 所以两次换算在这里正好抵消。
	want := uint64(deciWatts) * uint64(seconds) / (deciWattSecondsPerMilliWh * 1000)
	slack := want/10 + 1
	got := uint64(milliKWh)
	if got+slack < want || want+slack < got {
		t.Fatalf("the settlement reported %d milli-kWh over %d seconds at %d deciwatts, want about %d milli-kWh",
			milliKWh, seconds, deciWatts, want)
	}
}

// 结算是钱据以移动的数字，
// 所以它承载的电量必须与板子一路报上来的功率和时长一致。
// 取功率定得足够高，让这个数字能扛过
// 帧的整瓦时分辨率而不被截断成 0。
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
	// 3600.0W 的一秒是 1000mWh，
	// 所以这笔充电每跑一秒应该计量到约一毫瓦时。
	assertEnergyAgrees(t, report.ChargedMWh/1000, report.ChargedSeconds, report.PowerDeciWatts)
}
