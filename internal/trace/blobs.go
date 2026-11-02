package trace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// Ref points at a blob by content hash: "sha256:<hex>".
type Ref string

var reRef = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Valid reports whether r is a well-formed reference (the empty ref is valid and means "none").
func (r Ref) Valid() bool { return r == "" || reRef.MatchString(string(r)) }

// Store keeps large bodies (raw proxy transcript, exec log) beside the trace,
// addressed by content hash. Identical content is stored once.
type Store struct{ Dir string }

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{Dir: dir}, nil
}

func (s *Store) Put(b []byte) (Ref, error) {
	h := sha256.Sum256(b)
	sum := hex.EncodeToString(h[:])
	p := filepath.Join(s.Dir, sum)
	if _, err := os.Stat(p); err == nil {
		return Ref("sha256:" + sum), nil
	}
	tmp, err := os.CreateTemp(s.Dir, ".put-*")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return Ref("sha256:" + sum), nil
}

// Get reads a blob and verifies its content still matches its address.
func (s *Store) Get(r Ref) ([]byte, error) {
	if !reRef.MatchString(string(r)) {
		return nil, fmt.Errorf("bad blob ref %q", r)
	}
	b, err := os.ReadFile(filepath.Join(s.Dir, string(r)[len("sha256:"):]))
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(b)
	if "sha256:"+hex.EncodeToString(h[:]) != string(r) {
		return nil, fmt.Errorf("blob %s was modified after it was written", r)
	}
	return b, nil
}
