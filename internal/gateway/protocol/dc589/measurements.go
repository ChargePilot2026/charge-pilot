package dc589

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
)

var chinaLocation = time.FixedZone("CST", 8*3600)

// Civil 按 5.8.9 帧里携带的时区来呈现一个时刻。
//
// encodeTime 写的是交给它的那个 location 的日历字段，所以一块把时钟留在 UTC
// 的主板一旦给充电开始打时间戳，就会报出 8 小时的偏差——结算随之把这次会话
// 记到错误的小时里。
//
// 把换算收敛在这里，是为了让唯一那处时区假设待在一个地方，而不是让设备侧的
// 每个调用方各自挑一个 location。
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

// ChargeEndData 是一帧结算。
//
// 充电类型和金额也一并读出。两者都不用来判定充电用户该付多少钱——设备侧计费
// 的会话，钱在主板还没上报任何东西之前就已经收走了——但它们是这次会话究竟
// 怎么结束的唯一记录：没有类型，一次长时模式会话和一次普通会话就再也分不开。
type ChargeEndData struct {
	Port           uint8
	OrderNumber    string
	StartedAt      time.Time
	EndedAt        time.Time
	ChargeType     uint8
	ChargedSeconds uint32
	// RemainingSeconds 是主板当时还剩多少时间。功率分档打过折时，
	// 主板会在上报前把它换算回去，所以它可以与授权时长相比，
	// 而不是与打过折的时长相比。
	RemainingSeconds uint32
	ChargedMWh       uint32
	// AmountCents 是主板对这次充电自己的算法，单位是分。主板在
	// 这条命令里按每单位 0.01 元上报，而别的命令用 0.1 元，所以
	// 换算随命令而异，不做共用。
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
	// 这里的档位是从 1 开始的。端口状态应答对同一套档位是从 0
	// 数起的，两者不可互换，所以各自固定自己的起点，而不是共用一个
	// 对其中一方必然是错的辅助函数。
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
	// 厂商帧里是不带偏移量的本地民用时间。本协议部署在中国大陆，
	// 所以不能依赖容器的 TZ。
	date := time.Date(2000+values[0], time.Month(values[1]), values[2], values[3], values[4], values[5], 0, chinaLocation)
	if date.Month() != time.Month(values[1]) || date.Day() != values[2] || date.Hour() != values[3] || date.Minute() != values[4] || date.Second() != values[5] {
		return time.Time{}, fmt.Errorf("%w: invalid device time", ErrPayload)
	}
	return date, nil
}
