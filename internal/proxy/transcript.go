package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Exchange is one request/response pair seen by the proxy. It is Tier A
// evidence: the guest cannot alter it after the fact.
type Exchange struct {
	Index             int          `json:"index"`
	Start             time.Time    `json:"start"`
	End               time.Time    `json:"end"`
	Method            string       `json:"method"`
	Path              string       `json:"path"`
	Model             string       `json:"model,omitempty"`
	Status            int          `json:"status"`
	ReqSHA256         string       `json:"req_sha256,omitempty"`
	RespSHA256        string       `json:"resp_sha256,omitempty"`
	ReqBytes          int          `json:"req_bytes"`
	RespBytes         int          `json:"resp_bytes"`
	RespTruncated     bool         `json:"resp_truncated,omitempty"`
	ToolCalls         []ToolCall   `json:"tool_calls,omitempty"`   // calls the model made in this response
	ToolResults       []ToolResult `json:"tool_results,omitempty"` // results the agent reported in this request
	Echoes            []Echo       `json:"echoes,omitempty"`       // calls repeated in this request\'s history, with the agent\'s ids
	Thinking          []string     `json:"thinking,omitempty"`
	ThinkingRequested bool         `json:"thinking_requested,omitempty"`
	Text              string       `json:"text,omitempty"`
	Usage             Usage        `json:"usage"`
	Rejected          string       `json:"rejected,omitempty"` // why the request was not forwarded
	Infra             string       `json:"infra,omitempty"`    // rate_limited | upstream_error | upstream_unreachable
	Retries           int          `json:"retries,omitempty"`
}

// Recorder collects exchanges, links tool results to the calls that produced
// them, and optionally stores raw bodies hash-addressed under dir/blobs.
type Recorder struct {
	mu      sync.Mutex
	dir     string
	ex      []Exchange
	pending []ToolCall
	next    int
}

// NewRecorder returns a recorder; dir may be empty to keep everything in memory.
func NewRecorder(dir string) (*Recorder, error) {
	if dir != "" {
		if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o755); err != nil {
			return nil, err
		}
	}
	return &Recorder{dir: dir}, nil
}

// InFlight counts exchanges that began but have not finished: model requests the
// proxy is still waiting on.
func (r *Recorder) InFlight() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.next - len(r.ex)
}

// Begin reserves the next exchange index.
func (r *Recorder) Begin() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := r.next
	r.next++
	return i
}

// Finish stores the exchange. Tool results in the request are matched, in
// order, to calls the model made earlier and not yet answered. A result with
// no pending call is marked Unmatched: either a bug or a forged result.
func (r *Recorder) Finish(e Exchange, req, resp []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(req) > 0 {
		e.ReqSHA256 = r.blob(req)
	}
	if len(resp) > 0 {
		e.RespSHA256 = r.blob(resp)
	}
	for _, ec := range e.Echoes {
		r.learnAlias(ec)
	}
	for i := range e.ToolResults {
		res := &e.ToolResults[i]
		res.AgentCallID = res.CallID
		if idx := r.match(*res); idx >= 0 {
			res.CallID = r.pending[idx].ID
			r.pending = append(r.pending[:idx], r.pending[idx+1:]...)
		} else {
			res.CallID, res.Unmatched = "", true
		}
	}
	r.pending = append(r.pending, e.ToolCalls...)
	r.ex = append(r.ex, e)
}

// learnAlias records the agent's own id for the first call with the same name
// and arguments that has none yet, in both the stored exchanges and the pending list.
func (r *Recorder) learnAlias(ec Echo) {
	for xi := range r.ex {
		for ci := range r.ex[xi].ToolCalls {
			if r.ex[xi].ToolCalls[ci].AgentID == ec.ID {
				return // already known
			}
		}
	}
	set := func(c *ToolCall) bool {
		if c.AgentID == "" && c.Name == ec.Name && c.ArgsHash == ec.ArgsHash && c.ID != ec.ID {
			c.AgentID = ec.ID
			return true
		}
		return false
	}
	for xi := range r.ex {
		for ci := range r.ex[xi].ToolCalls {
			if set(&r.ex[xi].ToolCalls[ci]) {
				for pi := range r.pending {
					if r.pending[pi].ID == r.ex[xi].ToolCalls[ci].ID {
						r.pending[pi].AgentID = ec.ID
					}
				}
				return
			}
		}
	}
}

// match finds the pending call a result answers: by the proxy's id or the agent's
// alias first; by name only when the result's id is one we have never seen.
func (r *Recorder) match(res ToolResult) int {
	for i, c := range r.pending {
		if res.CallID != "" && (c.ID == res.CallID || (c.AgentID != "" && c.AgentID == res.CallID)) {
			return i
		}
	}
	if res.CallID != "" && r.knownID(res.CallID) {
		return -1 // it names a call that was already answered: not a fresh result
	}
	for i, c := range r.pending {
		if c.Name == res.Name {
			return i
		}
	}
	return -1
}

func (r *Recorder) knownID(id string) bool {
	for _, e := range r.ex {
		for _, c := range e.ToolCalls {
			if c.ID == id || c.AgentID == id {
				return true
			}
		}
	}
	return false
}

// blob stores b under its sha256 (when a directory is configured) and returns the hash.
func (r *Recorder) blob(b []byte) string {
	h := sha256.Sum256(b)
	sum := hex.EncodeToString(h[:])
	if r.dir != "" {
		p := filepath.Join(r.dir, "blobs", sum)
		if _, err := os.Stat(p); err != nil {
			_ = os.WriteFile(p, b, 0o600)
		}
	}
	return sum
}

// Snapshot returns the exchanges in completion order.
func (r *Recorder) Snapshot() []Exchange {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Exchange, len(r.ex))
	copy(out, r.ex)
	return out
}

// Unanswered returns tool calls the model made that the agent never reported back.
func (r *Recorder) Unanswered() []ToolCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ToolCall(nil), r.pending...)
}

// Flush writes exchanges.jsonl into the recorder directory.
func (r *Recorder) Flush() error {
	if r.dir == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := os.Create(filepath.Join(r.dir, "exchanges.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range r.ex {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}

// Blob reads a stored body back.
func (r *Recorder) Blob(sum string) ([]byte, error) {
	return os.ReadFile(filepath.Join(r.dir, "blobs", sum))
}
