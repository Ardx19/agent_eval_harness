// Package config loads and validates the run profile (profile.yaml).
package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Limits struct {
	WallS  int `yaml:"wall_s"`
	IdleS  int `yaml:"idle_s"` // end a run whose agent prints nothing for this long (default 900)
	Tokens int `yaml:"tokens"`
	MemMB  int `yaml:"mem_mb"`
	// MaxRefusals stops the run after this many refusals in a row from the model API
	// (0 = never stop, the breaker only pauses). Any trial that gets answers resets it.
	MaxRefusals int `yaml:"max_refusals"`
}

// Profile drives agent selection, model, limits, skill runner and realism.
type Profile struct {
	Agent       string   `yaml:"agent"`
	Provider    string   `yaml:"provider"`
	Model       string   `yaml:"model"`
	Limits      Limits   `yaml:"limits"`
	ToolMode    string   `yaml:"tool_mode"`
	SkillRunner string   `yaml:"skill_runner"`
	Realism     string   `yaml:"realism"`
	Web         bool     `yaml:"web"`
	BudgetUSD   float64  `yaml:"budget_usd"`
	Concurrency int      `yaml:"concurrency"`
	VMMemMiB    int      `yaml:"vm_mem_mib"`
	Skills      []string `yaml:"skills"`   // names under skills/<name>/ to make available
	API         string   `yaml:"api"`      // model API dialect the agent speaks: google (default) or openai
	Upstream    string   `yaml:"upstream"` // model API base URL; "" uses the dialect default
}

// Load reads path, applies defaults, rejects unknown keys and validates.
func Load(path string) (*Profile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read profile: %w", err)
	}
	var p Profile
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("parse profile %s: %w", path, err)
	}
	p.applyDefaults()
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid profile %s: %w", path, err)
	}
	return &p, nil
}

func (p *Profile) applyDefaults() {
	if p.ToolMode == "" {
		p.ToolMode = "cold"
	}
	if p.SkillRunner == "" {
		p.SkillRunner = "stub"
	}
	if p.Realism == "" {
		p.Realism = "default"
	}
	if p.BudgetUSD == 0 {
		p.BudgetUSD = 5
	}
	if p.Concurrency == 0 {
		p.Concurrency = 1
	}
	if p.VMMemMiB == 0 {
		p.VMMemMiB = 1536
	}
	if p.Limits.IdleS == 0 {
		p.Limits.IdleS = 900
	}
	if p.Limits.WallS == 0 {
		p.Limits.WallS = 600
	}
	if p.Limits.Tokens == 0 {
		p.Limits.Tokens = 200000
	}
	if p.Limits.MemMB == 0 {
		p.Limits.MemMB = 4096
	}
}

func (p *Profile) Validate() error {
	switch p.Agent {
	case "pi", "opencode", "claude":
	default:
		return fmt.Errorf("agent %q must be one of pi, opencode, claude", p.Agent)
	}
	if p.Model == "" {
		return fmt.Errorf("model is required")
	}
	if p.Upstream != "" && !strings.HasPrefix(p.Upstream, "https://") && !strings.HasPrefix(p.Upstream, "http://127.") {
		return fmt.Errorf("upstream %q must be an https URL", p.Upstream)
	}
	switch p.API {
	case "", "google", "openai":
	default:
		return fmt.Errorf("api %q must be google or openai", p.API)
	}
	switch p.ToolMode {
	case "cold", "warm":
	default:
		return fmt.Errorf("tool_mode %q must be cold or warm", p.ToolMode)
	}
	switch p.SkillRunner {
	case "stub", "native":
	default:
		return fmt.Errorf("skill_runner %q must be stub or native", p.SkillRunner)
	}
	if p.ToolMode == "warm" && p.SkillRunner != "stub" {
		return fmt.Errorf("tool_mode warm is only available with skill_runner stub")
	}
	if p.Concurrency < 1 || p.VMMemMiB < 256 || p.BudgetUSD < 0 {
		return fmt.Errorf("concurrency must be >=1, vm_mem_mib >=256, budget_usd >=0")
	}
	if p.Limits.MaxRefusals < 0 {
		return fmt.Errorf("limits.max_refusals must be >=0 (0 means never stop)")
	}
	return nil
}
