package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewRunDir(t *testing.T) {
	base := t.TempDir()
	dir, err := NewRunDir(base, "t042-1")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dir) != base {
		t.Fatalf("run dir escaped base: %s", dir)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatal("run dir not created")
	}
	if _, err := NewRunDir(base, "t042-1"); err == nil {
		t.Fatal("must refuse to reuse an existing run dir")
	}
	for _, bad := range []string{"", ".", "..", "../x", "a/b", "a b", "x\n"} {
		if _, err := NewRunDir(base, bad); err == nil {
			t.Errorf("trial id %q should be rejected", bad)
		}
	}
}
