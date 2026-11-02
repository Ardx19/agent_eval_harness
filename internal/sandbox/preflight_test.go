package sandbox

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
)

type fakeSys struct {
	kvmErr error
	run    map[string]fakeRun
	files  map[string][]byte
	tools  map[string]bool
}

type fakeRun struct {
	out string
	err error
}

func (f *fakeSys) OpenKVM() error { return f.kvmErr }
func (f *fakeSys) Run(_ context.Context, name string, args ...string) (string, error) {
	r, ok := f.run[name+" "+strings.Join(args, " ")]
	if !ok {
		return "", errors.New("unexpected command: " + name + " " + strings.Join(args, " "))
	}
	return r.out, r.err
}
func (f *fakeSys) ReadFile(p string) ([]byte, error) {
	if b, ok := f.files[p]; ok {
		return b, nil
	}
	return nil, os.ErrNotExist
}
func (f *fakeSys) FileSize(p string) (int64, error) {
	if b, ok := f.files[p]; ok {
		return int64(len(b)), nil
	}
	return 0, os.ErrNotExist
}
func (f *fakeSys) SHA256(p string) (string, error) {
	if b, ok := f.files[p]; ok {
		return sum(b), nil
	}
	return "", os.ErrNotExist
}
func (f *fakeSys) LookPath(n string) (string, error) {
	if f.tools[n] {
		return "/usr/bin/" + n, nil
	}
	return "", errors.New("not found")
}

const sgCmd = "sg kvm -c test -r /dev/kvm -a -w /dev/kvm"

func byName(r Report, name string) Check {
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	return Check{}
}

func TestKVMStates(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		sys  *fakeSys
		want Status
	}{
		{"ok", &fakeSys{}, OK},
		{"missing device", &fakeSys{kvmErr: fs.ErrNotExist}, Fail},
		{"permission but sg works", &fakeSys{kvmErr: fs.ErrPermission, run: map[string]fakeRun{sgCmd: {}}}, Warn},
		{"permission and sg fails", &fakeSys{kvmErr: fs.ErrPermission, run: map[string]fakeRun{sgCmd: {err: errors.New("exit 1")}}}, Fail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkKVM(ctx, tc.sys).Status; got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestMemAvailableParse(t *testing.T) {
	mi, ok := memAvailableMiB("MemTotal: 7786000 kB\nMemAvailable:    4194304 kB\n")
	if !ok || mi != 4096 {
		t.Fatalf("got %d %v", mi, ok)
	}
	if _, ok := memAvailableMiB("nothing here"); ok {
		t.Fatal("expected not ok")
	}
}

func TestRAMWarnsWhenShort(t *testing.T) {
	sys := &fakeSys{files: map[string][]byte{"/proc/meminfo": []byte("MemAvailable: 1048576 kB\n")}}
	c := checkRAM(Options{Sys: sys, Concurrency: 2, VMMemMiB: 1536})
	if c.Status != Warn {
		t.Fatalf("got %s", c.Status)
	}
}

func TestKeyCheckNeverRequestsCredentials(t *testing.T) {
	want := "pi auth check --provider google --model m --json --no-refresh"
	sys := &fakeSys{run: map[string]fakeRun{want: {out: `{"status":"ready","provider":"google","authType":"api_key"}`}}}
	c := checkKey(context.Background(), Options{Sys: sys, Provider: "google", Model: "m"})
	if c.Status != OK || !strings.Contains(c.Detail, "api_key") {
		t.Fatalf("unexpected: %+v", c)
	}
	if strings.Contains(want, "--credentials") {
		t.Fatal("must not request credentials")
	}
}

func TestKeyNotReady(t *testing.T) {
	sys := &fakeSys{run: map[string]fakeRun{
		"pi auth check --provider google --model m --json --no-refresh": {out: `{"status":"missing"}`},
	}}
	if c := checkKey(context.Background(), Options{Sys: sys, Provider: "google", Model: "m"}); c.Status != Fail {
		t.Fatalf("got %s", c.Status)
	}
}

func TestFirecrackerPinChecks(t *testing.T) {
	dir := "art"
	sys := &fakeSys{
		run:   map[string]fakeRun{dir + "/firecracker --version": {out: "Firecracker " + PinnedFirecrackerVersion + "\n"}},
		files: map[string][]byte{dir + "/fc.tgz": []byte("tampered")},
	}
	if c := checkFirecracker(context.Background(), Options{Sys: sys, ArtifactsDir: dir}); c.Status != Fail {
		t.Fatalf("tampered tarball should fail, got %+v", c)
	}
	sys.files = nil
	if c := checkFirecracker(context.Background(), Options{Sys: sys, ArtifactsDir: dir}); c.Status != Warn {
		t.Fatalf("absent tarball should warn, got %+v", c)
	}
	sys.run[dir+"/firecracker --version"] = fakeRun{out: "Firecracker v0.0.1"}
	if c := checkFirecracker(context.Background(), Options{Sys: sys, ArtifactsDir: dir}); c.Status != Fail {
		t.Fatalf("wrong version should fail, got %+v", c)
	}
}

func TestMissingArtifactFails(t *testing.T) {
	c := checkArtifact(Options{Sys: &fakeSys{}, ArtifactsDir: "art"}, PinnedKernel, true)
	if c.Status != Fail {
		t.Fatalf("got %s", c.Status)
	}
}

func TestReportFailed(t *testing.T) {
	if (Report{Checks: []Check{{Status: OK}, {Status: Warn}}}).Failed() {
		t.Fatal("warn must not fail the report")
	}
	if !(Report{Checks: []Check{{Status: Fail}}}).Failed() {
		t.Fatal("fail must fail the report")
	}
}

func TestRunPreflightRecordsHostKernel(t *testing.T) {
	sys := &fakeSys{files: map[string][]byte{"/proc/sys/kernel/osrelease": []byte("5.15.167.4-microsoft-standard-WSL2\n")}, run: map[string]fakeRun{}}
	r := RunPreflight(context.Background(), Options{Sys: sys, ArtifactsDir: "art"})
	if r.HostKernel != "5.15.167.4-microsoft-standard-WSL2" {
		t.Fatalf("host kernel not recorded: %q", r.HostKernel)
	}
	if byName(r, "kvm").Status != OK {
		t.Fatal("kvm should be ok with a nil error")
	}
	if !r.Failed() {
		t.Fatal("missing artifacts should fail the report")
	}
}
