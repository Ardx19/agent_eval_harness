package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The OpenAI-compatible chat-completions dialect (opencode-go/Hy3, OpenRouter,
// DeepSeek and others). Only model calls and the model list are reachable.
var (
	reOpenAICall = regexp.MustCompile(`^/v1/chat/completions$`)
	reOpenAIList = regexp.MustCompile(`^/v1/models(/[A-Za-z0-9._/-]+)?$`)
)

type oaMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// oaText flattens message content, which is a string or a list of text parts.
func oaText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return string(raw)
}

// OpenAIServerTools lists tools in the request that are not plain functions: a
// hosted search or code tool would run outside the sandbox.
func OpenAIServerTools(body []byte) []string {
	var req struct {
		Tools []struct {
			Type string `json:"type"`
		} `json:"tools"`
	}
	if json.Unmarshal(body, &req) != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, t := range req.Tools {
		if t.Type != "" && t.Type != "function" {
			seen[t.Type] = true
		}
	}
	var out []string
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func openAIModel(body []byte) string {
	var req struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &req)
	return req.Model
}

// parseOpenAIRequest extracts the tool results the agent reports back to the
// model (the trailing run of role "tool" messages), the tool calls it echoes
// from earlier turns, and whether reasoning was requested.
func parseOpenAIRequest(body []byte) parsedRequest {
	var req struct {
		Messages        []oaMessage     `json:"messages"`
		ReasoningEffort string          `json:"reasoning_effort"`
		Reasoning       json.RawMessage `json:"reasoning"`
	}
	var p parsedRequest
	if json.Unmarshal(body, &req) != nil {
		return p
	}
	p.Thinking = (req.ReasoningEffort != "" && req.ReasoningEffort != "none") || len(req.Reasoning) > 0
	p.Tools = OpenAIServerTools(body)
	names := map[string]string{}
	for _, m := range req.Messages {
		for _, tc := range m.ToolCalls {
			names[tc.ID] = tc.Function.Name
			args := json.RawMessage(tc.Function.Arguments)
			if len(bytes.TrimSpace(args)) == 0 {
				args = json.RawMessage(`{}`)
			}
			p.Echoes = append(p.Echoes, Echo{ID: tc.ID, Name: tc.Function.Name, ArgsHash: hashString(tc.Function.Name + "\x00" + canonical(args))})
		}
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if m.Role != "tool" {
			break
		}
		name := m.Name
		if name == "" {
			name = names[m.ToolCallID]
		}
		p.Results = append([]ToolResult{{CallID: m.ToolCallID, Name: name, Content: oaText(m.Content)}}, p.Results...)
	}
	return p
}

// parseOpenAIResponse accepts an SSE stream of chat.completion.chunk objects or a
// single chat.completion object. Streamed tool calls arrive as argument
// fragments keyed by index and are reassembled.
func parseOpenAIResponse(body []byte, exchangeIdx int) parsedResponse {
	var chunks []json.RawMessage
	trim := bytes.TrimSpace(body)
	if bytes.HasPrefix(trim, []byte("{")) && !bytes.Contains(trim[:min(len(trim), 64)], []byte("data:")) {
		chunks = []json.RawMessage{trim}
	} else {
		sc := bufio.NewScanner(bytes.NewReader(body))
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			if line := strings.TrimSpace(sc.Text()); strings.HasPrefix(line, "data:") {
				if d := strings.TrimSpace(strings.TrimPrefix(line, "data:")); d != "" && d != "[DONE]" {
					chunks = append(chunks, json.RawMessage(d))
				}
			}
		}
	}
	type pending struct {
		id, name string
		args     strings.Builder
	}
	var out parsedResponse
	calls := map[int]*pending{}
	var order []int
	var thinking strings.Builder
	for _, raw := range chunks {
		var c struct {
			Model   string `json:"model"`
			Choices []struct {
				Delta   *oaDelta `json:"delta"`
				Message *oaDelta `json:"message"`
			} `json:"choices"`
			Usage *struct {
				Prompt     int `json:"prompt_tokens"`
				Completion int `json:"completion_tokens"`
				Total      int `json:"total_tokens"`
				Details    struct {
					Reasoning int `json:"reasoning_tokens"`
				} `json:"completion_tokens_details"`
			} `json:"usage"`
		}
		if json.Unmarshal(raw, &c) != nil {
			continue
		}
		if c.Model != "" {
			out.Model = c.Model
		}
		if c.Usage != nil && c.Usage.Total > 0 {
			out.Usage = Usage{Prompt: c.Usage.Prompt, Completion: c.Usage.Completion, Thoughts: c.Usage.Details.Reasoning, Total: c.Usage.Total}
		}
		for _, ch := range c.Choices {
			d := ch.Delta
			if d == nil {
				d = ch.Message
			}
			if d == nil {
				continue
			}
			out.Text += d.Content
			thinking.WriteString(d.Reasoning + d.ReasoningContent)
			for pos, tc := range d.ToolCalls {
				idx := tc.Index
				if ch.Delta == nil { // a whole message has no index field: use position
					idx = pos
				}
				pc := calls[idx]
				if pc == nil {
					pc = &pending{}
					calls[idx] = pc
					order = append(order, idx)
				}
				if tc.ID != "" {
					pc.id = tc.ID
				}
				if tc.Function.Name != "" {
					pc.name = tc.Function.Name
				}
				pc.args.WriteString(tc.Function.Arguments)
			}
		}
	}
	if thinking.Len() > 0 {
		out.Thinking = []string{thinking.String()}
	}
	sort.Ints(order)
	for pos, idx := range order {
		pc := calls[idx]
		args := json.RawMessage(strings.TrimSpace(pc.args.String()))
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		call := ToolCall{ID: pc.id, Name: pc.name, Args: json.RawMessage(canonical(args))}
		call.ArgsHash = hashString(call.Name + "\x00" + string(call.Args))
		if call.ID == "" {
			call.ID = fmt.Sprintf("o%d.%d.%s", exchangeIdx, pos, call.ArgsHash[:8])
			call.Synth = true
		}
		out.Calls = append(out.Calls, call)
	}
	return out
}

type oaDelta struct {
	Content          string `json:"content"`
	Reasoning        string `json:"reasoning"`
	ReasoningContent string `json:"reasoning_content"`
	ToolCalls        []struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}
