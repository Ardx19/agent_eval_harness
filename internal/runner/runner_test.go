package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadTasks(t *testing.T) {
	d := t.TempDir()
	p := writeFile(t, d, "p.jsonl", `# comment
{"id":"t1","prompt":"hello","tags":["a"]}

{"id":"t2.b-c","prompt":"x","inputs":["data/m.smi"],"limits":{"wall_s":30}}
`)
	tasks, err := LoadTasks(p)
	if err != nil || len(tasks) != 2 || tasks[1].Limits.WallS != 30 || tasks[0].Tags[0] != "a" {
		t.Fatalf("%+v %v", tasks, err)
	}
	bad := map[string]string{
		"duplicate id":  `{"id":"a","prompt":"x"}` + "\n" + `{"id":"a","prompt":"y"}`,
		"unsafe id":     `{"id":"../x","prompt":"x"}`,
		"empty prompt":  `{"id":"a","prompt":"  "}`,
		"unknown field": `{"id":"a","prompt":"x","expected":"secret"}`,
		"not json":      `nope`,
		"empty file":    ``,
	}
	for name, body := range bad {
		if _, err := LoadTasks(writeFile(t, t.TempDir(), "p.jsonl", body)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// the runner must not be able to read what the grader will check against
func TestPromptsFileCannotCarryExpectations(t *testing.T) {
	_, err := LoadTasks(writeFile(t, t.TempDir(), "p.jsonl", `{"id":"a","prompt":"x","expect":{"status":"ok"}}`))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expectations in the prompts file must be refused: %v", err)
	}
}

func TestStageInputs(t *testing.T) {
	d := t.TempDir()
	writeFile(t, d, "data/mol.smi", "CCO\n")
	writeFile(t, d, "data/sub/b.txt", "b")
	dst, err := StageInputs(d, []string{"data"})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dst)
	if b, _ := os.ReadFile(filepath.Join(dst, "data", "mol.smi")); string(b) != "CCO\n" {
		t.Fatal("input not copied")
	}
	if _, err := os.Stat(filepath.Join(dst, "data", "sub", "b.txt")); err != nil {
		t.Fatal("nested input not copied")
	}
	for _, in := range []string{"../secret", "/etc/passwd", "data/../../x", ".."} {
		if _, err := StageInputs(d, []string{in}); err == nil {
			t.Errorf("input %q must be refused", in)
		}
	}
	if _, err := StageInputs(d, []string{"missing.txt"}); err == nil {
		t.Error("a missing input must be an error")
	}
}

func TestLoadSkillRefusesALeakyDoc(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "x")
	os.MkdirAll(skillDir, 0o755)
	good, _ := os.ReadFile("../../testdata/fixtures/toy_skill/manifest.json")
	writeFile(t, skillDir, "manifest.json", string(good))
	writeFile(t, skillDir, "SKILL.template.md", "# x\nRun it from {{.Command}}.\n")
	if _, err := LoadSkill(root, "toy"); err == nil {
		t.Fatal("the skill is named by its directory; a mismatch must fail")
	}
	os.Rename(skillDir, filepath.Join(root, "skills", "toy"))
	skillDir = filepath.Join(root, "skills", "toy")
	if _, err := LoadSkill(root, "toy"); err != nil {
		t.Fatalf("a clean doc must load: %v", err)
	}
	writeFile(t, skillDir, "SKILL.template.md", "# x\nThe code lives under /home/someone/vendor/impl.py; fix it if buggy.\n")
	if _, err := LoadSkill(root, "toy"); err == nil || !strings.Contains(err.Error(), "leaks") {
		t.Fatalf("a doc that leaks must be refused before any agent sees it: %v", err)
	}
}
