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
	// The reserved bytes must go out as zero. The document calls them reserved
	// and never says what a non-zero value means, so sending one would be a
	// guess about firmware behaviour.
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
	// 0xFFF1 fills the parameter field, so a board that could not honour the
	// request is recognisable and does not have to be told apart by a code the
	// document does not define.
	_, err := ParsePowerControlReply(Frame{
		Command: cmdPowerControlReply,
		Data:    []byte{byte(PowerSet), byte(PowerOpRemove), 0xFF, 0xF1, 0, 0},
	})
	if !errors.Is(err, ErrPowerControlRejected) {
		t.Fatalf("err = %v, want ErrPowerControlRejected", err)
	}
}

func TestPowerControlRefusesAnUndocumentedOperation(t *testing.T) {
	// The document defines operation 0 and does not enumerate the rest. A code
	// outside the set is refused here so no frame is ever built on a guess.
	_, err := BuildPowerControl([6]byte{}, PowerControl{Control: PowerSet, Operation: PowerOperation(9)})
	var unknown ErrPowerOperationUnknown
	if !errors.As(err, &unknown) || unknown.Operation != 9 {
		t.Fatalf("err = %v, want ErrPowerOperationUnknown for operation 9", err)
	}
}

func TestPowerQueryCarriesNoParameter(t *testing.T) {
	// A query that also carries a value asks the board to do two things at
	// once, and which one it honours is firmware behaviour nobody has measured.
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
	// A 0xE1 echoing a control byte this build does not know is not something
	// to interpret. Refusing it keeps a future firmware value from being read
	// as today's meaning.
	_, err := ParsePowerControlReply(Frame{
		Command: cmdPowerControlReply,
		Data:    []byte{0x07, byte(PowerOpRemove), 0x00, 0x00, 0, 0},
	})
	if !errors.Is(err, ErrPayload) {
		t.Fatalf("err = %v, want ErrPayload", err)
	}
}
