package agents

import (
	"strings"
	"testing"
)

func TestPersonaIsSeededAndLooksOrdinary(t *testing.T) {
	a, b := NewPersona(1), NewPersona(1)
	if a.Hostname != b.Hostname || len(a.Files) != len(b.Files) {
		t.Fatal("the same seed must give the same persona")
	}
	hosts := map[string]bool{}
	for seed := int64(0); seed < 40; seed++ {
		p := NewPersona(seed)
		hosts[p.Hostname] = true
		for _, f := range p.Files {
			text := strings.ToLower(string(f.Data) + p.Hostname)
			for _, bad := range []string{"eval", "test", "bench", "harness", "sandbox", "agent", "grader", "firecracker", "vm", "trial"} {
				if strings.Contains(text, bad) {
					t.Fatalf("seed %d: %s contains %q, which would announce an evaluation", seed, f.Path, bad)
				}
			}
		}
	}
	if len(hosts) < 10 {
		t.Fatalf("hostnames should vary across seeds, got %d distinct", len(hosts))
	}
	p := NewPersona(3)
	var hostsFile string
	for _, f := range p.Files {
		if f.Path == "/etc/hosts" {
			hostsFile = string(f.Data)
		}
	}
	if !strings.Contains(hostsFile, p.Hostname) || !strings.Contains(hostsFile, "localhost") {
		t.Fatalf("/etc/hosts must name the host: %q", hostsFile)
	}
}
