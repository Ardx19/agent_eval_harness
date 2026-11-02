package vm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"agentevalharness/internal/sandbox"
	"agentevalharness/internal/skills"
)

// The whole tool path in a real microVM. As the agent uid, a guest process calls
// a tool through the real setuid stub and then attacks it: reads the key, uses
// the stub to read a file it could not read itself, replaces the entrypoint,
// edits the skill's files, and speaks the raw protocol with a forged signature.
// The host checks both what the guest saw and what the host itself recorded.
func TestToolPathAndAttacksInARealGuest(t *testing.T) {
	r := needRig(t)
	ctx := context.Background()
	sys := sandbox.NewSystem()

	m, err := skills.LoadManifest("../../testdata/fixtures/toy_skill/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := filepath.Abs("../../testdata/fixtures/toy_skill")
	m.Host.Dir, m.Host.ScrubPrefixes = dir, []string{dir}

	raw := make([]byte, 32)
	rand.Read(raw)
	key := []byte(hex.EncodeToString(raw)) // 64 printable bytes
	tb, err := skills.NewToolbox(key, &skills.Tool{Manifest: m, Runner: &skills.HostExec{Manifest: m}})
	if err != nil {
		t.Fatal(err)
	}

	extras := append(m.Install("# toy skill\n"), skills.KeyFile(key))
	wsImg := filepath.Join(t.TempDir(), "workspace.ext4")
	wsEntries, err := sandbox.BuildWorkspaceWith(ctx, sys, t.TempDir(), wsImg, sandbox.DefaultIdentities, extras)
	if err != nil {
		t.Fatal(err)
	}
	baseEntries, err := sandbox.LoadEntries(filepath.Join("..", "..", "images", "build", "entries.jsonl"))
	if err != nil {
		t.Fatalf("load base entries (run images/build.sh): %v", err)
	}

	cfg := r.cfg(t, "/stub-selftest")
	cfg.WorkspaceImage, cfg.ScratchMiB, cfg.MemMiB = wsImg, 64, 512
	v, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Destroy()
	if err := v.Listen(5001, tb.ServeConn); err != nil {
		t.Fatal(err)
	}
	if err := v.Start(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := v.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	con := v.Console()
	if res.Exit != ExitOK || !strings.Contains(con, "SPIKE: DONE") {
		t.Fatalf("guest did not finish (exit %s); rerun images/build.sh if the image predates U14\n%s", res.Exit, tail(con, 30))
	}
	if strings.Contains(con, string(key)) {
		t.Fatal("the per-trial key appeared in the guest console")
	}

	has := func(label, want string) {
		t.Helper()
		if !strings.Contains(con, want) {
			t.Errorf("%s: guest output missing %q\n%s", label, want, tail(con, 20))
		}
	}
	has("entrypoint is root-owned", "scripts=lrwxrwxrwx:0:0:toy.sh")
	has("stub is setuid to the tool uid", "SPIKE: stub=4711:2000")
	has("t1 stdin round trip", `SPIKE: t1={"ok": true, "tool": "toy", "stdin": "hello"}`)
	has("t2 exit code kept", "SPIKE: t2 exit=1")
	has("t2 host path scrubbed", `"error": "ConfigurationError: missing <path>"`)
	has("t3 file argument delivered", `SPIKE: t3={"mode":"echo"}`)
	has("t4 confused deputy refused", "SPIKE: t4 exit=1 stdout_bytes=0")
	has("t4 the OS refused it as the agent", "Permission denied")
	has("t5 key unreadable", "cat_key=cat: /opt/svc/key: Permission denied")
	has("t5 tool dir unreadable", "ls_svc=ls: cannot open directory '/opt/svc': Permission denied")
	has("t5 stub binary unreadable", "read_stub=cat: /usr/local/bin/toolstub: Permission denied")
	has("t6 cannot delete the entrypoint", "rm=rm: cannot remove 'scripts/toy.sh': Permission denied")
	has("t6 cannot replace the entrypoint", "ln=ln: failed to create symbolic link 'scripts/toy.sh': Permission denied")
	has("t6 cannot edit the doc", "cannot create /usr/share/agent-skills/toy/SKILL.md")
	has("t6 cannot edit the stub config", "cannot create /usr/share/agent-skills/toy/stub.json")
	has("t7 forged call refused generically", `SPIKE: t7 forged -> {"error": "unavailable", "exit_code": 0, "ok": false}`)
	for _, leak := range []string{dir, "/home/someone", "weights.onnx", "core.py"} {
		if strings.Contains(con, leak) {
			t.Errorf("host detail %q reached the guest", leak)
		}
	}
	// the host's own records: what actually ran, measured by the host
	recs := tb.Records()
	if len(recs) != 3 { // t1, t2, t3. t4 never left the guest; t7 was refused, not run.
		for _, c := range recs {
			t.Logf("record %v", c.Argv)
		}
		t.Fatalf("expected exactly 3 recorded calls, got %d", len(recs))
	}
	if got := recs[0].Argv; len(got) != 1 || got[0] != "ok" || recs[0].Result.ExitCode != 0 || !recs[0].ArgsValid {
		t.Errorf("t1 record: %+v", recs[0])
	}
	if got := recs[1].Argv; len(got) != 1 || got[0] != "fail" || recs[1].Result.ExitCode != 1 {
		t.Errorf("t2 record: %+v", recs[1])
	}
	if got := recs[2].Argv; strings.Join(got, " ") != "file --request req.json" || string(recs[2].Result.Stdout) != `{"mode":"echo"}`+"\n" {
		t.Errorf("t3 record (the argv is kept exactly as typed): %+v / %q", recs[2].Argv, recs[2].Result.Stdout)
	}
	seen := map[int]bool{}
	for _, c := range recs {
		if c.PID <= 0 || c.PPID <= 0 || seen[c.PID] {
			t.Errorf("each call needs its own guest pid for the exec-log join: %+v", c)
		}
		seen[c.PID] = true
		if c.Result.StdoutSHA256 == "" || c.Result.Duration <= 0 {
			t.Errorf("host-measured facts missing: %+v", c.Result)
		}
		if h := c.ToTrace(); !h.ReceiptValid || h.CallID == "" {
			t.Errorf("trace record: %+v", h)
		}
	}
	if strings.Contains(string(recs[1].Result.Stderr)+string(recs[1].Result.Stdout), dir) {
		t.Error("the scrubbed output the host recorded must match what the guest saw")
	}
	kept, total := tb.Rejections()
	if total != 1 || kept[0].Reason != "bad_mac" || kept[0].PID <= 0 || kept[0].Argv0 != "toy.sh" {
		t.Errorf("the forged call must be recorded as evidence with the forger's pid: %+v total=%d", kept, total)
	}

	// the attacks changed nothing that matters
	lower := sandbox.NewLower(baseEntries, wsEntries)
	changes, err := sandbox.ExtractDiff(ctx, sys, filepath.Join(cfg.RunDir, "scratch.img"), lower, sandbox.DefaultIdentities)
	if err != nil {
		t.Fatal(err)
	}
	var sawReq bool
	for _, c := range changes {
		for _, protected := range []string{"/opt/svc", "/usr/share/agent-skills", "/usr/local/bin", "/home/dev/project/scripts"} {
			if c.Path == protected || strings.HasPrefix(c.Path, protected+"/") {
				t.Errorf("a protected path was changed: %+v", c)
			}
		}
		if c.Path == "/home/dev/project/req.json" {
			sawReq = c.Op == "create" && c.WriterHint == "agent"
		}
	}
	if !sawReq {
		t.Errorf("the agent's own file should be the visible change: %+v", changes)
	}
	t.Logf("guest run ok; %d filesystem changes, all in agent-writable places\n%s", len(changes), tail(con, 14))
}
