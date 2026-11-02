// Package skills describes the tools an agent can call and runs them on the host.
// A host-only manifest is the single source of truth: it drives argument
// validation, the generated agent-facing SKILL.md, and where the tool is installed.
package skills

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

// Manifest is host-only. The agent never sees it, only the SKILL.md rendered from it.
type Manifest struct {
	SchemaVersion int        `json:"schema_version"`
	Name          string     `json:"name"`
	Description   string     `json:"description"` // SKILL.md front matter
	Entrypoint    Entrypoint `json:"entrypoint"`
	Host          HostSpec   `json:"host"`
	CLI           CLISpec    `json:"cli"`
	Modes         []Mode     `json:"modes"`
	TopLevel      []Param    `json:"top_level"` // request keys valid at the top level
	Skills        []Skill    `json:"skills"`
	LeakPatterns  []string   `json:"leak_patterns"` // regexes that must not appear in the agent-facing doc
	// OutputSignatures are regexes that identify text as this tool's own output
	// (e.g. its JSON envelope). Text matching one that no recorded call produced
	// was made by something else: a forgery.
	OutputSignatures []string `json:"output_signatures"`
}

// Entrypoint is how the agent invokes the tool inside the guest.
type Entrypoint struct {
	Path   string `json:"path"`   // relative to the project, e.g. scripts/run_retrosynthesis.sh
	Argv0  string `json:"argv0"`  // basename the stub dispatches on
	Target string `json:"target"` // what the symlink points at in the guest
}

// HostSpec is how the real tool runs on the host.
type HostSpec struct {
	Dir           string   `json:"dir"`
	Command       []string `json:"command"`
	TimeoutS      int      `json:"timeout_s"`
	Serialize     bool     `json:"serialize"` // one call at a time (the tool rewrites a shared config on every call)
	ScrubPrefixes []string `json:"scrub_prefixes"`
}

// CLISpec lists the only argument shapes the stub will forward.
type CLISpec struct {
	Flags           []Flag `json:"flags"`
	StdinJSON       bool   `json:"stdin_json"`
	AllowPositional bool   `json:"allow_positional,omitempty"` // arguments that are not flags
}

type Flag struct {
	Name           string `json:"name"`
	TakesFile      bool   `json:"takes_file"`                // value is a path the stub reads (with the caller's privileges)
	ImpliesRequest string `json:"implies_request,omitempty"` // JSON request this flag stands for, used to validate it
}

type Mode struct {
	Name     string   `json:"name"`
	Required []string `json:"required"`
	Summary  string   `json:"summary"`
}

type Param struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"` // string integer number boolean array object
	Required bool     `json:"required"`
	Default  any      `json:"default,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
	Notes    string   `json:"notes,omitempty"`
}

type Skill struct {
	Name     string  `json:"name"`
	Summary  string  `json:"summary"`
	Speed    string  `json:"speed,omitempty"`
	Params   []Param `json:"params"`
	Response string  `json:"response,omitempty"`
	Notes    string  `json:"notes,omitempty"`
}

var validTypes = map[string]bool{"string": true, "integer": true, "number": true, "boolean": true, "array": true, "object": true}

// LoadManifest reads and sanity-checks a manifest file.
func LoadManifest(path string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	return &m, m.Check()
}

// Check reports a manifest that could not drive validation or doc generation.
func (m *Manifest) Check() error {
	if m.Name == "" || m.Description == "" {
		return fmt.Errorf("manifest needs a name and a description")
	}
	if m.Entrypoint.Path == "" || m.Entrypoint.Argv0 == "" {
		return fmt.Errorf("manifest %s: entrypoint.path and entrypoint.argv0 are required", m.Name)
	}
	if len(m.Host.Command) == 0 || m.Host.TimeoutS <= 0 {
		return fmt.Errorf("manifest %s: host.command and a positive host.timeout_s are required", m.Name)
	}
	check := func(where string, ps []Param) error {
		seen := map[string]bool{}
		for _, p := range ps {
			if p.Name == "" || !validTypes[p.Type] {
				return fmt.Errorf("%s: parameter %q has missing name or invalid type %q", where, p.Name, p.Type)
			}
			if seen[p.Name] {
				return fmt.Errorf("%s: duplicate parameter %q", where, p.Name)
			}
			seen[p.Name] = true
		}
		return nil
	}
	if err := check("top_level", m.TopLevel); err != nil {
		return err
	}
	names := map[string]bool{}
	for _, s := range m.Skills {
		if names[s.Name] {
			return fmt.Errorf("duplicate skill %q", s.Name)
		}
		names[s.Name] = true
		if err := check("skill "+s.Name, s.Params); err != nil {
			return err
		}
	}
	top := map[string]bool{}
	for _, p := range m.TopLevel {
		top[p.Name] = true
	}
	for _, md := range m.Modes {
		for _, r := range md.Required {
			if !top[r] {
				return fmt.Errorf("mode %s requires %q, which is not a top-level parameter", md.Name, r)
			}
		}
	}
	for _, pat := range append(append([]string{}, m.LeakPatterns...), m.OutputSignatures...) {
		if _, err := regexp.Compile(pat); err != nil {
			return fmt.Errorf("pattern %q: %w", pat, err)
		}
	}
	return nil
}

// Validation is the result of checking a request against the schema. Errors
// make args_valid false; warnings (unknown keys) do not, because the doc may
// not list every optional field the real tool accepts.
type Validation struct {
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

func (v Validation) Valid() bool { return len(v.Errors) == 0 }

// ValidateRequest checks a request JSON document against the manifest schema.
func (m *Manifest) ValidateRequest(raw []byte) Validation {
	var v Validation
	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		v.Errors = append(v.Errors, "request is not a JSON object")
		return v
	}
	mode, _ := req["mode"].(string)
	if mode == "" {
		v.Errors = append(v.Errors, "mode is required")
		return v
	}
	var md *Mode
	for i := range m.Modes {
		if m.Modes[i].Name == mode {
			md = &m.Modes[i]
		}
	}
	if md == nil {
		v.Errors = append(v.Errors, fmt.Sprintf("unknown mode %q", mode))
		return v
	}
	v.checkParams("", req, m.TopLevel, true)
	for _, r := range md.Required {
		if _, ok := req[r]; !ok {
			v.Errors = append(v.Errors, fmt.Sprintf("%s is required for mode %s", r, mode))
		}
	}
	if mode == "run_skill" || mode == "help_skill" {
		name, _ := req["skill"].(string)
		sk := m.skill(name)
		if name != "" && sk == nil {
			v.Errors = append(v.Errors, fmt.Sprintf("unknown skill %q", name))
		}
		if mode == "run_skill" && sk != nil {
			kw, _ := req["kwargs"].(map[string]any)
			if kw == nil {
				kw = map[string]any{}
			}
			v.checkParams("kwargs.", kw, sk.Params, false)
		}
	}
	return v
}

func (m *Manifest) skill(name string) *Skill {
	for i := range m.Skills {
		if m.Skills[i].Name == name {
			return &m.Skills[i]
		}
	}
	return nil
}

func (v *Validation) checkParams(prefix string, got map[string]any, spec []Param, topLevel bool) {
	known := map[string]Param{}
	for _, p := range spec {
		known[p.Name] = p
		if val, ok := got[p.Name]; ok {
			v.checkValue(prefix+p.Name, val, p)
		} else if p.Required && !topLevel {
			v.Errors = append(v.Errors, fmt.Sprintf("%s%s is required", prefix, p.Name))
		}
	}
	for k := range got {
		if _, ok := known[k]; !ok {
			v.Warnings = append(v.Warnings, fmt.Sprintf("unknown key %s%s", prefix, k))
		}
	}
	sort.Strings(v.Warnings)
}

func (v *Validation) checkValue(name string, val any, p Param) {
	okType := false
	switch p.Type {
	case "string":
		_, okType = val.(string)
	case "boolean":
		_, okType = val.(bool)
	case "number":
		_, okType = val.(float64)
	case "integer":
		if f, ok := val.(float64); ok {
			okType = f == float64(int64(f))
		}
	case "array":
		_, okType = val.([]any)
	case "object":
		_, okType = val.(map[string]any)
	}
	if !okType {
		v.Errors = append(v.Errors, fmt.Sprintf("%s must be %s", name, p.Type))
		return
	}
	if len(p.Enum) > 0 {
		s, _ := val.(string)
		found := false
		for _, e := range p.Enum {
			found = found || e == s
		}
		if !found {
			v.Errors = append(v.Errors, fmt.Sprintf("%s must be one of %s", name, strings.Join(p.Enum, ", ")))
		}
	}
	if f, ok := val.(float64); ok {
		if p.Min != nil && f < *p.Min {
			v.Errors = append(v.Errors, fmt.Sprintf("%s must be >= %g", name, *p.Min))
		}
		if p.Max != nil && f > *p.Max {
			v.Errors = append(v.Errors, fmt.Sprintf("%s must be <= %g", name, *p.Max))
		}
	}
}

// RenderDoc produces the agent-facing SKILL.md: front matter from the manifest
// plus a prose template whose {{.SkillReference}} is generated from the schema,
// so parameter docs can never drift from what validation enforces.
func (m *Manifest) RenderDoc(tmplText string) (string, error) {
	t, err := template.New("skill").Option("missingkey=error").Parse(tmplText)
	if err != nil {
		return "", err
	}
	var body bytes.Buffer
	data := map[string]string{
		"Command":        m.Entrypoint.Path,
		"SkillReference": m.skillReference(),
		"ModeReference":  m.modeReference(),
	}
	if err := t.Execute(&body, data); err != nil {
		return "", err
	}
	desc := strings.ReplaceAll(strings.TrimSpace(m.Description), "\n", " ")
	return fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s", m.Name, desc, strings.TrimLeft(body.String(), "\n")), nil
}

func (m *Manifest) modeReference() string {
	var b strings.Builder
	b.WriteString("| Mode | Required keys | What it does |\n|---|---|---|\n")
	for _, md := range m.Modes {
		req := strings.Join(md.Required, ", ")
		if req == "" {
			req = "none"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", md.Name, req, md.Summary)
	}
	return b.String()
}

func (m *Manifest) skillReference() string {
	var b strings.Builder
	for i, s := range m.Skills {
		fmt.Fprintf(&b, "### %d. `%s`\n\n%s\n\n", i+1, s.Name, s.Summary)
		if len(s.Params) > 0 {
			b.WriteString("| Parameter | Type | Required | Default | Notes |\n|---|---|---|---|---|\n")
			for _, p := range s.Params {
				fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s |\n", p.Name, p.Type, yesNo(p.Required), defaultText(p), paramNotes(p))
			}
			b.WriteString("\n")
		}
		if s.Response != "" {
			fmt.Fprintf(&b, "Response: %s\n\n", s.Response)
		}
		if s.Speed != "" {
			fmt.Fprintf(&b, "**Speed:** %s\n\n", s.Speed)
		}
		if s.Notes != "" {
			fmt.Fprintf(&b, "%s\n\n", s.Notes)
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func defaultText(p Param) string {
	if p.Default == nil {
		return "-"
	}
	return fmt.Sprintf("`%v`", p.Default)
}

func paramNotes(p Param) string {
	var parts []string
	if len(p.Enum) > 0 {
		parts = append(parts, "one of "+strings.Join(p.Enum, " / "))
	}
	if p.Min != nil {
		parts = append(parts, fmt.Sprintf(">= %g", *p.Min))
	}
	if p.Max != nil {
		parts = append(parts, fmt.Sprintf("<= %g", *p.Max))
	}
	if p.Notes != "" {
		parts = append(parts, p.Notes)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "; ")
}

// DefaultLeakPatterns are always checked, whatever the manifest adds. They name
// the things the agent must not learn from the docs: where the code and data
// live, how the tool is built, and instructions that assume it can edit code.
var DefaultLeakPatterns = []string{
	`/home/`, `/opt/svc`, `\.pi/`, `vendor`, `conda`, `aizynth-env`, `site-packages`,
	`\.py\b`, `FIXES\.md`, `HANDOFF`, `AIZYNTHFINDER_`, `local\.env`, `config\.local`,
	`config\.template`, `numpy`, `\.hdf5`, `\.onnx`, `(?i)fix it`, `(?i)vendored`,
	`(?i)upstream`, `(?i)git (log|history)`,
}

// LeakCheck returns every leak pattern that matches the document, with the matching line.
func (m *Manifest) LeakCheck(doc string) []string {
	var hits []string
	for _, pat := range append(append([]string{}, DefaultLeakPatterns...), m.LeakPatterns...) {
		re := regexp.MustCompile(pat)
		for i, line := range strings.Split(doc, "\n") {
			if re.MatchString(line) {
				hits = append(hits, fmt.Sprintf("line %d matches %q: %s", i+1, pat, strings.TrimSpace(line)))
			}
		}
	}
	return hits
}

var absPath = regexp.MustCompile(`/(?:home|root|usr|opt|tmp|var|mnt|srv|etc)/[^\s"'\\,;)\]}]*`)

// ScrubPaths removes host paths from tool output before the agent sees it.
// Whole path tokens are replaced, not just their prefix, because the remainder
// (".pi/skills/x/vendor/data") would still reveal how the tool is laid out.
// Known prefixes (project root, interpreter environment) are matched first;
// any other absolute path that looks like a host path is replaced too, since
// tracebacks and error strings embed them. SMILES strings are untouched: they
// can contain slashes but never a "/home/"-style segment.
func ScrubPaths(b []byte, prefixes []string, replacement string) []byte {
	out := b
	sorted := append([]string(nil), prefixes...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	for _, p := range sorted {
		if p == "" {
			continue
		}
		re := regexp.MustCompile(regexp.QuoteMeta(p) + `[^\s"'\\,;)\]}]*`)
		out = re.ReplaceAll(out, []byte(replacement))
	}
	return absPath.ReplaceAll(out, []byte(replacement))
}
