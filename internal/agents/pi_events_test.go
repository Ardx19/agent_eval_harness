package agents

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"agentevalharness/internal/proxy"
)

func fixtureLines(t *testing.T) []string {
	t.Helper()
	f, err := os.Open("../../testdata/fixtures/pi_events.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

func TestParsesARealPiRun(t *testing.T) {
	b := ParsePiEvents(fixtureLines(t))
	if !b.Started || !b.Ended || len(b.NonJSON) != 0 {
		t.Fatalf("%+v", b)
	}
	if len(b.Calls) != 1 || b.Calls[0].Name != "bash" || !strings.HasPrefix(b.Calls[0].ID, "bash_") || !strings.Contains(string(b.Calls[0].Args), "echo hello-from-tool") {
		t.Fatalf("%+v", b.Calls)
	}
	if len(b.Results) != 1 || b.Results[0].Text != "hello-from-tool\n" || b.Results[0].IsError || b.Results[0].ID != b.Calls[0].ID {
		t.Fatalf("%+v", b.Results)
	}
	if b.FinalText != "The directory is empty except for what I created. Done." {
		t.Fatalf("final text %q", b.FinalText)
	}
	if b.Input != 150 || b.Output != 26 || b.CostUSD < 1e-4 {
		t.Fatalf("usage not summed over both assistant messages: in=%d out=%d cost=%g", b.Input, b.Output, b.CostUSD)
	}
}

func TestNonJSONLinesAreReported(t *testing.T) {
	b := ParsePiEvents([]string{`{"type":"agent_start"}`, `warning: something printed to stdout`, `{"not":"an event"}`, ``})
	if len(b.NonJSON) != 2 {
		t.Fatalf("log lines mixed into the stream must be flagged, not silently parsed: %v", b.NonJSON)
	}
}

func TestErroredAssistantMessageIsCaptured(t *testing.T) {
	b := ParsePiEvents([]string{`{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"429 rate limited","content":[]}}`})
	if len(b.Errors) != 1 || !strings.Contains(b.Errors[0], "429") {
		t.Fatalf("%+v", b.Errors)
	}
}

// proxy exchanges equivalent to what the proxy recorded for the fixture run
func realFlow() []proxy.Exchange {
	args := json.RawMessage(`{"command":"echo hello-from-tool && ls"}`)
	call := proxy.ToolCall{ID: "g0.0.ab12cd34", Name: "bash", Args: args, ArgsHash: argsHash("bash", args), Synth: true, AgentID: ""}
	return []proxy.Exchange{
		{Index: 0, Status: 200, ToolCalls: []proxy.ToolCall{call}},
		{Index: 1, Status: 200, Text: "The directory is empty except for what I created. Done.",
			ToolResults: []proxy.ToolResult{{CallID: call.ID, AgentCallID: "bash_1791040070867_1", Name: "bash", Content: `{"output":"hello-from-tool\n"}`}}},
	}
}

func TestCrossCheckAgreesOnAnHonestRun(t *testing.T) {
	b := ParsePiEvents(fixtureLines(t))
	ex := realFlow()
	ex[0].ToolCalls[0].AgentID = b.Calls[0].ID // learned from the echo in the next request
	ex[1].ToolResults[0].AgentCallID = b.Calls[0].ID
	mism, miss := CrossCheck(b, ex)
	if len(mism) != 0 || len(miss) != 0 {
		t.Fatalf("mismatches=%v missing=%v", mism, miss)
	}
}

func TestCrossCheckFindsEveryKindOfDisagreement(t *testing.T) {
	base := func() (TierB, []proxy.Exchange) {
		b := ParsePiEvents(fixtureLines(t))
		ex := realFlow()
		ex[0].ToolCalls[0].AgentID = b.Calls[0].ID
		ex[1].ToolResults[0].AgentCallID = b.Calls[0].ID
		return b, ex
	}
	t.Run("agent reports a call the model never made", func(t *testing.T) {
		b, ex := base()
		b.Calls = append(b.Calls, BCall{ID: "bash_x_9", Name: "bash", Args: json.RawMessage(`{"command":"rm -rf /"}`), ArgsHash: argsHash("bash", json.RawMessage(`{"command":"rm -rf /"}`))})
		mism, _ := CrossCheck(b, ex)
		if len(mism) != 1 || !strings.Contains(mism[0], "never made") {
			t.Fatalf("%v", mism)
		}
	})
	t.Run("stream omits a call the model made", func(t *testing.T) {
		b, ex := base()
		b.Calls, b.Results = nil, nil
		_, miss := CrossCheck(b, ex)
		if len(miss) != 1 || !strings.Contains(miss[0], "never reported") {
			t.Fatalf("%v", miss)
		}
	})
	t.Run("different arguments", func(t *testing.T) {
		b, ex := base()
		b.Calls[0].ArgsHash = "deadbeef"
		mism, _ := CrossCheck(b, ex)
		if len(mism) == 0 || !strings.Contains(strings.Join(mism, "|"), "different arguments") {
			t.Fatalf("%v", mism)
		}
	})
	t.Run("tool result differs from what the model was sent", func(t *testing.T) {
		b, ex := base()
		b.Results[0].Text = "feasible: true\n"
		mism, _ := CrossCheck(b, ex)
		if len(mism) != 1 || !strings.Contains(mism[0], "differs from what the model was sent") {
			t.Fatalf("%v", mism)
		}
	})
	t.Run("different final answer", func(t *testing.T) {
		b, ex := base()
		b.FinalText = "Everything is fine and I saved routes.json."
		mism, _ := CrossCheck(b, ex)
		if len(mism) != 1 || !strings.Contains(mism[0], "different final answer") {
			t.Fatalf("%v", mism)
		}
	})
	t.Run("join falls back to name and arguments without an alias", func(t *testing.T) {
		b, ex := base()
		ex[0].ToolCalls[0].AgentID = ""
		mism, miss := CrossCheck(b, ex)
		if len(mism) != 0 || len(miss) != 0 {
			t.Fatalf("%v %v", mism, miss)
		}
	})
}
