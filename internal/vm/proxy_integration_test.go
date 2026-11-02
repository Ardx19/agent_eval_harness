package vm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"agentevalharness/internal/proxy"
)

const itKey = "AIzaSyREAL-KEY-NEVER-GOES-INTO-THE-GUEST-9876543210"

// A guest process reaches a (fake) model only through loopback -> guest-fwd ->
// vsock -> host proxy. The guest has no NIC and only a dummy key; the proxy
// injects the real one, records the exchange, links the tool result to its
// call, and refuses a server-side search tool.
func TestGuestReachesModelOnlyThroughProxy(t *testing.T) {
	r := needRig(t)

	var mu sync.Mutex
	var keysSeen []string
	var step int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		keysSeen = append(keysSeen, req.Header.Get("x-goog-api-key"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if atomic.AddInt32(&step, 1) == 1 {
			io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"bash","args":{"cmd":"ls"}}}]}}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3,"totalTokenCount":11}}`)
			return
		}
		io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"two files"}]}}],"usageMetadata":{"totalTokenCount":9}}`)
	}))
	defer upstream.Close()

	rec, err := proxy.NewRecorder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	px, err := proxy.New(proxy.Config{Upstream: upstream.URL, Key: itKey, Recorder: rec,
		AllowedModels: []string{"gemini-3.5-flash-lite"}})
	if err != nil {
		t.Fatal(err)
	}

	cfg := r.cfg(t, "/proxy-selftest")
	v, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Destroy()
	if err := v.Listen(5000, px.ServeConn); err != nil {
		t.Fatal(err)
	}
	if err := v.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := v.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	console := v.Console()
	if res.Exit != ExitOK || !strings.Contains(console, "SPIKE: DONE") {
		t.Fatalf("guest did not finish (exit %s); rerun images/build.sh if the image predates U5\n%s", res.Exit, tail(console, 25))
	}

	for _, want := range []string{
		"SPIKE: nics=lo ",
		"SPIKE: call1=200 tool=bash args={\"cmd\": \"ls\"}",
		"SPIKE: call2=200 text=two files",
		"SPIKE: search=403 status=PERMISSION_DENIED",
		"SPIKE: files=403",
	} {
		if !strings.Contains(console, want) {
			t.Errorf("guest output missing %q\n%s", want, tail(console, 15))
		}
	}
	if strings.Contains(console, itKey) {
		t.Fatal("the real API key appeared in the guest console")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(keysSeen) != 2 {
		t.Fatalf("only the two legitimate calls may reach upstream, saw %d", len(keysSeen))
	}
	for _, k := range keysSeen {
		if k != itKey {
			t.Fatalf("upstream must see the real key, got %q", k)
		}
	}

	ex := rec.Snapshot()
	if len(ex) != 4 {
		t.Fatalf("want 4 recorded exchanges, got %d: %+v", len(ex), ex)
	}
	var calls []proxy.Exchange
	for _, e := range ex {
		if e.Rejected == "" {
			calls = append(calls, e)
		}
	}
	if len(calls) != 2 || len(calls[0].ToolCalls) != 1 || calls[0].ToolCalls[0].Name != "bash" {
		t.Fatalf("first call should record the model's tool call: %+v", calls)
	}
	if rs := calls[1].ToolResults; len(rs) != 1 || rs[0].CallID != calls[0].ToolCalls[0].ID || rs[0].Unmatched {
		t.Fatalf("the tool result must link to the call that produced it: %+v", rs)
	}
	rejected := map[string]bool{}
	for _, e := range ex {
		if e.Rejected != "" {
			rejected[e.Rejected] = true
		}
	}
	if !rejected["server_tool:googleSearch"] || !rejected["forbidden_path"] {
		t.Fatalf("rejections not recorded: %v", rejected)
	}
	if px.TokensUsed() != 20 {
		t.Fatalf("token accounting: %d", px.TokensUsed())
	}
}

func tail(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
