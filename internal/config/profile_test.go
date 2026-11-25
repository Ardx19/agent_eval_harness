package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "profile.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAppliesDefaults(t *testing.T) {
	p, err := Load(write(t, "agent: pi\nprovider: google\nmodel: gemini-3.5-flash-lite\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.ToolMode != "cold" || p.SkillRunner != "stub" || p.BudgetUSD != 5 || p.VMMemMiB != 1536 || p.Limits.WallS != 600 {
		t.Fatalf("defaults not applied: %+v", p)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	_, err := Load(write(t, "agent: pi\nmodel: m\ntool_modee: warm\n"))
	if err == nil || !strings.Contains(err.Error(), "tool_modee") {
		t.Fatalf("expected unknown-key error, got %v", err)
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]string{
		"bad agent":       "agent: gpt\nmodel: m\n",
		"no model":        "agent: pi\n",
		"bad tool mode":   "agent: pi\nmodel: m\ntool_mode: lukewarm\n",
		"warm and native": "agent: pi\nmodel: m\ntool_mode: warm\nskill_runner: native\n",
		"tiny vm":         "agent: pi\nmodel: m\nvm_mem_mib: 64\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, body)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

// max_refusals is read from limits, defaults to 0 (never stop), and a negative value is
// rejected.
func TestMaxRefusalsKey(t *testing.T) {
	p, err := Load(write(t, "agent: pi\nmodel: m\nlimits:\n  max_refusals: 4\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Limits.MaxRefusals != 4 {
		t.Fatalf("max_refusals = %d", p.Limits.MaxRefusals)
	}
	p, err = Load(write(t, "agent: pi\nmodel: m\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Limits.MaxRefusals != 0 {
		t.Fatalf("default max_refusals = %d, want 0", p.Limits.MaxRefusals)
	}
	if _, err := Load(write(t, "agent: pi\nmodel: m\nlimits:\n  max_refusals: -1\n")); err == nil {
		t.Fatal("negative max_refusals must be rejected")
	}
}

// A misspelled key next to max_refusals is still an unknown key.
func TestMaxRefusalsTypoIsRejected(t *testing.T) {
	_, err := Load(write(t, "agent: pi\nmodel: m\nlimits:\n  max_refusal: 3\n"))
	if err == nil || !strings.Contains(err.Error(), "max_refusal") {
		t.Fatalf("expected unknown-key error, got %v", err)
	}
}

// 2026-11-25: DeepSeek's own API needs opencode's OpenAI-compatible adapter (the guest's
// offline opencode does not know the provider) and thinking switched off per request.
func TestOpencodeProviderPackageAndModelOptions(t *testing.T) {
	p, err := Load(write(t, "agent: opencode\nprovider: deepseek\nmodel: deepseek-flash\napi: openai\n"+
		"upstream: https://api.deepseek.com\nprovider_npm: \"@ai-sdk/openai-compatible\"\n"+
		"model_options:\n  thinking:\n    type: disabled\n"))
	if err != nil {
		t.Fatal(err)
	}
	th, ok := p.ModelOptions["thinking"].(map[string]any)
	if p.ProviderNPM != "@ai-sdk/openai-compatible" || !ok || th["type"] != "disabled" {
		t.Fatalf("%+v", p)
	}
	if _, err := Load(write(t, "agent: pi\nprovider: google\nmodel: m\nmodel_options:\n  x: 1\n")); err == nil {
		t.Fatal("model_options is opencode-only")
	}
	if _, err := Load(write(t, "agent: opencode\nprovider: x\nmodel: m\nprovider_npm: left-pad\n")); err == nil {
		t.Fatal("provider_npm must be an @ai-sdk/ package")
	}
}
