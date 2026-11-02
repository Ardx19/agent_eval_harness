package trace

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"agentevalharness/internal/proxy"
)

// FromProxy derives the canonical trajectory from the proxy transcript (Tier A):
// it is what the model was actually asked and answered, independent of any
// self-reported agent log. Step 1 is the user prompt; each successful model
// response is an agent step; tool results reported in the next request are
// attached as that step's observation, matched by the proxy's call linking.
func FromProxy(exchanges []proxy.Exchange, prompt string, agent ATIFAgent) (*Trajectory, error) {
	ex := make([]proxy.Exchange, 0, len(exchanges))
	for _, e := range exchanges {
		if e.Rejected == "" && e.Status == 200 && e.Infra == "" && (len(e.ToolCalls) > 0 || e.Text != "" || len(e.Thinking) > 0) {
			ex = append(ex, e)
		}
	}
	sort.SliceStable(ex, func(i, j int) bool { return ex[i].Index < ex[j].Index })

	// results by call id, from every request (including rejected-free ones)
	results := map[string]proxy.ToolResult{}
	var unmatched []proxy.ToolResult
	for _, e := range exchanges {
		for _, r := range e.ToolResults {
			if r.Unmatched || r.CallID == "" {
				unmatched = append(unmatched, r)
				continue
			}
			results[r.CallID] = r
		}
	}

	t := &Trajectory{SchemaVersion: ATIFVersion, Agent: agent}
	first := time.Time{}
	if len(ex) > 0 {
		first = ex[0].Start
	}
	t.Steps = append(t.Steps, Step{StepID: 1, Source: "user", Message: prompt, Timestamp: stamp(first)})

	var totalIn, totalOut int
	for _, e := range ex {
		s := Step{
			StepID:           len(t.Steps) + 1,
			Source:           "agent",
			Timestamp:        stamp(e.End),
			ModelName:        e.Model,
			Message:          e.Text,
			ReasoningContent: strings.Join(e.Thinking, "\n"),
		}
		in, out := e.Usage.Prompt, e.Usage.Completion+e.Usage.Thoughts
		s.Metrics = &StepMetrics{PromptTokens: &in, CompletionTokens: &out}
		totalIn += in
		totalOut += out
		var obs []ObservationResult
		for _, c := range e.ToolCalls {
			args := c.Args
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			// use the agent's own id when the proxy learned it, so the trajectory and the
			// agent's event stream speak the same ids
			id := c.ID
			if c.AgentID != "" {
				id = c.AgentID
			}
			s.ToolCalls = append(s.ToolCalls, ToolCall{ToolCallID: id, FunctionName: c.Name, Arguments: args})
			if r, ok := results[c.ID]; ok {
				content := r.Content
				obs = append(obs, ObservationResult{SourceCallID: id, Content: &content})
			}
		}
		if len(obs) > 0 {
			s.Observation = &Observation{Results: obs}
		}
		t.Steps = append(t.Steps, s)
	}
	if len(unmatched) > 0 {
		// results nobody asked for are evidence of a problem; keep them visible
		var items []map[string]string
		for _, r := range unmatched {
			items = append(items, map[string]string{"name": r.Name, "content": r.Content})
		}
		t.Extra = map[string]any{"unmatched_tool_results": items}
	}
	n := len(t.Steps)
	t.FinalMetrics = &FinalMetrics{TotalPromptTokens: &totalIn, TotalCompletionTokens: &totalOut, TotalSteps: &n}
	return t, ValidateATIF(t)
}

func stamp(tm time.Time) string {
	if tm.IsZero() {
		return ""
	}
	return tm.UTC().Format(time.RFC3339Nano)
}
