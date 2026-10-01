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
	// 验证保留字节始终为零。
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
	// 验证两字节失败标记 0xFFF1 被识别为请求拒绝。
	_, err := ParsePowerControlReply(Frame{
		Command: cmdPowerControlReply,
		Data:    []byte{byte(PowerSet), byte(PowerOpRemove), 0xF1, 0xFF, 0, 0},
	})
	if !errors.Is(err, ErrPowerControlRejected) {
		t.Fatalf("err = %v, want ErrPowerControlRejected", err)
	}
}

func TestPowerControlRefusesAnUndocumentedOperation(t *testing.T) {
	// 验证仅允许已定义操作 0，拒绝未知操作码。
	_, err := BuildPowerControl([6]byte{}, PowerControl{Control: PowerSet, Operation: PowerOperation(9)})
	var unknown ErrPowerOperationUnknown
	if !errors.As(err, &unknown) || unknown.Operation != 9 {
		t.Fatalf("err = %v, want ErrPowerOperationUnknown for operation 9", err)
	}
}

func TestPowerQueryCarriesNoParameter(t *testing.T) {
	// 查询操作必须拒绝非零参数，避免混合查询和设置语义。
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
	// 验证未知 0xE1 控制字被拒绝，避免按已有语义解析新协议值。
	_, err := ParsePowerControlReply(Frame{
		Command: cmdPowerControlReply,
		Data:    []byte{0x07, byte(PowerOpRemove), 0x00, 0x00, 0, 0},
	})
	if !errors.Is(err, ErrPayload) {
		t.Fatalf("err = %v, want ErrPayload", err)
	}
}
