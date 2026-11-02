package skills

import (
	"strings"

	"agentevalharness/internal/trace"
)

// ToTrace converts a host-side call record into the trace's host section.
func (c CallRecord) ToTrace() trace.HostToolCall {
	return trace.HostToolCall{
		CallID: c.CallID, PID: c.PID, PPID: c.PPID, Tool: c.Tool, Argv: c.Argv,
		ArgsValid: c.ArgsValid, ValidationError: strings.Join(c.Validation.Errors, "; "),
		TStart: c.TStart, DurationS: c.Result.Duration.Seconds(), ExitCode: c.Result.ExitCode,
		StdoutSHA256: c.Result.StdoutSHA256, StderrSHA256: c.Result.StderrSHA256,
		ReceiptValid: true, // only calls with a valid signature are recorded as calls
		CPUS:         (c.Result.UserCPU + c.Result.SysCPU).Seconds(), PeakMemMB: c.Result.PeakMemMB,
		OOMKilled: c.Result.OOMSuspect,
	}
}
