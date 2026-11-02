package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var trialIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// NewRunDir creates <base>/<trialID> for one trial. It refuses ids that could
// escape base and refuses to reuse an existing directory, so two trials can
// never share state.
func NewRunDir(base, trialID string) (string, error) {
	if !trialIDPattern.MatchString(trialID) || trialID == "." || trialID == ".." {
		return "", fmt.Errorf("invalid trial id %q", trialID)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	dir := filepath.Join(base, trialID)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", fmt.Errorf("create run dir: %w", err)
	}
	return dir, nil
}
