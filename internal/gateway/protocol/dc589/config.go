package dc589

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// DefaultHeartbeatSeconds 是 3 秒，而厂商对链路存活的规定是
	// 漏掉三次心跳，所以平台的读超时取所申请周期的三倍，而不是
	// 构建期拍脑袋写死的常数。
	DefaultHeartbeatSeconds = 3
	// MissedHeartbeatsBeforeReset 是连续多少次心跳无人应答之后，
	// 主板判定链路已死并重发登录。
	MissedHeartbeatsBeforeReset = 3
)

// ErrConfigRejected 表示主板拒绝了一份参数表。错误码指明是哪个
// 字段越界，这正是「运维照着改一个值」和「运维靠猜」之间的区别。
type ErrConfigRejected struct{ Code byte }

func (e ErrConfigRejected) Error() string {
	if field, ok := configErrorField(e.Code); ok {
		return fmt.Sprintf("设备拒绝参数下发：%s (code %d)", field, e.Code)
	}
	return fmt.Sprintf("设备拒绝参数下发：未知错误码 %d", e.Code)
}

func configErrorField(code byte) (string, bool) {
	switch code {
	case 0:
		return "成功", true
	case 1:
		return "设备运行模式超出范围", true
	case 2:
		return "音量超出范围", true
	case 3:
		return "本地投币时间/电量超出范围", true
	case 4:
		return "本地刷卡时间/电量超出范围", true
	case 5:
		return "刷卡扣费金额超出范围", true
	case 6:
		return "刷卡退费设置错误", true
	case 7:
		return "分档功率设置错误", true
	case 8:
		return "分档比例值设置错误", true
	case 9:
		return "充满自停设置错误", true
	case 10:
		return "浮充功率超出范围", true
	case 11:
		return "浮充时间超出范围", true
	case 12:
		return "充电器移除检查时间超出范围", true
	case 13:
		return "温度保护值超出范围", true
	default:
		return "", false
	}
}

// ConfigTable 是主板本地的参数表。
//
// 承载定价的两个字段——刷卡金额与刷卡时间/电量——其单位随运行模式
// 而变：时间模式下一个数值表示分钟，电量模式下则表示百分之一 kWh。
// Getter 和 setter 都绕道运行模式，而不让调用方直接读字节，因为同一
// 个数字在这里代表两种不同的量，弄混就是百倍量级的错误，却仍然长得
// 像一个合理的数字。
type ConfigTable struct {
	// RunMode 取值 0 时间优先、1 时间卡优先、2 免费、3 电量优先、
	// 4 电量卡优先。
	RunMode byte
	// LocalCoinTime 与 LocalCardTime 在时间模式下是分钟，在电量模式
	// 下是每 kWh 的百分之一。
	LocalCoinTime uint16
	LocalCardTime uint16
	// CardAmountCents 是刷卡扣费金额，单位分，0..2500。
	CardAmountCents uint16
	// CardRefund 取 0 不退费、1 退费。
	CardRefund byte
	// TierWatts 存放最多 5 档的功率上限，单位瓦。高档不大于低档的
	// 表会被主板拒绝。
	TierWatts [5]uint16
	// TierRatioPercent 存放与各档对应的折扣，1..100。第一档被固件
	// 写死为 100，写成其他任何值都会让整次下发失败，所以其他情况下
	// 从不发送。
	TierRatioPercent [5]byte
	// StopWhenFull 在电池充满时结束会话。
	StopWhenFull     byte
	FloatDeciWatts   uint16
	FloatSeconds     uint16
	RemoveSeconds    uint16
	TemperatureGuard byte // 0xFF 表示关闭
}

// cardTimeIsEnergy 报告这张表里刷卡时间/电量字段是否按电量读取。
func (c ConfigTable) cardTimeIsEnergy() bool { return c.RunMode == 3 || c.RunMode == 4 }

// CardMinutes 把存下来的刷卡时间/电量换算成分钟。电量模式下根本
// 没有时长可报，所以它返回 false，而不是凭空编一个出来。
func (c ConfigTable) CardMinutes() (uint16, bool) {
	if c.cardTimeIsEnergy() {
		return 0, false
	}
	return c.LocalCardTime, true
}

// CardMilliWh 把存下来的刷卡时间/电量换算成电量。时间模式下主板手里
// 根本没有电量数字，所以它返回 false。
func (c ConfigTable) CardMilliWh() (uint64, bool) {
	if !c.cardTimeIsEnergy() {
		return 0, false
	}
	// 该字段以百分之一 kWh 计数。
	return uint64(c.LocalCardTime) * 10, true
}

// Validate 按固件实际强制的范围校验这张表，好让一次拒绝在这里就被
// 挡住，而不是到了主板上变成一次悄无声息的空操作。
func (c ConfigTable) Validate() error {
	if c.RunMode > 4 {
		return errors.New("设备运行模式超出范围")
	}
	if c.LocalCoinTime > 999 || c.LocalCardTime > 999 {
		return errors.New("本地时间/电量超出范围")
	}
	if c.CardAmountCents > 2500 {
		return errors.New("刷卡扣费金额超出范围 0.0-25.0 元")
	}
	if c.CardRefund > 1 || c.StopWhenFull > 1 {
		return errors.New("开关值超出范围")
	}
	if c.TemperatureGuard != 0xFF && (c.TemperatureGuard < 50 || c.TemperatureGuard > 100) {
		return errors.New("温度保护值超出范围 50-100，或 0xFF 关闭")
	}
	if c.FloatSeconds < 120 || c.FloatSeconds > 10800 {
		return errors.New("浮充时间超出范围 120-10800 秒")
	}
	if c.FloatDeciWatts < 10 || c.FloatDeciWatts > 500 {
		return errors.New("浮充功率超出范围 1-50W")
	}
	if c.RemoveSeconds < 5 || c.RemoveSeconds > 3600 {
		return errors.New("移除时间超出范围 5-3600 秒")
	}
	for i := 1; i < len(c.TierWatts); i++ {
		if c.TierWatts[i] > 0 && c.TierWatts[i] <= c.TierWatts[i-1] {
			return errors.New("高档位功率必须大于低档位")
		}
	}
	return nil
}

// configEncoded5 是 5 档固件期望的字节长度。它是 22 个字段却占 33 字节，
// 而这个字节数正是文档自带的样例帧所印证的：LEN 0x29 即 41，等于 8 字节
// 帧头加 33。
//
// 采用 8 档插座变体的主板会多出 3 个功率档和 3 个比例值。文档没有说明
// 主板如何声明自己属于哪一种，所以这里核对长度而不是假定，其余长度一律
// 拒绝。
const configEncoded5 = 33

// 编码后参数表里的字节偏移。
const (
	offRunMode    = 0
	offVolume     = 1
	offCoinTime   = 2
	offCardTime   = 4
	offCardAmount = 6
	offCardRefund = 7
	offTierWatts  = 8
	offTierRatio  = 18
	offStopFull   = 23
	offFloatWatts = 24
	offFloatSecs  = 26
	offRemoveSecs = 28
	offPassword   = 30
	offTempGuard  = 32
)

// EncodeConfig 把这张表渲染成 0xC3 下发用的字节。5 档表用不到
// extra8Band，它是为插座变体预留的。
func EncodeConfig(table ConfigTable) ([]byte, error) {
	if err := table.Validate(); err != nil {
		return nil, err
	}
	// 固件把第一档比例写死为 100，写成别的值就会拒绝整次下发，
	// 所以这里取自常量而不是调用方的表。
	first := table
	first.TierRatioPercent[0] = 100

	data := make([]byte, 0, configEncoded5)
	data = append(data, first.RunMode)
	data = append(data, byte(4)) // 音量，平台不改动
	data = binary.LittleEndian.AppendUint16(data, first.LocalCoinTime)
	data = binary.LittleEndian.AppendUint16(data, first.LocalCardTime)
	data = append(data, byte(first.CardAmountCents/10))
	data = append(data, first.CardRefund)
	for _, watts := range first.TierWatts {
		data = binary.LittleEndian.AppendUint16(data, watts*10) // 0.1W 单位
	}
	for _, ratio := range first.TierRatioPercent {
		data = append(data, ratio)
	}
	data = append(data, first.StopWhenFull)
	data = binary.LittleEndian.AppendUint16(data, first.FloatDeciWatts)
	data = binary.LittleEndian.AppendUint16(data, first.FloatSeconds)
	data = binary.LittleEndian.AppendUint16(data, first.RemoveSeconds)
	data = binary.LittleEndian.AppendUint16(data, 8888) // 面板密码，不改动
	data = append(data, first.TemperatureGuard)
	return data, nil
}

// decodeConfigTable 把一张参数表读回来。5 档表是 22 字节，8 档插座变体
// 多 6 字节。其余长度一律拒绝而不是照猜着读，因为读一张错位的表会得到
// 一份看着像样却完全错误的配置。
func decodeConfigTable(frame Frame) (ConfigTable, error) {
	if (frame.Command != ConfigReport && frame.Command != SetConfig) || len(frame.Data) != configEncoded5 {
		return ConfigTable{}, ErrPayload
	}
	data := frame.Data
	var table ConfigTable
	table.RunMode = data[offRunMode]
	table.LocalCoinTime = binary.LittleEndian.Uint16(data[offCoinTime : offCoinTime+2])
	table.LocalCardTime = binary.LittleEndian.Uint16(data[offCardTime : offCardTime+2])
	table.CardAmountCents = uint16(data[offCardAmount]) * 10
	table.CardRefund = data[offCardRefund]
	for i := range table.TierWatts {
		at := offTierWatts + i*2
		table.TierWatts[i] = binary.LittleEndian.Uint16(data[at:at+2]) / 10
	}
	for i := range table.TierRatioPercent {
		table.TierRatioPercent[i] = data[offTierRatio+i]
	}
	table.StopWhenFull = data[offStopFull]
	table.FloatDeciWatts = binary.LittleEndian.Uint16(data[offFloatWatts : offFloatWatts+2])
	table.FloatSeconds = binary.LittleEndian.Uint16(data[offFloatSecs : offFloatSecs+2])
	table.RemoveSeconds = binary.LittleEndian.Uint16(data[offRemoveSecs : offRemoveSecs+2])
	table.TemperatureGuard = data[offTempGuard]
	return table, nil
}

// ParseConfigAck 读取 0xC4 应答。错误码非零表示主板保留了它原来的表，
// 因此把这次下发当作成功，会让平台以为某个计费策略正在设备上跑，
// 其实并没有。
func ParseConfigAck(frame Frame) error {
	if frame.Command != 0xC4 || len(frame.Data) != 1 {
		return ErrPayload
	}
	if frame.Data[0] != 0 {
		return ErrConfigRejected{Code: frame.Data[0]}
	}
	return nil
}

// SetConfig、ReadConfig 及它们的应答就是参数表相关的命令。下行是
// 0xC3，读取请求是 0xC5，主板用携带该表的 0xC6 回应读取。
const (
	SetConfig    byte = 0xC3
	ConfigAck    byte = 0xC4
	ReadConfig   byte = 0xC5
	ConfigReport byte = 0xC6
)

// BuildSetConfig 把 0xC3 下发用的字节渲染出来。
func BuildSetConfig(table ConfigTable) (Frame, error) {
	data, err := EncodeConfig(table)
	if err != nil {
		return Frame{}, err
	}
	return Frame{Command: SetConfig, Data: data}, nil
}

// DecodeConfig 按主板的读法把一张参数表从线上取下来，两个方向都接受：
// 0xC3 下行和 0xC6 上报携带的是同一份 payload。
func DecodeConfig(frame Frame) (ConfigTable, error) {
	return decodeConfigTable(frame)
}

// BuildConfigAck 渲染 0xC4 应答。错误码非零表示主板保留了它原来的表，
// 所以平台不能把这次计费策略下发当作已送达。
func BuildConfigAck(code byte) Frame {
	return Frame{Command: ConfigAck, Data: []byte{code}}
}

// BuildReadConfigAck 渲染 0xC5 的确认。
func BuildReadConfigAck() Frame {
	return Frame{Command: ReadConfig, Data: []byte{0}}
}

// BuildConfigReport 渲染携带所存参数表的 0xC6 应答。
func BuildConfigReport(table ConfigTable) (Frame, error) {
	data, err := EncodeConfig(table)
	if err != nil {
		return Frame{}, err
	}
	return Frame{Command: ConfigReport, Data: data}, nil
}
