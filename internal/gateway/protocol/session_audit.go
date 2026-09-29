package protocol

import (
	"sync/atomic"
	"time"
)

// Transport identifies the connection kind. These are exactly the values
// gateway_db.device_session.protocol accepts (an ENUM of tcp and mqtt), and
// deliberately the transport rather than the vendor adapter name: the column
// answers "how was this device connected", and the vendor is reachable from the
// device row. Writing an adapter name such as "dc589" here is rejected by MySQL.
type Transport string

const (
	TransportTCP  Transport = "tcp"
	TransportMQTT Transport = "mqtt"
)

// SessionAudit accumulates per-connection traffic counters so a session can be
// written to the database once, when the connection ends.
//
// The counters deliberately live in memory and are only flushed on detach.
// Writing every frame would put a database round trip on the TCP hot path and
// make a device that is flooding frames able to stall its own connection. A
// counter that is still running when the process dies is lost, which is the
// correct trade: the audit question is "how long was this connection up and how
// much did it move", and a lost row is recoverable while a stalled socket is
// not.
type SessionAudit struct {
	transport  Transport
	remoteAddr string
	startedAt  time.Time
	sessionID  string

	bytesIn   atomic.Int64
	bytesOut  atomic.Int64
	framesIn  atomic.Int64
	framesOut atomic.Int64
	lastSeen  atomic.Int64 // UnixNano of the most recent frame
}

// NewSessionAudit starts tracking one connection. remoteAddr is captured here
// because the connection is gone by the time the row is written.
func NewSessionAudit(transport Transport, sessionID, remoteAddr string, startedAt time.Time) *SessionAudit {
	audit := &SessionAudit{
		transport:  transport,
		remoteAddr: remoteAddr,
		startedAt:  startedAt,
		sessionID:  sessionID,
	}
	audit.lastSeen.Store(startedAt.UnixNano())
	return audit
}

// Inbound records one received frame. dataLen is the payload length, not the
// length of the framed bytes, so the audit counts what the protocol carried.
func (a *SessionAudit) Inbound(dataLen int, at time.Time) {
	if a == nil {
		return
	}
	a.bytesIn.Add(int64(dataLen))
	a.framesIn.Add(1)
	a.lastSeen.Store(at.UnixNano())
}

// Outbound records one sent frame.
func (a *SessionAudit) Outbound(dataLen int, at time.Time) {
	if a == nil {
		return
	}
	a.bytesOut.Add(int64(dataLen))
	a.framesOut.Add(1)
	a.lastSeen.Store(at.UnixNano())
}

// SessionRecord is the terminal state of one connection, ready to persist.
type SessionRecord struct {
	SessionID   string
	DeviceID    string
	Transport   Transport
	RemoteAddr  string
	StartedAt   time.Time
	LastActive  time.Time
	EndedAt     time.Time
	CloseReason string
	BytesIn     int64
	BytesOut    int64
	FramesIn    int64
	FramesOut   int64
}

// Snapshot freezes the counters. endedAt is passed in rather than sampled here
// so the caller can label the row with the reason it closed, and so the value
// matches the error the connection actually returned.
func (a *SessionAudit) Snapshot(deviceID, closeReason string, endedAt time.Time) SessionRecord {
	if a == nil {
		return SessionRecord{}
	}
	last := time.Unix(0, a.lastSeen.Load()).UTC()
	return SessionRecord{
		SessionID:   a.sessionID,
		DeviceID:    deviceID,
		Transport:   a.transport,
		RemoteAddr:  a.remoteAddr,
		StartedAt:   a.startedAt.UTC(),
		LastActive:  last,
		EndedAt:     endedAt.UTC(),
		CloseReason: closeReason,
		BytesIn:     a.bytesIn.Load(),
		BytesOut:    a.bytesOut.Load(),
		FramesIn:    a.framesIn.Load(),
		FramesOut:   a.framesOut.Load(),
	}
}
