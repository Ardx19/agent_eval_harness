package runner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agentevalharness/internal/config"
	"agentevalharness/internal/trace"
)

type rig struct {
	fc, kernel, base string
	sg               bool
	repo             string
}

func needRig(t *testing.T) rig {
	t.Helper()
	repo, _ := filepath.Abs(filepath.Join("..", ".."))
	r := rig{repo: repo,
		fc:     filepath.Join(repo, "spike/firecracker/firecracker"),
		kernel: filepath.Join(repo, "spike/firecracker/vmlinux-6.1.186"),
		base:   filepath.Join(repo, "images/build/rootfs.ext4")}
	for _, p := range []string{r.fc, r.kernel, r.base, filepath.Join(repo, "images/build/entries.jsonl")} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("missing %s (run images/build.sh)", p)
		}
	}
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err == nil {
		f.Close()
	} else if exec.Command("sg", "kvm", "-c", "test -r /dev/kvm -a -w /dev/kvm").Run() != nil {
		t.Skip("no access to /dev/kvm")
	}
	return r
}

// a repository-shaped root holding the toy skill, sharing the real image and spike directories
func toyRoot(t *testing.T, r rig) string {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "skills", "toy"), 0o755)
	m, _ := os.ReadFile(filepath.Join(r.repo, "testdata/fixtures/toy_skill/manifest.json"))
	var mm map[string]any
	json.Unmarshal(m, &mm)
	mm["host"].(map[string]any)["dir"] = filepath.Join(r.repo, "testdata/fixtures/toy_skill")
	mm["host"].(map[string]any)["scrub_prefixes"] = []string{filepath.Join(r.repo, "testdata/fixtures/toy_skill")}
	b, _ := json.Marshal(mm)
	os.WriteFile(filepath.Join(root, "skills/toy/manifest.json"), b, 0o644)
	os.WriteFile(filepath.Join(root, "skills/toy/SKILL.template.md"), []byte("# Toy\n\nRun `{{.Command}} smiles` to get the molecule.\n"), 0o644)
	os.Symlink(filepath.Join(r.repo, "images"), filepath.Join(root, "images"))
	os.Symlink(filepath.Join(r.repo, "spike"), filepath.Join(root, "spike"))
	return root
}

func fakeModel() (*httptest.Server, *int32) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		i := atomic.AddInt32(&n, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if i%2 == 1 { // odd requests: call the tool; even requests: answer
			io.WriteString(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"bash","args":{"command":"scripts/toy.sh smiles"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":20,"totalTokenCount":140}}`+"\r\n\r\n")
			return
		}
		io.WriteString(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"The toy tool returned the molecule F/C=C/F."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":220,"candidatesTokenCount":30,"totalTokenCount":250}}`+"\r\n\r\n")
	}))
	return srv, &n
}

func options(t *testing.T, r rig, root, prompts string, n int, budget float64, up string) Options {
	t.Helper()
	pp := filepath.Join(t.TempDir(), "profile.yaml")
	os.WriteFile(pp, []byte("agent: pi\nprovider: google\nmodel: gemini-3.5-flash-lite\nvm_mem_mib: 1024\nskills: [toy]\nlimits:\n  wall_s: 150\n  tokens: 100000\n"), 0o644)
	prof, err := config.Load(pp)
	if err != nil {
		t.Fatal(err)
	}
	return Options{Profile: prof, PromptsPath: prompts, OutDir: t.TempDir(), Root: root, N: n, Seed: 7, BudgetUSD: budget,
		FirecrackerBin: r.fc, Kernel: r.kernel, BaseImage: r.base,
		Entries: filepath.Join(root, "images/build/entries.jsonl"), ImageManifest: filepath.Join(root, "images/build/manifest.json"),
		Upstream: up, APIKey: "AIzaSy-test-key-not-a-real-one-0123456789", Log: io.Discard}
}

func TestRunWritesATracePerTrial(t *testing.T) {
	r := needRig(t)
	root := toyRoot(t, r)
	up, _ := fakeModel()
	defer up.Close()
	pd := t.TempDir()
	prompts := filepath.Join(pd, "prompts.jsonl")
	os.WriteFile(filepath.Join(pd, "notes.txt"), []byte("an input file\n"), 0o644)
	os.WriteFile(prompts, []byte(`{"id":"a","prompt":"What does the toy tool report?","inputs":["notes.txt"]}`+"\n"+`{"id":"b","prompt":"Tell me the toy molecule.","tags":["x"]}`+"\n"), 0o644)

	sum, err := Run(context.Background(), options(t, r, root, prompts, 2, 5, up.URL))
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Trials) != 4 || sum.Stopped != "" {
		t.Fatalf("2 tasks x 2 trials: %+v", sum)
	}
	seen := map[string]bool{}
	for _, tr := range sum.Trials {
		if tr.Error != "" || tr.Status != "ok" || tr.Steps != 3 || len(tr.Flags) != 0 {
			t.Fatalf("trial %+v", tr)
		}
		b, err := os.ReadFile(tr.TracePath)
		if err != nil {
			t.Fatal(err)
		}
		dec, err := trace.Decode(b)
		if err != nil {
			t.Fatalf("%s: %v", tr.TracePath, err)
		}
		if dec.TaskID != tr.TaskID || dec.Trial != tr.Trial || dec.RunID != sum.RunID || len(dec.Host.ToolCalls) != 1 {
			t.Fatalf("%+v", dec)
		}
		seen[dec.TaskID+"/"+string(rune('0'+dec.Trial))] = true
		if _, err := os.Stat(filepath.Join(filepath.Dir(tr.TracePath), "console.log")); err != nil {
			t.Error("the console log should be kept for diagnosing infra problems")
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(tr.TracePath), "workspace.ext4")); err == nil {
			t.Error("the workspace image is scratch and should be removed")
		}
	}
	if len(seen) != 4 {
		t.Fatalf("every trial gets its own trace: %v", seen)
	}
	if b, err := os.ReadFile(filepath.Join(sum.Dir, "summary.json")); err != nil || !strings.Contains(string(b), `"total_cost_usd"`) {
		t.Fatalf("summary.json: %v", err)
	}
	if sum.TotalCost <= 0 {
		t.Fatal("cost should be accumulated from the agent's own figures")
	}
}

func TestSessionBudgetStopsTheRun(t *testing.T) {
	r := needRig(t)
	root := toyRoot(t, r)
	up, _ := fakeModel()
	defer up.Close()
	prompts := filepath.Join(t.TempDir(), "prompts.jsonl")
	os.WriteFile(prompts, []byte(`{"id":"a","prompt":"x"}`+"\n"+`{"id":"b","prompt":"y"}`+"\n"), 0o644)
	sum, err := Run(context.Background(), options(t, r, root, prompts, 1, 0.0001, up.URL))
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Trials) != 1 || !strings.Contains(sum.Stopped, "budget") {
		t.Fatalf("the run must stop before starting a trial it cannot afford: %+v", sum)
	}
}

func TestABadInputDoesNotStopTheOtherTasks(t *testing.T) {
	r := needRig(t)
	root := toyRoot(t, r)
	up, _ := fakeModel()
	defer up.Close()
	prompts := filepath.Join(t.TempDir(), "prompts.jsonl")
	os.WriteFile(prompts, []byte(`{"id":"broken","prompt":"x","inputs":["does-not-exist.txt"]}`+"\n"+`{"id":"fine","prompt":"y"}`+"\n"), 0o644)
	sum, err := Run(context.Background(), options(t, r, root, prompts, 1, 5, up.URL))
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Trials) != 2 || sum.Trials[0].Error == "" || sum.Trials[1].Status != "ok" {
		t.Fatalf("%+v", sum.Trials)
	}
}
