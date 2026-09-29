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
	RemoteResult   byte = 0xA3
	Heartbeat      byte = 0xA4
	HeartbeatReply byte = 0xA5
	// HeartbeatInterval sets the heartbeat period and, since 5.8.6, whether the
	// board includes per-port telemetry in each heartbeat. The second field is
	// the one that matters most: without it the platform is relying on whatever
	// default the board shipped with, and cannot tell "no port is charging"
	// from "this build is not reporting ports".
	HeartbeatInterval byte = 0xA6
	HeartbeatSetReply byte = 0xA7
	// TimeRequest is the board asking the server for civil time; TimeReply is
	// the server's answer. They are named so both ends of the link stop
	// referring to the pair as bare hex literals.
	TimeRequest byte = 0xA8
	TimeReply   byte = 0xA9
	StartCharge byte = 0xB7
	StartReply  byte = 0xB8
	StopCharge  byte = 0xB9
	StopReply   byte = 0xBA
	ChargeEnd   byte = 0xBB
	// ChargeEndReply acknowledges a charge end.
	ChargeEndReply byte = 0xBC
	Fault          byte = 0xC0
	FaultReply     byte = 0xC1
	// ChargingBand is the unsolicited per-port report the board may send while
	// a charge runs.
	ChargingBand byte = 0xC2
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
	ByTime          ChargeMode = 0
	ByEnergy        ChargeMode = 1
	PlatformBilling ChargeMode = 4
	// The vendor reserves 2 (pay by amount) and 3 (stop when full) but states
	// the hardware implements neither, so they are deliberately not offered.
	//
	// 10/11/12 are the long-run variants, where the board keeps supplying until
	// the time or energy runs out or a remote stop arrives. The document says
	// in as many words that this mode is not used in normal operation, so it is
	// not reachable from an ordinary charge request.
	LongTime            ChargeMode = 10
	LongEnergy          ChargeMode = 11
	LongPlatformBilling ChargeMode = 12
)

// NormalChargeModes are the only charging types a request from a charging user may use.
var NormalChargeModes = map[ChargeMode]bool{
	ByTime: true, ByEnergy: true, PlatformBilling: true,
}

// IsNormal reports whether the mode may be used for an ordinary charge.
//
// Long-run modes are excluded rather than merely discouraged. They exist for
// troubleshooting and they bypass the time and energy limits the platform sets,
// so letting a routine request reach one would remove the only bound on how
// long a device can be occupied.
func (m ChargeMode) IsNormal() bool { return NormalChargeModes[m] }

type StartCommand struct {
	Session  [6]byte
	Port     byte
	OrderBCD [8]byte
	Mode     ChargeMode
	Quantity uint16 // minutes or 0.001 kWh, depending on Mode
}

// BuildStart uses the documented scan consumer type (2); reserved card fields
// are zero. The business layer must authorize payment before calling it.
//
// Long-run modes are rejected outright. They are a vendor capability, not a
// product option, and this is the only choke point every start request passes
// through.
func BuildStart(command StartCommand) (Frame, error) {
	if command.Port == 0 || !command.Mode.IsNormal() || command.Quantity == 0 {
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

// BuildHeartbeatInterval asks the board for a heartbeat period and turns
// per-port telemetry on or off.
//
// Sending it is not optional housekeeping. Since 5.8.6 the port block is
// present in a heartbeat only when the platform asked for it, so a platform
// that never sends this is accepting whatever the board's default happens to
// be — and "we get no port telemetry" becomes indistinguishable from "nothing
// is charging". Asking also fixes the read timeout, since the vendor's rule is
// three missed heartbeats rather than any absolute number.
func BuildHeartbeatInterval(session [6]byte, seconds uint16, portStatus bool) (Frame, error) {
	if seconds == 0 {
		return Frame{}, ErrPayload
	}
	flag := byte(0)
	if portStatus {
		flag = 1
	}
	return Frame{Command: HeartbeatInterval, Session: session,
		Data: []byte{byte(seconds), byte(seconds >> 8), flag}}, nil
}

// ParseHeartbeatSetReply reads the 0xA7 acknowledgement.
func ParseHeartbeatSetReply(frame Frame) (bool, error) {
	if frame.Command != HeartbeatSetReply || len(frame.Data) != 1 {
		return false, ErrPayload
	}
	return frame.Data[0] == 0, nil
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

// HeartbeatSetting is what the platform asked for in a 0xA6.
type HeartbeatSetting struct {
	Seconds    uint16
	PortStatus bool
}

// BuildHeartbeatSetReply renders the 0xA7 acknowledgement.
func BuildHeartbeatSetReply(setting HeartbeatSetting) Frame {
	code := byte(0)
	if setting.Seconds == 0 {
		code = 1
	}
	return Frame{Command: HeartbeatSetReply, Data: []byte{code}}
}

// ParseHeartbeatInterval reads a 0xA6 downlink. The port-status flag is what
// decides whether each heartbeat carries per-port telemetry at all, so it is
// returned rather than applied silently.
func ParseHeartbeatInterval(frame Frame) (HeartbeatSetting, error) {
	if frame.Command != HeartbeatInterval || len(frame.Data) != 3 {
		return HeartbeatSetting{}, ErrPayload
	}
	return HeartbeatSetting{
		Seconds:    binary.LittleEndian.Uint16(frame.Data[0:2]),
		PortStatus: frame.Data[2] == 1,
	}, nil
}
