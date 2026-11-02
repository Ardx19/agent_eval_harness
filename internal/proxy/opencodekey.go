package proxy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// FetchOpencodeKey returns the API key opencode stores for a provider (for
// example "opencode-go"). The key is read on the host into memory, handed to the
// proxy, and never printed, logged or placed in the guest. Errors never include
// file content.
func FetchOpencodeKey(provider string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := os.Getenv("OPENCODE_AUTH_FILE")
	if path == "" {
		path = filepath.Join(home, ".local", "share", "opencode", "auth.json")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("cannot read opencode's credential store: " + filepath.Base(path))
	}
	var auth map[string]struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	}
	if json.Unmarshal(b, &auth) != nil {
		return "", errors.New("opencode's credential store is not valid JSON")
	}
	if e, ok := auth[provider]; ok && e.Type == "api" && e.Key != "" {
		return e.Key, nil
	}
	// opencode's own free models (provider "opencode") need no account: it sends a
	// public key when none is stored
	if provider == "opencode" {
		return "public", nil
	}
	return "", errors.New("opencode has no API key stored for provider " + provider + " (log in with `opencode auth login`)")
}
