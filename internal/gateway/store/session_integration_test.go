package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
)

func sessionDB(t *testing.T) *MySQLSink {
	t.Helper()
	url := os.Getenv("TEST_GATEWAY_DATABASE_URL")
	if url == "" {
		t.Skip("set a disposable gateway database URL")
	}
	orm := aggregateDB(t)
	return &MySQLSink{DB: orm}
}

func TestRecordSessionPersistsConnectionAudit(t *testing.T) {
	sink := sessionDB(t)
	ctx := context.Background()
	started := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	record := protocol.SessionRecord{
		SessionID:   "sess-insert-" + recordSuffix(t),
		DeviceID:    "QA-SESSION-001",
		Transport:   protocol.TransportTCP,
		RemoteAddr:  "10.0.0.7:51000",
		StartedAt:   started,
		LastActive:  started.Add(50 * time.Second),
		EndedAt:     started.Add(55 * time.Second),
		CloseReason: "read_timeout",
		BytesIn:     4096,
		BytesOut:    512,
		FramesIn:    64,
		FramesOut:   32,
	}
	cleanup := func() {
		sink.DB.Exec("DELETE FROM device_session WHERE session_id = ?", record.SessionID)
	}
	t.Cleanup(cleanup)
	cleanup()

	if err := sink.RecordSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	var row deviceSessionRow
	if err := sink.DB.WithContext(ctx).Where("session_id = ? AND created_month = ?", record.SessionID, monthOf(record.StartedAt)).
		Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.DeviceID != record.DeviceID || row.RemoteAddr != record.RemoteAddr {
		t.Fatalf("row lost identity: device=%q addr=%q", row.DeviceID, row.RemoteAddr)
	}
	if row.BytesIn != 4096 || row.FramesIn != 64 || row.FramesOut != 32 {
		t.Fatalf("counters not stored: in=%d frames_in=%d frames_out=%d", row.BytesIn, row.FramesIn, row.FramesOut)
	}
	if row.CloseReason != "read_timeout" || row.EndedAt.IsZero() {
		t.Fatalf("terminal state missing: reason=%q ended=%v", row.CloseReason, row.EndedAt)
	}
}

// 重试的关闭不能让调用方失败：第二次写入携带的是同样的
// 终态数字，所以覆盖才是正确行为。
func TestRecordSessionIsIdempotentForTheSameSession(t *testing.T) {
	sink := sessionDB(t)
	ctx := context.Background()
	started := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	sessionID := "sess-idem-" + recordSuffix(t)
	t.Cleanup(func() { sink.DB.Exec("DELETE FROM device_session WHERE session_id = ?", sessionID) })
	sink.DB.Exec("DELETE FROM device_session WHERE session_id = ?", sessionID)

	record := protocol.SessionRecord{
		SessionID: sessionID, DeviceID: "QA-SESSION-002", Transport: protocol.TransportTCP,
		RemoteAddr: "10.0.0.8:52000", StartedAt: started,
		LastActive: started.Add(time.Second), EndedAt: started.Add(2 * time.Second),
		CloseReason: "replaced", FramesIn: 3,
	}
	if err := sink.RecordSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.FramesIn = 7
	record.CloseReason = "device_closed"
	if err := sink.RecordSession(ctx, record); err != nil {
		t.Fatalf("second write rejected: %v", err)
	}
	var count int64
	sink.DB.WithContext(ctx).Model(&deviceSessionRow{}).Where("session_id = ?", sessionID).Count(&count)
	if count != 1 {
		t.Fatalf("session written %d times, want a single updated row", count)
	}
	var row deviceSessionRow
	sink.DB.WithContext(ctx).Where("session_id = ?", sessionID).Take(&row)
	if row.FramesIn != 7 || row.CloseReason != "device_closed" {
		t.Fatalf("row not updated: frames_in=%d reason=%q", row.FramesIn, row.CloseReason)
	}
}

// 一条跨过午夜才关闭的会话，之后仍然必须能被查到，
// 这意味着分区键必须来自开始时间。
func TestRecordSessionDerivesPartitionFromStartTime(t *testing.T) {
	sink := sessionDB(t)
	ctx := context.Background()
	started := time.Date(2026, 9, 30, 23, 59, 0, 0, time.UTC)
	sessionID := "sess-partition-" + recordSuffix(t)
	t.Cleanup(func() { sink.DB.Exec("DELETE FROM device_session WHERE session_id = ?", sessionID) })
	sink.DB.Exec("DELETE FROM device_session WHERE session_id = ?", sessionID)

	record := protocol.SessionRecord{
		SessionID: sessionID, DeviceID: "QA-SESSION-003", Transport: protocol.TransportTCP,
		RemoteAddr: "10.0.0.9:53000", StartedAt: started,
		LastActive: started.Add(2 * time.Minute), // 跨过午夜
		EndedAt:    started.Add(2 * time.Minute), CloseReason: "device_closed", FramesIn: 5,
	}
	if err := sink.RecordSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	var row deviceSessionRow
	if err := sink.DB.WithContext(ctx).Where("session_id = ?", sessionID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if got := row.CreatedMonth.Format("2006-01-02"); got != "2026-09-30" {
		t.Fatalf("created_month = %q, want the start month 2026-09-30", got)
	}
}

// 两个键缺任何一个，这一行都无法归属，所以宁可拒绝，
// 也不写成一条孤儿记录。
func TestRecordSessionRejectsMissingIdentity(t *testing.T) {
	sink := sessionDB(t)
	ctx := context.Background()
	if err := sink.RecordSession(ctx, protocol.SessionRecord{DeviceID: "QA-SESSION-004"}); err == nil {
		t.Fatal("accepted a record with no session id")
	}
	if err := sink.RecordSession(ctx, protocol.SessionRecord{SessionID: "sess-orphan"}); err == nil {
		t.Fatal("accepted a record with no device id")
	}
}

// protocol 这一列是 tcp 与 mqtt 两个值的 ENUM，不是自由文本。
// 曾经有一个 "dc589" 这样的适配器名一路走到了这个 INSERT，
// MySQL 把它截断了，而这种事只有数据库自己才拦得住，
// 所以这里直接断言允许的取值，把这份词表固定在一处。
func TestRecordSessionRejectsTransportOutsideTheEnum(t *testing.T) {
	sink := sessionDB(t)
	ctx := context.Background()
	started := time.Now().UTC().Truncate(time.Millisecond)
	sessionID := "sess-enum-" + recordSuffix(t)
	t.Cleanup(func() { sink.DB.Exec("DELETE FROM device_session WHERE session_id = ?", sessionID) })

	record := protocol.SessionRecord{
		SessionID: sessionID, DeviceID: "QA-SESSION-005", Transport: protocol.Transport("dc589"),
		RemoteAddr: "10.0.0.10:54000", StartedAt: started,
		LastActive: started, EndedAt: started, CloseReason: "device_closed",
	}
	// 在非严格模式下，这里会插入一个空字符串并悄悄丢掉传输方式，
	// 所以测试断言的是这个值被拒绝，
	// 而不是接受服务器模式所允许的任何结果。
	if err := sink.RecordSession(ctx, record); err == nil {
		var row deviceSessionRow
		sink.DB.WithContext(ctx).Where("session_id = ?", sessionID).Take(&row)
		if row.Protocol == "" || row.Protocol == "dc589" {
			t.Fatalf("transport %q stored as %q; the column only accepts tcp or mqtt", "dc589", row.Protocol)
		}
	}
}

// monthOf 与 RecordSession 推导分区键的方式保持一致：
// DATE 这一列不携带时分秒，所以查询也必须拿零点来比。
func monthOf(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

func recordSuffix(t *testing.T) string {
	t.Helper()
	// 这一列是 VARCHAR(64)，而 session_id 本身已经带了前缀，
	// 所以这里裁掉后缀，而不是任由 MySQL 拒绝这次插入。
	seed := t.Name() + time.Now().Format("150405.000000")
	if len(seed) > 24 {
		seed = seed[len(seed)-24:]
	}
	return seed
}
