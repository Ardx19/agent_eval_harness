//go:build linux

package guest

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Supervise runs as PID 1 inside the microVM. It builds the agent's root
// (read-only base + workspace layer + writable scratch, as one overlay), starts
// the model forwarder, launches the agent as the unprivileged uid, relays the
// agent's output to the host in a signed chain, and reports how it ended. It
// never returns: it powers the VM off.
func Supervise() int {
	start := time.Now()
	logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, "guest-init: "+format+"\n", a...) }
	mountAll := func() {
		must(unix.Mount("proc", "/proc", "proc", 0, ""), logf)
		must(unix.Mount("sysfs", "/sys", "sysfs", 0, ""), logf)
		if err := unix.Mount("dev", "/dev", "devtmpfs", 0, ""); err != nil && err != unix.EBUSY {
			logf("devtmpfs: %v", err) // the kernel normally mounted it already
		}
	}
	mountAll()

	if err := switchRoot(); err != nil {
		logf("root switch failed: %v", err)
		poweroff()
	}
	key, err := os.ReadFile(CtlKeyPath)
	if err != nil {
		logf("control key: %v", err)
		poweroff()
	}
	var spec RunSpec
	raw, err := os.ReadFile(RunSpecPath)
	if err == nil {
		err = json.Unmarshal(raw, &spec)
	}
	if err != nil || len(spec.Argv) == 0 {
		logf("run spec: %v", err)
		poweroff()
	}

	conn, err := DialVsock(2, ControlPort)
	if err != nil {
		logf("control channel: %v", err)
		poweroff()
	}
	relay := NewRelay(conn, []byte(strings.TrimSpace(string(key))), start)
	relay.Event("supervisor started")

	if spec.Hostname != "" {
		must(unix.Sethostname([]byte(spec.Hostname)), logf)
	}
	if err := loopbackUp(); err != nil {
		relay.Event("loopback: " + err.Error())
	}
	fwd := exec.Command("/usr/local/bin/guest-fwd", "--listen", spec.Listen, "--port", "5000")
	if err := fwd.Start(); err != nil {
		relay.Event("forwarder: " + err.Error())
		relay.Send(Frame{Stream: StreamExit, Code: 70, Reason: "forwarder"})
		WaitForAck(conn, 10*time.Second)
		poweroff()
	}
	waitListening(spec.Listen)
	relay.Event("forwarder ready")

	code, sig, reason := runAgent(spec, relay)
	relay.Send(Frame{Stream: StreamExit, Code: code, Signal: sig, Reason: reason})
	WaitForAck(conn, 45*time.Second)
	syscall.Sync()
	poweroff()
	return 0
}

func must(err error, logf func(string, ...any)) {
	if err != nil {
		logf("%v", err)
	}
}

func poweroff() {
	syscall.Sync()
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_RESTART)
	select {}
}

func blkidLabel(label string) (string, error) {
	out, err := exec.Command("/usr/sbin/blkid", "-L", label).Output()
	dev := strings.TrimSpace(string(out))
	if err != nil || dev == "" {
		return "", fmt.Errorf("no drive labelled %q", label)
	}
	return dev, nil
}

// switchRoot makes the agent's world: scratch disk (upper) over the workspace
// layer over the read-only base, mounted as one root. The mount points are
// directories that exist in the base image because the root is read-only.
func switchRoot() error {
	scratch, err := blkidLabel("scratch")
	if err != nil {
		return err
	}
	workspace, err := blkidLabel("workspace")
	if err != nil {
		return err
	}
	// an optional task toolchain layer sits between the workspace and the base
	lower := "/media:/"
	envDev, envErr := blkidLabel("env")
	if envErr == nil {
		lower = "/media:/mnt/env:/"
	}
	steps := []func() error{
		func() error { return unix.Mount(scratch, "/mnt", "ext4", 0, "") },
		func() error { return unix.Mount(workspace, "/media", "ext4", unix.MS_RDONLY, "") },
		func() error { return os.MkdirAll("/mnt/upper", 0o755) },
		func() error { return os.MkdirAll("/mnt/work", 0o755) },
		func() error {
			if envErr != nil {
				return nil
			}
			if err := os.MkdirAll("/mnt/env", 0o755); err != nil {
				return err
			}
			return unix.Mount(envDev, "/mnt/env", "ext4", unix.MS_RDONLY, "")
		},
		func() error {
			return unix.Mount("overlay", "/srv", "overlay", 0, "lowerdir="+lower+",upperdir=/mnt/upper,workdir=/mnt/work")
		},
		// /run is memory-only so nothing under it is recorded as an agent change
		func() error { return unix.Mount("tmpfs", "/srv/run", "tmpfs", 0, "mode=755") },
		func() error { return unix.Mount("/proc", "/srv/proc", "", unix.MS_MOVE, "") },
		func() error { return unix.Mount("/sys", "/srv/sys", "", unix.MS_MOVE, "") },
		func() error { return unix.Mount("/dev", "/srv/dev", "", unix.MS_MOVE, "") },
		func() error { return unix.Chdir("/srv") },
		func() error { return unix.PivotRoot(".", "mnt") },
		func() error { return unix.Chdir("/") },
		func() error { return unix.Unmount("/mnt", unix.MNT_DETACH) },
	}
	for i, s := range steps {
		if err := s(); err != nil {
			return fmt.Errorf("root switch step %d: %w", i, err)
		}
	}
	return nil
}

func loopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return err
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP | unix.IFF_RUNNING)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr)
}

func waitListening(addr string) {
	for i := 0; i < 100; i++ {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// runAgent launches the agent as the unprivileged uid with stdin closed and its
// output relayed line by line. It returns the exit code, the signal that ended
// it (if any) and "timeout" when the watchdog killed it.
func runAgent(spec RunSpec, relay *Relay) (code int, signal, reason string) {
	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Env = spec.Env
	cmd.Dir = spec.Dir
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uint32(spec.UID), Gid: uint32(spec.GID), Groups: []uint32{}},
		Setsid:     true,
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		relay.Event("stdout pipe: " + err.Error())
		return 70, "", "launch"
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		relay.Event("stderr pipe: " + err.Error())
		return 70, "", "launch"
	}
	if err := cmd.Start(); err != nil {
		relay.Event("launch: " + err.Error())
		return 127, "", "launch"
	}
	relay.Event("agent started")

	var timedOut, idled bool
	var mu sync.Mutex
	var lastOut atomic.Int64
	lastOut.Store(time.Now().UnixNano())
	done := make(chan struct{})
	defer close(done)
	if spec.IdleS > 0 {
		// An agent that prints nothing for this long is stuck (for example waiting out a
		// rate limit forever). Long tool calls still print when they finish, so the limit
		// is set well above the slowest tool.
		go func() {
			tick := time.NewTicker(10 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-done:
					return
				case <-tick.C:
					if time.Since(time.Unix(0, lastOut.Load())) > time.Duration(spec.IdleS)*time.Second {
						mu.Lock()
						idled = true
						mu.Unlock()
						relay.Event(fmt.Sprintf("idle watchdog: no agent output for %d s, ending the run", spec.IdleS))
						_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
						return
					}
				}
			}
		}()
	}
	if spec.WallS > 0 {
		timer := time.AfterFunc(time.Duration(spec.WallS)*time.Second, func() {
			mu.Lock()
			timedOut = true
			mu.Unlock()
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		})
		defer timer.Stop()
	}
	var wg sync.WaitGroup
	for _, s := range []struct {
		name string
		r    io.Reader
	}{{StreamStdout, stdout}, {StreamStderr, stderr}} {
		wg.Add(1)
		go func(name string, r io.Reader) {
			defer wg.Done()
			ReadLines(r, MaxLine, func(line string, trunc bool) {
				lastOut.Store(time.Now().UnixNano())
				relay.Line(name, line, trunc)
			})
		}(s.name, s.r)
	}
	wg.Wait()
	_ = cmd.Wait()
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
		if ws.Signaled() {
			signal = ws.Signal().String()
			code = 128 + int(ws.Signal())
		} else {
			code = ws.ExitStatus()
		}
	}
	mu.Lock()
	switch {
	case timedOut:
		reason = "timeout"
	case idled:
		reason = "idle"
	}
	mu.Unlock()
	return code, signal, reason
}
