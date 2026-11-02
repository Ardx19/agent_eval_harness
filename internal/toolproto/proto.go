// Package toolproto is the wire protocol between the in-guest setuid stub and
// the host toolbox (vsock port 5001). Every request is signed with a per-trial
// key that only the tool uid can read, so a process that merely speaks the
// protocol cannot produce a call the host will honour.
package toolproto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

const (
	Version  = 1
	MaxFrame = 40 << 20
	// KeyPath is where the per-trial key lives in the guest: owned by the tool
	// uid, mode 0400, in a 0700 directory.
	KeyPath = "/opt/svc/key"
	// ConfGlob finds each installed tool's stub configuration.
	ConfGlob = "/usr/share/agent-skills/*/stub.json"
)

// StubConf tells the stub how to treat one tool's arguments. It reveals only
// flag names, so it is world-readable.
type StubConf struct {
	Argv0     string   `json:"argv0"`
	FileFlags []string `json:"file_flags"` // flags whose value is a path the stub must read
	Stdin     bool     `json:"stdin"`      // forward stdin
}

// Request is one tool call.
type Request struct {
	V       int               `json:"v"`
	Counter uint64            `json:"counter"` // strictly increasing per trial
	Nonce   string            `json:"nonce"`
	PID     int               `json:"pid"`
	PPID    int               `json:"ppid"`
	Argv0   string            `json:"argv0"`
	Args    []string          `json:"args"`
	Stdin   []byte            `json:"stdin,omitempty"`
	Files   map[string][]byte `json:"files,omitempty"` // path as typed -> content read with the caller's privileges
	MAC     string            `json:"mac"`
}

// Response is the toolbox's answer. Error is generic by design.
type Response struct {
	OK       bool   `json:"ok"`
	ExitCode int    `json:"exit_code"`
	Stdout   []byte `json:"stdout,omitempty"`
	Stderr   []byte `json:"stderr,omitempty"`
	Error    string `json:"error,omitempty"`
	CallID   string `json:"call_id,omitempty"`
}

// ComputeMAC authenticates every field with length-prefixed encoding, so no
// choice of field contents can be confused with a different request.
func (r *Request) ComputeMAC(key []byte) string {
	m := hmac.New(sha256.New, key)
	put := func(b []byte) {
		var l [binary.MaxVarintLen64]byte
		m.Write(l[:binary.PutUvarint(l[:], uint64(len(b)))])
		m.Write(b)
	}
	putU := func(v uint64) {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], v)
		put(b[:])
	}
	put([]byte("evh-tool-v1"))
	putU(uint64(r.V))
	putU(r.Counter)
	put([]byte(r.Nonce))
	putU(uint64(r.PID))
	putU(uint64(r.PPID))
	put([]byte(r.Argv0))
	putU(uint64(len(r.Args)))
	for _, a := range r.Args {
		put([]byte(a))
	}
	h := sha256.Sum256(r.Stdin)
	put(h[:])
	names := sortedKeys(r.Files)
	putU(uint64(len(names)))
	for _, n := range names {
		put([]byte(n))
		fh := sha256.Sum256(r.Files[n])
		put(fh[:])
	}
	return hex.EncodeToString(m.Sum(nil))
}

func (r *Request) Sign(key []byte) { r.MAC = r.ComputeMAC(key) }

// Verify is constant-time.
func (r *Request) Verify(key []byte) bool {
	want, err := hex.DecodeString(r.ComputeMAC(key))
	got, err2 := hex.DecodeString(r.MAC)
	return err == nil && err2 == nil && hmac.Equal(want, got)
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ { // tiny maps: insertion sort avoids an import
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// WriteFrame writes a 4-byte big-endian length followed by JSON.
func WriteFrame(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > MaxFrame {
		return fmt.Errorf("frame too large (%d bytes)", len(b))
	}
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(b)))
	if _, err := w.Write(l[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// ReadFrame reads one frame, refusing to allocate more than max bytes: the
// length comes from an untrusted peer.
func ReadFrame(r io.Reader, v any, max int) error {
	var l [4]byte
	if _, err := io.ReadFull(r, l[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(l[:])
	if int64(n) > int64(max) {
		return fmt.Errorf("frame of %d bytes exceeds the %d byte limit", n, max)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	return json.Unmarshal(buf, v)
}
