// Package dc589sim 提供开发和测试使用的 DC589 设备模拟器，通过 TCP 执行实际 5.8.9 协议。
// 复用协议编解码器，覆盖注册、心跳、控制及计量，不随生产进程启动。
// 不同厂商或传输协议使用独立模拟器包，避免混用设备行为。
package dc589sim

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol/dc589"
)

// Scenario 选择充电行为；各场景共用注册与心跳流程，仅改变启动后的执行结果。
type Scenario string

const (
	// ScenarioByEnergy 一直充到要求的电量送完为止。
	ScenarioByEnergy Scenario = "by-energy"
	// ScenarioByTime 一直充到要求的时长走完为止。
	ScenarioByTime Scenario = "by-time"
	// ScenarioStopOnCommand 一直充到服务器下发 B9，
	// 用来在不等到充满的前提下走通停止路径。
	ScenarioStopOnCommand Scenario = "stop-on-command"
	// ScenarioRejectStart 对 B8 回一个失败，
	// 用来驱动服务器「被拒即退款」的路径。
	ScenarioRejectStart Scenario = "reject-start"
	// ScenarioFault 在充电开始后不久上报一个故障。
	ScenarioFault Scenario = "fault"
	// ScenarioSilent 注册之后就不再说话，
	// 便于观察读超时和会话被遗弃之后的清理。
	ScenarioSilent Scenario = "silent"
	// ScenarioReconnect 断开并重新注册，
	// 用来验证会话替换与连接审计。
	ScenarioReconnect Scenario = "reconnect"
)

// Config 是一块模拟板所需的全部配置。
// 速率一律用协议自身的单位，方便和线路字段直接比对。
type Config struct {
	Inputs    <-chan Input
	StateFile string
	Identity  dc589.DeviceIdentity
	PortCount int
	Gateway   string
	Scenario  Scenario
	Heartbeat time.Duration
	// PowerDeciWatts 是充电时的恒定功率，单位 0.1W。
	PowerDeciWatts uint32
	// ReconnectAfter 是 ScenarioReconnect 下重连前的停顿。
	ReconnectAfter time.Duration
	// FaultAfter 是 ScenarioFault 下故障上报的延迟。
	FaultAfter time.Duration
	// Log 接收进度日志，为 nil 时丢弃。
	Log *log.Logger
}

func (c Config) withDefaults() Config {
	if c.Heartbeat <= 0 {
		c.Heartbeat = 60 * time.Second
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

// Run 建立连接并服务到 context 被取消
// 为止：reconnect 场景会自己重建连接，
// 其余场景在连接结束时返回。
func Run(ctx context.Context, config Config) error {
	config = config.withDefaults()
	if config.PortCount < 1 || config.PortCount > 20 || config.PowerDeciWatts > 65535 {
		return fmt.Errorf("DC589 needs 1-20 ports and power <=65535 deciwatts")
	}
	b := newBoard(config, nil)
	if err := b.restore(); err != nil {
		return err
	}
	for {
		err := b.serveOnce(ctx)
		b.online = false
		b.writer.conn = nil
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			config.Log.Printf("connection ended: %v; reconnecting in %s", err, config.ReconnectAfter)
		} else {
			config.Log.Printf("reconnecting in %s", config.ReconnectAfter)
		}
		timer := time.NewTimer(config.ReconnectAfter)
	backoff:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case in := <-config.Inputs:
				out := b.input(in)
				if in.Reply != nil {
					in.Reply <- out
				}
			case <-timer.C:
				break backoff
			}
		}
	}
}

func (board *board) serveOnce(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	config := board.config
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", config.Gateway)
	if err != nil {
		return fmt.Errorf("dial gateway: %w", err)
	}
	defer conn.Close()
	board.conn = conn
	board.reader = bufio.NewReader(conn)
	board.writer = &frameWriter{conn: conn, log: config.Log}
	board.session = [6]byte{}

	// 关闭连接以中断阻塞读取。
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
	board.online = true
	if config.Scenario == ScenarioSilent {
		config.Log.Printf("scenario %s: registered, staying silent", config.Scenario)
		<-ctx.Done()
		return nil
	}
	board.missedHeartbeats = 0
	for _, f := range board.pending {
		if err := board.writer.send(f); err != nil {
			return err
		}
	}
	return board.loop(ctx)
}

// deciWattSecondsPerMilliWh 将 0.1 W·s 累加值转换为 mWh：1 mWh = 3.6 W·s = 36 个累加单位。
// 先积分功率与时间，发送时再换算电量，避免逐 tick 取整或使用固定增量。
const deciWattSecondsPerMilliWh = 36

// charge 是一笔正在进行的充电的状态。
type charge struct {
	consumer       byte
	card           uint32
	band           byte
	peak           uint32
	floatSeconds   uint32
	removedSeconds uint32
	port           byte
	orderBCD       [8]byte
	mode           dc589.ChargeMode
	startedAt      time.Time
	// wattDeciSeconds 是已送出的电量，单位 0.1 瓦秒。
	wattDeciSeconds uint64
	// targetMilliWh 是这笔订单买下的电量，
	// 单位毫瓦时。只有按电量计费的充电会读
	// 它；按时间计费的把上限放在 remaining 里。
	targetMilliWh uint32
	// remaining 是按时间计费的充电还剩多久。
	remaining    time.Duration
	notBeforeEnd time.Time
	stopReason   byte
	faultSent    bool
}

// energyBilled 根据充电模式判断授权数量是否为电量；协议共用数值字段，单位不能独立推断。
func energyBilled(mode dc589.ChargeMode) bool {
	return mode == dc589.ByEnergy || mode == dc589.LongEnergy
}

// chargedMilliWh 是已经送出的电量。
func (c *charge) chargedMilliWh() uint32 {
	return uint32(c.wattDeciSeconds / deciWattSecondsPerMilliWh)
}

// remainingMilliWh 是这笔订单还欠的电量，不会为负。
func (c *charge) remainingMilliWh() uint32 {
	if delivered := c.chargedMilliWh(); delivered < c.targetMilliWh {
		return c.targetMilliWh - delivered
	}
	return 0
}

// complete 判断授权额度是否耗尽：电量模式比较累计电量，时长模式比较已用时长。
func (c *charge) complete() bool {
	if energyBilled(c.mode) {
		return c.chargedMilliWh() >= c.targetMilliWh
	}
	return c.remaining <= 0
}

type board struct {
	online           bool
	cards            map[uint32]CardStatus
	ports            map[byte]*PhysicalPort
	rawConfig        []byte
	removePower      uint16
	temperature      int16
	voltage          uint16
	smoke            bool
	pending          []dc589.Frame
	completed        map[string]bool
	lastMeter        time.Time
	missedHeartbeats int
	config           Config
	conn             net.Conn
	reader           *bufio.Reader
	writer           *frameWriter

	mu       sync.Mutex
	session  [6]byte
	charging map[byte]*charge
	// clockOffset 保存设备时钟相对服务器的偏移；注册应答初始化，后续 0xA9 校时更新。
	// 事件与结算时间戳均使用校正后的协议时间。
	clockOffset time.Duration
	// 收到平台心跳设置时重建 ticker，portTelemetry 保存端口遥测开关；两项均以平台下发值为准。
	heartbeat         *time.Ticker
	heartbeatInterval time.Duration
	portTelemetry     bool
	configTable       dc589.ConfigTable
}

// factoryTable 返回满足固件范围约束的初始参数表。
// 温度保护、浮充与移除定时等字段不能使用任意零值，避免首次查询被误判为无效配置。
func factoryTable() dc589.ConfigTable {
	return dc589.ConfigTable{
		RunMode:          0,   // 先充电后按键
		LocalCoinTime:    60,  // 本地投币一次 60 分钟
		LocalCardTime:    60,  // 本地刷卡一次 60 分钟
		CardAmountCents:  100, // 刷卡一次 1.00 元
		CardRefund:       0,   // 刷卡不退费
		TierWatts:        [5]uint16{100, 200, 300, 400, 500},
		TierRatioPercent: [5]byte{100, 100, 100, 100, 100},
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
		reader:      nil,
		writer:      &frameWriter{conn: conn, log: config.Log},
		charging:    map[byte]*charge{},
		configTable: factoryTable(),
		cards:       map[uint32]CardStatus{},
		ports:       map[byte]*PhysicalPort{}, completed: map[string]bool{}, temperature: 25, voltage: 220,
	}
}

// now 返回校正后的设备时间，并转换为协议民用时区，用于帧日期字段编码。
func (b *board) now() time.Time {
	b.mu.Lock()
	offset := b.clockOffset
	b.mu.Unlock()
	return dc589.Civil(time.Now().Add(offset))
}

// setClock 将服务器时间保存为相对宿主时钟的偏移，保留真实时间推进速率。
func (b *board) setClock(server time.Time) {
	offset := server.Sub(time.Now())
	b.mu.Lock()
	previous := b.clockOffset
	b.clockOffset = offset
	b.mu.Unlock()
	b.config.Log.Printf("server time %s adopted, clock moved %s", server.Format(time.RFC3339), (offset - previous).Truncate(time.Second))
}

// register 完成 A0/A1 交换，后续上行帧使用服务器分配的会话号。
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

	// 优先使用注册应答中的服务器时间校准时钟，避免依赖后续校时响应。
	b.clockOffset = at.Sub(time.Now())
	b.mu.Unlock()
	b.config.Log.Printf("registered at %s with server time %s", b.config.Gateway, at.Format(time.RFC3339))
	// 主动发送 0xA8 校时请求，接收 0xA9 并产生可审计的校时事件。
	if err := b.writer.send(dc589.BuildTimeRequest()); err != nil {
		return fmt.Errorf("send time request: %w", err)
	}
	return nil
}

// loop 处理下行命令、心跳及充电状态推进。
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
	b.heartbeatInterval = b.config.Heartbeat
	var reconnect <-chan time.Time
	if b.config.Scenario == ScenarioReconnect {
		timer := time.NewTimer(b.config.ReconnectAfter)
		defer timer.Stop()
		reconnect = timer.C
	}
	// 退出时通过闭包停止当前 ticker；平台更新周期可能已替换原 ticker。
	defer func() { b.heartbeat.Stop() }()
	// 计量按自己的节奏推进，这样即便服务器不催，
	// 充电在两次心跳之间也在走。
	meter := time.NewTicker(time.Second)
	defer meter.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-reconnect:
			return fmt.Errorf("simulated reconnect")
		case err := <-readErr:
			return err
		case in := <-b.config.Inputs:
			out := b.input(in)
			if in.Reply != nil {
				in.Reply <- out
			}
		case frame := <-frames:
			if err := b.handle(ctx, frame); err != nil {
				return err
			}
		case tick := <-meter.C:
			seconds := b.meterSeconds(tick)
			for i := 0; i < seconds; i++ {
				b.advance()
			}
			if err := b.persist(); err != nil {
				return err
			}
		case <-b.heartbeat.C:
			b.missedHeartbeats++
			if b.missedHeartbeats > dc589.MissedHeartbeatsBeforeReset {
				return fmt.Errorf("three heartbeats unacknowledged")
			}
			b.sendHeartbeat()
			for _, f := range b.pending {
				if err := b.writer.send(f); err != nil {
					return err
				}
			}
		}
	}
}

// meterSeconds 返回本次计量应推进的整秒数，保留不足一秒的余量供后续累计。
// 首次 tick 推进一秒；重复或早到的 tick 不改变已计量时刻。
func (b *board) meterSeconds(tick time.Time) int {
	if b.lastMeter.IsZero() {
		b.lastMeter = tick
		return 1
	}
	if !tick.After(b.lastMeter) {
		return 0
	}
	seconds := int(tick.Sub(b.lastMeter) / time.Second)
	if seconds > 0 {
		b.lastMeter = b.lastMeter.Add(time.Duration(seconds) * time.Second)
	}
	return seconds
}

// handle 应答一帧服务器下行。
func (b *board) handle(ctx context.Context, frame dc589.Frame) error {
	b.writer.session = frame.Session

	switch frame.Command {
	case 0xB0:
		if len(frame.Data) != 1 || (frame.Data[0] != 0 && frame.Data[0] != 0xfe) {
			return nil
		}
		return b.sendAllPorts(frame.Data[0] == 0xfe, 0xB1)
	case 0xB2:
		if len(frame.Data) != 1 {
			return nil
		}
		return b.writer.send(b.portReport(frame.Data[0], 0xB3))
	case 0xE0:
		return b.powerControl(frame)
	case dc589.RemoteControl:
		return b.remote(frame)
	case dc589.OnlineCardDenied, dc589.CardBalanceReply:
		expected := 7
		if frame.Command == dc589.OnlineCardDenied {
			expected = 9
		}
		if len(frame.Data) == expected {
			b.cards[binary.LittleEndian.Uint32(frame.Data[1:5])] = CardStatus{Valid: frame.Data[0] == 0, BalanceUnits: binary.LittleEndian.Uint16(frame.Data[5:7]), Denied: frame.Command == dc589.OnlineCardDenied}
			b.config.Log.Printf("card %d status %d, balance %.1f yuan", binary.LittleEndian.Uint32(frame.Data[1:5]), frame.Data[0], float64(binary.LittleEndian.Uint16(frame.Data[5:7]))/10)
		}
		return nil
	case dc589.HeartbeatReply:
		b.missedHeartbeats = 0
		return nil
	case dc589.ChargeEndReply:
		if len(frame.Data) == 2 && frame.Data[0] == 0 {
			b.acknowledge(dc589.ChargeEnd, frame.Data[1])
		}
		return nil
	case dc589.FaultReply:
		if len(frame.Data) == 2 && frame.Data[0] == 0 {
			b.acknowledge(dc589.Fault, frame.Data[1])
		}
		return nil
	case 0xB5:
		if len(frame.Data) == 2 && frame.Data[0] == 0 {
			b.acknowledge(0xB4, frame.Data[1])
		}
		return nil
	case dc589.StartCharge:
		command, err := dc589.ParseStartCommand(frame)
		if err != nil {
			port := byte(0)
			if len(frame.Data) > 0 {
				port = frame.Data[0]
			}
			return b.writer.send(dc589.Frame{Command: dc589.StartReply, Data: []byte{2, port}})
		}
		return b.start(ctx, command)
	case dc589.StopCharge:
		port, err := dc589.ParseStopCommand(frame)
		if err != nil {
			return fmt.Errorf("parse stop command: %w", err)
		}
		return b.stop(port, 7)
	case dc589.TimeReply:
		// A9 是平台对设备 A8 校时请求的应答。
		server, err := dc589.ParseTimeReply(frame)
		if err != nil {
			return fmt.Errorf("parse time reply: %w", err)
		}
		b.setClock(server)
		return nil
	case dc589.HeartbeatInterval:
		// 按 0xA6 下发的周期及端口遥测开关发送心跳，不保留旧默认配置。
		ok, err := dc589.ParseHeartbeatInterval(frame)
		if err != nil {
			return err
		}
		b.setHeartbeat(ok.Seconds)
		b.portTelemetry = ok.PortStatus
		if err := b.writer.send(dc589.BuildHeartbeatSetReply(ok)); err != nil {
			return err
		}
		// Registration/reconfiguration immediately reports actual port state so
		// the gateway can choose 15s for a resumed charge or 60s for idle.
		b.sendHeartbeat()
		return nil
	case dc589.SetConfig:
		return b.applyConfig(frame)
	case dc589.ReadConfig:
		if len(frame.Data) > 1 || (len(frame.Data) == 1 && frame.Data[0] != 0) {
			return nil
		}
		if len(b.rawConfig) > 0 {
			return b.writer.send(dc589.Frame{Command: dc589.ConfigReport, Data: append([]byte(nil), b.rawConfig...)})
		}
		report, err := dc589.BuildConfigReport(b.configTable)
		if err != nil {
			return err
		}
		return b.writer.send(report)
	case dc589.RegisterReply:
		// 这些帧确认设备已发送的请求，不再应答，避免形成确认循环。
		return nil
	default:
		// 记录未实现的下行命令字节，便于定位模拟器协议覆盖缺口。
		b.config.Log.Printf("unhandled downlink 0x%02X (%d bytes) ignored", frame.Command, len(frame.Data))
		return nil
	}
}

// setHeartbeat 采用服务器要求的周期。
func (b *board) setHeartbeat(seconds uint16) {
	if seconds == 0 {
		return
	}
	if b.heartbeat != nil {
		b.heartbeat.Stop()
	}
	b.heartbeat = time.NewTicker(time.Duration(seconds) * time.Second)
	b.heartbeatInterval = time.Duration(seconds) * time.Second
}

// applyConfig 校验参数范围；拒绝时保留原参数表，供 C4 失败应答测试使用。
func (b *board) applyConfig(frame dc589.Frame) error {
	code := configCode(frame)
	if code != 0 {
		return b.writer.send(dc589.BuildConfigAck(code))
	}
	table, err := dc589.DecodeConfig(frame)
	if err != nil {
		return b.writer.send(dc589.BuildConfigAck(1))
	}
	b.configTable = table
	b.rawConfig = append([]byte(nil), frame.Data...)
	if err := b.persist(); err != nil {
		return err
	}
	return b.writer.send(dc589.BuildConfigAck(0))
}

// start 执行或拒绝启动请求；拒绝时发送明确非零结果码，供平台处理启动失败与退款。
func (b *board) start(ctx context.Context, command dc589.StartCommand) error {
	code := byte(0)
	duplicate := false
	if command.Port == 0 || int(command.Port) > b.config.PortCount {
		code = 3
	} else if b.portState(command.Port) == 3 || b.portState(command.Port) == 4 || b.config.Scenario == ScenarioRejectStart {
		code = 1
	} else if !command.Mode.IsNormal() && command.Mode != dc589.LongTime && command.Mode != dc589.LongEnergy && command.Mode != dc589.LongPlatformBilling {
		code = 2
	} else if (command.ConsumerType != 2 && command.ConsumerType != 3) || command.Quantity == 0 {
		code = 2
	} else if command.ConsumerType == 3 && command.CardNumber == 0 {
		code = 2
	}
	key := fmt.Sprintf("%d:%x", command.Port, command.OrderBCD)
	if c := b.charging[command.Port]; c != nil {
		if c.orderBCD == command.OrderBCD && c.mode == command.Mode && c.card == command.CardNumber && c.consumer == command.ConsumerType {
			duplicate = true
		} else {
			code = 2
		}
	}
	if b.completed[key] {
		code = 2
	}
	reply, _ := dc589.BuildCommandResult(dc589.StartReply, code, command.Port)
	if err := b.writer.send(reply); err != nil {
		return err
	}
	if code != 0 || duplicate {
		return nil
	}
	port := command.Port
	// 协议时间戳和设备时钟均精确到秒，将启动时间对齐到秒，避免首个时钟步进使一分钟充电只累计 59 秒。
	running := &charge{port: port, orderBCD: command.OrderBCD, mode: command.Mode, startedAt: b.now().Truncate(time.Second), consumer: command.ConsumerType, card: command.CardNumber, band: 1}
	// 授权数量根据模式解释：时长模式单位为分钟，电量模式单位为 Wh。
	if energyBilled(command.Mode) {
		running.targetMilliWh = uint32(command.Quantity) * 1000
	} else {
		running.remaining = time.Duration(command.Quantity) * time.Minute
		running.notBeforeEnd = running.startedAt.Add(running.remaining)
	}
	b.mu.Lock()
	b.charging[port] = running
	b.mu.Unlock()
	b.chargingHeartbeat()
	if err := b.persist(); err != nil {
		return err
	}
	b.config.Log.Printf("started charge on port %d, mode %d, quantity %d", port, command.Mode, command.Quantity)
	return nil
}

// stop 结束一次充电并用 BB 上报，
// 这才让订单收尾。对没在充电的端口下发的停止同样要确认：
// 它是一条合法的迟到命令，不构成断连接的理由。
func (b *board) stop(port byte, reason byte) error {
	code := byte(0x10)
	if port == 0 || int(port) > b.config.PortCount {
		code = 0
	} else if b.portState(port) == 3 || b.portState(port) == 4 {
		code = 4
	} else if b.charging[port] == nil {
		code = 1
	}
	ack, err := dc589.BuildCommandResult(dc589.StopReply, code, port)
	if err != nil {
		return err
	}
	b.mu.Lock()
	running, ok := b.charging[port]
	if ok {
		b.completed[fmt.Sprintf("%d:%x", port, running.orderBCD)] = true
		delete(b.charging, port)
	}
	b.mu.Unlock()
	b.chargingHeartbeat()
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
		PowerDeciWatts: b.power(running.port),
		StopReason:     reason,
		ConsumerType:   running.consumer,
	})
	if err != nil {
		return fmt.Errorf("build charge end: %w", err)
	}
	frame.Data[0] = 1
	frame.Data[25] = byte(running.mode)
	frame.Data[41] = running.band
	binary.LittleEndian.PutUint32(frame.Data[36:40], running.card)
	binary.LittleEndian.PutUint16(frame.Data[30:32], uint16(running.remainingMilliWh()/1000))
	left := b.remainingSecs(running)
	binary.LittleEndian.PutUint16(frame.Data[22:24], uint16(left/60))
	frame.Data[24] = byte(left % 60)
	if running.consumer == 0 || running.consumer == 1 {
		binary.LittleEndian.PutUint16(frame.Data[34:36], b.configTable.CardAmountCents)
	}
	if err := b.queueReport(frame); err != nil {
		return err
	}
	b.config.Log.Printf("finished charge on port %d: %d mWh over %s", running.port, running.chargedMilliWh(), ended.Sub(running.startedAt).Truncate(time.Second))
	return nil
}

// remainingSecs 返回预计剩余秒数。
// 时长模式读取剩余时间；电量模式按剩余电量除以当前功率推算。
func (b *board) remainingSecs(running *charge) uint32 {
	if !energyBilled(running.mode) {
		if running.remaining <= 0 {
			return 0
		}
		return uint32(running.remaining / time.Second)
	}
	if b.power(running.port) == 0 {
		return 0
	}
	return uint32(uint64(running.remainingMilliWh()) * deciWattSecondsPerMilliWh / uint64(b.power(running.port)))
}

// advance 把每一笔进行中的充电向前推进一秒模拟时间。
func (b *board) advance() {
	now := b.now()
	var finished []*charge
	for port, c := range b.charging {
		power := b.power(port)
		c.wattDeciSeconds += uint64(power)
		if power > c.peak {
			c.peak = power
		}
		if !energyBilled(c.mode) {
			c.remaining -= time.Second
		}
		reason := byte(255)
		if b.physical(port).Fault != 0 {
			reason = 3
		} else if b.smoke {
			reason = 8
		} else if b.deviceStatus() == 1 {
			reason = 9
		}
		if !b.physical(port).Connected || power <= uint32(b.removePower) {
			c.removedSeconds++
		} else {
			c.removedSeconds = 0
		}
		if power > 0 && power <= uint32(b.configTable.FloatDeciWatts) {
			c.floatSeconds++
		} else {
			c.floatSeconds = 0
		}
		long := c.mode == dc589.LongTime || c.mode == dc589.LongEnergy || c.mode == dc589.LongPlatformBilling
		if !long && c.removedSeconds >= uint32(b.configTable.RemoveSeconds) {
			reason = 1
		}
		if !long && b.configTable.StopWhenFull == 1 && c.floatSeconds >= uint32(b.configTable.FloatSeconds) {
			reason = 2
		}
		if b.config.Scenario == ScenarioFault && !c.faultSent && now.Sub(c.startedAt) >= b.config.FaultAfter {
			c.faultSent = true
			_ = b.sendFault(port, 0x35)
			reason = 3
		}
		if reason == 255 && c.complete() && (energyBilled(c.mode) || c.notBeforeEnd.IsZero() || !now.Before(c.notBeforeEnd)) && b.config.Scenario != ScenarioStopOnCommand {
			reason = 0
			if energyBilled(c.mode) {
				reason = 10
			}
		}
		if reason != 255 {
			c.stopReason = reason
			finished = append(finished, c)
			delete(b.charging, port)
			b.completed[fmt.Sprintf("%d:%x", port, c.orderBCD)] = true
			continue
		}
		if err := b.updateBand(c); err != nil {
			b.config.Log.Printf("band report: %v", err)
		}
	}
	for _, c := range finished {
		if err := b.reportEnd(c, c.stopReason); err != nil {
			b.config.Log.Printf("charge end failed: %v", err)
		}
	}
	if len(finished) > 0 {
		b.chargingHeartbeat()
	}
}

func (b *board) chargingHeartbeat() {
	if b.heartbeat == nil {
		return
	}
	seconds := uint16(dc589.DefaultHeartbeatSeconds)
	if len(b.charging) > 0 {
		seconds = dc589.ChargingHeartbeatSeconds
	}
	b.setHeartbeat(seconds)
	b.sendHeartbeat()
}

// sendHeartbeat 发送设备状态；仅在平台启用端口遥测且存在充电时添加端口块。
func (b *board) sendHeartbeat() {
	if b.portTelemetry {
		if err := b.sendAllPorts(false, dc589.Heartbeat); err != nil {
			b.config.Log.Printf("heartbeat: %v", err)
		}
		return
	}
	frame, err := dc589.BuildHeartbeat(b.config.Identity, nil)
	if err == nil {
		err = b.writer.send(frame)
	}
	if err != nil {
		b.config.Log.Printf("heartbeat: %v", err)
	}
}

func (b *board) readFrame() (dc589.Frame, error) {
	_ = b.conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	if b.reader == nil {
		b.reader = bufio.NewReader(b.conn)
	}
	f, err := dc589.ReadFrame(b.reader)
	if err == nil {
		b.config.Log.Printf("RX %02X session=%x bytes=%d", f.Command, f.Session, len(f.Data))
	}
	return f, err
}

// frameWriter 串行化写操作。
// 心跳、命令回包和充电结束上报分别来自循环里的不同
// 位置，而两帧在链路上交插会让服务器的读取器错位。
type frameWriter struct {
	log     *log.Logger
	session [6]byte
	conn    net.Conn
	mu      sync.Mutex
}

func (w *frameWriter) send(frame dc589.Frame) error {
	if w.conn == nil {
		return fmt.Errorf("device is disconnected")
	}
	switch frame.Command {
	case dc589.StartReply, dc589.StopReply, dc589.HeartbeatSetReply, dc589.ConfigAck, dc589.ConfigReport, 0xB1, 0xB3, 0xE1:
		if w.session != ([6]byte{}) {
			frame.Session = w.session
		}
	}
	raw, err := dc589.Encode(frame)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err = w.conn.Write(raw)
	if w.log != nil {
		w.log.Printf("TX %02X session=%x bytes=%d result=%v", frame.Command, frame.Session, len(frame.Data), err)
	}
	return err
}
