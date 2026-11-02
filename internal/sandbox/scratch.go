package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Identities are the guest uids used to attribute file ownership.
type Identities struct{ Agent, Tool int }

// DefaultIdentities matches images/layout.json.
var DefaultIdentities = Identities{Agent: 1000, Tool: 2000}

// sbinTool finds an e2fsprogs binary even when /usr/sbin is not in PATH.
func sbinTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, d := range []string{"/usr/sbin", "/sbin"} {
		p := filepath.Join(d, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return name
}

// FormatExt4 formats an existing (sparse) file as ext4 without a journal. No
// root is needed. The guest mounts it as the overlay upper/work area.
func FormatExt4(ctx context.Context, sys System, path, label string) error {
	out, err := sys.Run(ctx, sbinTool("mke2fs"), "-q", "-F", "-t", "ext4", "-O", "^has_journal", "-m", "0", "-L", label, path)
	if err != nil {
		return fmt.Errorf("mke2fs %s: %v: %s", path, err, out)
	}
	return nil
}

// workspaceRel is where task inputs appear inside the guest.
const workspaceRel = "home/dev/project"

// BuildWorkspace turns srcDir into a read-only ext4 layer that the guest
// stacks above the base image. Files appear under /home/dev/project, owned by
// the agent uid; parent directories match the base image so overlay merging
// does not change their metadata. It returns the layer's entries so the diff
// can tell creates from modifies.
func BuildWorkspace(ctx context.Context, sys System, srcDir, outImage string, id Identities) ([]Entry, error) {
	return BuildWorkspaceWith(ctx, sys, srcDir, outImage, id, nil)
}

// ExtraFile is a file or symlink placed in the workspace layer outside the task
// inputs: skill docs, tool entrypoints. RootOwned files (and their parent
// directory) belong to root, so the non-root agent can read and run them but
// cannot edit, replace or delete them.
type ExtraFile struct {
	Path      string      // absolute guest path, under /home/dev/ or /usr/share/agent-skills/
	Data      []byte      // file content (ignored for symlinks)
	Mode      os.FileMode // file permission bits; 0 = 0644
	Symlink   string      // when set, the entry is a symlink to this target
	RootOwned bool
	// Owner gives the file (and its parent directory) to a uid, e.g. the tool uid
	// for the per-trial key. ParentMode is that directory's mode ("0755" if empty).
	Owner      *int
	ParentMode string
}

var extraPrefixes = []string{"/home/dev/", "/usr/share/agent-skills/", "/opt/svc/", "/etc/evh/", "/etc/hostname", "/etc/hosts"}

func validateExtra(e ExtraFile) error {
	if e.Path != filepath.Clean(e.Path) || !strings.HasPrefix(e.Path, "/") {
		return fmt.Errorf("extra file path %q must be absolute and clean", e.Path)
	}
	for _, p := range extraPrefixes {
		if strings.HasPrefix(e.Path, p) {
			return nil
		}
	}
	return fmt.Errorf("extra file %q must be under %s", e.Path, strings.Join(extraPrefixes, " or "))
}

// BuildWorkspaceWith is BuildWorkspace plus extra files.
func BuildWorkspaceWith(ctx context.Context, sys System, srcDir, outImage string, id Identities, extras []ExtraFile) ([]Entry, error) {
	tmp, err := os.MkdirTemp("", "evh-ws-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	dst := filepath.Join(tmp, filepath.FromSlash(workspaceRel))
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return nil, err
	}
	total, err := copyTree(srcDir, dst)
	if err != nil {
		return nil, fmt.Errorf("copy workspace: %w", err)
	}
	uid, gid := id.Agent, id.Agent
	root := 0
	layout := &Layout{Overrides: []Override{
		{Path: "/home/dev", UID: &uid, GID: &gid, Mode: "0750", Recursive: true},
	}}
	for _, e := range extras {
		if err := validateExtra(e); err != nil {
			return nil, err
		}
		full := filepath.Join(tmp, e.Path)
		if _, err := os.Lstat(full); err == nil {
			return nil, fmt.Errorf("extra file %s collides with a task input", e.Path)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return nil, err
		}
		if e.Symlink != "" {
			err = os.Symlink(e.Symlink, full)
		} else {
			mode := e.Mode
			if mode == 0 {
				mode = 0o644
			}
			err = os.WriteFile(full, e.Data, mode)
			if err == nil {
				err = os.Chmod(full, mode) // WriteFile is subject to umask
			}
		}
		if err != nil {
			return nil, err
		}
		if e.Owner != nil {
			owner := *e.Owner
			pm := e.ParentMode
			if pm == "" {
				pm = "0755"
			}
			m := e.Mode
			if m == 0 {
				m = 0o644
			}
			layout.Overrides = append(layout.Overrides,
				Override{Path: filepath.Dir(e.Path), UID: &owner, GID: &owner, Mode: pm},
				Override{Path: e.Path, UID: &owner, GID: &owner, Mode: fmt.Sprintf("%04o", m)})
		}
		if e.RootOwned {
			ov := Override{Path: e.Path, UID: &root, GID: &root}
			if e.Symlink == "" {
				m := e.Mode
				if m == 0 {
					m = 0o644
				}
				ov.Mode = fmt.Sprintf("%04o", m)
			}
			dirOv := Override{Path: filepath.Dir(e.Path), UID: &root, GID: &root, Mode: "0755"}
			layout.Overrides = append(layout.Overrides, dirOv, ov)
		}
	}
	entries, err := Walk(tmp, layout)
	if err != nil {
		return nil, err
	}
	script, err := DebugfsScript(entries)
	if err != nil {
		return nil, err
	}
	sizeMiB := total*2/(1<<20) + 16
	f, err := os.Create(outImage)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(sizeMiB << 20); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	if out, err := sys.Run(ctx, sbinTool("mke2fs"), "-q", "-F", "-t", "ext4", "-O", "^has_journal", "-m", "0", "-L", "workspace", "-d", tmp, outImage); err != nil {
		return nil, fmt.Errorf("mke2fs workspace: %v: %s", err, out)
	}
	sf := filepath.Join(tmp, "own.debugfs")
	if err := os.WriteFile(sf, []byte(script), 0o600); err != nil {
		return nil, err
	}
	out, _ := sys.Run(ctx, sbinTool("debugfs"), "-w", "-f", sf, outImage)
	if strings.Contains(out, "File not found") {
		return nil, fmt.Errorf("debugfs ownership pass failed: %s", firstErrLine(out))
	}
	return entries, nil
}

func firstErrLine(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "not found") || strings.Contains(l, "rror") {
			return l
		}
	}
	return out
}

// copyTree copies files, directories and symlinks and returns the byte total.
func copyTree(src, dst string) (int64, error) {
	var total int64
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(l, target)
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
			if err != nil {
				return err
			}
			n, err := io.Copy(out, in)
			total += n
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			return err
		default:
			return fmt.Errorf("unsupported file type at %s", p)
		}
	})
	return total, err
}

// LoadEntries reads images/build/entries.jsonl as written by imagetool.
func LoadEntries(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	dec := json.NewDecoder(f)
	for dec.More() {
		var r struct {
			Path, Kind, Perm, SHA256, Link string
			UID, GID                       int
		}
		if err := dec.Decode(&r); err != nil {
			return nil, err
		}
		var perm uint32
		fmt.Sscanf(r.Perm, "%o", &perm)
		k := byte('o')
		if len(r.Kind) == 1 {
			k = r.Kind[0]
		}
		out = append(out, Entry{Path: r.Path, Kind: k, Perm: perm, UID: r.UID, GID: r.GID, SHA256: r.SHA256, Link: r.Link})
	}
	return out, nil
}

// ErrScratchFull means the guest ran out of writable space, so the diff may be
// incomplete and the trial must be reported as infra, not as an agent failure.
var ErrScratchFull = fmt.Errorf("scratch disk is full: the guest could not persist all of its writes")

var (
	reBlocks = regexp.MustCompile(`(?m)^Block count:\s+(\d+)`)
	reFree   = regexp.MustCompile(`(?m)^Free blocks:\s+(\d+)`)
)

// ScratchUsage reports total and free blocks of a scratch image.
func ScratchUsage(ctx context.Context, sys System, image string) (total, free int64, err error) {
	out, _ := sys.Run(ctx, sbinTool("debugfs"), "-R", "stats", image)
	t, f := reBlocks.FindStringSubmatch(out), reFree.FindStringSubmatch(out)
	if t == nil || f == nil {
		return 0, 0, fmt.Errorf("cannot read filesystem stats of %s", image)
	}
	total, _ = strconv.ParseInt(t[1], 10, 64)
	free, _ = strconv.ParseInt(f[1], 10, 64)
	return total, free, nil
}

// CheckScratch returns ErrScratchFull when less than 2% of the disk is free.
func CheckScratch(ctx context.Context, sys System, image string) error {
	total, free, err := ScratchUsage(ctx, sys, image)
	if err != nil {
		return err
	}
	if total > 0 && free*100 < total*2 {
		return fmt.Errorf("%w (%d of %d blocks free)", ErrScratchFull, free, total)
	}
	return nil
}
