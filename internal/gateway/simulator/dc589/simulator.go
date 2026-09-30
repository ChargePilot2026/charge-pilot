// Package dc589sim 在本地顶替一块 dc589 充电板。
//
// 它通过 TCP 讲真实的 5.8.9 线路协议，
// 因此走的是与网关面对真板时完全相同的组帧、载荷校验和会话处理。
// 它不是网关的 mock：
// 它是同一份契约设备端的第二套实现。
//
// 之所以要它，是因为没有厂商硬件就无法把充电链路从头走到尾。
// 它是开发与测试工具，
// 绝不能接进任何生产进程。
//
// 模拟器按协议划分：
// 换一家厂商、或者换成 MQTT 板，应当在本网包旁
// 边另起一个包，而不是给共享实现加一个开关参数，
// 因为它们能表达的行为并不相同。
// 本包命名为 dc589sim 而不是 dc589，就是为了还能 import 它所实现的协议编解码器。
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

// Scenario 选定被测的行为。
// 每个场景共用同一套注册与心跳路径，只在被要求充电之后板子做什么上不同，
// 这样一次失败就能指向某一个具体行为。
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

// Run 建立连接并服务到 context 被取消
// 为止：reconnect 场景会自己重建连接，
// 其余场景在连接结束时返回。
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

	// 靠关连接来打断阻塞读。
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

// deciWattSecondsPerMilliWh 把板子电量累加器的单位换算成
// 毫瓦时：1 毫瓦时等于 3.6 瓦秒，而累加器计的是 0.1 瓦秒，
// 所以 1 毫瓦时是它的 36 个单位。
//
// 用 0.1W·s 累加、只有在数值要上
// 线时才换算，是计量精确的前提。
// 另一条路——每 tick 固定存进一个毫瓦时数——无法和板子上报的功率对账，
// 因为两者会变成互不相干的常数：
// 板子声称 150W 却每小时存 3.6kWh，就会多送 24
// 倍，而结算单里的每个数字看上去都仍然合理。
const deciWattSecondsPerMilliWh = 36

// charge 是一笔正在进行的充电的状态。
type charge struct {
	port      byte
	orderBCD  [8]byte
	mode      dc589.ChargeMode
	startedAt time.Time
	// wattDeciSeconds 是已送出的电量，单位 0.1 瓦秒。
	wattDeciSeconds uint64
	// targetMilliWh 是这笔订单买下的电量，
	// 单位毫瓦时。只有按电量计费的充电会读
	// 它；按时间计费的把上限放在 remaining 里。
	targetMilliWh uint32
	// remaining 是按时间计费的充电还剩多久。
	remaining  time.Duration
	stopReason byte
	faultSent  bool
}

// energyBilled 报告这笔订单的数量是电量而不是时长。
//
// 线路对两者只有同一个无符号字段，
// 唯一能说明它是哪一个的只有充电
// 模式。把按电量下的单当成时长去
// 读，就会让一笔 1Wh 的订单变成一次一
// 秒的充电，还上报数倍于卖出量的电。
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

// complete 报告这笔充电是否已送出订单买下的全部
// 量。按电量下的单看电表，按时间下的单看时钟，
// 因为那才是用户真正买的东西。
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
	// clockOffset 是板子自身时钟落后服务器多少：
	// 注册回包时取初值，之后每个 A9 刷新。
	// 结算用的是板子自己打的时间戳，
	// 所以未经校正的时钟不只是日志里看着别
	// 扭——它会把整个会话放到错误的时间里，而后续读数都是拿这些时间去比的。
	clockOffset time.Duration
	// heartbeat 在平台每次下发新周期时重
	// 建，portTelemetry 记录是否带上端口数据。
	// 自 5.8.6 起这两项都由服务器决定，
	// 所以板子把它们记下来，而不是假定自己的默认值还算数。
	heartbeat     *time.Ticker
	portTelemetry bool
	configTable   dc589.ConfigTable
}

// factoryTable 是板子出厂自带的参数表。
//
// 之所以要有它，是因为零值不是一张合法的表。
// 固件拒绝 50-100 之外的温度保护值（只有 0xFF 一个出口），
// 同样限制了浮充电流和拔枪定时器，
// 所以一块从未被配置过的板子会在自己的校验里失败，对一次读操作回「拒绝」
// ——平台会以为自己的费率没下发成功，
// 实际上压根没人发过。
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

// now 是板子认为的当前时间，
// 用协议承载的那个民用时区表示。encodeTime 会写入交给它的那个时区的日历字段，
// 所以板子若按宿主本地时区给事件打时间戳，
// 服务器读回来就会变成另一个瞬间。
func (b *board) now() time.Time {
	b.mu.Lock()
	offset := b.clockOffset
	b.mu.Unlock()
	return dc589.Civil(time.Now().Add(offset))
}

// setClock 采用服务器的时间。
// 校正量以偏移量而非新的时间基准保存，这样板子的时钟仍按宿主的真实速率前进，
// 第二次对时量到的是漂移，而不是把漂移重新推一遍。
func (b *board) setClock(server time.Time) {
	offset := server.Sub(time.Now())
	b.mu.Lock()
	previous := b.clockOffset
	b.clockOffset = offset
	b.mu.Unlock()
	b.config.Log.Printf("server time %s adopted, clock moved %s", server.Format(time.RFC3339), (offset - previous).Truncate(time.Second))
}

// register 完成 A0/A1 交换并采用服务器下发的会话字节。
// 之后每一帧都带着它们，
// 服务器就是靠这个把帧关联到这条连接的。
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
	// 注册回包本身已经带着服务器时间，
	// 所以在发出任何别的东西之前板子就已校准。在这里就读掉它而不是等 A9，
	// 意味着即便服务器从不回答对时请求，也不会让板
	// 子用未经校正的时钟去给结算打时间戳。
	b.clockOffset = at.Sub(time.Now())
	b.mu.Unlock()
	b.config.Log.Printf("registered at %s with server time %s", b.config.Gateway, at.Format(time.RFC3339))
	// 另外再显式索要一次时间。
	// 服务器会用 A9 回答 A8 并记一条对时事件，而这条
	// 事件是时钟校正在会话审计里唯一显形的地方——没有它，
	// 一块登录后发生漂移的板子会被悄悄地校正掉，永远查不到。
	if err := b.writer.send(dc589.BuildTimeRequest()); err != nil {
		return fmt.Errorf("send time request: %w", err)
	}
	return nil
}

// loop 把板子同时要做的三件事复合成一路：应答服务器命令、
// 发心跳、让充电跑到自己的终点。
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
	// 用闭包，
	// 因为平台每次下发新周期都会替换这个字段：
	// 现在求值会在返回时停掉原来那个 ticker，而真正在用的那个还在跑。
	defer func() { b.heartbeat.Stop() }()
	// 计量按自己的节奏推进，这样即便服务器不催，
	// 充电在两次心跳之间也在走。
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

// handle 应答一帧服务器下行。
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
		// A9 是服务器对本板注册时发出的 A8 的应答。
		// 这里当初处理的是 A8，两头都不成立：
		// 网关从不下发 A8（A8 是板子到服务
		// 器方向），而它确实下发的 A9 则掉进 default 分支被无声丢弃。
		// 于是即便服务器每次连接都主动提供校正，
		// 校正也从未发生。
		server, err := dc589.ParseTimeReply(frame)
		if err != nil {
			return fmt.Errorf("parse time reply: %w", err)
		}
		b.setClock(server)
		return nil
	case dc589.HeartbeatInterval:
		// 自 5.8.6 起，
		// 这条命令还决定每次心跳里是否带端口遥测。
		// 忽略它的板子会一直按自己的出厂默认值上报，而这正是这条命令要消除的歧义，
		// 所以模拟器遵守它，
		// 并按收到的值而不是自己的默认值去发心跳。
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
			// 板子编不出来的表要回「
			// 写入被拒」而不是丢掉，
			// 这样平台不会一直等一个永远不会到达的读响应。
			return b.writer.send(dc589.BuildConfigAck(1))
		}
		return b.writer.send(report)
	case dc589.RegisterReply, dc589.HeartbeatReply, dc589.ChargeEndReply, dc589.FaultReply:
		// 这些是对本板已发出帧的确认。
		// 回应确认会形成回路，
		// 所以按协议沉默才是正确读法，而不是 switch 的疏漏。
		return nil
	default:
		// 其余都是本版本未实现的下行，而吞掉
		// 它恰恰是唯一让问题无法诊断的响应：
		// 命令无影无踪，
		// 两边都没有记录，
		// 于是「模拟器没实现」和「网关没发」从外面看一模一样。
		// 打出这个字节就是全部差别——这里冒出的远程控制或功率控制，
		// 意味着产品为一条模拟器尚未应答的命令新增了调用方。
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
}

// applyConfig 像真板子那样接收参数表：
// 校验各区间，被拒的写入保留原来那张表。
// 一个什么都收的模拟器会让 0xC4 的错误路径无从测
// 试，而那是平台得知费率没有送达设备的唯一途径。
func (b *board) applyConfig(frame dc589.Frame) error {
	table, err := dc589.DecodeConfig(frame)
	if err != nil {
		return b.writer.send(dc589.BuildConfigAck(1))
	}
	b.configTable = table
	return b.writer.send(dc589.BuildConfigAck(0))
}

// start 开始或拒绝一次充电。
// 拒绝会被服务器转成退款，所以要用非零的结果码回应。
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
	// 数量在按时间下单时是分钟、在按电量下单时是瓦时，
	// 所以一个字段同时承载两者，只有模式能说
	// 明它到底是哪个。把电量数字当成秒数
	// 读，会让一笔 1Wh 的订单一秒就结束，
	// 却仍按好几瓦时上报。
	if energyBilled(command.Mode) {
		running.targetMilliWh = uint32(command.Quantity) * 1000
	} else {
		running.remaining = time.Duration(command.Quantity) * time.Minute
	}
	if b.config.Scenario == ScenarioStopOnCommand || b.config.Scenario == ScenarioFault {
		// 这两个场景靠命令或故障结束而不是自己跑完，
		// 所以上限只要远到不可能成为结束原因即可。
		// 但两种模式下它仍必须是个真实数值：
		// 心跳会上报预计剩余时间，而回「剩余为
		// 零」的充电在平台看来就是即将自行结束。
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

// stop 结束一次充电并用 BB 上报，
// 这才让订单收尾。对没在充电的端口下发的停止同样要确认：
// 它是一条合法的迟到命令，不构成断连接的理由。
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

// remainingSecs 推算这次充电还能跑多久，单位秒。
//
// 心跳的 remaining 字段在两种计费模式下问的是同一个问题，
// 所以按电量计费的充电要用「还欠的电量 ÷ 实际取的功率」来推算。
// 那种情况下回零，
// 等于告诉平台一次刚开始的充电马上就要结束了。
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

// advance 把每一笔进行中的充电向前推进一秒模拟时间。
func (b *board) advance() {
	b.mu.Lock()
	var finished []*charge
	for port, running := range b.charging {
		// 一个 tick 就是一秒，
		// 所以板子在这一秒里送出平台配置的功率。
		// 关键在于电量由它上报的同一个数字推导出来：
		// 一份和功率读数差一个固定比例的结算没人会发现，
		// 因为电量仍落在运维会认为合理的区间里。
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
			// 这两个场景靠命令或故障结束，而不是自己跑完。
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

// sendHeartbeat 上报板子信息；
// 平台要求了端口数据且确有充电时，再带上每个充电端口的状态。
//
// 端口块是否出现自 5.8.6 起由平台决定，在 A6 里下发。
// 无条件上报会让「本版本忽略了指令」与「本版本没问题」
// 变得无法区分，而这正是该命令要
// 消除的歧义——还会导致一个明确关
// 掉了端口遥测的平台照样收到它。
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

// frameWriter 串行化写操作。
// 心跳、命令回包和充电结束上报分别来自循环里的不同
// 位置，而两帧在链路上交插会让服务器的读取器错位。
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
