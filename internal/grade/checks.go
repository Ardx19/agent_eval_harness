package grade

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"agentevalharness/internal/trace"
)

// Check is one deterministic test of a trace. The vocabulary is small on purpose;
// anything that needs judgement is a "needs_review" check, which a human or a
// separate model resolves, and which never counts as a pass by itself.
type Check struct {
	Type     string   `json:"type"`
	Tool     string   `json:"tool,omitempty"`
	Values   []string `json:"values,omitempty"`   // status_in
	Patterns []string `json:"patterns,omitempty"` // text checks: case-insensitive substrings, or "re:<regex>"
	Codes    []string `json:"codes,omitempty"`    // provenance checks: which flag codes
	Max      float64  `json:"max,omitempty"`
	Min      int      `json:"min,omitempty"`
	Note     string   `json:"note,omitempty"`
}

// Expectation is the host-only description of what a good run looks like. The
// entry with id "*" holds defaults applied to every task.
type Expectation struct {
	ID       string  `json:"id"`
	Category string  `json:"category,omitempty"`
	Rubric   string  `json:"rubric,omitempty"`
	Checks   []Check `json:"checks"`
}

// CheckResult is the outcome of one check.
type CheckResult struct {
	Type   string `json:"type"`
	Passed bool   `json:"passed"`
	Review bool   `json:"needs_review,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// LoadExpectations reads a JSONL file of expectations.
func LoadExpectations(path string) (map[string]Expectation, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]Expectation{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Expectation
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		if _, dup := out[e.ID]; dup {
			return nil, fmt.Errorf("%s:%d: duplicate id %q", path, n, e.ID)
		}
		for _, c := range e.Checks {
			if err := validCheck(c); err != nil {
				return nil, fmt.Errorf("%s:%d (%s): %w", path, n, e.ID, err)
			}
		}
		out[e.ID] = e
	}
	return out, sc.Err()
}

var knownChecks = map[string]bool{
	"status_in": true, "tool_called": true, "tool_not_called": true, "tool_call_count_max": true,
	"answer_contains_any": true, "answer_contains_all": true, "answer_not_contains": true, "answer_regex": true,
	"tool_output_contains_any": true, "no_provenance_flags": true, "claims_supported": true, "no_forged_output": true,
	"answer_grounded": true, "model_saw_matches": true, "tool_call_fast": true, "message_before_first_tool": true, "cost_usd_max": true, "wall_s_max": true, "steps_max": true,
	"verify_resolved": true, "needs_review": true,
}

func validCheck(c Check) error {
	if !knownChecks[c.Type] {
		return fmt.Errorf("unknown check type %q", c.Type)
	}
	for _, p := range c.Patterns {
		if strings.HasPrefix(p, "re:") {
			if _, err := regexp.Compile("(?i)" + p[3:]); err != nil {
				return fmt.Errorf("check %s: bad pattern %q: %v", c.Type, p, err)
			}
		}
	}
	return nil
}

func matches(text, pattern string) bool {
	if strings.HasPrefix(pattern, "re:") {
		re, err := regexp.Compile("(?i)" + pattern[3:])
		return err == nil && re.MatchString(text)
	}
	return strings.Contains(strings.ToLower(text), strings.ToLower(pattern))
}

func flagCodes(t *trace.Trace) map[string]bool {
	m := map[string]bool{}
	for _, f := range t.Provenance.Flags {
		m[f.Code] = true
	}
	return m
}

func noFlags(t *trace.Trace, codes ...string) (bool, string) {
	have := flagCodes(t)
	var hit []string
	for _, c := range codes {
		if have[c] {
			hit = append(hit, c)
		}
	}
	if len(hit) > 0 {
		return false, "flagged: " + strings.Join(hit, ", ")
	}
	return true, ""
}

// toolOutputs returns the stdout of every host-recorded tool call.
func toolOutputs(t *trace.Trace, traceDir string) []string {
	if traceDir == "" {
		return nil
	}
	store, err := trace.NewStore(filepath.Join(traceDir, "blobs"))
	if err != nil {
		return nil
	}
	var out []string
	for _, c := range t.Host.ToolCalls {
		if b, err := store.Get(trace.Ref("sha256:" + c.StdoutSHA256)); err == nil {
			out = append(out, string(b))
		}
	}
	return out
}

// EvalChecks runs checks against a trace. traceDir lets output checks read the
// stored tool outputs; it may be empty.
func EvalChecks(t *trace.Trace, traceDir string, checks []Check) []CheckResult {
	var res []CheckResult
	for _, c := range checks {
		r := CheckResult{Type: c.Type}
		answer := t.FinalAnswer
		calls := func() int {
			n := 0
			for _, h := range t.Host.ToolCalls {
				if c.Tool == "" || h.Tool == c.Tool {
					n++
				}
			}
			return n
		}
		switch c.Type {
		case "needs_review":
			r.Review, r.Detail = true, c.Note
		case "status_in":
			r.Passed = false
			for _, v := range c.Values {
				if t.Status == v {
					r.Passed = true
				}
			}
			r.Detail = "status " + t.Status
		case "tool_called":
			n := calls()
			r.Passed, r.Detail = n >= max(c.Min, 1), fmt.Sprintf("%d call(s)", n)
		case "tool_not_called":
			n := calls()
			r.Passed, r.Detail = n == 0, fmt.Sprintf("%d call(s)", n)
		case "tool_call_count_max":
			n := calls()
			r.Passed, r.Detail = float64(n) <= c.Max, fmt.Sprintf("%d call(s), max %.0f", n, c.Max)
		case "answer_contains_any":
			for _, p := range c.Patterns {
				if matches(answer, p) {
					r.Passed, r.Detail = true, "matched "+p
					break
				}
			}
			if !r.Passed {
				r.Detail = "none of the patterns appear in the answer"
			}
		case "answer_contains_all":
			r.Passed = true
			for _, p := range c.Patterns {
				if !matches(answer, p) {
					r.Passed, r.Detail = false, "missing "+p
					break
				}
			}
		case "answer_not_contains":
			r.Passed = true
			for _, p := range c.Patterns {
				if matches(answer, p) {
					r.Passed, r.Detail = false, "the answer contains "+p
					break
				}
			}
		case "answer_regex":
			r.Passed = len(c.Patterns) > 0 && matches(answer, "re:"+c.Patterns[0])
		case "message_before_first_tool":
			r.Detail = "no matching message before the first tool call"
			if t.Trajectory != nil {
			steps:
				for _, st := range t.Trajectory.Steps {
					if st.Source != "agent" {
						continue
					}
					for _, p := range c.Patterns {
						if matches(st.Message, p) {
							r.Passed, r.Detail = true, fmt.Sprintf("step %d says %s", st.StepID, p)
							break steps
						}
					}
					if len(st.ToolCalls) > 0 {
						break
					}
				}
			}
		case "tool_call_fast":
			r.Detail = "no tool call"
			for _, h := range t.Host.ToolCalls {
				if (c.Tool == "" || h.Tool == c.Tool) && h.DurationS <= c.Max {
					r.Passed, r.Detail = true, fmt.Sprintf("a call took %.1f s", h.DurationS)
					break
				}
				r.Detail = fmt.Sprintf("the fastest call took more than %.0f s", c.Max)
			}
		case "tool_output_contains_any":
			outs := toolOutputs(t, traceDir)
			for _, o := range outs {
				for _, p := range c.Patterns {
					if matches(o, p) {
						r.Passed, r.Detail = true, "matched "+p
					}
				}
			}
			if !r.Passed {
				r.Detail = fmt.Sprintf("no pattern in %d tool output(s)", len(outs))
			}
		case "no_provenance_flags":
			codes := c.Codes
			if len(codes) == 0 {
				codes = []string{"F1", "F2", "F3", "F4", "F5", "F6", "U1", "U2"}
			}
			r.Passed, r.Detail = noFlags(t, codes...)
		case "claims_supported":
			r.Passed, r.Detail = noFlags(t, "F1", "F4", "F5")
		case "no_forged_output":
			r.Passed, r.Detail = noFlags(t, "F2", "U1", "U2")
		case "answer_grounded":
			r.Passed, r.Detail = noFlags(t, "F3")
		case "model_saw_matches":
			r.Passed, r.Detail = noFlags(t, "F6")
		case "cost_usd_max":
			r.Passed, r.Detail = t.Metrics.CostUSD <= c.Max, fmt.Sprintf("$%.4f", t.Metrics.CostUSD)
		case "wall_s_max":
			r.Passed, r.Detail = t.Metrics.WallS <= c.Max, fmt.Sprintf("%.0f s", t.Metrics.WallS)
		case "steps_max":
			r.Passed, r.Detail = float64(t.Metrics.Steps) <= c.Max, fmt.Sprintf("%d steps", t.Metrics.Steps)
		case "verify_resolved":
			r.Passed = t.Verify != nil && t.Verify.Resolved
			r.Detail = "no hidden-test result"
			if t.Verify != nil {
				r.Detail = "verify " + t.Verify.Status
			}
		}
		res = append(res, r)
	}
	return res
}

// Verdict folds check results: fail if any check failed, needs_review if every
// automatic check passed but something is left for a reviewer, pass otherwise.
func Verdict(rs []CheckResult) string {
	if len(rs) == 0 {
		return "needs_review"
	}
	review := false
	for _, r := range rs {
		switch {
		case r.Review:
			review = true
		case !r.Passed:
			return "fail"
		}
	}
	if review {
		return "needs_review"
	}
	return "pass"
}
