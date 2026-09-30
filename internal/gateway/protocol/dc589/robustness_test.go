package dc589

import (
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
)

// TestUnknownCommandDoesNotKillTheConnection 锁定了厂商文档自己的措辞所要求的
// 那条健壮性规则：文档把平台参数请求、充电分档上报和远程控制应答都标成
// 「有的主板会发、有的不发」。一个碰到不认识的命令就断连接的构建，会让每块
// 跑着老固件的主板都失联，而这个故障看上去像网络问题，而不是缺了个功能。
func TestUnknownCommandDoesNotKillTheConnection(t *testing.T) {
	frame := Frame{Command: 0xC7, Session: [6]byte{1, 2, 3, 4, 5, 6}, Data: []byte{0}}
	raw, err := Encode(frame)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// 这帧是良构的：它是不认识，不是畸形。解码器必须把它交还回来，
	// 好让 adapter 能跳过它。
	decoded, err := Decode(raw)
	if err != nil {
		t.Fatalf("a well-formed unknown command must still decode: %v", err)
	}
	if decoded.Command != 0xC7 {
		t.Fatalf("command = 0x%02x, want 0xC7", decoded.Command)
	}
}

func TestStartRejectsLongRunModes(t *testing.T) {
	// 文档说明长时变体不用于常规运营。它们同时会绕过平台设定的
	// 时间与电量上限，所以一次普通充电请求不该能走到那里。
	for _, mode := range []ChargeMode{LongTime, LongEnergy, LongPlatformBilling} {
		if _, err := BuildStart(StartCommand{Session: [6]byte{1}, Port: 1, OrderBCD: [8]byte{1}, Mode: mode, Quantity: 60}); err == nil {
			t.Fatalf("long-run mode %d must be rejected for a routine start", mode)
		}
	}
	for _, mode := range []ChargeMode{ByTime, ByEnergy, PlatformBilling} {
		if _, err := BuildStart(StartCommand{Session: [6]byte{1}, Port: 1, OrderBCD: [8]byte{1}, Mode: mode, Quantity: 60}); err != nil {
			t.Fatalf("normal mode %d must be accepted: %v", mode, err)
		}
	}
}

func TestStartRejectsReservedChargeTypes(t *testing.T) {
	// 2（按金额计费）和 3（充满自停）在文档里是保留的，硬件两者
	// 都没实现。开放它们会产出一个主板根本满足不了的启动。
	for _, mode := range []ChargeMode{2, 3, 5, 9, 13} {
		if mode.IsNormal() {
			t.Fatalf("charge type %d must not be treated as normal", mode)
		}
	}
}

func TestSessionAuditRecordsUnknownCommands(t *testing.T) {
	audit := newAudit()
	if len(audit.UnknownCommands()) != 0 {
		t.Fatal("a fresh audit must have recorded nothing")
	}
	audit.Unknown(0xC7)
	audit.Unknown(0xC7)
	audit.Unknown(0xB4)
	got := audit.UnknownCommands()
	if len(got) != 2 || got[0] != 0xB4 || got[1] != 0xC7 {
		t.Fatalf("unknown commands = %v, want a distinct ascending [180 199]", got)
	}
}

func newAudit() *protocol.SessionAudit {
	return protocol.NewSessionAudit(protocol.TransportTCP, "sess", "127.0.0.1:1", time.Now())
}
