package dc589

import (
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
)

// 设备端编码器与服务端解析器是同一份契约的两半。某个字段只在一侧挪位
// 照样能编译，所以这些测试直接断言这种对偶性：Build* 产出的东西，
// 必须原封不动地从配对的 Parse* 里出来。

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

// 协议文档里那份厂商样例帧是唯一已知可用的帧，所以这里逐字节复现它，
// 以证明编码器与文档一致，而不只是与我们自己的解析器一致。
func TestBuildRegistrationMatchesVendorSample(t *testing.T) {
	frame, err := BuildRegistration(testIdentity())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	// A0、LEN=53（8 字节帧头 + 45 字节 payload）、身份信息，然后是 XOR 校验和。
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

// 含字母的 SIM 必须以十六进制存活下来而不是被拒掉，这正是服务端的
// decodeIdentifier 在入口处做的事。
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
		TemperatureC: -10, // 让 +50 偏置跨过零点
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
	bad.Charging = []protocol.PortTelemetry{{Port: 9}} // 端口超出状态列表范围
	if _, err := BuildHeartbeat(testIdentity(), &bad); err == nil {
		t.Fatal("accepted a charging port outside the declared port list")
	}
	noPorts := base
	noPorts.PortStates = nil
	if _, err := BuildHeartbeat(testIdentity(), &noPorts); err == nil {
		t.Fatal("accepted port status with no ports")
	}
	hot := base
	hot.TemperatureC = 250 // +50 之后会溢出 1 字节
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

// 订单字节对主板是不透明的：原样回显才把结束帧与启动这笔充电的订单
// 绑回去。
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

// 启动命令的设备端一半，是把服务端的一条命令变成一次正在跑的充电，
// 所以服务端的编码器与设备端的解析器必须一致。
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

// A9 是主板得知平台认为此刻是几点时间的唯一途径，而模拟器的结算又是
// 按主板自己打的时间戳计时的。在 ParseTimeReply 存在之前，这个帧收
// 下来就被丢掉，于是这一对命令的两半根本谈不上对偶。
func TestParseTimeReplyRoundTripsServerBuild(t *testing.T) {
	// 故意不用宿主机本地时区：该帧携带的是不带偏移量的民用时间，
	// 所以处在别的时区的调用方读回来仍必须是同一时刻，而不是偏移
	// 过的另一个。
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

// 自打这个编解码器写出来起，网关就一直会解析 C2，却没有任何代码能
// 产出一帧，所以遥测路径从任何代码路径都到不了。这份上报说的是时间
// 不是钱：分档折掉的是剩余小时数，这正是服务端读 MinutesAfter 的原因。
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

// 这里的档位是从 1 开始的，与端口状态应答里从 0 数档位正好相反。
// 编码一个 0 或 6，就是往线上放一个主板自己的固件都读不回来的档。
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

// 注册应答与时间应答用同一种编码携带同一时刻，而主板靠前者校时、又会
// 靠后者重新校时。用不同时区回答这两帧，会让主板每次连接都看到服务
// 器自我修正了 8 小时，并采纳这个修正——于是跑在 UTC 容器里的网关，
// 会送给充电桩一个恰好偏离它本该消除的那个时差的时钟。
func TestRegisterReplyAndTimeReplyAgreeOnTheSameInstant(t *testing.T) {
	// 处在协议并不使用的时区的宿主，这既是容器的常态，也是两者唯一
	// 可能不一致的情形。
	host := time.Date(2026, 9, 29, 18, 39, 2, 0, time.UTC)
	_, registeredAt, err := ParseRegisterReply(BuildRegisterReply([6]byte{1, 2, 3, 4, 5, 6}, host))
	if err != nil {
		t.Fatal(err)
	}
	repliedAt, err := ParseTimeReply(BuildTimeReply([6]byte{1, 2, 3, 4, 5, 6}, host))
	if err != nil {
		t.Fatal(err)
	}
	if !registeredAt.Equal(repliedAt) {
		t.Fatalf("the register reply said %s but the time reply said %s for the same instant",
			registeredAt.Format(time.RFC3339), repliedAt.Format(time.RFC3339))
	}
	if !registeredAt.Equal(host) {
		t.Fatalf("the register reply read back as %s, want the instant it was given (%s)",
			registeredAt.Format(time.RFC3339), host.Format(time.RFC3339))
	}
}
