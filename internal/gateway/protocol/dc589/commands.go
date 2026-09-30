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
	// HeartbeatInterval 设置心跳周期，并且从 5.8.6 起还决定主板是否在
	// 每次心跳里带上各端口遥测。第二个字段才是要紧的：不设它，
	// 平台就只能将就主板出厂时的默认值，并且分不清「没有端口在充电」
	// 和「这个版本不上报端口」。
	HeartbeatInterval byte = 0xA6
	HeartbeatSetReply byte = 0xA7
	// TimeRequest 是主板向服务端索要本地民用时间，TimeReply 是服务
	// 端的回答。给它们起名字，是为了让链路两端不再把这一对命令当
	// 成裸的十六进制字面量来提。
	TimeRequest byte = 0xA8
	TimeReply   byte = 0xA9
	StartCharge byte = 0xB7
	StartReply  byte = 0xB8
	StopCharge  byte = 0xB9
	StopReply   byte = 0xBA
	ChargeEnd   byte = 0xBB
	// ChargeEndReply 确认一次充电结束。
	ChargeEndReply byte = 0xBC
	Fault          byte = 0xC0
	FaultReply     byte = 0xC1
	// ChargingBand 是充电过程中主板可以主动发来的各端口上报，
	// 平台不必为此发任何请求。
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

// 文档把模块号和 SIM 值称作 BCD，但它自己给的 SIM 示例里就有一个
// C 半字节。这里把这类标识原样保留成大写十六进制，而不是在注册阶段
// 就把设备拒掉。
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
	// 厂商保留了 2（按金额计费）和 3（充满自停），但同时说明硬件
	// 两者都没实现，所以这里刻意不开放。
	//
	// 10/11/12 是长时变体，主板会一直供电，直到时间或电量耗尽、或
	// 收到远程停止。文档白纸黑字说这种模式不用于常规运营，所以一次
	// 普通的充电请求到不了它。
	LongTime            ChargeMode = 10
	LongEnergy          ChargeMode = 11
	LongPlatformBilling ChargeMode = 12
)

// NormalChargeModes 是充电用户发起的请求唯一可以使用的充电类型。
var NormalChargeModes = map[ChargeMode]bool{
	ByTime: true, ByEnergy: true, PlatformBilling: true,
}

// IsNormal 报告该模式是否可以用于一次普通充电。
//
// 长时模式是被排除掉，而不只是不推荐。它们为排障而存在，而且会
// 绕过平台设定的时间与电量上限，放任一次日常请求走到那里，就等于
// 取消了设备最多能被占用多久的唯一约束。
func (m ChargeMode) IsNormal() bool { return NormalChargeModes[m] }

type StartCommand struct {
	ConsumerType     uint8
	CardNumber       uint32
	CardBalanceUnits uint16
	Session          [6]byte
	Port             byte
	OrderBCD         [8]byte
	Mode             ChargeMode
	Quantity         uint16 // 依 Mode 而定为分钟或 0.001 kWh
}

// BuildStart 使用扫码消费类型（2）或在线卡类型（3），在线卡携带卡号和余额。
// 业务层必须在调用它之前完成支付授权。
//
// 长时模式被直接拒绝。它们是厂商能力而不是产品选项，而这里是每一个
// 启动请求都必经的唯一收口。
func BuildStart(command StartCommand) (Frame, error) {
	if command.Port == 0 || !command.Mode.IsNormal() || command.Quantity == 0 {
		return Frame{}, ErrPayload
	}
	data := make([]byte, 19)
	data[0] = command.Port
	copy(data[1:9], command.OrderBCD[:])
	data[9] = byte(command.Mode)
	data[10] = 2
	if command.ConsumerType != 0 && command.ConsumerType != 2 && command.ConsumerType != 3 {
		return Frame{}, ErrPayload
	}
	if command.ConsumerType == 3 {
		if command.CardNumber == 0 {
			return Frame{}, ErrPayload
		}
		data[10] = 3
		binary.LittleEndian.PutUint32(data[13:17], command.CardNumber)
		binary.LittleEndian.PutUint16(data[17:19], command.CardBalanceUnits)
	} else if command.CardNumber != 0 || command.CardBalanceUnits != 0 {
		return Frame{}, ErrPayload
	}
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
	data[0] = 0 // 已连接
	// 和 BuildTimeReply 出于同一个理由做换算。主板靠这一帧校时，
	// 而服务端回答主板自己发出的时间请求时，用的是同一时刻的同一种
	// 编码，所以一帧要是漏了换算，就等于告诉主板这两者相差了网关
	// 宿主机与协议所带民用时区之间的时差——跑在 UTC 上的容器就是
	// 8 小时，而这个偏差随后还会被主板当成修正值采纳。
	encodeTime(data[1:], now.In(chinaLocation))
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

// BuildHeartbeatInterval 向主板索要心跳周期，并开关各端口遥测。
//
// 发这一帧不是可有可无的例行公事。从 5.8.6 起，端口块只有在平台
// 主动要求时才出现在心跳里，所以从不发它的平台等于听任主板出厂默认
// 值——「我们拿不到端口遥测」于是和「什么都没在充电」变得无法区分。
//
// 发这一帧同时也把读超时定死了，因为厂商的规定是漏掉三次心跳，而
// 不是某个绝对秒数。
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

// ParseHeartbeatSetReply 读取 0xA7 确认帧。
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

// HeartbeatSetting 是平台在 0xA6 里要的东西。
type HeartbeatSetting struct {
	Seconds    uint16
	PortStatus bool
}

// BuildHeartbeatSetReply 渲染 0xA7 确认帧。
func BuildHeartbeatSetReply(setting HeartbeatSetting) Frame {
	code := byte(0)
	if setting.Seconds == 0 {
		code = 1
	}
	return Frame{Command: HeartbeatSetReply, Data: []byte{code}}
}

// ParseHeartbeatInterval 读取 0xA6 下行帧。端口状态标志决定每次
// 心跳到底带不带各端口遥测，所以它是被返回出来，而不是悄悄生效。
func ParseHeartbeatInterval(frame Frame) (HeartbeatSetting, error) {
	if frame.Command != HeartbeatInterval || len(frame.Data) != 3 {
		return HeartbeatSetting{}, ErrPayload
	}
	return HeartbeatSetting{
		Seconds:    binary.LittleEndian.Uint16(frame.Data[0:2]),
		PortStatus: frame.Data[2] == 1,
	}, nil
}
