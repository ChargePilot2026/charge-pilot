package dc589

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

func TestFrameLogsSuppressHeartbeatAndExcludePayload(t *testing.T) {
	var output bytes.Buffer
	c := connection{deviceID: "device-1", logger: log.New(&output, "", 0)}
	for _, cmd := range []byte{Heartbeat, HeartbeatReply} {
		c.logFrame("RX", Frame{Command: cmd}, nil)
	}
	if output.Len() != 0 {
		t.Fatal("heartbeat logs must be opt-in")
	}
	c.debugHeartbeat = true
	c.logFrame("RX", Frame{Command: Heartbeat}, nil)
	if !strings.Contains(output.String(), "command=0xA4") {
		t.Fatal(output.String())
	}
	output.Reset()
	c.logFrame("RX", Frame{Command: OnlineCardSwipe, Data: []byte("secret-card-number")}, nil)
	if !strings.Contains(output.String(), "device=device-1") || strings.Contains(output.String(), "secret-card-number") {
		t.Fatal(output.String())
	}
	output.Reset()
	c.debugHeartbeat = false
	c.logFrame("TX", Frame{Command: HeartbeatReply}, errors.New("write failed"))
	if !strings.Contains(output.String(), "write failed") {
		t.Fatal("heartbeat failures must remain visible")
	}
}

func TestParsedLogsHavePhysicalUnitsAndCommandMeaning(t *testing.T) {
	identity := testIdentity()
	heartbeat, err := BuildHeartbeat(identity, &PortStatus{VoltageV: 220, TemperatureC: 25, PortStates: []byte{1, 0}, Charging: []protocol.PortTelemetry{{Port: 1, PowerDeciWatts: 1505, ChargedMWh: 2000, RemainingMWh: 10000, ChargedSeconds: 61, RemainingSecs: 3599}}})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	c := connection{deviceID: identity.BoardID, debugHeartbeat: true, logger: log.New(&output, "", 0)}
	c.logFrame("RX", heartbeat, nil)
	for _, expected := range []string{"name=心跳遥测", `"power_w":150.5`, `"charged_kwh":0.002`, `"remaining_seconds":3599`, `"state":"空闲"`} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %s in %s", expected, output.String())
		}
	}
	start, err := BuildStart(StartCommand{Port: 2, Mode: PlatformBilling, Quantity: 600, ConsumerType: 2})
	if err != nil {
		t.Fatal(err)
	}
	data, err := parsedLogData(start)
	if err != nil || data["duration_minutes"] != uint16(600) || data["mode"] != "平台计费" || data["port"] != byte(2) {
		t.Fatal(data, err)
	}
	energy, err := BuildStart(StartCommand{Port: 2, Mode: ByEnergy, Quantity: 1000})
	if err != nil {
		t.Fatal(err)
	}
	data, err = parsedLogData(energy)
	if err != nil || data["target_kwh"] != float64(1) {
		t.Fatal(data, err)
	}
	for _, code := range []byte{0, 1, 4, 0x10} {
		data, err = parsedLogData(Frame{Command: StopReply, Data: []byte{code, 2}})
		if err != nil || (data["result"] == "停止成功") != (code == 0x10) {
			t.Fatal(data, err)
		}
	}
	end, err := BuildChargeEnd(ChargeEndReport{Port: 2, StartedAt: time.Now().Add(-time.Minute), EndedAt: time.Now(), ChargedMWh: 5000, PowerDeciWatts: 1500, StopReason: 7})
	if err != nil {
		t.Fatal(err)
	}
	data, err = parsedLogData(end)
	if err != nil || data["stop_reason"] != "远程停止" || data["charged_kwh"] != float64(0.005) {
		t.Fatal(data, err)
	}
}

func TestParsedLogsDoNotExposeCardsAndMalformedFramesDoNotPanic(t *testing.T) {
	card, _ := BuildCardDenied([6]byte{}, 123456789, true, 300)
	data, err := parsedLogData(card)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(data)
	if strings.Contains(string(encoded), "123456789") || data["card"] != "[redacted]" || data["balance"] != "[redacted]" {
		t.Fatal(data)
	}
	for command := 0; command <= 255; command++ {
		for size := 0; size <= 255; size++ {
			_, _ = parsedLogData(Frame{Command: byte(command), Data: make([]byte, size)})
		}
	}
}
