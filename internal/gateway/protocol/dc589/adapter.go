package dc589

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
)

// TCPAdapter is one vendor listener. Other adapters use separate ports and
// implement protocol.Adapter without changing this package.
type TCPAdapter struct {
	Clock    func() time.Time
	Registry *protocol.Registry
}

func (TCPAdapter) Name() string { return "dc589" }

func (a TCPAdapter) ServeConn(ctx context.Context, conn net.Conn, sink protocol.Sink) error {
	clock := a.Clock
	if clock == nil {
		clock = time.Now
	}
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
	session := &connection{conn: conn}
	if err := session.writeFrame(BuildRegisterReply(serverSession, clock())); err != nil {
		return err
	}
	if a.Registry != nil {
		detach := a.Registry.Attach(deviceID, session)
		defer detach()
	}
	for {
		if err := conn.SetReadDeadline(clock().Add(90 * time.Second)); err != nil {
			return err
		}
		frame, err := ReadFrame(reader)
		if err != nil {
			return err
		}
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
			return fmt.Errorf("%w: unsupported command 0x%02x", ErrPayload, frame.Command)
		}
	}
}

type connection struct {
	conn net.Conn
	mu   sync.Mutex
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
			return err
		}
		if n == 0 {
			return errors.New("zero-byte TCP write")
		}
		raw = raw[n:]
	}
	return nil
}
