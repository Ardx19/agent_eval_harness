package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Pinned artifacts (see docs/spikes/firecracker.md).
const (
	PinnedFirecrackerVersion   = "v1.17.0"
	PinnedFirecrackerTgzSHA256 = "06094a1108ae9e82aa4c23a775aa92758f53f1175d422270d9d6162cb9ade558"
	PinnedKernel               = "vmlinux-6.1.186"
	PinnedRootfs               = "ubuntu-24.04.ext4"
)

type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
)

type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// Report is embedded in each trace's environment block.
type Report struct {
	HostKernel string  `json:"host_kernel"`
	Checks     []Check `json:"checks"`
}

// Failed reports whether any check failed.
func (r Report) Failed() bool {
	for _, c := range r.Checks {
		if c.Status == Fail {
			return true
		}
	}
	return false
}

// Options configures a preflight run.
type Options struct {
	ArtifactsDir string
	BaseImage    string // the guest root image; "" skips the check
	Concurrency  int
	VMMemMiB     int
	Provider     string
	Model        string
	Sys          System
	// KeyCheck, when set, replaces the pi credential check (used when the model is
	// reached through opencode's credential). It must not return the secret.
	KeyCheck func() error
}

// System abstracts the host so checks are unit-testable.
type System interface {
	OpenKVM() error
	Run(ctx context.Context, name string, args ...string) (string, error)
	ReadFile(path string) ([]byte, error)
	LookPath(name string) (string, error)
	// FileSize and SHA256 exist so large artifacts (a 1.5 GB image) are checked by
	// streaming, never by reading them into the harness's memory.
	FileSize(path string) (int64, error)
	SHA256(path string) (string, error)
}

type realSystem struct{}

// NewSystem returns the real host implementation.
func NewSystem() System { return realSystem{} }

func (realSystem) OpenKVM() error {
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	return f.Close()
}

func (realSystem) Run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (realSystem) ReadFile(p string) ([]byte, error) { return os.ReadFile(p) }
func (realSystem) FileSize(p string) (int64, error) {
	st, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}
func (realSystem) SHA256(p string) (string, error)   { return hashFile(p) }
func (realSystem) LookPath(n string) (string, error) { return exec.LookPath(n) }

// RunPreflight probes the host and returns a report. It never prints secrets.
func RunPreflight(ctx context.Context, o Options) Report {
	if o.Sys == nil {
		o.Sys = NewSystem()
	}
	if o.Concurrency < 1 {
		o.Concurrency = 1
	}
	if o.VMMemMiB < 1 {
		o.VMMemMiB = 1536
	}
	r := Report{}
	if b, err := o.Sys.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		r.HostKernel = strings.TrimSpace(string(b))
	}
	r.Checks = append(r.Checks,
		checkKVM(ctx, o.Sys),
		checkFirecracker(ctx, o),
		checkArtifact(o, PinnedKernel, true),
		checkBaseImage(o),
		checkTool(o.Sys, "mkfs.ext4", Fail, "sudo apt install e2fsprogs"),
		checkTool(o.Sys, "debugfs", Warn, "sudo apt install e2fsprogs (needed for scratch-disk diff, U4)"),
		checkRAM(o),
		checkAgent(ctx, o.Sys, "pi", Fail),
		checkAgent(ctx, o.Sys, "opencode", Warn),
		checkKey(ctx, o),
	)
	return r
}

func checkKVM(ctx context.Context, sys System) Check {
	c := Check{Name: "kvm"}
	err := sys.OpenKVM()
	switch {
	case err == nil:
		c.Status, c.Detail = OK, "/dev/kvm is readable and writable"
	case errors.Is(err, fs.ErrNotExist):
		c.Status, c.Detail = Fail, "/dev/kvm does not exist"
		c.Fix = "enable nested virtualization for WSL2 (Windows 11) and restart WSL"
	case errors.Is(err, fs.ErrPermission):
		if _, e := sys.Run(ctx, "sg", "kvm", "-c", "test -r /dev/kvm -a -w /dev/kvm"); e == nil {
			c.Status = Warn
			c.Detail = "accessible via the kvm group only; this process predates the group change"
			c.Fix = "run `wsl --shutdown` from Windows and reopen the terminal (until then the harness launches Firecracker via `sg kvm`)"
		} else {
			c.Status, c.Detail = Fail, "/dev/kvm exists but this user cannot open it"
			c.Fix = "sudo usermod -aG kvm $USER, then wsl --shutdown"
		}
	default:
		c.Status, c.Detail = Fail, err.Error()
	}
	return c
}

func checkFirecracker(ctx context.Context, o Options) Check {
	c := Check{Name: "firecracker"}
	bin := filepath.Join(o.ArtifactsDir, "firecracker")
	out, err := o.Sys.Run(ctx, bin, "--version")
	if err != nil {
		c.Status, c.Detail = Fail, fmt.Sprintf("cannot run %s: %v", bin, err)
		c.Fix = "download Firecracker " + PinnedFirecrackerVersion + " into " + o.ArtifactsDir
		return c
	}
	if !strings.Contains(out, PinnedFirecrackerVersion) {
		c.Status, c.Detail = Fail, fmt.Sprintf("version mismatch: %q, want %s", firstLine(out), PinnedFirecrackerVersion)
		return c
	}
	c.Status, c.Detail = OK, firstLine(out)
	tgzSum, terr := o.Sys.SHA256(filepath.Join(o.ArtifactsDir, "fc.tgz"))
	switch {
	case terr != nil:
		c.Status, c.Detail = Warn, c.Detail+"; release tarball absent so the pinned sha256 cannot be re-verified"
	case tgzSum != PinnedFirecrackerTgzSHA256:
		c.Status, c.Detail = Fail, "release tarball sha256 does not match the pinned value"
	default:
		c.Detail += "; tarball sha256 matches pin"
	}
	return c
}

func checkArtifact(o Options, name string, hash bool) Check {
	c := Check{Name: name}
	p := filepath.Join(o.ArtifactsDir, name)
	size, err := o.Sys.FileSize(p)
	if err != nil || size == 0 {
		c.Status, c.Detail = Fail, "missing or empty: "+p
		c.Fix = "rebuild guest artifacts (see docs/spikes/firecracker.md)"
		return c
	}
	c.Status, c.Detail = OK, fmt.Sprintf("%s, %d bytes", p, size)
	if hash {
		if h, err := o.Sys.SHA256(p); err == nil {
			c.Detail += ", sha256 " + h[:12] + " (record in trace)"
		}
	}
	return c
}

func checkBaseImage(o Options) Check {
	c := Check{Name: "base-image"}
	if o.BaseImage == "" {
		c.Status, c.Detail = Warn, "no base image configured; skipped"
		return c
	}
	size, err := o.Sys.FileSize(o.BaseImage)
	if err != nil || size == 0 {
		c.Status, c.Detail = Fail, "missing or empty: "+o.BaseImage
		c.Fix = "run images/build.sh"
		return c
	}
	c.Status, c.Detail = OK, fmt.Sprintf("%s, %d bytes", o.BaseImage, size)
	return c
}

func checkTool(sys System, name string, sev Status, fix string) Check {
	c := Check{Name: name}
	if p, err := sys.LookPath(name); err == nil {
		c.Status, c.Detail = OK, p
		return c
	}
	c.Status, c.Detail, c.Fix = sev, "not found in PATH", fix
	return c
}

func checkRAM(o Options) Check {
	c := Check{Name: "memory"}
	b, err := o.Sys.ReadFile("/proc/meminfo")
	if err != nil {
		c.Status, c.Detail = Warn, "cannot read /proc/meminfo"
		return c
	}
	availMiB, ok := memAvailableMiB(string(b))
	if !ok {
		c.Status, c.Detail = Warn, "MemAvailable not found"
		return c
	}
	need := o.Concurrency * o.VMMemMiB
	c.Detail = fmt.Sprintf("%d MiB available; %d guest(s) x %d MiB = %d MiB if fully used", availMiB, o.Concurrency, o.VMMemMiB, need)
	if availMiB < need {
		c.Status = Warn
		c.Fix = "lower concurrency or vm_mem_mib (guest memory is lazily allocated, so this is a risk, not a certainty)"
	} else {
		c.Status = OK
	}
	return c
}

func checkAgent(ctx context.Context, sys System, name string, sev Status) Check {
	c := Check{Name: "agent:" + name}
	if _, err := sys.LookPath(name); err != nil {
		c.Status, c.Detail = sev, "not found in PATH"
		return c
	}
	out, err := sys.Run(ctx, name, "--version")
	if err != nil {
		c.Status, c.Detail = sev, fmt.Sprintf("--version failed: %v", err)
		return c
	}
	c.Status, c.Detail = OK, firstLine(out)
	return c
}

// checkKey asks pi whether the provider credential is ready. It never passes
// --credentials, so no secret is printed.
func checkKey(ctx context.Context, o Options) Check {
	c := Check{Name: "model-key:" + o.Provider}
	if o.Provider == "" || o.Model == "" {
		c.Status, c.Detail = Warn, "provider/model not configured; skipped"
		return c
	}
	if o.KeyCheck != nil {
		if err := o.KeyCheck(); err != nil {
			c.Status, c.Detail, c.Fix = Fail, err.Error(), "log in to the provider with opencode"
			return c
		}
		c.Status, c.Detail = OK, "ready (opencode credential)"
		return c
	}
	out, err := o.Sys.Run(ctx, "pi", "auth", "check", "--provider", o.Provider, "--model", o.Model, "--json", "--no-refresh")
	var res struct {
		Status   string `json:"status"`
		AuthType string `json:"authType"`
	}
	if jerr := json.Unmarshal([]byte(out), &res); jerr != nil {
		c.Status, c.Detail = Fail, "unexpected output from `pi auth check`"
		if err != nil {
			c.Detail = fmt.Sprintf("`pi auth check` failed: %v", err)
		}
		return c
	}
	if res.Status != "ready" {
		c.Status, c.Detail = Fail, "provider status: "+res.Status
		c.Fix = "configure the provider credential in pi"
		return c
	}
	c.Status, c.Detail = OK, fmt.Sprintf("ready (%s)", res.AuthType)
	return c
}

func memAvailableMiB(meminfo string) (int, bool) {
	for _, line := range strings.Split(meminfo, "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				if kb, err := strconv.Atoi(f[1]); err == nil {
					return kb / 1024, true
				}
			}
		}
	}
	return 0, false
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
