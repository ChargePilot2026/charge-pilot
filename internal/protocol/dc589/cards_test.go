package dc589

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/google/uuid"
	"testing"
)

func TestDocumentedOnlineCardFrames(t *testing.T) {
	raw, _ := hex.DecodeString("EE12B60000000000000201020C010A00000000A2")
	frame, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	swipe, err := ParseCardSwipe(frame)
	if err != nil || swipe.Port != 2 || swipe.CardNumber != 17564161 || swipe.LocalDebitTenths != 10 {
		t.Fatalf("%+v %v", swipe, err)
	}
	reply, err := BuildCardBalanceReply([6]byte{}, 1711473153, true, CardBalanceUnits(1000))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := Encode(reply)
	expected, _ := hex.DecodeString("EE0FD100000000000000010203666400DC")
	if err != nil || !bytes.Equal(encoded, expected) {
		t.Fatalf("%x %v", encoded, err)
	}
	invalid, err := BuildCardDenied([6]byte{}, swipe.CardNumber, true, 999)
	if err != nil || invalid.Data[0] != 1 || binary.LittleEndian.Uint16(invalid.Data[5:7]) != 0 || !bytes.Equal(invalid.Data[7:], []byte{0, 0}) {
		t.Fatalf("%+v %v", invalid, err)
	}
	query, err := ParseCardBalanceQuery(Frame{Command: CardBalanceQuery, Data: make([]byte, 5)})
	if err != nil || query != 0 {
		t.Fatal(query, err)
	}
	if _, err := BuildCardBalanceReply([6]byte{}, 0, false, 999); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cents int64
		units uint16
	}{{-1, 0}, {9, 0}, {19, 1}, {1000, 100}, {99999999, 65535}} {
		if got := CardBalanceUnits(tc.cents); got != tc.units {
			t.Fatalf("%d -> %d", tc.cents, got)
		}
	}
}

func TestOnlineCardStartCarriesAuthorizedCardAndPlatformDuration(t *testing.T) {
	command := StartCommand{Port: 2, Mode: PlatformBilling, Quantity: 600, ConsumerType: 3, CardNumber: 17564161, CardBalanceUnits: 88}
	f, err := BuildStart(command)
	if err != nil || len(f.Data) != 19 || f.Data[9] != 4 || f.Data[10] != 3 || binary.LittleEndian.Uint16(f.Data[11:13]) != 600 || binary.LittleEndian.Uint32(f.Data[13:17]) != command.CardNumber || binary.LittleEndian.Uint16(f.Data[17:]) != 88 {
		t.Fatalf("%+v %v", f, err)
	}
	command.ConsumerType = 2
	if _, err := BuildStart(command); err == nil {
		t.Fatal("scan command accepted card fields")
	}
	command.ConsumerType = 3
	command.CardNumber = 0
	if _, err := BuildStart(command); err == nil {
		t.Fatal("card start accepted zero card")
	}
}

func TestLiftAndReswipeCreatesDistinctDurableIdentities(t *testing.T) {
	raw, _ := hex.DecodeString("EE12B60000000000000201020C010A00000000A2")
	f, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	sink := serveWithFrames(t, []Frame{f, f, {Command: CardBalanceQuery, Data: make([]byte, 5)}})
	var swipes []protocol.Event
	var query bool
	for _, e := range sink.all() {
		if e.Type == protocol.CardSwipe {
			swipes = append(swipes, e)
		}
		if e.Type == protocol.CardBalanceQuery {
			query = uuid.Validate(e.EventID) == nil
		}
	}
	if len(swipes) != 2 || swipes[0].EventID == swipes[1].EventID || uuid.Validate(swipes[0].EventID) != nil || uuid.Validate(swipes[1].EventID) != nil || !query {
		t.Fatalf("%+v query=%v", swipes, query)
	}
}
