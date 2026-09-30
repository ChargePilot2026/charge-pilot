package dc589

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// 0xE0 / 0xE1 设置或查询“移除功率”，参数单位为 0.1W。
// 这是充电器移除检测的功率阈值，不能用它表示关闭计费分档。
//
// 这里实现的只有文档定义的那一个操作。文档只写了「操作类型：0 = 移除功率」，
// 其余没有列举，而其余部分正是 docs/migration/go-rebuild.md 里的未决条目 28。
// 因此遇到未知的操作码，这里按名字拒掉，而不是硬编码成某种猜测：猜错就会发出
// 一帧主板解读成平台从未打算要求的东西。

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

// powerControlFailure 是主板在无法满足请求时写进 0xE1 参数字段的值。
// 文档写作 0xFFF1，那是整整两个字节：占满整个字段，而不是字段
// 里的某个取值。
const powerControlFailure uint16 = 0xFFF1

// ErrPowerControlRejected 表示主板拒绝了功率控制，或者
// 用失败标记作答。
var ErrPowerControlRejected = errors.New("主板拒绝功率控制")

// ErrPowerOperationUnknown 表示操作码是文档没有定义的。它与
// 「被拒绝」是不同的错误，因为主板压根没见过这个请求。
type ErrPowerOperationUnknown struct{ Operation PowerOperation }

func (e ErrPowerOperationUnknown) Error() string {
	return fmt.Sprintf("未知的 0xE0 操作类型 %d，厂商文档只定义了 0=移除功率", byte(e.Operation))
}

// PowerControl 是一条 0xE0 请求。
type PowerControl struct {
	Control   PowerControlKind
	Operation PowerOperation
	// DeciWatts 是文档里 0.1W 单位的参数。对 PowerQuery 它
	// 没有意义，必须填零：一条既查询又带参数的命令等于要求主板
	// 同时做两件事。
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
	// data[4:6] 保持为零：文档称它为保留位，却没说非零
	// 代表什么，所以永远不发非零值。
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

// ParsePowerControlReply 读取 0xE1。参数字段等于失败标记的
// 应答就是一次拒绝，这里直接返回 ErrPowerControlRejected，而不是
// 丢给调用方一个还得记得自己检查的值。
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

// SetNoTiering retains the historical helper name; the wire command actually
// sets the removal-detection threshold to zero. It does not disable tiers.
// Deprecated: use BuildPowerControl with an explicit DeciWatts value.
func SetNoTiering(session [6]byte) (Frame, error) {
	return BuildPowerControl(session, PowerControl{Control: PowerSet, Operation: PowerOpRemove})
}
