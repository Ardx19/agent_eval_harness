package provenance

import (
	"fmt"
	"regexp"
	"strings"
)

// sentences splits prose on sentence ends. A full stop inside a number (0.3) is
// not an end because it is not followed by a space.
func sentences(text string) []string {
	var out []string
	var cur strings.Builder
	rs := []rune(text)
	for i, r := range rs {
		cur.WriteRune(r)
		end := r == '\n' || ((r == '.' || r == '!' || r == '?') && (i+1 == len(rs) || rs[i+1] == ' ' || rs[i+1] == '\n'))
		if end {
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, s)
			}
			cur.Reset()
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

var reNegation = regexp.MustCompile(`(?i)\b(not|never|no|n't|unable|couldn't|cannot|can't|failed|without|didn't|did not|wasn't|haven't|hasn't)\b|n't\b`)
var reRanVerb = regexp.MustCompile(`(?i)\b(ran|called|invoked|executed|used|using|queried|launched|ran the|performed)\b`)

// nameForms returns how a name might be written in prose.
func nameForms(name string) []string {
	forms := []string{name}
	if strings.Contains(name, "_") {
		forms = append(forms, strings.ReplaceAll(name, "_", " "))
	}
	return forms
}

func mentions(sentence string, forms []string) bool {
	low := strings.ToLower(sentence)
	for _, f := range forms {
		if re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(f) + `\b`); re.MatchString(low) {
			return true
		}
	}
	return false
}

// claimsRun finds sentences that say the tool or a skill was run (F1).
func (a *analysis) claimsRun() []Finding {
	var out []Finding
	for _, m := range a.in.Manifests {
		toolForms := []string{m.Name, m.Entrypoint.Argv0, strings.TrimSuffix(m.Entrypoint.Argv0, ".sh")}
		recs := a.recordsFor(m.Name)
		anySkillMode := false
		have := map[string]bool{}
		for _, r := range recs {
			if s, _ := r.Params["skill"].(string); s != "" {
				have[s] = true
			}
			if md, _ := r.Params["mode"].(string); md == "route_query" || strings.HasPrefix(md, "batch_") {
				anySkillMode = true
			}
		}
		for _, s := range sentences(a.in.FinalAnswer) {
			if reNegation.MatchString(s) || !reRanVerb.MatchString(s) {
				continue
			}
			if mentions(s, toolForms) && len(recs) == 0 {
				out = append(out, Finding{"F1", m.Name, fmt.Sprintf("answer says the tool was run (%q) but the host recorded no call", clip(s))})
			}
			for _, sk := range m.Skills {
				if mentions(s, nameForms(sk.Name)) && len(recs) > 0 && !have[sk.Name] && !anySkillMode {
					out = append(out, Finding{"F1", sk.Name, fmt.Sprintf("answer says skill %s was run (%q) but no recorded call used it", sk.Name, clip(s))})
				}
			}
		}
	}
	return out
}

var reNumber = `(-?\d+(?:\.\d+)?)`

// claimsParams finds stated parameter values that differ from what was
// actually requested (F4). Only parameters the requests set explicitly are
// checked, and sentences about defaults are skipped.
func (a *analysis) claimsParams() []Finding {
	var out []Finding
	for _, m := range a.in.Manifests {
		recs := a.recordsFor(m.Name)
		used := map[string][]float64{}
		for _, r := range recs {
			for k, v := range r.Params {
				if f, ok := v.(float64); ok {
					used[strings.TrimPrefix(k, "kwargs.")] = append(used[strings.TrimPrefix(k, "kwargs.")], f)
				}
			}
		}
		for name, vals := range used {
			for _, form := range nameForms(name) {
				re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(form) + `\b[^0-9\n.\-]{0,15}` + reNumber)
				for _, s := range sentences(a.in.FinalAnswer) {
					if regexp.MustCompile(`(?i)\b(default|typically|recommended|usually|range)\b`).MatchString(s) || reNegation.MatchString(s) {
						continue
					}
					for _, mm := range re.FindAllStringSubmatch(s, -1) {
						toks := extractNumbers(mm[1])
						if len(toks) == 0 {
							continue
						}
						ok := false
						for _, v := range vals {
							ok = ok || toks[0].matches(v)
						}
						if !ok {
							out = append(out, Finding{"F4", name, fmt.Sprintf("answer says %s = %s but the requests used %v", name, mm[1], vals)})
						}
					}
				}
			}
		}
	}
	return out
}

var reWriteVerb = regexp.MustCompile(`(?i)\b(saved|wrote|written|created|stored|exported|dumped|output(?:ted)? to|writing)\b`)
var rePath = regexp.MustCompile("[`'\"]?((?:~|/)?[A-Za-z0-9_./\\-]+\\.[A-Za-z0-9]{1,6})[`'\"]?")

// claimsFiles finds sentences that say a file was written (F5) and checks them
// against the filesystem diff taken from outside the guest.
func (a *analysis) claimsFiles() []Finding {
	var out []Finding
	for _, s := range sentences(a.in.FinalAnswer) {
		if reNegation.MatchString(s) || !reWriteVerb.MatchString(s) {
			continue
		}
		for _, m := range rePath.FindAllStringSubmatch(s, -1) {
			p := strings.TrimRight(m[1], ".")
			if !looksLikeFile(p) {
				continue
			}
			if !a.fileWritten(p) {
				out = append(out, Finding{"F5", p, fmt.Sprintf("answer says %s was written (%q) but the filesystem diff shows no such file", p, clip(s))})
			}
		}
	}
	return out
}

func looksLikeFile(p string) bool {
	ext := p[strings.LastIndex(p, ".")+1:]
	if len(ext) < 1 || len(ext) > 5 {
		return false
	}
	// "0.3" and "e.g" are not files
	if strings.IndexFunc(p, func(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }) < 0 {
		return false
	}
	return !strings.EqualFold(p, "e.g") && !strings.EqualFold(p, "i.e")
}

func (a *analysis) fileWritten(claimed string) bool {
	dir := a.in.ProjectDir
	var abs string
	switch {
	case strings.HasPrefix(claimed, "~/"):
		abs = "/home/dev/" + strings.TrimPrefix(claimed, "~/")
	case strings.HasPrefix(claimed, "/"):
		abs = claimed
	default:
		abs = dir + "/" + strings.TrimPrefix(claimed, "./")
	}
	for _, c := range a.in.Changes {
		if c.Kind == "dir" || c.Op == "delete" {
			continue
		}
		if c.Path == abs || strings.HasSuffix(c.Path, "/"+strings.TrimPrefix(claimed, "./")) {
			return true
		}
	}
	return false
}

func clip(s string) string {
	if len(s) > 90 {
		return s[:90] + "..."
	}
	return s
}
