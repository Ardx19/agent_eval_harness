package skills

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Request is one tool call as the host receives it.
type Request struct {
	Args  []string
	Stdin []byte
}

// Result is what the agent gets back, plus host-measured facts it cannot influence.
type Result struct {
	Stdout       []byte // scrubbed: this is exactly what the agent sees
	Stderr       []byte
	ExitCode     int
	StdoutSHA256 string // of the scrubbed stdout
	StderrSHA256 string
	Truncated    bool
	Duration     time.Duration
	UserCPU      time.Duration
	SysCPU       time.Duration
	PeakMemMB    float64 // getrusage maxrss of the tool process
	TimedOut     bool
	Signaled     string // e.g. "killed" when the process died of a signal we did not send
	OOMSuspect   bool   // killed by SIGKILL that we did not send: most likely the kernel OOM killer
}

// ToolRunner executes a tool call. Implementations: HostExec (here), and the
// in-guest native runner (U13).
type ToolRunner interface {
	Name() string
	Run(ctx context.Context, req Request) (Result, error)
}

// HostExec runs the real tool on the host.
type HostExec struct {
	Manifest    *Manifest
	MaxOutput   int    // bytes kept per stream; 0 = 4 MiB
	Placeholder string // replaces host paths in output; "" = <path>

	mu sync.Mutex // serializes calls when Manifest.Host.Serialize
}

var _ ToolRunner = (*HostExec)(nil)

func (h *HostExec) Name() string { return h.Manifest.Name }

func (h *HostExec) Run(ctx context.Context, req Request) (Result, error) {
	m := h.Manifest
	if m.Host.Serialize {
		h.mu.Lock()
		defer h.mu.Unlock()
	}
	max := h.MaxOutput
	if max == 0 {
		max = 4 << 20
	}
	placeholder := h.Placeholder
	if placeholder == "" {
		placeholder = "<path>"
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(m.Host.TimeoutS)*time.Second)
	defer cancel()
	argv := append(append([]string{}, m.Host.Command...), req.Args...)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = m.Host.Dir
	cmd.Env = minimalEnv()
	cmd.Stdin = bytes.NewReader(req.Stdin)
	var stdout, stderr capped
	stdout.max, stderr.max = max, max
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}
	var killed atomic.Bool // set by the watchdog goroutine, read after Wait
	done := make(chan struct{})
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		select {
		case <-ctx.Done():
			killed.Store(true)
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		case <-done:
		}
	}()
	waitErr := cmd.Wait()
	close(done)
	<-watchdogDone
	weKilled := killed.Load()

	res := Result{Duration: time.Since(start), Truncated: stdout.truncated || stderr.truncated}
	res.Stdout = ScrubPaths(stdout.buf.Bytes(), m.Host.ScrubPrefixes, placeholder)
	res.Stderr = ScrubPaths(stderr.buf.Bytes(), m.Host.ScrubPrefixes, placeholder)
	res.StdoutSHA256, res.StderrSHA256 = sum(res.Stdout), sum(res.Stderr)
	if ps := cmd.ProcessState; ps != nil {
		res.ExitCode = ps.ExitCode()
		if ru, ok := ps.SysUsage().(*syscall.Rusage); ok {
			res.UserCPU = time.Duration(ru.Utime.Nano())
			res.SysCPU = time.Duration(ru.Stime.Nano())
			res.PeakMemMB = float64(ru.Maxrss) / 1024 // Linux reports KiB
		}
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Signaled = ws.Signal().String()
			res.ExitCode = 128 + int(ws.Signal()) // what a shell reports: 137 for SIGKILL, not -1
			if ws.Signal() == syscall.SIGKILL && !weKilled {
				res.OOMSuspect = true
			}
		}
	}
	if weKilled && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
	}
	var ee *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &ee) {
		return res, waitErr
	}
	return res, nil
}

// minimalEnv gives the tool only what a normal login shell needs. The wrapper
// finds its own interpreter from the project's local config.
func minimalEnv() []string {
	env := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "HOME=" + os.Getenv("HOME")}
	if v := os.Getenv("TMPDIR"); v != "" {
		env = append(env, "TMPDIR="+v)
	}
	return env
}

type capped struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (c *capped) Write(p []byte) (int, error) {
	room := c.max - c.buf.Len()
	if room > len(p) {
		room = len(p)
	}
	if room > 0 {
		c.buf.Write(p[:room])
	}
	if room < len(p) {
		c.truncated = true
	}
	return len(p), nil
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
