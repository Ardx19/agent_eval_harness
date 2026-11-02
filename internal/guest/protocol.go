package guest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// Control frames travel from the guest supervisor (root) to the host over vsock
// port 5002. They carry the agent's own output (Tier B). Anything in the guest
// can open that port, so every frame is signed with a key only root can read and
// chained to the previous frame's signature: a forged, dropped, reordered or
// replayed frame breaks the chain.
const (
	ControlPort = 5002
	CtlKeyPath  = "/etc/evh/ctl.key"
	RunSpecPath = "/etc/evh/run.json"
)

// Frame streams.
const (
	StreamStdout = "stdout" // one line of the agent's stdout
	StreamStderr = "stderr"
	StreamEvent  = "event" // supervisor notes (stage names, errors)
	StreamExit   = "exit"  // the agent's exit status; always the last frame
)

type Frame struct {
	Seq    int     `json:"seq"`
	Stream string  `json:"stream"`
	Data   string  `json:"data,omitempty"`
	Trunc  bool    `json:"trunc,omitempty"` // the line exceeded the size cap
	Code   int     `json:"code,omitempty"`
	Signal string  `json:"signal,omitempty"`
	Reason string  `json:"reason,omitempty"` // "timeout" for a watchdog kill
	T      float64 `json:"t"`                // seconds since the supervisor started
	MAC    string  `json:"mac"`
}

// RunSpec tells the supervisor what to launch. It lives in a root-only file.
type RunSpec struct {
	Argv     []string `json:"argv"`
	Env      []string `json:"env"`
	Dir      string   `json:"dir"`
	UID      int      `json:"uid"`
	GID      int      `json:"gid"`
	WallS    int      `json:"wall_s"`
	IdleS    int      `json:"idle_s,omitempty"`   // kill the agent after this long with no output; 0 = never
	Listen   string   `json:"listen"`             // guest-fwd loopback address, e.g. 127.0.0.1:18080
	Hostname string   `json:"hostname,omitempty"` // set before the agent starts
}

// MACFor signs a frame, chained to prev (the previous frame's MAC, "" for the first).
func (f *Frame) MACFor(key []byte, prev string) string {
	m := hmac.New(sha256.New, key)
	put := func(b []byte) {
		var l [8]byte
		binary.BigEndian.PutUint64(l[:], uint64(len(b)))
		m.Write(l[:])
		m.Write(b)
	}
	put([]byte("evh-ctl-v1"))
	put([]byte(prev))
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(f.Seq))
	put(n[:])
	put([]byte(f.Stream))
	put([]byte(f.Data))
	var c [8]byte
	binary.BigEndian.PutUint64(c[:], uint64(int64(f.Code)))
	put(c[:])
	put([]byte(f.Signal))
	put([]byte(f.Reason))
	if f.Trunc {
		put([]byte{1})
	} else {
		put([]byte{0})
	}
	return hex.EncodeToString(m.Sum(nil))
}

func (f *Frame) Sign(key []byte, prev string) { f.MAC = f.MACFor(key, prev) }

func (f *Frame) Verify(key []byte, prev string) bool {
	want, err1 := hex.DecodeString(f.MACFor(key, prev))
	got, err2 := hex.DecodeString(f.MAC)
	return err1 == nil && err2 == nil && hmac.Equal(want, got)
}
