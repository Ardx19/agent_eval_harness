// Package stub is the in-guest half of a tool call. It runs setuid to the tool
// uid, so it can read the per-trial key the agent cannot, and it signs the call
// and forwards it to the host toolbox over vsock. It never runs the tool.
package stub

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agentevalharness/internal/toolproto"
)

const maxInput = 8 << 20

// Env makes the stub testable; production wiring is in cmd/toolstub.
type Env struct {
	Argv      []string
	Stdin     io.Reader
	StdinTTY  bool
	Stdout    io.Writer
	Stderr    io.Writer
	PID, PPID int
	KeyPath   string
	ConfGlob  string
	Dial      func() (io.ReadWriteCloser, error)
	// DropToCaller sets the effective uid to the real uid. Everything the agent
	// names (files, stdin) is read after this, with the agent's own privileges.
	DropToCaller func() error
	Counter      func() uint64
}

// Exit codes. Messages stay generic so nothing about the host leaks.
const (
	exitUnavailable = 69
	exitNotFound    = 127
)

// Run performs one tool call and returns the exit code to use.
func Run(e Env) int {
	if len(e.Argv) == 0 {
		return exitNotFound
	}
	argv0 := filepath.Base(e.Argv[0])
	conf, ok := findConf(e.ConfGlob, argv0)
	if !ok {
		fmt.Fprintf(e.Stderr, "%s: command not found\n", argv0)
		return exitNotFound
	}

	// 1. The key: readable only to the tool uid, so this must happen first.
	key, err := os.ReadFile(e.KeyPath)
	if err != nil || len(bytes.TrimSpace(key)) == 0 {
		fmt.Fprintf(e.Stderr, "%s: tool unavailable\n", argv0)
		return exitUnavailable
	}
	key = bytes.TrimSpace(key)

	// 2. From here on, act as the caller. A setuid program that opens
	// agent-chosen paths with its own uid would be a confused deputy.
	if e.DropToCaller != nil {
		if err := e.DropToCaller(); err != nil {
			fmt.Fprintf(e.Stderr, "%s: tool unavailable\n", argv0)
			return exitUnavailable
		}
	}

	req := &toolproto.Request{V: toolproto.Version, PID: e.PID, PPID: e.PPID, Argv0: argv0, Args: append([]string{}, e.Argv[1:]...)}
	for _, path := range filePaths(req.Args, conf.FileFlags) {
		b, err := readCapped(path)
		if err != nil {
			fmt.Fprintf(e.Stderr, "%s: cannot read %s: %v\n", argv0, path, unwrap(err))
			return 1
		}
		if req.Files == nil {
			req.Files = map[string][]byte{}
		}
		req.Files[path] = b
	}
	if conf.Stdin && !e.StdinTTY && e.Stdin != nil {
		b, err := io.ReadAll(io.LimitReader(e.Stdin, maxInput+1))
		if err != nil || len(b) > maxInput {
			fmt.Fprintf(e.Stderr, "%s: input too large\n", argv0)
			return 1
		}
		req.Stdin = b
	}

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return exitUnavailable
	}
	req.Nonce = hex.EncodeToString(nonce)
	if e.Counter != nil {
		req.Counter = e.Counter()
	} else {
		req.Counter = uint64(time.Now().UnixNano())
	}
	req.Sign(key)

	conn, err := e.Dial()
	if err != nil {
		fmt.Fprintf(e.Stderr, "%s: tool unavailable\n", argv0)
		return exitUnavailable
	}
	defer conn.Close()
	if err := toolproto.WriteFrame(conn, req); err != nil {
		fmt.Fprintf(e.Stderr, "%s: tool unavailable\n", argv0)
		return exitUnavailable
	}
	var resp toolproto.Response
	if err := toolproto.ReadFrame(conn, &resp, toolproto.MaxFrame); err != nil {
		fmt.Fprintf(e.Stderr, "%s: tool unavailable\n", argv0)
		return exitUnavailable
	}
	if !resp.OK {
		fmt.Fprintf(e.Stderr, "%s: tool unavailable\n", argv0)
		return exitUnavailable
	}
	e.Stdout.Write(resp.Stdout)
	e.Stderr.Write(resp.Stderr)
	return resp.ExitCode
}

// filePaths returns the values of the flags that name files, in both
// "--flag path" and "--flag=path" forms.
func filePaths(args, fileFlags []string) []string {
	isFile := map[string]bool{}
	for _, f := range fileFlags {
		isFile[f] = true
	}
	var out []string
	for i := 0; i < len(args); i++ {
		if isFile[args[i]] && i+1 < len(args) {
			out = append(out, args[i+1])
			i++
			continue
		}
		if name, val, ok := strings.Cut(args[i], "="); ok && isFile[name] {
			out = append(out, val)
		}
	}
	return out
}

func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxInput+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxInput {
		return nil, fmt.Errorf("file too large")
	}
	return b, nil
}

func unwrap(err error) string {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err.Error()
	}
	return err.Error()
}

func findConf(glob, argv0 string) (toolproto.StubConf, bool) {
	matches, _ := filepath.Glob(glob)
	for _, m := range matches {
		raw, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var c toolproto.StubConf
		if jsonUnmarshal(raw, &c) == nil && c.Argv0 == argv0 {
			return c, true
		}
	}
	return toolproto.StubConf{}, false
}
