package skills

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"agentevalharness/internal/toolproto"
)

var testKey = []byte("per-trial-key-0123456789abcdef!!")

// fakeRunner stands in for the real tool and captures what the toolbox hands it.
type fakeRunner struct {
	mu     sync.Mutex
	calls  []Request
	files  map[string]string // file argument content as seen at call time
	result Result
	err    error
	delay  time.Duration
}

func (f *fakeRunner) Name() string { return "fake" }
func (f *fakeRunner) Run(_ context.Context, r Request) (Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, r)
	if f.files == nil {
		f.files = map[string]string{}
	}
	for i, a := range r.Args {
		if a == "--request" && i+1 < len(r.Args) {
			if b, err := os.ReadFile(r.Args[i+1]); err == nil {
				f.files[r.Args[i+1]] = string(b)
			}
		}
	}
	f.mu.Unlock()
	time.Sleep(f.delay)
	res := f.result
	if res.Stdout == nil {
		res.Stdout = []byte(`{"ok": true}`)
	}
	return res, f.err
}

func (f *fakeRunner) n() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

func newBox(t *testing.T, r *fakeRunner) (*Toolbox, *Manifest) {
	t.Helper()
	m := loadRetro(t)
	tb, err := NewToolbox(testKey, &Tool{Manifest: m, Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	return tb, m
}

var ctr uint64

func signed(argv0 string, args []string, stdin string, files map[string][]byte) *toolproto.Request {
	ctr++
	r := &toolproto.Request{V: 1, Counter: ctr, Nonce: fmt.Sprintf("nonce-%d", ctr), PID: 100 + int(ctr), PPID: 50,
		Argv0: argv0, Args: args, Stdin: []byte(stdin), Files: files}
	r.Sign(testKey)
	return r
}

func send(t *testing.T, tb *Toolbox, req *toolproto.Request) *toolproto.Response {
	t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() { tb.ServeConn(context.Background(), server); server.Close(); close(done) }()
	if err := toolproto.WriteFrame(client, req); err != nil {
		t.Fatal(err)
	}
	var resp toolproto.Response
	err := toolproto.ReadFrame(client, &resp, toolproto.MaxFrame)
	<-done
	if err != nil {
		return nil // the toolbox closed without answering
	}
	return &resp
}

const retro = "run_retrosynthesis.sh"

func TestAcceptedCallIsRunAndRecorded(t *testing.T) {
	fr := &fakeRunner{result: Result{ExitCode: 0, Stdout: []byte(`{"ok": true}`), StdoutSHA256: "h", Duration: 2 * time.Second, UserCPU: time.Second, PeakMemMB: 812}}
	tb, _ := newBox(t, fr)
	resp := send(t, tb, signed(retro, []string{}, `{"mode":"list_skills"}`, nil))
	if resp == nil || !resp.OK || string(resp.Stdout) != `{"ok": true}` || resp.CallID != "tb1" {
		t.Fatalf("%+v", resp)
	}
	recs := tb.Records()
	if len(recs) != 1 {
		t.Fatalf("%d records", len(recs))
	}
	r := recs[0]
	if r.Tool != "retrosynthesis" || r.PPID != 50 || !r.ArgsValid || r.Result.PeakMemMB != 812 || r.TStart < 0 {
		t.Fatalf("%+v", r)
	}
	if h := r.ToTrace(); !h.ReceiptValid || h.CPUS != 1 || h.CallID != "tb1" || h.PPID != 50 {
		t.Fatalf("%+v", h)
	}
}

func TestForgedSignatureIsRefusedAndRecorded(t *testing.T) {
	fr := &fakeRunner{}
	tb, _ := newBox(t, fr)
	forged := &toolproto.Request{V: 1, Counter: 1, Nonce: "n", PID: 666, Argv0: retro, Args: []string{"--list-skills"}}
	forged.Sign([]byte("a key the agent guessed or invented.."))
	if resp := send(t, tb, forged); resp == nil || resp.OK || resp.Error != "unavailable" {
		t.Fatalf("a forged call must get a generic refusal: %+v", resp)
	}
	unsigned := &toolproto.Request{V: 1, Counter: 2, Nonce: "m", PID: 667, Argv0: retro, Args: []string{"--list-skills"}}
	if resp := send(t, tb, unsigned); resp == nil || resp.OK {
		t.Fatalf("an unsigned call must be refused: %+v", resp)
	}
	if fr.n() != 0 {
		t.Fatal("the tool must never run for an unauthenticated call")
	}
	kept, total := tb.Rejections()
	if total != 2 || kept[0].Reason != "bad_mac" || kept[0].PID != 666 || len(tb.Records()) != 0 {
		t.Fatalf("rejections must be evidence, not calls: %+v total=%d", kept, total)
	}
}

func TestTamperedRequestIsRefused(t *testing.T) {
	fr := &fakeRunner{}
	tb, _ := newBox(t, fr)
	r := signed(retro, []string{"--list-skills"}, "", nil)
	r.Args = []string{"--request", "x"} // change what to run after signing
	if resp := send(t, tb, r); resp == nil || resp.OK || fr.n() != 0 {
		t.Fatal("a tampered request must be refused")
	}
}

func TestReplayIsRefusedButConcurrentOrderIsFine(t *testing.T) {
	fr := &fakeRunner{}
	tb, _ := newBox(t, fr)
	a, b := signed(retro, []string{"--list-skills"}, "", nil), signed(retro, []string{"--list-skills"}, "", nil)
	// b was created later (higher counter) but arrives first: legitimate with parallel tool calls
	if resp := send(t, tb, b); !resp.OK {
		t.Fatal("first arrival must be accepted")
	}
	if resp := send(t, tb, a); !resp.OK {
		t.Fatal("an earlier counter arriving later must still be accepted")
	}
	if resp := send(t, tb, a); resp.OK {
		t.Fatal("replaying a captured request must be refused")
	}
	same := signed(retro, []string{"--list-skills"}, "", nil)
	dup := *same
	dup.Nonce = "different"
	dup.Sign(testKey)
	send(t, tb, same)
	if resp := send(t, tb, &dup); resp.OK {
		t.Fatal("a reused counter must be refused")
	}
	kept, _ := tb.Rejections()
	if len(kept) != 2 || kept[0].Reason != "replay" {
		t.Fatalf("%+v", kept)
	}
	if fr.n() != 3 {
		t.Fatalf("exactly the three fresh calls should have run, got %d", fr.n())
	}
}

func TestArgumentPolicy(t *testing.T) {
	fr := &fakeRunner{}
	tb, _ := newBox(t, fr)
	for _, args := range [][]string{{"--self-check"}, {"--request"}, {"somefile.json"}, {"--bogus", "x"}} {
		resp := send(t, tb, signed(retro, args, "", nil))
		if resp == nil || !resp.OK || resp.ExitCode != 2 || !strings.Contains(string(resp.Stderr), "usage:") {
			t.Errorf("%v: should get the tool's own style of usage error, got %+v", args, resp)
		}
		if strings.Contains(string(resp.Stderr), "/") && strings.Contains(string(resp.Stderr), "home") {
			t.Errorf("%v: usage text must not mention host paths", args)
		}
	}
	if fr.n() != 0 {
		t.Fatal("refused arguments must never reach the real tool (--self-check especially)")
	}
	// all refusals are recorded as calls with args_valid=false
	for _, r := range tb.Records() {
		if r.ArgsValid || r.Result.ExitCode != 2 {
			t.Fatalf("%+v", r)
		}
	}
}

func TestFileArgumentsAreMaterializedAndCleanedUp(t *testing.T) {
	fr := &fakeRunner{}
	tb, _ := newBox(t, fr)
	body := `{"mode":"run_skill","skill":"route_generation","kwargs":{"smiles":"CCO"}}`
	resp := send(t, tb, signed(retro, []string{"--request", "work/req.json"}, "", map[string][]byte{"work/req.json": []byte(body)}))
	if !resp.OK || fr.n() != 1 {
		t.Fatalf("%+v", resp)
	}
	args := fr.calls[0].Args
	if args[0] != "--request" || strings.Contains(args[1], "work/req.json") || !strings.HasPrefix(args[1], os.TempDir()) {
		t.Fatalf("the agent's path must be replaced by a host temp path: %v", args)
	}
	if fr.files[args[1]] != body {
		t.Fatal("the tool must see the exact file content the agent supplied")
	}
	if _, err := os.Stat(args[1]); !os.IsNotExist(err) {
		t.Fatal("the temp file must be removed after the call")
	}
	if rec := tb.Records()[0]; !rec.ArgsValid || rec.Argv[1] != "work/req.json" {
		t.Fatalf("the record keeps the arguments exactly as the agent typed them: %+v", rec)
	}
	// inline form
	resp = send(t, tb, signed(retro, []string{"--request=work/r2.json"}, "", map[string][]byte{"work/r2.json": []byte(body)}))
	if !resp.OK || fr.n() != 2 || fr.calls[1].Args[0] != "--request" && !strings.HasPrefix(fr.calls[1].Args[0], "--request=") {
		t.Fatalf("inline form: %+v", fr.calls)
	}
	// a file flag without the file's content is refused
	if resp := send(t, tb, signed(retro, []string{"--request", "missing.json"}, "", nil)); resp.ExitCode != 2 {
		t.Fatalf("a path the stub did not read must be refused: %+v", resp)
	}
}

func TestInvalidRequestsAreRecordedButStillRun(t *testing.T) {
	fr := &fakeRunner{}
	tb, _ := newBox(t, fr)
	send(t, tb, signed(retro, []string{}, `{"mode":"run_skill","skill":"route_generation","kwargs":{}}`, nil))
	send(t, tb, signed(retro, []string{"--list-skills"}, "", nil))
	send(t, tb, signed(retro, []string{}, `not json at all`, nil))
	recs := tb.Records()
	if fr.n() != 3 {
		t.Fatalf("the real tool must run in every case so the agent sees real behaviour; ran %d", fr.n())
	}
	if recs[0].ArgsValid || !strings.Contains(strings.Join(recs[0].Validation.Errors, ""), "kwargs.smiles") {
		t.Fatalf("missing smiles must be recorded: %+v", recs[0].Validation)
	}
	if !recs[1].ArgsValid {
		t.Fatal("--list-skills stands for a valid list_skills request")
	}
	if recs[2].ArgsValid {
		t.Fatal("non-JSON input is invalid")
	}
}

func TestRunnerFailureIsInfraAndGeneric(t *testing.T) {
	fr := &fakeRunner{err: fmt.Errorf("fork/exec /home/me/secret/path: no such file")}
	tb, _ := newBox(t, fr)
	resp := send(t, tb, signed(retro, []string{"--list-skills"}, "", nil))
	if resp.OK || strings.Contains(resp.Error, "/home") {
		t.Fatalf("the guest must learn nothing about a host failure: %+v", resp)
	}
	if recs := tb.Records(); len(recs) != 1 || recs[0].RunnerError == "" {
		t.Fatal("the host must record why")
	}
}

func TestUnknownToolAndBadRequests(t *testing.T) {
	fr := &fakeRunner{}
	tb, _ := newBox(t, fr)
	if resp := send(t, tb, signed("nope.sh", nil, "", nil)); resp.OK {
		t.Fatal("unknown tool")
	}
	many := make([]string, 65)
	if resp := send(t, tb, signed(retro, many, "", nil)); resp.OK {
		t.Fatal("too many arguments")
	}
	if resp := send(t, tb, signed(retro, []string{strings.Repeat("a", 70<<10)}, "", nil)); resp.OK {
		t.Fatal("oversized argument")
	}
	if resp := send(t, tb, signed(retro, []string{"a\x00b"}, "", nil)); resp.OK {
		t.Fatal("NUL in argument")
	}
	if fr.n() != 0 {
		t.Fatal("none of these may run")
	}
}

func TestHostileFramesDoNotCrashOrAllocate(t *testing.T) {
	tb, _ := newBox(t, &fakeRunner{})
	for name, raw := range map[string][]byte{
		"huge length":     {0xff, 0xff, 0xff, 0xff},
		"truncated":       {0, 0, 0, 50, '{'},
		"not json":        append([]byte{0, 0, 0, 4}, "nope"...),
		"empty":           {},
		"length then eof": binary.BigEndian.AppendUint32(nil, 10),
	} {
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() { tb.ServeConn(context.Background(), server); close(done) }()
		go func() { client.Write(raw); client.Close() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: toolbox hung", name)
		}
		server.Close()
	}
	if _, total := tb.Rejections(); total != 5 {
		t.Fatalf("every malformed frame should be counted, got %d", total)
	}
	if len(tb.Records()) != 0 {
		t.Fatal("no call may be recorded")
	}
}

func TestRejectionLogIsBounded(t *testing.T) {
	tb, _ := newBox(t, &fakeRunner{})
	for i := 0; i < maxKept+50; i++ {
		tb.reject("frame", nil)
	}
	kept, total := tb.Rejections()
	if len(kept) != maxKept || total != maxKept+50 {
		t.Fatalf("kept=%d total=%d: a flood must not grow memory without bound", len(kept), total)
	}
}

func TestConcurrentCallsGetUniqueIDs(t *testing.T) {
	fr := &fakeRunner{delay: 20 * time.Millisecond}
	tb, _ := newBox(t, fr)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		req := signed(retro, []string{"--list-skills"}, "", nil)
		go func() { defer wg.Done(); send(t, tb, req) }()
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, r := range tb.Records() {
		if seen[r.CallID] {
			t.Fatalf("duplicate call id %s", r.CallID)
		}
		seen[r.CallID] = true
	}
	if len(seen) != 12 {
		t.Fatalf("want 12 records, got %d", len(seen))
	}
}

func TestNewToolboxRejectsWeakKeys(t *testing.T) {
	if _, err := NewToolbox([]byte("short"), nil); err == nil {
		t.Fatal("a short key must be refused")
	}
	_ = bytes.Equal
}
