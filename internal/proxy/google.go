package proxy

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Server-side tools run outside the sandbox (search, URL fetch, code execution),
// so they are rejected unless explicitly allowed. Both Google and Anthropic
// spellings are recognised so the policy holds for either provider.
var googleServerTools = map[string]string{
	"googlesearch": "googleSearch", "google_search": "googleSearch",
	"googlesearchretrieval": "googleSearchRetrieval", "google_search_retrieval": "googleSearchRetrieval",
	"urlcontext": "urlContext", "url_context": "urlContext",
	"codeexecution": "codeExecution", "code_execution": "codeExecution",
}

// ServerTools lists server-side tools declared in a request body.
func ServerTools(body []byte) []string {
	var req struct {
		Tools []map[string]json.RawMessage `json:"tools"`
	}
	if json.Unmarshal(body, &req) != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, t := range req.Tools {
		for k := range t {
			if name, ok := googleServerTools[strings.ToLower(k)]; ok {
				seen[name] = true
			}
		}
		var typ string
		if raw, ok := t["type"]; ok && json.Unmarshal(raw, &typ) == nil && strings.HasPrefix(typ, "web_search") {
			seen["web_search"] = true // Anthropic server tool
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ToolCall is a function call the model made (from a response).
type ToolCall struct {
	ID       string          `json:"id"` // provider id if present, else synthesized
	Name     string          `json:"name"`
	Args     json.RawMessage `json:"args"`
	ArgsHash string          `json:"args_hash"` // sha256 of the canonical args, for joining with agent streams
	Synth    bool            `json:"synthesized_id"`
	// AgentID is the id the agent itself gave this call. When the model returns no
	// id the agent invents one and echoes it back in later requests; the proxy
	// learns the alias from that echo, which gives an exact join with the agent's
	// own event stream.
	AgentID string `json:"agent_id,omitempty"`
}

// Echo is a function call the agent repeated in a request's history, with the id it uses.
type Echo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ArgsHash string `json:"args_hash"`
}

// ToolResult is a function response the agent sent back (from a request).
type ToolResult struct {
	CallID      string `json:"call_id"`                 // the proxy's id of the call it answers; "" when none matched
	AgentCallID string `json:"agent_call_id,omitempty"` // the id the agent used for it
	Name        string `json:"name"`
	Content     string `json:"content"` // canonical JSON of the response object
	Unmatched   bool   `json:"unmatched,omitempty"`
}

// Usage is the provider-reported token usage.
type Usage struct {
	Prompt, Completion, Thoughts, Total int
}

type parsedRequest struct {
	Echoes    []Echo
	Results   []ToolResult
	Thinking  bool // the request asked for thought summaries
	Tools     []string
	Truncated bool
}

type part struct {
	Text             string `json:"text"`
	Thought          bool   `json:"thought"`
	ThoughtSignature string `json:"thoughtSignature"`
	FunctionCall     *struct {
		ID   string          `json:"id"`
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"functionCall"`
	FunctionResponse *struct {
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Response json.RawMessage `json:"response"`
	} `json:"functionResponse"`
}

type content struct {
	Role  string `json:"role"`
	Parts []part `json:"parts"`
}

// parseGoogleRequest extracts the tool results the agent is reporting back to
// the model, and whether thoughts were requested.
func parseGoogleRequest(body []byte) parsedRequest {
	var req struct {
		Contents         []content `json:"contents"`
		GenerationConfig struct {
			ThinkingConfig struct {
				IncludeThoughts bool `json:"includeThoughts"`
			} `json:"thinkingConfig"`
		} `json:"generationConfig"`
	}
	var p parsedRequest
	if json.Unmarshal(body, &req) != nil {
		return p
	}
	p.Thinking = req.GenerationConfig.ThinkingConfig.IncludeThoughts
	p.Tools = ServerTools(body)
	for _, c := range req.Contents {
		for _, pt := range c.Parts {
			if pt.FunctionCall != nil && pt.FunctionCall.ID != "" {
				args := pt.FunctionCall.Args
				if len(args) == 0 {
					args = json.RawMessage(`{}`)
				}
				p.Echoes = append(p.Echoes, Echo{ID: pt.FunctionCall.ID, Name: pt.FunctionCall.Name, ArgsHash: hashString(pt.FunctionCall.Name + "\x00" + canonical(args))})
			}
		}
	}
	// Only the trailing run of function responses is new: earlier ones were
	// already reported in earlier requests.
	for i := len(req.Contents) - 1; i >= 0; i-- {
		var batch []ToolResult
		for _, pt := range req.Contents[i].Parts {
			if pt.FunctionResponse != nil {
				batch = append(batch, ToolResult{CallID: pt.FunctionResponse.ID, Name: pt.FunctionResponse.Name, Content: canonical(pt.FunctionResponse.Response)})
			}
		}
		if len(batch) == 0 {
			break
		}
		p.Results = append(batch, p.Results...)
	}
	return p
}

type parsedResponse struct {
	Calls    []ToolCall
	Thinking []string
	Text     string
	Usage    Usage
	Model    string
}

// parseGoogleResponse accepts an SSE stream ("data: {...}" lines), a JSON array
// of chunks, or a single JSON object.
func parseGoogleResponse(body []byte, exchangeIdx int) parsedResponse {
	var chunks []json.RawMessage
	trim := bytes.TrimSpace(body)
	switch {
	case bytes.HasPrefix(trim, []byte("[")):
		_ = json.Unmarshal(trim, &chunks)
	case bytes.HasPrefix(trim, []byte("{")):
		chunks = []json.RawMessage{trim}
	default:
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
	var out parsedResponse
	pos := 0
	for _, raw := range chunks {
		var c struct {
			Candidates []struct {
				Content content `json:"content"`
			} `json:"candidates"`
			Usage struct {
				PromptTokenCount     int `json:"promptTokenCount"`
				CandidatesTokenCount int `json:"candidatesTokenCount"`
				ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
				TotalTokenCount      int `json:"totalTokenCount"`
			} `json:"usageMetadata"`
			ModelVersion string `json:"modelVersion"`
		}
		if json.Unmarshal(raw, &c) != nil {
			continue
		}
		if c.ModelVersion != "" {
			out.Model = c.ModelVersion
		}
		// usage is cumulative in later chunks: keep the latest non-zero values
		if c.Usage.TotalTokenCount > 0 {
			out.Usage = Usage{Prompt: c.Usage.PromptTokenCount, Completion: c.Usage.CandidatesTokenCount, Thoughts: c.Usage.ThoughtsTokenCount, Total: c.Usage.TotalTokenCount}
		}
		for _, cand := range c.Candidates {
			for _, pt := range cand.Content.Parts {
				switch {
				case pt.FunctionCall != nil:
					args := pt.FunctionCall.Args
					if len(args) == 0 {
						args = json.RawMessage(`{}`)
					}
					call := ToolCall{ID: pt.FunctionCall.ID, Name: pt.FunctionCall.Name, Args: json.RawMessage(canonical(args))}
					call.ArgsHash = hashString(call.Name + "\x00" + string(call.Args))
					if call.ID == "" {
						call.ID = fmt.Sprintf("g%d.%d.%s", exchangeIdx, pos, call.ArgsHash[:8])
						call.Synth = true
					}
					out.Calls = append(out.Calls, call)
					pos++
				case pt.Thought:
					out.Thinking = append(out.Thinking, pt.Text)
				case pt.Text != "":
					out.Text += pt.Text
				}
			}
		}
	}
	return out
}

var reModel = regexp.MustCompile(`/models/([A-Za-z0-9._-]+)(?::|$)`)

func modelFromPath(p string) string {
	if m := reModel.FindStringSubmatch(p); m != nil {
		return m[1]
	}
	return ""
}

// canonical re-encodes JSON with sorted keys so equal values hash equally.
func canonical(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(b)
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
