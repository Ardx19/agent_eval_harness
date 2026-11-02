package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const realKey = "AIzaSyREAL-KEY-THAT-MUST-NEVER-LEAK-0123456789"
const genPath = "/v1beta/models/gemini-3.5-flash-lite:streamGenerateContent"

func newProxy(t *testing.T, upstream *httptest.Server, mut func(*Config)) (*Proxy, *httptest.Server, *Recorder) {
	t.Helper()
	rec, err := NewRecorder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Upstream: upstream.URL, Key: realKey, Recorder: rec, RetryBase: time.Millisecond}
	if mut != nil {
		mut(&cfg)
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(p.Handler())
	t.Cleanup(front.Close)
	return p, front, rec
}

func post(t *testing.T, base, path, body string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", base+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

const okBody = `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4}}`

func okUpstream(calls *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			atomic.AddInt32(calls, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, okBody)
	}))
}

func TestForwardsWithRealKeyAndStripsGuestCredentials(t *testing.T) {
	var got http.Header
	var gotQuery string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, gotQuery = r.Header.Clone(), r.URL.RawQuery
		io.WriteString(w, okBody)
	}))
	defer up.Close()
	_, front, rec := newProxy(t, up, nil)
	code, _ := post(t, front.URL, genPath+"?alt=sse&key=dummy", `{"contents":[]}`,
		map[string]string{"x-goog-api-key": "dummy", "Authorization": "Bearer dummy", "Cookie": "x=y"})
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if got.Get("x-goog-api-key") != realKey {
		t.Fatalf("upstream did not receive the real key: %q", got.Get("x-goog-api-key"))
	}
	if got.Get("Authorization") != "" || got.Get("Cookie") != "" {
		t.Fatalf("guest credentials leaked upstream: %v", got)
	}
	if strings.Contains(gotQuery, "key=") || !strings.Contains(gotQuery, "alt=sse") {
		t.Fatalf("query not sanitized: %q", gotQuery)
	}
	// the real key must not appear anywhere the recorder wrote
	rec.Flush()
	err := filepath.WalkDir(filepath.Dir(filepath.Join(recDir(rec), "x")), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), realKey) {
				t.Errorf("real key found in recorded file %s", p)
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func recDir(r *Recorder) string { return r.dir }

func TestRejectsServerToolsUnlessAllowed(t *testing.T) {
	cases := map[string]string{
		"googleSearch":  `{"tools":[{"googleSearch":{}}]}`,
		"google_search": `{"tools":[{"google_search":{}}]}`,
		"urlContext":    `{"tools":[{"urlContext":{}}]}`,
		"codeExecution": `{"tools":[{"codeExecution":{}}]}`,
		"web_search":    `{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var calls int32
			up := okUpstream(&calls)
			defer up.Close()
			_, front, rec := newProxy(t, up, nil)
			code, msg := post(t, front.URL, genPath, body, nil)
			if code != 403 || atomic.LoadInt32(&calls) != 0 {
				t.Fatalf("code=%d upstream calls=%d", code, calls)
			}
			if !strings.Contains(msg, "PERMISSION_DENIED") {
				t.Fatalf("error should look like a provider error: %s", msg)
			}
			ex := rec.Snapshot()
			if len(ex) != 1 || !strings.HasPrefix(ex[0].Rejected, "server_tool:") {
				t.Fatalf("rejection not recorded: %+v", ex)
			}
		})
	}
	var calls int32
	up := okUpstream(&calls)
	defer up.Close()
	_, front, _ := newProxy(t, up, func(c *Config) { c.AllowWeb = true })
	if code, _ := post(t, front.URL, genPath, cases["googleSearch"], nil); code != 200 || calls != 1 {
		t.Fatalf("with AllowWeb the request must pass: code=%d calls=%d", code, calls)
	}
	// ordinary function tools are never blocked
	if code, _ := post(t, front.URL, genPath, `{"tools":[{"functionDeclarations":[{"name":"bash"}]}]}`, nil); code != 200 {
		t.Fatal("function declarations must pass")
	}
}

func TestPathModelAndSizePolicy(t *testing.T) {
	var calls int32
	up := okUpstream(&calls)
	defer up.Close()
	_, front, rec := newProxy(t, up, func(c *Config) { c.AllowedModels = []string{"gemini-3.5-flash-lite"}; c.MaxBodyBytes = 1024 })
	if code, _ := post(t, front.URL, "/v1beta/files", `{}`, nil); code != 403 {
		t.Fatalf("files endpoint must be refused, got %d", code)
	}
	if code, _ := post(t, front.URL, "/v1beta/cachedContents", `{}`, nil); code != 403 {
		t.Fatalf("cachedContents must be refused, got %d", code)
	}
	if code, _ := post(t, front.URL, "/v1beta/models/gemini-2.5-pro:generateContent", `{}`, nil); code != 403 {
		t.Fatalf("other model must be refused, got %d", code)
	}
	if code, _ := post(t, front.URL, genPath, strings.Repeat("x", 4096), nil); code != 413 {
		t.Fatalf("oversized body must be 413, got %d", code)
	}
	if calls != 0 {
		t.Fatalf("nothing refused may reach upstream (calls=%d)", calls)
	}
	if code, _ := post(t, front.URL, genPath, `{}`, nil); code != 200 {
		t.Fatalf("allowed model must pass, got %d", code)
	}
	reasons := map[string]bool{}
	for _, e := range rec.Snapshot() {
		reasons[e.Rejected] = true
	}
	for _, want := range []string{"forbidden_path", "model_not_allowed", "body_too_large"} {
		if !reasons[want] {
			t.Errorf("missing recorded rejection %q: %v", want, reasons)
		}
	}
}

func TestStreamingPassesThroughIncrementally(t *testing.T) {
	first := "data: " + `{"candidates":[{"content":{"role":"model","parts":[{"text":"weighing options","thought":true}]}}],"usageMetadata":{"promptTokenCount":10,"totalTokenCount":10}}` + "\n\n"
	second := "data: " + `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"bash","args":{"cmd":"ls","a":1}}}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":7,"totalTokenCount":22}}` + "\n\n"
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, first)
		w.(http.Flusher).Flush()
		<-release // the proxy must deliver chunk one before the upstream finishes
		io.WriteString(w, second)
	}))
	defer up.Close()
	p, front, rec := newProxy(t, up, nil)
	resp, err := http.Post(front.URL+genPath+"?alt=sse", "application/json", strings.NewReader(`{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, len(first))
	if _, err := io.ReadFull(resp.Body, buf); err != nil || string(buf) != first {
		t.Fatalf("first chunk not streamed through before upstream finished: %v %q", err, buf)
	}
	close(release)
	rest, _ := io.ReadAll(resp.Body)
	if string(rest) != second {
		t.Fatalf("remaining stream differs: %q", rest)
	}
	ex := rec.Snapshot()[0]
	if len(ex.ToolCalls) != 1 || ex.ToolCalls[0].Name != "bash" || !ex.ToolCalls[0].Synth || !strings.HasPrefix(ex.ToolCalls[0].ID, "g0.0.") {
		t.Fatalf("tool call / synthesized id: %+v", ex.ToolCalls)
	}
	if string(ex.ToolCalls[0].Args) != `{"a":1,"cmd":"ls"}` {
		t.Fatalf("args must be canonical (sorted keys): %s", ex.ToolCalls[0].Args)
	}
	if len(ex.Thinking) != 1 || ex.Thinking[0] != "weighing options" || !ex.ThinkingRequested {
		t.Fatalf("thinking: %+v", ex)
	}
	if ex.Usage.Total != 22 || ex.Usage.Thoughts != 7 || p.TokensUsed() != 22 {
		t.Fatalf("usage: %+v used=%d", ex.Usage, p.TokensUsed())
	}
	raw, err := rec.Blob(ex.RespSHA256)
	if err != nil || string(raw) != first+second {
		t.Fatalf("raw response blob must be byte-identical to what the guest saw: %v", err)
	}
}

func TestToolResultsAreLinkedToCalls(t *testing.T) {
	step := int32(0)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&step, 1) == 1 {
			io.WriteString(w, `{"candidates":[{"content":{"parts":[{"functionCall":{"id":"fc-1","name":"bash","args":{"cmd":"ls"}}}]}}],"usageMetadata":{"totalTokenCount":5}}`)
			return
		}
		io.WriteString(w, okBody)
	}))
	defer up.Close()
	_, front, rec := newProxy(t, up, nil)
	post(t, front.URL, genPath, `{"contents":[{"role":"user","parts":[{"text":"go"}]}]}`, nil)
	post(t, front.URL, genPath, `{"contents":[{"role":"user","parts":[{"text":"go"}]},{"role":"model","parts":[{"functionCall":{"name":"bash","args":{"cmd":"ls"}}}]},{"role":"user","parts":[{"functionResponse":{"name":"bash","response":{"output":"a b"}}}]}]}`, nil)
	// a result nobody asked for: forged or buggy
	post(t, front.URL, genPath, `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"retrosynthesis","response":{"output":"made up"}}}]}]}`, nil)
	ex := rec.Snapshot()
	if len(ex) != 3 {
		t.Fatalf("want 3 exchanges, got %d", len(ex))
	}
	if r := ex[1].ToolResults; len(r) != 1 || r[0].CallID != "fc-1" || r[0].Unmatched || r[0].Content != `{"output":"a b"}` {
		t.Fatalf("result not linked to its call: %+v", r)
	}
	if r := ex[2].ToolResults; len(r) != 1 || !r[0].Unmatched || r[0].CallID != "" {
		t.Fatalf("an unrequested result must be flagged: %+v", r)
	}
	if n := len(rec.Unanswered()); n != 0 {
		t.Fatalf("all calls were answered, got %d unanswered", n)
	}
}

func TestOnlyTrailingFunctionResponsesAreNew(t *testing.T) {
	body := `{"contents":[
	 {"role":"user","parts":[{"functionResponse":{"name":"old","response":{"x":1}}}]},
	 {"role":"model","parts":[{"text":"ok"}]},
	 {"role":"user","parts":[{"functionResponse":{"name":"new","response":{"y":2}}}]}]}`
	r := parseGoogleRequest([]byte(body)).Results
	if len(r) != 1 || r[0].Name != "new" {
		t.Fatalf("history must not be re-reported: %+v", r)
	}
}

func TestRetriesRateLimitsThenSucceeds(t *testing.T) {
	var n int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) <= 2 {
			w.WriteHeader(429)
			return
		}
		io.WriteString(w, okBody)
	}))
	defer up.Close()
	_, front, rec := newProxy(t, up, nil)
	if code, _ := post(t, front.URL, genPath, `{}`, nil); code != 200 {
		t.Fatalf("got %d", code)
	}
	if e := rec.Snapshot()[0]; e.Retries != 2 || e.Infra != "" {
		t.Fatalf("retries=%d infra=%q", e.Retries, e.Infra)
	}
}

func TestPersistentRateLimitIsInfra(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }))
	defer up.Close()
	_, front, rec := newProxy(t, up, nil)
	if code, _ := post(t, front.URL, genPath, `{}`, nil); code != 429 {
		t.Fatalf("status must pass through, got %d", code)
	}
	if e := rec.Snapshot()[0]; e.Infra != "rate_limited" || e.Retries != 10 {
		t.Fatalf("a rate limit is the harness's problem, not the agent's: %+v", e)
	}
}

func TestUpstreamDownIsInfra(t *testing.T) {
	up := httptest.NewServer(http.NotFoundHandler())
	up.Close()
	_, front, rec := newProxy(t, up, func(c *Config) { c.Retries = 1 })
	if code, _ := post(t, front.URL, genPath, `{}`, nil); code != 502 {
		t.Fatalf("got %d", code)
	}
	if e := rec.Snapshot()[0]; e.Infra != "upstream_unreachable" {
		t.Fatalf("%+v", e)
	}
}

func TestTokenBudget(t *testing.T) {
	var calls int32
	up := okUpstream(&calls)
	defer up.Close()
	p, front, rec := newProxy(t, up, func(c *Config) { c.TokenBudget = 6 })
	post(t, front.URL, genPath, `{}`, nil) // uses 4
	post(t, front.URL, genPath, `{}`, nil) // uses 4 more (total 8 >= 6)
	if p.BudgetExceeded() {
		t.Fatal("budget is only exceeded once a request is refused")
	}
	if code, _ := post(t, front.URL, genPath, `{}`, nil); code != 403 {
		t.Fatalf("over-budget request must be refused, got %d", code)
	}
	if !p.BudgetExceeded() || calls != 2 {
		t.Fatalf("exceeded=%v upstream calls=%d", p.BudgetExceeded(), calls)
	}
	if e := rec.Snapshot()[2]; e.Rejected != "budget_exceeded" {
		t.Fatalf("%+v", e)
	}
}

func TestServeConnOverUnixSocket(t *testing.T) {
	up := okUpstream(nil)
	defer up.Close()
	p, _, _ := newProxy(t, up, nil)
	dir, _ := os.MkdirTemp("/tmp", "evh-px-")
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c, _ := l.Accept()
		p.ServeConn(ctx, c)
	}()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return net.Dial("unix", sock)
	}}}
	for i := 0; i < 2; i++ { // keep-alive: two requests on the one vsock connection
		resp, err := client.Post("http://guest"+genPath, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(b), "hi") {
			t.Fatalf("round trip %d: %d %s", i, resp.StatusCode, b)
		}
	}
	client.CloseIdleConnections()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("ServeConn did not return after the connection closed")
	}
	cancel()
}

type fakeSys struct {
	out string
	err error
}

func (f fakeSys) OpenKVM() error                                         { return nil }
func (f fakeSys) ReadFile(string) ([]byte, error)                        { return nil, os.ErrNotExist }
func (f fakeSys) LookPath(string) (string, error)                        { return "", errors.New("n/a") }
func (f fakeSys) FileSize(string) (int64, error)                         { return 0, os.ErrNotExist }
func (f fakeSys) SHA256(string) (string, error)                          { return "", os.ErrNotExist }
func (f fakeSys) Run(context.Context, string, ...string) (string, error) { return f.out, f.err }

func TestFetchKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	if k, err := FetchKey(context.Background(), fakeSys{out: "AIzaFromPi\n"}, "google", "m"); err != nil || k != "AIzaFromPi" {
		t.Fatalf("got %q %v", k, err)
	}
	t.Setenv("GEMINI_API_KEY", "AIzaFromEnv")
	if k, err := FetchKey(context.Background(), fakeSys{err: errors.New("exit 1")}, "google", "m"); err != nil || k != "AIzaFromEnv" {
		t.Fatalf("env fallback: got %q %v", k, err)
	}
	t.Setenv("GEMINI_API_KEY", "")
	_, err := FetchKey(context.Background(), fakeSys{out: "SECRET-IN-OUTPUT", err: errors.New("exit 1")}, "google", "m")
	if err == nil || strings.Contains(err.Error(), "SECRET-IN-OUTPUT") {
		t.Fatalf("error must exist and must not echo command output: %v", err)
	}
}

func TestParseResponseShapes(t *testing.T) {
	obj := `{"candidates":[{"content":{"parts":[{"text":"a"},{"functionCall":{"name":"f","args":{"k":"v"}}}]}}],"usageMetadata":{"totalTokenCount":9},"modelVersion":"m-1"}`
	for name, body := range map[string]string{"object": obj, "array": "[" + obj + "]", "sse": "data: " + obj + "\n\ndata: [DONE]\n\n"} {
		p := parseGoogleResponse([]byte(body), 4)
		if len(p.Calls) != 1 || p.Calls[0].Name != "f" || p.Text != "a" || p.Usage.Total != 9 || p.Model != "m-1" {
			t.Errorf("%s: %+v", name, p)
		}
	}
	if len(parseGoogleResponse([]byte("garbage"), 0).Calls) != 0 {
		t.Error("garbage must parse to nothing, not panic")
	}
	a, b := parseGoogleResponse([]byte(obj), 0), parseGoogleResponse([]byte(strings.Replace(obj, `{"k":"v"}`, `{ "k" : "v" }`, 1)), 0)
	if a.Calls[0].ArgsHash != b.Calls[0].ArgsHash {
		t.Error("args hash must not depend on formatting")
	}
}

func TestNewValidates(t *testing.T) {
	rec, _ := NewRecorder("")
	if _, err := New(Config{Recorder: rec}); err == nil {
		t.Error("missing key must fail")
	}
	if _, err := New(Config{Key: "k"}); err == nil {
		t.Error("missing recorder must fail")
	}
	_ = json.Marshal
	_ = fmt.Sprint
}

// A request captured from the real pi 0.85.1 (Google provider, baseUrl override).
// If pi changes how it talks to Gemini, this fails before an eval run does.
func TestRealPiRequestShapeIsAccepted(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/fixtures/pi_google_request.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Path, Query string
		Body        json.RawMessage
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if tools := ServerTools(fx.Body); len(tools) != 0 {
		t.Fatalf("pi declares no server-side tools; got %v", tools)
	}
	if pr := parseGoogleRequest(fx.Body); !pr.Thinking {
		t.Fatal("pi asks for thought summaries, so thinking must be capturable")
	}
	if gd, _ := dialectFor("google"); !gd.call.MatchString(fx.Path) {
		t.Fatalf("pi's real endpoint %q is rejected by the proxy allowlist", fx.Path)
	}
	var calls int32
	up := okUpstream(&calls)
	defer up.Close()
	_, front, rec := newProxy(t, up, func(c *Config) { c.AllowedModels = []string{"gemini-3.5-flash-lite"} })
	code, _ := post(t, front.URL, fx.Path+"?"+fx.Query, string(fx.Body), map[string]string{"x-goog-api-key": "dummy-key"})
	if code != 200 || calls != 1 {
		t.Fatalf("pi's real request must pass the proxy: code=%d upstream calls=%d", code, calls)
	}
	if e := rec.Snapshot()[0]; e.Model != "gemini-3.5-flash-lite" || !e.ThinkingRequested {
		t.Fatalf("%+v", e)
	}
}

// The real two-request flow from pi 0.85.1 (tool call, then its result). The
// model returned no id, so pi invented one and echoes it back; the proxy must
// learn the alias and link the result to the call instead of calling it unmatched.
func TestRealPiToolFlowLinksThroughTheAgentsOwnIds(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/fixtures/pi_google_tool_flow.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct{ Requests []json.RawMessage }
	if err := json.Unmarshal(raw, &fx); err != nil || len(fx.Requests) != 2 {
		t.Fatalf("fixture: %v", err)
	}
	step := int32(0)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&step, 1) == 1 {
			io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"I will list the files.","thought":true},{"functionCall":{"name":"bash","args":{"command":"echo hello-from-tool && ls"}}}]}}],"usageMetadata":{"totalTokenCount":63}}`)
			return
		}
		io.WriteString(w, okBody)
	}))
	defer up.Close()
	_, front, rec := newProxy(t, up, nil)
	for _, b := range fx.Requests {
		if code, _ := post(t, front.URL, genPath+"?alt=sse", string(b), nil); code != 200 {
			t.Fatalf("status %d", code)
		}
	}
	ex := rec.Snapshot()
	call := ex[0].ToolCalls[0]
	if !call.Synth || call.AgentID == "" || !strings.HasPrefix(call.AgentID, "bash_") {
		t.Fatalf("the proxy must learn pi's id for the call: %+v", ex[0].ToolCalls)
	}
	res := ex[1].ToolResults
	if len(res) != 1 || res[0].Unmatched || res[0].CallID != call.ID || res[0].AgentCallID != call.AgentID {
		t.Fatalf("the result must link to the call it answers: %+v", res)
	}
	if res[0].Content != `{"output":"hello-from-tool\n"}` {
		t.Fatalf("content as the model saw it: %s", res[0].Content)
	}
	if len(rec.Unanswered()) != 0 {
		t.Fatal("the call was answered")
	}
}

// An id the proxy has never seen, on a name that was never called, is flagged:
// the agent cannot manufacture a result by inventing an id.
func TestInventedIdsAreStillUnmatched(t *testing.T) {
	up := okUpstream(nil)
	defer up.Close()
	_, front, rec := newProxy(t, up, nil)
	post(t, front.URL, genPath, `{"contents":[{"role":"user","parts":[{"functionResponse":{"id":"made_up_1","name":"retrosynthesis","response":{"output":"fake"}}}]}]}`, nil)
	if r := rec.Snapshot()[0].ToolResults; len(r) != 1 || !r[0].Unmatched {
		t.Fatalf("%+v", r)
	}
}

func TestRetryDelayReadsProviderHints(t *testing.T) {
	body := []byte(`{"error":{"message":"quota. Please retry in 51.287658847s.","details":[{"@type":"x.RetryInfo","retryDelay":"51s"}]}}`)
	if d, ok := retryDelay("", body); !ok || d < 51*time.Second || d > 53*time.Second {
		t.Fatalf("got %v %v", d, ok)
	}
	if d, ok := retryDelay("7", nil); !ok || d != 7*time.Second {
		t.Fatalf("Retry-After: %v %v", d, ok)
	}
	if _, ok := retryDelay("", []byte(`{}`)); ok {
		t.Fatal("no hint, no delay")
	}
}

// ---- OpenAI-compatible dialect ----

const oaSSE = `data: {"id":"c1","model":"hy3","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"think "}}]}

data: {"id":"c1","model":"hy3","choices":[{"index":0,"delta":{"reasoning_content":"hard"}}]}

data: {"id":"c1","model":"hy3","choices":[{"index":0,"delta":{"content":"Running it. "}}]}

data: {"id":"c1","model":"hy3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"bash","arguments":"{\"command\":"}}]}}]}

data: {"id":"c1","model":"hy3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls -la\"}"}},{"index":1,"id":"call_b","type":"function","function":{"name":"read","arguments":"{\"path\":\"a.txt\"}"}}]}}]}

data: {"id":"c1","model":"hy3","choices":[{"index":0,"finish_reason":"tool_calls","delta":{}}],"usage":{"prompt_tokens":120,"completion_tokens":30,"total_tokens":150,"completion_tokens_details":{"reasoning_tokens":8}}}

data: [DONE]

`

func TestParseOpenAIStreamReassemblesToolCalls(t *testing.T) {
	r := parseOpenAIResponse([]byte(oaSSE), 3)
	if r.Text != "Running it. " || len(r.Thinking) != 1 || r.Thinking[0] != "think hard" || r.Model != "hy3" {
		t.Fatalf("%+v", r)
	}
	if len(r.Calls) != 2 || r.Calls[0].ID != "call_a" || r.Calls[0].Name != "bash" || string(r.Calls[0].Args) != `{"command":"ls -la"}` || r.Calls[1].Name != "read" {
		t.Fatalf("calls: %+v", r.Calls)
	}
	if r.Usage.Prompt != 120 || r.Usage.Completion != 30 || r.Usage.Total != 150 || r.Usage.Thoughts != 8 {
		t.Fatalf("usage: %+v", r.Usage)
	}
}

func TestParseOpenAIWholeMessageWithTwoCalls(t *testing.T) {
	body := `{"model":"hy3","choices":[{"message":{"role":"assistant","content":"","tool_calls":[` +
		`{"id":"x1","type":"function","function":{"name":"a","arguments":"{}"}},{"id":"","type":"function","function":{"name":"b","arguments":"{\"k\":1}"}}]}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`
	r := parseOpenAIResponse([]byte(body), 0)
	if len(r.Calls) != 2 || r.Calls[0].ID != "x1" || !r.Calls[1].Synth || r.Calls[1].Name != "b" {
		t.Fatalf("%+v", r.Calls)
	}
}

func TestParseOpenAIRequestToolResults(t *testing.T) {
	body := `{"model":"hy3","reasoning_effort":"high","messages":[{"role":"user","content":"hi"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_a","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]},` +
		`{"role":"tool","tool_call_id":"call_a","content":"file1\nfile2"}]}`
	p := parseOpenAIRequest([]byte(body))
	if !p.Thinking || len(p.Results) != 1 || p.Results[0].CallID != "call_a" || p.Results[0].Name != "bash" || p.Results[0].Content != "file1\nfile2" {
		t.Fatalf("%+v", p)
	}
	if len(p.Echoes) != 1 || p.Echoes[0].ID != "call_a" {
		t.Fatalf("echoes %+v", p.Echoes)
	}
	// results already reported in an earlier request are not new
	again := `{"messages":[{"role":"tool","tool_call_id":"old","content":"o"},{"role":"assistant","content":"ok"},{"role":"user","content":"more"}]}`
	if p := parseOpenAIRequest([]byte(again)); len(p.Results) != 0 {
		t.Fatalf("%+v", p.Results)
	}
}

func TestOpenAIServerToolsAreRefused(t *testing.T) {
	if got := OpenAIServerTools([]byte(`{"tools":[{"type":"function","function":{"name":"x"}},{"type":"web_search"}]}`)); len(got) != 1 || got[0] != "web_search" {
		t.Fatalf("%v", got)
	}
	if got := OpenAIServerTools([]byte(`{"tools":[{"type":"function"}]}`)); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestOpenAIProxyEndToEnd(t *testing.T) {
	var gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, oaSSE)
	}))
	defer up.Close()
	rec, err := NewRecorder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	px, err := New(Config{API: "openai", Upstream: up.URL, Key: "real-key", Recorder: rec, AllowedModels: []string{"hy3"}})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(px.Handler())
	defer front.Close()

	code, _ := post(t, front.URL, "/v1/chat/completions", `{"model":"hy3","messages":[{"role":"user","content":"hi"}]}`, map[string]string{"Authorization": "Bearer dummy"})
	if code != 200 || gotAuth != "Bearer real-key" {
		t.Fatalf("code=%d upstream saw auth %q: the guest's credential must be replaced", code, gotAuth)
	}
	ex := rec.Snapshot()[0]
	if ex.Model != "hy3" || len(ex.ToolCalls) != 2 || ex.Usage.Total != 150 || px.TokensUsed() != 150 {
		t.Fatalf("%+v", ex)
	}
	if code, _ := post(t, front.URL, "/v1/chat/completions", `{"model":"other","messages":[]}`, nil); code != 403 {
		t.Fatalf("a model outside the allow list must be refused, got %d", code)
	}
	if code, _ := post(t, front.URL, "/v1/chat/completions", `{"model":"hy3","tools":[{"type":"web_search"}],"messages":[]}`, nil); code != 403 {
		t.Fatalf("hosted tools must be refused, got %d", code)
	}
	if code, _ := post(t, front.URL, "/v1/files", `{}`, nil); code != 403 {
		t.Fatalf("other endpoints must be refused, got %d", code)
	}
}

func TestUnknownDialectIsRejected(t *testing.T) {
	rec, _ := NewRecorder(t.TempDir())
	if _, err := New(Config{API: "carrier-pigeon", Key: "k", Recorder: rec}); err == nil {
		t.Fatal("an unknown dialect must be an error")
	}
}

func TestUpstreamPathPrefixIsKept(t *testing.T) {
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		io.WriteString(w, oaSSE)
	}))
	defer up.Close()
	rec, _ := NewRecorder(t.TempDir())
	px, err := New(Config{API: "openai", Upstream: up.URL + "/zen/go/", Key: "k", Recorder: rec})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(px.Handler())
	defer front.Close()
	if code, _ := post(t, front.URL, "/v1/chat/completions", `{"model":"hy3","messages":[]}`, nil); code != 200 || gotPath != "/zen/go/v1/chat/completions" {
		t.Fatalf("code=%d upstream path %q", code, gotPath)
	}
}

func TestRoutingHeadersAreForwardedButNotCredentials(t *testing.T) {
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		io.WriteString(w, oaSSE)
	}))
	defer up.Close()
	rec, _ := NewRecorder(t.TempDir())
	px, err := New(Config{API: "openai", Upstream: up.URL, Key: "real", Recorder: rec})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(px.Handler())
	defer front.Close()
	post(t, front.URL, "/v1/chat/completions", `{"model":"hy3","messages":[]}`, map[string]string{
		"x-opencode-session": "ses_1", "x-opencode-project": "global", "Authorization": "Bearer dummy", "Cookie": "c=1", "X-Other": "no"})
	if got.Get("x-opencode-session") != "ses_1" || got.Get("x-opencode-project") != "global" {
		t.Fatalf("routing headers must reach the provider: %v", got)
	}
	if got.Get("Authorization") != "Bearer real" || got.Get("Cookie") != "" || got.Get("X-Other") != "" {
		t.Fatalf("only the key we hold and routing headers may be sent: %v", got)
	}
}

func TestDailyQuotaIsHandedBackAtOnce(t *testing.T) {
	var calls int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"code":429,"message":"Quota exceeded for metric: x_requests_per_day. Please retry in 6h28m17s.","details":[{"retryDelay":"23297s"}]}}`)
	}))
	defer up.Close()
	_, front, rec := newProxy(t, up, nil)
	start := time.Now()
	code, body := post(t, front.URL, genPath, `{}`, nil)
	if code != 429 || !strings.Contains(body, "Quota exceeded") {
		t.Fatalf("the provider's answer must pass through unchanged: %d %q", code, body)
	}
	if atomic.LoadInt32(&calls) != 1 || time.Since(start) > 3*time.Second {
		t.Fatalf("a daily quota must not be retried or waited for: %d calls in %v", calls, time.Since(start))
	}
	if e := rec.Snapshot()[0]; e.Infra != "rate_limited" {
		t.Fatalf("%+v", e)
	}
}

// InFlight counts exchanges that began and have not finished.
func TestRecorderInFlight(t *testing.T) {
	rec, err := NewRecorder("")
	if err != nil {
		t.Fatal(err)
	}
	a := rec.Begin()
	b := rec.Begin()
	if rec.InFlight() != 2 {
		t.Fatalf("in flight = %d, want 2", rec.InFlight())
	}
	rec.Finish(Exchange{Index: a}, nil, nil)
	if rec.InFlight() != 1 {
		t.Fatalf("after one finish, in flight = %d, want 1", rec.InFlight())
	}
	rec.Finish(Exchange{Index: b}, nil, nil)
	if rec.InFlight() != 0 {
		t.Fatalf("in flight = %d, want 0", rec.InFlight())
	}
}

// A malformed chunk in a model stream must not crash the parser or lose the valid
// chunks around it. The raw body is stored as a blob, so the evidence is kept.
func TestMalformedStreamChunkIsSkipped(t *testing.T) {
	body := "data: {\"model\":\"hy3\",\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n" +
		"data: {this is not json\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n" +
		"data: [DONE]\n"
	r := parseOpenAIResponse([]byte(body), 0)
	if r.Text != "Hello" || r.Model != "hy3" {
		t.Fatalf("text=%q model=%q", r.Text, r.Model)
	}
}
