package vm

import (
	"bytes"
	"sync"
)

var panicMarker = []byte("Kernel panic")

// cappedBuffer keeps the first max bytes of console output and remembers
// whether a kernel panic line ever passed through, even past the cap. The
// console is infra diagnostics, never agent evidence.
type cappedBuffer struct {
	mu       sync.Mutex
	max      int
	buf      []byte
	dropped  int
	carry    []byte // tail of the previous write, so a marker split across writes is still seen
	sawPanic bool
}

func newCappedBuffer(max int) *cappedBuffer { return &cappedBuffer{max: max} }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	window := append(append([]byte{}, b.carry...), p...)
	if bytes.Contains(window, panicMarker) {
		b.sawPanic = true
	}
	if keep := len(panicMarker) - 1; len(window) > keep {
		b.carry = append(b.carry[:0], window[len(window)-keep:]...)
	} else {
		b.carry = append(b.carry[:0], window...)
	}
	room := b.max - len(b.buf)
	if room > len(p) {
		room = len(p)
	}
	if room > 0 {
		b.buf = append(b.buf, p[:room]...)
	}
	b.dropped += len(p) - max(room, 0)
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dropped > 0 {
		return string(b.buf) + "\n[console truncated]\n"
	}
	return string(b.buf)
}

func (b *cappedBuffer) panicked() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sawPanic
}
