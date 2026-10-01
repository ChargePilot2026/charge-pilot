package dc589

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"github.com/google/uuid"
)

// remoteAddrOf 取对端地址用于会话审计。保留 host 部分，IPv6 的 zone
// 或过长的端口后缀会裁到塞进列宽为止，因为一行记录不该因为纯外观上的
// 超长就被拒。
func remoteAddrOf(conn net.Conn) string {
	if conn == nil || conn.RemoteAddr() == nil {
		return ""
	}
	addr := conn.RemoteAddr().String()
	if len(addr) > 64 {
		addr = addr[:64]
	}
	return addr
}

// finish 写入终态的会话行。没有实现 SessionRecorder 的 sink 被放行而
// 不是失败：会话审计有价值，但为了它拒服务设备是笔亏本买卖。
func (a TCPAdapter) finish(ctx context.Context, sink protocol.Sink, audit *protocol.SessionAudit, deviceID string, reason protocol.CloseReason) {
	recorder, ok := sink.(protocol.SessionRecorder)
	if !ok {
		return
	}
	clock := a.Clock
	if clock == nil {
		clock = time.Now
	}
	_ = recorder.RecordSession(ctx, audit.Snapshot(deviceID, string(reason), clock()))
}

// TCPAdapter 是一个厂商监听器。其他 adapter 使用各自的端口并实现
// protocol.Adapter，不必改动本包。
type TCPAdapter struct {
	Clock          func() time.Time
	Registry       *protocol.Registry
	DebugHeartbeat bool
}

func (TCPAdapter) Name() string { return "dc589" }

func (a TCPAdapter) ServeConn(ctx context.Context, conn net.Conn, sink protocol.Sink) (serveErr error) {
	clock := a.Clock
	if clock == nil {
		clock = time.Now
	}
	// 会话由服务器挑定的 session 字节标识，也就是在注册应答里交给
	// 设备、设备此后每一帧都会回带的那串字节。拿它当审计主键，意味
	// 着重连会写出一行新记录，而不是覆盖掉上一次连接。
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	reader := bufio.NewReaderSize(conn, 512)
	if err := conn.SetReadDeadline(clock().Add(30 * time.Second)); err != nil {
		return err
	}
	first, err := ReadFrame(reader)
	if err != nil {
		return err
	}
	registration, err := ParseRegistration(first)
	if err != nil {
		return err
	}
	deviceID := registration.BoardID
	(&connection{deviceID: deviceID}).logFrame("RX", first, nil)
	if err := sink.Register(ctx, protocol.Registration{Protocol: a.Name(), DeviceID: deviceID, HardwareVersion: registration.HardwareVersion, SoftwareVersion: fmt.Sprintf("%s:%d", registration.SoftwareID, registration.SoftwareVersion), ReceivedAt: clock()}); err != nil {
		log.Printf("device registration rejected protocol=dc589 device=%s remote=%s error=%v", deviceID, remoteAddrOf(conn), err)
		return err
	}
	var serverSession [6]byte
	if _, err := rand.Read(serverSession[:]); err != nil {
		return err
	}
	audit := protocol.NewSessionAudit(protocol.TransportTCP, hex.EncodeToString(serverSession[:]), remoteAddrOf(conn), clock())
	// 较新的登录挤掉本连接时置位，好让审计行写「被替换」，而不是
	// 暗示设备自己挂断。
	var replaced atomic.Bool
	// 每条退出路径都把结束的连接落库。写入发生在协议交互之后，所以
	// 这里的数据库问题既不会污染交互，也不会盖掉真正结束连接的那个
	// 错误。
	reason := protocol.CloseDeviceClosed
	defer func() {
		if errors.Is(serveErr, os.ErrDeadlineExceeded) {
			reason = protocol.CloseReadTimeout
		} else if replaced.Load() {
			reason = protocol.CloseReplaced
		} else if serveErr != nil && !errors.Is(serveErr, io.EOF) {
			reason = protocol.CloseProtocol
		}
		if ctx.Err() != nil {
			reason = protocol.CloseContextEnded
		}
		a.finish(context.WithoutCancel(ctx), sink, audit, deviceID, reason)
		log.Printf("device offline protocol=dc589 device=%s session=%x remote=%s reason=%s error=%v", deviceID, serverSession, remoteAddrOf(conn), reason, serveErr)
	}()
	session := &connection{conn: conn, audit: audit, deviceID: deviceID, debugHeartbeat: a.DebugHeartbeat}
	if err := session.writeFrame(BuildRegisterReply(serverSession, clock())); err != nil {
		return err
	}
	// 在第一次心跳到达之前，先索要心跳周期和每端口遥测。
	//
	// 从 5.8.6 起，端口块只有平台主动要求时才出现在心跳里，所以从不
	// 发这一帧的网关等于在收下主板出厂时的配置。那会让「没有端口
	// 遥测」和「什么都没在充电」变成同一个观测，而前者几乎从来不是
	// 真相。它同时也把读死限定死：厂商的规定是漏掉三次心跳，而不是
	// 这个构建随便挑的某个常数。
	heartbeatSeconds := uint16(DefaultHeartbeatSeconds)
	if reader, ok := sink.(interface {
		HasChargingPorts(context.Context, string) (bool, error)
	}); ok {
		charging, err := reader.HasChargingPorts(ctx, deviceID)
		if err != nil {
			return err
		}
		if charging {
			heartbeatSeconds = ChargingHeartbeatSeconds
		}
	}
	interval, err := BuildHeartbeatInterval(serverSession, heartbeatSeconds, true)
	if err != nil {
		return err
	}
	if err := session.writeFrame(interval); err != nil {
		return err
	}
	session.heartbeatSeconds.Store(uint32(heartbeatSeconds))
	session.serverSession = serverSession
	// 读死跟刚申请到的周期走，按厂商漏掉三次心跳的规则算。写死成常数
	// 总有一边是错的：过长会让已经闭嘴的主板继续占着端口好几分钟；
	// 过短则会在一台老实执行比我们申请的更长周期的主板还在正常充电
	// 时把它踢掉。
	if a.Registry != nil {
		detach := a.Registry.Attach(deviceID, session, func(protocol.Session) { replaced.Store(true) })
		defer detach()
	}
	log.Printf("device online protocol=dc589 device=%s session=%x remote=%s hardware=%s software=%s:%d heartbeat_seconds=%d", deviceID, serverSession, remoteAddrOf(conn), registration.HardwareVersion, registration.SoftwareID, registration.SoftwareVersion, heartbeatSeconds)
	for {
		if err := conn.SetReadDeadline(clock().Add(time.Duration(session.heartbeatSeconds.Load()*MissedHeartbeatsBeforeReset) * time.Second)); err != nil {
			return err
		}
		frame, err := ReadFrame(reader)
		if err != nil {
			return err
		}
		audit.Inbound(len(frame.Data), clock())
		session.logFrame("RX", frame, nil)
		event := protocol.Event{Protocol: a.Name(), DeviceID: deviceID, ReceivedAt: clock(), RawPayload: frame.Data, SessionID: frame.Session}
		switch frame.Command {
		case OnlineCardSwipe:
			card, err := ParseCardSwipe(frame)
			if err != nil {
				return err
			}
			event.Type = protocol.CardSwipe
			event.Port = card.Port
			event.CardNumber = card.CardNumber
			event.EventID = uuid.NewString()
			if err := sink.Record(ctx, event); err != nil {
				return err
			}
		case CardBalanceQuery:
			card, err := ParseCardBalanceQuery(frame)
			if err != nil {
				return err
			}
			event.Type = protocol.CardBalanceQuery
			event.CardNumber = card
			event.EventID = uuid.NewString()
			if err := sink.Record(ctx, event); err != nil {
				return err
			}
		case 0xA8: // 设备对时请求；先记录再应答
			if len(frame.Data) != 6 {
				return ErrPayload
			}
			event.Type = protocol.TimeSync
			if err := sink.Record(ctx, event); err != nil {
				return err
			}
			if err := session.writeFrame(BuildTimeReply(frame.Session, clock())); err != nil {
				return err
			}
		case Heartbeat:
			heartbeat, err := ParseHeartbeat(frame)
			if err != nil || heartbeat.BoardID != deviceID {
				return ErrPayload
			}
			event.Type = protocol.Heartbeat
			event.Signal = heartbeat.Signal
			if heartbeat.HasPortStatus {
				event.DeviceStatus = heartbeat.DeviceStatus
				event.VoltageV = heartbeat.VoltageV
				event.TemperatureC = heartbeat.TemperatureC
				event.PortStates = heartbeat.PortStates
				event.ChargingPorts = heartbeat.ChargingPorts
			}
			if err := sink.Record(ctx, event); err != nil {
				return err
			}
			if err := session.writeFrame(BuildHeartbeatReply(frame.Session)); err != nil {
				return err
			}
			if heartbeat.HasPortStatus {
				charging := len(heartbeat.ChargingPorts) > 0
				for _, state := range heartbeat.PortStates {
					if state == 1 {
						charging = true
					}
				}
				seconds := uint32(DefaultHeartbeatSeconds)
				if charging {
					seconds = ChargingHeartbeatSeconds
				}
				if err := session.setHeartbeat(seconds); err != nil {
					return err
				}
			}
		case StartReply, StopReply:
			result, err := ParseCommandResult(frame)
			if err != nil {
				return err
			}
			event.Port, event.ResultCode = result.Port, result.Code
			log.Printf("device command result protocol=dc589 device=%s command=0x%02X session=%x port=%d result=0x%02X", deviceID, frame.Command, frame.Session, result.Port, result.Code)
			if frame.Command == StartReply {
				event.Type = protocol.StartResult
			} else {
				event.Type = protocol.StopResult
			}
			if err := sink.Record(ctx, event); err != nil {
				return err
			}
		case ChargeEnd:
			end, err := ParseChargeEnd(frame)
			if err != nil {
				return err
			}
			event.Type = protocol.ChargeEnd
			event.Port = end.Port
			event.OrderNumber = end.OrderNumber
			event.EnergyMilliKWh = end.ChargedMWh / 1000
			event.ChargedSeconds = end.ChargedSeconds
			event.StartedAt, event.EndedAt = end.StartedAt, end.EndedAt
			event.PowerDeciWatts = end.PowerDeciWatts
			event.StopReason = end.StopReason
			event.ConsumerType = end.ConsumerType
			log.Printf("device charge ended protocol=dc589 device=%s port=%d order=%s seconds=%d energy_mwh=%d stop_reason=0x%02X", deviceID, end.Port, end.OrderNumber, end.ChargedSeconds, end.ChargedMWh, end.StopReason)
			if err := sink.Record(ctx, event); err != nil {
				return err
			}
			if err := session.writeFrame(BuildChargeEndReply(frame.Session, event.Port)); err != nil {
				return err
			}
		case Fault:
			if len(frame.Data) != 5 {
				return ErrPayload
			}
			event.Type, event.Port, event.FaultCode = protocol.Fault, frame.Data[0], frame.Data[1]
			log.Printf("device fault protocol=dc589 device=%s port=%d code=0x%02X", deviceID, event.Port, event.FaultCode)
			if err := sink.Record(ctx, event); err != nil {
				return err
			}
			if err := session.writeFrame(BuildFaultReply(frame.Session, event.Port)); err != nil {
				return err
			}
		case RemoteControl + 1: // A3：远程控制结果；厂商可能不发
			if len(frame.Data) != 2 {
				return ErrPayload
			}
			event.Type, event.ResultCode = protocol.RemoteResult, frame.Data[1]
			if err := sink.Record(ctx, event); err != nil {
				return err
			}
		case 0xC2: // 本地分档上报；按协议无需确认
			meter, err := ParseChargingBand(frame)
			if err != nil {
				return err
			}
			event.Type = protocol.Telemetry
			event.Port = meter.Port
			event.PowerDeciWatts = meter.PowerDeciWatts
			event.ChargingPorts = []protocol.PortTelemetry{meter}
			if err := sink.Record(ctx, event); err != nil {
				return err
			}
		case HeartbeatSetReply, ConfigAck, ConfigReport, cmdPowerControlReply:
			// 主板对平台刚刚向它发出的某个请求的回答。
			//
			// 这些在健康链路上都是正常流量，而在被显式处理之前它们会落到
			// 下面的 default 分支里——那意味着一块拒绝了参数表的主板被
			// 记成「发了个我们不认识的命令」，原因就此消失。
			// 这跟停止路径当初在 0x00 和 0x04 上犯的是同一个错：把拒绝
			// 当成无事发生，就等于让这次拒绝谁也诊断不了。
			//
			// 解码失败不算致命。一块用这个构建不认识的形状作答的主板，
			// 仍然是一块还在说话的主板，为此把链路掐掉，会让它因为一个
			// 跟桩子坏了毫无关系的原因而失联。
			event.Type = protocol.ConfigResult
			event.Signal = frame.Command
			switch frame.Command {
			case HeartbeatSetReply:
				accepted, err := ParseHeartbeatSetReply(frame)
				if err == nil {
					if accepted {
						event.ResultCode = 0
					} else {
						// 主板保留了它原来的周期，本连接所用的读超时
						// 从此与链路不符。
						event.ResultCode = 1
					}
				}
			case ConfigAck:
				if err := ParseConfigAck(frame); err == nil {
					event.ResultCode = 0
				} else {
					var rejected ErrConfigRejected
					if errors.As(err, &rejected) {
						// 错误码指明是哪个字段越界，这正是「运维照着
						// 改一个值」和「运维靠猜」之间的区别。
						event.ResultCode = rejected.Code
					} else {
						event.ResultCode = 0xFF
					}
				}
			case ConfigReport:
				// 主板在告诉我们它实际在跑什么。这里只解码到能分辨
				// 「这份上报读得懂」与「读不懂」为止；字节保持原样，
				// 因为 RawPayload 就是重放记录，把它换成重新编码的
				// 结果，意味着你重放的东西已经不是主板发出来的那份
				// 了。
				if _, err := DecodeConfig(frame); err != nil {
					event.ResultCode = 0xFF
				}
			default: // 即 cmdPowerControlReply
				if _, err := ParsePowerControlReply(frame); err != nil {
					if errors.Is(err, ErrPowerControlRejected) {
						event.ResultCode = 1
					} else {
						event.ResultCode = 0xFF
					}
				}
			}
			if err := sink.Record(ctx, event); err != nil {
				return err
			}
		default:
			// 不认识的命令被记录后跳过，不致命。
			//
			// 厂商文档把若干命令标为可选：平台参数请求「部分主板不发」，
			// 分档上报「部分主板不发」，远程控制的应答对重启和升级
			// 「不上报」。跑着老固件的主板照样会发它们。碰上一个就断
			// 连接，意味着一个完好的充电桩仅仅因为这个构建没有实现
			// 一个它并不需要的命令而失联——而且第一个这样的命令，恰恰
			// 就是让人没法诊断的那一个。
			audit.Unknown(frame.Command)
			log.Printf("device unknown command protocol=dc589 device=%s command=0x%02X session=%x", deviceID, frame.Command, frame.Session)
		}
	}
}

type connection struct {
	conn             net.Conn
	mu               sync.Mutex
	audit            *protocol.SessionAudit
	deviceID         string
	debugHeartbeat   bool
	logger           *log.Logger
	heartbeatSeconds atomic.Uint32
	serverSession    [6]byte
	heartbeatMu      sync.Mutex
}

func (c *connection) setHeartbeat(seconds uint32) error {
	c.heartbeatMu.Lock()
	defer c.heartbeatMu.Unlock()
	if c.heartbeatSeconds.Load() == seconds {
		return nil
	}
	f, err := BuildHeartbeatInterval(c.serverSession, uint16(seconds), true)
	if err != nil {
		return err
	}
	if err := c.writeFrame(f); err != nil {
		return err
	}
	c.heartbeatSeconds.Store(seconds)
	return c.conn.SetReadDeadline(time.Now().Add(time.Duration(seconds*MissedHeartbeatsBeforeReset) * time.Second))
}

// Never log raw payloads: card numbers, balances and configuration secrets may be present.
func (c *connection) logFrame(direction string, frame Frame, err error) {
	if (frame.Command == Heartbeat || frame.Command == HeartbeatReply) && !c.debugHeartbeat && err == nil {
		return
	}
	logger := c.logger
	if logger == nil {
		logger = log.Default()
	}
	data, parseErr := parsedLogData(frame)
	if parseErr != nil {
		data = map[string]any{"decode_status": "invalid_payload", "decode_error": parseErr.Error()}
	}
	parsed, _ := json.Marshal(data)
	logger.Printf("device frame protocol=dc589 device=%s direction=%s command=0x%02X name=%s session=%x bytes=%d data=%s error=%v", c.deviceID, direction, frame.Command, codeName(frame.Command, commandNames), frame.Session, len(frame.Data), parsed, err)
}

func (c *connection) Close() error { return c.conn.Close() }

func (c *connection) Send(ctx context.Context, command protocol.Command) (sendErr error) {
	defer func() {
		log.Printf("device command sent protocol=dc589 device=%s kind=%v port=%d session=%x error=%v", c.deviceID, command.Kind, command.Port, command.SessionID, sendErr)
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if command.SessionID == ([6]byte{}) && command.Kind != protocol.CommandCardDenied && command.Kind != protocol.CommandCardBalance {
		return ErrPayload
	}
	var frame Frame
	var err error
	switch command.Kind {
	case protocol.CommandCardDenied:
		frame, err = BuildCardDenied(command.SessionID, command.CardNumber, command.CardInvalid, command.CardBalanceUnits)
	case protocol.CommandCardBalance:
		frame, err = BuildCardBalanceReply(command.SessionID, command.CardNumber, !command.CardInvalid, command.CardBalanceUnits)
	case protocol.CommandStart:
		frame, err = BuildStart(StartCommand{Session: command.SessionID, Port: command.Port, OrderBCD: command.OrderBCD, Mode: ChargeMode(command.Mode), Quantity: command.Quantity, ConsumerType: command.ConsumerType, CardNumber: command.CardNumber, CardBalanceUnits: command.CardBalanceUnits})
	case protocol.CommandStop:
		frame, err = BuildStop(command.SessionID, command.Port)
	case protocol.CommandReboot:
		frame, err = BuildRemoteControl(command.SessionID, 1, false, [8]byte{})
	case protocol.CommandOTA:
		frame, err = BuildRemoteControl(command.SessionID, command.RemoteMode, command.UseUpgradeID, command.UpgradeID)
	default:
		return ErrPayload
	}
	if err != nil {
		return err
	}
	if err := c.writeFrame(frame); err != nil {
		return err
	}
	if command.Kind == protocol.CommandStart {
		return c.setHeartbeat(ChargingHeartbeatSeconds)
	}
	return nil
}

func (c *connection) writeFrame(frame Frame) (writeErr error) {
	defer func() { c.logFrame("TX", frame, writeErr) }()
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, err := Encode(frame)
	if err != nil {
		return err
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	for len(raw) > 0 {
		n, err := c.conn.Write(raw)
		if err != nil {
			c.audit.Outbound(len(frame.Data), time.Now())
			return err
		}
		if n == 0 {
			c.audit.Outbound(len(frame.Data), time.Now())
			return errors.New("zero-byte TCP write")
		}
		raw = raw[n:]
	}
	c.audit.Outbound(len(frame.Data), time.Now())
	return nil
}
