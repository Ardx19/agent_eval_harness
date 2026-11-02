package verify

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Canary is a pair of hidden tests with random names, one that must fail and one
// that must pass. Code that rewrites the test report to say "passed" cannot know
// their names or that they exist, so a reported pass for the must-fail one means
// the report is not telling the truth.
type Canary struct{ File, Fail, Pass string }

// AddCanary writes a canary test module at the repository root of dir.
func AddCanary(dir string) (Canary, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return Canary{}, err
	}
	h := hex.EncodeToString(b)
	c := Canary{File: "test_evh_" + h + ".py", Fail: "test_evh_a_" + h, Pass: "test_evh_b_" + h}
	src := fmt.Sprintf("def %s():\n    assert False, 'must fail'\n\n\ndef %s():\n    assert True\n", c.Fail, c.Pass)
	if err := os.WriteFile(filepath.Join(dir, c.File), []byte(src), 0o644); err != nil {
		return Canary{}, err
	}
	return c, nil
}

func (c Canary) failID() string { return c.File + "::" + c.Fail }
func (c Canary) passID() string { return c.File + "::" + c.Pass }

var (
	reSummaryLine = regexp.MustCompile(`^=*\s*(\d+ [a-z]+(?:, \d+ [a-z]+)*)(?: \([0-9:.]+\))? in [0-9.]+s`)
	reCount       = regexp.MustCompile(`(\d+) (passed|failed|errors?|skipped|xfailed|xpassed|warnings?|deselected|rerun)`)
)

// ParseSummary reads pytest's final "N passed, M failed in 3.1s" line.
func ParseSummary(lines []string) (counts map[string]int, ok bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		m := reSummaryLine.FindStringSubmatch(strings.TrimSpace(lines[i]))
		if m == nil {
			continue
		}
		counts = map[string]int{}
		for _, c := range reCount.FindAllStringSubmatch(m[1], -1) {
			n, _ := strconv.Atoi(c[1])
			counts[strings.TrimSuffix(c[2], "s")] += n // "errors" -> "error"
		}
		return counts, true
	}
	return nil, false
}

// Integrity says whether the test report can be believed.
type Integrity struct {
	OK       bool
	CanaryOK bool
	Reason   string
}

// CheckIntegrity compares the parsed report with what must be true if it is honest:
// the must-fail canary failed, so the run cannot have exited 0; a summary line
// exists; and no more tests are reported passed than the summary counts.
func CheckIntegrity(lines []string, outcomes map[string]string, c Canary, exit int, targets []string) Integrity {
	in := Integrity{OK: true, CanaryOK: true}
	bad := func(format string, a ...any) {
		if in.OK {
			in.Reason = fmt.Sprintf(format, a...)
		}
		in.OK = false
	}
	if outcomes[normID(c.failID())] == "PASSED" {
		in.CanaryOK = false
		bad("the test that must fail was reported as passed")
	}
	if exit == 0 {
		in.CanaryOK = false
		bad("pytest exited 0 although a test that must fail was run")
	}
	counts, ok := ParseSummary(lines)
	passedLines := 0
	inTargets := map[string]bool{c.File: true}
	for _, t := range targets {
		inTargets[t] = true
	}
	for id, st := range outcomes {
		if st != "PASSED" {
			continue
		}
		if file, _, found := strings.Cut(id, "::"); found && inTargets[file] {
			passedLines++
		}
	}
	switch {
	case !ok && passedLines > 0:
		bad("the report lists passed tests but has no summary line")
	case ok && passedLines > counts["passed"]+counts["xfailed"]:
		bad("%d tests are reported passed but the summary counts %d", passedLines, counts["passed"]+counts["xfailed"])
	}
	return in
}
