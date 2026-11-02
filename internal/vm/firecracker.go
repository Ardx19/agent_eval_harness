// Package vm drives Firecracker microVMs: one VM per trial, no network device,
// host services reachable only over per-VM vsock Unix sockets.
package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"agentevalharness/internal/sandbox"
)

// Exit classifications reported in sandbox.Result.Exit.
const (
	ExitOK         = "ok"
	ExitTimeout    = "timeout"
	ExitVMMCrash   = "vmm_crash"
	ExitGuestPanic = "guest_panic"
)

// Config describes one trial's VM.
type Config struct {
	FirecrackerBin string
	Kernel         string
	BaseImage      string   // shared, attached read-only
	Init           string   // guest init path, e.g. /guest-init
	InitArgs       []string // extra kernel args after init= (used by tests)
	RunDir         string   // config, scratch disk (host side)
	SockDir        string   // short directory for vsock sockets; "" = create under /tmp
	VCPUs          int
	MemMiB         int
	ScratchMiB     int
	WorkspaceImage string // optional read-only layer stacked above the base image
	EnvImage       string // optional read-only layer between the base image and the workspace (task toolchain)
	WallS          int
	UseSG          bool // launch via `sg kvm` when this process lacks the kvm group
	MaxConns       int  // concurrent vsock connections across all ports
	ConsoleBytes   int
	KeepRunDir     bool
}

type drive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

type fcConfig struct {
	BootSource struct {
		KernelImagePath string `json:"kernel_image_path"`
		BootArgs        string `json:"boot_args"`
	} `json:"boot-source"`
	Drives        []drive `json:"drives"`
	MachineConfig struct {
		VCPUCount  int `json:"vcpu_count"`
		MemSizeMiB int `json:"mem_size_mib"`
	} `json:"machine-config"`
	Vsock struct {
		GuestCID int    `json:"guest_cid"`
		UDSPath  string `json:"uds_path"`
	} `json:"vsock"`
}

func (c Config) bootArgs() string {
	args := []string{"console=ttyS0", "reboot=k", "panic=1", "ro", "init=" + c.Init}
	return strings.Join(append(args, c.InitArgs...), " ")
}

// firecrackerConfig builds the --config-file document. There is deliberately
// no "network-interfaces" key: the guest has no NIC.
func (c Config) firecrackerConfig(scratchPath, vsockPath string) fcConfig {
	var f fcConfig
	f.BootSource.KernelImagePath = c.Kernel
	f.BootSource.BootArgs = c.bootArgs()
	f.Drives = []drive{{DriveID: "rootfs", PathOnHost: c.BaseImage, IsRootDevice: true, IsReadOnly: true}}
	if c.ScratchMiB > 0 {
		f.Drives = append(f.Drives, drive{DriveID: "scratch", PathOnHost: scratchPath})
	}
	if c.EnvImage != "" {
		f.Drives = append(f.Drives, drive{DriveID: "env", PathOnHost: c.EnvImage, IsReadOnly: true})
	}
	if c.WorkspaceImage != "" {
		f.Drives = append(f.Drives, drive{DriveID: "workspace", PathOnHost: c.WorkspaceImage, IsReadOnly: true})
	}
	f.MachineConfig.VCPUCount, f.MachineConfig.MemSizeMiB = c.VCPUs, c.MemMiB
	f.Vsock.GuestCID, f.Vsock.UDSPath = 3, vsockPath
	return f
}

// VM is one microVM. It implements sandbox.Sandbox.
type VM struct {
	cfg     Config
	sockDir string
	hub     *hub
	console *cappedBuffer

	mu       sync.Mutex
	cmd      *exec.Cmd
	done     chan struct{}
	waitErr  error
	started  time.Time
	ended    time.Time
	timedOut bool
	timer    *time.Timer
}

var _ sandbox.Sandbox = (*VM)(nil)

// New validates the config and prepares host-side directories and sockets.
func New(cfg Config) (*VM, error) {
	for name, p := range map[string]string{"firecracker": cfg.FirecrackerBin, "kernel": cfg.Kernel, "base image": cfg.BaseImage} {
		if p == "" {
			return nil, fmt.Errorf("%s path is required", name)
		}
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	if cfg.Init == "" || !strings.HasPrefix(cfg.Init, "/") {
		return nil, fmt.Errorf("init must be an absolute guest path, got %q", cfg.Init)
	}
	if cfg.RunDir == "" {
		return nil, errors.New("run dir is required")
	}
	if cfg.VCPUs < 1 || cfg.MemMiB < 128 {
		return nil, fmt.Errorf("need vcpus>=1 and mem_mib>=128 (got %d, %d)", cfg.VCPUs, cfg.MemMiB)
	}
	if cfg.MaxConns < 1 {
		cfg.MaxConns = 16
	}
	if cfg.ConsoleBytes < 1 {
		cfg.ConsoleBytes = 1 << 20
	}
	if cfg.WorkspaceImage != "" {
		if _, err := os.Stat(cfg.WorkspaceImage); err != nil {
			return nil, fmt.Errorf("workspace image: %w", err)
		}
	}
	if cfg.EnvImage != "" {
		if _, err := os.Stat(cfg.EnvImage); err != nil {
			return nil, fmt.Errorf("env image: %w", err)
		}
	}
	for _, p := range []*string{&cfg.FirecrackerBin, &cfg.Kernel, &cfg.BaseImage, &cfg.RunDir, &cfg.WorkspaceImage, &cfg.EnvImage} {
		if *p == "" {
			continue
		}
		abs, err := filepath.Abs(*p)
		if err != nil {
			return nil, err
		}
		*p = abs
	}
	sockDir := cfg.SockDir
	if sockDir == "" {
		d, err := os.MkdirTemp("/tmp", "evh-")
		if err != nil {
			return nil, err
		}
		sockDir = d
	} else if err := os.MkdirAll(sockDir, 0o700); err != nil {
		return nil, err
	}
	return &VM{cfg: cfg, sockDir: sockDir, hub: newHub(filepath.Join(sockDir, "v.sock"), cfg.MaxConns), console: newCappedBuffer(cfg.ConsoleBytes)}, nil
}

// Listen registers a host service on a vsock port. Call before Start.
func (v *VM) Listen(port int, h Handler) error {
	v.mu.Lock()
	started := v.cmd != nil
	v.mu.Unlock()
	if started {
		return errors.New("listeners must be registered before Start")
	}
	return v.hub.listen(port, h)
}

// Start boots the VM. Listeners must already be registered.
func (v *VM) Start(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cmd != nil {
		return errors.New("vm already started")
	}
	if err := os.MkdirAll(v.cfg.RunDir, 0o755); err != nil {
		return err
	}
	scratch := filepath.Join(v.cfg.RunDir, "scratch.img")
	if v.cfg.ScratchMiB > 0 {
		if err := makeSparse(scratch, int64(v.cfg.ScratchMiB)<<20); err != nil {
			return fmt.Errorf("scratch disk: %w", err)
		}
		if err := sandbox.FormatExt4(ctx, sandbox.NewSystem(), scratch, "scratch"); err != nil {
			return fmt.Errorf("scratch disk: %w", err)
		}
	}
	cfgPath := filepath.Join(v.cfg.RunDir, "firecracker.json")
	raw, err := json.MarshalIndent(v.cfg.firecrackerConfig(scratch, filepath.Join(v.sockDir, "v.sock")), "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		return err
	}

	var cmd *exec.Cmd
	if v.cfg.UseSG {
		cmd = exec.Command("sg", "kvm", "-c", "exec "+shellQuote(v.cfg.FirecrackerBin)+" --no-api --config-file "+shellQuote(cfgPath))
	} else {
		cmd = exec.Command(v.cfg.FirecrackerBin, "--no-api", "--config-file", cfgPath)
	}
	cmd.Stdout, cmd.Stderr = v.console, v.console
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start firecracker: %w", err)
	}
	v.cmd, v.done, v.started = cmd, make(chan struct{}), time.Now()
	go func() {
		err := cmd.Wait()
		v.mu.Lock()
		v.waitErr, v.ended = err, time.Now()
		v.mu.Unlock()
		close(v.done)
	}()
	if v.cfg.WallS > 0 {
		v.timer = time.AfterFunc(time.Duration(v.cfg.WallS)*time.Second, func() {
			v.mu.Lock()
			v.timedOut = true
			v.mu.Unlock()
			v.kill()
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			v.kill()
		case <-v.done:
		}
	}()
	return nil
}

// Wait blocks until the VM stops and classifies how it ended.
func (v *VM) Wait(ctx context.Context) (sandbox.Result, error) {
	v.mu.Lock()
	done := v.done
	v.mu.Unlock()
	if done == nil {
		return sandbox.Result{}, errors.New("vm not started")
	}
	select {
	case <-done:
	case <-ctx.Done():
		v.kill()
		<-done
		return sandbox.Result{}, ctx.Err()
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	exit, code := classify(v.waitErr, v.timedOut, v.console.panicked())
	return sandbox.Result{Exit: exit, ExitCode: code, WallS: v.ended.Sub(v.started).Seconds()}, nil
}

// classify maps process outcome to an exit class. A guest panic reboots (panic=1)
// and Firecracker then exits 0, so the console is the only reliable signal.
func classify(waitErr error, timedOut, panicked bool) (string, int) {
	code := 0
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		code = ee.ExitCode()
	}
	switch {
	case timedOut:
		return ExitTimeout, code
	case panicked:
		return ExitGuestPanic, code
	case waitErr == nil:
		return ExitOK, 0
	default:
		return ExitVMMCrash, code
	}
}

// Console returns captured VMM and guest console output (diagnostics only).
func (v *VM) Console() string { return v.console.String() }

// SockDir is the directory holding this VM's vsock sockets.
func (v *VM) SockDir() string { return v.sockDir }

// Destroy kills the VM if running, stops listeners and removes host state.
func (v *VM) Destroy() error {
	v.kill()
	v.mu.Lock()
	done, timer := v.done, v.timer
	v.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			return errors.New("firecracker did not exit after SIGKILL")
		}
	}
	v.hub.close()
	err := os.RemoveAll(v.sockDir)
	if !v.cfg.KeepRunDir {
		if e := os.RemoveAll(v.cfg.RunDir); err == nil {
			err = e
		}
	}
	return err
}

// kill sends SIGKILL to the whole process group (sg, sh and firecracker).
func (v *VM) kill() {
	v.mu.Lock()
	cmd := v.cmd
	v.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func makeSparse(path string, size int64) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Truncate(size)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Backend creates VMs from a fixed set of pinned artifacts.
type Backend struct {
	FirecrackerBin string
	Kernel         string
	BaseImage      string
	Init           string
	UseSG          bool
	MaxConns       int
}

var _ sandbox.Backend = Backend{}

// Create returns an unstarted *VM. Callers needing vsock services type-assert
// to *VM and call Listen before Start.
func (b Backend) Create(_ context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	return New(Config{
		FirecrackerBin: b.FirecrackerBin, Kernel: b.Kernel, BaseImage: b.BaseImage, Init: b.Init,
		RunDir: spec.RunDir, VCPUs: spec.VCPUs, MemMiB: spec.MemMiB, ScratchMiB: spec.ScratchMiB,
		WallS: spec.WallS, UseSG: b.UseSG, MaxConns: b.MaxConns,
	})
}
