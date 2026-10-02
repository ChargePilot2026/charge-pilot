package dc589

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

var chinaLocation = time.FixedZone("CST", 8*3600)

// Civil 将时刻转换为协议使用的民用时区，供不含时区偏移的 BCD 日期字段编码。
// 设备和服务器统一使用该时区，不依赖宿主机本地设置。
func Civil(at time.Time) time.Time { return at.In(chinaLocation) }

// HeartbeatData 是心跳里的公共字段，外加在 A6 打开时可能不完整的充电端口集合。
// 端口超过 12 个的主板可能发两个 A4 帧，每个只带 10 条充电端口测量。
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

// ChargeEndData 表示充电结束上报，保留类型、金额和计量字段用于会话审计。
// 设备计费金额取实际支付记录，不由上报的类型或金额决定。
type ChargeEndData struct {
	Port           uint8
	OrderNumber    string
	StartedAt      time.Time
	EndedAt        time.Time
	ChargeType     uint8
	ChargedSeconds uint32
	// RemainingSeconds 为上报时剩余的授权秒数；功率档位折算后由固件还原到可比较的时长。
	RemainingSeconds uint32
	ChargedMWh       uint32
	// AmountCents 为设备上报的消费金额，单位分；本命令单位为 0.01 元，其他命令可能为 0.1 元。
	AmountCents    int64
	StopReason     uint8
	ConsumerType   uint8
	PowerDeciWatts uint32
	// Band 是会话结束时的功率档位，1..5。
	Band uint8
}

// 0xBB 结算 payload 里的字节偏移。
const (
	endOffsetUpload   = 0
	endOffsetPort     = 1
	endOffsetOrder    = 2
	endOffsetStart    = 10
	endOffsetEnd      = 16
	endOffsetLeftMin  = 22
	endOffsetLeftSec  = 24
	endOffsetType     = 25
	endOffsetUsedMin  = 26
	endOffsetUsedSec  = 28
	endOffsetConsumer = 29
	endOffsetLeftWh   = 30
	endOffsetUsedWh   = 32
	endOffsetAmount   = 34
	endOffsetCard     = 36
	endOffsetStop     = 40
	endOffsetBand     = 41
	endOffsetPower    = 42
)

func ParseChargeEnd(frame Frame) (ChargeEndData, error) {
	if frame.Command != ChargeEnd || len(frame.Data) != 44 || frame.Data[endOffsetPort] == 0 {
		return ChargeEndData{}, ErrPayload
	}
	order, err := decodeBCD(frame.Data[endOffsetOrder : endOffsetOrder+8])
	if err != nil {
		return ChargeEndData{}, err
	}
	started, err := decodeTime(frame.Data[endOffsetStart : endOffsetStart+6])
	if err != nil {
		return ChargeEndData{}, err
	}
	ended, err := decodeTime(frame.Data[endOffsetEnd : endOffsetEnd+6])
	if err != nil || ended.Before(started) {
		return ChargeEndData{}, ErrPayload
	}
	if frame.Data[endOffsetUsedSec] > 59 || frame.Data[endOffsetLeftSec] > 59 {
		return ChargeEndData{}, ErrPayload
	}
	data := frame.Data
	amount := int64(binary.LittleEndian.Uint16(data[endOffsetAmount : endOffsetAmount+2]))
	// 此命令档位从 1 开始，端口状态应答从 0 开始，分别按命令处理。
	band := data[endOffsetBand]
	if band > 5 {
		return ChargeEndData{}, ErrPayload
	}
	return ChargeEndData{
		Port: data[endOffsetPort], OrderNumber: order,
		StartedAt: started, EndedAt: ended,
		ChargeType:       data[endOffsetType],
		RemainingSeconds: uint32(binary.LittleEndian.Uint16(data[endOffsetLeftMin:endOffsetLeftMin+2]))*60 + uint32(data[endOffsetLeftSec]),
		ChargedSeconds:   uint32(binary.LittleEndian.Uint16(data[endOffsetUsedMin:endOffsetUsedMin+2]))*60 + uint32(data[endOffsetUsedSec]),
		ConsumerType:     data[endOffsetConsumer],
		ChargedMWh:       uint32(binary.LittleEndian.Uint16(data[endOffsetUsedWh:endOffsetUsedWh+2])) * 1000,
		AmountCents:      amount,
		StopReason:       data[endOffsetStop],
		Band:             band,
		PowerDeciWatts:   uint32(binary.LittleEndian.Uint16(data[endOffsetPower : endOffsetPower+2])),
	}, nil
}

func ParseChargingBand(frame Frame) (protocol.PortTelemetry, error) {
	if frame.Command != ChargingBand || len(frame.Data) != 9 || frame.Data[0] == 0 {
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
	// 帧中的民用时间不含偏移，按中国大陆协议时区解析，不依赖容器 TZ。
	date := time.Date(2000+values[0], time.Month(values[1]), values[2], values[3], values[4], values[5], 0, chinaLocation)
	if date.Month() != time.Month(values[1]) || date.Day() != values[2] || date.Hour() != values[3] || date.Minute() != values[4] || date.Second() != values[5] {
		return time.Time{}, fmt.Errorf("%w: invalid device time", ErrPayload)
	}
	return date, nil
}
