package protocol

import (
	"context"
	"errors"
	"fmt"
	"log"
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
						// A session that ends in an error used to end in silence:
						// an unknown device, a malformed registration or a payload
						// the board could not read all closed the socket with
						// nothing written anywhere, so the only symptom an operator
						// had was a pile that never came online. The normal end of a
						// session is not an error and is left unlogged; this is the
						// same distinction the board's own silent branch needed.
						if err := adapter.ServeConn(ctx, conn, sink); err != nil && ctx.Err() == nil {
							log.Printf("device session ended on %s: %v", adapter.Name(), err)
						}
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
