package dc589

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
)

// TCPAdapter is one vendor listener. Other adapters use separate ports and
// implement protocol.Adapter without changing this package.
type TCPAdapter struct {
	Clock func() time.Time
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
	if err := writeFrame(conn, BuildRegisterReply(serverSession, clock())); err != nil {
		return err
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
		case Heartbeat:
			if len(frame.Data) < 17 {
				return ErrPayload
			}
			board, err := decodeBCD(frame.Data[:8])
			if err != nil || board != deviceID {
				return ErrPayload
			}
			event.Type = protocol.Heartbeat
			if err := sink.Record(ctx, event); err != nil {
				return err
			}
			if err := writeFrame(conn, BuildHeartbeatReply(frame.Session)); err != nil {
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
			if len(frame.Data) != 44 {
				return ErrPayload
			}
			event.Type = protocol.ChargeEnd
			event.Port = frame.Data[1]
			event.EnergyMilliKWh = uint32(binary.LittleEndian.Uint16(frame.Data[32:34]))
			event.PowerDeciWatts = uint32(binary.LittleEndian.Uint16(frame.Data[42:44]))
			event.StopReason = frame.Data[40]
			if err := sink.Record(ctx, event); err != nil {
				return err
			}
			if err := writeFrame(conn, BuildChargeEndReply(frame.Session, event.Port)); err != nil {
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
			if err := writeFrame(conn, BuildFaultReply(frame.Session, event.Port)); err != nil {
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
			event.Type = protocol.Telemetry
			if err := sink.Record(ctx, event); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: unsupported command 0x%02x", ErrPayload, frame.Command)
		}
	}
}

func writeFrame(conn net.Conn, frame Frame) error {
	raw, err := Encode(frame)
	if err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	for len(raw) > 0 {
		n, err := conn.Write(raw)
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
