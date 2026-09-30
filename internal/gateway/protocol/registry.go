package protocol

import (
	"context"
	"errors"
	"sync"
)

var ErrOffline = errors.New("device is not connected")

type CommandKind string

const (
	CommandStart  CommandKind = "start"
	CommandStop   CommandKind = "stop"
	CommandReboot CommandKind = "reboot"
	CommandOTA    CommandKind = "ota"
)

// Command 刻意与厂商无关。领域层必须先把一条指令落库并鉴权，
// 然后才能让当前那条连接去发送它。
type Command struct {
	Kind         CommandKind
	SessionID    [6]byte
	Port         uint8
	OrderBCD     [8]byte
	Mode         uint8
	Quantity     uint16
	RemoteMode   uint8
	UseUpgradeID bool
	UpgradeID    [8]byte
}

type Session interface {
	Send(context.Context, Command) error
	Close() error
}

// Registry 把命令路由到设备当前那条已认证的连接。
// 一次新的登录会顶替掉上一个会话，而迟到的断开回调
// 不会把这条被顶替的会话也一起摘掉。
type Registry struct {
	mu       sync.RWMutex
	sessions map[string]*registryEntry
}

type registryEntry struct{ session Session }

// Attach 把 session 装成 deviceID 当前的连接，并返回一个 detach 函数。
// 如果该设备已经登记过一个会话，它会被关闭，
// 并连同 onReplaced 一起交给调用方，好让调用方记录下
// 旧连接结束是因为来了更新的登录，
// 而不是因为设备自己挂断。
//
// onReplaced 可以为 nil。
func (r *Registry) Attach(deviceID string, session Session, onReplaced func(Session)) func() {
	r.mu.Lock()
	if r.sessions == nil {
		r.sessions = make(map[string]*registryEntry)
	}
	previous := r.sessions[deviceID]
	current := &registryEntry{session: session}
	r.sessions[deviceID] = current
	r.mu.Unlock()
	if previous != nil {
		if onReplaced != nil {
			onReplaced(previous.session)
		}
		_ = previous.session.Close()
	}
	return func() {
		r.mu.Lock()
		if r.sessions[deviceID] == current {
			delete(r.sessions, deviceID)
		}
		r.mu.Unlock()
	}
}

func (r *Registry) Send(ctx context.Context, deviceID string, command Command) error {
	r.mu.RLock()
	entry := r.sessions[deviceID]
	r.mu.RUnlock()
	if entry == nil {
		return ErrOffline
	}
	return entry.session.Send(ctx, command)
}

func (r *Registry) Connected(deviceID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessions[deviceID] != nil
}
