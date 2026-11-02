package vm

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
)

// Handler serves one guest-initiated vsock connection. It must treat the peer
// as hostile: bound reads, validate input, and return promptly on ctx cancel.
type Handler func(ctx context.Context, conn net.Conn)

// maxUDSPath is the sockaddr_un limit on Linux (108) minus the NUL.
const maxUDSPath = 107

// hub owns one VM's host-side vsock listeners. Firecracker connects a
// guest-initiated vsock connection to port P to the Unix socket "<base>_<P>",
// so each VM has its own listeners and its identity is by construction.
type hub struct {
	base     string
	ctx      context.Context
	cancel   context.CancelFunc
	sem      chan struct{}
	mu       sync.Mutex
	listener map[int]net.Listener
	conns    map[net.Conn]struct{}
	wg       sync.WaitGroup
}

func newHub(base string, maxConns int) *hub {
	ctx, cancel := context.WithCancel(context.Background())
	return &hub{base: base, ctx: ctx, cancel: cancel, sem: make(chan struct{}, maxConns), listener: map[int]net.Listener{}, conns: map[net.Conn]struct{}{}}
}

func (h *hub) listen(port int, handler Handler) error {
	path := fmt.Sprintf("%s_%d", h.base, port)
	if len(path) > maxUDSPath {
		return fmt.Errorf("socket path too long (%d > %d): %s", len(path), maxUDSPath, path)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, dup := h.listener[port]; dup {
		return fmt.Errorf("port %d already has a listener", port)
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	h.listener[port] = ln
	h.wg.Add(1)
	go h.accept(ln, handler)
	return nil
}

func (h *hub) accept(ln net.Listener, handler Handler) {
	defer h.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		select {
		case h.sem <- struct{}{}:
		default: // connection flood: shed load instead of growing without bound
			conn.Close()
			continue
		}
		h.track(conn, true)
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			defer func() { <-h.sem }()
			defer h.track(conn, false)
			defer conn.Close()
			handler(h.ctx, conn)
		}()
	}
}

func (h *hub) track(c net.Conn, add bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if add {
		h.conns[c] = struct{}{}
	} else {
		delete(h.conns, c)
	}
}

// close stops accepting, closes in-flight connections so a handler blocked on
// a hostile guest cannot stall teardown, and waits for handlers to return.
func (h *hub) close() {
	h.cancel()
	h.mu.Lock()
	for _, ln := range h.listener {
		ln.Close()
	}
	for c := range h.conns {
		c.Close()
	}
	h.mu.Unlock()
	h.wg.Wait()
}
