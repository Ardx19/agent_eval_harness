package skills

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"agentevalharness/internal/toolproto"
)

// Tool is one tool the toolbox serves.
type Tool struct {
	Manifest *Manifest
	Runner   ToolRunner
}

// CallRecord is Tier A evidence about one accepted call, measured by the host.
// The agent can neither see nor alter it.
type CallRecord struct {
	CallID      string
	PID, PPID   int
	Counter     uint64
	Nonce       string
	Tool        string
	Argv        []string // exactly as the agent typed them
	ArgsValid   bool
	Validation  Validation
	TStart      float64 // seconds since the trial started
	Result      Result
	RunnerError string // the tool could not be started: an infra problem
	// What the agent asked for, parsed on the host. Params holds scalar values from
	// the request ("kwargs.min_confidence", "mode", "skill"); InputText is the
	// stdin plus file contents the agent supplied (capped), used to tell a tool's
	// result from a value the agent merely passed in and had echoed back.
	Params    map[string]any
	InputText string
}

// Rejection records a call the toolbox refused. A bad signature means something
// in the guest spoke the protocol without the key.
type Rejection struct {
	At     time.Time
	Reason string // bad_mac | replay | bad_request | unknown_tool | frame
	PID    int
	Argv0  string
}

const (
	maxArgs   = 64
	maxArgLen = 64 << 10
	maxKept   = 1000 // rejections kept in memory; a flood only increments a counter
)

// Toolbox verifies and runs signed tool calls arriving over vsock. It is a
// vm.Handler: the guest is treated as hostile at every step.
type Toolbox struct {
	Epoch time.Time
	key   []byte
	tools map[string]*Tool

	mu       sync.Mutex
	nonces   map[string]bool
	counters map[uint64]bool
	calls    []CallRecord
	rejects  []Rejection
	rejectsN int
	nextID   int
}

func NewToolbox(key []byte, tools ...*Tool) (*Toolbox, error) {
	if len(key) < 16 {
		return nil, fmt.Errorf("toolbox key must be at least 16 bytes")
	}
	t := &Toolbox{Epoch: time.Now(), key: key, tools: map[string]*Tool{}, nonces: map[string]bool{}, counters: map[uint64]bool{}}
	for _, tool := range tools {
		t.tools[tool.Manifest.Entrypoint.Argv0] = tool
	}
	return t, nil
}

func (t *Toolbox) Records() []CallRecord {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]CallRecord(nil), t.calls...)
}

func (t *Toolbox) Rejections() (kept []Rejection, total int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Rejection(nil), t.rejects...), t.rejectsN
}

func (t *Toolbox) reject(reason string, req *toolproto.Request) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rejectsN++
	if len(t.rejects) < maxKept {
		r := Rejection{At: time.Now(), Reason: reason}
		if req != nil {
			r.PID, r.Argv0 = req.PID, req.Argv0
		}
		t.rejects = append(t.rejects, r)
	}
}

// unavailable is the only thing the guest learns about a refusal.
var unavailable = &toolproto.Response{OK: false, Error: "unavailable"}

// ServeConn handles one connection: one signed request, one response.
func (t *Toolbox) ServeConn(ctx context.Context, conn net.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	var req toolproto.Request
	if err := toolproto.ReadFrame(conn, &req, toolproto.MaxFrame); err != nil {
		t.reject("frame", nil)
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	resp := t.handle(ctx, &req)
	_ = conn.SetWriteDeadline(time.Now().Add(60 * time.Second))
	_ = toolproto.WriteFrame(conn, resp)
}

func (t *Toolbox) handle(ctx context.Context, req *toolproto.Request) *toolproto.Response {
	if req.V != toolproto.Version || !t.verified(req) {
		t.reject("bad_mac", req)
		return unavailable
	}
	// the signature is good, so this came from the stub; now refuse replays
	t.mu.Lock()
	replay := t.nonces[req.Nonce] || t.counters[req.Counter]
	if !replay {
		t.nonces[req.Nonce], t.counters[req.Counter] = true, true
	}
	t.mu.Unlock()
	if replay || req.Nonce == "" {
		t.reject("replay", req)
		return unavailable
	}
	tool, ok := t.tools[req.Argv0]
	if !ok {
		t.reject("unknown_tool", req)
		return unavailable
	}
	if len(req.Args) > maxArgs {
		t.reject("bad_request", req)
		return unavailable
	}
	for _, a := range req.Args {
		if len(a) > maxArgLen || strings.ContainsRune(a, 0) {
			t.reject("bad_request", req)
			return unavailable
		}
	}

	m := tool.Manifest
	rec := CallRecord{PID: req.PID, PPID: req.PPID, Counter: req.Counter, Nonce: req.Nonce, Tool: m.Name, Argv: append([]string{}, req.Args...)}
	hostArgs, usage, requestJSON, tmp, err := t.prepareArgs(m, req)
	if tmp != "" {
		defer os.RemoveAll(tmp)
	}
	t.mu.Lock()
	t.nextID++
	rec.CallID = fmt.Sprintf("tb%d", t.nextID)
	t.mu.Unlock()

	if err != nil {
		// the arguments are not something the real tool accepts: answer like its own
		// usage error, without spending a model load, and record it
		rec.ArgsValid, rec.Validation = false, Validation{Errors: []string{err.Error()}}
		rec.TStart = time.Since(t.Epoch).Seconds()
		rec.Result = Result{ExitCode: 2, Stderr: []byte(usage)}
		rec.Result.StdoutSHA256, rec.Result.StderrSHA256 = sum(nil), sum(rec.Result.Stderr)
		t.record(rec)
		return &toolproto.Response{OK: true, ExitCode: 2, Stderr: rec.Result.Stderr, CallID: rec.CallID}
	}

	rec.ArgsValid = true
	if len(m.Modes) > 0 && requestJSON != nil {
		rec.Validation = m.ValidateRequest(requestJSON)
		rec.ArgsValid = rec.Validation.Valid() // recorded, not enforced: the agent sees the real tool's behaviour
	}
	rec.Params = requestParams(requestJSON)
	rec.InputText = inputText(req)
	rec.TStart = time.Since(t.Epoch).Seconds()
	res, runErr := tool.Runner.Run(ctx, Request{Args: hostArgs, Stdin: req.Stdin})
	rec.Result = res
	if runErr != nil {
		rec.RunnerError = runErr.Error()
		t.record(rec)
		return unavailable
	}
	t.record(rec)
	return &toolproto.Response{OK: true, ExitCode: res.ExitCode, Stdout: res.Stdout, Stderr: res.Stderr, CallID: rec.CallID}
}

func (t *Toolbox) record(r CallRecord) {
	t.mu.Lock()
	t.calls = append(t.calls, r)
	t.mu.Unlock()
}

func (t *Toolbox) verified(req *toolproto.Request) bool {
	want := req.ComputeMAC(t.key)
	return subtle.ConstantTimeCompare([]byte(want), []byte(req.MAC)) == 1
}

var safeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// prepareArgs applies the tool's argument policy and materializes file
// arguments in a private temp directory. It returns the host argv, a usage
// message (when the arguments are refused), the request JSON to validate, and
// the temp directory to remove.
func (t *Toolbox) prepareArgs(m *Manifest, req *toolproto.Request) (hostArgs []string, usage string, requestJSON []byte, tmp string, err error) {
	flags := map[string]Flag{}
	var names []string
	for _, f := range m.CLI.Flags {
		flags[f.Name] = f
		names = append(names, f.Name)
	}
	usage = fmt.Sprintf("usage: %s [%s]\n%s: error: ", filepath.Base(m.Entrypoint.Path), strings.Join(names, "] ["), filepath.Base(m.Entrypoint.Path))
	fail := func(format string, a ...any) ([]string, string, []byte, string, error) {
		msg := fmt.Sprintf(format, a...)
		return nil, usage + msg + "\n", nil, tmp, fmt.Errorf("%s", msg)
	}

	mkTmp := func() error {
		if tmp != "" {
			return nil
		}
		d, e := os.MkdirTemp("", "evh-call-")
		tmp = d
		return e
	}
	materialize := func(path string) (string, error) {
		content, ok := req.Files[path]
		if !ok {
			return "", fmt.Errorf("argument file was not provided")
		}
		if err := mkTmp(); err != nil {
			return "", err
		}
		dst := filepath.Join(tmp, safeName.ReplaceAllString(filepath.Base(path), "_"))
		if _, err := os.Stat(dst); err == nil {
			dst = filepath.Join(tmp, fmt.Sprintf("%d_%s", len(hostArgs), filepath.Base(dst)))
		}
		return dst, os.WriteFile(dst, content, 0o600)
	}

	var impliedRequest, fileRequest []byte
	for i := 0; i < len(req.Args); i++ {
		a := req.Args[i]
		name, inline, hasInline := strings.Cut(a, "=")
		f, isFlag := flags[a]
		if !isFlag && hasInline {
			if ff, ok := flags[name]; ok && ff.TakesFile {
				f, isFlag, a = ff, true, name
			}
		}
		switch {
		case isFlag && f.TakesFile:
			val := inline
			if !hasInline || a != name {
				if i+1 >= len(req.Args) {
					return fail("argument %s: expected one argument", a)
				}
				i++
				val = req.Args[i]
			}
			dst, err := materialize(val)
			if err != nil {
				return fail("argument %s: cannot use that file", a)
			}
			fileRequest = req.Files[val]
			hostArgs = append(hostArgs, a, dst)
		case isFlag:
			hostArgs = append(hostArgs, a)
			if f.ImpliesRequest != "" {
				impliedRequest = []byte(f.ImpliesRequest)
			}
		case strings.HasPrefix(a, "-") && !m.CLI.AllowPositional:
			return fail("unrecognized arguments: %s", a)
		case !m.CLI.AllowPositional:
			return fail("unrecognized arguments: %s", a)
		default:
			hostArgs = append(hostArgs, a)
		}
	}
	switch {
	case impliedRequest != nil:
		requestJSON = impliedRequest
	case fileRequest != nil:
		requestJSON = fileRequest
	case m.CLI.StdinJSON && len(req.Stdin) > 0:
		requestJSON = req.Stdin
	}
	return hostArgs, "", requestJSON, tmp, nil
}

// requestParams flattens the scalar values of a request: top-level keys as-is,
// kwargs entries as "kwargs.<name>". Large structures (route trees) are skipped.
func requestParams(raw []byte) map[string]any {
	if raw == nil {
		return nil
	}
	var req map[string]any
	if json.Unmarshal(raw, &req) != nil {
		return nil
	}
	out := map[string]any{}
	add := func(prefix string, m map[string]any) {
		for k, v := range m {
			switch v.(type) {
			case string, float64, bool:
				out[prefix+k] = v
			}
		}
	}
	add("", req)
	if kw, ok := req["kwargs"].(map[string]any); ok {
		add("kwargs.", kw)
	}
	return out
}

const maxInputText = 1 << 20

func inputText(req *toolproto.Request) string {
	var b strings.Builder
	b.Write(req.Stdin)
	for _, name := range sortedFileNames(req.Files) {
		b.WriteByte('\n')
		b.Write(req.Files[name])
	}
	s := b.String()
	if len(s) > maxInputText {
		s = s[:maxInputText]
	}
	return s
}

func sortedFileNames(m map[string][]byte) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
