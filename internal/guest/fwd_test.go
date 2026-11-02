package guest

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// upstream stands in for the vsock peer: a unix socket server that uppercases
// whatever it reads until the client half-closes, then replies and closes.
func upstream(t *testing.T) func() (Conn, error) {
	t.Helper()
	dir, _ := os.MkdirTemp("/tmp", "evh-fwd-")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "up")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				b, _ := io.ReadAll(c) // returns at client half-close
				c.Write([]byte(strings.ToUpper(string(b))))
			}()
		}
	}()
	return func() (Conn, error) {
		c, err := net.Dial("unix", sock)
		if err != nil {
			return nil, err
		}
		return c.(*net.UnixConn), nil
	}
}

func startForward(t *testing.T, dial func() (Conn, error)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Forward(ctx, l, dial); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Forward did not stop after cancel")
		}
	})
	return l.Addr().String()
}

func TestForwardRelaysBothDirectionsWithHalfClose(t *testing.T) {
	addr := startForward(t, upstream(t))
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("hello model"))
	c.(*net.TCPConn).CloseWrite()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "HELLO MODEL" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestForwardHandlesConcurrentConnections(t *testing.T) {
	addr := startForward(t, upstream(t))
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			c, err := net.Dial("tcp", addr)
			if err != nil {
				errs <- err
				return
			}
			defer c.Close()
			c.Write([]byte("abc"))
			c.(*net.TCPConn).CloseWrite()
			b, _ := io.ReadAll(c)
			if string(b) != "ABC" {
				errs <- io.ErrUnexpectedEOF
				return
			}
			errs <- nil
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestForwardClosesWhenUpstreamUnavailable(t *testing.T) {
	addr := startForward(t, func() (Conn, error) { return nil, io.ErrClosedPipe })
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadAll(c); err != nil {
		t.Fatalf("the client should see a clean close, not hang: %v", err)
	}
}
