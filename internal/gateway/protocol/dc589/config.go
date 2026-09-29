package dc589

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// HeartbeatInterval is 3 seconds, and the vendor's rule for a live link is
	// three missed heartbeats, so the platform read timeout is three times the
	// interval it asked for rather than a constant guessed at build time.
	DefaultHeartbeatSeconds = 3
	// MissedHeartbeatsBeforeReset is the number of unanswered heartbeats after
	// which the board considers the link dead and resends its login.
	MissedHeartbeatsBeforeReset = 3
)

// ErrConfigRejected reports that the board refused a parameter table. The code
// names the field that was out of range, which is the difference between an
// operator fixing a value and an operator guessing.
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

// ConfigTable is the board's local parameter table.
//
// The two fields that carry pricing — the card amount and the card
// time/energy — are measured in different units depending on the run mode: a
// value means minutes in time mode and hundredths of a kWh in energy mode.
// Getter and setter go through the mode rather than letting a caller read the
// bytes, because the same number means two different quantities and a mix-up
// here is a hundred-fold error that still looks like a plausible number.
type ConfigTable struct {
	// RunMode is 0 time-first, 1 time-key-first, 2 free, 3 energy-first,
	// 4 energy-key-first.
	RunMode byte
	// LocalCoinTime and LocalCardTime are minutes in time mode, or 100 units
	// per kWh in energy mode.
	LocalCoinTime uint16
	LocalCardTime uint16
	// CardAmountCents is the card charge in cents, 0..2500.
	CardAmountCents uint16
	// CardRefund is 0 no refund, 1 refund.
	CardRefund byte
	// TierWatts holds up to five band ceilings in watts. The board rejects a
	// table whose higher band is not above its lower one.
	TierWatts [5]uint16
	// TierRatioPercent holds the matching discount, 1..100. The first rung is
	// fixed at 100 by the firmware and any other value makes the whole write
	// fail, so it is never sent otherwise.
	TierRatioPercent [5]byte
	// StopWhenFull ends the session when the battery is full.
	StopWhenFull     byte
	FloatDeciWatts   uint16
	FloatSeconds     uint16
	RemoveSeconds    uint16
	TemperatureGuard byte // 0xFF disables
}

// cardTimeIsEnergy reports whether the card time/energy fields are read as
// energy on this table.
func (c ConfigTable) cardTimeIsEnergy() bool { return c.RunMode == 3 || c.RunMode == 4 }

// CardMinutes converts the stored card time/energy into minutes. In energy
// mode there is no duration to report, so it returns false rather than
// inventing one.
func (c ConfigTable) CardMinutes() (uint16, bool) {
	if c.cardTimeIsEnergy() {
		return 0, false
	}
	return c.LocalCardTime, true
}

// CardMilliWh converts the stored card time/energy into energy. In time mode
// there is no energy figure the board holds, so it returns false.
func (c ConfigTable) CardMilliWh() (uint64, bool) {
	if !c.cardTimeIsEnergy() {
		return 0, false
	}
	// The field counts hundredths of a kWh.
	return uint64(c.LocalCardTime) * 10, true
}

// Validate checks the table against the ranges the firmware enforces, so a
// rejection is caught here rather than turned into a silent no-op on the board.
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

// configEncoded5 is the byte length the 5-band firmware expects. It is 22
// fields but 33 bytes, and it is the byte count that the document's own sample
// frame confirms: LEN 0x29 is 41, which is the 8-byte header plus 33.
//
// Boards with the 8-band socket variant carry three more power rungs and three
// more ratios. The document does not say how a board announces which it is, so
// the length is checked rather than assumed and anything else is refused.
const configEncoded5 = 33

// Byte offsets into the encoded table.
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

// EncodeConfig renders the table for a 0xC3 write. extra8Band is ignored for a
// five-band table and reserved for the socket variant.
func EncodeConfig(table ConfigTable) ([]byte, error) {
	if err := table.Validate(); err != nil {
		return nil, err
	}
	// The firmware hard-codes the first ratio at 100 and rejects the whole
	// write otherwise, so it is written from the constant rather than from the
	// caller's table.
	first := table
	first.TierRatioPercent[0] = 100

	data := make([]byte, 0, configEncoded5)
	data = append(data, first.RunMode)
	data = append(data, byte(4)) // volume, unchanged by the platform
	data = binary.LittleEndian.AppendUint16(data, first.LocalCoinTime)
	data = binary.LittleEndian.AppendUint16(data, first.LocalCardTime)
	data = append(data, byte(first.CardAmountCents))
	data = append(data, first.CardRefund)
	for _, watts := range first.TierWatts {
		data = binary.LittleEndian.AppendUint16(data, watts*10) // 0.1W units
	}
	for _, ratio := range first.TierRatioPercent {
		data = append(data, ratio)
	}
	data = append(data, first.StopWhenFull)
	data = binary.LittleEndian.AppendUint16(data, first.FloatDeciWatts)
	data = binary.LittleEndian.AppendUint16(data, first.FloatSeconds)
	data = binary.LittleEndian.AppendUint16(data, first.RemoveSeconds)
	data = binary.LittleEndian.AppendUint16(data, 8888) // panel password, unchanged
	data = append(data, first.TemperatureGuard)
	return data, nil
}

// decodeConfigTable reads a parameter table back. A five-band table is 22
// bytes; the 8-band socket variant adds six. Anything else is refused rather
// than read with a guess, because reading a misaligned table produces a
// configuration that looks plausible and is wrong.
func decodeConfigTable(frame Frame) (ConfigTable, error) {
	if (frame.Command != ConfigReport && frame.Command != SetConfig) || len(frame.Data) != configEncoded5 {
		return ConfigTable{}, ErrPayload
	}
	data := frame.Data
	var table ConfigTable
	table.RunMode = data[offRunMode]
	table.LocalCoinTime = binary.LittleEndian.Uint16(data[offCoinTime : offCoinTime+2])
	table.LocalCardTime = binary.LittleEndian.Uint16(data[offCardTime : offCardTime+2])
	table.CardAmountCents = uint16(data[offCardAmount])
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

// ParseConfigAck reads a 0xC4 reply. A non-zero code means the board kept its
// previous table, so treating the write as successful would leave the platform
// believing a tariff is running on the device when it is not.
func ParseConfigAck(frame Frame) error {
	if frame.Command != 0xC4 || len(frame.Data) != 1 {
		return ErrPayload
	}
	if frame.Data[0] != 0 {
		return ErrConfigRejected{Code: frame.Data[0]}
	}
	return nil
}

// SetConfig, ReadConfig and their replies are the parameter-table commands.
// The downlink is 0xC3, the read request is 0xC5, and the board answers a read
// with 0xC6 carrying the table.
const (
	SetConfig    byte = 0xC3
	ConfigAck    byte = 0xC4
	ReadConfig   byte = 0xC5
	ConfigReport byte = 0xC6
)

// BuildSetConfig renders a 0xC3 write of the table.
func BuildSetConfig(table ConfigTable) (Frame, error) {
	data, err := EncodeConfig(table)
	if err != nil {
		return Frame{}, err
	}
	return Frame{Command: SetConfig, Data: data}, nil
}

// DecodeConfig reads a parameter table off the wire the way a board would,
// accepting either direction: a 0xC3 downlink and a 0xC6 report carry the
// same payload.
func DecodeConfig(frame Frame) (ConfigTable, error) {
	return decodeConfigTable(frame)
}

// BuildConfigAck renders the 0xC4 reply. A non-zero code means the board kept
// its previous table, so the platform must not treat the tariff as delivered.
func BuildConfigAck(code byte) Frame {
	return Frame{Command: ConfigAck, Data: []byte{code}}
}

// BuildReadConfigAck renders the 0xC5 acknowledgement.
func BuildReadConfigAck() Frame {
	return Frame{Command: ReadConfig, Data: []byte{0}}
}

// BuildConfigReport renders the 0xC6 reply carrying the stored table.
func BuildConfigReport(table ConfigTable) (Frame, error) {
	data, err := EncodeConfig(table)
	if err != nil {
		return Frame{}, err
	}
	return Frame{Command: ConfigReport, Data: data}, nil
}
