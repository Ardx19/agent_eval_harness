package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchOpencodeKey(t *testing.T) {
	f := filepath.Join(t.TempDir(), "auth.json")
	os.WriteFile(f, []byte(`{"opencode-go":{"type":"api","key":"sk-secret"},"openai":{"type":"oauth","access":"x"}}`), 0o600)
	t.Setenv("OPENCODE_AUTH_FILE", f)
	if k, err := FetchOpencodeKey("opencode-go"); err != nil || k != "sk-secret" {
		t.Fatalf("%q %v", k, err)
	}
	_, err := FetchOpencodeKey("openai")
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "access") {
		t.Fatalf("an oauth entry is not an API key and the error must not echo content: %v", err)
	}
	if _, err := FetchOpencodeKey("missing"); err == nil {
		t.Fatal("expected an error")
	}
	t.Setenv("OPENCODE_AUTH_FILE", filepath.Join(t.TempDir(), "nope.json"))
	if _, err := FetchOpencodeKey("opencode-go"); err == nil || strings.Contains(err.Error(), t.TempDir()) {
		t.Fatalf("%v", err)
	}
}

func TestFetchOpencodeKeyFreeModelsNeedNoAccount(t *testing.T) {
	f := filepath.Join(t.TempDir(), "auth.json")
	os.WriteFile(f, []byte(`{"opencode-go":{"type":"api","key":"sk-secret"}}`), 0o600)
	t.Setenv("OPENCODE_AUTH_FILE", f)
	if k, err := FetchOpencodeKey("opencode"); err != nil || k != "public" {
		t.Fatalf("%q %v", k, err)
	}
}
