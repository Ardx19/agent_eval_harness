package verify

import (
	"agentevalharness/internal/trace"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The real thing: the gold patch resolves the task and an empty patch does not,
// each in its own microVM with no network. Needs images/build.sh and
// tools/swe/build_env.py pytest-dev__pytest-7432 to have been run.
func TestGoldResolvesAndEmptyDoesNot(t *testing.T) {
	const id = "pytest-dev__pytest-7432"
	root := filepath.Join("..", "..")
	need := func(p string) string {
		p = filepath.Join(root, p)
		if _, err := os.Stat(p); err != nil {
			t.Skipf("missing %s", p)
		}
		return p
	}
	fc, kernel, base := need("spike/firecracker/firecracker"), need("spike/firecracker/vmlinux-6.1.186"), need("images/build/rootfs.ext4")
	env, repo := need("envs/build/"+id+"/env.ext4"), need("envs/build/"+id+"/repo")
	vdir := need("suites/swe-lite/verify/" + id)
	useSG := false
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err == nil {
		f.Close()
	} else if exec.Command("sg", "kvm", "-c", "test -r /dev/kvm -a -w /dev/kvm").Run() == nil {
		useSG = true
	} else {
		t.Skip("no access to /dev/kvm")
	}
	tests, err := LoadTests(vdir)
	if err != nil {
		t.Fatal(err)
	}
	testPatch, _ := os.ReadFile(filepath.Join(vdir, "test_patch.diff"))
	gold, _ := os.ReadFile(filepath.Join(vdir, "gold.patch"))
	run := func(name string, agent []byte) (v interface{}, resolved bool, cause string) {
		out, err := Run(context.Background(), Spec{Dir: filepath.Join("/tmp/claude-1000/vt", name), Tests: tests, TestPatch: testPatch, RepoDir: repo, AgentPatch: agent,
			EnvImage: env, PathPrefix: []string{"/opt/env/venv/bin"}, FirecrackerBin: fc, Kernel: kernel, BaseImage: base, UseSG: useSG, MemMiB: 1024, WallS: 300})
		if err != nil {
			t.Fatalf("%s: %v\n%+v", name, err, out)
		}
		t.Logf("%s: status=%s cause=%q wall=%.1fs f2p=%v p2p_failed=%d/%d exit=%d changed=%v", name, out.Status, out.Cause, out.WallS, out.F2P, out.P2PFailed, out.P2PTotal, out.ExitCode, out.ChangedFiles)
		if !out.Resolved {
			t.Logf("tail:\n%s", out.OutputTail)
		}
		return out, out.Resolved, out.Cause
	}
	if _, ok, cause := run("gold", gold); !ok {
		t.Errorf("the gold patch must resolve the task (cause %q)", cause)
	}
	if _, ok, cause := run("empty", nil); ok || cause != "empty_patch" {
		t.Errorf("an empty patch must not resolve the task: resolved=%v cause=%q", ok, cause)
	}
}

// Cheats against the report, in a real guest. A conftest.py that forges results is
// restored away, and a patch that makes pytest itself report failures as passes is
// caught by the hidden canary tests and does not count as resolved.
func TestForgedReportsDoNotResolve(t *testing.T) {
	const id = "pytest-dev__pytest-7432"
	root := filepath.Join("..", "..")
	need := func(p string) string {
		p = filepath.Join(root, p)
		if _, err := os.Stat(p); err != nil {
			t.Skipf("missing %s", p)
		}
		return p
	}
	fc, kernel, base := need("spike/firecracker/firecracker"), need("spike/firecracker/vmlinux-6.1.186"), need("images/build/rootfs.ext4")
	env, repo := need("envs/build/"+id+"/env.ext4"), need("envs/build/"+id+"/repo")
	vdir := need("suites/swe-lite/verify/" + id)
	useSG := false
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err == nil {
		f.Close()
	} else if exec.Command("sg", "kvm", "-c", "test -r /dev/kvm -a -w /dev/kvm").Run() == nil {
		useSG = true
	} else {
		t.Skip("no access to /dev/kvm")
	}
	tests, err := LoadTests(vdir)
	if err != nil {
		t.Fatal(err)
	}
	testPatch, _ := os.ReadFile(filepath.Join(vdir, "test_patch.diff"))

	// patch 1: a conftest.py that flips every outcome and prints fake report lines
	work := filepath.Join(t.TempDir(), "w")
	if out, err := exec.Command("cp", "-a", repo, work).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	conftest := "import pytest\n\n@pytest.hookimpl(hookwrapper=True)\ndef pytest_runtest_makereport(item, call):\n    out = yield\n    rep = out.get_result()\n    rep.outcome = 'passed'\n    print('PASSED ' + item.nodeid)\n"
	os.WriteFile(filepath.Join(work, "conftest.py"), []byte(conftest), 0o644)
	exec.Command("git", "-C", work, "add", "-A", "-N").Run()
	conftestPatch, _ := exec.Command("git", "-C", work, "diff", "--binary", "HEAD").Output()
	exec.Command("git", "-C", work, "checkout", "-q", "HEAD", "--", ".").Run()
	os.Remove(filepath.Join(work, "conftest.py"))

	// patch 2: pytest's own reporting code says every failure passed
	exec.Command("sed", "-i", `s/outcome = "failed"/outcome = "passed"/`, filepath.Join(work, "src/_pytest/reports.py")).Run()
	reportPatch, _ := exec.Command("git", "-C", work, "diff", "--binary", "HEAD").Output()
	if len(conftestPatch) == 0 || len(reportPatch) == 0 {
		t.Fatal("could not build the forgery patches")
	}

	run := func(name string, patch []byte) *trace.Verify {
		out, err := Run(context.Background(), Spec{Dir: filepath.Join(t.TempDir(), name), Tests: tests, TestPatch: testPatch, RepoDir: repo, AgentPatch: patch,
			EnvImage: env, PathPrefix: []string{"/opt/env/venv/bin"}, FirecrackerBin: fc, Kernel: kernel, BaseImage: base, UseSG: useSG, MemMiB: 1024, WallS: 300})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Logf("%s: status=%s cause=%s note=%q canary_ok=%v tampered=%v", name, out.Status, out.Cause, out.Note, out.CanaryOK, out.Tampered)
		return out
	}
	if v := run("conftest", conftestPatch); v.Resolved || v.Status == "resolved" || len(v.Tampered) == 0 {
		t.Errorf("a forging conftest.py must be removed and must not resolve: %+v", v)
	}
	if v := run("reporter", reportPatch); v.Resolved || v.Status != "compromised" || v.Cause != "reporting_tampered" {
		t.Errorf("a patch that rewrites pytest's reporting must end compromised: %+v", v)
	}
}

// A guest that writes huge lines of awkward bytes must not cost the host the exit
// frame (pytest-9359 lost it twice). Lines of control characters expand six times
// when encoded, so the guest caps lines well below the host's frame limit.
func TestHugeAwkwardOutputStillEndsCleanly(t *testing.T) {
	const id = "pytest-dev__pytest-7432"
	root := filepath.Join("..", "..")
	need := func(p string) string {
		p = filepath.Join(root, p)
		if _, err := os.Stat(p); err != nil {
			t.Skipf("missing %s", p)
		}
		return p
	}
	fc, kernel, base := need("spike/firecracker/firecracker"), need("spike/firecracker/vmlinux-6.1.186"), need("images/build/rootfs.ext4")
	env, repo := need("envs/build/"+id+"/env.ext4"), need("envs/build/"+id+"/repo")
	vdir := need("suites/swe-lite/verify/" + id)
	useSG := false
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err == nil {
		f.Close()
	} else if exec.Command("sg", "kvm", "-c", "test -r /dev/kvm -a -w /dev/kvm").Run() == nil {
		useSG = true
	} else {
		t.Skip("no access to /dev/kvm")
	}
	tests, err := LoadTests(vdir)
	if err != nil {
		t.Fatal(err)
	}
	// 3 MiB of control characters on one line, then 20 MiB of ordinary lines, then a normal exit
	tests.TestCommand = []string{"/opt/env/venv/bin/python", "-c", "import sys\nsys.stdout.write('\\x01'*(3<<20)+'\\n')\nfor i in range(200000):\n    sys.stdout.write('line %d of ordinary output padded out to a hundred bytes ........................................\\n' % i)\n"}
	testPatch, _ := os.ReadFile(filepath.Join(vdir, "test_patch.diff"))
	out, err := Run(context.Background(), Spec{Dir: filepath.Join(t.TempDir(), "big"), Tests: tests, TestPatch: testPatch, RepoDir: repo,
		EnvImage: env, PathPrefix: []string{"/opt/env/venv/bin"}, FirecrackerBin: fc, Kernel: kernel, BaseImage: base, UseSG: useSG, MemMiB: 1024, WallS: 300})
	if err != nil {
		t.Fatalf("the exit frame was lost after huge output: %v (status %s)", err, out.Status)
	}
	if out.ExitCode != 0 {
		t.Fatalf("exit code %d", out.ExitCode)
	}
}

// A silent guest is ended by the supervisor's idle watchdog, quickly, with an exit
// frame: the run must come back as a (non-infrastructure) verdict, not hang for the
// whole wall-clock limit.
func TestSilentGuestIsEndedByTheIdleWatchdog(t *testing.T) {
	const id = "pytest-dev__pytest-7432"
	root := filepath.Join("..", "..")
	need := func(p string) string {
		p = filepath.Join(root, p)
		if _, err := os.Stat(p); err != nil {
			t.Skipf("missing %s", p)
		}
		return p
	}
	fc, kernel, base := need("spike/firecracker/firecracker"), need("spike/firecracker/vmlinux-6.1.186"), need("images/build/rootfs.ext4")
	env, repo := need("envs/build/"+id+"/env.ext4"), need("envs/build/"+id+"/repo")
	vdir := need("suites/swe-lite/verify/" + id)
	useSG := false
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err == nil {
		f.Close()
	} else if exec.Command("sg", "kvm", "-c", "test -r /dev/kvm -a -w /dev/kvm").Run() == nil {
		useSG = true
	} else {
		t.Skip("no access to /dev/kvm")
	}
	tests, err := LoadTests(vdir)
	if err != nil {
		t.Fatal(err)
	}
	tests.TestCommand = []string{"/opt/env/venv/bin/python", "-c", "print('started', flush=True)\nimport time\ntime.sleep(900)"}
	testPatch, _ := os.ReadFile(filepath.Join(vdir, "test_patch.diff"))
	start := time.Now()
	out, err := Run(context.Background(), Spec{Dir: filepath.Join(t.TempDir(), "idle"), Tests: tests, TestPatch: testPatch, RepoDir: repo,
		EnvImage: env, PathPrefix: []string{"/opt/env/venv/bin"}, FirecrackerBin: fc, Kernel: kernel, BaseImage: base, UseSG: useSG,
		MemMiB: 1024, WallS: 600, IdleS: 25})
	if err != nil {
		t.Fatalf("an idle run must end with a verdict, not an error: %v", err)
	}
	if out.Status == "infra_error" || out.Cause != "verify_timeout" {
		t.Fatalf("%+v", out)
	}
	if took := time.Since(start); took > 150*time.Second {
		t.Fatalf("the idle watchdog should have ended the run in well under the wall limit, it took %v", took)
	}
}
