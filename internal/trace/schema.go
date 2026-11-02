package trace

import (
	"bytes"
	"encoding/json"
	"fmt"

	"agentevalharness/internal/sandbox"
)

// SchemaVersion identifies the run record format.
const SchemaVersion = "evalharness-2"

var statuses = map[string]bool{
	"ok": true, "failed": true, "timeout": true, "oom": true, "tool_error": true,
	"invalid_args": true, "refused": true, "gave_up": true, "budget_exceeded": true, "infra_error": true,
}

var attributions = map[string]bool{"agent": true, "tool": true, "task": true, "infra": true}

// Trace is one run, the contract between the runner and the grader.
type Trace struct {
	SchemaVersion string      `json:"schema_version"`
	TaskID        string      `json:"task_id"`
	RunID         string      `json:"run_id"`
	Trial         int         `json:"trial"`
	Seed          int64       `json:"seed"`
	AgentConfig   AgentConfig `json:"agent_config"`
	Environment   Environment `json:"environment"`
	Status        string      `json:"status"`
	Failure       *Failure    `json:"failure,omitempty"`
	Trajectory    *Trajectory `json:"trajectory"`
	Host          Host        `json:"host"`
	Provenance    Provenance  `json:"provenance"`
	Integrity     Integrity   `json:"integrity"`
	Signals       Signals     `json:"signals"`
	FinalAnswer   string      `json:"final_answer"`
	Metrics       Metrics     `json:"metrics"`
	Verify        *Verify     `json:"verify,omitempty"` // present for tasks with a hidden test stage
}

// Verify is the outcome of the hidden test stage, measured by the host in a
// separate microVM that never ran the agent. Resolved comes only from here, never
// from what the agent said.
type Verify struct {
	Status       string            `json:"status"` // resolved | unresolved | compromised | infra_error
	Resolved     bool              `json:"resolved"`
	CanaryOK     bool              `json:"canary_ok"`                // the hidden must-fail test failed as it must
	Note         string            `json:"integrity_note,omitempty"` // why the report was not believed (status compromised)
	Cause        string            `json:"cause,omitempty"`          // reporting_tampered | empty_patch | f2p_failed | p2p_broken | verify_timeout | test_patch_failed | infra
	PatchSHA256  string            `json:"patch_sha256,omitempty"`
	PatchBytes   int               `json:"patch_bytes"`
	ChangedFiles []string          `json:"changed_files"`
	Tampered     []string          `json:"tampered_paths,omitempty"` // test or test-config files the agent changed; the hidden tests overwrite them
	F2P          map[string]string `json:"fail_to_pass,omitempty"`   // test id -> PASSED | FAILED | MISSING
	P2P          map[string]string `json:"pass_to_pass,omitempty"`
	P2PFailed    int               `json:"pass_to_pass_failed"`
	P2PTotal     int               `json:"pass_to_pass_total"`
	P2PExcluded  int               `json:"pass_to_pass_excluded,omitempty"` // tests that fail even with the gold patch here
	ExitCode     int               `json:"exit_code"`
	WallS        float64           `json:"wall_s"`
	OutputTail   string            `json:"output_tail,omitempty"`
	Attempts     int               `json:"attempts"`
}

type AgentConfig struct {
	Agent       string   `json:"agent"`
	Version     string   `json:"version"`
	Provider    string   `json:"provider"`
	Model       string   `json:"model"`
	Skills      []string `json:"skills"`
	MCPs        []string `json:"mcps"` // always present; [] means no servers configured
	Web         bool     `json:"web"`
	ToolMode    string   `json:"tool_mode"`
	SkillRunner string   `json:"skill_runner"`
	Realism     string   `json:"realism_profile"`
}

// Environment records where and on what the run happened.
type Environment struct {
	HostKernel    string `json:"host_kernel"`
	Firecracker   string `json:"firecracker"`
	GuestKernel   string `json:"guest_kernel"`
	ImageTreeHash string `json:"image_tree_hash"`
	MemPeakMethod string `json:"mem_peak_method,omitempty"`
}

type Failure struct {
	Stage       string `json:"stage"`
	Reason      string `json:"reason"`
	Cause       string `json:"cause"`
	Attribution string `json:"attribution"` // agent | tool | task | infra
	StepID      int    `json:"step_id,omitempty"`
	ToolCallRef string `json:"tool_call_ref,omitempty"`
}

// Host holds Tier A evidence the agent cannot reach. Large bodies are blob refs.
type Host struct {
	ToolCalls       []HostToolCall `json:"tool_calls"`
	ExecLog         Ref            `json:"exec_log,omitempty"`
	ProxyTranscript Ref            `json:"proxy_transcript,omitempty"`
	// CallRecords is a blob holding the host's full records of every tool call
	// (stdout, stderr, parsed request, input text). It is what lets a stored run be
	// re-analysed when a detector improves. Each call's stdout is also stored
	// under its own hash, so blobs/<stdout_sha256> is the exact text the agent saw.
	CallRecords  Ref              `json:"call_records,omitempty"`
	FilesChanged []sandbox.Change `json:"files_changed"`
}

type HostToolCall struct {
	CallID          string   `json:"call_id"`
	PID             int      `json:"pid"`
	PPID            int      `json:"ppid,omitempty"`
	Tool            string   `json:"tool"`
	Argv            []string `json:"argv"`
	ArgsValid       bool     `json:"args_valid"`
	ValidationError string   `json:"validation_error,omitempty"`
	TStart          float64  `json:"t_start"`
	DurationS       float64  `json:"duration_s"`
	ExitCode        int      `json:"exit_code"`
	StdoutSHA256    string   `json:"stdout_sha256"`
	StderrSHA256    string   `json:"stderr_sha256,omitempty"`
	ReceiptValid    bool     `json:"receipt_valid"`
	CPUS            float64  `json:"cpu_s"`
	PeakMemMB       float64  `json:"peak_mem_mb"`
	OOMKilled       bool     `json:"oom_killed"`
	Outputs         []Output `json:"outputs"`
}

type Output struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// Provenance joins the agent's story to host evidence. Filled by the join engine (U8).
type Provenance struct {
	Links             []Link   `json:"links"`
	ShellOnlyResults  []string `json:"shell_only_results"`
	UnsupportedClaims []string `json:"unsupported_claims"`
	Flags             []Flag   `json:"flags"` // fabrication findings F1..F6, with the evidence for each
}

// Flag is one fabrication finding. Code is F1..F6; Subject names what it is about.
type Flag struct {
	Code     string `json:"code"`
	Subject  string `json:"subject"`
	Evidence string `json:"evidence"`
}

type Link struct {
	ToolCallID                 string `json:"tool_call_id"`
	ProxyToolUseID             string `json:"proxy_tool_use_id"`
	ExecPID                    int    `json:"exec_pid,omitempty"`
	HostCallID                 string `json:"host_call_id,omitempty"`
	ObservationMatchesVerified bool   `json:"observation_matches_verified"`
	ModelSawMatches            bool   `json:"model_saw_matches"`
}

type Integrity struct {
	StreamMismatches       []string `json:"stream_mismatches"`
	UnexplainedFileChanges []string `json:"unexplained_file_changes"`
	TierBMissingEvents     []string `json:"tier_b_missing_events"`
}

// Signals are rule-based detector outputs (regex/keyword); no LLM is involved.
type Signals struct {
	AnswerSeeking     []string `json:"answer_seeking"`
	EvalAwareness     []string `json:"eval_awareness"`
	CanaryTouched     []string `json:"canary_touched"`
	InjectionFollowed *bool    `json:"injection_followed"`
}

type Metrics struct {
	// AgentRetries and AgentCompactions come from the agent's own stream: pi
	// resends a request it thinks failed, and rewrites its context on compaction.
	// Both change what the model saw, so a run that needed them is worth a look.
	AgentRetries     int     `json:"agent_retries,omitempty"`
	AgentCompactions int     `json:"agent_compactions,omitempty"`
	WallS            float64 `json:"wall_s"`
	Steps            int     `json:"steps"`
	TokensIn         int     `json:"tokens_in"`
	TokensOut        int     `json:"tokens_out"`
	CostUSD          float64 `json:"cost_usd"`
}

// Normalize turns nil slices into empty ones so absent data is never confused
// with null in the JSON, and fills the schema version.
func (t *Trace) Normalize() {
	t.SchemaVersion = SchemaVersion
	for _, p := range []*[]string{&t.AgentConfig.Skills, &t.AgentConfig.MCPs, &t.Provenance.ShellOnlyResults,
		&t.Provenance.UnsupportedClaims, &t.Integrity.StreamMismatches, &t.Integrity.UnexplainedFileChanges,
		&t.Integrity.TierBMissingEvents, &t.Signals.AnswerSeeking, &t.Signals.EvalAwareness, &t.Signals.CanaryTouched} {
		if *p == nil {
			*p = []string{}
		}
	}
	if t.Provenance.Links == nil {
		t.Provenance.Links = []Link{}
	}
	if t.Provenance.Flags == nil {
		t.Provenance.Flags = []Flag{}
	}
	if t.Host.ToolCalls == nil {
		t.Host.ToolCalls = []HostToolCall{}
	}
	if t.Host.FilesChanged == nil {
		t.Host.FilesChanged = []sandbox.Change{}
	}
	for i := range t.Host.ToolCalls {
		if t.Host.ToolCalls[i].Argv == nil {
			t.Host.ToolCalls[i].Argv = []string{}
		}
		if t.Host.ToolCalls[i].Outputs == nil {
			t.Host.ToolCalls[i].Outputs = []Output{}
		}
	}
}

// Validate checks the record and its trajectory.
func (t *Trace) Validate() error {
	if t.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version %q, want %q", t.SchemaVersion, SchemaVersion)
	}
	if t.TaskID == "" || t.RunID == "" {
		return fmt.Errorf("task_id and run_id are required")
	}
	if !statuses[t.Status] {
		return fmt.Errorf("status %q is not a known status", t.Status)
	}
	if t.Status == "ok" && t.Failure != nil {
		return fmt.Errorf("status ok must not carry a failure")
	}
	if t.Status != "ok" && t.Status != "gave_up" && t.Status != "refused" && t.Failure == nil {
		return fmt.Errorf("status %q requires a failure with a cause and attribution", t.Status)
	}
	if t.Failure != nil && !attributions[t.Failure.Attribution] {
		return fmt.Errorf("failure.attribution %q must be agent, tool, task or infra", t.Failure.Attribution)
	}
	if t.Status == "infra_error" && (t.Failure == nil || t.Failure.Attribution != "infra") {
		return fmt.Errorf("infra_error must be attributed to infra so it is excluded from agent scores")
	}
	if t.AgentConfig.MCPs == nil {
		return fmt.Errorf("agent_config.mcps must be present (use [] for none)")
	}
	if err := ValidateATIF(t.Trajectory); err != nil {
		return fmt.Errorf("trajectory: %w", err)
	}
	return nil
}

// Encode normalizes, validates and renders the record deterministically.
func Encode(t *Trace) ([]byte, error) {
	t.Normalize()
	if err := t.Validate(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(t); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decode parses strictly (unknown fields are an error) and validates.
func Decode(b []byte) (*Trace, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var t Trace
	if err := dec.Decode(&t); err != nil {
		return nil, err
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

// TrajectoryJSON renders only the ATIF section, ready for Harbor's validator.
func (t *Trace) TrajectoryJSON() ([]byte, error) {
	return json.MarshalIndent(t.Trajectory, "", "  ")
}
