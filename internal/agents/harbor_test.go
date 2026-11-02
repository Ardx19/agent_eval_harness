package agents

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"agentevalharness/internal/trace"
)

// harborValidates runs Harbor's own validator on the trace's trajectory section.
// Skipped when the validator is not installed (tools/setup-harbor.sh).
func harborValidates(t *testing.T, tr *trace.Trace) (bool, string) {
	t.Helper()
	py := "../../tools/harbor-venv/bin/python"
	if _, err := os.Stat(py); err != nil {
		return true, "validator not installed"
	}
	b, _ := json.MarshalIndent(tr.Trajectory, "", "  ")
	f := filepath.Join(t.TempDir(), "trajectory.json")
	os.WriteFile(f, b, 0o644)
	out, err := exec.Command(py, "-m", "harbor.utils.trajectory_validator", "--no-validate-images", f).CombinedOutput()
	return err == nil, string(out)
}
