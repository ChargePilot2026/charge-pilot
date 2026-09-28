package protocol

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

type testAdapter struct {
	name      string
	connected chan<- string
}

func (a testAdapter) Name() string { return a.name }
func (a testAdapter) ServeConn(ctx context.Context, conn net.Conn, _ Sink) error {
	a.connected <- a.name
	<-ctx.Done()
	return ctx.Err()
}

type testSink struct{}

func (testSink) Register(context.Context, Registration) error { return nil }
func (testSink) Record(context.Context, Event) error          { return nil }

func TestServeMultipleProtocolPorts(t *testing.T) {
	ports := make([]string, 2)
	for i := range ports {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ports[i] = listener.Addr().String()
		_ = listener.Close()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connected := make(chan string, 2)
	result := make(chan error, 1)
	go func() {
		result <- Serve(ctx, []Endpoint{{Address: ports[0], Adapter: testAdapter{"dc589", connected}}, {Address: ports[1], Adapter: testAdapter{"future", connected}}}, testSink{}, 2)
	}()
	var wg sync.WaitGroup
	for _, address := range ports {
		wg.Add(1)
		go func(address string) {
			defer wg.Done()
			var conn net.Conn
			var err error
			for attempt := 0; attempt < 20; attempt++ {
				conn, err = net.DialTimeout("tcp", address, 100*time.Millisecond)
				if err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil {
				t.Errorf("dial %s: %v", address, err)
				return
			}
			defer conn.Close()
		}(address)
	}
	wg.Wait()
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case name := <-connected:
			seen[name] = true
		case <-time.After(time.Second):
			t.Fatalf("only adapters %v accepted connections", seen)
		}
	}
	cancel()
	select {
	case err := <-result:
		if err != context.Canceled {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("listener did not stop")
	}
}
