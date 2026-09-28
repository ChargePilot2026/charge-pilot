package dc589

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	Register       byte = 0xA0
	RegisterReply  byte = 0xA1
	RemoteControl  byte = 0xA2
	Heartbeat      byte = 0xA4
	HeartbeatReply byte = 0xA5
	StartCharge    byte = 0xB7
	StartReply     byte = 0xB8
	StopCharge     byte = 0xB9
	StopReply      byte = 0xBA
	ChargeEnd      byte = 0xBB
	ChargeEndReply byte = 0xBC
	Fault          byte = 0xC0
	FaultReply     byte = 0xC1
)

var ErrPayload = errors.New("invalid 5.8.9 payload")

type Registration struct {
	BoardID         string
	HardwareVersion string
	SoftwareID      string
	SoftwareVersion uint16
	ModuleID        string
	SIM             string
	Signal          byte
}

func ParseRegistration(frame Frame) (Registration, error) {
	if frame.Command != Register || len(frame.Data) != 45 {
		return Registration{}, ErrPayload
	}
	board, err := decodeBCD(frame.Data[:8])
	if err != nil {
		return Registration{}, err
	}
	module := decodeIdentifier(frame.Data[26:34])
	sim := decodeIdentifier(frame.Data[34:44])
	return Registration{
		BoardID:         board,
		HardwareVersion: string(frame.Data[8:16]),
		SoftwareID:      string(frame.Data[16:24]),
		SoftwareVersion: binary.LittleEndian.Uint16(frame.Data[24:26]),
		ModuleID:        module,
		SIM:             sim,
		Signal:          frame.Data[44],
	}, nil
}

// The document calls module and SIM values BCD, but its own SIM example has a
// C nibble. Preserve such identifiers as uppercase hex instead of rejecting
// the device during registration.
func decodeIdentifier(data []byte) string {
	if result, err := decodeBCD(data); err == nil {
		return result
	}
	return strings.ToUpper(hex.EncodeToString(data))
}

type ChargeMode byte

const (
	ByTime              ChargeMode = 0
	ByEnergy            ChargeMode = 1
	PlatformBilling     ChargeMode = 4
	LongTime            ChargeMode = 10
	LongEnergy          ChargeMode = 11
	LongPlatformBilling ChargeMode = 12
)

type StartCommand struct {
	Session  [6]byte
	Port     byte
	OrderBCD [8]byte
	Mode     ChargeMode
	Quantity uint16 // minutes or 0.001 kWh, depending on Mode
}

// BuildStart uses the documented scan consumer type (2); reserved card fields
// are zero. The business layer must authorize payment before calling it.
func BuildStart(command StartCommand) (Frame, error) {
	if command.Port == 0 || command.Mode != ByTime && command.Mode != ByEnergy && command.Mode != PlatformBilling && command.Mode != LongTime && command.Mode != LongEnergy && command.Mode != LongPlatformBilling || command.Quantity == 0 {
		return Frame{}, ErrPayload
	}
	data := make([]byte, 19)
	data[0] = command.Port
	copy(data[1:9], command.OrderBCD[:])
	data[9] = byte(command.Mode)
	data[10] = 2
	binary.LittleEndian.PutUint16(data[11:13], command.Quantity)
	return Frame{Command: StartCharge, Session: command.Session, Data: data}, nil
}

func BuildStop(session [6]byte, port byte) (Frame, error) {
	if port == 0 {
		return Frame{}, ErrPayload
	}
	return Frame{Command: StopCharge, Session: session, Data: []byte{port}}, nil
}

func BuildRemoteControl(session [6]byte, controlType byte, useUpgradeID bool, upgradeID [8]byte) (Frame, error) {
	if controlType != 1 && controlType != 2 && controlType != 3 {
		return Frame{}, ErrPayload
	}
	data := make([]byte, 10)
	data[0] = controlType
	if controlType != 1 && useUpgradeID {
		data[1] = 1
		copy(data[2:], upgradeID[:])
	}
	return Frame{Command: RemoteControl, Session: session, Data: data}, nil
}

type CommandResult struct {
	Code byte
	Port byte
}

func ParseCommandResult(frame Frame) (CommandResult, error) {
	if frame.Command != StartReply && frame.Command != StopReply || len(frame.Data) != 2 {
		return CommandResult{}, ErrPayload
	}
	return CommandResult{Code: frame.Data[0], Port: frame.Data[1]}, nil
}

func BuildRegisterReply(session [6]byte, now time.Time) Frame {
	data := make([]byte, 7)
	data[0] = 0 // connected
	encodeTime(data[1:], now)
	return Frame{Command: RegisterReply, Session: session, Data: data}
}

func BuildHeartbeatReply(session [6]byte) Frame {
	return Frame{Command: HeartbeatReply, Session: session, Data: []byte{1}}
}

func BuildTimeReply(session [6]byte, now time.Time) Frame {
	data := make([]byte, 6)
	encodeTime(data, now.In(chinaLocation))
	return Frame{Command: 0xA9, Session: session, Data: data}
}

func BuildChargeEndReply(session [6]byte, port byte) Frame {
	return Frame{Command: ChargeEndReply, Session: session, Data: []byte{0, port}}
}

func BuildFaultReply(session [6]byte, port byte) Frame {
	return Frame{Command: FaultReply, Session: session, Data: []byte{0, port}}
}

func encodeTime(dst []byte, value time.Time) {
	values := [6]int{value.Year() % 100, int(value.Month()), value.Day(), value.Hour(), value.Minute(), value.Second()}
	for i, n := range values {
		dst[i] = byte(n/10<<4 | n%10)
	}
}

func decodeBCD(data []byte) (string, error) {
	out := make([]byte, len(data)*2)
	for i, b := range data {
		hi, lo := b>>4, b&0x0f
		if hi > 9 || lo > 9 {
			return "", fmt.Errorf("%w: non-decimal BCD", ErrPayload)
		}
		out[2*i], out[2*i+1] = '0'+hi, '0'+lo
	}
	return string(out), nil
}
