package protocol

import (
	"context"
	"errors"
	"sync"
)

var ErrOffline = errors.New("device is not connected")

type CommandKind string

const (
	CommandStart       CommandKind = "start"
	CommandStop        CommandKind = "stop"
	CommandReboot      CommandKind = "reboot"
	CommandCardDenied  CommandKind = "card_denied"
	CommandCardBalance CommandKind = "card_balance"
)

// Command 表示与厂商协议无关的命令，须由领域层持久化并鉴权后发送。
type Command struct {
	ConsumerType     uint8
	CardNumber       uint32
	CardBalanceUnits uint16
	CardInvalid      bool
	Kind             CommandKind
	SessionID        [6]byte
	Port             uint8
	OrderBCD         [8]byte
	Mode             uint8
	Quantity         uint16
}

type Session interface {
	Send(context.Context, Command) error
	Close() error
}

// Registry 将命令路由至设备当前已认证的连接。
// 新连接替换旧会话；旧会话的延迟断开回调不会移除新连接。
type Registry struct {
	mu       sync.RWMutex
	sessions map[string]*registryEntry
}

type registryEntry struct{ session Session }

// Attach 登记设备当前会话并返回 detach 函数。
// 新会话替换并关闭同设备的旧连接，通过可选 onReplaced 回调记录替换原因。
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
