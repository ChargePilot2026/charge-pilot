package dc589

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
)

// remoteAddrOf captures the peer address for the session audit. The host part
// is kept, and an IPv6 zone or a long port suffix is trimmed to what fits the
// column, because a row must never be rejected over a cosmetic overflow.
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

// finish writes the terminal session row. A sink that does not implement
// SessionRecorder is left alone rather than failed: session auditing is
// valuable, but refusing to serve devices over it would be the wrong trade.
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

// TCPAdapter is one vendor listener. Other adapters use separate ports and
// implement protocol.Adapter without changing this package.
type TCPAdapter struct {
	Clock    func() time.Time
	Registry *protocol.Registry
}

func (TCPAdapter) Name() string { return "dc589" }

func (a TCPAdapter) ServeConn(ctx context.Context, conn net.Conn, sink protocol.Sink) (serveErr error) {
	clock := a.Clock
	if clock == nil {
		clock = time.Now
	}
	// A session is identified by the server-chosen session bytes we hand the
	// device in the register reply, which is what the device echoes back in
	// every later frame. Using it as the audit key means a reconnect produces a
	// distinct row rather than overwriting the previous connection.
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
	if err := sink.Register(ctx, protocol.Registration{Protocol: a.Name(), DeviceID: deviceID, HardwareVersion: registration.HardwareVersion, SoftwareVersion: fmt.Sprintf("%s:%d", registration.SoftwareID, registration.SoftwareVersion), ReceivedAt: clock()}); err != nil {
		return err
	}
	var serverSession [6]byte
	if _, err := rand.Read(serverSession[:]); err != nil {
		return err
	}
	audit := protocol.NewSessionAudit(protocol.TransportTCP, hex.EncodeToString(serverSession[:]), remoteAddrOf(conn), clock())
	// Set when a newer login displaces this connection, so the audit row says
	// "replaced" rather than implying the device hung up on its own.
	var replaced atomic.Bool
	// Persist the finished connection on every exit path. The write happens after
	// the protocol exchange is over, so a database problem here cannot corrupt
	// the exchange or mask the real error that ended the connection.
	reason := protocol.CloseDeviceClosed
	defer func() {
		if errors.Is(serveErr, os.ErrDeadlineExceeded) {
			reason = protocol.CloseReadTimeout
		} else if serveErr != nil {
			reason = protocol.CloseProtocol
		} else if replaced.Load() {
			reason = protocol.CloseReplaced
		}
		if ctx.Err() != nil {
			reason = protocol.CloseContextEnded
		}
		a.finish(context.WithoutCancel(ctx), sink, audit, deviceID, reason)
	}()
	session := &connection{conn: conn, audit: audit}
	if err := session.writeFrame(BuildRegisterReply(serverSession, clock())); err != nil {
		return err
	}
	// Ask for the heartbeat period and for per-port telemetry before the first
	// heartbeat arrives.
	//
	// Since 5.8.6 the port block is only present in a heartbeat when the
	// platform asked for it, so a gateway that never sends this is accepting
	// whatever the board shipped with. That makes "no port telemetry" and
	// "nothing is charging" the same observation, and the first is almost never
	// the truth. It also fixes the read deadline: the vendor's rule is three
	// missed heartbeats, not any constant this build could pick.
	interval, err := BuildHeartbeatInterval(serverSession, DefaultHeartbeatSeconds, true)
	if err != nil {
		return err
	}
	if err := session.writeFrame(interval); err != nil {
		return err
	}
	// The read deadline follows the period just asked for, at the vendor's rule
	// of three missed heartbeats. A constant would be wrong in one direction or
	// the other: too long and a board that has stopped talking keeps its ports
	// open for minutes, too short and a board honouring a longer period than we
	// asked for is dropped mid-session.
	readTimeout := time.Duration(DefaultHeartbeatSeconds*3) * time.Second
	if a.Registry != nil {
		detach := a.Registry.Attach(deviceID, session, func(protocol.Session) { replaced.Store(true) })
		defer detach()
	}
	for {
		if err := conn.SetReadDeadline(clock().Add(readTimeout)); err != nil {
			return err
		}
		frame, err := ReadFrame(reader)
		if err != nil {
			return err
		}
		audit.Inbound(len(frame.Data), clock())
		event := protocol.Event{Protocol: a.Name(), DeviceID: deviceID, ReceivedAt: clock(), RawPayload: frame.Data, SessionID: frame.Session}
		switch frame.Command {
		case 0xA8: // device time request; record before responding
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
		case StartReply, StopReply:
			result, err := ParseCommandResult(frame)
			if err != nil {
				return err
			}
			event.Port, event.ResultCode = result.Port, result.Code
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
			if err := sink.Record(ctx, event); err != nil {
				return err
			}
			if err := session.writeFrame(BuildFaultReply(frame.Session, event.Port)); err != nil {
				return err
			}
		case RemoteControl + 1: // A3: remote-control result; vendor may not send it
			if len(frame.Data) != 2 {
				return ErrPayload
			}
			event.Type, event.ResultCode = protocol.RemoteResult, frame.Data[1]
			if err := sink.Record(ctx, event); err != nil {
				return err
			}
		case 0xC2: // local charging-band report; no acknowledgement per protocol
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
		default:
			// An unrecognised command is recorded and skipped, not fatal.
			//
			// The vendor document marks several commands as optional: the
			// platform-parameter request is "not sent by some boards", the
			// charging-band report is "not sent by some boards", and the remote
			// control reply "is not reported" for reset and upgrade. Boards
			// running older firmware still send them. Dropping the connection on
			// one would mean a perfectly healthy pile is unreachable purely
			// because this build has not implemented a command it does not need
			// — and the first such command would be the one that keeps it from
			// being diagnosed.
			audit.Unknown(frame.Command)
		}
	}
}

type connection struct {
	conn  net.Conn
	mu    sync.Mutex
	audit *protocol.SessionAudit
}

func (c *connection) Close() error { return c.conn.Close() }

func (c *connection) Send(ctx context.Context, command protocol.Command) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if command.SessionID == ([6]byte{}) {
		return ErrPayload
	}
	var frame Frame
	var err error
	switch command.Kind {
	case protocol.CommandStart:
		frame, err = BuildStart(StartCommand{Session: command.SessionID, Port: command.Port, OrderBCD: command.OrderBCD, Mode: ChargeMode(command.Mode), Quantity: command.Quantity})
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
	return c.writeFrame(frame)
}

func (c *connection) writeFrame(frame Frame) error {
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
