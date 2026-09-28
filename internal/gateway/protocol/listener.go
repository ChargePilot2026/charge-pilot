package protocol

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
)

type Endpoint struct {
	Address string
	Adapter Adapter
}

// Serve binds all protocol ports before accepting any devices. A bind failure
// leaves no partially running listener behind.
func Serve(ctx context.Context, endpoints []Endpoint, sink Sink, maxConnections int) error {
	if len(endpoints) == 0 || sink == nil || maxConnections < 1 {
		return errors.New("invalid protocol listener configuration")
	}
	listeners := make([]net.Listener, 0, len(endpoints))
	seen := make(map[string]bool, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint.Adapter == nil || endpoint.Address == "" || seen[endpoint.Address] {
			closeAll(listeners)
			return errors.New("duplicate address or missing adapter")
		}
		seen[endpoint.Address] = true
		listener, err := net.Listen("tcp", endpoint.Address)
		if err != nil {
			closeAll(listeners)
			return fmt.Errorf("listen %s: %w", endpoint.Address, err)
		}
		listeners = append(listeners, listener)
	}
	defer closeAll(listeners)
	limit := make(chan struct{}, maxConnections)
	errorsOut := make(chan error, len(listeners))
	var acceptWG sync.WaitGroup
	var connWG sync.WaitGroup
	var connMu sync.Mutex
	active := make(map[net.Conn]struct{})
	for i, listener := range listeners {
		adapter := endpoints[i].Adapter
		acceptWG.Add(1)
		go func() {
			defer acceptWG.Done()
			for {
				conn, err := listener.Accept()
				if err != nil {
					if ctx.Err() == nil {
						errorsOut <- fmt.Errorf("accept %s: %w", adapter.Name(), err)
					}
					return
				}
				select {
				case limit <- struct{}{}:
					connMu.Lock()
					active[conn] = struct{}{}
					connMu.Unlock()
					connWG.Add(1)
					go func() {
						defer connWG.Done()
						defer func() {
							_ = conn.Close()
							connMu.Lock()
							delete(active, conn)
							connMu.Unlock()
							<-limit
						}()
						_ = adapter.ServeConn(ctx, conn, sink)
					}()
				default:
					_ = conn.Close()
				}
			}
		}()
	}
	var result error
	select {
	case <-ctx.Done():
		result = ctx.Err()
	case err := <-errorsOut:
		result = err
	}
	closeAll(listeners)
	acceptWG.Wait()
	connMu.Lock()
	for conn := range active {
		_ = conn.Close()
	}
	connMu.Unlock()
	connWG.Wait()
	return result
}

func closeAll(listeners []net.Listener) {
	for _, listener := range listeners {
		_ = listener.Close()
	}
}
