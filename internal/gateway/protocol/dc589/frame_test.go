package dc589

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

func TestDocumentedStopFrame(t *testing.T) {
	raw, err := hex.DecodeString("EE09B900000000000005B5")
	if err != nil {
		t.Fatal(err)
	}
	frame, err := Decode(raw)
	if err != nil || frame.Command != StopCharge || !bytes.Equal(frame.Data, []byte{5}) {
		t.Fatal(frame, err)
	}
	encoded, err := Encode(frame)
	if err != nil || !bytes.Equal(encoded, raw) {
		t.Fatal(hex.EncodeToString(encoded), err)
	}
	read, err := ReadFrame(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil || read.Command != StopCharge {
		t.Fatal(read, err)
	}
}

func TestDocumentedRegistrationFrame(t *testing.T) {
	raw, err := hex.DecodeString("EE35A00000000000005348240514082652534831304850303153483130535030326B000863121077410587898604041018C0013999111E")
	if err != nil {
		t.Fatal(err)
	}
	frame, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := ParseRegistration(frame)
	if err != nil {
		t.Fatal(err)
	}
	if registration.BoardID != "5348240514082652" || registration.SoftwareVersion != 107 || registration.HardwareVersion != "SH10HP01" {
		t.Fatal(registration)
	}
}

func TestFrameRejectsCorruptionAndBadLength(t *testing.T) {
	raw, _ := hex.DecodeString("EE09B900000000000005B5")
	raw[len(raw)-1] ^= 1
	if _, err := Decode(raw); !errors.Is(err, ErrSum) {
		t.Fatal(err)
	}
	raw[1] = 8
	if _, err := Decode(raw); !errors.Is(err, ErrLength) {
		t.Fatal(err)
	}
}

func TestStartCommandUsesDocumentedPayload(t *testing.T) {
	frame, err := BuildStart(StartCommand{Port: 5, Mode: ByTime, Quantity: 100})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	if raw[0] != 0xEE || raw[1] != 0x1B || raw[2] != 0xB7 || raw[9] != 5 || raw[19] != 2 || raw[20] != 100 {
		t.Fatal(hex.EncodeToString(raw))
	}
}
