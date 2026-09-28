package dc589

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
)

var chinaLocation = time.FixedZone("CST", 8*3600)

// HeartbeatData contains the common fields and, when enabled by A6, a
// possibly partial set of charging ports. Boards with more than 12 ports may
// send two A4 frames, each containing only ten charging port measurements.
type HeartbeatData struct {
	BoardID       string
	ModuleID      string
	Signal        uint8
	HasPortStatus bool
	DeviceStatus  uint8
	VoltageV      uint16
	TemperatureC  int16
	PortStates    []uint8
	ChargingPorts []protocol.PortTelemetry
}

func ParseHeartbeat(frame Frame) (HeartbeatData, error) {
	if frame.Command != Heartbeat || len(frame.Data) < 17 {
		return HeartbeatData{}, ErrPayload
	}
	board, err := decodeBCD(frame.Data[:8])
	if err != nil {
		return HeartbeatData{}, err
	}
	result := HeartbeatData{BoardID: board, ModuleID: decodeIdentifier(frame.Data[8:16]), Signal: frame.Data[16]}
	if len(frame.Data) == 17 {
		return result, nil
	}
	if len(frame.Data) < 22 {
		return HeartbeatData{}, ErrPayload
	}
	result.HasPortStatus = true
	result.DeviceStatus = frame.Data[17]
	result.VoltageV = binary.LittleEndian.Uint16(frame.Data[18:20])
	result.TemperatureC = int16(frame.Data[20]) - 50
	portCount := int(frame.Data[21])
	if portCount == 0 || portCount > 64 || len(frame.Data) < 23+portCount {
		return HeartbeatData{}, ErrPayload
	}
	result.PortStates = append([]uint8(nil), frame.Data[22:22+portCount]...)
	chargedTotal := int(frame.Data[22+portCount])
	remaining := frame.Data[23+portCount:]
	if len(remaining)%13 != 0 || len(remaining)/13 > chargedTotal || chargedTotal > portCount {
		return HeartbeatData{}, ErrPayload
	}
	seen := make(map[uint8]bool)
	for len(remaining) > 0 {
		port := remaining[0]
		if port == 0 || int(port) > portCount || seen[port] || remaining[3] > 59 || remaining[6] > 59 {
			return HeartbeatData{}, ErrPayload
		}
		seen[port] = true
		result.ChargingPorts = append(result.ChargingPorts, protocol.PortTelemetry{
			Port:           port,
			RemainingSecs:  uint32(binary.LittleEndian.Uint16(remaining[1:3]))*60 + uint32(remaining[3]),
			ChargedSeconds: uint32(binary.LittleEndian.Uint16(remaining[4:6]))*60 + uint32(remaining[6]),
			RemainingMWh:   uint32(binary.LittleEndian.Uint16(remaining[7:9])) * 1000,
			ChargedMWh:     uint32(binary.LittleEndian.Uint16(remaining[9:11])) * 1000,
			PowerDeciWatts: uint32(binary.LittleEndian.Uint16(remaining[11:13])),
		})
		remaining = remaining[13:]
	}
	return result, nil
}

type ChargeEndData struct {
	Port           uint8
	OrderNumber    string
	StartedAt      time.Time
	EndedAt        time.Time
	ChargedSeconds uint32
	ChargedMWh     uint32
	PowerDeciWatts uint32
	StopReason     uint8
	ConsumerType   uint8
}

func ParseChargeEnd(frame Frame) (ChargeEndData, error) {
	if frame.Command != ChargeEnd || len(frame.Data) != 44 || frame.Data[1] == 0 {
		return ChargeEndData{}, ErrPayload
	}
	order, err := decodeBCD(frame.Data[2:10])
	if err != nil {
		return ChargeEndData{}, err
	}
	started, err := decodeTime(frame.Data[10:16])
	if err != nil {
		return ChargeEndData{}, err
	}
	ended, err := decodeTime(frame.Data[16:22])
	if err != nil || ended.Before(started) {
		return ChargeEndData{}, ErrPayload
	}
	if frame.Data[28] > 59 {
		return ChargeEndData{}, ErrPayload
	}
	return ChargeEndData{
		Port: frame.Data[1], OrderNumber: order,
		StartedAt: started, EndedAt: ended,
		ChargedSeconds: uint32(binary.LittleEndian.Uint16(frame.Data[26:28]))*60 + uint32(frame.Data[28]),
		ConsumerType:   frame.Data[29],
		ChargedMWh:     uint32(binary.LittleEndian.Uint16(frame.Data[32:34])) * 1000,
		StopReason:     frame.Data[40],
		PowerDeciWatts: uint32(binary.LittleEndian.Uint16(frame.Data[42:44])),
	}, nil
}

func ParseChargingBand(frame Frame) (protocol.PortTelemetry, error) {
	if frame.Command != 0xC2 || len(frame.Data) != 9 || frame.Data[0] == 0 {
		return protocol.PortTelemetry{}, ErrPayload
	}
	return protocol.PortTelemetry{
		Port:           frame.Data[0],
		RemainingSecs:  uint32(binary.LittleEndian.Uint16(frame.Data[4:6])) * 60,
		PowerDeciWatts: uint32(binary.LittleEndian.Uint16(frame.Data[7:9])),
	}, nil
}

func decodeTime(data []byte) (time.Time, error) {
	if len(data) != 6 {
		return time.Time{}, ErrPayload
	}
	values := make([]int, 6)
	for i, value := range data {
		if value>>4 > 9 || value&15 > 9 {
			return time.Time{}, ErrPayload
		}
		values[i] = int(value>>4)*10 + int(value&15)
	}
	// The vendor frame contains local civil time without an offset. This
	// protocol is deployed in mainland China; do not depend on container TZ.
	date := time.Date(2000+values[0], time.Month(values[1]), values[2], values[3], values[4], values[5], 0, chinaLocation)
	if date.Month() != time.Month(values[1]) || date.Day() != values[2] || date.Hour() != values[3] || date.Minute() != values[4] || date.Second() != values[5] {
		return time.Time{}, fmt.Errorf("%w: invalid device time", ErrPayload)
	}
	return date, nil
}
