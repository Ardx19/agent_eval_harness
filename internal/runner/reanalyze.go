package runner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agentevalharness/internal/provenance"
	"agentevalharness/internal/proxy"
	"agentevalharness/internal/skills"
	"agentevalharness/internal/trace"
)

// Reanalyze re-runs the provenance checks over a stored trial using only what was
// stored: the proxy transcript, the host's call records and the filesystem diff.
// It never runs the agent, so improving a detector costs nothing. The result is a
// copy of the trace with its provenance section replaced; the original is untouched.
//
// Findings that cannot be recomputed from the stored data are carried over:
// U1/U2 (forged calls and frames) come from live counters that are not stored.
func Reanalyze(root, traceDir string) (*trace.Trace, error) {
	raw, err := os.ReadFile(filepath.Join(traceDir, "trace.json"))
	if err != nil {
		return nil, err
	}
	tr, err := trace.Decode(raw)
	if err != nil {
		return nil, err
	}
	store, err := trace.NewStore(filepath.Join(traceDir, "blobs"))
	if err != nil {
		return nil, err
	}

	var exchanges []proxy.Exchange
	if tr.Host.ProxyTranscript == "" {
		return nil, fmt.Errorf("%s: no stored proxy transcript", traceDir)
	}
	b, err := store.Get(tr.Host.ProxyTranscript)
	if err != nil {
		return nil, fmt.Errorf("proxy transcript: %w", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var e proxy.Exchange
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("proxy transcript line: %w", err)
		}
		exchanges = append(exchanges, e)
	}

	var records []skills.CallRecord
	if len(tr.Host.ToolCalls) > 0 {
		if tr.Host.CallRecords == "" {
			return nil, fmt.Errorf("%s: this run predates stored call records, so it cannot be re-analysed", traceDir)
		}
		rb, err := store.Get(tr.Host.CallRecords)
		if err != nil {
			return nil, fmt.Errorf("call records: %w", err)
		}
		if err := json.Unmarshal(rb, &records); err != nil {
			return nil, err
		}
	}

	var manifests []*skills.Manifest
	var docs []string
	for _, name := range tr.AgentConfig.Skills {
		sk, err := LoadSkill(root, name)
		if err != nil {
			return nil, err
		}
		manifests = append(manifests, sk.Manifest)
		docs = append(docs, sk.Doc)
	}
	prompt := ""
	if tr.Trajectory != nil && len(tr.Trajectory.Steps) > 0 {
		prompt = tr.Trajectory.Steps[0].Message
	}
	f := provenance.Analyze(provenance.Input{Prompt: prompt, Exchanges: exchanges, Records: records,
		Changes: tr.Host.FilesChanged, FinalAnswer: tr.FinalAnswer, Manifests: manifests, Docs: docs})

	var carried []trace.Flag
	for _, old := range tr.Provenance.Flags {
		if old.Code == "U1" || old.Code == "U2" {
			carried = append(carried, old)
		}
	}
	f.Apply(tr)
	tr.Provenance.Flags = append(tr.Provenance.Flags, carried...)
	return tr, nil
}

// ReanalyzeRun re-analyses every trial under a run directory and writes
// trace.reanalyzed.json beside each trace. It returns, per trial, the codes
// before and after, so a detector change can be reviewed.
func ReanalyzeRun(root, runDir string) ([]ReanalysisResult, error) {
	var out []ReanalysisResult
	err := filepath.WalkDir(runDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "trace.json" {
			return err
		}
		dir := filepath.Dir(p)
		res := ReanalysisResult{Dir: dir}
		old, derr := readCodes(p)
		res.Before = old
		tr, rerr := Reanalyze(root, dir)
		if rerr != nil {
			res.Error = rerr.Error()
			out = append(out, res)
			return nil
		}
		for _, fl := range tr.Provenance.Flags {
			res.After = appendUnique(res.After, fl.Code)
		}
		enc, eerr := trace.Encode(tr)
		if eerr != nil {
			res.Error = eerr.Error()
		} else if werr := os.WriteFile(filepath.Join(dir, "trace.reanalyzed.json"), enc, 0o644); werr != nil {
			res.Error = werr.Error()
		}
		_ = derr
		out = append(out, res)
		return nil
	})
	return out, err
}

// ReanalysisResult compares a trial's findings before and after.
type ReanalysisResult struct {
	Dir           string
	Before, After []string
	Error         string
}

func readCodes(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t struct {
		Provenance struct {
			Flags []struct{ Code string } `json:"flags"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	var out []string
	for _, f := range t.Provenance.Flags {
		out = appendUnique(out, f.Code)
	}
	return out, nil
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// Describe renders one result for the terminal.
func (r ReanalysisResult) Describe() string {
	if r.Error != "" {
		return fmt.Sprintf("%-60s  skipped: %s", filepath.Base(filepath.Dir(r.Dir))+"/"+filepath.Base(r.Dir), r.Error)
	}
	return fmt.Sprintf("%-60s  %v -> %v", filepath.Base(filepath.Dir(r.Dir))+"/"+filepath.Base(r.Dir), r.Before, r.After)
}

var _ = strings.TrimSpace
