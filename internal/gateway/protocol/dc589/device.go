package dc589

import (
	"encoding/binary"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
)

// 本文件实现 DC589 设备方向的编解码，与服务端 commands.go 和 measurements.go 配对。
// 模拟器复用相同字段布局，并通过往返测试验证编码与解析的一致性。

// BoardIDDigits 为板号的固定 16 位十进制长度，对应 8 字节 BCD。
// 板号校验比通用 device_id 规则严格，不能接受其他长度或字符。
const BoardIDDigits = 16

// DeviceIdentity 是主板在 A0 中传递的固定身份信息。
type DeviceIdentity struct {
	BoardID         string
	HardwareVersion string
	SoftwareID      string
	SoftwareVersion uint16
	ModuleID        string
	SIM             string
	Signal          byte
}

// BuildRegistration 编码 0xA0；板号必须为 16 位十进制数字，按 BCD 表示，不截断。
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

// PortStatus 表示可选扩展心跳的整机状态；未上报扩展字段时使用 17 字节短格式。
type PortStatus struct {
	DeviceStatus byte
	VoltageV     uint16
	// TemperatureC 以 +50 偏置编码为无符号值，解码后恢复摄氏温度。
	TemperatureC int16
	PortStates   []byte
	Charging     []protocol.PortTelemetry
}

// BuildHeartbeat 编码 A4。status 传 nil 产出 17 字节短格式；传一个则
// 产出服务端在 A6 上启用后的扩展格式。
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
	// 载荷最大为 255 字节，单帧不能包含超出该限制的全部端口状态。
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
		// 线上单位是毫瓦时的千分之一，与服务端一致。
		binary.LittleEndian.PutUint16(entry[7:9], uint16(port.RemainingMWh/1000))
		binary.LittleEndian.PutUint16(entry[9:11], uint16(port.ChargedMWh/1000))
		binary.LittleEndian.PutUint16(entry[11:13], uint16(port.PowerDeciWatts))
		data = append(data, entry...)
	}
	return Frame{Command: Heartbeat, Data: data}, nil
}

// BuildCommandResult 按 command 编码 B8 启动或 BA 停止应答，两者共用相同载荷格式。
func BuildCommandResult(command, code, port byte) (Frame, error) {
	if command != StartReply && command != StopReply {
		return Frame{}, ErrPayload
	}
	return Frame{Command: command, Data: []byte{code, port}}, nil
}

// ChargeEndReport 是充电结束时主板上报的内容，或是自行结束，或是被叫停。
type ChargeEndReport struct {
	Port byte
	// OrderBCD 是从启动本次充电的 B7 原样抄来的 8 个字节。主板不
	// 解释它；原样回显才把结束帧与这笔订单绑在一起。
	OrderBCD       [8]byte
	StartedAt      time.Time
	EndedAt        time.Time
	ChargedMWh     uint32
	PowerDeciWatts uint32
	StopReason     byte
	ConsumerType   byte
}

// BuildChargeEnd 编码 BB，固定 44 字节 payload。服务端会读的每个字段
// 都写在文档规定的偏移上；空出来的位置是保留位，一律发零。
func BuildChargeEnd(report ChargeEndReport) (Frame, error) {
	if report.Port == 0 || report.EndedAt.Before(report.StartedAt) {
		return Frame{}, ErrPayload
	}
	data := make([]byte, 44)
	data[1] = report.Port
	copy(data[2:10], report.OrderBCD[:])
	encodeTime(data[10:16], report.StartedAt)
	encodeTime(data[16:22], report.EndedAt)
	// 将时长拆分为分钟及 0–59 秒余数，满足服务端时长重建校验。
	seconds := uint32(report.EndedAt.Sub(report.StartedAt).Seconds())
	binary.LittleEndian.PutUint16(data[26:28], uint16(seconds/60))
	data[28] = byte(seconds % 60)
	data[29] = report.ConsumerType
	binary.LittleEndian.PutUint16(data[32:34], uint16(report.ChargedMWh/1000))
	data[40] = report.StopReason
	binary.LittleEndian.PutUint16(data[42:44], uint16(report.PowerDeciWatts))
	return Frame{Command: ChargeEnd, Data: data}, nil
}

// BuildFault 编码 C0。payload 是 5 字节：出问题的端口、厂商故障码，
// 外加 3 个服务端不读的保留字节。
func BuildFault(port, code byte) (Frame, error) {
	if port == 0 {
		return Frame{}, ErrPayload
	}
	return Frame{Command: Fault, Data: []byte{port, code, 0, 0, 0}}, nil
}

// BuildTimeRequest 编码 A8，即设备向服务端索要本地民用时间。
func BuildTimeRequest() Frame {
	return Frame{Command: TimeRequest, Data: make([]byte, 6)}
}

// ParseTimeReply 解码 0xA9 的六字节 BCD 民用时间，与注册应答复用相同解码器及协议时区。
func ParseTimeReply(frame Frame) (time.Time, error) {
	if frame.Command != TimeReply || len(frame.Data) != 6 {
		return time.Time{}, ErrPayload
	}
	return decodeTime(frame.Data)
}

// ChargingBandReport 表示 0xC2 功率档位变化及折算后的剩余时长。
// 档位从 1 开始，与端口状态应答从 0 开始的编号方式不同。
type ChargingBandReport struct {
	Port           byte
	BandBefore     byte
	BandAfter      byte
	MinutesBefore  uint16
	MinutesAfter   uint16
	PowerDeciWatts uint16
}

// BuildChargingBand 编码 C2 分档上报，与 ParseChargingBand 使用相同载荷布局。
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

// ParseRegisterReply 解析 A1，返回帧头分配的新 session，供设备后续上行帧使用。
func ParseRegisterReply(frame Frame) (status byte, at time.Time, err error) {
	if frame.Command != RegisterReply || len(frame.Data) != 7 {
		return 0, time.Time{}, ErrPayload
	}
	parsed, err := decodeTime(frame.Data[1:7])
	return frame.Data[0], parsed, err
}

// ParseStartCommand 解码 0xB7，提取设备启动会话的模式、额度、端口及订单标识。
func ParseStartCommand(frame Frame) (StartCommand, error) {
	if frame.Command != StartCharge || len(frame.Data) != 19 {
		return StartCommand{}, ErrPayload
	}
	command := StartCommand{Port: frame.Data[0], Mode: ChargeMode(frame.Data[9])}
	command.ConsumerType = frame.Data[10]
	command.CardNumber = binary.LittleEndian.Uint32(frame.Data[13:17])
	command.CardBalanceUnits = binary.LittleEndian.Uint16(frame.Data[17:19])
	copy(command.OrderBCD[:], frame.Data[1:9])
	command.Quantity = binary.LittleEndian.Uint16(frame.Data[11:13])
	if command.Port == 0 {
		return StartCommand{}, ErrPayload
	}
	return command, nil
}

// ParseStopCommand 读取 B9，1 字节 payload 指明要停哪个端口。
func ParseStopCommand(frame Frame) (port byte, err error) {
	if frame.Command != StopCharge || len(frame.Data) != 1 || frame.Data[0] == 0 {
		return 0, ErrPayload
	}
	return frame.Data[0], nil
}

// encodeBCD 把十进制数字压成 BCD，凡是服务端的解码器读不回去的
// 一律拒绝。
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

// encodeIdentifier 将模块或 SIM 标识编码到固定宽度，字符数必须为字节宽度的两倍。
// 十进制字符按 BCD 编码，其余合法字符按十六进制半字节编码；不补零或截断。
func encodeIdentifier(value string, width int) ([]byte, error) {
	if len(value) != width*2 {
		return nil, ErrPayload
	}
	if packed, err := encodeBCD(value); err == nil {
		return packed, nil
	}
	raw := make([]byte, width)
	for i := range width {
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
