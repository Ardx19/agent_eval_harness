// Package sandbox defines the isolation boundary used for each trial.
// The only planned backend is a Firecracker microVM (see internal/vm, U3).
package sandbox

import "context"

// Spec describes one trial's sandbox.
type Spec struct {
	TrialID    string
	VCPUs      int
	MemMiB     int
	ScratchMiB int // size of the per-trial writable disk (0 = none)
	WallS      int
	RunDir     string // host directory for this trial's config, scratch disk and console log
}

// Result is what Wait reports once the sandbox has stopped.
type Result struct {
	Exit     string // ok | timeout | vmm_crash | guest_panic
	ExitCode int
	WallS    float64
}

// Sandbox is one running trial environment.
type Sandbox interface {
	Start(ctx context.Context) error
	Wait(ctx context.Context) (Result, error)
	Destroy() error
}

// Backend creates sandboxes. A fallback backend can implement this without
// touching the provenance join engine.
type Backend interface {
	Create(ctx context.Context, spec Spec) (Sandbox, error)
}
