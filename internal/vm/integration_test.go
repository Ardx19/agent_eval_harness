package vm

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type rig struct {
	fc, kernel, base string
	sg               bool
}

// needRig skips unless the pinned artifacts, the built image and KVM access exist.
func needRig(t *testing.T) rig {
	t.Helper()
	r := rig{
		fc:     filepath.Join("..", "..", "spike", "firecracker", "firecracker"),
		kernel: filepath.Join("..", "..", "spike", "firecracker", "vmlinux-6.1.186"),
		base:   filepath.Join("..", "..", "images", "build", "rootfs.ext4"),
	}
	for _, p := range []string{r.fc, r.kernel, r.base} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("missing %s (run images/build.sh)", p)
		}
	}
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err == nil {
		f.Close()
		return r
	}
	if exec.Command("sg", "kvm", "-c", "test -r /dev/kvm -a -w /dev/kvm").Run() == nil {
		r.sg = true
		return r
	}
	t.Skip("no access to /dev/kvm")
	return r
}

func (r rig) cfg(t *testing.T, init string, args ...string) Config {
	return Config{FirecrackerBin: r.fc, Kernel: r.kernel, BaseImage: r.base, Init: init, InitArgs: args,
		RunDir: filepath.Join(t.TempDir(), "run"), VCPUs: 2, MemMiB: 512, ScratchMiB: 16, WallS: 60, UseSG: r.sg}
}

func procGone(pgid int) bool { return syscall.Kill(-pgid, 0) == syscall.ESRCH }

func TestBootHandshakeAndCleanExit(t *testing.T) {
	r := needRig(t)
	v, err := New(r.cfg(t, "/placeholder-init"))
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	if err := v.Listen(5000, func(_ context.Context, c net.Conn) {
		line, _ := bufio.NewReader(c).ReadString('\n')
		c.Write([]byte("ack-from-host\n"))
		got <- strings.TrimSpace(line)
	}); err != nil {
		t.Fatal(err)
	}
	if err := v.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := v.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Exit != ExitOK {
		t.Fatalf("exit %s code %d\n%s", res.Exit, res.ExitCode, v.Console())
	}
	select {
	case line := <-got:
		if line != "hello-from-guest-init" {
			t.Fatalf("unexpected handshake %q", line)
		}
	default:
		t.Fatalf("host never saw the guest's vsock hello\n%s", v.Console())
	}
	if !strings.Contains(v.Console(), "SPIKE: DONE") {
		t.Fatalf("guest init did not finish\n%s", v.Console())
	}
	if _, err := os.Stat(filepath.Join(v.cfg.RunDir, "scratch.img")); err != nil {
		t.Fatalf("scratch disk missing: %v", err)
	}
	t.Logf("booted and handshook in %.2fs", res.WallS)
	sock := v.SockDir()
	pgid := v.cmd.Process.Pid
	if err := v.Destroy(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatal("socket dir not cleaned up")
	}
	if _, err := os.Stat(v.cfg.RunDir); !os.IsNotExist(err) {
		t.Fatal("run dir not cleaned up")
	}
	if !procGone(pgid) {
		t.Fatal("firecracker process group still alive")
	}
}

func TestGuestPanicIsClassified(t *testing.T) {
	r := needRig(t)
	v, err := New(r.cfg(t, "/usr/bin/true")) // init exits -> "Attempted to kill init" panic
	if err != nil {
		t.Fatal(err)
	}
	defer v.Destroy()
	if err := v.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, _ := v.Wait(context.Background())
	if res.Exit != ExitGuestPanic {
		t.Fatalf("want guest_panic, got %s (code %d)\n%s", res.Exit, res.ExitCode, v.Console())
	}
}

func TestWatchdogMarksTimeout(t *testing.T) {
	r := needRig(t)
	cfg := r.cfg(t, "/usr/bin/sleep", "1000")
	cfg.WallS = 4
	v, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Destroy()
	if err := v.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, _ := v.Wait(context.Background())
	if res.Exit != ExitTimeout {
		t.Fatalf("want timeout, got %s\n%s", res.Exit, v.Console())
	}
	if res.WallS < 3.5 || res.WallS > 15 {
		t.Fatalf("unexpected wall time %.2fs", res.WallS)
	}
}

func TestDestroyKillsRunningVM(t *testing.T) {
	r := needRig(t)
	v, err := New(r.cfg(t, "/usr/bin/sleep", "1000"))
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	pgid := v.cmd.Process.Pid
	if procGone(pgid) {
		t.Fatal("vm should still be running")
	}
	if err := v.Destroy(); err != nil {
		t.Fatal(err)
	}
	if !procGone(pgid) {
		t.Fatal("process group survived Destroy")
	}
}

func TestTwoConcurrentVMsStayIsolated(t *testing.T) {
	r := needRig(t)
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	socks := map[string]string{}
	for _, tag := range []string{"A", "B"} {
		v, err := New(r.cfg(t, "/placeholder-init"))
		if err != nil {
			t.Fatal(err)
		}
		defer v.Destroy()
		tag := tag
		socks[tag] = v.SockDir()
		v.Listen(5000, func(_ context.Context, c net.Conn) {
			bufio.NewReader(c).ReadString('\n')
			c.Write([]byte("ack\n"))
			mu.Lock()
			seen[tag]++
			mu.Unlock()
		})
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := v.Start(context.Background()); err != nil {
				t.Error(err)
				return
			}
			if res, _ := v.Wait(context.Background()); res.Exit != ExitOK {
				t.Errorf("%s: exit %s\n%s", tag, res.Exit, v.Console())
			}
		}()
	}
	wg.Wait()
	if socks["A"] == socks["B"] {
		t.Fatal("VMs share a socket directory")
	}
	mu.Lock()
	defer mu.Unlock()
	if seen["A"] != 1 || seen["B"] != 1 {
		t.Fatalf("each VM's service should see exactly its own guest: %v", seen)
	}
}
