package toolproto

import (
	"bytes"
	"strings"
	"testing"
)

func sample() *Request {
	return &Request{V: 1, Counter: 7, Nonce: "ab12", PID: 41, PPID: 40, Argv0: "toy.sh", Args: []string{"file", "--request", "req.json"},
		Stdin: []byte("in"), Files: map[string][]byte{"req.json": []byte(`{"mode":"x"}`)}}
}

func TestSignAndVerify(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	r := sample()
	r.Sign(key)
	if !r.Verify(key) {
		t.Fatal("a signed request must verify")
	}
	if r.Verify([]byte("a different key entirely.........")) {
		t.Fatal("a request signed with another key must not verify")
	}
}

func TestEveryFieldIsAuthenticated(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	mutations := map[string]func(*Request){
		"counter":   func(r *Request) { r.Counter++ },
		"nonce":     func(r *Request) { r.Nonce = "zz" },
		"pid":       func(r *Request) { r.PID++ },
		"ppid":      func(r *Request) { r.PPID++ },
		"argv0":     func(r *Request) { r.Argv0 = "other.sh" },
		"arg":       func(r *Request) { r.Args[0] = "ok" },
		"extra arg": func(r *Request) { r.Args = append(r.Args, "--list-skills") },
		"stdin":     func(r *Request) { r.Stdin = []byte("x") },
		"file":      func(r *Request) { r.Files["req.json"] = []byte("evil") },
		"file name": func(r *Request) { r.Files["other.json"] = r.Files["req.json"]; delete(r.Files, "req.json") },
		"version":   func(r *Request) { r.V = 2 },
	}
	for name, mut := range mutations {
		r := sample()
		r.Sign(key)
		mut(r)
		if r.Verify(key) {
			t.Errorf("changing %s must invalidate the signature", name)
		}
	}
}

// Without length prefixes these two requests would hash to the same bytes.
func TestEncodingIsUnambiguous(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	a := &Request{V: 1, Argv0: "t", Args: []string{"ab", "c"}}
	b := &Request{V: 1, Argv0: "t", Args: []string{"a", "bc"}}
	if a.ComputeMAC(key) == b.ComputeMAC(key) {
		t.Fatal("argument boundaries must be part of the signature")
	}
}

func TestMACIsMalformedSafe(t *testing.T) {
	r := sample()
	r.MAC = "not hex"
	if r.Verify([]byte("k")) {
		t.Fatal("garbage MAC must not verify")
	}
	r.MAC = ""
	if r.Verify([]byte("k")) {
		t.Fatal("empty MAC must not verify")
	}
}

func TestFramesRoundTripAndBound(t *testing.T) {
	var buf bytes.Buffer
	in := &Response{OK: true, ExitCode: 3, Stdout: []byte("out"), CallID: "tb1"}
	if err := WriteFrame(&buf, in); err != nil {
		t.Fatal(err)
	}
	var out Response
	if err := ReadFrame(&buf, &out, 1<<20); err != nil || out.ExitCode != 3 || string(out.Stdout) != "out" {
		t.Fatalf("%v %+v", err, out)
	}
	// a hostile length must be refused before any allocation
	hostile := bytes.NewReader([]byte{0xff, 0xff, 0xff, 0xff, 'x'})
	if err := ReadFrame(hostile, &out, 1<<20); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized frame must be refused: %v", err)
	}
	if err := ReadFrame(bytes.NewReader([]byte{0, 0}), &out, 10); err == nil {
		t.Fatal("a truncated header must be an error")
	}
	if err := ReadFrame(bytes.NewReader([]byte{0, 0, 0, 5, 'a'}), &out, 10); err == nil {
		t.Fatal("a truncated body must be an error")
	}
	if err := ReadFrame(bytes.NewReader([]byte{0, 0, 0, 3, 'n', 'o', 't'}), &out, 10); err == nil {
		t.Fatal("non-JSON must be an error")
	}
}
