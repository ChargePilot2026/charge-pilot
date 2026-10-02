package dc589

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

// recordingSink 收集适配器产生的事件，供测试断言解码结果与运维审计内容。
type recordingSink struct {
	mu     sync.Mutex
	events []protocol.Event
}

func (s *recordingSink) Register(context.Context, protocol.Registration) error { return nil }

func (s *recordingSink) Record(_ context.Context, event protocol.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return nil
}

func (s *recordingSink) all() []protocol.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocol.Event(nil), s.events...)
}

func (s *recordingSink) findConfigResults() []protocol.Event {
	var out []protocol.Event
	for _, event := range s.all() {
		if event.Type == protocol.ConfigResult {
			out = append(out, event)
		}
	}
	return out
}

// serveWithFrames 通过回环 TCP 建立适配器会话，发送注册及指定帧并返回记录事件的 sink。
func serveWithFrames(t *testing.T, replies []Frame) *recordingSink {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	sink := &recordingSink{}
	adapter := TCPAdapter{}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			served <- err
			return
		}
		served <- adapter.ServeConn(ctx, conn, sink)
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// 后台读取注册应答及 A6 心跳周期请求，避免下行写入阻塞被测上报帧。
	go func() {
		buf := make([]byte, 512)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}()

	login, err := BuildRegistration(testIdentity())
	if err != nil {
		t.Fatalf("build login: %v", err)
	}
	raw, err := Encode(login)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(raw); err != nil {
		t.Fatalf("write login: %v", err)
	}
	for _, reply := range replies {
		raw, err := Encode(reply)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write(raw); err != nil {
			t.Fatalf("write reply: %v", err)
		}
	}
	time.Sleep(250 * time.Millisecond)
	cancel()
	_ = conn.Close()
	select {
	case <-served:
	case <-time.After(3 * time.Second):
		t.Fatal("the adapter did not release the connection")
	}
	return sink
}

// 参数表拒绝应记录具体错误原因，不能归为未知命令。
func TestConfigRejectionIsRecordedRatherThanCalledUnknown(t *testing.T) {
	// 错误码 5 是文档里的「刷卡扣费金额超出范围」。
	sink := serveWithFrames(t, []Frame{{Command: ConfigAck, Data: []byte{5}}})

	results := sink.findConfigResults()
	if len(results) == 0 {
		t.Fatalf("a parameter-table refusal produced no config result; events were %+v", sink.all())
	}
	var rejection protocol.Event
	for _, event := range results {
		if event.ResultCode == 5 {
			rejection = event
		}
	}
	if rejection.Type != protocol.ConfigResult || rejection.ResultCode != 5 {
		t.Fatalf("config results = %+v, want one carrying code 5", results)
	}
	if rejection.Signal != ConfigAck {
		t.Fatalf("signal = %#x, want the 0xC4 that was answered", rejection.Signal)
	}
}

// 拒绝必须与「链路只是没回答」区分得开。从外面看两者都是「没有确认」，
// 但只有其中一个意味着运维得去改一个数字。
func TestConfigAcceptanceAndRefusalAreDistinguishable(t *testing.T) {
	sink := serveWithFrames(t, []Frame{{Command: ConfigAck, Data: []byte{0}}})
	results := sink.findConfigResults()
	if len(results) == 0 || results[0].ResultCode != 0 {
		t.Fatalf("an accepted table produced %+v, want a zero result code", results)
	}
}

// 验证配置主动上报被识别为正常命令，并保留 RawPayload 原始字节。
func TestConfigReportIsNotTreatedAsUnknown(t *testing.T) {
	table := ConfigTable{RunMode: 0, LocalCoinTime: 10, LocalCardTime: 20, CardAmountCents: 500,
		TemperatureGuard: 0xFF, FloatSeconds: 300, FloatDeciWatts: 100, RemoveSeconds: 60}
	report, err := BuildConfigReport(table)
	if err != nil {
		t.Fatalf("build report: %v", err)
	}
	sink := serveWithFrames(t, []Frame{report})
	results := sink.findConfigResults()
	if len(results) == 0 {
		t.Fatalf("a configuration report produced no config result; events were %+v", sink.all())
	}
	if results[0].ResultCode != 0 {
		t.Fatalf("result code = %#x, want 0 for a report that decoded", results[0].ResultCode)
	}
	if len(results[0].RawPayload) != len(report.Data) {
		t.Fatalf("raw payload = %d bytes, want the %d that arrived", len(results[0].RawPayload), len(report.Data))
	}
	// 验证无法解析的上报记录明确错误，不伪装为有效配置。
	sink = serveWithFrames(t, []Frame{{Command: ConfigReport, Data: []byte{0, 1, 2}}})
	results = sink.findConfigResults()
	if len(results) == 0 || results[0].ResultCode != 0xFF {
		t.Fatalf("an unreadable report produced %+v, want the undecodable marker", results)
	}
}

// 功率控制的应答是同一类问题：一块不肯放弃分档的主板，必须以「拒绝」
// 的形态被看见。
func TestPowerControlRefusalIsRecorded(t *testing.T) {
	sink := serveWithFrames(t, []Frame{{Command: cmdPowerControlReply,
		Data: []byte{byte(PowerSet), byte(PowerOpRemove), 0xF1, 0xFF, 0, 0}}})
	results := sink.findConfigResults()
	if len(results) == 0 {
		t.Fatalf("a power-control refusal produced no config result; events were %+v", sink.all())
	}
	if results[0].ResultCode != 1 {
		t.Fatalf("result code = %#x, want the refusal marker", results[0].ResultCode)
	}
}
