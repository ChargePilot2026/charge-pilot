package protocol

import (
	"sync"
	"sync/atomic"
	"time"
)

// Transport 标识连接的种类。这些取值恰好就是
// gateway_db.device_session.protocol 允许的那些（tcp 与 mqtt 两个值的 ENUM），
// 刻意取传输方式而不是厂商适配器名：这一列回答的是"这台设备是怎么连上来的"，
// 而厂商信息从 device 行就能取到。
// 这里写 "dc589" 这类适配器名，MySQL 会直接拒绝。
type Transport string

const (
	TransportTCP  Transport = "tcp"
	TransportMQTT Transport = "mqtt"
)

// SessionAudit 累加每条连接的流量计数器，这样一条会话只要在连接结束时
// 写一次库就够了。
//
// 这些计数器刻意留在内存里，只在 detach 时落盘。
// 每帧都写会把一次数据库往返放到 TCP 热路径上，
// 让一台疯狂发帧的设备有能力把自己的连接卡死。
// 进程死掉时仍在累加的计数器会丢，这是有意做的取舍：
// 审计要回答的是"这条连接挂了多久、搬了多少数据"，
// 而丢一行是可以补救的，卡住的 socket 不是。
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
	// unknown 是一小撮本版本选择不去处理的命令字节。
	// 它是定长而不是一个会增长的 map，这样一台狂发未知命令的设备
	// 就不能借此让自己的会话行无限膨胀下去。
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

// Inbound 记录一个收到的帧。dataLen 是载荷长度，不是成帧之后的长度，
// 这样审计统计的才是协议实际承载的量。
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

// Unknown 记录"某条命令收到了但被有意忽略"这件事。
//
// 这里记的是集合而不是计数，这样运维才能把"这块板子讲的是
// 我们实现范围的超集"和"这块板子一切正常"区分开。
// 正是这个区别，让一条被跳过的命令从无头案变成待办事项。
func (a *SessionAudit) Unknown(command byte) {
	if a == nil {
		return
	}
	a.unknownMu.Lock()
	a.unknown[command] = true
	a.unknownMu.Unlock()
}

// UnknownCommands 列出被跳过的那些互不相同的命令字节，
// 按升序返回，这样多次运行之间取值是稳定的。
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

// Snapshot 冻结计数器。endedAt 由调用方传入而不是在这里取当前时间，
// 这样调用方可以给这一行标上它关闭的原因，
// 这个值也与连接实际返回的错误对得上。
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
