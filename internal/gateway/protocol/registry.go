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

// Command is intentionally vendor-neutral. The domain layer must persist and
// authorize an instruction before asking the active connection to send it.
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

// Registry routes commands to the current authenticated device connection.
// A new login replaces the prior session, and stale disconnects cannot remove
// the replacement.
type Registry struct {
	mu       sync.RWMutex
	sessions map[string]*registryEntry
}

type registryEntry struct{ session Session }

func (r *Registry) Attach(deviceID string, session Session) func() {
	r.mu.Lock()
	if r.sessions == nil {
		r.sessions = make(map[string]*registryEntry)
	}
	previous := r.sessions[deviceID]
	current := &registryEntry{session: session}
	r.sessions[deviceID] = current
	r.mu.Unlock()
	if previous != nil {
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
