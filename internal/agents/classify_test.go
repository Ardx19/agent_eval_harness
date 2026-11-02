package agents

import (
	"testing"
	"time"

	"agentevalharness/internal/guest"
	"agentevalharness/internal/proxy"
	"agentevalharness/internal/sandbox"
	"agentevalharness/internal/skills"
	"agentevalharness/internal/trace"
	"agentevalharness/internal/vm"
)

// A tool the kernel killed gave the agent nothing to work with. That is the
// harness's problem and must not count against the agent.
func TestAToolKilledByTheKernelIsAnInfraError(t *testing.T) {
	tr := &trace.Trace{}
	run := &Run{Records: []skills.CallRecord{{Tool: "retrosynthesis", Result: skills.Result{ExitCode: 137, OOMSuspect: true}}}}
	classify(tr, run, sandbox.Result{Exit: vm.ExitOK}, NewControl([]byte("k")), nil, nil, nil)
	if tr.Status != "infra_error" || tr.Failure == nil || tr.Failure.Attribution != "infra" || tr.Failure.Reason != "tool_oom" {
		t.Fatalf("%s %+v", tr.Status, tr.Failure)
	}
}

func TestATimeoutWeCausedIsNotAnOOM(t *testing.T) {
	tr := &trace.Trace{}
	run := &Run{Records: []skills.CallRecord{{Tool: "t", Result: skills.Result{ExitCode: 137, TimedOut: true}}}}
	classify(tr, run, sandbox.Result{Exit: vm.ExitTimeout}, NewControl([]byte("k")), nil, nil, nil)
	if tr.Status != "timeout" {
		t.Fatalf("%s", tr.Status)
	}
}

func exitedControl(t *testing.T, reason string) *Control {
	t.Helper()
	key := []byte("k")
	ctl := NewControl(key)
	f := guest.Frame{Seq: 0, Stream: guest.StreamExit, Code: 137, Reason: reason}
	f.Sign(key, "")
	if !ctl.accept(f) {
		t.Fatal("test frame rejected")
	}
	return ctl
}

func testProxy(t *testing.T) *proxy.Proxy {
	t.Helper()
	rec, err := proxy.NewRecorder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	px, err := proxy.New(proxy.Config{Key: "k", Recorder: rec})
	if err != nil {
		t.Fatal(err)
	}
	return px
}

// An agent that went silent and was ended by the guest watchdog is a stuck agent,
// unless the model API was refusing it, which is the harness's problem.
func TestIdleAgentIsATimeoutUnlessTheAPIRefusedIt(t *testing.T) {
	tr := &trace.Trace{}
	classify(tr, &Run{}, sandbox.Result{Exit: vm.ExitOK}, exitedControl(t, "idle"), testProxy(t), nil, nil)
	if tr.Status != "timeout" || tr.Failure == nil || tr.Failure.Reason != "idle" || tr.Failure.Attribution != "agent" {
		t.Fatalf("%s %+v", tr.Status, tr.Failure)
	}
	tr = &trace.Trace{}
	run := &Run{Exchange: []proxy.Exchange{{Status: 429, Infra: "rate_limited"}}}
	classify(tr, run, sandbox.Result{Exit: vm.ExitOK}, exitedControl(t, "idle"), testProxy(t), nil, nil)
	if tr.Status != "infra_error" || tr.Failure.Reason != "upstream" {
		t.Fatalf("a stall behind a refused model call must be infrastructure: %s %+v", tr.Status, tr.Failure)
	}
}

// The final frame of a silent run is read: the host must not drop the control
// channel after a quarter of an hour of silence, which is how a stuck agent's exit
// frame used to be lost.
func TestControlChannelOutlivesALongSilence(t *testing.T) {
	if controlIdleTimeout < 2*time.Hour {
		t.Fatalf("idle timeout %v is shorter than a long run", controlIdleTimeout)
	}
}

// A guest that never reports an exit (cut off, crashed before its final frame) is
// infrastructure: nothing the agent did ended the run.
func TestLostExitFrameIsInfra(t *testing.T) {
	tr := &trace.Trace{}
	classify(tr, &Run{}, sandbox.Result{Exit: vm.ExitOK}, NewControl([]byte("k")), testProxy(t), nil, nil)
	if tr.Status != "infra_error" || tr.Failure == nil || tr.Failure.Reason != "no_exit_frame" || tr.Failure.Attribution != "infra" {
		t.Fatalf("%s %+v", tr.Status, tr.Failure)
	}
}

// The agent waited on a model request that never completed, and the idle watchdog
// ended the run: the stall belongs to the model API, not the agent.
func TestIdleWhileAModelRequestHangsIsInfra(t *testing.T) {
	rec, err := proxy.NewRecorder("")
	if err != nil {
		t.Fatal(err)
	}
	px, err := proxy.New(proxy.Config{Key: "k", Recorder: rec})
	if err != nil {
		t.Fatal(err)
	}
	rec.Begin() // a request that never finishes
	tr := &trace.Trace{}
	classify(tr, &Run{}, sandbox.Result{Exit: vm.ExitOK}, exitedControl(t, "idle"), px, nil, nil)
	if tr.Status != "infra_error" || tr.Failure == nil || tr.Failure.Reason != "upstream_hang" || tr.Failure.Attribution != "infra" {
		t.Fatalf("%s %+v", tr.Status, tr.Failure)
	}
}

// Once every model request has finished, a silent agent is still the agent's timeout.
func TestIdleAfterModelRequestsFinishedIsAgentTimeout(t *testing.T) {
	rec, err := proxy.NewRecorder("")
	if err != nil {
		t.Fatal(err)
	}
	px, err := proxy.New(proxy.Config{Key: "k", Recorder: rec})
	if err != nil {
		t.Fatal(err)
	}
	idx := rec.Begin()
	rec.Finish(proxy.Exchange{Index: idx}, nil, nil)
	tr := &trace.Trace{}
	classify(tr, &Run{}, sandbox.Result{Exit: vm.ExitOK}, exitedControl(t, "idle"), px, nil, nil)
	if tr.Status != "timeout" || tr.Failure == nil || tr.Failure.Reason != "idle" || tr.Failure.Attribution != "agent" {
		t.Fatalf("%s %+v", tr.Status, tr.Failure)
	}
}
