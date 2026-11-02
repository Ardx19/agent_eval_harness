package guest

import (
	"bufio"
	"io"
	"strings"
	"sync"
	"time"

	"agentevalharness/internal/toolproto"
)

// MaxLine caps a relayed line; longer lines are cut and marked. The host refuses a
// control frame larger than 8 MiB, and JSON encoding can expand a line up to six
// times (a control byte becomes \u0001, an invalid UTF-8 byte becomes U+FFFD, three
// bytes), so a line must stay under a sixth of that. A frame the host cannot read
// ends the chain, and with it the exit frame.
const MaxLine = 1 << 20

// Relay sends signed, chained frames to the host. It is safe for concurrent use
// (stdout and stderr readers share one chain).
type Relay struct {
	mu    sync.Mutex
	w     io.Writer
	key   []byte
	start time.Time
	seq   int
	prev  string
	err   error
}

func NewRelay(w io.Writer, key []byte, start time.Time) *Relay {
	return &Relay{w: w, key: key, start: start}
}

// Send appends one frame to the chain. After a write error every later Send is a no-op.
func (r *Relay) Send(f Frame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return
	}
	f.Seq = r.seq
	f.T = time.Since(r.start).Seconds()
	f.Data = strings.ToValidUTF8(f.Data, "�")
	f.Sign(r.key, r.prev)
	if err := toolproto.WriteFrame(r.w, f); err != nil {
		r.err = err
		return
	}
	r.prev, r.seq = f.MAC, r.seq+1
}

func (r *Relay) Line(stream, line string, trunc bool) {
	r.Send(Frame{Stream: stream, Data: line, Trunc: trunc})
}

func (r *Relay) Event(msg string) { r.Send(Frame{Stream: StreamEvent, Data: msg}) }

// ReadLines splits r into lines of at most max bytes and calls emit for each.
// A longer line is cut and reported with trunc set; nothing is silently dropped.
func ReadLines(r io.Reader, max int, emit func(line string, trunc bool)) {
	br := bufio.NewReaderSize(r, 64<<10)
	var cur []byte
	trunc := false
	for {
		chunk, isPrefix, err := br.ReadLine()
		if room := max - len(cur); len(chunk) <= room {
			cur = append(cur, chunk...)
		} else {
			if room > 0 {
				cur = append(cur, chunk[:room]...) // keep what fits; the cut is reported
			}
			trunc = true
		}
		if !isPrefix && (len(chunk) > 0 || err == nil || len(cur) > 0) {
			emit(string(cur), trunc)
			cur, trunc = cur[:0], false
		}
		if err != nil {
			return
		}
	}
}

// ExitAck is the byte the host sends back once it has read the exit frame.
const ExitAck = 0x06

// WaitForAck blocks until the host acknowledges the exit frame, or d passes. The
// supervisor powers the machine off right after the last frame; without this wait the
// final frames can still be in flight when the machine goes, and the host would see a
// run that never reported an exit. The ack comes only after every earlier frame was
// read, so it also drains any backlog.
func WaitForAck(c io.Reader, d time.Duration) bool {
	got := make(chan bool, 1)
	go func() {
		var b [1]byte
		n, _ := c.Read(b[:])
		got <- n == 1 && b[0] == ExitAck
	}()
	select {
	case ok := <-got:
		return ok
	case <-time.After(d):
		return false
	}
}
