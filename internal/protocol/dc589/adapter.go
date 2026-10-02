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

	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/google/uuid"
)

// remoteAddrOf 提取审计用的对端地址，优先保留 host，并按数据库列宽截断。
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

// finish 保存会话终态；未实现 SessionRecorder 的 sink 跳过审计，不阻止设备通信。
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
	// 会话标识由服务器生成并通过注册响应下发，用于连接审计；每次重连生成独立记录。
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
	// 新连接替换本连接时标记 replaced，区分替换与设备主动断开。
	var replaced atomic.Bool
	// 连接退出后记录结束原因；审计存储错误仅记录日志，不覆盖原通信错误。
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
	// 注册后下发心跳周期并启用端口遥测，避免依赖设备出厂配置。
	// 协议自 5.8.6 起按平台要求携带端口状态；读超时使用所申请周期的三倍。
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
	// 读取超时采用当前申请的心跳周期乘以 3，对应厂商连续漏报三次心跳的判定。
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
			// 处理主板对平台请求的应答，并记录参数表拒绝原因。
			// 应答解码失败只记录错误，保留连接以继续接收心跳和其他帧。
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
					if rejected, ok := errors.AsType[ErrConfigRejected](err); ok {
						// 保留参数拒绝码，供运维定位越界字段。
						event.ResultCode = rejected.Code
					} else {
						event.ResultCode = 0xFF
					}
				}
			case ConfigReport:
				// 解析配置上报并记录是否有效；RawPayload 保留接收到的原始字节，供诊断和重放。
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
			// 未实现的命令记录后跳过，保留连接以兼容固件的可选命令。
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
		frame = BuildReboot(command.SessionID)
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
