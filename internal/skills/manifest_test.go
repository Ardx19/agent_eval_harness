package skills

import (
	"os"
	"strings"
	"testing"
)

const retroManifest = "../../skills/retrosynthesis/manifest.json"
const retroTemplate = "../../skills/retrosynthesis/SKILL.template.md"

func loadRetro(t *testing.T) *Manifest {
	t.Helper()
	m, err := LoadManifest(retroManifest)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestManifestLoadsAndChecks(t *testing.T) {
	m := loadRetro(t)
	if len(m.Skills) != 11 || len(m.Modes) != 6 {
		t.Fatalf("expected 11 skills and 6 modes, got %d and %d", len(m.Skills), len(m.Modes))
	}
	for _, f := range m.CLI.Flags {
		if f.Name == "--self-check" {
			t.Fatal("--self-check is not part of the documented interface and must not be forwardable")
		}
	}
	bad := *m
	bad.Skills = append(append([]Skill{}, m.Skills...), m.Skills[0])
	if err := bad.Check(); err == nil {
		t.Fatal("a duplicate skill must be rejected")
	}
	bad = *m
	bad.Modes = []Mode{{Name: "x", Required: []string{"nonexistent"}}}
	if err := bad.Check(); err == nil {
		t.Fatal("a mode requiring an undeclared key must be rejected")
	}
}

func TestValidateRequest(t *testing.T) {
	m := loadRetro(t)
	cases := []struct {
		name, req string
		valid     bool
		errPart   string
		warnPart  string
	}{
		{"list skills", `{"mode":"list_skills"}`, true, "", ""},
		{"route generation", `{"mode":"run_skill","skill":"route_generation","kwargs":{"smiles":"CCO","max_routes":5}}`, true, "", ""},
		{"feasibility with thresholds", `{"mode":"run_skill","skill":"synthesis_feasibility","kwargs":{"smiles":"CCO","min_confidence":0.3,"min_availability":0.5,"max_steps":5}}`, true, "", ""},
		{"natural language", `{"mode":"route_query","query":"is aspirin synthesizable?"}`, true, "", ""},
		{"batch", `{"mode":"batch_generate_routes","smiles_list":["CCO","CCN"],"n_workers":2,"pool_size":2}`, true, "", ""},
		{"unknown kwarg is only a warning", `{"mode":"run_skill","skill":"route_generation","kwargs":{"smiles":"CCO","colour":"red"}}`, true, "", "kwargs.colour"},
		{"not json", `not json`, false, "not a JSON object", ""},
		{"no mode", `{}`, false, "mode is required", ""},
		{"unknown mode", `{"mode":"explode"}`, false, "unknown mode", ""},
		{"unknown skill", `{"mode":"run_skill","skill":"teleport","kwargs":{}}`, false, "unknown skill", ""},
		{"missing smiles", `{"mode":"run_skill","skill":"route_generation","kwargs":{}}`, false, "kwargs.smiles is required", ""},
		{"missing kwargs", `{"mode":"run_skill","skill":"route_generation"}`, false, "kwargs is required", ""},
		{"wrong type", `{"mode":"run_skill","skill":"route_generation","kwargs":{"smiles":42}}`, false, "kwargs.smiles must be string", ""},
		{"non-integer", `{"mode":"run_skill","skill":"route_generation","kwargs":{"smiles":"CCO","max_routes":2.5}}`, false, "must be integer", ""},
		{"out of range", `{"mode":"run_skill","skill":"synthesis_feasibility","kwargs":{"smiles":"CCO","min_confidence":1.5}}`, false, "<= 1", ""},
		{"below minimum", `{"mode":"run_skill","skill":"route_generation","kwargs":{"smiles":"CCO","max_routes":0}}`, false, ">= 1", ""},
		{"bad enum", `{"mode":"run_skill","skill":"synthesis_feasibility","kwargs":{"smiles":"CCO","best_route_metric":"coolest"}}`, false, "one of", ""},
		{"batch without list", `{"mode":"batch_assess_feasibility"}`, false, "smiles_list is required", ""},
		{"help without skill", `{"mode":"help_skill"}`, false, "skill is required", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := m.ValidateRequest([]byte(tc.req))
			if v.Valid() != tc.valid {
				t.Fatalf("valid=%v want %v: %+v", v.Valid(), tc.valid, v)
			}
			if tc.errPart != "" && !strings.Contains(strings.Join(v.Errors, "|"), tc.errPart) {
				t.Fatalf("errors %v should mention %q", v.Errors, tc.errPart)
			}
			if tc.warnPart != "" && !strings.Contains(strings.Join(v.Warnings, "|"), tc.warnPart) {
				t.Fatalf("warnings %v should mention %q", v.Warnings, tc.warnPart)
			}
		})
	}
}

func renderRetro(t *testing.T) string {
	t.Helper()
	m := loadRetro(t)
	tb, err := os.ReadFile(retroTemplate)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := m.RenderDoc(string(tb))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestGeneratedDocIsCleanAndComplete(t *testing.T) {
	m := loadRetro(t)
	doc := renderRetro(t)
	if hits := m.LeakCheck(doc); len(hits) > 0 {
		t.Fatalf("the agent-facing doc leaks:\n%s", strings.Join(hits, "\n"))
	}
	if !strings.HasPrefix(doc, "---\nname: retrosynthesis\ndescription: Use AiZynthFinder") {
		t.Fatalf("front matter missing: %.120s", doc)
	}
	for _, s := range m.Skills {
		if !strings.Contains(doc, "`"+s.Name+"`") {
			t.Errorf("skill %s missing from the doc", s.Name)
		}
		for _, p := range s.Params {
			if !strings.Contains(doc, "| `"+p.Name+"` |") {
				t.Errorf("parameter %s.%s missing from the generated reference", s.Name, p.Name)
			}
		}
	}
	for _, must := range []string{"scripts/run_retrosynthesis.sh --request req.json", "about 40 s", "Never write custom threshold logic",
		"`min_confidence` | number | no | `0.3`", "Never present a generated route as a validated"} {
		if !strings.Contains(doc, must) {
			t.Errorf("doc is missing %q", must)
		}
	}
	if strings.Contains(doc, "{{") {
		t.Error("unexpanded template action")
	}
}

func TestGeneratedDocGolden(t *testing.T) {
	doc := renderRetro(t)
	golden := "../../skills/retrosynthesis/SKILL.generated.md"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		os.WriteFile(golden, []byte(doc), 0o644)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("missing golden (run with UPDATE_GOLDEN=1): %v", err)
	}
	if string(want) != doc {
		t.Fatal("generated doc changed; review it and rerun with UPDATE_GOLDEN=1 if intended")
	}
}

// The original dev doc is what motivated the generated one. The leak filter must
// catch the problems in it, or the filter is not doing its job.
func TestLeakCheckRejectsTheOriginalDevDoc(t *testing.T) {
	raw, err := os.ReadFile("/home/aryandutt/home/BTP/Pi/.pi/skills/retrosynthesis/SKILL.md")
	if err != nil {
		t.Skip("original dev doc not present on this machine")
	}
	hits := loadRetro(t).LeakCheck(string(raw))
	joined := strings.Join(hits, "\n")
	for _, want := range []string{"vendor", "FIXES.md", "numpy", ".pi/skills", "fix it"} { // text of the offending lines
		if !strings.Contains(joined, want) {
			t.Errorf("filter missed %s in the original dev doc", want)
		}
	}
	if len(hits) < 10 {
		t.Errorf("expected many hits in the dev doc, got %d", len(hits))
	}
}

func TestLeakCheckPatterns(t *testing.T) {
	m := &Manifest{LeakPatterns: []string{`secretproject`}}
	for _, bad := range []string{"run /home/me/tool", "see vendor/skills", "edit core.py", "uses conda", "secretproject lives here", "if buggy, fix it minimally"} {
		if len(m.LeakCheck(bad)) == 0 {
			t.Errorf("%q should be flagged", bad)
		}
	}
	for _, ok := range []string{"scripts/run_retrosynthesis.sh --request req.json", "AiZynthFinder-based retrosynthesis", "report the error you received"} {
		if h := m.LeakCheck(ok); len(h) != 0 {
			t.Errorf("%q should be clean: %v", ok, h)
		}
	}
}

func TestScrubPaths(t *testing.T) {
	prefixes := []string{"/home/u/proj", "/home/u/miniconda3/envs/e"}
	in := `Traceback:
  File "/home/u/proj/scripts/cli.py", line 9
  File "/home/u/miniconda3/envs/e/lib/python3.10/site-packages/x.py", line 3
ConfigurationError: data_dir=/home/u/proj/.pi/skills/r/vendor/data; also /home/other/secret/w.onnx and /tmp/x`
	out := string(ScrubPaths([]byte(in), prefixes, "<path>"))
	for _, leak := range []string{"/home/u", "miniconda", "/home/other", "/tmp/x", ".pi/skills", "vendor", "site-packages", "cli.py", "w.onnx"} {
		if strings.Contains(out, leak) {
			t.Errorf("scrub left %q in:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, `File "<path>", line 9`) || !strings.Contains(out, "ConfigurationError: data_dir=<path>; also <path> and <path>") {
		t.Errorf("whole path tokens should be replaced and the sentence kept intact:\n%s", out)
	}
	// chemistry must survive: SMILES contain slashes
	smi := `{"smiles": "F/C=C/F", "route": "C/C=C/C", "n": 3}`
	if string(ScrubPaths([]byte(smi), prefixes, "<path>")) != smi {
		t.Error("scrubbing must not touch SMILES")
	}
}
