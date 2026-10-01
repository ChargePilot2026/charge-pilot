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

// 重复关闭应成功，并保留相同终态统计。
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

// 会话联合键任一字段缺失时必须拒绝写入。
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

// protocol 仅允许 tcp、mqtt 传输方式，不能写入 dc589 等适配器名称。
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
	// 无论 MySQL 是否启用严格模式，非法传输方式均应在写入前被拒绝。
	if err := sink.RecordSession(ctx, record); err == nil {
		var row deviceSessionRow
		sink.DB.WithContext(ctx).Where("session_id = ?", sessionID).Take(&row)
		if row.Protocol == "" || row.Protocol == "dc589" {
			t.Fatalf("transport %q stored as %q; the column only accepts tcp or mqtt", "dc589", row.Protocol)
		}
	}
}

// monthOf 与 RecordSession 使用相同分区键算法，按 DATE 的零点时间比较。
func monthOf(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

func recordSuffix(t *testing.T) string {
	t.Helper()
	// session_id 含固定前缀，裁剪夹具后缀以满足 VARCHAR(64) 长度限制。
	seed := t.Name() + time.Now().Format("150405.000000")
	if len(seed) > 24 {
		seed = seed[len(seed)-24:]
	}
	return seed
}
