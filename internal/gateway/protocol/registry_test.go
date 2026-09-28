package protocol

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type fakeSession struct {
	closed atomic.Bool
	sent   atomic.Int32
}

func (f *fakeSession) Send(context.Context, Command) error {
	if f.closed.Load() {
		return errors.New("closed")
	}
	f.sent.Add(1)
	return nil
}
func (f *fakeSession) Close() error { f.closed.Store(true); return nil }

func TestReconnectRetiresOldSessionWithoutLosingReplacement(t *testing.T) {
	registry := &Registry{}
	old := &fakeSession{}
	detachOld := registry.Attach("board", old)
	current := &fakeSession{}
	detachCurrent := registry.Attach("board", current)
	if !old.closed.Load() {
		t.Fatal("previous connection stayed open")
	}
	detachOld()
	if err := registry.Send(context.Background(), "board", Command{Kind: CommandStop}); err != nil || current.sent.Load() != 1 {
		t.Fatalf("replacement lost: sent=%d err=%v", current.sent.Load(), err)
	}
	detachCurrent()
	if err := registry.Send(context.Background(), "board", Command{Kind: CommandStop}); !errors.Is(err, ErrOffline) {
		t.Fatalf("detached connection still routable: %v", err)
	}
}
