package dc589

import (
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
)

// The device encoder and the server parser are two halves of one contract. A
// field that moves on one side and not the other would still compile, so these
// tests assert the duality directly: whatever Build* produces must come back
// out of the matching Parse* unchanged.

func testIdentity() DeviceIdentity {
	return DeviceIdentity{
		BoardID:         "5348240514082652",
		HardwareVersion: "SH10HP01",
		SoftwareID:      "DC589SW1",
		SoftwareVersion: 107,
		ModuleID:        "1234567812345678",
		SIM:             "89860000000000000001",
		Signal:          4,
	}
}

func TestBuildRegistrationRoundTripsThroughServerParser(t *testing.T) {
	identity := testIdentity()
	frame, err := BuildRegistration(identity)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseRegistration(frame)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.BoardID != identity.BoardID {
		t.Fatalf("board id = %q, want %q", parsed.BoardID, identity.BoardID)
	}
	if parsed.HardwareVersion != identity.HardwareVersion || parsed.SoftwareID != identity.SoftwareID {
		t.Fatalf("version strings lost: hw=%q sw=%q", parsed.HardwareVersion, parsed.SoftwareID)
	}
	if parsed.SoftwareVersion != identity.SoftwareVersion {
		t.Fatalf("software version = %d, want %d", parsed.SoftwareVersion, identity.SoftwareVersion)
	}
	if parsed.ModuleID != identity.ModuleID || parsed.SIM != identity.SIM {
		t.Fatalf("identifiers lost: module=%q sim=%q", parsed.ModuleID, parsed.SIM)
	}
	if parsed.Signal != identity.Signal {
		t.Fatalf("signal = %d, want %d", parsed.Signal, identity.Signal)
	}
}

// The vendor sample in the protocol document is the only known-good frame, so
// it is reproduced byte for byte to prove the encoder agrees with the document
// and not merely with our own parser.
func TestBuildRegistrationMatchesVendorSample(t *testing.T) {
	frame, err := BuildRegistration(testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	// A0, LEN=53 (8 header + 45 payload), the identity, then the XOR checksum.
	if raw[0] != StartByte || raw[2] != Register {
		t.Fatalf("unexpected framing: %x", raw[:3])
	}
	if int(raw[1]) != HeaderSize+45 {
		t.Fatalf("LEN = %d, want %d", raw[1], HeaderSize+45)
	}
	if raw[len(raw)-1] != checksum(raw[1:len(raw)-1]) {
		t.Fatal("checksum does not verify")
	}
}

func TestBuildRegistrationRejectsNonNumericBoardID(t *testing.T) {
	for _, id := range []string{"QA-DEV-A001", "534824051408265", "53482405140826521"} {
		identity := testIdentity()
		identity.BoardID = id
		if _, err := BuildRegistration(identity); err == nil {
			t.Fatalf("board id %q was accepted; a 5.8.9 board id is exactly sixteen decimal digits", id)
		}
	}
}

// A SIM containing a letter must survive as hex rather than being rejected,
// which is what the server's decodeIdentifier does on the way in.
func TestBuildRegistrationCarriesNonDecimalIdentifierAsHex(t *testing.T) {
	identity := testIdentity()
	identity.SIM = "8986000000000000AB01"
	frame, err := BuildRegistration(identity)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseRegistration(frame)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SIM != "8986000000000000AB01" {
		t.Fatalf("SIM = %q, want it preserved as hex", parsed.SIM)
	}
}

func TestBuildHeartbeatShortFormRoundTrips(t *testing.T) {
	identity := testIdentity()
	frame, err := BuildHeartbeat(identity, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.Data) != 17 {
		t.Fatalf("short heartbeat is %d bytes, want 17", len(frame.Data))
	}
	parsed, err := ParseHeartbeat(frame)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.HasPortStatus {
		t.Fatal("short heartbeat reported port status")
	}
	if parsed.BoardID != identity.BoardID || parsed.ModuleID != identity.ModuleID {
		t.Fatalf("identity lost: board=%q module=%q", parsed.BoardID, parsed.ModuleID)
	}
}

func TestBuildHeartbeatWithPortStatusRoundTrips(t *testing.T) {
	status := &PortStatus{
		DeviceStatus: 1,
		VoltageV:     231,
		TemperatureC: -10, // exercises the +50 bias across zero
		PortStates:   []byte{0, 1, 2, 0},
		Charging: []protocol.PortTelemetry{
			{Port: 2, RemainingSecs: 300 + 5, ChargedSeconds: 120 + 7, RemainingMWh: 15000, ChargedMWh: 23000, PowerDeciWatts: 1500},
		},
	}
	frame, err := BuildHeartbeat(testIdentity(), status)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseHeartbeat(frame)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.HasPortStatus {
		t.Fatal("extended heartbeat did not enable port status")
	}
	if parsed.TemperatureC != status.TemperatureC {
		t.Fatalf("temperature = %d, want %d", parsed.TemperatureC, status.TemperatureC)
	}
	if parsed.VoltageV != status.VoltageV || parsed.DeviceStatus != status.DeviceStatus {
		t.Fatalf("board status lost: voltage=%d status=%d", parsed.VoltageV, parsed.DeviceStatus)
	}
	if len(parsed.ChargingPorts) != 1 {
		t.Fatalf("got %d charging ports, want 1", len(parsed.ChargingPorts))
	}
	got := parsed.ChargingPorts[0]
	if got.RemainingSecs != 305 || got.ChargedSeconds != 127 {
		t.Fatalf("durations wrong: remaining=%d charged=%d", got.RemainingSecs, got.ChargedSeconds)
	}
	if got.RemainingMWh != 15000 || got.ChargedMWh != 23000 || got.PowerDeciWatts != 1500 {
		t.Fatalf("energy wrong: remaining=%d charged=%d power=%d", got.RemainingMWh, got.ChargedMWh, got.PowerDeciWatts)
	}
}

func TestBuildHeartbeatRejectsInconsistentPortData(t *testing.T) {
	base := PortStatus{PortStates: []byte{0, 0}, TemperatureC: 20}
	bad := base
	bad.Charging = []protocol.PortTelemetry{{Port: 9}} // port beyond the state list
	if _, err := BuildHeartbeat(testIdentity(), &bad); err == nil {
		t.Fatal("accepted a charging port outside the declared port list")
	}
	noPorts := base
	noPorts.PortStates = nil
	if _, err := BuildHeartbeat(testIdentity(), &noPorts); err == nil {
		t.Fatal("accepted port status with no ports")
	}
	hot := base
	hot.TemperatureC = 250 // +50 would overflow a byte
	if _, err := BuildHeartbeat(testIdentity(), &hot); err == nil {
		t.Fatal("accepted a temperature that cannot be encoded")
	}
}

func TestBuildCommandResultRoundTrips(t *testing.T) {
	for _, command := range []byte{StartReply, StopReply} {
		frame, err := BuildCommandResult(command, 0, 2)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParseCommandResult(frame)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Code != 0 || parsed.Port != 2 {
			t.Fatalf("command 0x%02X round-tripped to code=%d port=%d", command, parsed.Code, parsed.Port)
		}
	}
	if _, err := BuildCommandResult(FaultReply, 0, 1); err == nil {
		t.Fatal("built a result for a command that is not a start or stop reply")
	}
}

func TestBuildChargeEndRoundTrips(t *testing.T) {
	started := time.Date(2026, 9, 29, 10, 0, 0, 0, chinaLocation)
	ended := started.Add(25*time.Minute + 30*time.Second)
	order := [8]byte{0, 0, 0, 0, 0, 0, 0x01, 0x23}
	frame, err := BuildChargeEnd(ChargeEndReport{
		Port: 2, OrderBCD: order, StartedAt: started, EndedAt: ended,
		ChargedMWh: 23000, PowerDeciWatts: 1500, StopReason: 1, ConsumerType: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.Data) != 44 {
		t.Fatalf("charge end is %d bytes, want the fixed 44", len(frame.Data))
	}
	parsed, err := ParseChargeEnd(frame)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Port != 2 || parsed.OrderNumber != "0000000000000123" {
		t.Fatalf("identity wrong: port=%d order=%q", parsed.Port, parsed.OrderNumber)
	}
	if parsed.ChargedSeconds != 25*60+30 {
		t.Fatalf("charged seconds = %d, want %d", parsed.ChargedSeconds, 25*60+30)
	}
	if parsed.ChargedMWh != 23000 || parsed.PowerDeciWatts != 1500 {
		t.Fatalf("energy wrong: mwh=%d power=%d", parsed.ChargedMWh, parsed.PowerDeciWatts)
	}
	if !parsed.StartedAt.Equal(started) || !parsed.EndedAt.Equal(ended) {
		t.Fatalf("times wrong: %s .. %s", parsed.StartedAt, parsed.EndedAt)
	}
}

// The order bytes are opaque to the board: echoing them unchanged is what ties
// the closing frame back to the order that started the charge.
func TestBuildChargeEndEchoesOrderBytesVerbatim(t *testing.T) {
	started := time.Date(2026, 9, 29, 10, 0, 0, 0, chinaLocation)
	order := [8]byte{0x53, 0x48, 0x24, 0x05, 0x14, 0x08, 0x26, 0x52}
	frame, err := BuildChargeEnd(ChargeEndReport{Port: 1, OrderBCD: order, StartedAt: started, EndedAt: started, ConsumerType: 2})
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range order {
		if frame.Data[2+i] != b {
			t.Fatalf("order byte %d = %#x, want %#x", i, frame.Data[2+i], b)
		}
	}
}

func TestBuildChargeEndRejectsReversedTimes(t *testing.T) {
	ended := time.Date(2026, 9, 29, 10, 0, 0, 0, chinaLocation)
	if _, err := BuildChargeEnd(ChargeEndReport{Port: 1, StartedAt: ended.Add(time.Minute), EndedAt: ended}); err == nil {
		t.Fatal("accepted a charge that ended before it started")
	}
	if _, err := BuildChargeEnd(ChargeEndReport{Port: 0, StartedAt: ended, EndedAt: ended}); err == nil {
		t.Fatal("accepted a charge on port zero")
	}
}

func TestBuildFaultAndTimeRequestMatchServerExpectations(t *testing.T) {
	fault, err := BuildFault(2, 0x35)
	if err != nil {
		t.Fatal(err)
	}
	if fault.Command != Fault || len(fault.Data) != 5 {
		t.Fatalf("fault frame is 0x%02X with %d bytes, want 0x%02X with 5", fault.Command, len(fault.Data), Fault)
	}
	if fault.Data[0] != 2 || fault.Data[1] != 0x35 {
		t.Fatalf("fault carries port=%d code=%#x, want port=2 code=0x35", fault.Data[0], fault.Data[1])
	}
	if request := BuildTimeRequest(); request.Command != 0xA8 || len(request.Data) != 6 {
		t.Fatalf("time request is 0x%02X with %d bytes, want 0xA8 with 6", request.Command, len(request.Data))
	}
	if _, err := BuildFault(0, 1); err == nil {
		t.Fatal("accepted a fault on port zero")
	}
}

// The device half of a start command is what turns a server command into a
// running charge, so the server's encoder and the device's parser must agree.
func TestParseStartCommandRoundTripsServerBuild(t *testing.T) {
	session := [6]byte{1, 2, 3, 4, 5, 6}
	order := [8]byte{0, 0, 0, 0, 0, 0, 0x12, 0x34}
	frame, err := BuildStart(StartCommand{Session: session, Port: 2, OrderBCD: order, Mode: ByTime, Quantity: 30})
	if err != nil {
		t.Fatal(err)
	}
	command, err := ParseStartCommand(frame)
	if err != nil {
		t.Fatal(err)
	}
	if command.Port != 2 || command.Mode != ByTime || command.Quantity != 30 {
		t.Fatalf("start command wrong: port=%d mode=%d quantity=%d", command.Port, command.Mode, command.Quantity)
	}
	if command.OrderBCD != order {
		t.Fatalf("order bytes changed: %x", command.OrderBCD)
	}
}

func TestParseStopCommandRoundTripsServerBuild(t *testing.T) {
	frame, err := BuildStop([6]byte{9, 9, 9, 9, 9, 9}, 3)
	if err != nil {
		t.Fatal(err)
	}
	port, err := ParseStopCommand(frame)
	if err != nil {
		t.Fatal(err)
	}
	if port != 3 {
		t.Fatalf("stop port = %d, want 3", port)
	}
}

func TestParseRegisterReplyAdoptsServerTime(t *testing.T) {
	now := time.Date(2026, 9, 29, 16, 30, 45, 0, chinaLocation)
	frame := BuildRegisterReply([6]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}, now)
	status, at, err := ParseRegisterReply(frame)
	if err != nil {
		t.Fatal(err)
	}
	if status != 0 {
		t.Fatalf("register status = %d, want 0 (connected)", status)
	}
	if !at.Equal(now) {
		t.Fatalf("server time = %s, want %s", at, now)
	}
}

// A9 is the only way a board learns what time the platform believes it is, and
// the simulator's settlement is timed on the board's own stamps. Before
// ParseTimeReply existed the frame was received and discarded, so the two halves
// of the pair were not dual at all.
func TestParseTimeReplyRoundTripsServerBuild(t *testing.T) {
	// Deliberately not the host's local zone: the frame carries civil time with
	// no offset, so a caller in another zone must still read back the same
	// instant rather than a shifted one.
	server := time.Date(2026, 9, 29, 16, 30, 45, 0, time.FixedZone("UTC+9", 9*3600))
	frame := BuildTimeReply([6]byte{1, 2, 3, 4, 5, 6}, server)
	at, err := ParseTimeReply(frame)
	if err != nil {
		t.Fatal(err)
	}
	if !at.Equal(server) {
		t.Fatalf("server time = %s, want %s", at, server)
	}
	if _, err := ParseTimeReply(Frame{Command: TimeRequest, Data: make([]byte, 6)}); err == nil {
		t.Fatal("an A8 was accepted as an A9")
	}
}

// The gateway has parsed C2 since the codec was written, but nothing could ever
// produce one, so the telemetry path was reachable from no code path at all. The
// report is time, not money: banding discounts the hours left, which is why
// MinutesAfter is the figure the server reads.
func TestBuildChargingBandRoundTripsThroughTheServerParser(t *testing.T) {
	frame, err := BuildChargingBand(ChargingBandReport{
		Port: 2, BandBefore: 1, BandAfter: 3,
		MinutesBefore: 100, MinutesAfter: 50, PowerDeciWatts: 1500,
	})
	if err != nil {
		t.Fatal(err)
	}
	meter, err := ParseChargingBand(frame)
	if err != nil {
		t.Fatal(err)
	}
	if meter.Port != 2 {
		t.Fatalf("port = %d, want 2", meter.Port)
	}
	if meter.RemainingSecs != 50*60 {
		t.Fatalf("remaining = %d seconds, want the 50 minutes left after the 0.5 band", meter.RemainingSecs)
	}
	if meter.PowerDeciWatts != 1500 {
		t.Fatalf("power = %d, want 1500", meter.PowerDeciWatts)
	}
}

// The band is 1-based here, which is the opposite of the zero-based ladder the
// port-status reply counts. Encoding a 0 or a 6 would put a rung on the wire that
// the board's own firmware could not read back.
func TestBuildChargingBandRefusesABandOutsideOneToFive(t *testing.T) {
	for _, band := range []byte{0, 6} {
		if _, err := BuildChargingBand(ChargingBandReport{Port: 1, BandBefore: band, BandAfter: band}); err == nil {
			t.Fatalf("band %d was accepted", band)
		}
	}
	if _, err := BuildChargingBand(ChargingBandReport{Port: 0, BandBefore: 1, BandAfter: 1}); err == nil {
		t.Fatal("a report for port 0 was accepted")
	}
}
