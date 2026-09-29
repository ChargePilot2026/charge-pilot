package protocol

import (
	"testing"
	"time"
)

func TestSessionAuditCountsFramesAndBytes(t *testing.T) {
	start := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	audit := NewSessionAudit(TransportTCP, "abcdef", "10.0.0.7:51000", start)

	audit.Inbound(10, start.Add(time.Second))
	audit.Inbound(20, start.Add(2*time.Second))
	audit.Outbound(4, start.Add(3*time.Second))

	record := audit.Snapshot("QA-DEV-A001", "device_closed", start.Add(4*time.Second))
	if record.BytesIn != 30 || record.FramesIn != 2 {
		t.Fatalf("inbound = %d bytes / %d frames, want 30 / 2", record.BytesIn, record.FramesIn)
	}
	if record.BytesOut != 4 || record.FramesOut != 1 {
		t.Fatalf("outbound = %d bytes / %d frames, want 4 / 1", record.BytesOut, record.FramesOut)
	}
	if record.LastActive != start.Add(3*time.Second) {
		t.Fatalf("last active = %s, want the most recent frame at %s", record.LastActive, start.Add(3*time.Second))
	}
	if record.StartedAt != start {
		t.Fatalf("started at = %s, want %s", record.StartedAt, start)
	}
	if record.SessionID != "abcdef" || record.DeviceID != "QA-DEV-A001" {
		t.Fatalf("identity lost: session=%q device=%q", record.SessionID, record.DeviceID)
	}
	if record.RemoteAddr != "10.0.0.7:51000" {
		t.Fatalf("remote addr = %q, want the peer address", record.RemoteAddr)
	}
}

// A connection that never carries a frame must still produce a usable row:
// "connected and went quiet" is exactly the case an operator needs to see.
func TestSessionAuditWithNoFramesReportsStartAsLastActive(t *testing.T) {
	start := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	record := NewSessionAudit(TransportTCP, "s1", "10.0.0.7:51000", start).
		Snapshot("board", "read_timeout", start.Add(time.Minute))
	if !record.LastActive.Equal(start) {
		t.Fatalf("last active = %s, want the start time %s", record.LastActive, start)
	}
	if record.BytesIn != 0 || record.FramesIn != 0 {
		t.Fatalf("idle connection reported traffic: %d bytes / %d frames", record.BytesIn, record.FramesIn)
	}
}

// The audit is read once, after the connection is gone, but a session can be
// detached from a different goroutine than the one serving frames. A data race
// here would corrupt the recorded totals rather than merely reorder them.
func TestSessionAuditIsSafeUnderConcurrentFrames(t *testing.T) {
	start := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	audit := NewSessionAudit(TransportTCP, "s1", "addr", start)
	const writers, frames = 8, 500
	done := make(chan struct{})
	for i := 0; i < writers; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < frames; j++ {
				audit.Inbound(2, start)
				audit.Outbound(3, start)
			}
		}()
	}
	for i := 0; i < writers; i++ {
		<-done
	}
	record := audit.Snapshot("board", "device_closed", start)
	if want := int64(writers * frames); record.FramesIn != want || record.BytesIn != want*2 {
		t.Fatalf("frames_in=%d bytes_in=%d, want %d frames and %d bytes", record.FramesIn, record.BytesIn, want, want*2)
	}
	if want := int64(writers * frames); record.FramesOut != want || record.BytesOut != want*3 {
		t.Fatalf("frames_out=%d bytes_out=%d, want %d frames and %d bytes", record.FramesOut, record.BytesOut, want, want*3)
	}
}
