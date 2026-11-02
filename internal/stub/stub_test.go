package stub

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agentevalharness/internal/skills"
)

var key = []byte("per-trial-key-0123456789abcdef!!")

type recordingRunner struct {
	args  [][]string
	stdin []string
	files []string
	res   skills.Result
}

func (r *recordingRunner) Name() string { return "fake" }
func (r *recordingRunner) Run(_ context.Context, q skills.Request) (skills.Result, error) {
	r.args = append(r.args, q.Args)
	r.stdin = append(r.stdin, string(q.Stdin))
	for i, a := range q.Args {
		if a == "--request" && i+1 < len(q.Args) {
			b, _ := os.ReadFile(q.Args[i+1])
			r.files = append(r.files, string(b))
		}
	}
	res := r.res
	if res.Stdout == nil {
		res.Stdout = []byte(`{"ok": true}` + "\n")
	}
	return res, nil
}

type rig struct {
	dir, keyPath, glob string
	runner             *recordingRunner
	tb                 *skills.Toolbox
	dialed             int32
	out, errOut        bytes.Buffer
}

func newRig(t *testing.T) *rig {
	t.Helper()
	m, err := skills.LoadManifest("../../skills/retrosynthesis/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{dir: t.TempDir(), runner: &recordingRunner{}}
	r.keyPath = filepath.Join(r.dir, "key")
	os.WriteFile(r.keyPath, append(key, '\n'), 0o400)
	os.MkdirAll(filepath.Join(r.dir, "skills", "retrosynthesis"), 0o755)
	os.WriteFile(filepath.Join(r.dir, "skills", "retrosynthesis", "stub.json"),
		[]byte(`{"argv0":"run_retrosynthesis.sh","file_flags":["--request"],"stdin":true}`), 0o444)
	r.glob = filepath.Join(r.dir, "skills", "*", "stub.json")
	r.tb, err = skills.NewToolbox(key, &skills.Tool{Manifest: m, Runner: r.runner})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) env(argv []string, stdin string) Env {
	return Env{
		Argv: argv, Stdin: strings.NewReader(stdin), Stdout: &r.out, Stderr: &r.errOut, PID: 4242, PPID: 4000,
		KeyPath: r.keyPath, ConfGlob: r.glob,
		Dial: func() (io.ReadWriteCloser, error) {
			atomic.AddInt32(&r.dialed, 1)
			c, s := net.Pipe()
			go func() { r.tb.ServeConn(context.Background(), s); s.Close() }()
			return c, nil
		},
	}
}

func TestEndToEndThroughARealToolbox(t *testing.T) {
	r := newRig(t)
	r.runner.res = skills.Result{ExitCode: 0, Stdout: []byte("RESULT\n"), Stderr: []byte("warn\n")}
	code := Run(r.env([]string{"scripts/run_retrosynthesis.sh"}, `{"mode":"list_skills"}`))
	if code != 0 || r.out.String() != "RESULT\n" || r.errOut.String() != "warn\n" {
		t.Fatalf("code=%d out=%q err=%q", code, r.out.String(), r.errOut.String())
	}
	if r.runner.stdin[0] != `{"mode":"list_skills"}` {
		t.Fatalf("stdin not forwarded: %q", r.runner.stdin)
	}
	rec := r.tb.Records()
	if len(rec) != 1 || rec[0].PID != 4242 || rec[0].PPID != 4000 {
		t.Fatalf("%+v", rec)
	}
}

func TestExitCodeIsPropagated(t *testing.T) {
	r := newRig(t)
	r.runner.res = skills.Result{ExitCode: 1, Stdout: []byte(`{"ok": false}`)}
	if code := Run(r.env([]string{"run_retrosynthesis.sh"}, `{"mode":"list_skills"}`)); code != 1 {
		t.Fatalf("the tool's exit code must reach the agent, got %d", code)
	}
}

func TestFileArgumentIsReadAndForwarded(t *testing.T) {
	r := newRig(t)
	req := filepath.Join(r.dir, "req.json")
	os.WriteFile(req, []byte(`{"mode":"list_skills"}`), 0o644)
	for _, args := range [][]string{{"run_retrosynthesis.sh", "--request", req}, {"run_retrosynthesis.sh", "--request=" + req}} {
		if code := Run(r.env(args, "")); code != 0 {
			t.Fatalf("%v: exit %d, stderr %q", args, code, r.errOut.String())
		}
	}
	if len(r.runner.files) != 2 || r.runner.files[0] != `{"mode":"list_skills"}` || r.runner.files[1] != r.runner.files[0] { // one per argument form
		t.Fatalf("the tool must see the file the agent named: %v", r.runner.files)
	}
}

func TestTTYStdinIsNotRead(t *testing.T) {
	r := newRig(t)
	e := r.env([]string{"run_retrosynthesis.sh", "--list-skills"}, "should not be read")
	e.StdinTTY = true
	Run(e)
	if r.runner.stdin[0] != "" {
		t.Fatalf("an interactive stdin must not be forwarded: %q", r.runner.stdin[0])
	}
}

func TestDroppingPrivilegesFailsClosed(t *testing.T) {
	r := newRig(t)
	e := r.env([]string{"run_retrosynthesis.sh", "--list-skills"}, "")
	e.DropToCaller = func() error { return errors.New("EPERM") }
	if code := Run(e); code != exitUnavailable || atomic.LoadInt32(&r.dialed) != 0 {
		t.Fatalf("if privileges cannot be dropped, nothing agent-controlled may be touched: code=%d dialed=%d", code, r.dialed)
	}
}

func TestMissingKeyDoesNotConnect(t *testing.T) {
	r := newRig(t)
	os.Remove(r.keyPath)
	if code := Run(r.env([]string{"run_retrosynthesis.sh", "--list-skills"}, "")); code != exitUnavailable || r.dialed != 0 {
		t.Fatalf("code=%d dialed=%d", code, r.dialed)
	}
	if !strings.Contains(r.errOut.String(), "tool unavailable") || strings.Contains(r.errOut.String(), r.keyPath) {
		t.Fatalf("the message must be generic: %q", r.errOut.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	r := newRig(t)
	if code := Run(r.env([]string{"/usr/local/bin/mystery", "x"}, "")); code != exitNotFound || r.dialed != 0 {
		t.Fatalf("code=%d dialed=%d", code, r.dialed)
	}
}

func TestUnreadableOrOddFilesAreRefusedLocally(t *testing.T) {
	r := newRig(t)
	cases := map[string]string{
		"missing":   filepath.Join(r.dir, "nope.json"),
		"directory": r.dir,
	}
	for name, path := range cases {
		r.errOut.Reset()
		if code := Run(r.env([]string{"run_retrosynthesis.sh", "--request", path}, "")); code != 1 || r.dialed != 0 {
			t.Errorf("%s: code=%d dialed=%d", name, code, r.dialed)
		}
		if !strings.Contains(r.errOut.String(), "cannot read") {
			t.Errorf("%s: %q", name, r.errOut.String())
		}
	}
	big := filepath.Join(r.dir, "big.json")
	os.WriteFile(big, bytes.Repeat([]byte("x"), maxInput+10), 0o644)
	if code := Run(r.env([]string{"run_retrosynthesis.sh", "--request", big}, "")); code != 1 || !strings.Contains(r.errOut.String(), "too large") {
		t.Fatalf("an oversized file must be refused: %d %q", code, r.errOut.String())
	}
}

func TestToolboxRefusalIsGeneric(t *testing.T) {
	r := newRig(t)
	if err := os.Remove(r.keyPath); err != nil { // the key file is 0400, so replace it rather than write to it
		t.Fatal(err)
	}
	if err := os.WriteFile(r.keyPath, []byte("a different key than the toolbox holds!!"), 0o400); err != nil {
		t.Fatal(err)
	}
	code := Run(r.env([]string{"run_retrosynthesis.sh", "--list-skills"}, ""))
	if code != exitUnavailable || strings.TrimSpace(r.errOut.String()) != "run_retrosynthesis.sh: tool unavailable" {
		t.Fatalf("code=%d err=%q", code, r.errOut.String())
	}
	if len(r.tb.Records()) != 0 {
		t.Fatal("a wrongly signed call must not be recorded as a call")
	}
}

func TestDialFailure(t *testing.T) {
	r := newRig(t)
	e := r.env([]string{"run_retrosynthesis.sh", "--list-skills"}, "")
	e.Dial = func() (io.ReadWriteCloser, error) { return nil, errors.New("no vsock") }
	if code := Run(e); code != exitUnavailable || strings.Contains(r.errOut.String(), "vsock") {
		t.Fatalf("code=%d err=%q", code, r.errOut.String())
	}
}

func TestFilePathsParsing(t *testing.T) {
	got := filePaths([]string{"--request", "a", "--list-skills", "--request=b", "--other=c", "--request"}, []string{"--request"})
	if strings.Join(got, ",") != "a,b" {
		t.Fatalf("%v", got)
	}
}
