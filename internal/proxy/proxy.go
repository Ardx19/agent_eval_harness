// Package proxy is the host-side model proxy. The guest has no network; its
// only route to a model is vsock port 5000 to this proxy, which holds the real
// key, allows only model-call endpoints, rejects server-side tools, records
// every exchange, and enforces a token budget.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxBody   = 32 << 20
	defaultMaxRecord = 64 << 20
)

// A dialect is how one provider family spells model calls. Only model calls and
// the model list are reachable; files, cached contents, tuned models and
// everything else are refused.
type dialect struct {
	name            string
	defaultUpstream string
	call, list      *regexp.Regexp
	modelFromBody   func([]byte) string // nil: the model is in the path
	serverTools     func([]byte) []string
	parseRequest    func([]byte) parsedRequest
	parseResponse   func([]byte, int) parsedResponse
	setAuth         func(*http.Request, string)
	// forwardPrefixes are lowercase header-name prefixes passed through to the
	// provider (routing and session identifiers, never credentials).
	forwardPrefixes []string
}

func dialectFor(api string) (dialect, error) {
	switch api {
	case "", "google":
		return dialect{name: "google", defaultUpstream: "https://generativelanguage.googleapis.com",
			call:        regexp.MustCompile(`^/v1(beta)?/models/[A-Za-z0-9._-]+:(streamGenerateContent|generateContent|countTokens)$`),
			list:        regexp.MustCompile(`^/v1(beta)?/models(/[A-Za-z0-9._-]+)?$`),
			serverTools: ServerTools, parseRequest: parseGoogleRequest, parseResponse: parseGoogleResponse,
			setAuth: func(r *http.Request, key string) { r.Header.Set("x-goog-api-key", key) }}, nil
	case "openai":
		return dialect{name: "openai", defaultUpstream: "https://opencode.ai/zen/go",
			call: reOpenAICall, list: reOpenAIList, modelFromBody: openAIModel,
			serverTools: OpenAIServerTools, parseRequest: parseOpenAIRequest, parseResponse: parseOpenAIResponse,
			setAuth:         func(r *http.Request, key string) { r.Header.Set("Authorization", "Bearer "+key) },
			forwardPrefixes: []string{"x-opencode-"}}, nil
	}
	return dialect{}, fmt.Errorf("proxy: unknown api dialect %q (google or openai)", api)
}

type Config struct {
	Upstream      string
	Key           string
	AllowWeb      bool
	AllowedModels []string // empty = any model
	MaxBodyBytes  int64
	MaxRecord     int64
	TokenBudget   int    // total tokens across the trial; 0 = unlimited
	API           string // "google" (default) or "openai": which chat dialect the guest agent speaks
	Retries       int    // retries on 429/503 before any bytes are streamed
	RetryBase     time.Duration
	Recorder      *Recorder
	Client        *http.Client
}

type Proxy struct {
	cfg    Config
	client *http.Client
	d      dialect

	mu      sync.Mutex
	used    int
	overrun bool
}

func New(cfg Config) (*Proxy, error) {
	if cfg.Key == "" {
		return nil, errors.New("proxy: API key is required")
	}
	if cfg.Recorder == nil {
		return nil, errors.New("proxy: recorder is required")
	}
	d, err := dialectFor(cfg.API)
	if err != nil {
		return nil, err
	}
	if cfg.Upstream == "" {
		cfg.Upstream = d.defaultUpstream
	}
	if _, err := url.Parse(cfg.Upstream); err != nil {
		return nil, err
	}
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = defaultMaxBody
	}
	if cfg.MaxRecord == 0 {
		cfg.MaxRecord = defaultMaxRecord
	}
	if cfg.Retries == 0 {
		cfg.Retries = 3
	}
	if cfg.RetryBase == 0 {
		cfg.RetryBase = time.Second
	}
	c := cfg.Client
	if c == nil {
		c = &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 180 * time.Second, MaxIdleConnsPerHost: 4}}
	}
	return &Proxy{cfg: cfg, client: c, d: d}, nil
}

// BudgetExceeded reports whether a request was refused for exceeding the token budget.
func (p *Proxy) BudgetExceeded() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.overrun
}

// TokensUsed is the provider-reported total so far.
func (p *Proxy) TokensUsed() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.used
}

func (p *Proxy) Handler() http.Handler { return http.HandlerFunc(p.serve) }

// InFlight is the number of model requests the proxy has started and not finished.
func (p *Proxy) InFlight() int { return p.cfg.Recorder.InFlight() }

func (p *Proxy) serve(w http.ResponseWriter, r *http.Request) {
	idx := p.cfg.Recorder.Begin()
	ex := Exchange{Index: idx, Start: time.Now(), Method: r.Method, Path: r.URL.Path, Model: modelFromPath(r.URL.Path)}
	finish := func(req, resp []byte) {
		ex.End = time.Now()
		p.cfg.Recorder.Finish(ex, req, resp)
	}
	reject := func(code int, reason, msg string, req []byte) {
		ex.Status, ex.Rejected = code, reason
		writeError(w, code, msg)
		finish(req, nil)
	}

	isCall := r.Method == http.MethodPost && p.d.call.MatchString(r.URL.Path)
	isList := r.Method == http.MethodGet && p.d.list.MatchString(r.URL.Path)
	if !isCall && !isList {
		reject(http.StatusForbidden, "forbidden_path", "this endpoint is not available", nil)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, p.cfg.MaxBodyBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			reject(http.StatusRequestEntityTooLarge, "body_too_large", "request body too large", nil)
		} else {
			reject(http.StatusBadRequest, "bad_request", "could not read request body", nil)
		}
		return
	}
	ex.ReqBytes = len(body)

	if isCall {
		if p.d.modelFromBody != nil {
			ex.Model = p.d.modelFromBody(body)
		}
		if len(p.cfg.AllowedModels) > 0 && !contains(p.cfg.AllowedModels, ex.Model) {
			reject(http.StatusForbidden, "model_not_allowed", "this model is not available", body)
			return
		}
		if tools := p.d.serverTools(body); len(tools) > 0 && !p.cfg.AllowWeb {
			reject(http.StatusForbidden, "server_tool:"+strings.Join(tools, ","), "server-side tools are not available", body)
			return
		}
		if p.cfg.TokenBudget > 0 && p.TokensUsed() >= p.cfg.TokenBudget {
			p.mu.Lock()
			p.overrun = true
			p.mu.Unlock()
			reject(http.StatusForbidden, "budget_exceeded", "token budget exhausted", body)
			return
		}
		pr := p.d.parseRequest(body)
		ex.ToolResults, ex.ThinkingRequested, ex.Echoes = pr.Results, pr.Thinking, pr.Echoes
	}

	resp, retries, err := p.forward(r, body)
	ex.Retries = retries
	if err != nil {
		ex.Status, ex.Infra = http.StatusBadGateway, "upstream_unreachable"
		writeError(w, http.StatusBadGateway, "upstream unreachable")
		finish(body, nil)
		return
	}
	defer resp.Body.Close()
	ex.Status = resp.StatusCode
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		ex.Infra = "rate_limited"
	case resp.StatusCode >= 500:
		ex.Infra = "upstream_error"
	}
	for _, h := range []string{"Content-Type", "Cache-Control", "Retry-After"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	var rec bytes.Buffer
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 16<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if int64(rec.Len()+n) <= p.cfg.MaxRecord {
				rec.Write(buf[:n])
			} else {
				ex.RespTruncated = true
			}
			if _, werr := w.Write(buf[:n]); werr != nil {
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}
	ex.RespBytes = rec.Len()
	if isCall && resp.StatusCode == http.StatusOK {
		pr := p.d.parseResponse(rec.Bytes(), idx)
		ex.ToolCalls, ex.Thinking, ex.Text, ex.Usage = pr.Calls, pr.Thinking, pr.Text, pr.Usage
		if pr.Model != "" && ex.Model == "" {
			ex.Model = pr.Model
		}
		p.mu.Lock()
		p.used += pr.Usage.Total
		p.mu.Unlock()
	}
	finish(body, rec.Bytes())
}

// forward sends the request upstream with the real key, retrying 429/503
// before any response bytes have been streamed to the guest.
func (p *Proxy) forward(r *http.Request, body []byte) (*http.Response, int, error) {
	u, _ := url.Parse(p.cfg.Upstream)
	u.Path = strings.TrimRight(u.Path, "/") + r.URL.Path // keep a prefix such as /zen/go
	q := r.URL.Query()
	q.Del("key") // never forward a guest-supplied credential
	u.RawQuery = q.Encode()

	var lastErr error
	// A rate limit is the provider asking us to wait, not a failure: honour its
	// retry delay for up to rateLimitRetries attempts. Other transient errors
	// use the shorter, configured retry count.
	const rateLimitRetries = 10
	maxRateLimitWait := 5 * time.Minute
	for attempt := 0; attempt <= rateLimitRetries; attempt++ {
		req, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), bytes.NewReader(body))
		if err != nil {
			return nil, attempt, err
		}
		for _, h := range []string{"Content-Type", "Accept", "User-Agent"} {
			if v := r.Header.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
		for name, vals := range r.Header {
			for _, pre := range p.d.forwardPrefixes {
				if strings.HasPrefix(strings.ToLower(name), pre) {
					req.Header[name] = vals
				}
			}
		}
		p.d.setAuth(req, p.cfg.Key)
		resp, err := p.client.Do(req)
		if err != nil {
			lastErr = err
			if r.Context().Err() != nil || attempt >= p.cfg.Retries {
				return nil, attempt, err
			}
		} else if resp.StatusCode == http.StatusTooManyRequests && attempt < rateLimitRetries ||
			resp.StatusCode == http.StatusServiceUnavailable && attempt < p.cfg.Retries {
			wait := p.cfg.RetryBase << min(attempt, 5)
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			d, hasDelay := retryDelay(resp.Header.Get("Retry-After"), raw)
			if hasDelay && d > maxRateLimitWait {
				// a quota that resets in hours (a daily limit) will not clear while the
				// trial runs: hand the 429 back now instead of stalling until the wall clock ends
				resp.Body = io.NopCloser(bytes.NewReader(raw))
				return resp, attempt, nil
			}
			if hasDelay && d > wait {
				wait = d
			}
			if wait > 120*time.Second {
				wait = 120 * time.Second
			}
			select {
			case <-time.After(wait):
			case <-r.Context().Done():
				return nil, attempt, r.Context().Err()
			}
			continue
		} else {
			return resp, attempt, nil
		}
		time.Sleep(p.cfg.RetryBase << attempt)
	}
	if lastErr == nil {
		lastErr = errors.New("upstream retries exhausted")
	}
	return nil, rateLimitRetries, lastErr
}

var reRetryDelay = regexp.MustCompile(`"retryDelay":\s*"([0-9.]+)s"|[Pp]lease retry in ([0-9.]+)s`)

// retryDelay reads how long the provider asked us to wait: the Retry-After header
// or the retry hint in a Google error body.
func retryDelay(header string, body []byte) (time.Duration, bool) {
	if n, err := strconv.Atoi(header); err == nil && n > 0 {
		return time.Duration(n) * time.Second, true
	}
	if m := reRetryDelay.FindSubmatch(body); m != nil {
		for _, g := range m[1:] {
			if f, err := strconv.ParseFloat(string(g), 64); err == nil && len(g) > 0 {
				return time.Duration((f + 1) * float64(time.Second)), true
			}
		}
	}
	return 0, false
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	status := map[int]string{403: "PERMISSION_DENIED", 413: "INVALID_ARGUMENT", 400: "INVALID_ARGUMENT", 502: "UNAVAILABLE"}[code]
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": msg, "status": status}})
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ServeConn serves HTTP over one vsock connection. Its signature matches vm.Handler.
func (p *Proxy) ServeConn(ctx context.Context, conn net.Conn) {
	l := &oneShot{conn: conn, done: make(chan struct{})}
	srv := &http.Server{
		Handler:           p.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ConnState: func(_ net.Conn, s http.ConnState) {
			if s == http.StateClosed || s == http.StateHijacked {
				l.release()
			}
		},
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			srv.Close()
		case <-stop:
		}
	}()
	_ = srv.Serve(l)
}

// oneShot is a listener that hands out a single connection, then blocks until
// that connection is finished so http.Server.Serve returns cleanly.
type oneShot struct {
	conn net.Conn
	used bool
	mu   sync.Mutex
	done chan struct{}
	once sync.Once
}

func (l *oneShot) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.used {
		l.used = true
		l.mu.Unlock()
		return l.conn, nil
	}
	l.mu.Unlock()
	<-l.done
	return nil, net.ErrClosed
}
func (l *oneShot) release()       { l.once.Do(func() { close(l.done) }) }
func (l *oneShot) Close() error   { l.release(); return nil }
func (l *oneShot) Addr() net.Addr { return dummyAddr{} }

type dummyAddr struct{}

func (dummyAddr) Network() string { return "vsock" }
func (dummyAddr) String() string  { return "guest" }
