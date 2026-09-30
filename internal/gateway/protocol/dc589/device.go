package dc589

import (
	"encoding/binary"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
)

// 本文件是 5.8.9 编解码器的设备端一半。commands.go 与 measurements.go
// 只描述服务端一半：解析主板发来的东西，构造服务端应答的东西。主板
// 模拟器需要它的镜像，而且需要*真正的*编码器，而不是一个手工凑出来的
// 近似——近似会让一个 bug 躲在另一个正好抵消它的 bug 后面。
//
// 这里每个函数都是服务端一半某个函数的对偶，往返测试会断言这种对偶
// 性。厂商字段一旦挪位，两边必有且只有一边会失败。

// BoardIDDigits 是主板标识中十进制位数的固定值。主板 ID 占固定 8 个
// BCD 字节，所以它总是正好 16 位，绝不会更短。这比 device_id 列更窄，
// 也比开通规则 (^[A-Za-z0-9_-]{8，32}$) 更窄：那条规则会放行本帧
// 承载不了的标识；见 DeviceIdentity。
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

// BuildRegistration 编码 A0。主板 ID 必须正好 16 位十进制数字，因为它
// 按 BCD 编码；其他任何长度都表示不出来，所以这里直接拒绝，而不是在
// 送往设备的路上被悄悄截断。
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

// PortStatus 是心跳在端口列表之外还能带的整机状态。扩展心跳是可选的：
// 不报这块的主板改发 17 字节短格式。
type PortStatus struct {
	DeviceStatus byte
	VoltageV     uint16
	// TemperatureC 带 +50 偏置发送，好让该字段保持无符号，服务端
	// 解码时再把这个偏置减回去。
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
	// 一帧最多带 255 字节 payload，所以一块满载的主板没法在一次
	// 心跳里报完所有充电端口。
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

// BuildCommandResult 编码 B8（启动）和 BA（停止）。用哪一个由调用方
// 通过 command 决定；两者 payload 完全相同，所以一个编码器就够。
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
	// 服务端用这两个字段重建时长，并拒绝秒余数大于 59 的值，所以
	// 这一刀必须切准。
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

// ParseTimeReply 读取 A9，也就是服务端对上面那个请求的应答。
//
// 一块刚登录的主板没有理由相信自己的时钟，而结算又是按主板打的时间戳
// 计时的，所以设备只能靠这个帧知道平台认为现在几点。payload 是与 A1
// 携带的一样 6 个 BCD 字节、处在同一个民用时区，也由同一个解码器读取
// ——再写一份实现，就等于给时区假设多留一个出错的地方。
func ParseTimeReply(frame Frame) (time.Time, error) {
	if frame.Command != TimeReply || len(frame.Data) != 6 {
		return time.Time{}, ErrPayload
	}
	return decodeTime(frame.Data)
}

// ChargingBandReport 是主板切换功率档位时用 C2 发来的内容。
//
// 分档折的是时间不是钱：主板照旧供电，但它报的是打折*之后*还剩多少
// 时间，而不是打折之前。两个档位编号都是从 1 开始的，这跟端口状态
// 应答里从 0 数档位正好相反，所以两边各自固定自己的起点。
type ChargingBandReport struct {
	Port           byte
	BandBefore     byte
	BandAfter      byte
	MinutesBefore  uint16
	MinutesAfter   uint16
	PowerDeciWatts uint16
}

// BuildChargingBand 编码 C2。它是 ParseChargingBand 的镜像，缺了它，
// 这个编解码器就只会解一种没有任何设备能产生的上行帧——网关的遥测
// 路径将完全无法从任何代码路径到达。
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

// ParseRegisterReply 读取 A1。帧头里的 session 字节是主板的新 session，
// 所以设备之后发的每一帧都改用它们。
func ParseRegisterReply(frame Frame) (status byte, at time.Time, err error) {
	if frame.Command != RegisterReply || len(frame.Data) != 7 {
		return 0, time.Time{}, ErrPayload
	}
	parsed, err := decodeTime(frame.Data[1:7])
	return frame.Data[0], parsed, err
}

// ParseStartCommand 读取 B7。主板没法给一笔自己不知情的充电计量，所以
// 正是这个函数把一条命令变成一个运行中的会话。
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

// encodeIdentifier 把模块号或 SIM 标识压进固定字节数。该字段宽度固定，
// 所以标识的字符数必须正好是该宽度的两倍；更短的直接拒绝而不是补零，
// 补零会悄悄改掉服务端读回来的标识。
//
// 干净的十进制标识按 BCD 压，与厂商示例一致。其余一律按十六进制半字节
// 压，对应服务端的 decodeIdentifier——它对含字母的 SIM 回退成大写
// 十六进制，而不是把那块主板直接拒掉。
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
