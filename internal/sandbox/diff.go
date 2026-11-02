package sandbox

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Change is one filesystem change the agent made, read from the guest's
// overlay upper layer after the VM has stopped.
type Change struct {
	Path           string `json:"path"`
	Op             string `json:"op"`   // create | modify | delete | replace
	Kind           string `json:"kind"` // file | dir | symlink
	Perm           uint32 `json:"perm"`
	UID            int    `json:"uid"`
	GID            int    `json:"gid"`
	Size           int64  `json:"size"`
	SHA256         string `json:"sha256,omitempty"`
	Link           string `json:"link,omitempty"`
	ContentChanged bool   `json:"content_changed"`   // modify: false means copy-up with identical content (e.g. touch)
	Derived        bool   `json:"derived,omitempty"` // delete implied by an opaque (removed and recreated) directory
	WriterHint     string `json:"writer_hint"`       // from the owner uid; see note below
}

// WriterHint note: a created file is owned by whoever created it, so the hint is
// reliable for creates. A modified file keeps its original owner, so for modifies
// the hint reflects ownership, not the writer. Authoritative tool writes come
// from the signed tool records (U8/U14), not from this field.

// Lower maps absolute paths to the entries of every read-only layer, so the
// diff can tell a create from a modify.
type Lower map[string]Entry

// NewLower merges layers; later layers win.
func NewLower(layers ...[]Entry) Lower {
	l := Lower{}
	for _, layer := range layers {
		for _, e := range layer {
			l[e.Path] = e
		}
	}
	return l
}

var (
	reLs = regexp.MustCompile(`^/(\d+)/(\d+)/(\d+)/(\d+)/([^/]*)/(\d*)/$`)
	// S_IFMT type bits
	typeDir, typeReg, typeLnk, typeChr uint32 = 040000, 0100000, 0120000, 020000
)

type listing struct {
	mode     uint32
	uid, gid int
	name     string
}

// ExtractDiff reads the overlay upper layer ("/upper") out of a scratch image
// with debugfs, without root. rdump supplies file contents; a scripted ls pass
// supplies true owners and modes plus overlayfs whiteouts (char device 0:0),
// which rdump skips; ea_list reveals opaque directories.
func ExtractDiff(ctx context.Context, sys System, scratchImage string, lower Lower, id Identities) ([]Change, error) {
	return ExtractDiffKeep(ctx, sys, scratchImage, lower, id, "")
}

// ExtractDiffKeep is ExtractDiff, and when keepDir is set the dumped upper layer
// stays on disk at keepDir/upper so the verify stage can copy the agent's final
// files from it. keepDir must already exist; the caller removes it.
func ExtractDiffKeep(ctx context.Context, sys System, scratchImage string, lower Lower, id Identities, keepDir string) ([]Change, error) {
	tmp := keepDir
	if tmp == "" {
		var err error
		if tmp, err = os.MkdirTemp("", "evh-diff-"); err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmp)
	}
	dbg := sbinTool("debugfs")

	out, _ := sys.Run(ctx, dbg, "-R", fmt.Sprintf(`rdump /upper "%s"`, tmp), scratchImage)
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		// rdump cannot chown root-owned entries as a normal user; that is expected.
		if l == "" || strings.HasPrefix(l, "debugfs ") || strings.Contains(l, "while changing ownership") {
			continue
		}
		return nil, fmt.Errorf("rdump: %s", l)
	}
	dumped := filepath.Join(tmp, "upper")
	if st, err := os.Stat(dumped); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("scratch image has no /upper directory")
	}

	// the agent may have left read-only or mode-0 files and directories (a test that
	// checks how tools treat them); make the copy readable so it can be walked and hashed
	makeReadable(dumped)

	dirs := []string{"/"}
	err := filepath.WalkDir(dumped, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != dumped {
			rel, _ := filepath.Rel(dumped, p)
			dirs = append(dirs, "/"+filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(dirs)

	var script strings.Builder
	for _, d := range dirs {
		if unsafePath.MatchString(d) {
			return nil, fmt.Errorf("directory %q cannot be listed safely", d)
		}
		up := path.Join("/upper", d)
		fmt.Fprintf(&script, "ls -l -p \"%s\"\n", up)
		if d != "/" {
			fmt.Fprintf(&script, "ea_list \"%s\"\n", up)
		}
	}
	sf := filepath.Join(tmp, "list.debugfs")
	if err := os.WriteFile(sf, []byte(script.String()), 0o600); err != nil {
		return nil, err
	}
	out, err = sys.Run(ctx, dbg, "-f", sf, scratchImage)
	if err != nil {
		return nil, fmt.Errorf("debugfs listing: %v", err)
	}
	listings, opaque := parseListing(out)

	// every path present in the upper layer, for deriving deletes under opaque dirs
	upperPaths := map[string]bool{}
	for d, ents := range listings {
		for _, e := range ents {
			upperPaths[joinInner(d, e.name)] = true
		}
	}

	var changes []Change
	for _, d := range sortedKeys(listings) {
		for _, e := range listings[d] {
			p := joinInner(d, e.name)
			typ := e.mode & 0170000
			perm := e.mode & 07777
			lo, inLower := lower[p]
			c := Change{Path: p, Perm: perm, UID: e.uid, GID: e.gid, WriterHint: writerHint(e.uid, id)}
			switch {
			case typ == typeChr && perm == 0: // overlayfs whiteout
				c.Op, c.Kind = "delete", kindName(lo, inLower)
				changes = append(changes, c)
			case typ == typeDir:
				c.Kind = "dir"
				switch {
				case opaque[p]:
					c.Op = "replace"
					changes = append(changes, c)
					prefix := p + "/"
					for lp := range lower {
						if strings.HasPrefix(lp, prefix) && !upperPaths[lp] {
							le := lower[lp]
							changes = append(changes, Change{Path: lp, Op: "delete", Kind: kindName(le, true), Derived: true,
								UID: le.UID, GID: le.GID, Perm: le.Perm, WriterHint: writerHint(e.uid, id)})
						}
					}
				case !inLower:
					c.Op = "create"
					changes = append(changes, c)
				} // an existing, non-opaque directory is only a copy-up container
			case typ == typeReg:
				c.Kind = "file"
				cp := filepath.Join(dumped, filepath.FromSlash(strings.TrimPrefix(p, "/")))
				if st, err := os.Stat(cp); err == nil {
					c.Size = st.Size()
				}
				if c.SHA256, err = hashFile(cp); err != nil {
					return nil, fmt.Errorf("hash %s: %w", p, err)
				}
				c.Op = "create"
				if inLower {
					c.Op = "modify"
					c.ContentChanged = lo.SHA256 != c.SHA256
				} else {
					c.ContentChanged = true
				}
				changes = append(changes, c)
			case typ == typeLnk:
				c.Kind = "symlink"
				cp := filepath.Join(dumped, filepath.FromSlash(strings.TrimPrefix(p, "/")))
				if c.Link, err = os.Readlink(cp); err != nil {
					return nil, fmt.Errorf("readlink %s: %w", p, err)
				}
				c.Op = "create"
				if inLower {
					c.Op = "modify"
					c.ContentChanged = lo.Link != c.Link
				} else {
					c.ContentChanged = true
				}
				changes = append(changes, c)
			default:
				// fifo, socket, block device: not part of any task's work, but still recorded
				c.Kind, c.Op = "other", "create"
				changes = append(changes, c)
			}
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

// parseListing reads `debugfs -f` output. Each command is echoed as
// "debugfs:  <command>", which tells us which directory the lines belong to.
func parseListing(out string) (map[string][]listing, map[string]bool) {
	listings := map[string][]listing{}
	opaque := map[string]bool{}
	var mode, dir string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "debugfs:") {
			cmd := strings.TrimSpace(strings.TrimPrefix(line, "debugfs:"))
			switch {
			case strings.HasPrefix(cmd, "ls -l -p "):
				mode, dir = "ls", innerDir(strings.TrimPrefix(cmd, "ls -l -p "))
				listings[dir] = listings[dir] // present even when empty
			case strings.HasPrefix(cmd, "ea_list "):
				mode, dir = "ea", innerDir(strings.TrimPrefix(cmd, "ea_list "))
			default:
				mode = ""
			}
			continue
		}
		switch mode {
		case "ls":
			m := reLs.FindStringSubmatch(line)
			if m == nil || m[5] == "." || m[5] == ".." || m[5] == "" {
				continue
			}
			mv, _ := strconv.ParseUint(m[2], 8, 32)
			uid, _ := strconv.Atoi(m[3])
			gid, _ := strconv.Atoi(m[4])
			listings[dir] = append(listings[dir], listing{mode: uint32(mv), uid: uid, gid: gid, name: m[5]})
		case "ea":
			if strings.Contains(line, "trusted.overlay.opaque") && strings.Contains(line, `"y"`) {
				opaque[dir] = true
			}
		}
	}
	return listings, opaque
}

// innerDir converts a quoted "/upper/x" argument into the guest-relative "/x".
func innerDir(arg string) string {
	arg = strings.Trim(strings.TrimSpace(arg), `"`)
	arg = strings.TrimPrefix(arg, "/upper")
	if arg == "" {
		return "/"
	}
	return arg
}

func joinInner(dir, name string) string {
	if dir == "/" {
		return "/" + name
	}
	return dir + "/" + name
}

func kindName(e Entry, ok bool) string {
	if !ok {
		return "file"
	}
	switch e.Kind {
	case 'd':
		return "dir"
	case 'l':
		return "symlink"
	}
	return "file"
}

func writerHint(uid int, id Identities) string {
	switch uid {
	case id.Agent:
		return "agent"
	case id.Tool:
		return "tool"
	case 0:
		return "root"
	}
	return "uid:" + strconv.Itoa(uid)
}

func sortedKeys(m map[string][]listing) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// makeReadable gives the owner access to everything under dir, descending only
// after a directory is opened up. Symlinks are left alone.
func makeReadable(dir string) {
	_ = os.Chmod(dir, 0o700)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		switch {
		case e.Type()&os.ModeSymlink != 0:
		case e.IsDir():
			makeReadable(p)
		default:
			if info, err := os.Lstat(p); err == nil {
				_ = os.Chmod(p, info.Mode().Perm()|0o600)
			}
		}
	}
}
