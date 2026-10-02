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

// Serve 在接收任何设备之前先把所有协议端口都绑定好。任何一个绑定失败
// 都不会留下半启动状态的监听器。
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
		log.Printf("device listener ready protocol=%s address=%s", endpoint.Adapter.Name(), listener.Addr())
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
		acceptWG.Go(func() {
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
					connWG.Go(func() {
						defer func() {
							_ = conn.Close()
							connMu.Lock()
							delete(active, conn)
							connMu.Unlock()
							<-limit
						}()
						// 记录注册、协议解码等异常会话的结束原因；正常断开不记为错误。
						if err := adapter.ServeConn(ctx, conn, sink); err != nil && ctx.Err() == nil {
							log.Printf("device session ended on %s: %v", adapter.Name(), err)
						}
					})
				default:
					log.Printf("device connection rejected protocol=%s remote=%s reason=connection_limit", adapter.Name(), conn.RemoteAddr())
					_ = conn.Close()
				}
			}
		})
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
