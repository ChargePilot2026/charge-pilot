package dc589

import (
	"errors"
	"testing"
)

func TestPowerControlRoundTrip(t *testing.T) {
	session := [6]byte{1, 2, 3, 4, 5, 6}
	frame, err := SetNoTiering(session)
	if err != nil {
		t.Fatalf("set no tiering: %v", err)
	}
	if frame.Command != cmdPowerControl {
		t.Fatalf("command = %#x, want %#x", frame.Command, cmdPowerControl)
	}
	if frame.Session != session {
		t.Fatalf("session was not carried: %#v", frame.Session)
	}
	if len(frame.Data) != powerControlDataBytes {
		t.Fatalf("data = %d bytes, want %d", len(frame.Data), powerControlDataBytes)
	}
	// 保留字节必须发零。文档称它们为保留，却从没说非零代表
	// 什么，所以发一个非零值就是对固件行为的猜测。
	if frame.Data[4] != 0 || frame.Data[5] != 0 {
		t.Fatalf("reserved bytes are %#x, want 0", frame.Data[4:6])
	}
	reply, err := ParsePowerControlReply(Frame{
		Command: cmdPowerControlReply,
		Data:    []byte{byte(PowerSet), byte(PowerOpRemove), 0x00, 0x00, 0, 0},
	})
	if err != nil {
		t.Fatalf("parse reply: %v", err)
	}
	if reply.Operation != PowerOpRemove || reply.DeciWatts != 0 {
		t.Fatalf("reply = %+v, want the operation echoed with no parameter", reply)
	}
}

func TestPowerControlRejectsTheDocumentedFailureMarker(t *testing.T) {
	// 0xFFF1 占满参数字段，所以一块满足不了请求的主板是可以
	// 认出来的，不必去靠一个文档没定义的错误码来区分。
	_, err := ParsePowerControlReply(Frame{
		Command: cmdPowerControlReply,
		Data:    []byte{byte(PowerSet), byte(PowerOpRemove), 0xF1, 0xFF, 0, 0},
	})
	if !errors.Is(err, ErrPowerControlRejected) {
		t.Fatalf("err = %v, want ErrPowerControlRejected", err)
	}
}

func TestPowerControlRefusesAnUndocumentedOperation(t *testing.T) {
	// 文档只定义了操作 0，其余没有列举。这个集合之外的码
	// 在这里就被拒绝，免得任何一帧建立在猜测之上。
	_, err := BuildPowerControl([6]byte{}, PowerControl{Control: PowerSet, Operation: PowerOperation(9)})
	var unknown ErrPowerOperationUnknown
	if !errors.As(err, &unknown) || unknown.Operation != 9 {
		t.Fatalf("err = %v, want ErrPowerOperationUnknown for operation 9", err)
	}
}

func TestPowerQueryCarriesNoParameter(t *testing.T) {
	// 一条又查又带值的命令是在要求主板同时做两件事，而它听
	// 哪一件，全看没人测量过的固件行为。
	if _, err := BuildPowerControl([6]byte{},
		PowerControl{Control: PowerQuery, Operation: PowerOpRemove, DeciWatts: 100}); err == nil {
		t.Fatal("a query carrying a parameter was accepted")
	}
	if _, err := BuildPowerControl([6]byte{},
		PowerControl{Control: PowerQuery, Operation: PowerOpRemove}); err != nil {
		t.Fatalf("a plain query was refused: %v", err)
	}
}

func TestPowerControlRejectsAnUnknownControlByte(t *testing.T) {
	// 一帧回显了本构建不认识控制字的 0xE1，不是什么该去
	// 解释的东西。拒掉它，就不至于将来固件的一个新取值被
	// 当成今天的意思来读。
	_, err := ParsePowerControlReply(Frame{
		Command: cmdPowerControlReply,
		Data:    []byte{0x07, byte(PowerOpRemove), 0x00, 0x00, 0, 0},
	})
	if !errors.Is(err, ErrPayload) {
		t.Fatalf("err = %v, want ErrPayload", err)
	}
}
