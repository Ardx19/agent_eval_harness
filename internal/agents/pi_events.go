package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"agentevalharness/internal/proxy"
)

// TierB is what the agent says it did, parsed from its own JSON event stream
// (pi --mode json). It is useful structure but not evidence: it comes from a
// process the agent can influence, so it is always cross-checked against Tier A.
type TierB struct {
	Calls       []BCall
	Results     []BResult
	FinalText   string
	Input       int
	Output      int // includes reasoning tokens
	CostUSD     float64
	Retries     int
	Compactions int
	Errors      []string // an assistant message that ended in an error
	NonJSON     []string // lines in the stream that were not JSON events
	Started     bool
	Ended       bool
}

type BCall struct {
	ID       string
	Name     string
	Args     json.RawMessage
	ArgsHash string
}

type BResult struct {
	ID      string
	Name    string
	Text    string
	IsError bool
}

type piEvent struct {
	Type       string          `json:"type"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Args       json.RawMessage `json:"args"`
	IsError    bool            `json:"isError"`
	Result     struct {
		Content []piContent `json:"content"`
	} `json:"result"`
	Message *struct {
		Role         string      `json:"role"`
		Content      []piContent `json:"content"`
		StopReason   string      `json:"stopReason"`
		ErrorMessage string      `json:"errorMessage"`
		Usage        *struct {
			Input     int `json:"input"`
			Output    int `json:"output"`
			Reasoning int `json:"reasoning"`
			Cost      struct {
				Total float64 `json:"total"`
			} `json:"cost"`
		} `json:"usage"`
	} `json:"message"`
}

type piContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ParsePiEvents reads the stdout lines of a pi --mode json run.
func ParsePiEvents(lines []string) TierB {
	var b TierB
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e piEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil || e.Type == "" {
			b.NonJSON = append(b.NonJSON, line)
			continue
		}
		switch e.Type {
		case "agent_start":
			b.Started = true
		case "agent_end":
			b.Ended = true
		case "tool_execution_start":
			args := e.Args
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			b.Calls = append(b.Calls, BCall{ID: e.ToolCallID, Name: e.ToolName, Args: args, ArgsHash: argsHash(e.ToolName, args)})
		case "tool_execution_end":
			var sb strings.Builder
			for _, c := range e.Result.Content {
				sb.WriteString(c.Text)
			}
			b.Results = append(b.Results, BResult{ID: e.ToolCallID, Name: e.ToolName, Text: sb.String(), IsError: e.IsError})
		case "auto_retry_start":
			b.Retries++
		case "compaction_start":
			b.Compactions++
		case "message_end":
			if e.Message == nil || e.Message.Role != "assistant" {
				continue
			}
			if u := e.Message.Usage; u != nil {
				b.Input += u.Input
				b.Output += u.Output
				b.CostUSD += u.Cost.Total
			}
			if e.Message.StopReason == "error" || e.Message.ErrorMessage != "" {
				b.Errors = append(b.Errors, e.Message.ErrorMessage)
			}
			var text strings.Builder
			hasCall := false
			for _, c := range e.Message.Content {
				switch c.Type {
				case "text":
					text.WriteString(c.Text)
				case "toolCall":
					hasCall = true
				}
			}
			if !hasCall {
				b.FinalText = text.String()
			}
		}
	}
	return b
}

func argsHash(name string, args json.RawMessage) string {
	var v any
	canon := string(args)
	if json.Unmarshal(args, &v) == nil {
		if cb, err := json.Marshal(v); err == nil {
			canon = string(cb)
		}
	}
	h := sha256.Sum256([]byte(name + "\x00" + canon))
	return hex.EncodeToString(h[:])
}

// CrossCheck compares the agent's own account (Tier B) with the proxy transcript
// (Tier A). Disagreements mean the agent's stream cannot be trusted for that
// run. mismatches are contradictions; missing are things one side never saw.
func CrossCheck(b TierB, ex []proxy.Exchange) (mismatches, missing []string) {
	var proxyCalls []proxy.ToolCall
	results := map[string]proxy.ToolResult{} // by the agent's id
	var lastText string
	for _, e := range ex {
		proxyCalls = append(proxyCalls, e.ToolCalls...)
		for _, r := range e.ToolResults {
			if r.AgentCallID != "" {
				results[r.AgentCallID] = r
			}
		}
		if e.Text != "" || len(e.ToolCalls) == 0 && e.Status == 200 {
			if len(e.ToolCalls) == 0 && e.Text != "" {
				lastText = e.Text
			}
		}
	}

	matched := map[int]bool{}
	for _, bc := range b.Calls {
		idx := -1
		for i, pc := range proxyCalls { // exact: the proxy learned the agent's own id
			if !matched[i] && pc.AgentID == bc.ID {
				idx = i
				break
			}
		}
		if idx < 0 { // fall back to name and arguments, in order
			for i, pc := range proxyCalls {
				if !matched[i] && pc.Name == bc.Name && pc.ArgsHash == bc.ArgsHash {
					idx = i
					break
				}
			}
		}
		if idx < 0 {
			mismatches = append(mismatches, fmt.Sprintf("the agent's stream reports a %s call (%s) that the model never made", bc.Name, bc.ID))
			continue
		}
		matched[idx] = true
		if proxyCalls[idx].ArgsHash != bc.ArgsHash {
			mismatches = append(mismatches, fmt.Sprintf("call %s: the agent's stream shows different arguments than the model produced", bc.ID))
		}
	}
	for i, pc := range proxyCalls {
		if !matched[i] {
			missing = append(missing, fmt.Sprintf("the model made a %s call (%s) that the agent's stream never reported", pc.Name, pc.ID))
		}
	}

	for _, br := range b.Results {
		pr, ok := results[br.ID]
		if !ok {
			continue // the run may have ended before the model saw it
		}
		seen := strings.Join(leaves(pr.Content), "\n")
		if !strings.Contains(squashWS(seen), squashWS(strings.TrimSpace(br.Text))) {
			mismatches = append(mismatches, fmt.Sprintf("call %s: the agent's stream shows a tool result that differs from what the model was sent", br.ID))
		}
	}
	if b.FinalText != "" && lastText != "" && squashWS(b.FinalText) != squashWS(lastText) {
		mismatches = append(mismatches, "the agent's stream reports a different final answer than the model produced")
	}
	return mismatches, missing
}

func squashWS(s string) string { return strings.Join(strings.Fields(s), "") }

func leaves(content string) []string {
	var v any
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return []string{content}
	}
	var out []string
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			out = append(out, t)
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}
