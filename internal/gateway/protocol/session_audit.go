package protocol

import (
	"sync"
	"sync/atomic"
	"time"
)

// Transport 记录 tcp 或 mqtt，与 device_session.protocol 枚举一致。
// 该字段表示传输方式，不填写 dc589 等设备协议名称。
type Transport string

const (
	TransportTCP  Transport = "tcp"
	TransportMQTT Transport = "mqtt"
)

// SessionAudit 在内存累加连接流量，并在关闭时一次写入数据库，避免每帧访问存储。
// 进程异常退出时未落盘计数可能丢失，遗留会话由后续清理处理。
type SessionAudit struct {
	transport  Transport
	remoteAddr string
	startedAt  time.Time
	sessionID  string

	bytesIn   atomic.Int64
	bytesOut  atomic.Int64
	framesIn  atomic.Int64
	framesOut atomic.Int64
	lastSeen  atomic.Int64 // 最近一帧的 UnixNano
	// unknown 使用定长命令集合，限制未知命令审计数据的大小。
	unknownMu sync.Mutex
	unknown   [256]bool
}

// NewSessionAudit 开始跟踪一条连接。remoteAddr 在这里就记下来，
// 因为等到写这一行的时候连接早就没了。
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

// Inbound 记录接收帧及载荷字节数；dataLen 不含帧头。
func (a *SessionAudit) Inbound(dataLen int, at time.Time) {
	if a == nil {
		return
	}
	a.bytesIn.Add(int64(dataLen))
	a.framesIn.Add(1)
	a.lastSeen.Store(at.UnixNano())
}

// Outbound 记录一个发出的帧。
func (a *SessionAudit) Outbound(dataLen int, at time.Time) {
	if a == nil {
		return
	}
	a.bytesOut.Add(int64(dataLen))
	a.framesOut.Add(1)
	a.lastSeen.Store(at.UnixNano())
}

// Unknown 记录收到但未处理的命令集合，用于识别超出实现范围的设备上报。
func (a *SessionAudit) Unknown(command byte) {
	if a == nil {
		return
	}
	a.unknownMu.Lock()
	a.unknown[command] = true
	a.unknownMu.Unlock()
}

// UnknownCommands 按升序返回去重后的未知命令字节。
func (a *SessionAudit) UnknownCommands() []int {
	if a == nil {
		return nil
	}
	a.unknownMu.Lock()
	defer a.unknownMu.Unlock()
	var out []int
	for i, seen := range a.unknown {
		if seen {
			out = append(out, i)
		}
	}
	return out
}

// SessionRecord 是一条连接的终态，随时可以落库。
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

// Snapshot 生成流量快照，endedAt 与结束原因由调用方依据实际连接终态提供。
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
