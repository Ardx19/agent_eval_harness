package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func toy(t *testing.T, mut func(*Manifest)) *HostExec {
	t.Helper()
	m, err := LoadManifest("../../testdata/fixtures/toy_skill/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := filepath.Abs("../../testdata/fixtures/toy_skill")
	m.Host.Dir, m.Host.ScrubPrefixes = dir, []string{dir}
	if mut != nil {
		mut(m)
	}
	return &HostExec{Manifest: m}
}

func run(t *testing.T, h *HostExec, stdin string, args ...string) Result {
	t.Helper()
	r, err := h.Run(context.Background(), Request{Args: args, Stdin: []byte(stdin)})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunPassesStdinAndReportsFacts(t *testing.T) {
	r := run(t, toy(t, nil), "hello", "ok")
	if r.ExitCode != 0 || !strings.Contains(string(r.Stdout), `"stdin": "hello"`) {
		t.Fatalf("%d %s", r.ExitCode, r.Stdout)
	}
	if r.StdoutSHA256 != sum(r.Stdout) {
		t.Fatal("the recorded hash must be of exactly what the agent receives")
	}
	if r.Duration <= 0 {
		t.Fatal("duration not measured")
	}
}

func TestFailureIsScrubbedButExitCodeKept(t *testing.T) {
	h := toy(t, nil)
	r := run(t, h, "", "fail")
	if r.ExitCode != 1 {
		t.Fatalf("exit code %d", r.ExitCode)
	}
	all := string(r.Stdout) + string(r.Stderr)
	dir := h.Manifest.Host.Dir
	for _, leak := range []string{dir, "/home/someone", "weights.onnx"} {
		if strings.Contains(all, leak) {
			t.Fatalf("host detail %q reached the agent:\n%s", leak, all)
		}
	}
	if !strings.Contains(all, "ConfigurationError") || !strings.Contains(all, "<path>") || strings.Contains(all, "core.py") || strings.Contains(all, ".hdf5") {
		t.Fatalf("the error type should stay, host paths and file names should not:\n%s", all)
	}
}

func TestTimeoutIsFlagged(t *testing.T) {
	h := toy(t, func(m *Manifest) { m.Host.TimeoutS = 1 })
	start := time.Now()
	r := run(t, h, "", "sleep", "30")
	if !r.TimedOut || time.Since(start) > 10*time.Second {
		t.Fatalf("timed out=%v after %v", r.TimedOut, time.Since(start))
	}
	if r.OOMSuspect {
		t.Fatal("a kill we sent ourselves is not an OOM")
	}
}

func TestOutputIsCappedAndFlagged(t *testing.T) {
	h := toy(t, nil)
	h.MaxOutput = 1000
	r := run(t, h, "", "big")
	if len(r.Stdout) != 1000 || !r.Truncated {
		t.Fatalf("len=%d truncated=%v", len(r.Stdout), r.Truncated)
	}
}

func TestHostMeasuresCPUAndMemory(t *testing.T) {
	r := run(t, toy(t, nil), "", "burn")
	if r.UserCPU < 200*time.Millisecond {
		t.Fatalf("cpu not measured: %v", r.UserCPU)
	}
	if r.PeakMemMB < 40 {
		t.Fatalf("peak memory should reflect the 60 MiB allocation, got %.1f MB", r.PeakMemMB)
	}
}

func TestExternalKillLooksLikeOOM(t *testing.T) {
	r := run(t, toy(t, nil), "", "suicide")
	if !r.OOMSuspect || r.Signaled != "killed" || r.TimedOut {
		t.Fatalf("%+v", r)
	}
	if r.ExitCode != 137 {
		t.Fatalf("a SIGKILLed tool reports 137 as a shell would, not %d", r.ExitCode)
	}
}

func TestToolSeesOnlyAMinimalEnvironment(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "super-secret")
	t.Setenv("MY_PRIVATE_TOKEN", "x")
	out := string(run(t, toy(t, nil), "", "env").Stdout)
	for _, v := range []string{"GEMINI_API_KEY", "MY_PRIVATE_TOKEN"} {
		if strings.Contains(out, v) {
			t.Fatalf("the tool inherited %s: %s", v, out)
		}
	}
}

func TestSmilesSurviveScrubbing(t *testing.T) {
	out := string(run(t, toy(t, nil), "", "smiles").Stdout)
	if !strings.Contains(out, "F/C=C/F") || !strings.Contains(out, "C/C=C/C") {
		t.Fatalf("chemistry was damaged: %s", out)
	}
}

func timed(t *testing.T, h *HostExec) time.Duration {
	t.Helper()
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); h.Run(context.Background(), Request{Args: []string{"sleep", "1"}}) }()
	}
	wg.Wait()
	return time.Since(start)
}

func TestSerializeRunsOneCallAtATime(t *testing.T) {
	if d := timed(t, toy(t, func(m *Manifest) { m.Host.Serialize = true })); d < 2*time.Second {
		t.Fatalf("serialized calls must not overlap, took %v", d)
	}
	if d := timed(t, toy(t, func(m *Manifest) { m.Host.Serialize = false })); d > 1900*time.Millisecond {
		t.Fatalf("unserialized calls should overlap, took %v", d)
	}
}

func TestMissingCommandIsAHarnessError(t *testing.T) {
	h := toy(t, func(m *Manifest) { m.Host.Command = []string{"./does-not-exist.sh"} })
	if _, err := h.Run(context.Background(), Request{}); err == nil {
		t.Fatal("a tool that cannot start is an infra error, not an agent result")
	}
	_ = os.Stdout
}

// Runs the real retrosynthesis skill (slow: ~40-60 s and 1-2 GB). Opt-in:
//
//	EVH_REAL_TOOL=1 go test ./internal/skills -run TestRealRetrosynthesis -v
func TestRealRetrosynthesis(t *testing.T) {
	if os.Getenv("EVH_REAL_TOOL") == "" {
		t.Skip("set EVH_REAL_TOOL=1 to run the real skill")
	}
	m := loadRetro(t)
	if _, err := os.Stat(m.Host.Dir); err != nil {
		t.Skipf("skill project not found: %v", err)
	}
	h := &HostExec{Manifest: m}
	r, err := h.Run(context.Background(), Request{Args: []string{"--list-skills"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("exit=%d duration=%v cpu=%v peak=%.0f MB stdout=%d bytes stderr=%d bytes", r.ExitCode, r.Duration.Round(time.Millisecond), r.UserCPU+r.SysCPU, r.PeakMemMB, len(r.Stdout), len(r.Stderr))
	if r.ExitCode != 0 || r.TimedOut || r.OOMSuspect {
		t.Fatalf("real tool failed: %+v\nstderr: %s", r, r.Stderr)
	}
	all := string(r.Stdout) + string(r.Stderr)
	for _, leak := range []string{m.Host.Dir, "aryandutt", "miniconda", "aizynth-env"} {
		if strings.Contains(all, leak) {
			t.Fatalf("host detail %q reached the agent", leak)
		}
	}
	for _, s := range m.Skills {
		if !strings.Contains(string(r.Stdout), s.Name) {
			t.Errorf("the real tool does not list skill %q that the manifest documents", s.Name)
		}
	}
	if v := m.ValidateRequest([]byte(`{"mode":"list_skills"}`)); !v.Valid() {
		t.Fatal("the request we sent must also validate")
	}
}
