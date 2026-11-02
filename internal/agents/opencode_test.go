package agents

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"agentevalharness/internal/config"
	"agentevalharness/internal/proxy"
)

func ocFixtureLines(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile("../../testdata/fixtures/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// The events below were captured from a real `opencode run --format json` (1.18.34).
func TestParseOpencodeEventsRealCapture(t *testing.T) {
	b := ParseOpencodeEvents(ocFixtureLines(t, "opencode_events.jsonl"))
	if !b.Started || !b.Ended || len(b.NonJSON) != 0 || len(b.Errors) != 0 {
		t.Fatalf("%+v", b)
	}
	if len(b.Calls) != 1 || b.Calls[0].ID != "call_1" || b.Calls[0].Name != "bash" {
		t.Fatalf("calls %+v", b.Calls)
	}
	var args map[string]string
	if err := json.Unmarshal(b.Calls[0].Args, &args); err != nil || args["command"] != "echo hello-from-tool" {
		t.Fatalf("args %s", b.Calls[0].Args)
	}
	if len(b.Results) != 1 || b.Results[0].Text != "hello-from-tool\n" || b.Results[0].IsError {
		t.Fatalf("results %+v", b.Results)
	}
	if b.FinalText != "The tool printed hello-from-tool." || b.Input != 250 || b.Output != 30 || b.CostUSD < 5e-5 {
		t.Fatalf("final=%q in=%d out=%d cost=%v", b.FinalText, b.Input, b.Output, b.CostUSD)
	}
}

func TestOpencodeFailedToolAndTruncation(t *testing.T) {
	lines := []string{
		`{"type":"step_start","part":{}}`,
		`{"type":"tool_use","part":{"tool":"bash","callID":"c1","state":{"status":"error","input":{"command":"false"},"error":"exit 1"}}}`,
		`{"type":"tool_use","part":{"tool":"read","callID":"c2","state":{"status":"completed","input":{"filePath":"x"},"output":"big","metadata":{"truncated":true}}}}`,
		`{"type":"error","error":{"name":"APIError","data":{"message":"boom"}}}`,
		"not json",
	}
	b := ParseOpencodeEvents(lines)
	if len(b.Calls) != 2 || len(b.Results) != 1 || !b.Results[0].IsError || b.Results[0].Text != "exit 1" {
		t.Fatalf("%+v", b)
	}
	if len(b.Errors) != 1 || !strings.Contains(b.Errors[0], "boom") || len(b.NonJSON) != 1 {
		t.Fatalf("%+v", b)
	}
}

// The request opencode really sends must pass the proxy's allowlist and parse into
// tool results the recorder can join against.
func TestOpencodeRequestFitsTheOpenAIProxy(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/fixtures/opencode_openai_request.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Path string          `json:"path"`
		Body json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if fx.Path != "/v1/chat/completions" {
		t.Fatalf("path %q", fx.Path)
	}
	if tools := proxy.OpenAIServerTools(fx.Body); len(tools) != 0 {
		t.Fatalf("opencode declares hosted tools: %v", tools)
	}
	var req struct {
		Tools []struct {
			Function struct{ Name string } `json:"function"`
		} `json:"tools"`
	}
	json.Unmarshal(fx.Body, &req)
	for _, tl := range req.Tools {
		if tl.Function.Name == "webfetch" || tl.Function.Name == "websearch" {
			t.Fatalf("web tool %s is still offered to the model", tl.Function.Name)
		}
	}
}

func TestOpencodeSpecAndConfig(t *testing.T) {
	s := Spec{Prompt: "-rm everything", Profile: &config.Profile{Agent: "opencode", Provider: "opencode-go", Model: "hy3", API: "openai"}}
	s.Profile.Limits.WallS = 60
	spec := OpencodeRunSpec(s)
	joined := strings.Join(spec.Argv, " ")
	for _, want := range []string{"run", "--pure", "--auto", "--title", "--format json", "--model opencode-go/hy3", "-- -rm everything"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q: %s", want, joined)
		}
	}
	env := strings.Join(spec.Env, "\n")
	if !strings.Contains(env, "OPENCODE_DISABLE_AUTOUPDATE=1") || strings.Contains(env, "KEY") {
		t.Errorf("env: %s", env)
	}
	var cfg map[string]any
	if err := json.Unmarshal(opencodeConfigJSON(s), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["share"] != "disabled" || cfg["mcp"] == nil || strings.Contains(string(opencodeConfigJSON(s)), "real") {
		t.Errorf("%v", cfg)
	}
	tools := cfg["tools"].(map[string]any)
	if tools["webfetch"] != false || tools["websearch"] != false {
		t.Errorf("web tools must be off: %v", tools)
	}
	opts := cfg["provider"].(map[string]any)["opencode-go"].(map[string]any)["options"].(map[string]any)
	if opts["baseURL"] != "http://127.0.0.1:18080/v1" || opts["apiKey"] != "dummy-key" {
		t.Errorf("%v", opts)
	}
	if _, ok := cfg["skills"]; ok {
		t.Error("no skills were installed, so none should be configured")
	}
}
