package guest

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"agentevalharness/internal/toolproto"
)

func collect(t *testing.T, input string, max int) (lines []string, truncs []bool) {
	t.Helper()
	ReadLines(strings.NewReader(input), max, func(l string, tr bool) { lines = append(lines, l); truncs = append(truncs, tr) })
	return
}

func TestReadLinesSplitsAndKeepsEverything(t *testing.T) {
	lines, _ := collect(t, "a\nb\n\nc", 100)
	if strings.Join(lines, "|") != "a|b||c" {
		t.Fatalf("%q", lines)
	}
	lines, _ = collect(t, "last line without newline", 100)
	if len(lines) != 1 || lines[0] != "last line without newline" {
		t.Fatalf("a final line with no newline must still be delivered: %q", lines)
	}
	lines, _ = collect(t, "", 100)
	if len(lines) != 0 {
		t.Fatalf("%q", lines)
	}
}

func TestReadLinesCutsAndMarksLongLines(t *testing.T) {
	lines, truncs := collect(t, strings.Repeat("x", 100)+"\nshort\n", 10)
	if len(lines) != 2 || len(lines[0]) != 10 || !truncs[0] || lines[1] != "short" || truncs[1] {
		t.Fatalf("an over-long line is cut and marked, and the next line is unaffected: %q %v", lines, truncs)
	}
}

func TestRelayChainsFramesAndFixesSequence(t *testing.T) {
	var buf bytes.Buffer
	key := []byte("control-key-0123456789abcdef0123")
	r := NewRelay(&buf, key, time.Now())
	r.Line("stdout", "one", false)
	r.Line("stderr", "two", false)
	r.Send(Frame{Stream: StreamExit, Code: 3})
	prev := ""
	for i := 0; i < 3; i++ {
		var f Frame
		if err := toolproto.ReadFrame(&buf, &f, 1<<20); err != nil {
			t.Fatal(err)
		}
		if f.Seq != i || !f.Verify(key, prev) {
			t.Fatalf("frame %d: seq=%d verifies=%v", i, f.Seq, f.Verify(key, prev))
		}
		prev = f.MAC
	}
}

func TestRelayReplacesInvalidUTF8(t *testing.T) {
	var buf bytes.Buffer
	r := NewRelay(&buf, []byte("k"), time.Now())
	r.Line("stdout", "ok \xff\xfe bytes", false)
	var f Frame
	if err := toolproto.ReadFrame(&buf, &f, 1<<20); err != nil || !strings.Contains(f.Data, "�") {
		t.Fatalf("binary output must survive JSON framing: %v %q", err, f.Data)
	}
}

type failingWriter struct{ n int }

func (w *failingWriter) Write(p []byte) (int, error) {
	w.n++
	return 0, bytes.ErrTooLarge
}

func TestRelayStopsAfterAWriteError(t *testing.T) {
	w := &failingWriter{}
	r := NewRelay(w, []byte("k"), time.Now())
	r.Line("stdout", "a", false)
	r.Line("stdout", "b", false)
	if w.n != 1 {
		t.Fatalf("after a failure the relay must stop writing, wrote %d times", w.n)
	}
}

// Whatever the guest relays must fit the host's frame limit once encoded, even for
// the worst bytes: a line of control characters and one of invalid UTF-8.
func TestWorstCaseLineFitsTheHostFrameLimit(t *testing.T) {
	const hostLimit = 8<<20 + 4096 // agents.maxFrameData + 4096
	for name, b := range map[string]byte{"control characters": 0x01, "invalid UTF-8": 0xff, "quotes": '"', "backslashes": '\\'} {
		var emitted []string
		ReadLines(strings.NewReader(strings.Repeat(string([]byte{b}), 3*MaxLine)+"\n"), MaxLine, func(l string, _ bool) { emitted = append(emitted, l) })
		if len(emitted) == 0 {
			t.Fatalf("%s: nothing emitted", name)
		}
		var buf bytes.Buffer
		r := NewRelay(&buf, []byte("k"), time.Now())
		r.Line(StreamStdout, emitted[0], true)
		if buf.Len() >= hostLimit {
			t.Errorf("%s: a maximal line encodes to %d bytes, over the host's %d byte frame limit", name, buf.Len(), hostLimit)
		}
	}
}

func TestWaitForAck(t *testing.T) {
	r, w := io.Pipe()
	go func() { w.Write([]byte{ExitAck}) }()
	if !WaitForAck(r, time.Second) {
		t.Fatal("an ack byte must be recognised")
	}
	r2, w2 := io.Pipe()
	go func() { w2.Write([]byte{0x00}) }()
	if WaitForAck(r2, time.Second) {
		t.Fatal("any other byte is not an ack")
	}
	r3, _ := io.Pipe()
	start := time.Now()
	if WaitForAck(r3, 150*time.Millisecond) || time.Since(start) > time.Second {
		t.Fatal("a host that never answers must not hold the machine up for long")
	}
}
