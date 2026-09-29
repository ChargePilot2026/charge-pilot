package dc589

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// 0xE0 / 0xE1 are new in 5.8.9 and they are the protocol's own way of saying
// "do not tier this board", which is the entry point the service-billed modes
// need. The alternative — charge type 4 — implies it for the duration of a
// session, while these set it on the board itself.
//
// What is implemented here is only the operation the document defines. It says
// "operation type: 0 = remove power" and does not enumerate the rest, and the
// rest is unresolved item 28 in docs/migration/go-rebuild.md. An unknown
// operation code is therefore refused here by name rather than being encoded as
// a guess: a wrong guess would be a frame the board interprets as something
// this platform never intended to ask for.

const (
	cmdPowerControl       = 0xE0
	cmdPowerControlReply  = 0xE1
	powerControlDataBytes = 6 // control + operation + parameter + reserved
)

type PowerControlKind byte

const (
	// PowerQuery asks the board what its current setting is.
	PowerQuery PowerControlKind = 0
	// PowerSet changes it.
	PowerSet PowerControlKind = 1
)

type PowerOperation byte

const (
	// PowerOpRemove turns off the tiered power control: the board stops
	// dividing its output into tiers and delivers a flat setting, which is what
	// a platform doing its own power pricing needs.
	PowerOpRemove PowerOperation = 0
)

// powerControlFailure is the value the board writes into the parameter field of
// a 0xE1 it could not honour. The document states it as 0xFFF1, which is two
// bytes: the whole field, not a value inside it.
const powerControlFailure uint16 = 0xFFF1

// ErrPowerControlRejected reports that the board refused a power control, or
// answered with the failure marker.
var ErrPowerControlRejected = errors.New("主板拒绝功率控制")

// ErrPowerOperationUnknown reports an operation code the document does not
// define. It is a distinct error from a refusal, because the board never saw
// the request: this platform declined to encode it.
type ErrPowerOperationUnknown struct{ Operation PowerOperation }

func (e ErrPowerOperationUnknown) Error() string {
	return fmt.Sprintf("未知的 0xE0 操作类型 %d，厂商文档只定义了 0=移除功率", byte(e.Operation))
}

// PowerControl is one 0xE0 request.
type PowerControl struct {
	Control   PowerControlKind
	Operation PowerOperation
	// DeciWatts is the parameter in the document's 0.1W unit. It is meaningless
	// for PowerQuery and must be zero there: a query that also carries a
	// parameter is asking the board to do two things at once.
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
	binary.BigEndian.PutUint16(data[2:4], c.DeciWatts)
	// data[4:6] stays zero: the document calls it reserved and says nothing
	// about what a non-zero value means, so nothing non-zero is ever sent.
	return data, nil
}

// BuildPowerControl encodes a 0xE0 for the wire.
func BuildPowerControl(session [6]byte, c PowerControl) (Frame, error) {
	data, err := encodePowerControl(c)
	if err != nil {
		return Frame{}, err
	}
	return Frame{Command: cmdPowerControl, Session: session, Data: data}, nil
}

// ParsePowerControlReply reads a 0xE1. A reply whose parameter field is the
// failure marker is a refusal, and it is returned as ErrPowerControlRejected
// rather than as a value the caller has to remember to check.
func ParsePowerControlReply(frame Frame) (PowerControl, error) {
	if len(frame.Data) != powerControlDataBytes {
		return PowerControl{}, ErrPayload
	}
	reply := PowerControl{
		Control:   PowerControlKind(frame.Data[0]),
		Operation: PowerOperation(frame.Data[1]),
		DeciWatts: binary.BigEndian.Uint16(frame.Data[2:4]),
	}
	if reply.DeciWatts == powerControlFailure {
		return reply, ErrPowerControlRejected
	}
	if !reply.Valid() {
		return reply, ErrPayload
	}
	return reply, nil
}

// SetNoTiering is the call the service-billed modes need: take the board out of
// tiered power control so the platform's own power pricing is the only one in
// effect. It is a named operation rather than a raw frame so the intent is
// visible at the call site.
func SetNoTiering(session [6]byte) (Frame, error) {
	return BuildPowerControl(session, PowerControl{Control: PowerSet, Operation: PowerOpRemove})
}
