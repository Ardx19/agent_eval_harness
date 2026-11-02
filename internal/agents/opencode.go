package agents

import (
	"encoding/json"
	"strings"

	"agentevalharness/internal/guest"
	"agentevalharness/internal/skills"
)

const (
	opencodeBin = "/opt/opencode/bin/opencode"
	ocConfig    = agentHome + "/.config/opencode/opencode.json"
)

// OpencodeRunSpec builds the supervisor's instructions for an opencode run:
// non-interactive, JSON events on stdout, no plugins, permissions auto-approved,
// no title-generation request. The prompt follows "--" so a leading dash is safe.
func OpencodeRunSpec(s Spec) guest.RunSpec {
	p := s.Profile
	argv := []string{opencodeBin, "run", "--pure", "--auto", "--title", "task", "--format", "json",
		"--model", p.Provider + "/" + p.Model, "--", s.Prompt}
	return guest.RunSpec{
		Argv: argv, Dir: agentHome + "/project", UID: agentUID, GID: agentUID, WallS: p.Limits.WallS, IdleS: p.Limits.IdleS, Listen: proxyListen,
		Env: []string{"HOME=" + agentHome, "USER=dev", "LOGNAME=dev", "SHELL=/bin/bash", "LANG=C.UTF-8", "TERM=dumb", "NO_COLOR=1",
			"PATH=" + GuestPath(s.PathPrefix), "PYTHONDONTWRITEBYTECODE=1",
			"XDG_CONFIG_HOME=" + agentHome + "/.config", "XDG_DATA_HOME=" + agentHome + "/.local/share", "XDG_CACHE_HOME=" + agentHome + "/.cache",
			"OPENCODE_DISABLE_AUTOUPDATE=1", "OPENCODE_DISABLE_MODELS_FETCH=1", "OPENCODE_DISABLE_LSP_DOWNLOAD=1",
			"OPENCODE_DISABLE_CLAUDE_CODE=1", "OPENCODE_DISABLE_EXTERNAL_SKILLS=1"},
	}
}

// opencodeConfigJSON points the built-in provider at the host proxy with a dummy
// key, switches off everything that reaches outside the sandbox (web tools,
// sharing, updates, MCP servers) and makes the installed skills discoverable.
func opencodeConfigJSON(s Spec) []byte {
	cfg := map[string]any{
		"$schema":           "https://opencode.ai/config.json",
		"model":             s.Profile.Provider + "/" + s.Profile.Model,
		"autoupdate":        false,
		"share":             "disabled",
		"snapshot":          false,
		"enabled_providers": []string{s.Profile.Provider},
		"provider": map[string]any{s.Profile.Provider: map[string]any{
			"options": map[string]any{"baseURL": "http://" + proxyListen + "/v1", "apiKey": "dummy-key"}}},
		"tools":      map[string]bool{"webfetch": false, "websearch": false},
		"permission": map[string]string{"webfetch": "deny", "websearch": "deny"},
		"mcp":        map[string]any{},
	}
	if len(s.Skills) > 0 {
		cfg["skills"] = map[string]any{"paths": []string{skills.SkillDocDir}}
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return b
}

type ocEvent struct {
	Type string `json:"type"`
	Part struct {
		Type   string  `json:"type"`
		Text   string  `json:"text"`
		Tool   string  `json:"tool"`
		CallID string  `json:"callID"`
		Reason string  `json:"reason"`
		Cost   float64 `json:"cost"`
		Tokens struct {
			Input     int `json:"input"`
			Output    int `json:"output"`
			Reasoning int `json:"reasoning"`
		} `json:"tokens"`
		State struct {
			Status   string          `json:"status"`
			Input    json.RawMessage `json:"input"`
			Output   string          `json:"output"`
			Error    string          `json:"error"`
			Metadata struct {
				Truncated bool `json:"truncated"`
			} `json:"metadata"`
		} `json:"state"`
	} `json:"part"`
	Error json.RawMessage `json:"error"`
}

// ParseOpencodeEvents reads the stdout lines of an `opencode run --format json`
// run. A tool_use event carries both the call and its result, so each produces
// one BCall and one BResult.
func ParseOpencodeEvents(lines []string) TierB {
	var b TierB
	var stepText strings.Builder
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e ocEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil || e.Type == "" {
			b.NonJSON = append(b.NonJSON, line)
			continue
		}
		switch e.Type {
		case "step_start":
			b.Started = true
			stepText.Reset()
		case "text":
			stepText.WriteString(e.Part.Text)
		case "tool_use":
			args := e.Part.State.Input
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			b.Calls = append(b.Calls, BCall{ID: e.Part.CallID, Name: e.Part.Tool, Args: args, ArgsHash: argsHash(e.Part.Tool, args)})
			if e.Part.State.Status == "completed" || e.Part.State.Status == "error" {
				text, isErr := e.Part.State.Output, e.Part.State.Status == "error"
				if isErr && text == "" {
					text = e.Part.State.Error
				}
				if !e.Part.State.Metadata.Truncated { // a truncated output legitimately differs from what the model saw
					b.Results = append(b.Results, BResult{ID: e.Part.CallID, Name: e.Part.Tool, Text: text, IsError: isErr})
				}
			}
		case "step_finish":
			b.Input += e.Part.Tokens.Input
			b.Output += e.Part.Tokens.Output + e.Part.Tokens.Reasoning
			b.CostUSD += e.Part.Cost
			if e.Part.Reason == "stop" {
				b.FinalText = stepText.String()
				b.Ended = true
			}
		case "error":
			b.Errors = append(b.Errors, strings.TrimSpace(string(e.Error)))
		}
	}
	return b
}

// ParseEvents picks the parser for the agent that produced the stream.
func ParseEvents(agent string, lines []string) TierB {
	if agent == "opencode" {
		return ParseOpencodeEvents(lines)
	}
	return ParsePiEvents(lines)
}

// RunSpecFor builds the supervisor's instructions for the profile's agent.
func RunSpecFor(s Spec) guest.RunSpec {
	if s.Profile.Agent == "opencode" {
		return OpencodeRunSpec(s)
	}
	return PiRunSpec(s)
}
