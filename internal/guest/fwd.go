// Package guest holds code that runs inside the microVM.
package guest

import (
	"context"
	"io"
	"net"
	"sync"
)

// Conn is a stream connection that supports half-close.
type Conn interface {
	io.ReadWriteCloser
	CloseWrite() error
}

const maxRelays = 32

// Forward accepts connections on l and relays each to a fresh connection from
// dial (vsock to the host proxy in production). Agents keep a plain
// http://127.0.0.1:PORT base URL while the guest has no network device.
func Forward(ctx context.Context, l net.Listener, dial func() (Conn, error)) error {
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	sem := make(chan struct{}, maxRelays)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case sem <- struct{}{}:
		default:
			c.Close() // too many concurrent connections: shed load
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			up, err := dial()
			if err != nil {
				c.Close()
				return
			}
			relay(c, up)
		}()
	}
}

type closeWriter interface{ CloseWrite() error }

func relay(a net.Conn, b Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(b, a)
		b.CloseWrite()
	}()
	go func() {
		defer wg.Done()
		io.Copy(a, b)
		if cw, ok := a.(closeWriter); ok {
			cw.CloseWrite()
		}
	}()
	wg.Wait()
	a.Close()
	b.Close()
}
