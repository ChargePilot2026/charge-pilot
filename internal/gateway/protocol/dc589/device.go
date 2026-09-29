package dc589

import (
	"encoding/binary"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
)

// This file is the device half of the 5.8.9 codec. commands.go and
// measurements.go only describe the server half: they parse what a board sends
// and build what a server answers. A board simulator needs the mirror image, and
// it needs the *real* encoder rather than a hand-rolled approximation — an
// approximation would let a bug hide behind a matching bug.
//
// Every function here is the dual of a function in the server half, and the
// round-trip tests assert that duality. If a vendor field moves, exactly one of
// the two sides fails.

// BoardIDDigits is the number of decimal digits in a board identifier. The
// board id occupies a fixed eight BCD bytes, so it is always exactly sixteen
// digits and never shorter. This is narrower than the device_id column and than
// the provisioning rule (^[A-Za-z0-9_-]{8,32}$), which accepts identifiers this
// frame cannot carry; see DeviceIdentity.
const BoardIDDigits = 16

// DeviceIdentity is a board's fixed identity as it travels in A0.
type DeviceIdentity struct {
	BoardID         string
	HardwareVersion string
	SoftwareID      string
	SoftwareVersion uint16
	ModuleID        string
	SIM             string
	Signal          byte
}

// BuildRegistration encodes A0. The board id must be exactly sixteen decimal
// digits because it is encoded as BCD; any other length cannot be represented,
// so it is refused here rather than silently truncated on the way to the device.
func BuildRegistration(identity DeviceIdentity) (Frame, error) {
	board, err := encodeBCD(identity.BoardID)
	if err != nil {
		return Frame{}, err
	}
	if len(identity.HardwareVersion) != 8 || len(identity.SoftwareID) != 8 {
		return Frame{}, ErrPayload
	}
	module, err := encodeIdentifier(identity.ModuleID, 8)
	if err != nil {
		return Frame{}, err
	}
	sim, err := encodeIdentifier(identity.SIM, 10)
	if err != nil {
		return Frame{}, err
	}
	data := make([]byte, 45)
	copy(data[0:8], board)
	copy(data[8:16], identity.HardwareVersion)
	copy(data[16:24], identity.SoftwareID)
	binary.LittleEndian.PutUint16(data[24:26], identity.SoftwareVersion)
	copy(data[26:34], module)
	copy(data[34:44], sim)
	data[44] = identity.Signal
	return Frame{Command: Register, Data: data}, nil
}

// PortStatus is the board-wide state a heartbeat can carry alongside the port
// list. The extended heartbeat is optional: a board that does not report it
// sends the seventeen byte form instead.
type PortStatus struct {
	DeviceStatus byte
	VoltageV     uint16
	// TemperatureC is sent with a +50 bias to keep the field unsigned, which is
	// what the server subtracts on the way in.
	TemperatureC int16
	PortStates   []byte
	Charging     []protocol.PortTelemetry
}

// BuildHeartbeat encodes A4. Passing no status produces the short seventeen byte
// form; passing one produces the extended form the server enables on A6.
func BuildHeartbeat(identity DeviceIdentity, status *PortStatus) (Frame, error) {
	board, err := encodeBCD(identity.BoardID)
	if err != nil {
		return Frame{}, err
	}
	module, err := encodeIdentifier(identity.ModuleID, 8)
	if err != nil {
		return Frame{}, err
	}
	data := make([]byte, 17)
	copy(data[0:8], board)
	copy(data[8:16], module)
	data[16] = identity.Signal
	if status == nil {
		return Frame{Command: Heartbeat, Data: data}, nil
	}
	if len(status.PortStates) == 0 || len(status.PortStates) > 64 {
		return Frame{}, ErrPayload
	}
	// A frame may carry at most 255 bytes of payload, so a full board cannot
	// report every charging port in one heartbeat.
	if 23+len(status.PortStates)+13*len(status.Charging) > 255 {
		return Frame{}, ErrLength
	}
	if status.TemperatureC+50 < 0 || status.TemperatureC+50 > 255 {
		return Frame{}, ErrPayload
	}
	data = append(data, status.DeviceStatus)
	voltage := make([]byte, 2)
	binary.LittleEndian.PutUint16(voltage, status.VoltageV)
	data = append(data, voltage[0], voltage[1], byte(int16(status.TemperatureC)+50), byte(len(status.PortStates)))
	data = append(data, status.PortStates...)
	data = append(data, byte(len(status.Charging)))
	for _, port := range status.Charging {
		if port.Port == 0 || port.Port > uint8(len(status.PortStates)) ||
			port.RemainingSecs%60 > 59 || port.ChargedSeconds%60 > 59 {
			return Frame{}, ErrPayload
		}
		entry := make([]byte, 13)
		entry[0] = port.Port
		binary.LittleEndian.PutUint16(entry[1:3], uint16(port.RemainingSecs/60))
		entry[3] = byte(port.RemainingSecs % 60)
		binary.LittleEndian.PutUint16(entry[4:6], uint16(port.ChargedSeconds/60))
		entry[6] = byte(port.ChargedSeconds % 60)
		// The wire unit is thousandths of a milliwatt-hour, matching the server.
		binary.LittleEndian.PutUint16(entry[7:9], uint16(port.RemainingMWh/1000))
		binary.LittleEndian.PutUint16(entry[9:11], uint16(port.ChargedMWh/1000))
		binary.LittleEndian.PutUint16(entry[11:13], uint16(port.PowerDeciWatts))
		data = append(data, entry...)
	}
	return Frame{Command: Heartbeat, Data: data}, nil
}

// BuildCommandResult encodes B8 (start) and BA (stop). The caller chooses which
// by command; the payload is identical, so one encoder serves both.
func BuildCommandResult(command, code, port byte) (Frame, error) {
	if command != StartReply && command != StopReply {
		return Frame{}, ErrPayload
	}
	return Frame{Command: command, Data: []byte{code, port}}, nil
}

// ChargeEndReport is what a board reports when a charge finishes, on its own or
// because it was told to stop.
type ChargeEndReport struct {
	Port byte
	// OrderBCD is the eight bytes copied verbatim from the B7 that started this
	// charge. The board does not interpret it; echoing it unchanged is what ties
	// the closing frame to the order.
	OrderBCD       [8]byte
	StartedAt      time.Time
	EndedAt        time.Time
	ChargedMWh     uint32
	PowerDeciWatts uint32
	StopReason     byte
	ConsumerType   byte
}

// BuildChargeEnd encodes BB, a fixed forty-four byte payload. Every field the
// server reads is written at the documented offset; the gaps are reserved and
// sent as zero.
func BuildChargeEnd(report ChargeEndReport) (Frame, error) {
	if report.Port == 0 || report.EndedAt.Before(report.StartedAt) {
		return Frame{}, ErrPayload
	}
	data := make([]byte, 44)
	data[1] = report.Port
	copy(data[2:10], report.OrderBCD[:])
	encodeTime(data[10:16], report.StartedAt)
	encodeTime(data[16:22], report.EndedAt)
	// The server rebuilds the duration from these two fields and rejects a
	// seconds remainder above 59, so the split must be exact.
	seconds := uint32(report.EndedAt.Sub(report.StartedAt).Seconds())
	binary.LittleEndian.PutUint16(data[26:28], uint16(seconds/60))
	data[28] = byte(seconds % 60)
	data[29] = report.ConsumerType
	binary.LittleEndian.PutUint16(data[32:34], uint16(report.ChargedMWh/1000))
	data[40] = report.StopReason
	binary.LittleEndian.PutUint16(data[42:44], uint16(report.PowerDeciWatts))
	return Frame{Command: ChargeEnd, Data: data}, nil
}

// BuildFault encodes C0. The payload is five bytes: the affected port, the
// vendor fault code, and three reserved bytes the server does not read.
func BuildFault(port, code byte) (Frame, error) {
	if port == 0 {
		return Frame{}, ErrPayload
	}
	return Frame{Command: Fault, Data: []byte{port, code, 0, 0, 0}}, nil
}

// BuildTimeRequest encodes A8, the device asking the server for civil time.
func BuildTimeRequest() Frame {
	return Frame{Command: TimeRequest, Data: make([]byte, 6)}
}

// ParseTimeReply reads A9, the server's answer to that request.
//
// A board that has just logged in has no business trusting its own clock, and
// the settlement is timed on the board's timestamps, so this is how a device
// learns what time the platform believes it is. The payload is the same six BCD
// bytes A1 carries in the same civil timezone, and it is read by the same
// decoder — a second implementation would be a second place for the timezone
// assumption to be wrong.
func ParseTimeReply(frame Frame) (time.Time, error) {
	if frame.Command != TimeReply || len(frame.Data) != 6 {
		return time.Time{}, ErrPayload
	}
	return decodeTime(frame.Data)
}

// ChargingBandReport is what a board sends on C2 when it changes power tier.
//
// Banding discounts time, not money: the board keeps supplying, but it reports
// how much time is left *after* the discount rather than before it. Both
// band numbers are 1-based, which is the opposite of the zero-based ladder the
// port-status reply counts, so each side pins its own base.
type ChargingBandReport struct {
	Port           byte
	BandBefore     byte
	BandAfter      byte
	MinutesBefore  uint16
	MinutesAfter   uint16
	PowerDeciWatts uint16
}

// BuildChargingBand encodes C2. It is the mirror of ParseChargingBand, and
// without it the codec has a decoder for an uplink no device can produce — the
// gateway's telemetry path was reachable from no code path at all.
func BuildChargingBand(report ChargingBandReport) (Frame, error) {
	if report.Port == 0 || report.BandBefore < 1 || report.BandBefore > 5 ||
		report.BandAfter < 1 || report.BandAfter > 5 {
		return Frame{}, ErrPayload
	}
	data := make([]byte, 9)
	data[0] = report.Port
	data[1] = report.BandBefore
	binary.LittleEndian.PutUint16(data[2:4], report.MinutesBefore)
	binary.LittleEndian.PutUint16(data[4:6], report.MinutesAfter)
	data[6] = report.BandAfter
	binary.LittleEndian.PutUint16(data[7:9], report.PowerDeciWatts)
	return Frame{Command: ChargingBand, Data: data}, nil
}

// ParseRegisterReply reads A1. The session bytes in the frame header are the
// board's new session, so a device adopts them for every frame it sends after.
func ParseRegisterReply(frame Frame) (status byte, at time.Time, err error) {
	if frame.Command != RegisterReply || len(frame.Data) != 7 {
		return 0, time.Time{}, ErrPayload
	}
	parsed, err := decodeTime(frame.Data[1:7])
	return frame.Data[0], parsed, err
}

// ParseStartCommand reads B7. A board cannot meter a charge it does not know
// about, so this is what turns a command into a running session.
func ParseStartCommand(frame Frame) (StartCommand, error) {
	if frame.Command != StartCharge || len(frame.Data) != 19 {
		return StartCommand{}, ErrPayload
	}
	command := StartCommand{Port: frame.Data[0], Mode: ChargeMode(frame.Data[9])}
	copy(command.OrderBCD[:], frame.Data[1:9])
	command.Quantity = binary.LittleEndian.Uint16(frame.Data[11:13])
	if command.Port == 0 {
		return StartCommand{}, ErrPayload
	}
	return command, nil
}

// ParseStopCommand reads B9, a one byte payload naming the port to stop.
func ParseStopCommand(frame Frame) (port byte, err error) {
	if frame.Command != StopCharge || len(frame.Data) != 1 || frame.Data[0] == 0 {
		return 0, ErrPayload
	}
	return frame.Data[0], nil
}

// encodeBCD turns decimal digits into packed BCD, rejecting anything the
// server's decoder would refuse to read back.
func encodeBCD(digits string) ([]byte, error) {
	if len(digits)%2 != 0 {
		return nil, ErrPayload
	}
	out := make([]byte, len(digits)/2)
	for i := 0; i < len(digits); i += 2 {
		hi, lo := digits[i], digits[i+1]
		if hi < '0' || hi > '9' || lo < '0' || lo > '9' {
			return nil, ErrPayload
		}
		out[i/2] = (hi-'0')<<4 | (lo - '0')
	}
	return out, nil
}

// encodeIdentifier packs a module or SIM identifier into a fixed number of
// bytes. The field is fixed width, so the identifier must be exactly twice that
// width in characters; a shorter one is rejected rather than zero-padded, which
// would silently change the identifier the server reads back.
//
// A clean decimal identifier is packed as BCD, matching the vendor example.
// Anything else is packed as hex nibbles, mirroring the server's
// decodeIdentifier, which falls back to uppercase hex rather than rejecting a
// board whose SIM contains a letter.
func encodeIdentifier(value string, width int) ([]byte, error) {
	if len(value) != width*2 {
		return nil, ErrPayload
	}
	if packed, err := encodeBCD(value); err == nil {
		return packed, nil
	}
	raw := make([]byte, width)
	for i := 0; i < width; i++ {
		raw[i] = hexNibble(value[i*2])<<4 | hexNibble(value[i*2+1])
	}
	return raw, nil
}

func hexNibble(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}
