package dc589

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// 0xE0/0xE1 设置或查询移除检测功率阈值，单位为 0.1 W。
// 仅支持文档定义的操作 0，未知操作返回错误；此参数不用于关闭计费分档。

const (
	cmdPowerControl       = 0xE0
	cmdPowerControlReply  = 0xE1
	powerControlDataBytes = 6 // 控制字 + 操作 + 参数 + 保留
)

type PowerControlKind byte

const (
	// PowerQuery 询问主板当前的设置是什么。
	PowerQuery PowerControlKind = 0
	// PowerSet 修改这个设置。
	PowerSet PowerControlKind = 1
)

type PowerOperation byte

const (
	// PowerOpRemove 表示文档中的移除功率参数。
	PowerOpRemove PowerOperation = 0
)

// powerControlFailure 为 0xE1 参数字段的失败标记 0xFFF1，占两个字节。
const powerControlFailure uint16 = 0xFFF1

// ErrPowerControlRejected 表示主板拒绝了功率控制，或者
// 用失败标记作答。
var ErrPowerControlRejected = errors.New("主板拒绝功率控制")

// ErrPowerOperationUnknown 表示未定义的操作码，与设备明确拒绝已知操作的错误区分。
type ErrPowerOperationUnknown struct{ Operation PowerOperation }

func (e ErrPowerOperationUnknown) Error() string {
	return fmt.Sprintf("未知的 0xE0 操作类型 %d，厂商文档只定义了 0=移除功率", byte(e.Operation))
}

// PowerControl 是一条 0xE0 请求。
type PowerControl struct {
	Control   PowerControlKind
	Operation PowerOperation
	// DeciWatts 是以 0.1 W 为单位的参数；PowerQuery 必须设为零。
	DeciWatts uint16
}

func (c PowerControl) Valid() bool {
	if c.Control != PowerQuery && c.Control != PowerSet {
		return false
	}
	if c.Control == PowerQuery {
		return c.Operation == PowerOpRemove && c.DeciWatts == 0
	}
	return c.Operation == PowerOpRemove
}

func encodePowerControl(c PowerControl) ([]byte, error) {
	if !c.Valid() {
		if c.Operation != PowerOpRemove {
			return nil, ErrPowerOperationUnknown{Operation: c.Operation}
		}
		return nil, errors.New("功率控制参数无效")
	}
	data := make([]byte, powerControlDataBytes)
	data[0] = byte(c.Control)
	data[1] = byte(c.Operation)
	binary.LittleEndian.PutUint16(data[2:4], c.DeciWatts)
	// data[4:6] 为保留字节，按协议编码为零。
	return data, nil
}

// BuildPowerControl 把一条 0xE0 编码到线上。
func BuildPowerControl(session [6]byte, c PowerControl) (Frame, error) {
	data, err := encodePowerControl(c)
	if err != nil {
		return Frame{}, err
	}
	return Frame{Command: cmdPowerControl, Session: session, Data: data}, nil
}

// ParsePowerControlReply 解码 0xE1；参数为失败标记时返回 ErrPowerControlRejected。
func ParsePowerControlReply(frame Frame) (PowerControl, error) {
	if frame.Command != cmdPowerControlReply || len(frame.Data) != powerControlDataBytes {
		return PowerControl{}, ErrPayload
	}
	reply := PowerControl{
		Control:   PowerControlKind(frame.Data[0]),
		Operation: PowerOperation(frame.Data[1]),
		DeciWatts: binary.LittleEndian.Uint16(frame.Data[2:4]),
	}
	if reply.DeciWatts == powerControlFailure || reply.Control == 2 {
		return reply, ErrPowerControlRejected
	}
	if reply.Control > PowerSet || reply.Operation != PowerOpRemove || frame.Data[4] != 0 || frame.Data[5] != 0 {
		return reply, ErrPayload
	}
	return reply, nil
}

// SetNoTiering 将移除检测阈值设为零，不改变功率分档。
// Deprecated: use BuildPowerControl with an explicit DeciWatts value.
func SetNoTiering(session [6]byte) (Frame, error) {
	return BuildPowerControl(session, PowerControl{Control: PowerSet, Operation: PowerOpRemove})
}
