// Package trace defines the evalharness-2 run record and its embedded
// ATIF-v1.7 trajectory (Harbor's Agent Trajectory Interchange Format).
package trace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

// ATIFVersion is the trajectory schema version we emit.
const ATIFVersion = "ATIF-v1.7"

type Trajectory struct {
	SchemaVersion string         `json:"schema_version"`
	SessionID     string         `json:"session_id,omitempty"`
	TrajectoryID  string         `json:"trajectory_id,omitempty"`
	Agent         ATIFAgent      `json:"agent"`
	Steps         []Step         `json:"steps"`
	Notes         string         `json:"notes,omitempty"`
	FinalMetrics  *FinalMetrics  `json:"final_metrics,omitempty"`
	Extra         map[string]any `json:"extra,omitempty"`
}

type ATIFAgent struct {
	Name      string         `json:"name"`
	Version   string         `json:"version"`
	ModelName string         `json:"model_name,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

type Step struct {
	StepID           int            `json:"step_id"`
	Timestamp        string         `json:"timestamp,omitempty"`
	Source           string         `json:"source"` // system | user | agent
	ModelName        string         `json:"model_name,omitempty"`
	Message          string         `json:"message"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall     `json:"tool_calls,omitempty"`
	Observation      *Observation   `json:"observation,omitempty"`
	Metrics          *StepMetrics   `json:"metrics,omitempty"`
	LLMCallCount     *int           `json:"llm_call_count,omitempty"`
	Extra            map[string]any `json:"extra,omitempty"`
}

type ToolCall struct {
	ToolCallID   string          `json:"tool_call_id"`
	FunctionName string          `json:"function_name"`
	Arguments    json.RawMessage `json:"arguments"` // must be a JSON object
}

type Observation struct {
	Results []ObservationResult `json:"results"`
}

type ObservationResult struct {
	SourceCallID string  `json:"source_call_id,omitempty"`
	Content      *string `json:"content,omitempty"`
}

type StepMetrics struct {
	PromptTokens     *int     `json:"prompt_tokens,omitempty"`
	CompletionTokens *int     `json:"completion_tokens,omitempty"`
	CachedTokens     *int     `json:"cached_tokens,omitempty"`
	CostUSD          *float64 `json:"cost_usd,omitempty"`
}

type FinalMetrics struct {
	TotalPromptTokens     *int     `json:"total_prompt_tokens,omitempty"`
	TotalCompletionTokens *int     `json:"total_completion_tokens,omitempty"`
	TotalCachedTokens     *int     `json:"total_cached_tokens,omitempty"`
	TotalCostUSD          *float64 `json:"total_cost_usd,omitempty"`
	TotalSteps            *int     `json:"total_steps,omitempty"`
}

var reATIFVersion = regexp.MustCompile(`^ATIF-v1\.\d+$`)

// ValidateATIF enforces the ATIF-v1.7 rules we rely on. It is checked against
// Harbor's own validator in tests (differential), so the two cannot drift.
func ValidateATIF(t *Trajectory) error {
	if t == nil {
		return fmt.Errorf("trajectory is nil")
	}
	if !reATIFVersion.MatchString(t.SchemaVersion) {
		return fmt.Errorf("schema_version %q is not ATIF-v1.x", t.SchemaVersion)
	}
	if t.Agent.Name == "" || t.Agent.Version == "" {
		return fmt.Errorf("agent.name and agent.version are required")
	}
	if len(t.Steps) == 0 {
		return fmt.Errorf("trajectory has no steps")
	}
	for i, s := range t.Steps {
		at := fmt.Sprintf("steps[%d]", i)
		if s.StepID != i+1 {
			return fmt.Errorf("%s: step_id %d, want %d (sequential from 1)", at, s.StepID, i+1)
		}
		switch s.Source {
		case "system", "user", "agent":
		default:
			return fmt.Errorf("%s: source %q must be system, user or agent", at, s.Source)
		}
		if s.Timestamp != "" {
			if _, err := time.Parse(time.RFC3339Nano, s.Timestamp); err != nil {
				return fmt.Errorf("%s: timestamp %q is not ISO 8601", at, s.Timestamp)
			}
		}
		if s.Source != "agent" {
			switch {
			case s.ModelName != "":
				return fmt.Errorf("%s: model_name is only allowed on agent steps", at)
			case s.ReasoningContent != "":
				return fmt.Errorf("%s: reasoning_content is only allowed on agent steps", at)
			case len(s.ToolCalls) > 0:
				return fmt.Errorf("%s: tool_calls are only allowed on agent steps", at)
			case s.Metrics != nil:
				return fmt.Errorf("%s: metrics are only allowed on agent steps", at)
			}
		}
		if s.Observation != nil && s.Source == "user" {
			return fmt.Errorf("%s: observation is only allowed on agent or system steps", at)
		}
		if s.LLMCallCount != nil && *s.LLMCallCount == 0 && (s.ReasoningContent != "" || s.Metrics != nil) {
			return fmt.Errorf("%s: llm_call_count 0 forbids reasoning_content and metrics", at)
		}
		ids := map[string]bool{}
		for j, c := range s.ToolCalls {
			if c.ToolCallID == "" || c.FunctionName == "" {
				return fmt.Errorf("%s.tool_calls[%d]: tool_call_id and function_name are required", at, j)
			}
			if ids[c.ToolCallID] {
				return fmt.Errorf("%s: duplicate tool_call_id %q", at, c.ToolCallID)
			}
			ids[c.ToolCallID] = true
			if !isJSONObject(c.Arguments) {
				return fmt.Errorf("%s.tool_calls[%d]: arguments must be a JSON object", at, j)
			}
		}
		if s.Observation != nil {
			for j, r := range s.Observation.Results {
				if r.SourceCallID != "" && !ids[r.SourceCallID] {
					return fmt.Errorf("%s.observation.results[%d]: source_call_id %q matches no tool_call_id on this step", at, j, r.SourceCallID)
				}
			}
		}
	}
	return nil
}

func isJSONObject(raw json.RawMessage) bool {
	b := bytes.TrimSpace(raw)
	return len(b) > 0 && b[0] == '{' && json.Valid(b)
}
