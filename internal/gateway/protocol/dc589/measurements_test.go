package dc589

import (
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

func TestHeartbeatWithChargingPortAndShortForm(t *testing.T) {
	board, _ := hex.DecodeString("5348240514082652")
	module, _ := hex.DecodeString("0863121077410587")
	base := append(append([]byte(nil), board...), module...)
	base = append(base, 17)
	short, err := ParseHeartbeat(Frame{Command: Heartbeat, Data: base})
	if err != nil || short.HasPortStatus || short.BoardID != "5348240514082652" {
		t.Fatalf("short heartbeat: %+v, %v", short, err)
	}
	// One of a two-frame report for a 20-port board. The total charging
	// count is two, but this frame contains one 13-byte port measurement.
	data := append(append([]byte(nil), base...), 0, 220, 0, 100, 20)
	states := make([]byte, 20)
	states[14] = 1
	states[18] = 1
	data = append(data, states...)
	data = append(data, 2, 15, 206, 0, 52, 21, 0, 8, 0, 0, 69, 0, 0x0f, 0x05)
	got, err := ParseHeartbeat(Frame{Command: Heartbeat, Data: data})
	if err != nil || !got.HasPortStatus || got.VoltageV != 220 || got.TemperatureC != 50 || len(got.ChargingPorts) != 1 || got.ChargingPorts[0].Port != 15 || got.ChargingPorts[0].ChargedMWh != 69000 || got.ChargingPorts[0].PowerDeciWatts != 1295 {
		t.Fatalf("parsed heartbeat: %+v, %v", got, err)
	}
	if _, err := ParseHeartbeat(Frame{Command: Heartbeat, Data: data[:len(data)-1]}); !errors.Is(err, ErrPayload) {
		t.Fatalf("truncated charging record accepted: %v", err)
	}
}

func TestDocumentedChargeEndMeterOffsetsAndLocalTime(t *testing.T) {
	// The PDF labels 0C12 as 300 minutes; little-endian 300 is 2C01.
	// Use the stated duration while retaining all other sample fields.
	data, err := hex.DecodeString("01050000000000000000240829150000240829200000000000002c0100020000e8036400000000000001d007")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 44 {
		t.Fatalf("document sample has %d data bytes", len(data))
	}
	end, err := ParseChargeEnd(Frame{Command: ChargeEnd, Data: data})
	if err != nil || end.Port != 5 || end.ChargedSeconds != 300*60 || end.ChargedMWh != 1000000 || end.PowerDeciWatts != 2000 || end.ConsumerType != 2 || end.EndedAt.UTC().Hour() != 12 {
		t.Fatalf("charge end: %+v, %v", end, err)
	}
	data[16] = 0x23 // end before start
	if _, err := ParseChargeEnd(Frame{Command: ChargeEnd, Data: data}); !errors.Is(err, ErrPayload) {
		t.Fatalf("bad end time accepted: %v", err)
	}
}

func TestChargingBandRequiresCompleteMeter(t *testing.T) {
	data, _ := hex.DecodeString("016400015a0002d007")
	got, err := ParseChargingBand(Frame{Command: 0xC2, Data: data})
	if err != nil || got.Port != 1 || got.RemainingSecs != 90*60 || got.PowerDeciWatts != 2000 {
		t.Fatalf("band: %+v, %v", got, err)
	}
	if _, err := ParseChargingBand(Frame{Command: 0xC2, Data: data[:8]}); !errors.Is(err, ErrPayload) {
		t.Fatalf("truncated band accepted: %v", err)
	}
}

func TestTimeReplyUsesChinaCivilTime(t *testing.T) {
	frame := BuildTimeReply([6]byte{}, time.Date(2024, 8, 29, 2, 53, 22, 0, time.UTC))
	if frame.Command != 0xA9 || hex.EncodeToString(frame.Data) != "240829105322" {
		t.Fatalf("time reply: %+v", frame)
	}
}
