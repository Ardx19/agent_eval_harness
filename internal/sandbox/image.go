package sandbox

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Layout declares the ownership and mode the built guest image must have.
// Everything not listed is root:root with the mode it has in the staging tree.
type Layout struct {
	Remove     []string    `json:"remove"`
	Overrides  []Override  `json:"overrides"`
	Invariants []Invariant `json:"invariants"`
}

// Override sets owner and/or permission bits for a path. uid/gid apply to the
// whole subtree when Recursive is set; Mode applies to the named path only.
type Override struct {
	Path      string `json:"path"`
	UID       *int   `json:"uid,omitempty"`
	GID       *int   `json:"gid,omitempty"`
	Mode      string `json:"mode,omitempty"` // octal permission bits, e.g. "04711"
	Recursive bool   `json:"recursive,omitempty"`
}

// Invariant is asserted against the finished image with debugfs.
type Invariant struct {
	Path   string `json:"path"`
	UID    int    `json:"uid"`
	GID    int    `json:"gid"`
	Mode   string `json:"mode"` // octal permission bits
	Absent bool   `json:"absent,omitempty"`
}

// Entry is one filesystem object with its effective (intended) metadata.
type Entry struct {
	Path           string
	Kind           byte // f d l o
	Perm           uint32
	UID, GID       int
	SHA256         string
	Link           string
	ModeOverridden bool
}

// LoadLayout reads and validates a layout file.
func LoadLayout(path string) (*Layout, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var l Layout
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return nil, nil, fmt.Errorf("parse layout: %w", err)
	}
	for _, o := range l.Overrides {
		if !strings.HasPrefix(o.Path, "/") {
			return nil, nil, fmt.Errorf("override path %q must be absolute", o.Path)
		}
		if o.Mode != "" {
			if _, err := strconv.ParseUint(o.Mode, 8, 32); err != nil {
				return nil, nil, fmt.Errorf("override %s: bad mode %q", o.Path, o.Mode)
			}
		}
	}
	return &l, raw, nil
}

// Prepare deletes the layout's Remove paths from the staging tree (missing is fine).
func Prepare(root string, l *Layout) ([]string, error) {
	var removed []string
	for _, r := range l.Remove {
		clean := filepath.Clean(r)
		if !strings.HasPrefix(clean, "/") || clean == "/" {
			return nil, fmt.Errorf("refusing to remove %q", r)
		}
		target := filepath.Join(root, clean)
		if _, err := os.Lstat(target); err != nil {
			continue
		}
		if err := os.RemoveAll(target); err != nil {
			return nil, err
		}
		removed = append(removed, clean)
	}
	return removed, nil
}

// Walk lists the tree under root in path order with effective ownership applied.
func Walk(root string, l *Layout) ([]Entry, error) {
	var entries []Entry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		ip := "/" + filepath.ToSlash(rel)
		if rel == "." {
			ip = "/"
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		e := Entry{Path: ip, Perm: permBits(info.Mode())}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			e.Kind = 'l'
			if e.Link, err = os.Readlink(p); err != nil {
				return err
			}
		case info.IsDir():
			e.Kind = 'd'
		case info.Mode().IsRegular():
			e.Kind = 'f'
			if e.SHA256, err = hashFile(p); err != nil {
				return err
			}
		default:
			e.Kind = 'o'
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	index := map[string]int{}
	for i, e := range entries {
		index[e.Path] = i
	}
	for _, o := range l.Overrides {
		start, ok := index[filepath.Clean(o.Path)]
		if !ok {
			return nil, fmt.Errorf("override path %s not found in tree", o.Path)
		}
		for i := start; i < len(entries); i++ {
			e := &entries[i]
			inside := e.Path == o.Path || (o.Recursive && strings.HasPrefix(e.Path, strings.TrimSuffix(o.Path, "/")+"/"))
			if !inside {
				continue
			}
			if o.UID != nil {
				e.UID = *o.UID
			}
			if o.GID != nil {
				e.GID = *o.GID
			}
			if o.Mode != "" && e.Path == o.Path {
				m, _ := strconv.ParseUint(o.Mode, 8, 32)
				e.Perm, e.ModeOverridden = uint32(m), true
			}
		}
	}
	return entries, nil
}

// TreeHash is a deterministic hash of the intended image content: path, kind,
// permission bits, owner, content hash and link target. Timestamps are excluded.
func TreeHash(entries []Entry) string {
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%c\x00%o\x00%d\x00%d\x00%s\x00%s\n", e.Path, e.Kind, e.Perm, e.UID, e.GID, e.SHA256, e.Link)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// debugfs passes a single backslash through literally (verified against
// e2fsprogs 1.46.5), so only quotes and line breaks cannot be expressed.
var unsafePath = regexp.MustCompile(`["\n\r]`)

// DebugfsScript emits sif commands that set owner (and overridden modes) on a
// freshly built image whose files are all owned by the builder.
func DebugfsScript(entries []Entry) (string, error) {
	var b strings.Builder
	for _, e := range entries {
		if unsafePath.MatchString(e.Path) {
			return "", fmt.Errorf("path %q cannot be expressed in a debugfs script", e.Path)
		}
		fmt.Fprintf(&b, "sif \"%s\" uid %d\n", e.Path, e.UID)
		fmt.Fprintf(&b, "sif \"%s\" gid %d\n", e.Path, e.GID)
		if e.ModeOverridden {
			fmt.Fprintf(&b, "sif \"%s\" mode 0%o\n", e.Path, typeBits(e.Kind)|e.Perm)
		}
	}
	return b.String(), nil
}

var (
	reMode = regexp.MustCompile(`Mode:\s+([0-7]+)`)
	reOwn  = regexp.MustCompile(`User:\s+(\d+)\s+Group:\s+(\d+)`)
)

// ParseStat extracts permission bits and owner from `debugfs -R "stat"` output.
func ParseStat(out string) (perm uint32, uid, gid int, err error) {
	m := reMode.FindStringSubmatch(out)
	o := reOwn.FindStringSubmatch(out)
	if m == nil || o == nil {
		return 0, 0, 0, fmt.Errorf("unrecognized debugfs stat output")
	}
	pv, _ := strconv.ParseUint(m[1], 8, 32)
	uid, _ = strconv.Atoi(o[1])
	gid, _ = strconv.Atoi(o[2])
	return uint32(pv) & 07777, uid, gid, nil
}

// VerifyImage checks every invariant against the built image and returns the failures.
func VerifyImage(ctx context.Context, sys System, image string, invs []Invariant) []string {
	var bad []string
	for _, inv := range invs {
		out, err := sys.Run(ctx, "debugfs", "-R", fmt.Sprintf("stat \"%s\"", inv.Path), image)
		missing := err != nil || strings.Contains(out, "File not found")
		if inv.Absent {
			if !missing {
				bad = append(bad, fmt.Sprintf("%s should be absent", inv.Path))
			}
			continue
		}
		if missing {
			bad = append(bad, fmt.Sprintf("%s missing from image", inv.Path))
			continue
		}
		perm, uid, gid, perr := ParseStat(out)
		if perr != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", inv.Path, perr))
			continue
		}
		want, _ := strconv.ParseUint(inv.Mode, 8, 32)
		if uid != inv.UID || gid != inv.GID || perm != uint32(want) {
			bad = append(bad, fmt.Sprintf("%s: got %d:%d %04o, want %d:%d %04o", inv.Path, uid, gid, perm, inv.UID, inv.GID, want))
		}
	}
	return bad
}

// Sha256File hashes a file by streaming it.
func Sha256File(path string) (string, error) { return hashFile(path) }

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, bufio.NewReaderSize(f, 1<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func permBits(m os.FileMode) uint32 {
	p := uint32(m.Perm())
	if m&os.ModeSetuid != 0 {
		p |= 04000
	}
	if m&os.ModeSetgid != 0 {
		p |= 02000
	}
	if m&os.ModeSticky != 0 {
		p |= 01000
	}
	return p
}

func typeBits(kind byte) uint32 {
	switch kind {
	case 'd':
		return 040000
	case 'l':
		return 0120000
	default:
		return 0100000
	}
}
