package agents

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"agentevalharness/internal/guest"
	"agentevalharness/internal/toolproto"
)

var ctlKey = []byte("control-key-0123456789abcdef0123")

type chain struct {
	prev string
	seq  int
}

func (c *chain) frame(stream, data string) guest.Frame {
	f := guest.Frame{Seq: c.seq, Stream: stream, Data: data}
	f.Sign(ctlKey, c.prev)
	c.prev, c.seq = f.MAC, c.seq+1
	return f
}

func feed(t *testing.T, ctl *Control, frames ...guest.Frame) {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		ctl.ServeConn(context.Background(), server)
		server.Close() // what the VM hub does when a handler returns; unblocks a writer still sending
		close(done)
	}()
	go io.Copy(io.Discard, client) // the host acknowledges the exit frame; the pipe is synchronous, so read it
	for _, f := range frames {
		if err := toolproto.WriteFrame(client, f); err != nil {
			break // after the exit frame the host stops reading and closes: later frames fail to send
		}
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ServeConn did not return")
	}
}

func TestChainIsAcceptedInOrder(t *testing.T) {
	ctl := NewControl(ctlKey)
	var c chain
	feed(t, ctl, c.frame("stdout", "a"), c.frame("stderr", "e"), c.frame("stdout", "b"), c.frame("exit", ""))
	if got := strings.Join(ctl.Lines("stdout"), ","); got != "a,b" {
		t.Fatalf("%q", got)
	}
	if ctl.Exit() == nil || ctl.Rejected() != 0 {
		t.Fatalf("exit=%v rejected=%d", ctl.Exit(), ctl.Rejected())
	}
}

func TestForgedFrameIsDropped(t *testing.T) {
	ctl := NewControl(ctlKey)
	var c chain
	forged := guest.Frame{Seq: 1, Stream: "stdout", Data: `{"type":"tool_execution_end"}`}
	forged.Sign([]byte("a key the agent invented......"), "")
	feed(t, ctl, c.frame("stdout", "real-0"), forged)
	if len(ctl.Lines("stdout")) != 1 || ctl.Rejected() != 1 {
		t.Fatalf("a frame without the key must not enter the stream: %v rejected=%d", ctl.Lines("stdout"), ctl.Rejected())
	}
}

func TestDroppedReorderedAndReplayedFramesBreakTheChain(t *testing.T) {
	var c chain
	f0, f1, f2 := c.frame("stdout", "0"), c.frame("stdout", "1"), c.frame("stdout", "2")
	cases := map[string][]guest.Frame{
		"a dropped frame":  {f0, f2},
		"swapped frames":   {f0, f2, f1},
		"a replayed frame": {f0, f1, f1},
		"starts late":      {f1},
	}
	for name, frames := range cases {
		ctl := NewControl(ctlKey)
		feed(t, ctl, frames...)
		if ctl.Rejected() == 0 {
			t.Errorf("%s must be detected", name)
		}
		if name == "a dropped frame" && len(ctl.Lines("stdout")) != 1 {
			t.Errorf("only the frame before the gap may be accepted: %v", ctl.Lines("stdout"))
		}
	}
}

func TestTamperedDataIsRejected(t *testing.T) {
	ctl := NewControl(ctlKey)
	var c chain
	f := c.frame("stdout", "honest")
	f.Data = "edited"
	feed(t, ctl, f)
	if len(ctl.Lines("stdout")) != 0 || ctl.Rejected() != 1 {
		t.Fatal("editing a signed frame must invalidate it")
	}
}

func TestNothingIsAcceptedAfterExit(t *testing.T) {
	ctl := NewControl(ctlKey)
	var c chain
	feed(t, ctl, c.frame("stdout", "a"), c.frame("exit", ""), c.frame("stdout", "late"))
	if strings.Contains(strings.Join(ctl.Lines("stdout"), ","), "late") {
		t.Fatal("the exit frame ends the chain")
	}
}

func TestNoExitFrameMeansTheRunWasCutOff(t *testing.T) {
	ctl := NewControl(ctlKey)
	var c chain
	feed(t, ctl, c.frame("stdout", "a"))
	if ctl.Exit() != nil {
		t.Fatal("without an exit frame the run did not end cleanly")
	}
}

func TestHostileSizesAreBounded(t *testing.T) {
	ctl := NewControl(ctlKey)
	var c chain
	huge := c.frame("stdout", strings.Repeat("x", maxFrameData+1))
	feed(t, ctl, huge)
	if len(ctl.Lines("stdout")) != 0 || ctl.Rejected() == 0 {
		t.Fatal("an oversized frame must be refused")
	}
}

func TestFrameMACCoversEveryField(t *testing.T) {
	base := guest.Frame{Seq: 3, Stream: "exit", Data: "d", Code: 1, Signal: "killed", Reason: "timeout", Trunc: true}
	base.Sign(ctlKey, "prev")
	muts := map[string]func(*guest.Frame){
		"seq": func(f *guest.Frame) { f.Seq++ }, "stream": func(f *guest.Frame) { f.Stream = "stdout" },
		"data": func(f *guest.Frame) { f.Data = "x" }, "code": func(f *guest.Frame) { f.Code = 0 },
		"signal": func(f *guest.Frame) { f.Signal = "" }, "reason": func(f *guest.Frame) { f.Reason = "" },
		"trunc": func(f *guest.Frame) { f.Trunc = false },
	}
	for name, m := range muts {
		f := base
		m(&f)
		if f.Verify(ctlKey, "prev") {
			t.Errorf("changing %s must break the signature", name)
		}
	}
	if base.Verify(ctlKey, "different-prev") {
		t.Error("the previous signature is part of the chain")
	}
	if !base.Verify(ctlKey, "prev") {
		t.Error("unmodified frame must verify")
	}
}

// The supervisor's relay and the host's control receiver must agree end to end.
func TestRelayAndControlAgree(t *testing.T) {
	ctl := NewControl(ctlKey)
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { ctl.ServeConn(context.Background(), server); close(done) }()
	r := guest.NewRelay(client, ctlKey, time.Now())
	r.Event("supervisor started")
	r.Line("stdout", `{"type":"agent_start"}`, false)
	r.Line("stderr", "warn", false)
	r.Send(guest.Frame{Stream: guest.StreamExit, Code: 0})
	client.Close()
	<-done
	if ex := ctl.Exit(); ex == nil || ctl.Rejected() != 0 || len(ctl.Lines("stdout")) != 1 {
		t.Fatalf("exit=%v rejected=%d stdout=%v", ex, ctl.Rejected(), ctl.Lines("stdout"))
	}
}

// The supervisor waits for this byte before powering the machine off, so the exit
// frame is never lost to a race with shutdown.
func TestControlAcknowledgesTheExitFrame(t *testing.T) {
	key := []byte("k")
	ctl := NewControl(key)
	host, guestSide := net.Pipe()
	defer guestSide.Close()
	done := make(chan struct{})
	go func() { ctl.ServeConn(context.Background(), host); close(done) }()

	send := func(f guest.Frame, prev string) string {
		f.Sign(key, prev)
		if err := toolproto.WriteFrame(guestSide, f); err != nil {
			t.Fatal(err)
		}
		return f.MAC
	}
	mac := send(guest.Frame{Seq: 0, Stream: guest.StreamStdout, Data: "hello"}, "")
	// no acknowledgement for an ordinary frame
	guestSide.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if n, _ := guestSide.Read(make([]byte, 1)); n != 0 {
		t.Fatal("only the exit frame is acknowledged")
	}
	guestSide.SetReadDeadline(time.Now().Add(2 * time.Second))
	go func() {
		f := guest.Frame{Seq: 1, Stream: guest.StreamExit, Code: 0}
		f.Sign(key, mac)
		_ = toolproto.WriteFrame(guestSide, f)
	}()
	b := make([]byte, 1)
	if n, err := guestSide.Read(b); n != 1 || b[0] != guest.ExitAck || err != nil {
		t.Fatalf("expected the ack byte after the exit frame: %d %v %v", n, b, err)
	}
	<-done
	if ctl.Exit() == nil {
		t.Fatal("the exit frame must have been recorded before the ack")
	}
}
