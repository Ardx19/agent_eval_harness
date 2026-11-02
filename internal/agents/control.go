// Package agents launches an agent in a microVM and turns what happened into a trace.
package agents

import (
	"context"
	"net"
	"sync"
	"time"

	"agentevalharness/internal/guest"
	"agentevalharness/internal/toolproto"
)

// controlIdleTimeout is how long the host waits for the next frame. A stuck agent can
// be silent for many minutes and its final exit frame must still be read, so this is
// far above any run limit; the guest's own idle watchdog ends stuck runs.
const controlIdleTimeout = 3 * time.Hour

const (
	maxFrames    = 2_000_000
	maxFrameData = 8 << 20
	maxTotalData = 256 << 20
)

// Control receives the guest supervisor's signed frame chain (vsock port 5002).
// It is a vm.Handler. The guest is hostile: frames that do not extend the signed
// chain are dropped and counted, and nothing the guest sends can make it grow
// without bound.
type Control struct {
	key []byte

	mu       sync.Mutex
	frames   []guest.Frame
	lastMAC  string
	nextSeq  int
	total    int
	rejected int
	capped   bool
	exited   bool
}

func NewControl(key []byte) *Control { return &Control{key: key} }

func (c *Control) ServeConn(ctx context.Context, conn net.Conn) {
	for {
		_ = conn.SetReadDeadline(time.Now().Add(controlIdleTimeout))
		var f guest.Frame
		if err := toolproto.ReadFrame(conn, &f, maxFrameData+4096); err != nil {
			return
		}
		if !c.accept(f) {
			// a bad signature means something other than the supervisor wrote here
			continue
		}
		if f.Stream == guest.StreamExit {
			// every earlier frame has been read: tell the supervisor it may power off
			_, _ = conn.Write([]byte{guest.ExitAck})
			return
		}
	}
}

func (c *Control) accept(f guest.Frame) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.exited || c.capped {
		c.rejected++
		return false
	}
	if f.Seq != c.nextSeq || !f.Verify(c.key, c.lastMAC) || len(f.Data) > maxFrameData {
		c.rejected++
		return false
	}
	c.total += len(f.Data)
	if len(c.frames) >= maxFrames || c.total > maxTotalData {
		c.capped = true
		return false
	}
	c.frames = append(c.frames, f)
	c.lastMAC = f.MAC
	c.nextSeq++
	if f.Stream == guest.StreamExit {
		c.exited = true
	}
	return true
}

// Lines returns the data of every accepted frame on a stream, in order.
func (c *Control) Lines(stream string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, f := range c.frames {
		if f.Stream == stream {
			out = append(out, f.Data)
		}
	}
	return out
}

// Exit returns the supervisor's exit frame, or nil if the chain never ended
// (the guest died, or the run was cut off).
func (c *Control) Exit() *guest.Frame {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.exited {
		return nil
	}
	f := c.frames[len(c.frames)-1]
	return &f
}

// Rejected counts frames dropped for a bad signature, a gap, or a flood.
func (c *Control) Rejected() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rejected
}

// Capped reports that the guest sent more than the caps allow.
func (c *Control) Capped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.capped
}

// Frames returns a copy of the accepted chain.
func (c *Control) Frames() []guest.Frame {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]guest.Frame(nil), c.frames...)
}
