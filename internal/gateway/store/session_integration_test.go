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

// A retried close must not fail the caller: the second write carries the same
// terminal numbers, so overwriting is the correct behaviour.
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

// A session that opens before midnight and closes after it must still be found
// by a later read, which means the partition key has to come from the start.
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
		LastActive: started.Add(2 * time.Minute), // crosses midnight
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

// Without both keys the row would be unattributable, so it is refused rather
// than written as an orphan.
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

// The protocol column is an ENUM of tcp and mqtt, not free text. An adapter
// name such as "dc589" once reached this insert and MySQL truncated it, which
// only the database can catch, so the accepted values are asserted here to keep
// the vocabulary in one place.
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
	// Under a non-strict server this would insert an empty string and quietly
	// lose the transport, so the test asserts the value is refused rather than
	// accepting whatever the server mode allows.
	if err := sink.RecordSession(ctx, record); err == nil {
		var row deviceSessionRow
		sink.DB.WithContext(ctx).Where("session_id = ?", sessionID).Take(&row)
		if row.Protocol == "" || row.Protocol == "dc589" {
			t.Fatalf("transport %q stored as %q; the column only accepts tcp or mqtt", "dc589", row.Protocol)
		}
	}
}

// monthOf matches how RecordSession derives the partition key: a DATE column
// carries no time of day, so a query has to compare against midnight as well.
func monthOf(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

func recordSuffix(t *testing.T) string {
	t.Helper()
	// The column is VARCHAR(64) and session_id already carries a prefix, so the
	// suffix is trimmed rather than letting MySQL reject the insert.
	seed := t.Name() + time.Now().Format("150405.000000")
	if len(seed) > 24 {
		seed = seed[len(seed)-24:]
	}
	return seed
}
