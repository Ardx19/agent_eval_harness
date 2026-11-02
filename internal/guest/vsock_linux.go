package guest

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

type vsockConn struct{ f *os.File }

func (c *vsockConn) Read(p []byte) (int, error)  { return c.f.Read(p) }
func (c *vsockConn) Write(p []byte) (int, error) { return c.f.Write(p) }
func (c *vsockConn) Close() error                { return c.f.Close() }
func (c *vsockConn) CloseWrite() error           { return unix.Shutdown(int(c.f.Fd()), unix.SHUT_WR) }

// DialVsock connects to the host (CID 2) on a port.
func DialVsock(cid, port uint32) (Conn, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	if err := unix.Connect(fd, &unix.SockaddrVM{CID: cid, Port: port}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("vsock connect: %w", err)
	}
	return &vsockConn{f: os.NewFile(uintptr(fd), "vsock")}, nil
}
