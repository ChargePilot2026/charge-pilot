package dc589

import (
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

// TestUnknownCommandDoesNotKillTheConnection 验证未知但格式正确的命令不会关闭连接。
// 兼容固件可选上报；畸形帧仍按协议错误处理。
func TestUnknownCommandDoesNotKillTheConnection(t *testing.T) {
	frame := Frame{Command: 0xC7, Session: [6]byte{1, 2, 3, 4, 5, 6}, Data: []byte{0}}
	raw, err := Encode(frame)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// 该帧格式正确但命令未知，解码器应返回帧，由适配器记录并跳过。
	decoded, err := Decode(raw)
	if err != nil {
		t.Fatalf("a well-formed unknown command must still decode: %v", err)
	}
	if decoded.Command != 0xC7 {
		t.Fatalf("command = 0x%02x, want 0xC7", decoded.Command)
	}
}

func TestStartRejectsLongRunModes(t *testing.T) {
	// 验证普通充电请求拒绝长时模式，保持平台时间与电量上限。
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
	// 拒绝保留类型及排障长时模式；普通启动仅接受已实现且受额度约束的模式。
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
