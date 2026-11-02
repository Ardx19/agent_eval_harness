package vm

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBootArgsAndConfig(t *testing.T) {
	c := Config{Kernel: "/k", BaseImage: "/base.ext4", Init: "/guest-init", InitArgs: []string{"x"}, VCPUs: 2, MemMiB: 1024, ScratchMiB: 64}
	if got := c.bootArgs(); got != "console=ttyS0 reboot=k panic=1 ro init=/guest-init x" {
		t.Fatalf("boot args: %q", got)
	}
	raw, _ := json.Marshal(c.firecrackerConfig("/run/scratch.img", "/s/v.sock"))
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, has := m["network-interfaces"]; has {
		t.Fatal("guest must have no network interface")
	}
	drives := m["drives"].([]any)
	if len(drives) != 2 {
		t.Fatalf("want base + scratch, got %d drives", len(drives))
	}
	base, scratch := drives[0].(map[string]any), drives[1].(map[string]any)
	if base["is_read_only"] != true || base["is_root_device"] != true {
		t.Fatalf("base image must be a read-only root device: %v", base)
	}
	if scratch["is_read_only"] == true || scratch["is_root_device"] == true {
		t.Fatalf("scratch must be writable and not root: %v", scratch)
	}
	vs := m["vsock"].(map[string]any)
	if vs["guest_cid"].(float64) != 3 || vs["uds_path"] != "/s/v.sock" {
		t.Fatalf("vsock: %v", vs)
	}
	c.ScratchMiB = 0
	raw, _ = json.Marshal(c.firecrackerConfig("", "/s/v.sock"))
	if strings.Contains(string(raw), "scratch") {
		t.Fatal("no scratch drive expected when ScratchMiB is 0")
	}
}

func TestWorkspaceDriveIsReadOnlyAndAfterScratch(t *testing.T) {
	c := Config{Kernel: "/k", BaseImage: "/b", Init: "/i", VCPUs: 1, MemMiB: 256, ScratchMiB: 8, WorkspaceImage: "/ws.ext4"}
	f := c.firecrackerConfig("/s.img", "/v")
	if len(f.Drives) != 3 || f.Drives[0].DriveID != "rootfs" || f.Drives[1].DriveID != "scratch" || f.Drives[2].DriveID != "workspace" {
		t.Fatalf("drive order must be rootfs, scratch, workspace: %+v", f.Drives)
	}
	if !f.Drives[2].IsReadOnly || f.Drives[1].IsReadOnly {
		t.Fatalf("workspace must be read-only and scratch writable: %+v", f.Drives)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote(`/a b/it's`); got != `'/a b/it'\''s'` {
		t.Fatalf("got %s", got)
	}
}

func TestCappedBuffer(t *testing.T) {
	b := newCappedBuffer(10)
	b.Write([]byte("0123456789ABCDEF"))
	if s := b.String(); !strings.HasPrefix(s, "0123456789") || !strings.Contains(s, "truncated") || strings.Contains(s, "ABC") {
		t.Fatalf("cap not applied: %q", s)
	}
	if b.panicked() {
		t.Fatal("no panic yet")
	}
	// a panic line past the cap must still be detected, even when split across writes
	b.Write([]byte("...Kernel pa"))
	b.Write([]byte("nic - not syncing"))
	if !b.panicked() {
		t.Fatal("panic marker split across writes was missed")
	}
}

func TestClassify(t *testing.T) {
	exitErr := func(code int) error {
		err := exec.Command("sh", "-c", "exit "+string(rune('0'+code))).Run()
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatal("expected ExitError")
		}
		return err
	}
	cases := []struct {
		name     string
		err      error
		timedOut bool
		panicked bool
		want     string
	}{
		{"clean", nil, false, false, ExitOK},
		{"panic reboots with exit 0", nil, false, true, ExitGuestPanic},
		{"timeout beats everything", exitErr(1), true, true, ExitTimeout},
		{"vmm crash", exitErr(1), false, false, ExitVMMCrash},
	}
	for _, tc := range cases {
		if got, _ := classify(tc.err, tc.timedOut, tc.panicked); got != tc.want {
			t.Errorf("%s: got %s want %s", tc.name, got, tc.want)
		}
	}
}

func tmpSock(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "evh-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return filepath.Join(d, "v.sock")
}

func dial(t *testing.T, base string, port int) net.Conn {
	t.Helper()
	c, err := net.Dial("unix", base+"_"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestHubDeliversPerVMOnly(t *testing.T) {
	var mu sync.Mutex
	seen := map[string][]string{}
	mk := func(tag string) string {
		base := tmpSock(t)
		h := newHub(base, 4)
		if err := h.listen(5000, func(_ context.Context, c net.Conn) {
			buf := make([]byte, 16)
			n, _ := c.Read(buf)
			mu.Lock()
			seen[tag] = append(seen[tag], string(buf[:n]))
			mu.Unlock()
			c.Write([]byte("ack"))
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(h.close)
		return base
	}
	bases := map[string]string{"A": mk("A"), "B": mk("B")}
	for tag, base := range bases {
		c := dial(t, base, 5000)
		c.Write([]byte("hi-" + tag))
		c.Read(make([]byte, 3))
		c.Close()
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen["A"]) != 1 || seen["A"][0] != "hi-A" || len(seen["B"]) != 1 || seen["B"][0] != "hi-B" {
		t.Fatalf("messages crossed VMs: %v", seen)
	}
}

func TestHubShedsConnectionFlood(t *testing.T) {
	base := tmpSock(t)
	h := newHub(base, 1)
	release := make(chan struct{})
	h.listen(5000, func(_ context.Context, c net.Conn) { <-release })
	defer h.close()
	defer close(release)
	first := dial(t, base, 5000)
	defer first.Close()
	time.Sleep(100 * time.Millisecond)
	second := dial(t, base, 5000)
	defer second.Close()
	second.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := second.Read(make([]byte, 1)); err == nil || os.IsTimeout(err) {
		t.Fatalf("second connection should be closed immediately, got %v", err)
	}
}

func TestHubCloseUnblocksStuckHandler(t *testing.T) {
	base := tmpSock(t)
	h := newHub(base, 2)
	h.listen(5000, func(_ context.Context, c net.Conn) { c.Read(make([]byte, 1)) }) // hostile guest never writes
	c := dial(t, base, 5000)
	defer c.Close()
	time.Sleep(100 * time.Millisecond)
	done := make(chan struct{})
	go func() { h.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("close hung on a blocked handler")
	}
}

func TestHubRejectsLongPathAndDuplicatePort(t *testing.T) {
	long := newHub("/tmp/"+strings.Repeat("a", 120)+"/v.sock", 1)
	if err := long.listen(5000, nil); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("expected path-length error, got %v", err)
	}
	base := tmpSock(t)
	h := newHub(base, 1)
	defer h.close()
	if err := h.listen(5000, func(context.Context, net.Conn) {}); err != nil {
		t.Fatal(err)
	}
	if err := h.listen(5000, func(context.Context, net.Conn) {}); err == nil {
		t.Fatal("duplicate port must be rejected")
	}
}

func TestNewValidates(t *testing.T) {
	f := filepath.Join(t.TempDir(), "x")
	os.WriteFile(f, []byte("x"), 0o644)
	good := Config{FirecrackerBin: f, Kernel: f, BaseImage: f, Init: "/guest-init", RunDir: t.TempDir(), VCPUs: 1, MemMiB: 256}
	bad := map[string]func(*Config){
		"missing kernel": func(c *Config) { c.Kernel = "/nonexistent" },
		"relative init":  func(c *Config) { c.Init = "guest-init" },
		"no run dir":     func(c *Config) { c.RunDir = "" },
		"tiny memory":    func(c *Config) { c.MemMiB = 16 },
		"no firecracker": func(c *Config) { c.FirecrackerBin = "" },
	}
	for name, mut := range bad {
		c := good
		mut(&c)
		if _, err := New(c); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	v, err := New(good)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Destroy()
	if err := v.Listen(5000, func(context.Context, net.Conn) {}); err != nil {
		t.Fatal(err)
	}
}
